package persona.feature.chat

import android.content.res.Resources
import androidx.compose.animation.animateContentSize
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.IntrinsicSize
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.Stable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateMapOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.runtime.staticCompositionLocalOf
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.rotate
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.stateDescription
import androidx.compose.ui.semantics.testTag
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import persona.core.contracts.Subagent
import persona.core.designsystem.JIcon
import persona.core.designsystem.JSpinner
import persona.core.designsystem.JTextButton
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import persona.core.designsystem.icons.PersonaIcons
import persona.core.domain.lines.SubagentStatus
import persona.core.domain.lines.Subagents
import persona.core.domain.lines.ToolCall

/**
 * Which task cards are open, by their call, while the chat is (the web's `subagentCardKey`): a card that scrolls away
 * and back, or turns from running to done, stays as the reader left it.
 */
@Stable
internal class SubagentFolds {
    private val open = mutableStateMapOf<String, Boolean>()
    fun isOpen(call: String) = open[call] == true
    fun toggle(call: String) { open[call] = !isOpen(call) }
    fun open(call: String) { open[call] = true }
}

internal val LocalSubagentFolds = staticCompositionLocalOf<SubagentFolds?> { null }

/** A task card's words (the web's `taskStatusWord` and `subagentMeta`). */
internal object SubagentCardText {
    /** The status word, the same four on every task card. */
    fun status(res: Resources, status: SubagentStatus): String = res.getString(when (status) {
        SubagentStatus.Running -> R.string.chat_subagent_status_running
        SubagentStatus.Completed -> R.string.chat_subagent_status_completed
        SubagentStatus.Failed -> R.string.chat_subagent_status_failed
        SubagentStatus.Stopped -> R.string.chat_subagent_status_stopped
    })

    /**
     * The second line's pieces, in order: what ran it (Claude Code; none in a Persona conversation, whose reader does not
     * work with the tool), the model chip (empty: none), its tokens, its tool uses and how long it took ([took], none while
     * it runs: "Running for …" counts there instead).
     */
    fun meta(res: Resources, agent: Subagent?, took: Long?, assistant: Boolean): Meta = Meta(
        harness = if (assistant) null else res.getString(R.string.chat_subagent_harness_claude),
        model = persona.core.domain.modelShortName(agent?.model).ifEmpty { null },
        tokens = agent?.tokens?.takeIf { it > 0 }?.let { res.getString(R.string.chat_subagent_tokens, Subagents.tokens(it)) },
        uses = agent?.steps?.takeIf { it > 0 }?.let { res.getQuantityString(R.plurals.chat_subagent_tool_uses, it, it) },
        took = took?.let { workDuration(res, it) },
    )

    data class Meta(val harness: String?, val model: String?, val tokens: String?, val uses: String?, val took: String?)
}

/**
 * A subagent the agent launched (an Agent / Task call) as its task card, in Glass (#5045; `docs/design/DESIGN_SYSTEM.md`
 * section 4.7, "Task cards"; the web's `SubagentCard` on `TaskCard`): one full-width card where it was launched, running
 * or done, never a conversation of its own. Two lines: its avatar, "Subagent", its title and its status word; then what
 * ran it, the model chip, its tokens, its tool uses and how long it took, or "Running for …" each second. The head opens
 * it in place: what it was asked, its latest result (four lines until Show full result), its last tool and its output.
 * [live]: its exchange is the running one; [assistant]: a Persona conversation.
 */
