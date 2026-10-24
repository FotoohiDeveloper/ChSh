package ir.chador.core

import kotlinx.serialization.KSerializer
import kotlinx.serialization.json.JsonElement
import java.util.UUID

sealed interface Outcome<out T> {
    /** The server accepted the operation. */
    data class Done<T>(val value: T) : Outcome<T>

    /** No connection (or the server is struggling): saved on the device, will be sent automatically. */
    data class Queued(val label: String) : Outcome<Nothing>

    /** The server refused it; retrying will not help. */
    data class Rejected(val error: ApiError) : Outcome<Nothing>
}

/**
 * Every state-changing action goes through here. It sends the request with a fresh `op_id`; if the network
 * is down it stores the *same* op in the on-device queue instead. Because the server remembers op ids,
 * it does not matter whether the first attempt actually reached it: replaying is always safe.
 */
class Ops(
    private val api: Api,
    private val queue: OpQueue,
    private val session: SessionManager,
    private val newId: () -> String = { UUID.randomUUID().toString() },
    private val now: () -> Long = System::currentTimeMillis,
) {
    /** Called after any successful direct call; the app uses it to flush the offline queue promptly. */
    var onSuccess: (() -> Unit)? = null

    private suspend fun <Res> run(
        type: String,
        label: String,
        opId: String,
        queuePayload: JsonElement,
        call: suspend (clientTs: String) -> ApiResult<Res>,
    ): Outcome<Res> {
        val userId = session.userId ?: return Outcome.Rejected(ApiError(ApiError.Kind.AUTH, "unauthorized", "ابتدا وارد شوید."))
        val ts = Time.format(now())
        return when (val r = call(ts)) {
            is ApiResult.Ok -> { onSuccess?.invoke(); Outcome.Done(r.value) }
            is ApiResult.Failure ->
                if (r.error.retryable) {
                    queue.add(QueuedOp(opId, userId, type, session.shiftId, ts, queuePayload, now(), label))
                    Outcome.Queued(label)
                } else {
                    Outcome.Rejected(r.error)
                }
        }
    }

    private fun <T> payload(ser: KSerializer<T>, v: T): JsonElement = ChadorJson.encodeToJsonElement(ser, v)

    fun newOpId(): String = newId()

    suspend fun assign(pilgrimId: String, codes: List<String>, socks: Int, opId: String = newId()): Outcome<AssignResponse> =
        run("assign", "تحویل ${Jalali.persianDigits(codes.size.toString())} چادر", opId,
            payload(AssignRequest.serializer(), AssignRequest(pilgrimId, codes, socks))) { ts ->
            api.assign(AssignRequest(pilgrimId, codes, socks, opId, ts))
        }

    suspend fun returns(items: List<ReturnItemRequest>, opId: String = newId()): Outcome<ReturnResponse> =
        run("return", "بازگشت ${Jalali.persianDigits(items.size.toString())} چادر", opId,
            payload(ReturnRequest.serializer(), ReturnRequest(items))) { ts ->
            api.returns(ReturnRequest(items, opId, ts))
        }

    suspend fun cancel(code: String, opId: String = newId()): Outcome<CancelResponse> =
        // the sync endpoint decodes cancel payloads strictly: only {"code": ...}
        run("cancel", "لغو تخصیص $code", opId, payload(CancelRequest.serializer(), CancelRequest(code))) {
            api.cancel(CancelRequest(code, opId))
        }

    suspend fun setStatus(code: String, to: TentStatus, note: String? = null, opId: String = newId()): Outcome<StatusResponse> =
        run("status", "${Labels.status(to)}: $code", opId, payload(StatusRequest.serializer(), StatusRequest(code, to.wire, note))) { ts ->
            api.setStatus(StatusRequest(code, to.wire, note, opId, ts))
        }

    suspend fun move(codes: List<String>, toPlaceId: Int, opId: String = newId()): Outcome<MoveResponse> =
        run("move", "انتقال ${Jalali.persianDigits(codes.size.toString())} چادر", opId, payload(MoveRequest.serializer(), MoveRequest(codes, toPlaceId))) { ts ->
            api.move(MoveRequest(codes, toPlaceId, opId, ts))
        }

    suspend fun socks(kind: String, qty: Int, placeId: Int? = null, pilgrimId: String? = null, opId: String = newId()): Outcome<SocksResponse> =
        run("socks", "جوراب ($qty)", opId, payload(SocksRequest.serializer(), SocksRequest(kind, qty, placeId, pilgrimId))) { ts ->
            api.socks(SocksRequest(kind, qty, placeId, pilgrimId, opId, ts))
        }
}

/** Pieces every presenter needs. */
class Services(
    val api: Api,
    val session: SessionManager,
    val ops: Ops,
    val queue: OpQueue,
    val sync: SyncEngine,
    val now: () -> Long = System::currentTimeMillis,
)
