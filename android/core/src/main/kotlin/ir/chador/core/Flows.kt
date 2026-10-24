package ir.chador.core

import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import kotlinx.serialization.Serializable
import kotlinx.serialization.builtins.ListSerializer

// ---------------------------------------------------------------- scanning list

enum class ItemState { Pending, Done, Failed, Queued }
enum class ScanResult { Added, Duplicate, Invalid, Full }

data class ScanItem(
    val code: String,
    val state: ItemState = ItemState.Pending,
    val message: String? = null,
    val status: TentStatus = TentStatus.READY,
)

/** Adds a scanned/typed code to a list: normalises it, ignores repeats, enforces a size limit. */
internal fun List<ScanItem>.withScan(raw: String, max: Int, status: TentStatus = TentStatus.READY): Pair<List<ScanItem>, ScanResult> {
    val code = TentCode.normalize(raw) ?: return this to ScanResult.Invalid
    if (any { it.code == code }) return this to ScanResult.Duplicate
    if (size >= max) return this to ScanResult.Full
    return this + ScanItem(code, status = status) to ScanResult.Added
}

private fun reasonText(reason: String): String =
    if (reason == "not_found") "این کد ثبت نشده است" else "وضعیت فعلی: ${Labels.status(reason)}"

@Serializable
private data class Unavailable(val code: String, val reason: String)

private fun parseUnavailable(e: ApiError): Map<String, String> = try {
    e.details?.let { d -> ChadorJson.decodeFromJsonElement(ListSerializer(Unavailable.serializer()), d).associate { it.code to reasonText(it.reason) } } ?: emptyMap()
} catch (ex: Exception) {
    emptyMap()
}

// ---------------------------------------------------------------- hand out

enum class HandoutStage { Identity, Tents, Done }

data class HandoutDone(val codes: List<String>, val socksAmount: Long, val queued: Boolean)

data class HandoutState(
    val stage: HandoutStage = HandoutStage.Identity,
    val busy: Boolean = false,
    val error: String? = null,
    val pilgrim: PilgrimView? = null,
    val tents: List<ScanItem> = emptyList(),
    val socks: Int = 0,
    val done: HandoutDone? = null,
)

/** Identity -> scan tents -> confirm. */
class HandoutPresenter(private val s: Services, private val scope: CoroutineScope) {
    private val _state = MutableStateFlow(HandoutState())
    val state: StateFlow<HandoutState> = _state.asStateFlow()

    // the same op id is reused while the content is identical, so a double tap or retry cannot hand out twice
    private var pendingOp: Pair<String, String>? = null

    fun submitIdentity(nationalId: String, birthDate: String, mobile: String) {
        if (_state.value.busy) return
        val nid = Validators.normalizeDigits(nationalId)
        val birth = Validators.birthDate(birthDate)
        val mob = Validators.mobile(mobile)
        val err = when {
            !Validators.validNationalId(nid) -> "کد ملی معتبر نیست."
            birth == null -> "تاریخ تولد را مثل ۱۳۷۰/۰۵/۱۲ وارد کنید."
            mob == null -> "شماره موبایل باید مثل ۰۹۱۲۳۴۵۶۷۸۹ باشد."
            else -> null
        }
        if (err != null) {
            _state.update { it.copy(error = err) }
            return
        }
        _state.update { it.copy(busy = true, error = null) }
        scope.launch {
            when (val r = s.api.registerPilgrim(PilgrimInput(nid, birth!!, mob!!))) {
                is ApiResult.Failure -> _state.update {
                    it.copy(busy = false, error = if (r.error.kind == ApiError.Kind.NETWORK) "برای ثبت زائر اینترنت لازم است. ارتباط را بررسی کنید." else r.error.userMessage())
                }
                is ApiResult.Ok -> {
                    val p = r.value
                    if (!p.verified) {
                        _state.update { it.copy(busy = false, pilgrim = null, error = "هویت زائر هنوز تأیید نشده است (سرویس استعلام در دسترس نیست). کمی بعد دوباره تلاش کنید.") }
                    } else {
                        _state.update { it.copy(busy = false, pilgrim = p, stage = HandoutStage.Tents, error = null) }
                    }
                }
            }
        }
    }

    fun scan(raw: String): ScanResult {
        var result = ScanResult.Added
        _state.update {
            val (list, res) = it.tents.withScan(raw, MAX_TENTS)
            result = res
            if (res == ScanResult.Added) it.copy(tents = list, error = null) else it
        }
        return result
    }

    fun remove(code: String) = _state.update { it.copy(tents = it.tents.filterNot { t -> t.code == code }) }
    fun setSocks(n: Int) = _state.update { it.copy(socks = n.coerceIn(0, 50)) }

