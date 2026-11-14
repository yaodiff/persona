package persona.feature.chat

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.Immutable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.drawWithContent
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.TransformOrigin
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.graphics.layer.GraphicsLayer
import androidx.compose.ui.graphics.layer.drawLayer
import androidx.compose.ui.graphics.rememberGraphicsLayer
import androidx.compose.ui.layout.Layout
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.rememberTextMeasurer
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Constraints
import androidx.compose.ui.unit.dp
import persona.core.contracts.Session
import persona.core.designsystem.AgentMarkSpec
import persona.core.designsystem.agentMark
import persona.core.domain.sort.slotLabel
import persona.core.domain.views.AssistantFences
import persona.core.designsystem.ContentTokens
import persona.core.designsystem.FeedbackNotice
import persona.core.designsystem.FeedbackStyle
import persona.core.designsystem.JAgentMark
import persona.core.designsystem.JAvatar
import persona.core.designsystem.JBottomBar
import persona.core.designsystem.JIcon
import persona.core.designsystem.JIconButton
import persona.core.designsystem.JPage
import persona.core.designsystem.JSegment
import persona.core.designsystem.JSegmented
import persona.core.designsystem.JToastHost
import persona.core.designsystem.JFullScreen
import persona.core.designsystem.LocalizedFormats
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import persona.core.designsystem.currentLocale
import persona.core.designsystem.icons.PersonaIcons
import persona.core.designsystem.rememberToastState
import persona.feature.chat.media.MediaShare
import java.io.ByteArrayOutputStream
import java.time.Instant
import kotlin.math.max
import kotlin.math.min
import kotlin.math.roundToInt
import kotlin.math.sqrt
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

/**
 * A message to share as a picture (iOS `ShareMessage`, Share in the message menu): its words, whether they are yours,
 * where it was said ("space › tab"), when, and the agent who answers there ([agent], drawn with [mark]; none for Persona's
 * own conversations).
 */
@Immutable
internal data class MessagePicture(
    val text: String,
    val user: Boolean,
    val source: String = "",
    val time: Instant? = null,
    val agent: String = "Claude",
    val mark: AgentMarkSpec? = null,
) {
    /** The words the card draws: a very long answer is cut (a picture of ten screens helps nobody). */
    val cardText: String get() = shareCardText(text)
}

/** Who answers in a conversation and where it is, for its messages' pictures (iOS `MessageMenu.agent` / `source`). */
@Immutable
internal data class ShareCardSource(val agent: String, val mark: AgentMarkSpec?, val source: String)

/**
 * Who answers in [session] and where it is (iOS `menu.agent` / `menu.source`): its agent and "space › tab", or [persona]
 * in Persona's own conversations.
 */
internal fun shareCardSource(session: Session?, persona: String): ShareCardSource =
    if (session?.assistant == true) ShareCardSource(persona, null, "")
    else ShareCardSource(session?.agentLabel?.takeIf(String::isNotBlank) ?: "Claude", session?.agentMark,
        listOfNotNull(session?.spaceName, session?.slotLabel).filter(String::isNotBlank).joinToString(" › "))

/** The most of a message a card draws (iOS `MessageShareCard.maxChars`). */
internal const val SHARE_CARD_MAX_CHARS = 3000

/** [text] trimmed, and past [SHARE_CARD_MAX_CHARS] cut with "…" after a blank line, never inside a card (#7261). */
internal fun shareCardText(text: String): String {
    val trimmed = text.trim()
    if (trimmed.length <= SHARE_CARD_MAX_CHARS) return trimmed
    // A card the cut would split goes whole: half its JSON is no card, only its source.
    val split = if (AssistantFences.mentions(trimmed)) AssistantFences.blocks(trimmed).firstOrNull { SHARE_CARD_MAX_CHARS in it.start + 1 until it.end } else null
    return (if (split == null) trimmed.take(SHARE_CARD_MAX_CHARS) else trimmed.take(split.start).trimEnd()) + "\n\n…"
}

/**
 * How wide a message's card is drawn, in dp (iOS `ShareCardWidth`). Plain text keeps a phone's width; tables, code
 * blocks and their long lines widen it to their own width, up to [CAP], past which they wrap. A card that would still
 * be a thin strip, taller than [TALLEST] times its width, widens too, also up to [CAP], its words keeping about the
 * same area.
 */
internal object ShareCardWidth {
    const val BASE = 390f
    const val CAP = 520f
    const val TALLEST = 2.6f