@Composable
internal fun SubagentCard(call: ToolCall, live: Boolean, assistant: Boolean, modifier: Modifier = Modifier,
    statusOverride: SubagentStatus? = null, metaOverride: SubagentCardText.Meta? = null) {
    val links = LocalChatSubagents.current
    val agent = call.use.item.toolUseId?.let { links?.agents?.get(it) }
    val res = LocalResources.current
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    val status = statusOverride ?: Subagents.status(call, agent, live)
    val running = status == SubagentStatus.Running
    val folds = LocalSubagentFolds.current
    var openHere by rememberSaveable(call.id) { mutableStateOf(false) }
    val open = folds?.isOpen(call.id) ?: openHere
    val title = Subagents.title(call, agent) ?: stringResource(R.string.chat_subagent_untitled)
    val meta = metaOverride ?: SubagentCardText.meta(res, agent, Subagents.took(call, agent, status), assistant)
    val started = Subagents.started(call, agent)
    val now by rememberNow(1_000, enabled = running && started != null)
    val runningFor = if (running && started != null)
        stringResource(R.string.chat_subagent_running_for, workDuration(res, java.time.Duration.between(started, now).seconds.coerceAtLeast(1)))
        else null
    val shape = PersonaTheme.shapes.cardShape
    // The page under the card hides the work rail behind it; its avatar sits where the rail runs.
    Column(modifier.fillMaxWidth().padding(vertical = Tokens.Space.xs).clip(shape).background(palette.background)
        .background(palette.surface).border(Tokens.Size.lineMedium, palette.line, shape)
        .animateContentSize(PersonaTheme.motion.size())
        .padding(horizontal = Tokens.Space.m, vertical = Tokens.Space.s)
        .semantics { testTag = "chat.subagent-card" }) {
        val state = stringResource(if (open) R.string.chat_tasks_expanded else R.string.chat_tasks_collapsed)
        val action = stringResource(if (open) R.string.chat_subagent_collapse_details else R.string.chat_subagent_expand_details)
        Row(Modifier.fillMaxWidth().heightIn(min = Tokens.MinTouch.android).clickable(role = Role.Button, onClickLabel = action) {
                if (folds != null) folds.toggle(call.id) else openHere = !openHere
            }.semantics(mergeDescendants = true) { stateDescription = state },
            verticalAlignment = Alignment.Top, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            Box(Modifier.padding(top = Tokens.Space.xxs).size(Tokens.Size.iconXS).clip(CircleShape).background(palette.sunken),
                contentAlignment = Alignment.Center) {
                JIcon(toolCategory(call.name), null, size = Tokens.Space.m, tint = palette.ink2, decorative = true)
            }
            Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xxs)) {
                val titleStyle = type.callout.copy(fontWeight = FontWeight.Medium)
                // At the large text sizes the title gets a line of its own: beside the label and the status it was cut to a letter.
                if (LocalDensity.current.fontScale >= LargeText) {
                    Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                        Text(stringResource(R.string.chat_subagent_label), Modifier.weight(1f), style = type.micro, color = palette.ink2, maxLines = 1)
                        StatusWord(status, SubagentCardText.status(res, status))
                    }
                    Text(title, style = titleStyle, color = palette.ink, maxLines = 2, overflow = TextOverflow.Ellipsis)
                } else Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                    Text(stringResource(R.string.chat_subagent_label), style = type.micro, color = palette.ink2, maxLines = 1)
                    Text(title, Modifier.weight(1f), style = titleStyle, color = palette.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
                    StatusWord(status, SubagentCardText.status(res, status))
                }
                MetaLine(meta, runningFor)
            }
            JIcon(PersonaIcons.disclosure, null, Modifier.padding(top = Tokens.Space.xs).rotate(if (open) 90f else 0f),
                size = Tokens.Space.m, tint = palette.ink3)
        }
        if (open) Details(call, agent, status)
    }
}

/** The font scale from which a card's title takes a line of its own (Android's large text sizes). */
private const val LargeText = 1.5f

/** The status mark and word: a spinner while it runs, otherwise the outcome's mark; only a failure is red. */
@Composable
private fun StatusWord(status: SubagentStatus, word: String) {
    val palette = PersonaTheme.palette
    val color = if (status == SubagentStatus.Failed) palette.danger.text else palette.ink2
    Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
        when (status) {
            SubagentStatus.Running -> JSpinner(Modifier.size(Tokens.Space.Standard.m), color = palette.ink2, stroke = Tokens.Space.Standard.xxs)
            SubagentStatus.Failed -> JIcon(PersonaIcons.error, null, size = Tokens.Space.m, tint = color, decorative = true)
            SubagentStatus.Stopped -> JIcon(PersonaIcons.interrupt, null, size = Tokens.Space.m, tint = color, decorative = true)
            SubagentStatus.Completed -> JIcon(PersonaIcons.done, null, size = Tokens.Space.m, tint = color, decorative = true)
        }
        Text(word, style = PersonaTheme.type.micro, color = color, maxLines = 1)
    }
}