    fun confirm() {
        val st = _state.value
        val pilgrim = st.pilgrim ?: return
        if (st.busy || st.tents.isEmpty()) return
        val codes = st.tents.map { it.code }.sorted()
        val fingerprint = "${pilgrim.id}|${codes.joinToString(",")}|${st.socks}"
        val opId = pendingOp?.takeIf { it.second == fingerprint }?.first ?: s.ops.newOpId()
        pendingOp = opId to fingerprint
        _state.update { it.copy(busy = true, error = null) }
        scope.launch {
            when (val o = s.ops.assign(pilgrim.id, codes, st.socks, opId)) {
                is Outcome.Done -> _state.update {
                    it.copy(busy = false, stage = HandoutStage.Done, done = HandoutDone(codes, o.value.socksAmount, queued = false))
                }
                is Outcome.Queued -> _state.update {
                    it.copy(busy = false, stage = HandoutStage.Done, done = HandoutDone(codes, 0, queued = true))
                }
                is Outcome.Rejected -> {
                    pendingOp = null
                    val bad = parseUnavailable(o.error)
                    _state.update {
                        it.copy(
                            busy = false,
                            tents = it.tents.map { t -> bad[t.code]?.let { m -> t.copy(state = ItemState.Failed, message = m) } ?: t },
                            error = if (bad.isNotEmpty()) "برخی چادرها قابل تحویل نیستند؛ آن‌ها را حذف یا جایگزین کنید." else o.error.userMessage(),
                        )
                    }
                }
            }
        }
    }

    fun reset() {
        pendingOp = null
        _state.value = HandoutState()
    }

    companion object { const val MAX_TENTS = 20 }
}

// ---------------------------------------------------------------- return

data class ReturnSummary(val returned: Int, val completedPilgrims: Int, val failed: Int, val queued: Boolean)

data class ReturnState(
    val items: List<ScanItem> = emptyList(),
    val busy: Boolean = false,
    val error: String? = null,
    val summary: ReturnSummary? = null,
)

class ReturnPresenter(private val s: Services, private val scope: CoroutineScope) {
    private val _state = MutableStateFlow(ReturnState())
    val state: StateFlow<ReturnState> = _state.asStateFlow()
    private var pendingOp: Pair<String, String>? = null

    fun scan(raw: String): ScanResult {
        var result = ScanResult.Added
        _state.update {
            val (list, res) = it.items.withScan(raw, MAX_ITEMS, TentStatus.READY)
            result = res
            if (res == ScanResult.Added) it.copy(items = list, summary = null, error = null) else it
        }
        return result
    }

    /** What state the returned tent is in: ready (clean), dirty or repair. */
    fun setStatus(code: String, status: TentStatus) = _state.update {
        it.copy(items = it.items.map { i -> if (i.code == code && i.state != ItemState.Done) i.copy(status = status, state = ItemState.Pending, message = null) else i })
    }

    fun remove(code: String) = _state.update { it.copy(items = it.items.filterNot { i -> i.code == code }) }

    fun submit() {
        val st = _state.value
        val todo = st.items.filter { it.state == ItemState.Pending || it.state == ItemState.Failed }
        if (st.busy || todo.isEmpty()) return
        val reqItems = todo.map { ReturnItemRequest(it.code, it.status.wire) }
        val fingerprint = reqItems.sortedBy { it.code }.joinToString("|") { it.code + ":" + it.status }
        val opId = pendingOp?.takeIf { it.second == fingerprint }?.first ?: s.ops.newOpId()
        pendingOp = opId to fingerprint
        _state.update { it.copy(busy = true, error = null) }
        scope.launch {
            when (val o = s.ops.returns(reqItems, opId)) {
                is Outcome.Done -> {
                    pendingOp = null
                    val byCode = o.value.results.associateBy { it.code }
                    _state.update {
                        it.copy(
                            busy = false,
                            items = it.items.map { i ->
                                val r = byCode[i.code] ?: return@map i
                                when {
                                    r.ok -> i.copy(state = ItemState.Done, message = null)
                                    r.error == "not_assigned" -> i.copy(state = ItemState.Failed, message = "این چادر به کسی تحویل نشده بود (${reasonText(r.detail ?: "")}).")
                                    else -> i.copy(state = ItemState.Failed, message = reasonText("not_found"))
                                }
                            },
                            summary = ReturnSummary(o.value.returned, o.value.completedPilgrims, o.value.results.count { r -> !r.ok }, queued = false),
                        )
                    }
                }
                is Outcome.Queued -> _state.update {
                    it.copy(busy = false, items = it.items.map { i -> if (i in todo) i.copy(state = ItemState.Queued) else i },
                        summary = ReturnSummary(todo.size, 0, 0, queued = true))
                }
                is Outcome.Rejected -> _state.update { it.copy(busy = false, error = o.error.userMessage()) }
            }
        }
    }

