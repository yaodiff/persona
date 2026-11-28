package persona.platform.notify

import android.Manifest
import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Build
import androidx.core.app.NotificationCompat
import androidx.core.app.NotificationManagerCompat
import androidx.core.app.RemoteInput
import androidx.core.content.ContextCompat
import dagger.hilt.android.qualifiers.ApplicationContext
import persona.core.designsystem.PendingStatus
import persona.core.designsystem.Status
import persona.core.contracts.Interaction
import persona.core.contracts.NotifyEvent
import persona.core.contracts.SessionKey
import persona.core.contracts.api.DeviceNotificationPreferences
import persona.core.contracts.api.DoneNotification
import persona.core.contracts.api.ExtensionNotificationActionRecord
import persona.core.contracts.api.ExtensionNotificationClaim
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.collect
import persona.core.contracts.api.SessionsRepository
import persona.core.navigation.AppTab
import persona.core.navigation.NotificationRoute
import persona.core.navigation.TerminalRoute
import java.time.Instant
import javax.inject.Inject
import javax.inject.Singleton

@Singleton
class Notifier @Inject constructor(
    @ApplicationContext private val context: Context,
    private val sessions: SessionsRepository,
    private val preferences: NotificationPreferencesSource,
) {
    private val manager = NotificationManagerCompat.from(context)
    private val requestHistory = RequestNotificationHistory(context)
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate)
    private val showLockDetails get() = preferences.preferences.value?.lockDetails == true

    init {
        scope.launch {
            var previous: persona.core.contracts.api.DeviceNotificationPreferences? = null
            // null is "not known yet" (startup, every reconnect while the route is re-picked), not "off" (#4699);
            // unpairing clears through NotificationRuntime instead.
            preferences.preferences.collect { current ->
                if (current == null) return@collect
                val before = previous
                if (before != null && (before.lockDetails && !current.lockDetails ||
                    before.input && !current.input ||
                    before.done != DoneNotification.Off && current.done == DoneNotification.Off)) clear()
                previous = current
            }
        }
        context.getSystemService(NotificationManager::class.java).apply {
            deleteNotificationChannel("ongoing")
            createNotificationChannels(listOf(
                NotificationChannel(CH_INPUT, context.getString(R.string.notify_channel_input), NotificationManager.IMPORTANCE_HIGH),
                NotificationChannel(CH_ALERT, context.getString(R.string.notify_channel_alert), NotificationManager.IMPORTANCE_HIGH),
                NotificationChannel(CH_DONE, context.getString(R.string.notify_channel_done), NotificationManager.IMPORTANCE_DEFAULT),
                NotificationChannel(CH_ASSISTANT, "Persona", NotificationManager.IMPORTANCE_DEFAULT),
                NotificationChannel(CH_STATUS, context.getString(R.string.notify_channel_status), NotificationManager.IMPORTANCE_LOW).apply {
                    setSound(null, null)
                    enableVibration(false)
                    setShowBadge(false)
                },
            ))
        }
    }

    private fun isAssistant(session: SessionKey) = sessions.sessions.value.firstOrNull { it.key == session }?.assistant

    /**
     * [request]: the interaction to show first in the conversation, so a tap lands on the thing it is about. A request
     * outside any session (empty session id, #4515) has no conversation or terminal: it opens Agents with that card open
     * (#4808), or just Agents when no request is named.
     */
    private fun open(session: SessionKey?, assistant: Boolean? = null, terminal: Boolean = false, request: String? = null): PendingIntent {
        val target = session?.takeIf { it.sessionId.isNotBlank() }
        val uri = if (terminal && target != null) Uri.parse(terminalLink(TerminalRoute(target.machineId, target.sessionId)))
        else target?.let {
            NotificationRoute.conversation(it.machineId, it.sessionId, assistant ?: isAssistant(it), request)
        }?.uri() ?: NotificationRoute.card(session?.machineId.orEmpty(), request).uri()
        val intent = Intent(Intent.ACTION_VIEW, uri).setPackage(context.packageName)
            .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_SINGLE_TOP)
        return PendingIntent.getActivity(context, 0, intent, PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE)
    }

    internal fun ongoing(status: OngoingStatus): Notification {
        val icon = persona.core.designsystem.R.drawable.ic_stat
        val title = status.titleRes?.let(context::getString) ?: status.title
        // The lock screen's version: counts and "An approval is waiting" only, never an agent's name or what it asks. The private one
        // names agents only with "Show details on lock screen" (OngoingStatus.from: a phone may show it on the lock screen too), and the system
        // shows it there only where the user lets sensitive content show; elsewhere this one.
        val public = NotificationCompat.Builder(context, CH_STATUS).setSmallIcon(icon)
            .setContentTitle(title).setContentText(status.publicText)
            .setShowWhen(false)
            .setVisibility(NotificationCompat.VISIBILITY_PUBLIC).build()
        val builder = NotificationCompat.Builder(context, CH_STATUS).setSmallIcon(icon)
            .setContentTitle(title)
            .setContentText(status.text)
            .setSubText(status.subText)
            .apply { status.lines?.let { setStyle(NotificationCompat.BigTextStyle().bigText(it)) } }
            // Never public: with "Show details on lock screen" on, a public notification put names on lock screens set to hide sensitive content.
            .setVisibility(NotificationCompat.VISIBILITY_PRIVATE)
            .setPublicVersion(public)
            .setCategory(when {
                status.needsYou > 0 -> NotificationCompat.CATEGORY_STATUS
                status.summary -> NotificationCompat.CATEGORY_PROGRESS
                else -> NotificationCompat.CATEGORY_SERVICE
            })
            .setOngoing(true)
            .setSilent(true)
            .setOnlyAlertOnce(true)
            .setForegroundServiceBehavior(NotificationCompat.FOREGROUND_SERVICE_IMMEDIATE)
            // One agent: straight to it. Several: the Agents list.
            .setContentIntent(open(status.open))
        val since = status.workingSince
        // The clock runs on the phone from when the longest turn started (iOS Text(timerInterval:)).
        if (since != null) builder.setWhen(since).setShowWhen(true).setUsesChronometer(true) else builder.setShowWhen(false)
        status.action?.let { request -> request.buttons.forEach { builder.addAction(ongoingAction(request, it)) } }
        val notification = builder.build()
        val enabled = preferences.preferences.value?.liveActivity == true
        // setRequestPromotedOngoing is Android 16 QPR1 (36.1): on 36.0 it throws NoSuchMethodError, which the catch
        // below does not take. SDK_INT_FULL itself only exists from 36.
        if (Build.VERSION.SDK_INT < 36 || Build.VERSION.SDK_INT_FULL < Build.VERSION_CODES_FULL.BAKLAVA_1 || !enabled)
            return notification
        // Promotion is optional: OEM policy or user settings must never break the foreground service.
        return try {
            val allowed = context.getSystemService(NotificationManager::class.java).canPostPromotedNotifications()
            if (!status.shouldPromote(Build.VERSION.SDK_INT_FULL, allowed, enabled)) notification
            else Notification.Builder.recoverBuilder(context, notification)
                .setRequestPromotedOngoing(true)
                .setShortCriticalText(status.criticalText)
                .apply {
                    if (Build.VERSION.SDK_INT >= 37) {
                        setStyle(Notification.MetricStyle()
                            .addMetric(Notification.Metric(Notification.Metric.FixedInt(status.needsYou), Status.NeedsYou.label(context)))
                            .addMetric(Notification.Metric(Notification.Metric.FixedInt(status.working), Status.Working.label(context)))
                            .addMetric(Notification.Metric(Notification.Metric.FixedInt(status.unread), Status.Unread.label(context)))
                            .setCriticalMetric(if (status.needsYou > 0) 0 else if (status.working > 0) 1 else 2))
                    }
                }
                .build()
        } catch (_: RuntimeException) {
            notification
        }
    }

    /**
     * A button of the ongoing notification. Answers go through ActionReceiver and NotificationActionExecutor like the
     * "Needs you" notification's (refresh, expiry, unlock, one durable claim per request); "View" opens the conversation.
     * Each PendingIntent is immutable and told apart by its data URI, so a later redraw can never point a token the
     * system holds while the user unlocks at another request.
     */
    private fun ongoingAction(request: OngoingAction, button: OngoingButton): NotificationCompat.Action {
        val key = button.key
            ?: return NotificationCompat.Action.Builder(0, context.getString(button.label),
                open(request.session, request = request.interaction)).build()
        val intent = Intent(context, ActionReceiver::class.java).apply {
            this.action = ActionReceiver.RESOLVE
            data = Uri.Builder().scheme("persona-notify").authority("ongoing")
                .appendPath(request.interaction).appendPath(key).build()
            request.session?.let { session ->
                putExtra(ActionReceiver.EXTRA_MACHINE, session.machineId)
                putExtra(ActionReceiver.EXTRA_SESSION, session.sessionId)
                isAssistant(session)?.let { putExtra(ActionReceiver.EXTRA_ASSISTANT, it) }
            }
            putExtra(ActionReceiver.EXTRA_ID, request.interaction)
            putExtra(ActionReceiver.EXTRA_CREATED_AT, request.createdAt)
            putExtra(ActionReceiver.EXTRA_ACTION, key)
            putExtra(ActionReceiver.EXTRA_QUIET, true)
        }
        val pending = PendingIntent.getBroadcast(context, 0, intent, PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE)
        return NotificationCompat.Action.Builder(0, context.getString(button.label), pending)
            .setAuthenticationRequired(button.authenticationRequired).build()
    }

    internal fun updateOngoing(status: OngoingStatus) = post(null, ONGOING_ID, ongoing(status))

    /** [hushed]: inside this device's quiet hours (the hub's `quiet`), shown without a sound. */
    fun postInteraction(ix: Interaction, hushed: Boolean = false) {
        postInteraction(ix, note = null, hushed = hushed, deliveryPreferences = null)
    }

    /** Catch-up supplies its own hub group's preferences; null keeps live delivery's current-preference check. */
    fun postInteraction(ix: Interaction, hushed: Boolean, deliveryPreferences: DeviceNotificationPreferences?) {
        postInteraction(ix, note = null, hushed = hushed, deliveryPreferences = deliveryPreferences)
    }

    /** [note] replaces the line under the title and leads the expanded text (#5008). False when nothing was posted. */
    private fun postInteraction(
        ix: Interaction,
        note: String?,
        hushed: Boolean = false,
        deliveryPreferences: DeviceNotificationPreferences? = null,
    ): Boolean {
        val settings = deliveryPreferences ?: preferences.preferences.value
        if (settings?.input != true) return false
        if (NotificationPolicy.expired(ix)) { retract(ix.id, ix.createdAt); return false }
        val assistant = isAssistant(ix.sessionKey)
        val title = if (assistant == true && ix.kind == "question") context.getString(R.string.notify_title_assistant_asks)
            else ix.sessionTitle?.takeIf { it.isNotBlank() } ?: context.getString(R.string.notify_title_needs_you)
        val (line, detail) = requestCopy(context, ix).let { (line, detail) ->
            if (note == null) line to detail else note to "$note\n$detail"
        }
        val builder = base(CH_INPUT, settings.lockDetails).forSession(ix.sessionKey).setContentTitle(title).setContentText(line)
            .addExtras(android.os.Bundle().apply {
                putBoolean(EXTRA_REQUEST, true)
                putString(ActionReceiver.EXTRA_CREATED_AT, ix.createdAt)
                if (note != null) putBoolean(EXTRA_OCCURRENCE_GUARD, true)
            })
            .setStyle(NotificationCompat.BigTextStyle().bigText(detail.take(2000)))
            .setPriority(NotificationCompat.PRIORITY_HIGH)
            .setCategory(NotificationCompat.CATEGORY_MESSAGE)
            .setOnlyAlertOnce(true)
            .setSilent(hushed)
            .setAutoCancel(true)
            .setContentIntent(open(ix.sessionKey, request = ix.id))
        ix.expiresAt?.let { deadline ->
            builder.setTimeoutAfter((Instant.parse(deadline).toEpochMilli() - System.currentTimeMillis()).coerceAtLeast(1))
        }
        NotificationPolicy.actions(ix).forEach { action -> builder.addAction(action(ix, action)) }
        post(interactionTag(ix.id), EVENT_ID, builder.build())
        return true
    }

    private fun action(ix: Interaction, action: NotificationAction): NotificationCompat.Action {
        val intent = Intent(context, ActionReceiver::class.java).apply {
            this.action = ActionReceiver.RESOLVE
            data = Uri.Builder().scheme("persona-notify").authority("resolve")
                .appendPath(ix.id).appendPath(action.key).build()
            putExtra(ActionReceiver.EXTRA_MACHINE, ix.machineId)
            putExtra(ActionReceiver.EXTRA_SESSION, ix.sessionId)
            isAssistant(ix.sessionKey)?.let { putExtra(ActionReceiver.EXTRA_ASSISTANT, it) }
            putExtra(ActionReceiver.EXTRA_ID, ix.id)
            putExtra(ActionReceiver.EXTRA_CREATED_AT, ix.createdAt)
            putExtra(ActionReceiver.EXTRA_EXPIRES_AT, ix.expiresAt)
            putExtra(ActionReceiver.EXTRA_ACTION, action.key)
        }
        val mutable = if (action.replyLabel != null) PendingIntent.FLAG_MUTABLE else PendingIntent.FLAG_IMMUTABLE
        val pending = PendingIntent.getBroadcast(context, 0, intent, PendingIntent.FLAG_UPDATE_CURRENT or mutable)
        val label = action.labelRes?.let(context::getString) ?: action.label.orEmpty()
        return NotificationCompat.Action.Builder(0, label.replace(Regex("\\s+"), " ").trim(), pending)
            .setAuthenticationRequired(action.authenticationRequired)
            .apply {
                action.replyLabel?.let { reply ->
                    addRemoteInput(RemoteInput.Builder(ActionReceiver.EXTRA_REPLY).setLabel(context.getString(reply)).build())
                }
            }.build()
    }

    fun postNotify(event: NotifyEvent) {
        val channel = NotificationPolicy.channel(event) ?: return
        val settings = preferences.preferences.value ?: return
        if (channel == CH_DONE && settings.done == DoneNotification.Off) return
        if (channel == CH_ALERT && !settings.input) return
        val session = NotificationPolicy.session(event)
        val tag = NotificationPolicy.tag(event)
        val extension = prepareExtensionNotification(context, event, tag)
        val title = event.title.ifBlank { when (channel) { CH_DONE -> context.getString(R.string.notify_title_done); CH_ALERT -> context.getString(R.string.notify_title_alert); else -> "Persona" } }
        post(tag, EVENT_ID, base(channel).forSession(session)
            .setContentTitle(title)
            .setContentText(event.body.ifBlank { event.subtitle })
            .setSubText(event.subtitle.takeIf { it.isNotBlank() })
            .setStyle(NotificationCompat.BigTextStyle().bigText(event.body.take(2000)))
            // Inside this device's quiet hours (the hub marks reply reminders so): shown, without a sound.
            .setSilent(event.quiet)
            .setAutoCancel(true).apply {
                if (extension != null) {
                    val intents = ExtensionNotificationIntents(context)
                    setContentIntent(intents.contentIntent(extension.body, title))
                    extension.buttons.forEach { addAction(intents.action(it)) }
                } else if (session != null) setContentIntent(open(session, if (event.kind == "assistant") true else isAssistant(session)))
            }.build())
    }

    internal fun notifyExtensionReport(report: ExtensionNotificationActionReport) = when (report) {
        is ExtensionNotificationActionReport.Toast -> extensionNotice(report.record,
            context.getString(R.string.notify_extension_action_done_title), report.text)
        is ExtensionNotificationActionReport.Unavailable -> extensionNotice(report.record,
            context.getString(R.string.notify_extension_action_failed_title), context.getString(R.string.notify_extension_action_unavailable))
        is ExtensionNotificationActionReport.PageExpired -> extensionNotice(report.record,
            context.getString(R.string.notify_extension_action_failed_title), context.getString(R.string.notify_extension_action_page_expired))
        is ExtensionNotificationActionReport.WrongScope -> extensionNotice(report.record,
            context.getString(R.string.notify_extension_action_failed_title), context.getString(R.string.notify_extension_action_wrong_scope))
        is ExtensionNotificationActionReport.ActionFailed -> extensionNotice(report.record,
            context.getString(R.string.notify_extension_action_failed_title), context.getString(R.string.notify_extension_action_failed_detail))
    }

    internal fun notifyExtensionTimeout(record: ExtensionNotificationActionRecord) = extensionNotice(record,
        context.getString(R.string.notify_result_unconfirmed_title), context.getString(R.string.notify_extension_action_unconfirmed))

    internal fun notifyExtensionMissing(token: String) = extensionNotice(null,
        context.getString(R.string.notify_extension_action_expired_title), context.getString(R.string.notify_extension_action_expired_detail), token)

    internal fun notifyExtensionClaimFailure(claim: ExtensionNotificationClaim) = when (claim) {
        is ExtensionNotificationClaim.WrongScope -> extensionNotice(claim.record,
            context.getString(R.string.notify_extension_action_failed_title), context.getString(R.string.notify_extension_action_wrong_scope))
        is ExtensionNotificationClaim.Expired -> extensionNotice(claim.record,
            context.getString(R.string.notify_extension_action_expired_title), context.getString(R.string.notify_extension_action_expired_detail))
        is ExtensionNotificationClaim.Invalid -> extensionNotice(claim.record,
            context.getString(R.string.notify_extension_action_expired_title), context.getString(R.string.notify_extension_action_expired_detail))
        else -> Unit
    }

    private fun extensionNotice(record: ExtensionNotificationActionRecord?, title: String, detail: String, token: String? = null) {
        val identity = record?.token ?: token ?: return
        post("alert:extension-result:$identity", EVENT_ID, base(CH_ALERT)
            .setContentTitle(title).setContentText(detail)
            .setStyle(NotificationCompat.BigTextStyle().bigText(detail.take(2000)))
            .setOnlyAlertOnce(true).setAutoCancel(true)
            .apply {
                record?.let { setContentIntent(ExtensionNotificationIntents(context).contentIntent(it, it.action?.title ?: it.extension)) }
            }.build())
    }

    @Synchronized
    fun retract(id: String, createdAt: String? = null) {
        val tag = interactionTag(id)
        if (createdAt != null) {
            val current = runCatching {
                context.getSystemService(NotificationManager::class.java).activeNotifications
                    .firstOrNull { it.id == EVENT_ID && it.tag == tag }
            }.getOrNull()
            if (current == null) {
                requestHistory.clear(id, createdAt)
                return
            }
            val shownAt = current.notification.extras.getString(ActionReceiver.EXTRA_CREATED_AT)
            if (shownAt != null && shownAt != createdAt) return
        }
        manager.cancel(tag, EVENT_ID)
        requestHistory.clear(id, createdAt)
    }

    /** Delivered occurrence by interaction ID; null is a record written by an older app. */
    fun deliveredOccurrences(): Map<String, String?> = requestHistory.occurrences()

    /** Every request already posted, including one the user dismissed; a watchdog pass must not post it again. */
    fun deliveredRequests(): Set<String> = requestHistory.ids()

    /**
     * Ids of the request notifications showing now: not the verifying / result notices that reuse their tag (#3401).
     * Empty when the system cannot be asked.
     */
    fun showingRequests(): Set<String> = runCatching {
        context.getSystemService(NotificationManager::class.java).activeNotifications
            .filter { it.id == EVENT_ID && it.notification.extras.getBoolean(EXTRA_REQUEST) }
            .mapNotNullTo(HashSet()) { notice -> notice.tag?.takeIf { it.startsWith(INTERACTION_PREFIX) }?.removePrefix(INTERACTION_PREFIX) }
    }.getOrDefault(emptySet())

    internal fun pending(id: String, session: SessionKey?, status: PendingStatus, assistant: Boolean?, createdAt: String? = null) =
        resultNotice(id, session, status.label(context), context.getString(R.string.notify_result_open_chat), assistant, createdAt = createdAt)

    fun retractSession(session: SessionKey) {
        context.getSystemService(NotificationManager::class.java).activeNotifications
            .filter { it.id == EVENT_ID && it.notification.extras.getString("persona.session") == session.path }
            .forEach { manager.cancel(it.tag, it.id) }
    }

    /** Tags of the "Finished" notifications showing now, with when each was posted (#4088). Empty when the system cannot be asked. */
    fun showingDone(): Map<String, Long> = showing(StaleDoneNotifications.DONE_PREFIX)

    /** Tags of the assistant notifications showing now, with when each was posted (#4805). */
    fun showingAssistant(): Map<String, Long> = showing(SeenAssistantNotifications.PREFIX)

    private fun showing(prefix: String): Map<String, Long> = runCatching {
        context.getSystemService(NotificationManager::class.java).activeNotifications
            .filter { it.id == EVENT_ID && it.tag?.startsWith(prefix) == true }
            .associate { it.tag to it.postTime }
    }.getOrDefault(emptyMap())

    /** Cancels an assistant notification only if the one showing was posted before [before] (#4805). */
    fun retractAssistant(tag: String, before: Long) {
        if (showingAssistant()[tag]?.let { it < before } == true) manager.cancel(tag, EVENT_ID)
    }

    /** Cancels a "Finished" notification only if the one showing was posted before [before]: a fresh one under the same tag stays. */
    fun retractDone(tag: String, before: Long) {
        if (showingDone()[tag]?.let { it < before } == true) manager.cancel(tag, EVENT_ID)
    }

    /** Takes away a session's "Finished" / assistant notifications once its result has been seen (#3567). */
    fun retractSeen(session: SessionKey) = NotificationPolicy.resultTags(session).forEach { manager.cancel(it, EVENT_ID) }

    private fun NotificationCompat.Builder.forSession(session: SessionKey?) = apply {
        session?.let { addExtras(android.os.Bundle().apply { putString("persona.session", it.path) }) }
    }

    fun failure(id: String, session: SessionKey?, assistant: Boolean? = null, createdAt: String? = null) = resultNotice(
        id, session, context.getString(R.string.notify_result_unconfirmed_title), context.getString(R.string.notify_result_check_chat), assistant,
        createdAt = createdAt,
    )

    /**
     * A tap that never reached the hub (#5008): the request comes back with its buttons and a line saying so, to be
     * tapped again. Without the request at hand (a cold start that could not reach the hub), there are no buttons to
     * bring back: a notice says it was not sent and opens the chat.
     */
    internal fun notSent(id: String, ix: Interaction?, session: SessionKey?, assistant: Boolean?, createdAt: String? = null) {
        if (ix != null && (createdAt == null || ix.createdAt == createdAt) &&
            postInteraction(ix, context.getString(R.string.notify_result_not_sent_detail))) return
        resultNotice(id, session, context.getString(R.string.notify_result_not_sent_title),
            context.getString(R.string.notify_result_open_chat), assistant, createdAt = createdAt)
    }

    internal fun actionResult(id: String, session: SessionKey?, result: NotificationActionResult, assistant: Boolean?, createdAt: String? = null) {
        val (title, detail) = actionResultCopy(result) ?: return
        resultNotice(id, session, context.getString(title), context.getString(detail), assistant,
            terminal = result == NotificationActionResult.Expired, createdAt = createdAt)
    }

    private fun resultNotice(
        id: String, session: SessionKey?, title: String, detail: String, assistant: Boolean?, terminal: Boolean = false,
        createdAt: String? = null,
    ) {
        val destination = open(session, assistant, terminal)
        post(interactionTag(id), EVENT_ID, base(CH_INPUT).forSession(session)
            .addExtras(android.os.Bundle().apply {
                if (createdAt != null) {
                    putString(ActionReceiver.EXTRA_CREATED_AT, createdAt)
                    putBoolean(EXTRA_OCCURRENCE_GUARD, true)
                }
            })
            .setContentTitle(title).setContentText(detail)
            .setOnlyAlertOnce(true)
            .apply {
                if (terminal && session?.sessionId?.isNotBlank() == true) addAction(0, context.getString(R.string.notify_open_terminal), destination)
            }
            .setAutoCancel(true).setContentIntent(destination).build())
    }

    fun userStopped() {
        val intent = Intent(context, ActionReceiver::class.java).setAction(ActionReceiver.RESUME)
        val pending = PendingIntent.getBroadcast(context, 0, intent, PendingIntent.FLAG_IMMUTABLE)
        post("stopped", EVENT_ID, base(CH_STATUS)
            .setContentTitle(context.getString(R.string.notify_stopped_title)).setContentText(context.getString(R.string.notify_stopped_text))
            .addAction(NotificationCompat.Action.Builder(0, context.getString(R.string.notify_resume), pending).setAuthenticationRequired(true).build())
            .setAutoCancel(true).setContentIntent(open(null)).build())
    }

    fun clear() {
        requestHistory.clear()
        context.getSystemService(NotificationManager::class.java).activeNotifications
            .filter { it.id == EVENT_ID && it.tag?.let(NotificationPolicy::cleared) == true }
            .forEach { manager.cancel(it.tag, it.id) }
    }

    fun dismissStopped() = manager.cancel("stopped", EVENT_ID)

    private fun base(channel: String, lockDetails: Boolean = showLockDetails) = NotificationCompat.Builder(context, channel)
        .setSmallIcon(persona.core.designsystem.R.drawable.ic_stat)
        .setVisibility(if (lockDetails) NotificationCompat.VISIBILITY_PUBLIC else NotificationCompat.VISIBILITY_PRIVATE)
        .apply {
            if (!lockDetails) setPublicVersion(NotificationCompat.Builder(context, channel)
                .setSmallIcon(persona.core.designsystem.R.drawable.ic_stat).setContentTitle(context.getString(R.string.notify_public_title))
                .setVisibility(NotificationCompat.VISIBILITY_PUBLIC).build())
        }

    @Synchronized
    private fun post(tag: String?, id: Int, notification: Notification) {
        if (Build.VERSION.SDK_INT >= 33 && ContextCompat.checkSelfPermission(context, Manifest.permission.POST_NOTIFICATIONS)
            != PackageManager.PERMISSION_GRANTED) return
        val occurrence = notification.extras.getString(ActionReceiver.EXTRA_CREATED_AT)
        if (occurrence != null && notification.extras.getBoolean(EXTRA_OCCURRENCE_GUARD) && tag != null) {
            // A result for the occurrence the owner tapped must not overwrite a replacement already on the shade.
            val current = runCatching {
                context.getSystemService(NotificationManager::class.java).activeNotifications
                    .firstOrNull { it.id == id && it.tag == tag }
            }.getOrNull()
            val currentAt = current?.notification?.extras?.getString(ActionReceiver.EXTRA_CREATED_AT)
            if (currentAt != null && currentAt != occurrence) return
        }
        runCatching { manager.notify(tag, id, notification) }.onSuccess {
            if (notification.extras.getBoolean(EXTRA_REQUEST) && tag?.startsWith(INTERACTION_PREFIX) == true) {
                requestHistory.mark(tag.removePrefix(INTERACTION_PREFIX), occurrence)
            }
        }
    }

    companion object {
        const val CH_INPUT = "input"
        const val CH_ALERT = "alert"
        const val CH_DONE = "done"
        const val CH_ASSISTANT = "assistant"
        const val CH_STATUS = "realtime_status"
        const val ONGOING_ID = 1
        private const val EVENT_ID = 2
        private const val INTERACTION_PREFIX = "interaction:"
        private fun interactionTag(id: String) = "$INTERACTION_PREFIX$id"
        private const val EXTRA_REQUEST = "persona.request"
        private const val EXTRA_OCCURRENCE_GUARD = "persona.occurrence_guard"
    }
}
