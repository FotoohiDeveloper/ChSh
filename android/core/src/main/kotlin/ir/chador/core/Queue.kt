package ir.chador.core

import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.serialization.Serializable
import kotlinx.serialization.builtins.ListSerializer
import kotlinx.serialization.json.JsonElement
import java.io.File
import java.nio.file.Files
import java.nio.file.StandardCopyOption

/** An operation recorded on the device that has not reached the server yet. */
@Serializable
data class QueuedOp(
    val opId: String,
    val userId: String,
    val type: String,        // assign | return | cancel | status | move | socks
    val shiftId: String?,
    val clientTs: String,
    val payload: JsonElement,
    val createdAt: Long,
    val label: String,       // short Persian description shown to the user
    val attempts: Int = 0,
)

/** An operation the server refused for a business reason. It is kept so the user can see what did not go through. */
@Serializable
data class RejectedOp(val op: QueuedOp, val code: String, val message: String, val at: Long)

interface OpQueue {
    val pending: StateFlow<Int>
    val rejected: StateFlow<List<RejectedOp>>
    suspend fun add(op: QueuedOp)
    suspend fun pendingFor(userId: String, limit: Int = 200): List<QueuedOp>
    suspend fun apply(done: Set<String>, newlyRejected: List<RejectedOp>, retried: Set<String>)
    suspend fun clearRejected()
}

/** In-memory queue; also the base class of the file-backed one. */
open class MemoryOpQueue : OpQueue {
    protected val mutex = Mutex()
    protected var ops: MutableList<QueuedOp> = mutableListOf()
    protected var rej: MutableList<RejectedOp> = mutableListOf()
    private val _pending = MutableStateFlow(0)
    private val _rejected = MutableStateFlow<List<RejectedOp>>(emptyList())
    override val pending: StateFlow<Int> = _pending.asStateFlow()
    override val rejected: StateFlow<List<RejectedOp>> = _rejected.asStateFlow()

    protected open fun persist() {}
    protected fun publish() {
        _pending.value = ops.size
        _rejected.value = rej.toList()
    }

    override suspend fun add(op: QueuedOp) = mutex.withLock {
        if (ops.none { it.opId == op.opId }) ops.add(op)
        persist(); publish()
    }

    override suspend fun pendingFor(userId: String, limit: Int): List<QueuedOp> = mutex.withLock {
        ops.filter { it.userId == userId }.sortedBy { it.createdAt }.take(limit)
    }

    override suspend fun apply(done: Set<String>, newlyRejected: List<RejectedOp>, retried: Set<String>) = mutex.withLock {
        ops = ops.filter { it.opId !in done }.map { if (it.opId in retried) it.copy(attempts = it.attempts + 1) else it }.toMutableList()
        rej.addAll(newlyRejected)
        if (rej.size > 100) rej = rej.takeLast(100).toMutableList()
        persist(); publish()
    }

    override suspend fun clearRejected() = mutex.withLock {
        rej.clear()
        persist(); publish()
    }
}

/**
 * Queue stored in two JSON files, replaced atomically (write temp file, then rename), so a crash or
 * a killed process can never leave a half-written queue. Volumes are tiny (hundreds of entries).
 */
class FileOpQueue(private val dir: File) : MemoryOpQueue() {
    private val queueFile = File(dir, "queue.json")
    private val rejectedFile = File(dir, "rejected.json")

    init {
        dir.mkdirs()
        ops = read(queueFile, QueuedOp.serializer())
        rej = read(rejectedFile, RejectedOp.serializer())
        publish()
    }

    private fun <T> read(f: File, ser: kotlinx.serialization.KSerializer<T>): MutableList<T> = try {
        if (f.exists()) ChadorJson.decodeFromString(ListSerializer(ser), f.readText()).toMutableList() else mutableListOf()
    } catch (e: Exception) {
        // A corrupt file must not brick the app; keep it for inspection and start empty.
        f.renameTo(File(dir, f.name + ".corrupt-" + System.currentTimeMillis()))
        mutableListOf()
    }

    private fun <T> write(f: File, ser: kotlinx.serialization.KSerializer<T>, items: List<T>) {
        val tmp = File(dir, f.name + ".tmp")
        tmp.writeText(ChadorJson.encodeToString(ListSerializer(ser), items))
        Files.move(tmp.toPath(), f.toPath(), StandardCopyOption.REPLACE_EXISTING, StandardCopyOption.ATOMIC_MOVE)
    }

    override fun persist() {
        write(queueFile, QueuedOp.serializer(), ops)
        write(rejectedFile, RejectedOp.serializer(), rej)
    }
}

data class SyncReport(
    val sent: Int = 0,
    val ok: Int = 0,
    val rejected: Int = 0,
    val retry: Int = 0,
    val networkDown: Boolean = false,
    val authLost: Boolean = false,
)

/** Sends the queue to POST /sync. Safe to call at any time and from several places: calls are serialised. */
const val MAX_BAD_REQUEST_ATTEMPTS = 5

class SyncEngine(
    private val api: Api,
    private val queue: OpQueue,
    private val now: () -> Long = System::currentTimeMillis,
) {
    private val mutex = Mutex()

    suspend fun syncOnce(userId: String): SyncReport = mutex.withLock {
        val ops = queue.pendingFor(userId)
        if (ops.isEmpty()) return SyncReport()
        val req = SyncRequest(ops.map { SyncOpDto(it.opId, it.type, it.shiftId, it.clientTs, it.payload) })
        when (val r = api.sync(req)) {
            is ApiResult.Failure -> {
                val e = r.error
                if (e.kind == ApiError.Kind.REJECTED && e.status == 400) {
                    // The server refuses the whole batch as malformed. Keep trying a few times, then park the ops
                    // in the "rejected" list so one bad entry can never block the queue forever.
                    val poisoned = ops.filter { it.attempts + 1 >= MAX_BAD_REQUEST_ATTEMPTS }
                    val parked = poisoned.map { it.opId }.toSet()
                    queue.apply(parked, poisoned.map { RejectedOp(it, e.code, e.message, now()) }, ops.map { it.opId }.toSet() - parked)
                    SyncReport(sent = ops.size, rejected = poisoned.size, retry = ops.size - poisoned.size)
                } else {
                    SyncReport(
                        sent = ops.size,
                        networkDown = e.kind == ApiError.Kind.NETWORK || e.kind == ApiError.Kind.SERVER,
                        authLost = e.kind == ApiError.Kind.AUTH,
                        retry = if (e.kind == ApiError.Kind.REJECTED) ops.size else 0, // e.g. password change pending: keep everything, try later
                    )
                }
            }
            is ApiResult.Ok -> {
                val byId = ops.associateBy { it.opId }
                val done = mutableSetOf<String>()
                val retried = mutableSetOf<String>()
                val rejected = mutableListOf<RejectedOp>()
                for (res in r.value.results) {
                    val op = byId[res.opId] ?: continue
                    when {
                        res.status == "ok" || res.status == "replayed" -> done += op.opId
                        res.status == "rejected" && res.error?.code != "op_in_progress" ->
                            { done += op.opId; rejected += RejectedOp(op, res.error?.code ?: "rejected", res.error?.message ?: "سرور این عملیات را نپذیرفت.", now()) }
                        else -> retried += op.opId
                    }
                }
                queue.apply(done, rejected, retried)
                SyncReport(sent = ops.size, ok = done.size - rejected.size, rejected = rejected.size, retry = retried.size)
            }
        }
    }
}