    fun reset() {
        pendingOp = null
        _state.value = ReturnState()
    }

    companion object { const val MAX_ITEMS = 100 }
}

// ---------------------------------------------------------------- one tent: info and actions

sealed interface TentAction {
    data object CancelAssignment : TentAction
    data class SetStatus(val to: TentStatus) : TentAction
    data object RequestRetire : TentAction
}

data class TentInfoState(
    val busy: Boolean = false,
    val error: String? = null,
    val message: String? = null,
    val tent: TentView? = null,
    val history: List<TentEvent> = emptyList(),
    val actions: List<TentAction> = emptyList(),
)

class TentInfoPresenter(private val s: Services, private val scope: CoroutineScope) {
    private val _state = MutableStateFlow(TentInfoState())
    val state: StateFlow<TentInfoState> = _state.asStateFlow()

    fun lookup(raw: String) {
        val code = TentCode.normalize(raw)
        if (code == null) {
            _state.update { it.copy(error = "کد چادر معتبر نیست.", tent = null) }
            return
        }
        load(code)
    }

    private fun load(code: String, message: String? = null) {
        _state.update { it.copy(busy = true, error = null, message = null) }
        scope.launch {
            val tentR = s.api.tent(code)
            val histR = s.api.tentHistory(code)
            when (tentR) {
                is ApiResult.Failure -> _state.update { it.copy(busy = false, tent = null, history = emptyList(), actions = emptyList(), error = tentR.error.userMessage()) }
                is ApiResult.Ok -> _state.update {
                    // the message is set together with the fresh data, so a reader that waits for it sees the new state
                    it.copy(busy = false, tent = tentR.value, history = histR.valueOrNull()?.events ?: emptyList(), actions = actionsFor(tentR.value), message = message)
                }
            }
        }
    }

    private fun actionsFor(t: TentView): List<TentAction> {
        val role = s.session.role
        val out = mutableListOf<TentAction>()
        val st = t.statusEnum
        if (st == TentStatus.WITH_PILGRIM && role.canCancelOwn) out += TentAction.CancelAssignment
        Transitions.targets(role, st).forEach { out += TentAction.SetStatus(it) }
        if (st != TentStatus.RETIRED && role.canRequestRetire) out += TentAction.RequestRetire
        return out
    }

    fun perform(action: TentAction, reason: String = "other", note: String? = null) {
        val t = _state.value.tent ?: return
        if (_state.value.busy) return
        _state.update { it.copy(busy = true, error = null, message = null) }
        scope.launch {
            val failure: ApiError?
            val done: String
            when (action) {
                is TentAction.CancelAssignment -> {
                    when (val o = s.ops.cancel(t.code)) {
                        is Outcome.Done -> { failure = null; done = "تخصیص لغو شد؛ چادر دوباره آماده است." }
                        is Outcome.Queued -> { failure = null; done = "ذخیره شد و هنگام اتصال ارسال می‌شود." }
                        is Outcome.Rejected -> { failure = o.error; done = "" }
                    }
                }
                is TentAction.SetStatus -> {
                    when (val o = s.ops.setStatus(t.code, action.to, note)) {
                        is Outcome.Done -> { failure = null; done = "وضعیت چادر شد «${Labels.status(action.to)}»." }
                        is Outcome.Queued -> { failure = null; done = "ذخیره شد و هنگام اتصال ارسال می‌شود." }
                        is Outcome.Rejected -> { failure = o.error; done = "" }
                    }
                }
                is TentAction.RequestRetire -> {
                    when (val r = s.api.requestRetire(RetireRequest(t.code, reason, note))) {
                        is ApiResult.Ok -> { failure = null; done = "درخواست خروج از چرخه ثبت شد و منتظر تأیید دفتر است." }
                        is ApiResult.Failure -> { failure = r.error; done = "" }
                    }
                }
            }
            if (failure != null) _state.update { it.copy(busy = false, error = failure.userMessage()) } else load(t.code, done)
        }
    }

    fun clear() { _state.value = TentInfoState() }
}

// ---------------------------------------------------------------- batch status change (workshop, review)

data class StatusBatchState(
    val items: List<ScanItem> = emptyList(),
    val busy: Boolean = false,
    val error: String? = null,
    val done: Int = 0,
    val queued: Int = 0,
)

