package ir.chador.core

import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Job
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.intOrNull

// ---------------------------------------------------------------- dashboard (live)

data class DashboardState(
    val data: Dashboard? = null,
    val loading: Boolean = false,
    val error: String? = null,
    val live: Boolean = false,
    val updatedAt: Long? = null,
)

/** Shows counts and keeps them fresh: server-sent events trigger a reload, plus a slow poll as a safety net. */
class DashboardPresenter(private val s: Services, private val scope: CoroutineScope) {
    private val _state = MutableStateFlow(DashboardState())
    val state: StateFlow<DashboardState> = _state.asStateFlow()
    private var job: Job? = null
    private val trigger = Channel<Unit>(Channel.CONFLATED)

    fun refresh() { trigger.trySend(Unit) }

    private suspend fun load() {
        _state.update { it.copy(loading = true) }
        when (val r = s.api.dashboard()) {
            is ApiResult.Ok -> _state.update { it.copy(data = r.value, loading = false, error = null, updatedAt = s.now()) }
            is ApiResult.Failure -> _state.update { it.copy(loading = false, error = r.error.userMessage()) }
        }
    }

    fun start() {
        if (job?.isActive == true) return
        job = scope.launch {
            launch { for (t in trigger) { load(); delay(300) } }          // at most ~3 reloads per second
            launch { while (isActive) { delay(20_000); refresh() } }       // safety net
            refresh()
            var backoff = 2_000L
            while (isActive) {
                try {
                    _state.update { it.copy(live = true) }
                    s.api.events().collect { refresh(); backoff = 2_000L }
                } catch (e: Exception) {
                    // connection lost or token expired: fall through and reconnect
                }
                _state.update { it.copy(live = false) }
                delay(backoff)
                backoff = (backoff * 2).coerceAtMost(30_000L)
                load() // an authenticated call first: it also refreshes an expired token before the next stream attempt
            }
        }
    }

    fun stop() { job?.cancel(); job = null; _state.update { it.copy(live = false) } }
}

// ---------------------------------------------------------------- daily report

data class ReportState(
    val dayOffset: Int = 0,
    val date: String = "",
    val report: DailyReport? = null,
    val busy: Boolean = false,
    val error: String? = null,
    val export: ByteArray? = null,
    val exportName: String? = null,
)

class ReportPresenter(private val s: Services, private val scope: CoroutineScope) {
    private val _state = MutableStateFlow(ReportState(date = Time.tehranDate(s.now())))
    val state: StateFlow<ReportState> = _state.asStateFlow()

    private fun dateFor(offset: Int) = Time.tehranDate(s.now() + offset * 86_400_000L)

    fun load(offset: Int = _state.value.dayOffset) {
        val date = dateFor(offset.coerceAtMost(0))
        _state.update { it.copy(dayOffset = offset.coerceAtMost(0), date = date, busy = true, error = null) }
        scope.launch {
            when (val r = s.api.dailyReport(date)) {
                is ApiResult.Ok -> _state.update { it.copy(busy = false, report = r.value) }
                is ApiResult.Failure -> _state.update { it.copy(busy = false, report = null, error = r.error.userMessage()) }
            }
        }
    }

    fun previousDay() = load(_state.value.dayOffset - 1)
    fun nextDay() = load(_state.value.dayOffset + 1)

    fun export() {
        val date = _state.value.date
        _state.update { it.copy(busy = true, error = null) }
        scope.launch {
            when (val r = s.api.dailyReportXlsx(date)) {
                is ApiResult.Ok -> _state.update { it.copy(busy = false, export = r.value, exportName = "report-$date.xlsx") }
                is ApiResult.Failure -> _state.update { it.copy(busy = false, error = r.error.userMessage()) }
            }
        }
    }

    fun consumeExport() = _state.update { it.copy(export = null, exportName = null) }
}

// ---------------------------------------------------------------- socks

data class SocksState(
    val places: List<Place> = emptyList(),
    val placeId: Int? = null,
    val busy: Boolean = false,
    val message: String? = null,
    val error: String? = null,
)

