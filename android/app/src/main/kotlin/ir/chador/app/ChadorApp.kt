package ir.chador.app

import android.app.Application
import android.net.ConnectivityManager
import android.net.Network
import android.net.NetworkCapabilities
import android.net.NetworkRequest
import androidx.work.Constraints
import androidx.work.CoroutineWorker
import androidx.work.ExistingPeriodicWorkPolicy
import androidx.work.NetworkType
import androidx.work.PeriodicWorkRequestBuilder
import androidx.work.WorkManager
import androidx.work.WorkerParameters
import ir.chador.core.ChadorCore
import ir.chador.core.FileOpQueue
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import java.io.File
import java.util.concurrent.TimeUnit

class ChadorApp : Application() {
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)

    lateinit var core: ChadorCore
        private set

    override fun onCreate() {
        super.onCreate()
        val dir = File(filesDir, "chador").apply { mkdirs() }
        core = ChadorCore(
            baseUrl = BuildConfig.API_BASE_URL,
            storage = KeystoreSessionStorage(File(dir, "session.bin")),
            queue = FileOpQueue(File(dir, "queue").apply { mkdirs() }),
            scope = scope,
        )
        core.session.restore()
        watchConnectivity()
        scheduleBackgroundSync()
        core.syncCoordinator.kick()
    }

    /** As soon as the network comes back, send what was saved while offline. */
    private fun watchConnectivity() {
        val cm = getSystemService(CONNECTIVITY_SERVICE) as ConnectivityManager
        val request = NetworkRequest.Builder().addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET).build()
        try {
            cm.registerNetworkCallback(request, object : ConnectivityManager.NetworkCallback() {
                override fun onAvailable(network: Network) { core.syncCoordinator.kick() }
            })
        } catch (e: Exception) {
            // not critical: the coordinator also retries by itself
        }
    }

    /** Safety net: even if the app is closed, Android wakes a worker now and then to flush the queue. */
    private fun scheduleBackgroundSync() {
        val req = PeriodicWorkRequestBuilder<SyncWorker>(15, TimeUnit.MINUTES)
            .setConstraints(Constraints.Builder().setRequiredNetworkType(NetworkType.CONNECTED).build())
            .build()
        WorkManager.getInstance(this).enqueueUniquePeriodicWork("chador-sync", ExistingPeriodicWorkPolicy.KEEP, req)
    }
}

class SyncWorker(ctx: android.content.Context, params: WorkerParameters) : CoroutineWorker(ctx, params) {
    override suspend fun doWork(): Result {
        val core = (applicationContext as ChadorApp).core
        val uid = core.session.userId ?: return Result.success()   // nobody signed in: nothing may be sent
        val r = core.sync.syncOnce(uid)
        return if (r.networkDown || r.retry > 0) Result.retry() else Result.success()
    }
}