    /** [natural]: the widest table or code block at its own width (0 for none); [height]: the card's height at [BASE]; [chrome]: around the words. */
    fun width(natural: Float, height: Float, chrome: Float): Float {
        var width = BASE
        if (natural > 0) width = max(width, natural + chrome)
        if (height > TALLEST * BASE) {
            // height × the text's width stays about the same as it widens.
            val area = height * (BASE - chrome)
            val fit = (TALLEST * chrome + sqrt(TALLEST * TALLEST * chrome * chrome + 4 * TALLEST * area)) / (2 * TALLEST)
            width = max(width, fit)
        }
        return min(width.roundToInt().toFloat(), CAP)
    }
}

/**
 * The parts of a message that want width (iOS `ShareCardBlocks`): its fenced code blocks and its tables (a row of pipes
 * over a delimiter row), each as the Markdown that draws it alone.
 */
internal object ShareCardBlocks {
    fun wide(text: String): List<String> {
        val lines = text.split("\n")
        val out = mutableListOf<String>()
        var i = 0
        while (i < lines.size) {
            val trimmed = lines[i].trim()
            val fence = fenceOf(trimmed)
            if (fence != null) {
                var j = i + 1
                while (j < lines.size && !closes(fence, lines[j].trim())) j++
                // A card is drawn as a card, as wide as the picture: its JSON never widens it (#7261).
                if (!isCard(trimmed)) out += lines.subList(i, min(j, lines.size - 1) + 1).joinToString("\n")
                i = j + 1
            } else if ('|' in trimmed && i + 1 < lines.size && isDelimiter(lines[i + 1])) {
                var j = i + 2
                while (j < lines.size && '|' in lines[j] && lines[j].isNotBlank()) j++
                out += lines.subList(i, j).joinToString("\n")
                i = j
            } else i++
        }
        return out
    }

    /** A fence line that opens a card: its language (the info's first word) is persona-view or persona-card. */
    fun isCard(fenceLine: String): Boolean {
        val language = fenceLine.trimStart('`', '~').trim().split(' ', '\t').first().lowercase()
        return language == AssistantFences.View || language == AssistantFences.Card
    }

    /** `| --- | :-: |`, `---|---`: a table's second line (with a pipe: a lone `---` is a rule). */
    fun isDelimiter(line: String): Boolean {
        val cells = line.trim().split('|').filter(String::isNotEmpty).map(String::trim)
        if ('|' !in line || cells.isEmpty()) return false
        return cells.all { cell -> cell.trim(':').let { core -> core.isNotEmpty() && core.all { it == '-' } } }
    }

    /** The fence a line opens (its character and run), or null; a backtick run with another backtick after it is inline code. */
    private fun fenceOf(line: String): Pair<Char, Int>? {
        if (!line.startsWith("```") && !line.startsWith("~~~")) return null
        val c = line.first()
        val count = line.takeWhile { it == c }.length
        if (c == '`' && '`' in line.drop(count)) return null
        return c to count
    }

    private fun closes(fence: Pair<Char, Int>, line: String): Boolean {
        val run = line.takeWhile { it == fence.first }.length
        return run >= fence.second && line.drop(run).all { it == ' ' || it == '\t' }
    }

    /** Whether [block] is a fenced code block (else a table). */
    fun fenced(block: String): Boolean = fenceOf(block.lineSequence().first().trim()) != null
}

/** Around the card's words, in dp: its margin and its padding on each side ([MessageShareCard]). */
private val shareCardChrome: Float get() = 2 * (Tokens.Space.xl.value + Tokens.Space.xl.value)

/**
 * The message laid out as a card (iOS `MessageShareCard`): who said it and where, its time, the words and a quiet Persona
 * mark, on a soft gradient. It draws in [dark] or light whatever the app's appearance; [modifier] sets its width.
 */
@Composable
internal fun MessageShareCard(message: MessagePicture, dark: Boolean, modifier: Modifier = Modifier) {
    PersonaTheme(darkTheme = dark, reduceMotion = true) {
        val palette = PersonaTheme.palette
        val type = PersonaTheme.type
        val locale = currentLocale
        val base = if (dark) Tokens.System.Dark.page else Tokens.System.Light.page
        val card = if (dark) Tokens.System.Dark.card else Tokens.System.Light.page
        val hues = if (dark) listOf(Tokens.Common.Dark.Hue.blue, Tokens.Common.Dark.Hue.purple)
            else listOf(Tokens.Common.Light.Hue.blue, Tokens.Common.Light.Hue.purple)
        val wash = if (dark) DARK_WASH else LIGHT_WASH
        val time = remember(message.time, locale) { message.time?.let { LocalizedFormats.format(it, LocalizedFormats.MONTH_DAY_TIME, locale) } }
        Box(modifier.background(base).background(Brush.linearGradient(hues.map { it.copy(alpha = wash) })).padding(Tokens.Space.xl)) {
            Column(Modifier.fillMaxWidth().clip(RoundedCornerShape(Tokens.Radius.panel)).background(card).padding(Tokens.Space.xl),
                verticalArrangement = Arrangement.spacedBy(Tokens.Space.l)) {
                Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
                    ShareCardAvatar(message)
                    Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xxs)) {
                        Text(if (message.user) stringResource(R.string.chat_share_card_me) else message.agent, maxLines = 1, overflow = TextOverflow.Ellipsis,
                            style = type.callout.copy(fontWeight = FontWeight.SemiBold), color = palette.ink)
                        if (message.source.isNotEmpty()) Text(message.source, maxLines = 1, overflow = TextOverflow.Ellipsis,
                            style = type.micro, color = palette.ink2)
                    }
                    time?.let { Text(it, maxLines = 1, style = type.micro, color = palette.ink2) }
                }
                CompositionLocalProvider(LocalInPicture provides true) {
                    ChatMarkdown(message.cardText, selectable = false, bodyStyle = type.callout, color = palette.ink)
                }
                Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.sm)) {
                    JIcon(PersonaIcons.ai, null, size = Tokens.Size.markXS, tint = palette.ink3)
                    Text(stringResource(R.string.chat_share_card_brand), style = type.micro.copy(fontWeight = FontWeight.SemiBold), color = palette.ink3)
                }
            }
        }
    }
}