/** The second line: what ran it, the model chip, tokens, tool uses, the time; it wraps rather than cutting a piece. */
@OptIn(ExperimentalLayoutApi::class)
@Composable
private fun MetaLine(meta: SubagentCardText.Meta, runningFor: String?) {
    val palette = PersonaTheme.palette
    val style = PersonaTheme.type.micro
    val pieces = listOfNotNull(meta.harness, meta.tokens, meta.uses, meta.took, runningFor)
    if (pieces.isEmpty() && meta.model == null) return
    FlowRow(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xxs),
        itemVerticalAlignment = Alignment.CenterVertically) {
        meta.harness?.let { Text(it, style = style, color = palette.ink3, maxLines = 1) }
        // The model chip: the line's own size and colour, only a chip's fill and radius.
        meta.model?.let {
            Text(it, Modifier.clip(CircleShape).background(palette.sunken).padding(horizontal = Tokens.Space.sm, vertical = Tokens.Space.xxs),
                style = style, color = palette.ink3, maxLines = 1)
        }
        listOfNotNull(meta.tokens, meta.uses, meta.took, runningFor).forEach { Text(it, style = style, color = palette.ink3, maxLines = 1) }
    }
}

/** The opened card: what it was asked, its latest result, its last tool and its output, on a rail of their own. */
@Composable
private fun Details(call: ToolCall, agent: Subagent?, status: SubagentStatus) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    Row(Modifier.fillMaxWidth().padding(top = Tokens.Space.s).height(IntrinsicSize.Min).semantics { testTag = "chat.subagent-details" }) {
        Box(Modifier.width(Tokens.Space.xxs).fillMaxHeight().background(palette.line))
        Column(Modifier.weight(1f).padding(start = Tokens.Space.m), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
            Subagents.prompt(call).takeIf(String::isNotEmpty)?.let { Text(it, style = type.caption, color = palette.ink2) }
            SubagentResult(call, agent, status)
            agent?.lastTool?.name?.takeIf(String::isNotBlank)?.let {
                Text(stringResource(R.string.chat_subagent_last_tool, it), style = type.micro, color = palette.ink3)
            }
            Subagents.outputFile(call)?.let { Text(stringResource(R.string.chat_subagent_output_file, it), style = type.micro, color = palette.ink3) }
        }
    }
}

/**
 * Its latest result as plain text (the web's `SubagentResult`): four lines until "Show full result" when it is longer; a
 * report the hub cut loads whole in its place first ("Show full text").
 */
@Composable
private fun SubagentResult(call: ToolCall, agent: Subagent?, status: SubagentStatus) {
    val item = remember(call) { Subagents.resultItem(call) }
    var loaded by remember(item?.id) { mutableStateOf<String?>(null) }
    val text = remember(call, agent, status, loaded) {
        val said = Subagents.resultText(call, agent, status)
        if (loaded != null && item != null) Subagents.resultText(call.copy(results = call.results.map {
            if (it.id == item.id) it.copy(item = it.item.copy(text = loaded, fullSize = null)) else it
        }), agent, status) else said
    }
    if (text.isEmpty()) return
    var whole by remember(item?.id) { mutableStateOf(false) }
    var cut by remember(text) { mutableStateOf(false) }
    val palette = PersonaTheme.palette
    Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.xxs)) {
        Text(text, style = PersonaTheme.type.caption, color = palette.ink2, maxLines = if (whole) Int.MAX_VALUE else 4,
            overflow = TextOverflow.Ellipsis, onTextLayout = { if (!whole) cut = it.hasVisualOverflow })
        if (cut || whole) JTextButton(stringResource(if (whole) R.string.chat_subagent_hide_full_result else R.string.chat_subagent_show_full_result),
            { whole = !whole })
        if (loaded == null && item?.item?.fullSize != null) FullTextButton(item.item) { loaded = it }
    }
}
