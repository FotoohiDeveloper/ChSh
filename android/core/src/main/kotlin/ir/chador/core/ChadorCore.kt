package ir.chador.core

import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import okhttp3.OkHttpClient
import java.util.UUID

/**
 * Keeps the offline queue draining: kicked after successful calls, on connectivity changes and on app start.
 * While the server is unreachable it retries with a growing delay (10 s ... 2 min).
 */
class SyncCoordinator(private val s: Services, private val scope: CoroutineScope) {
    private val trigger = Channel<Unit>(Channel.CONFLATED)

    fun kick() { trigger.trySend(Unit) }

    fun start() {
        scope.launch {
            for (t in trigger) {
                val uid = s.session.userId ?: continue
                var attempt = 0
                while (isActive && s.queue.pendingFor(uid, 1).isNotEmpty()) {
                    val r = s.sync.syncOnce(uid)
                    if (r.authLost) break
                    if (r.networkDown || r.retry > 0) {
                        delay((10_000L shl attempt.coerceAtMost(4)).coerceAtMost(120_000L))
                        attempt++
                    } else {
                        attempt = 0
                    }
                }
            }
        }
    }
}

/** Composition root: wires the API client, session, offline queue and presenters together. */
class ChadorCore(
    baseUrl: String,
    storage: SessionStorage,
    val queue: OpQueue,
    private val scope: CoroutineScope,
    http: OkHttpClient = defaultHttpClient(),
    newId: () -> String = { UUID.randomUUID().toString() },
    now: () -> Long = System::currentTimeMillis,
) {
    val tokens = TokenHolder()
    private lateinit var sessionRef: SessionManager
    val client = ApiClient(baseUrl, http, tokens, onTokensChanged = { sessionRef.onTokensChanged(it) }, onSessionExpired = { sessionRef.onExpired() })
    val api = Api(client)
    val session: SessionManager = SessionManager(api, tokens, storage, scope).also { sessionRef = it }
    val sync = SyncEngine(api, queue, now)
    val ops = Ops(api, queue, session, newId, now)
    val services = Services(api, session, ops, queue, sync, now)
    val syncCoordinator = SyncCoordinator(services, scope)

    init {
        ops.onSuccess = { syncCoordinator.kick() }
        syncCoordinator.start()
    }

    // Presenters are created per screen with that screen's scope (a ViewModel scope on Android).
    fun handout(scope: CoroutineScope) = HandoutPresenter(services, scope)
    fun returns(scope: CoroutineScope) = ReturnPresenter(services, scope)
    fun tentInfo(scope: CoroutineScope) = TentInfoPresenter(services, scope)
    fun statusBatch(scope: CoroutineScope, target: TentStatus) = StatusBatchPresenter(services, scope, target)
    fun move(scope: CoroutineScope) = MovePresenter(services, scope)
    fun dashboard(scope: CoroutineScope) = DashboardPresenter(services, scope)
    fun report(scope: CoroutineScope) = ReportPresenter(services, scope)
    fun socks(scope: CoroutineScope) = SocksPresenter(services, scope)
    fun handover(scope: CoroutineScope) = HandoverPresenter(services, scope)
    fun retire(scope: CoroutineScope) = RetirePresenter(services, scope)
    fun users(scope: CoroutineScope) = UsersPresenter(services, scope)
    fun settings(scope: CoroutineScope) = SettingsPresenter(services, scope)
    fun audit(scope: CoroutineScope) = AuditPresenter(services, scope)
    fun queueStatus(scope: CoroutineScope) = QueuePresenter(services, scope)
    fun places(scope: CoroutineScope) = PlacesPresenter(services, scope)
}
