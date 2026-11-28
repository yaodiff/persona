package persona.feature.chat

import android.content.res.Resources
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.MutableIntState
import androidx.compose.runtime.MutableState
import androidx.compose.runtime.Stable
import androidx.compose.runtime.State
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import persona.core.contracts.Interaction
import persona.core.contracts.Resolution
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.AssistantInboxDraft
import persona.core.contracts.api.ChatComposerService
import persona.core.contracts.api.InteractionCenter
import persona.core.contracts.api.MessageSender
import persona.core.contracts.api.OutboxMessage
import persona.core.contracts.api.ResolveOutcome
import persona.core.contracts.api.SessionsRepository
import persona.core.designsystem.AppFeedback
import persona.core.designsystem.Haptic
import persona.core.designsystem.operationFeedback
import persona.core.domain.ChatTaps

/** The page's own message-delivery and request-answer operations, sharing its existing state objects. */
@Stable
internal class ChatInteractionController(
    private val key: SessionKey,
    private val sender: MessageSender,
    private val interactions: InteractionCenter,
    private val sessions: SessionsRepository,
    private val composer: ChatComposerState,
    private val service: ChatComposerService?,
    private val scope: CoroutineScope,
    private val resources: Resources,
    private val feedback: AppFeedback?,
    private val haptics: (Haptic) -> Unit,
    private val taps: MutableState<ChatTaps>,
    private val interactionFeedback: MutableState<String?>,
    private val answeredHere: MutableIntState,
    private val offerNext: MutableState<Boolean>,
    private val trayApproving: MutableState<Boolean>,
    private val traySending: MutableState<Boolean>,
    private val canSend: State<Boolean>,
    private val readOnly: State<Boolean>,
    private val statusSheet: MutableState<Boolean>,
    private val delivery: ChatDeliveryActions,
    private val clearingPane: MutableState<String?>,
    private val drawerSwitch: MutableState<DrawnSwitch?>,
    private val switchSession: Session?,
    private val tasks: persona.core.domain.tasks.TasksStore?,
    private val timelineFollow: MutableState<(() -> Unit)?>,
    private val submittedContexts: MutableState<List<ChatLinkContext>>,
) {
    val notice = mutableStateOf<String?>(null)
    val commandInputErrors = androidx.compose.runtime.mutableStateMapOf<String, String>()
    val sending = mutableStateOf(false)
    val sendingId = mutableStateOf<String?>(null)
    val submittedDraft = mutableStateOf<Pair<String, ChatComposerDraft>?>(null)

    private fun notify(message: String) {
        notice.value = message
    }

    fun removed(message: OutboxMessage) {
        val deleted = resources.getString(R.string.chat_delivery_deleted)
        val undo = persona.core.designsystem.FeedbackAction(resources.getString(R.string.chat_delivery_undo)) { sender.restore(message) }
        if (feedback?.notice(deleted, undo) != true) notify(deleted)
    }

    fun deliveryAction(message: OutboxMessage, action: DeliveryAction) {
        if (readOnly.value) return
        if (!message.state.allows(action, canSend.value)) return
        scope.launch {
            try {
                if (!delivery.perform(message.clientMessageId, action, composer::restoreMessage, ::removed))
                    notify(resources.getString(R.string.chat_screen_delivery_changed))
                else if (action == DeliveryAction.Discard) statusSheet.value = false
            } catch (cancelled: CancellationException) {
                throw cancelled
            } catch (_: Exception) {
                notify(resources.getString(R.string.chat_screen_delivery_failed))
            }
        }
    }

    private suspend fun resolve(interaction: Interaction, resolution: Resolution, handoffCard: Boolean = false): ResolveOutcome {
        val phrase = resolution.ownerApprovalPhrase ?:
            return interactions.resolveOccurrence(interaction, resolution, handoffCard)
        interactions.replyOwnerApproval(interaction, phrase)
        return ResolveOutcome()
    }

    fun answer(interaction: Interaction, resolution: Resolution) {
        if (!taps.value.canTap(interaction, interactions.pending.value, java.time.Instant.now())) return
        commandInputErrors.remove(interaction.id)
        taps.value = taps.value.tapped(interaction.id)
        scope.launch {
            try {
                val result = submitInteraction(keepUnconfirmedReason = interaction.isCommandInputPermission) { resolve(interaction, resolution,
                    handoffCard = (interaction.isSecretHandoff || interaction.secretRun?.delivery == "env") && resolution.allow == true) }
                result.submission.haptic?.let(haptics)
                taps.value = taps.value.answered(interaction.id, result.card)
                if (result.withdraw) {
                    interactionFeedback.value = result.notice(resources, interaction, resolution)
                    if (result.offersNext(resolution)) answeredHere.value++
                }
                val failure = if (result.withdraw) null else result.notice(resources, interaction, resolution)
                if (failure != null && interaction.isCommandInputPermission) {
                    commandInputErrors[interaction.id] = failure
                    notice.value = null
                } else {
                    notice.value = failure
                }
                if (result.submission == InteractionSubmission.PromptChanged || !result.withdraw) {
                    try {
                        interactions.refresh()
                    } catch (cancelled: CancellationException) {
                        throw cancelled
                    } catch (_: Exception) {}
                }
            } finally {
                taps.value = taps.value.idle()
            }
        }
    }

    fun discard(interaction: Interaction) {
        if (taps.value.busy) return
        taps.value = taps.value.tapped(interaction.id)
        scope.launch {
            try {
                interactions.discardLocal(interaction.id)
                taps.value = taps.value.discarded(interaction.id)
                interactionFeedback.value = resources.getString(R.string.chat_card_discarded_local)
                notice.value = null
            } catch (cancelled: CancellationException) {
                throw cancelled
            } catch (_: Exception) {
                notify(resources.getString(R.string.chat_card_discard_failed))
            } finally {
                taps.value = taps.value.idle()
            }
        }
    }

    fun approve(captured: List<Pair<Interaction, Resolution>>, taskAgentSessions: Map<SessionKey, Session>) {
        if (trayApproving.value || taps.value.busy || readOnly.value) return
        trayApproving.value = true
        scope.launch {
            try {
                for ((interaction, resolution) in captured) {
                    val current = interactions.pending.value
                    val sourceSession = sessions.sessions.value.firstOrNull { it.key == interaction.sessionKey }
                        ?: taskAgentSessions[persona.core.contracts.GroupKeys.rawKey(interaction.sessionKey)]
                    if (!taps.value.canTap(interaction, current, java.time.Instant.now()) ||
                        trayApproval(interaction, sourceSession) != resolution) continue
                    taps.value = taps.value.tapped(interaction.id)
                    try {
                        val result = submitInteraction { resolve(interaction, resolution) }
                        result.submission.haptic?.let(haptics)
                        taps.value = taps.value.answered(interaction.id, result.card)
                        if (!result.withdraw) notice.value = result.notice(resources, interaction, resolution)
                        // An approval from the tray answers a card here too: the next one waiting elsewhere is offered.
                        else if (result.offersNext(resolution)) answeredHere.value++
                    } finally {
                        taps.value = taps.value.idle()
                    }
                }
            } finally {
                trayApproving.value = false
            }
        }
    }

    /** A draft leaves the box only for a captured send, including a queued task route and launcher switch. */
    fun send(
        command: String? = null,
        draft: ChatComposerDraft,
        contexts: List<ChatLinkContext>,
        hasContent: Boolean,
        ready: Boolean,
        wireText: String,
        paneId: String?,
        conversationLauncher: String?,
        conversationModel: String?,
        glassAgent: Boolean,
        onFailed: () -> Unit = {},
        onCleared: (List<ChatLinkContext>) -> Unit,
    ): Boolean {
        if (readOnly.value || !canSend.value) return false
        if (sending.value || submittedDraft.value != null) return false
        if (command == null && ((!hasContent && contexts.isEmpty()) || !ready)) return false
        val applyingSwitch = command == null && switchesNext(switchSession, glassAgent)
        if (applyingSwitch && draft.attachments.isNotEmpty() &&
            switchSession?.let(persona.core.domain.models.AgentModelPanel::pendingEngine) != null) {
            notify(resources.getString(R.string.chat_engine_switch_text_only))
            return false
        }
        val refs = if (command == null) contexts else emptyList()
        val id = java.util.UUID.randomUUID().toString()
        if (command == null) {
            submittedDraft.value = id to draft
            submittedContexts.value = refs
        }
        if (command == SlashCommand.CLEAR) clearingPane.value = paneId
        sendingId.value = id
        sending.value = true
        timelineFollow.value?.invoke()
        scope.launch {
            try {
                // A task route is consumed by its accepted response; otherwise the draft clears as an ordinary send.
                val routed = command == null && tasks != null && refs.isEmpty() && draft.attachments.isEmpty() &&
                    tasks.sendRouted(key.sessionId, draft.text.trim())
                if (!routed) {
                    // The user's own message carries a launcher switch waiting on Glass.
                    if (applyingSwitch) {
                        sender.sendApplyingSwitch(key, wireText, id)
                        drawerSwitch.value = null
                    } else {
                        sender.send(key, wireText, id, null, conversationLauncher, conversationModel)
                    }
                }
                if (command == null) {
                    composer.clearIfUnchanged(draft)
                    onCleared(refs)
                    submittedDraft.value = null
                    submittedContexts.value = emptyList()
                }
            } catch (cancelled: CancellationException) {
                throw cancelled
            } catch (error: Exception) {
                submittedDraft.value = null
                submittedContexts.value = emptyList()
                if (command == SlashCommand.CLEAR) clearingPane.value = null
                onFailed()
                if (feedback?.failure(error) != true) notify(operationFeedback(error).text.resolve(resources))
            } finally {
                sending.value = false
                sendingId.value = null
            }
        }
        return true
    }

    fun sendTray(draft: TrayDraft, onSend: suspend (TrayDraft) -> Unit) {
        if (traySending.value || readOnly.value || !canSend.value) return
        traySending.value = true
        scope.launch {
            try {
                onSend(draft)
            } catch (cancelled: CancellationException) {
                throw cancelled
            } catch (error: Exception) {
                notify(operationFeedback(error).text.resolve(resources))
            } finally {
                traySending.value = false
            }
        }
    }
}