class SocksPresenter(private val s: Services, private val scope: CoroutineScope) {
    private val _state = MutableStateFlow(SocksState())
    val state: StateFlow<SocksState> = _state.asStateFlow()

    fun load() {
        scope.launch {
            val r = s.api.places()
            r.valueOrNull()?.let { list -> _state.update { it.copy(places = list.filter { p -> p.kind == "gate" }) } }
        }
    }

    fun selectPlace(id: Int) = _state.update { it.copy(placeId = id) }

    /** Office: hand socks to a gate. A gate lead: return what is left at the end of the shift. */
    fun submit(qty: Int) {
        val role = s.session.role
        if (qty <= 0) { _state.update { it.copy(error = "تعداد را وارد کنید.") }; return }
        val st = _state.value
        val (kind, place) = if (role.canSupplySocks) "supply" to st.placeId else "return" to null
        if (kind == "supply" && place == null) { _state.update { it.copy(error = "گیت را انتخاب کنید.") }; return }
        _state.update { it.copy(busy = true, error = null, message = null) }
        scope.launch {
            when (val o = s.ops.socks(kind, qty, place)) {
                is Outcome.Done -> _state.update { it.copy(busy = false, message = "ثبت شد. موجودی گیت: ${Jalali.persianDigits(o.value.balance.toString())}") }
                is Outcome.Queued -> _state.update { it.copy(busy = false, message = "ذخیره شد و هنگام اتصال ارسال می‌شود.") }
                is Outcome.Rejected -> _state.update { it.copy(busy = false, error = o.error.userMessage()) }
            }
        }
    }
}

// ---------------------------------------------------------------- shift handover

data class HandoverUi(
    val colleagues: List<Colleague> = emptyList(),
    val handovers: List<HandoverView> = emptyList(),
    val busy: Boolean = false,
    val message: String? = null,
    val error: String? = null,
)

class HandoverPresenter(private val s: Services, private val scope: CoroutineScope) {
    private val _state = MutableStateFlow(HandoverUi())
    val state: StateFlow<HandoverUi> = _state.asStateFlow()

    fun load() {
        _state.update { it.copy(busy = true, error = null) }
        scope.launch {
            val c = s.api.colleagues()
            val h = s.api.handovers()
            _state.update {
                it.copy(
                    busy = false,
                    colleagues = c.valueOrNull()?.colleagues ?: it.colleagues,
                    handovers = h.valueOrNull()?.handovers ?: it.handovers,
                    error = (c.errorOrNull() ?: h.errorOrNull())?.userMessage(),
                )
            }
        }
    }

    fun create(toUser: String, notes: String) {
        _state.update { it.copy(busy = true, error = null, message = null) }
        scope.launch {
            when (val r = s.api.createHandover(HandoverRequest(toUser, notes.ifBlank { null }))) {
                is ApiResult.Ok -> { _state.update { it.copy(busy = false, message = "تحویل شیفت ثبت شد؛ منتظر تأیید گیرنده است.") }; load() }
                is ApiResult.Failure -> _state.update { it.copy(busy = false, error = r.error.userMessage()) }
            }
        }
    }

    fun resolve(id: String, accept: Boolean) {
        scope.launch {
            when (val r = s.api.resolveHandover(id, accept)) {
                is ApiResult.Ok -> { _state.update { it.copy(message = if (accept) "تحویل پذیرفته شد." else "اختلاف ثبت شد.") }; load() }
                is ApiResult.Failure -> _state.update { it.copy(error = r.error.userMessage()) }
            }
        }
    }
}

// ---------------------------------------------------------------- retire requests (office)

data class RetireUi(val requests: List<RetireRequestView> = emptyList(), val busy: Boolean = false, val error: String? = null)

class RetirePresenter(private val s: Services, private val scope: CoroutineScope) {
    private val _state = MutableStateFlow(RetireUi())
    val state: StateFlow<RetireUi> = _state.asStateFlow()

