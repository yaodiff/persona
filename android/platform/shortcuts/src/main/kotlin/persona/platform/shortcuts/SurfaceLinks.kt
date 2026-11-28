package persona.platform.shortcuts

import android.content.Context
import android.content.Intent
import android.net.Uri
import persona.core.contracts.FleetItem
import persona.core.contracts.FleetState
import persona.core.contracts.Interaction
import persona.core.contracts.SessionKey
import persona.core.domain.fleet.FleetSummary
import persona.core.navigation.AppTab
import persona.core.navigation.AskPersonaLink
import persona.core.navigation.NewTodoLink
import persona.core.navigation.NotificationRoute

/**
 * Where the widget, the tile and the shortcuts take the user: `persona://open` links handled by the app's own activity
 * exactly like a notification's (the same Router), cold start or not.
 */
internal object SurfaceLinks {
    /** The app's launcher activity, made explicit: a shortcut's intent has to name the activity it starts. */
    private fun view(context: Context, uri: Uri): Intent = Intent(Intent.ACTION_VIEW, uri).apply {
        context.packageManager.getLaunchIntentForPackage(context.packageName)?.component?.let { component = it }
            ?: setPackage(context.packageName)
        addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_SINGLE_TOP)
    }

    /**
     * An agent's conversation; [assistant] picks the tab without waiting for session metadata when it is known, and
     * [request] opens it at that interaction (shown first in its request panel), as a needs-you notification does.
     */
    fun conversation(context: Context, key: SessionKey, assistant: Boolean?, request: String? = null): Intent =
        view(context, NotificationRoute.conversation(key.machineId, key.sessionId, assistant, request).uri())

    /**
     * The request "Approve pending" could not settle, in its conversation. A card outside any session (empty session id,
     * #4515) has none: it opens Agents with that card, as its notification does (#4808).
     */
    fun request(context: Context, request: Interaction, assistant: (SessionKey) -> Boolean?): Intent =
        if (request.sessionId.isBlank()) view(context, NotificationRoute.card(request.machineId, request.id).uri())
        else conversation(context, request.sessionKey, assistant(request.sessionKey), request.id)

    /** The fleet row's conversation, at its request when the row waits on the user. */
    fun row(context: Context, item: FleetItem, assistant: (SessionKey) -> Boolean?): Intent? =
        FleetSummary.key(item)?.let { conversation(context, it, assistant(it), item.interaction.takeIf { FleetSummary.needsYou(item) }) }

    fun agents(context: Context): Intent = view(context, NotificationRoute(AppTab.Agents).uri())

    /** The Persona tab (main conversation root). */
    fun persona(context: Context): Intent = view(context, NotificationRoute(AppTab.Persona).uri())

    /** Opens the main conversation directly when its key is known, otherwise the Persona tab. */
    fun mainChat(context: Context, mainSession: SessionKey?): Intent =
        mainSession?.let { conversation(context, it, assistant = true) } ?: persona(context)

    /** The Persona tab with the cursor in its composer ("Ask Persona"). */
    fun ask(context: Context): Intent = view(context, Uri.parse(AskPersonaLink))

    /** Opens a specific conversation with its composer focused and keyboard up. */
    fun askConversation(context: Context, key: SessionKey, assistant: Boolean?): Intent {
        val builder = Uri.Builder().scheme("persona").authority("open")
            .appendQueryParameter("target", "chat")
            .appendQueryParameter("machine", key.machineId)
            .appendQueryParameter("session", key.sessionId)
            .appendQueryParameter("focus", "composer")
        when (assistant) {
            true -> builder.appendQueryParameter("kind", "assistant")
            false -> builder.appendQueryParameter("kind", "agent")
            null -> Unit
        }
        return view(context, builder.build())
    }

    /** A tap on the Ask Persona widget: the main chat composer when paired, otherwise the pairing page. */
    fun askWidget(context: Context, paired: Boolean): Intent = if (paired) ask(context) else app(context)

    /** Agents › To-dos with "New to-do" open ("New to-do"). */
    fun newTodo(context: Context): Intent = view(context, Uri.parse(NewTodoLink))

    /** The app as the launcher opens it (the pairing page when not paired). */
    fun app(context: Context): Intent = context.packageManager.getLaunchIntentForPackage(context.packageName)
        ?.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK) ?: Intent().setPackage(context.packageName)

    /** A tap on the whole widget or the tile: the only agent when there is one, otherwise Agents. */
    fun fleet(context: Context, paired: Boolean, fleet: FleetState, assistant: (SessionKey) -> Boolean?): Intent = when {
        !paired -> app(context)
        else -> fleet.items.firstOrNull()?.takeIf { FleetSummary.single(fleet) != null }?.let { row(context, it, assistant) }
            ?: agents(context)
    }
}
