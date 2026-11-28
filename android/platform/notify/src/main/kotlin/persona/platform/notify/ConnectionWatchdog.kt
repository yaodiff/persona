package persona.platform.notify

import android.content.Context
import android.os.PowerManager
import androidx.work.CoroutineWorker
import androidx.work.ExistingPeriodicWorkPolicy
import androidx.work.ListenableWorker
import androidx.work.PeriodicWorkRequestBuilder
import androidx.work.WorkManager
import androidx.work.WorkerParameters
import kotlinx.coroutines.CancellationException
import java.util.concurrent.TimeUnit

open class ConnectionWatchdog(context: Context, parameters: WorkerParameters) : CoroutineWorker(context, parameters) {
    override suspend fun doWork(): Result {
        val dependencies = Notify.dependencies(applicationContext)
        dependencies.journal().capture()
        dependencies.pairing().restore()
        val exempt = applicationContext.getSystemService(PowerManager::class.java)
            .isIgnoringBatteryOptimizations(applicationContext.packageName)
        return watchdogResult(
            paired = dependencies.pairing().isPaired.value,
            userStopped = dependencies.journal().userStopped,
            serviceRunning = HubService.running,
            mayStartService = exempt,
            startService = { HubService.start(applicationContext) },
            wake = { ResidentConnection.wake(dependencies.connection()) },
            catchUp = {
                val notifier = dependencies.notifier()
                val preferences = dependencies.notificationPreferences()
                NotificationDelivery(dependencies.interactions(), dependencies.sessions(),
                    { NotificationPolicy.suppressed(it, Notify.foreground.value, Notify.visibleSession.value) },
                    notifier::postInteraction, notifier::postNotify, notifier::retract,
                    preferences::refreshPreferences, notifier::deliveredRequests, preferences::desktopOnline,
                    catchUpPreferences = preferences::refreshPreferencesForCatchUp,
                    retractOccurrence = { id, occurrence -> notifier.retract(id, occurrence) },
                    notifiedOccurrences = notifier::deliveredOccurrences).catchUp()
            },
        )
    }

    companion object {
        private const val WORK_NAME = "persona-hub-watchdog"

        internal fun schedule(context: Context) {
            WorkManager.getInstance(context).enqueueUniquePeriodicWork(WORK_NAME, ExistingPeriodicWorkPolicy.KEEP,
                PeriodicWorkRequestBuilder<ConnectionWatchdog>(15, TimeUnit.MINUTES).build())
        }

        internal fun cancel(context: Context) { WorkManager.getInstance(context).cancelUniqueWork(WORK_NAME) }
    }
}

internal suspend fun watchdogResult(
    paired: Boolean,
    userStopped: Boolean,
    serviceRunning: Boolean,
    mayStartService: Boolean,
    startService: () -> Boolean,
    wake: () -> Unit,
    catchUp: suspend () -> Unit,
): ListenableWorker.Result {
    if (!paired || userStopped) return ListenableWorker.Result.success()
    if (serviceRunning) {
        wake()
        return ListenableWorker.Result.success()
    }
    return try {
        if (mayStartService && startService()) return ListenableWorker.Result.success()
        catchUp()
        ListenableWorker.Result.success()
    } catch (cancelled: CancellationException) {
        throw cancelled
    } catch (_: Exception) {
        ListenableWorker.Result.retry()
    }
}