/** How strongly the gradient takes its two hues over the page (iOS's pale blue to lilac, and their night colors). */
private const val LIGHT_WASH = 0.16f
private const val DARK_WASH = 0.25f

/** Who wrote it: you, the agent's own mark in a well, or Persona's mark in its own conversations. */
@Composable
private fun ShareCardAvatar(message: MessagePicture) {
    val mark = message.mark
    when {
        message.user -> JAvatar("", size = ContentTokens.smallAvatar, icon = PersonaIcons.user)
        mark != null -> Box(Modifier.size(ContentTokens.smallAvatar).clip(CircleShape).background(PersonaTheme.palette.surfaceStrong),
            contentAlignment = Alignment.Center) { JAgentMark(mark, size = Tokens.Size.markS) }
        else -> JAvatar("", size = ContentTokens.smallAvatar, icon = PersonaIcons.persona)
    }
}

/**
 * The widest of [text]'s tables and code blocks drawn at their own width, in dp (iOS `ShareCardWidth.natural`): a code
 * block's longest line in the code type, a table's longest row with its cells' padding.
 */
@Composable
private fun rememberShareCardNatural(text: String): Float {
    val measurer = rememberTextMeasurer()
    val type = PersonaTheme.type
    val density = LocalDensity.current
    return remember(text, type, density) {
        ShareCardBlocks.wide(text).maxOfOrNull { block ->
            val lines = block.lines()
            val px = if (ShareCardBlocks.fenced(block)) {
                lines.drop(1).dropLast(1).maxOfOrNull { measurer.measure(it, type.code, softWrap = false, maxLines = 1).size.width } ?: 0
            } else {
                lines.filterNot(ShareCardBlocks::isDelimiter).maxOfOrNull { row ->
                    val cells = row.trim().trim('|').split('|').map(String::trim)
                    measurer.measure(cells.joinToString(" "), type.callout, softWrap = false, maxLines = 1).size.width +
                        with(density) { (Tokens.Space.m * 2 * cells.size).roundToPx() }
                } ?: 0
            }
            with(density) { px.toDp().value } + 2 * Tokens.Space.m.value
        } ?: 0f
    }
}

/**
 * The card at the width its content wants ([ShareCardWidth], measured at [ShareCardWidth.BASE] first), scaled down to
 * fit where it is shown. [layer] records it at its own size: that recording is the picture that goes.
 */
@Composable
internal fun ShareCardPicture(message: MessagePicture, dark: Boolean, layer: GraphicsLayer?, modifier: Modifier = Modifier) {
    val natural = rememberShareCardNatural(message.cardText)
    val record = if (layer == null) Modifier else Modifier.drawWithContent {
        layer.record { this@drawWithContent.drawContent() }
        drawLayer(layer)
    }
    // The first card only measures (it is never placed); accessibility reads the one drawn.
    Layout(contents = listOf({ MessageShareCard(message, dark, Modifier.clearAndSetSemantics {}) },
        { MessageShareCard(message, dark, record.testTag("share-card")) }),
        modifier) { (probe, card), constraints ->
        val base = ShareCardWidth.BASE.dp.roundToPx()
        val height = probe.first().measure(Constraints(minWidth = base, maxWidth = base)).height
        val width = ShareCardWidth.width(natural, height.toDp().value, shareCardChrome).dp.roundToPx()
        val placeable = card.first().measure(Constraints(minWidth = width, maxWidth = width))
        val scale = if (constraints.hasBoundedWidth) min(1f, constraints.maxWidth.toFloat() / width) else 1f
        layout((width * scale).roundToInt(), (placeable.height * scale).roundToInt()) {
            placeable.placeWithLayer(0, 0) {
                scaleX = scale
                scaleY = scale
                transformOrigin = TransformOrigin(0f, 0f)
            }
        }
    }
}