    fun load() {
        _state.update { it.copy(busy = true, error = null) }
        scope.launch {
            when (val r = s.api.retireRequests()) {
                is ApiResult.Ok -> _state.update { it.copy(busy = false, requests = r.value.requests) }
                is ApiResult.Failure -> _state.update { it.copy(busy = false, error = r.error.userMessage()) }
            }
        }
    }

    fun decide(id: String, approve: Boolean) {
        scope.launch {
            when (val r = s.api.decideRetire(id, approve)) {
                is ApiResult.Ok -> load()
                is ApiResult.Failure -> _state.update { it.copy(error = r.error.userMessage()) }
            }
        }
    }
}

// ---------------------------------------------------------------- admin

data class UsersUi(
    val users: List<UserView> = emptyList(),
    val busy: Boolean = false,
    val error: String? = null,
    /** Shown once after creating a user or resetting a password; the UI must clear it after display. */
    val tempPassword: Pair<String, String>? = null,
)

class UsersPresenter(private val s: Services, private val scope: CoroutineScope) {
    private val _state = MutableStateFlow(UsersUi())
    val state: StateFlow<UsersUi> = _state.asStateFlow()

    fun load() {
        _state.update { it.copy(busy = true, error = null) }
        scope.launch {
            when (val r = s.api.users()) {
                is ApiResult.Ok -> _state.update { it.copy(busy = false, users = r.value.users) }
                is ApiResult.Failure -> _state.update { it.copy(busy = false, error = r.error.userMessage()) }
            }
        }
    }

    fun create(username: String, fullName: String, role: Role) {
        _state.update { it.copy(busy = true, error = null) }
        scope.launch {
            when (val r = s.api.createUser(CreateUserRequest(username.trim().lowercase(), fullName.trim(), role.wire))) {
                is ApiResult.Ok -> { _state.update { it.copy(tempPassword = r.value.username to r.value.tempPassword) }; load() }
                is ApiResult.Failure -> _state.update { it.copy(busy = false, error = r.error.userMessage()) }
            }
        }
    }

    fun setActive(id: String, active: Boolean) {
        scope.launch {
            when (val r = s.api.updateUser(id, UpdateUserRequest(active = active))) {
                is ApiResult.Ok -> load()
                is ApiResult.Failure -> _state.update { it.copy(error = r.error.userMessage()) }
            }
        }
    }

    fun resetPassword(user: UserView) {
        scope.launch {
            when (val r = s.api.resetPassword(user.id)) {
                is ApiResult.Ok -> _state.update { it.copy(tempPassword = user.username to r.value.tempPassword) }
                is ApiResult.Failure -> _state.update { it.copy(error = r.error.userMessage()) }
            }
        }
    }

    fun consumeTempPassword() = _state.update { it.copy(tempPassword = null) }
}

data class SettingsUi(val socksPrice: Int? = null, val maxTents: Int? = null, val busy: Boolean = false, val message: String? = null, val error: String? = null)

class SettingsPresenter(private val s: Services, private val scope: CoroutineScope) {
    private val _state = MutableStateFlow(SettingsUi())
    val state: StateFlow<SettingsUi> = _state.asStateFlow()

    private fun JsonElement?.int(): Int? = (this as? JsonPrimitive)?.intOrNull

    fun load() {
        _state.update { it.copy(busy = true, error = null) }
        scope.launch {
            when (val r = s.api.settings()) {
                is ApiResult.Ok -> _state.update { it.copy(busy = false, socksPrice = r.value["socks_unit_price"].int(), maxTents = r.value["max_tents_per_request"].int()) }
                is ApiResult.Failure -> _state.update { it.copy(busy = false, error = r.error.userMessage()) }
            }
        }
    }