/** One controller per chat and per set of original MutableState identities. */
@Composable
internal fun rememberChatInteractionController(
    key: SessionKey,
    sender: MessageSender,
    interactions: InteractionCenter,
    sessions: SessionsRepository,
    composer: ChatComposerState,
    service: ChatComposerService?,
    scope: CoroutineScope,
    resources: Resources,
    feedback: AppFeedback?,
    haptics: (Haptic) -> Unit,
    requests: ChatPageRequests,
    trays: ChatPageTrays,
    canSend: State<Boolean>,
    readOnly: State<Boolean>,
    statusSheet: MutableState<Boolean>,
    deliveryActions: ChatDeliveryActions,
    clearingPane: MutableState<String?>,
    drawerSwitch: MutableState<DrawnSwitch?>,
    switchSession: Session?,
    tasks: persona.core.domain.tasks.TasksStore?,
    timelineFollow: MutableState<(() -> Unit)?>,
    submittedContexts: MutableState<List<ChatLinkContext>>,
): ChatInteractionController {
    val controller = remember(key, sender, interactions, sessions, composer, service, scope, resources, feedback, haptics,
        requests.taps, requests.interactionFeedback, requests.answeredHere, requests.offerNext,
        trays.trayApproving, trays.traySending, canSend, readOnly, statusSheet, deliveryActions,
        clearingPane, drawerSwitch, switchSession, tasks, timelineFollow, submittedContexts) {
        ChatInteractionController(key, sender, interactions, sessions, composer, service, scope, resources, feedback, haptics,
            requests.taps, requests.interactionFeedback, requests.answeredHere, requests.offerNext,
            trays.trayApproving, trays.traySending,
            canSend, readOnly, statusSheet, deliveryActions, clearingPane, drawerSwitch, switchSession, tasks, timelineFollow,
            submittedContexts)
    }
    LaunchedEffect(controller.notice.value) {
        controller.notice.value?.let { if (feedback?.notice(it) == true) controller.notice.value = null }
    }
    LaunchedEffect(requests.interactionFeedback.value) {
        if (requests.interactionFeedback.value != null) {
            delay(3_000)
            requests.interactionFeedback.value = null
        }
    }
    // As long as the web shows its "Answered" receipts (20 s); never moves on by itself.
    LaunchedEffect(requests.answeredHere.value) {
        if (requests.answeredHere.value > 0) {
            requests.offerNext.value = true
            delay(NEXT_WAITING_MILLIS)
            requests.offerNext.value = false
        }
    }
    return controller
}