/** A recorded card as PNG bytes. Call off the main thread. */
internal fun shareCardPng(picture: Bitmap): ByteArray =
    ByteArrayOutputStream().also { picture.compress(Bitmap.CompressFormat.PNG, 100, it) }.toByteArray()

/**
 * A message's picture before it goes (iOS `MessageShareSheet`), full screen: Light / Dark in the top bar (the app's
 * appearance at first), the card (it scrolls when tall), and Share (the picture), Save, Copy and Share Text along the bottom.
 * Each action takes the card as drawn; what it did shows on this page, which stays open.
 */
@Composable
internal fun MessageSharePage(message: MessagePicture, onDismiss: () -> Unit) {
    JFullScreen(onDismiss) { MessageShareContent(message, onDismiss) }
}

/** [MessageSharePage] inside its window; [layer] records the card (a test reads the picture from it). */
@Composable
internal fun MessageShareContent(message: MessagePicture, onDismiss: () -> Unit, layer: GraphicsLayer = rememberGraphicsLayer()) {
    val appDark = PersonaTheme.darkTheme
    var chosen by rememberSaveable { mutableStateOf<Boolean?>(null) }
    val dark = chosen ?: appDark
    val toast = rememberToastState()
    val scope = rememberCoroutineScope()
    val context = LocalContext.current
    val resources = LocalResources.current
    var busy by remember { mutableStateOf(false) }
    fun done(ok: Boolean, success: Int, failure: Int): FeedbackNotice =
        if (ok) FeedbackNotice(resources.getString(success), style = FeedbackStyle.Success)
        else FeedbackNotice(resources.getString(failure), style = FeedbackStyle.Error)
    fun failed(failure: Int) = FeedbackNotice(resources.getString(failure), style = FeedbackStyle.Error)
    fun run(action: suspend (ByteArray) -> FeedbackNotice?) {
        if (busy) return
        busy = true
        scope.launch {
            try {
                val picture = layer.toImageBitmap().asAndroidBitmap()
                val png = withContext(Dispatchers.Default) { shareCardPng(picture) }
                action(png)?.let(toast::show)
            } catch (e: CancellationException) {
                throw e
            } catch (_: Exception) {
                toast.show(failed(R.string.chat_viewer_share_failed))
            } finally {
                busy = false
            }
        }
    }
    Box(Modifier.fillMaxSize()) {
        // Light / Dark in the bar, as iOS puts them in its top bar.
        JPage(stringResource(R.string.chat_share_card_title), onBack = onDismiss, actions = {
            JSegmented(listOf(JSegment(stringResource(R.string.chat_share_card_light)), JSegment(stringResource(R.string.chat_share_card_dark))),
                selected = if (dark) 1 else 0, onSelect = { chosen = it == 1 })
        }, bottomBar = {
            JBottomBar {
                Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween, verticalAlignment = Alignment.CenterVertically) {
                    JIconButton(PersonaIcons.share, stringResource(R.string.chat_viewer_share), {
                        run { if (MediaShare.shareImage(context, it)) null else failed(R.string.chat_viewer_share_failed) }
                    }, enabled = !busy, raised = false)
                    Row {
                        JIconButton(PersonaIcons.down, stringResource(R.string.chat_viewer_save), {
                            run { done(MediaShare.saveImage(context, it), R.string.chat_viewer_saved, R.string.chat_viewer_save_failed) }
                        }, enabled = !busy, raised = false)
                        JIconButton(PersonaIcons.copy, stringResource(R.string.chat_viewer_copy), {
                            run { done(MediaShare.copyImage(context, it), R.string.chat_viewer_copied, R.string.chat_viewer_copy_failed) }
                        }, enabled = !busy, raised = false)
                    }
                    JIconButton(PersonaIcons.fileText, stringResource(R.string.chat_share_card_share_text), {
                        if (!shareText(context, message.text)) toast.show(failed(R.string.chat_menu_no_share))
                    }, raised = false)
                }
            }
        }) { padding ->
            Column(Modifier.fillMaxSize().verticalScroll(rememberScrollState()).padding(padding)
                .padding(start = Tokens.Space.xl, end = Tokens.Space.xl, top = Tokens.Space.xxl, bottom = Tokens.Space.l), horizontalAlignment = Alignment.CenterHorizontally) {
                ShareCardPicture(message, dark, layer, Modifier.clip(RoundedCornerShape(Tokens.Radius.card)))
            }
        }
        JToastHost(toast, Modifier.align(Alignment.TopCenter))
    }
}
