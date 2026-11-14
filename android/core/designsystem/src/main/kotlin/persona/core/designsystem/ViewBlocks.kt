package persona.core.designsystem

import androidx.compose.runtime.Composable
import androidx.compose.runtime.staticCompositionLocalOf

/**
 * Fenced blocks in an assistant's reply that are drawn as an extension's view (docs/extensions/extensions.md "Declarative UI", #1850):
 * ```persona-view (a view, or one component) and the older ```persona-card. [parse] reads a block (null: not one, or
 * not whole yet while it streams in, and it stays the code it is); [draw] draws what it read. The app provides them
 * (they are the extensions' renderer); without them every fence is code.
 */
class ViewBlocks(
    val parse: (language: String, code: String) -> Any?,
    val draw: @Composable (Any) -> Unit,
    /**
     * An assistant's ```persona-view block under its key (`<item id>/<n>`, docs/assistant/assistant-views.md "Card identity"), [raw]
     * its text, [complete] whether its closing fence came: a card, or its fallback when it is not one.
     */
    val block: @Composable (key: String, raw: String, complete: Boolean) -> Unit = { _, raw, complete ->
        if (complete) parse("persona-view", raw)?.let { draw(it) }
    },
    /** A live card of the conversation (a feed `view` item), by its id: drawn as the hub's latest version of it. */
    val live: @Composable (id: String) -> Unit = {},
    /**
     * A card block drawn into a picture (Share as a picture, #7261): as the chat draws it, with nothing that answers and
     * nothing that only runs live; one that cannot be drawn is its words, never its JSON. [language] is the fence's,
     * [index] a ```persona-view's number in the text (-1 for a ```persona-card).
     */
    val picture: @Composable (language: String, raw: String, complete: Boolean, index: Int) -> Unit = { language, raw, complete, _ ->
        if (complete) parse(language, raw)?.let { draw(it) }
    },
)

val LocalViewBlocks = staticCompositionLocalOf<ViewBlocks?> { null }

/**
 * The assistant conversation the cards drawn here belong to (its hub's store and actions); none outside one, where a
 * card only draws.
 */
val LocalAssistantCards = staticCompositionLocalOf<persona.core.domain.views.AssistantCardsHost?> { null }