/** Scan several tents and move them all to [target] (e.g. workshop: dirty -> in_wash). */
class StatusBatchPresenter(private val s: Services, private val scope: CoroutineScope, val target: TentStatus) {
    private val _state = MutableStateFlow(StatusBatchState())
    val state: StateFlow<StatusBatchState> = _state.asStateFlow()

    fun scan(raw: String): ScanResult {
        var result = ScanResult.Added
        _state.update {
            val (list, res) = it.items.withScan(raw, 200, target)
            result = res
            if (res == ScanResult.Added) it.copy(items = list, error = null) else it
        }
        return result
    }

    fun remove(code: String) = _state.update { it.copy(items = it.items.filterNot { i -> i.code == code }) }

    fun submit() {
        val st = _state.value
        val todo = st.items.filter { it.state == ItemState.Pending || it.state == ItemState.Failed }
        if (st.busy || todo.isEmpty()) return
        _state.update { it.copy(busy = true, error = null) }
        scope.launch {
            for (item in todo) {
                val o = s.ops.setStatus(item.code, target)
                _state.update { cur ->
                    cur.copy(items = cur.items.map { i ->
                        if (i.code != item.code) i else when (o) {
                            is Outcome.Done -> i.copy(state = ItemState.Done, message = null)
                            is Outcome.Queued -> i.copy(state = ItemState.Queued, message = null)
                            is Outcome.Rejected -> i.copy(state = ItemState.Failed, message = when (o.error.code) {
                                "invalid_transition" -> "در وضعیت فعلی نمی‌تواند «${Labels.status(target)}» شود."
                                "not_found" -> "این کد ثبت نشده است."
                                "not_at_workshop" -> "ورود به کارگاه باید از داخل شیفت کارگاه ثبت شود."
                                else -> o.error.userMessage()
                            })
                        }
                    })
                }
            }
            _state.update { cur ->
                cur.copy(busy = false, done = cur.items.count { it.state == ItemState.Done }, queued = cur.items.count { it.state == ItemState.Queued })
            }
        }
    }

    fun reset() { _state.value = StatusBatchState() }
}

// ---------------------------------------------------------------- move between places

data class MoveState(
    val places: List<Place> = emptyList(),
    val toPlaceId: Int? = null,
    val items: List<ScanItem> = emptyList(),
    val busy: Boolean = false,
    val error: String? = null,
    val message: String? = null,
)

class MovePresenter(private val s: Services, private val scope: CoroutineScope) {
    private val _state = MutableStateFlow(MoveState())
    val state: StateFlow<MoveState> = _state.asStateFlow()

    fun load() {
        scope.launch {
            when (val r = s.api.places()) {
                is ApiResult.Ok -> _state.update { it.copy(places = r.value) }
                is ApiResult.Failure -> _state.update { it.copy(error = r.error.userMessage()) }
            }
        }
    }

    fun selectPlace(id: Int) = _state.update { it.copy(toPlaceId = id, message = null) }

    fun scan(raw: String): ScanResult {
        var result = ScanResult.Added
        _state.update {
            val (list, res) = it.items.withScan(raw, 200)
            result = res
            if (res == ScanResult.Added) it.copy(items = list, error = null, message = null) else it
        }
        return result
    }

    fun remove(code: String) = _state.update { it.copy(items = it.items.filterNot { i -> i.code == code }) }

    fun submit() {
        val st = _state.value
        val to = st.toPlaceId ?: run { _state.update { it.copy(error = "مقصد را انتخاب کنید.") }; return }
        val todo = st.items.filter { it.state != ItemState.Done }
        if (st.busy || todo.isEmpty()) return
        _state.update { it.copy(busy = true, error = null) }
        scope.launch {
            when (val o = s.ops.move(todo.map { it.code }, to)) {
                is Outcome.Done -> {
                    val moved = o.value.moved.toSet()
                    _state.update {
                        it.copy(busy = false, items = it.items.map { i ->
                            when {
                                i.code in moved -> i.copy(state = ItemState.Done, message = null)
                                i in todo -> i.copy(state = ItemState.Failed, message = "قابل انتقال نیست (نزد زائر یا خارج از چرخه).")
                                else -> i
                            }
                        }, message = "${Jalali.persianDigits(moved.size.toString())} چادر منتقل شد.")
                    }
                }
                is Outcome.Queued -> _state.update { it.copy(busy = false, items = it.items.map { i -> if (i in todo) i.copy(state = ItemState.Queued) else i }, message = "ذخیره شد و هنگام اتصال ارسال می‌شود.") }
                is Outcome.Rejected -> _state.update { it.copy(busy = false, error = o.error.userMessage()) }
            }
        }
    }

    fun reset() { _state.update { MoveState(places = it.places, toPlaceId = it.toPlaceId) } }
}