    fun saveSocksPrice(price: Int) {
        if (price < 0) { _state.update { it.copy(error = "قیمت نامعتبر است.") }; return }
        _state.update { it.copy(busy = true, error = null, message = null) }
        scope.launch {
            when (val r = s.api.putSetting("socks_unit_price", JsonPrimitive(price))) {
                is ApiResult.Ok -> _state.update { it.copy(busy = false, socksPrice = price, message = "ذخیره شد.") }
                is ApiResult.Failure -> _state.update { it.copy(busy = false, error = r.error.userMessage()) }
            }
        }
    }
}

data class AuditUi(val rows: List<AuditRow> = emptyList(), val busy: Boolean = false, val error: String? = null, val canLoadMore: Boolean = false)

class AuditPresenter(private val s: Services, private val scope: CoroutineScope) {
    private val _state = MutableStateFlow(AuditUi())
    val state: StateFlow<AuditUi> = _state.asStateFlow()

    fun load(more: Boolean = false) {
        val before = if (more) _state.value.rows.lastOrNull()?.id ?: 0 else 0
        _state.update { it.copy(busy = true, error = null) }
        scope.launch {
            when (val r = s.api.audit(before, 100)) {
                is ApiResult.Ok -> _state.update {
                    it.copy(busy = false, rows = if (more) it.rows + r.value.records else r.value.records, canLoadMore = r.value.records.size == 100)
                }
                is ApiResult.Failure -> _state.update { it.copy(busy = false, error = r.error.userMessage()) }
            }
        }
    }
}

// ---------------------------------------------------------------- offline queue status

data class QueueUi(val syncing: Boolean = false, val lastMessage: String? = null)

class QueuePresenter(private val s: Services, private val scope: CoroutineScope) {
    val pending: StateFlow<Int> = s.queue.pending
    val rejected: StateFlow<List<RejectedOp>> = s.queue.rejected
    private val _ui = MutableStateFlow(QueueUi())
    val ui: StateFlow<QueueUi> = _ui.asStateFlow()

    fun syncNow() {
        val uid = s.session.userId ?: return
        if (_ui.value.syncing) return
        _ui.value = QueueUi(syncing = true)
        scope.launch {
            val r = s.sync.syncOnce(uid)
            _ui.value = QueueUi(
                syncing = false,
                lastMessage = when {
                    r.sent == 0 -> "چیزی برای ارسال نیست."
                    r.authLost -> "برای ارسال، دوباره وارد شوید."
                    r.networkDown -> "اتصال برقرار نیست؛ بعداً دوباره تلاش می‌شود."
                    else -> "ارسال شد: ${Jalali.persianDigits(r.ok.toString())}، ردشده: ${Jalali.persianDigits(r.rejected.toString())}، در انتظار: ${Jalali.persianDigits(r.retry.toString())}"
                },
            )
        }
    }

    fun clearRejected() { scope.launch { s.queue.clearRejected() } }
}

// ---------------------------------------------------------------- places (shift selection)

data class PlacesUi(val places: List<Place> = emptyList(), val busy: Boolean = false, val error: String? = null)

class PlacesPresenter(private val s: Services, private val scope: CoroutineScope) {
    private val _state = MutableStateFlow(PlacesUi())
    val state: StateFlow<PlacesUi> = _state.asStateFlow()

    /** Only the places this role may start a shift at (mirrors the server rule; the server re-checks). */
    fun load() {
        _state.update { it.copy(busy = true, error = null) }
        scope.launch {
            when (val r = s.api.places()) {
                is ApiResult.Ok -> {
                    val role = s.session.role
                    _state.update { it.copy(busy = false, places = r.value.filter { p -> mayWorkAt(role, p.kind) }) }
                }
                is ApiResult.Failure -> _state.update { it.copy(busy = false, error = r.error.userMessage()) }
            }
        }
    }

    private fun mayWorkAt(role: Role, kind: String): Boolean = when (role) {
        Role.SERVER, Role.GATE_LEAD -> kind == "gate" || kind == "storage"
        Role.TEAM -> kind == "gate" || kind == "storage" || kind == "repair"
        Role.WORKSHOP -> kind == "workshop"
        Role.SERVICE -> true
        else -> false
    }
}
