package persona.android.ui

import java.util.UUID
import persona.core.contracts.SessionKey
import persona.core.contracts.api.AssistantConversations
import persona.core.contracts.api.ManagerRoster
import persona.feature.chat.AssistDraftBus
import persona.feature.chat.AssistDraftQueue
import persona.feature.chat.AssistDraftTarget
import persona.feature.chat.AssistDraftTextMode

/** Where an extension's compose link goes: an existing main conversation, or the assistant's new-chat page. */
internal data class ExtensionComposeHandoff(val manager: String, val main: SessionKey?)

/**
 * Puts an extension's quick prompt in its owner's composer without creating a main conversation: the composer's own
 * draft editor persists it, whether that composer is already mounted or is opened by the caller next. Never sends it.
 */
internal suspend fun prepareExtensionCompose(
    conversations: AssistantConversations,
    text: String,
    manager: String?,
    defaultManager: String,
    bus: AssistDraftQueue = AssistDraftBus,
): ExtensionComposeHandoff {
    val owner = manager?.takeIf { it.isNotBlank() }
        ?: defaultManager.takeIf { it.isNotBlank() }
        ?: ManagerRoster.ASSISTANT_MANAGER
    val main = conversations.mainConversationOrNull(owner)
    val target = main?.let(AssistDraftTarget::Session) ?: AssistDraftTarget.Starter
    bus.publish(id = "extension-compose:${UUID.randomUUID()}", text = text, textMode = AssistDraftTextMode.Replace)
    bus.claim(target)
    return ExtensionComposeHandoff(owner, main)
}
