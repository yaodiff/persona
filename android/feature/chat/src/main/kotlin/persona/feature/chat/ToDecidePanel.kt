package persona.feature.chat

import androidx.compose.foundation.background
import androidx.compose.foundation.gestures.detectHorizontalDragGestures
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.produceState
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.platform.LocalLayoutDirection
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.LayoutDirection
import kotlinx.coroutines.delay
import persona.core.contracts.Machine
import persona.core.contracts.Interaction
import persona.core.contracts.Resolution
import persona.core.domain.answer.AnswerAction
import persona.core.domain.answer.AnswerBuilder
import persona.core.domain.answer.AnswerContext
import persona.core.domain.answer.AnswerDraft
import persona.core.domain.answer.RequestExpiry
import persona.core.designsystem.AssistantTrayEntry
import persona.core.designsystem.AssistantTrayTone
import persona.core.designsystem.DecisionDock
import persona.core.designsystem.DecisionKind
import persona.core.designsystem.JIconButton
import persona.core.designsystem.JSheet
import persona.core.designsystem.JTextButton
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import persona.core.designsystem.icons.PersonaIcons
import persona.core.designsystem.text
import java.time.Instant

@Composable
internal fun ToDecideEntry(ids: List<String>, state: ToDecideState, showing: Boolean, onShow: () -> Unit) {
    if (ids.isEmpty()) return
    val arrived = state.newSinceCollapse(ids)
    val title = if (arrived == 0) stringResource(R.string.chat_to_decide)
        else stringResource(R.string.chat_to_decide_arrived, arrived)
    AssistantTrayEntry(title, ids.size, AssistantTrayTone.Pending, showing,
        stringResource(if (showing) R.string.chat_tray_expanded else R.string.chat_tray_collapsed),
        { if (showing) state.collapse(ids) else onShow() }, Modifier.testTag("assistant-entry-to-decide"))
}

@Composable
@OptIn(ExperimentalLayoutApi::class)
internal fun ToDecidePanel(
    card: ToDecideCard, ids: List<String>, index: Int, state: ToDecideState, height: Dp,
    machine: Machine?, busy: Boolean, uncertain: Boolean, linkVisible: Boolean, blocked: String?,
    onViewSource: (() -> Unit)?, onAnswer: (Resolution) -> Unit,
    modifier: Modifier = Modifier,
    onOverlay: (Boolean) -> Unit = {}, suggestion: (@Composable (Interaction) -> Unit)? = null,
    onDiscardLocal: (() -> Unit)? = null,
) {
    val request = card.request
    var full by rememberSaveable(request.id) { mutableStateOf(false) }
    val overlayChanged by rememberUpdatedState(onOverlay)
    LaunchedEffect(LocalChatLinkOpening.current) { full = false }
    LaunchedEffect(full) { overlayChanged(full) }
    DisposableEffect(Unit) { onDispose { overlayChanged(false) } }
    val now by produceState(Instant.now(), request.id) { while (true) { value = Instant.now(); delay(1_000) } }
    val currentAnswer by rememberUpdatedState(onAnswer)
    val kind = when (request.kind) {
        "question" -> DecisionKind.Question
        "plan" -> DecisionKind.Plan
        "permission" -> if (request.toolName == "Bash") DecisionKind.Command else DecisionKind.Tool
        "screen" -> DecisionKind.Screen
        "elicitation" -> DecisionKind.Form
        else -> DecisionKind.Other
    }
    val direction = LocalLayoutDirection.current
    val select = { step: Int -> state.select(ids, index + step) }
    val noSkip = request.toDecideSkipReason()
    val requestBlock = resolveRequestApprovalBlockedReason(request, blocked)
    val canAnswer = requestBlock == null && !busy && !uncertain && !linkVisible && !RequestExpiry.expired(request.expiresAt, now)
    val skip = if (canAnswer && noSkip == null) AnswerBuilder.build(request, AnswerAction.CancelQuestion, AnswerDraft()).resolution else null
    val editor: @Composable (Boolean) -> Unit = { fullPage ->
        RequestEditor(request, machine, busy, uncertain, full = fullPage, linkVisible = linkVisible,
            blockedReason = requestBlock, onExpand = { full = true }, terminalHelp = false) { action, draft, read ->
            AnswerBuilder.build(request, action, draft, AnswerContext(fullPlanRead = read)).resolution?.let(currentAnswer)
        }
    }
    val actions: @Composable (() -> Unit) -> Unit = { collapse ->
        FlowRow(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
            if (onViewSource != null) JTextButton(stringResource(R.string.chat_to_decide_view_source), onViewSource)
            if (skip != null || noSkip != null || request.canCancel == true) JTextButton(
                stringResource(R.string.chat_to_decide_skip), { skip?.let(currentAnswer) }, enabled = skip != null,
            )
            if (onDiscardLocal != null && request.isLocalDiscard(requestBlock)) JTextButton(
                stringResource(R.string.chat_card_discard_local), onDiscardLocal, enabled = !busy && !linkVisible,
            )
            JTextButton(stringResource(R.string.chat_request_collapse_full), collapse)
        }
        noSkip?.let {
            Text(stringResource(when (it) {
                ToDecideSkipReason.Terminal -> R.string.chat_to_decide_skip_terminal
                ToDecideSkipReason.Permission -> R.string.chat_to_decide_skip_permission
                ToDecideSkipReason.Plan -> R.string.chat_to_decide_skip_plan
            }), style = PersonaTheme.type.caption, color = PersonaTheme.palette.cautionText)
        }
    }
    if (height < Tokens.Size.box150) Row(modifier.fillMaxWidth().padding(horizontal = Tokens.Space.m),
        verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
        Text(stringResource(R.string.chat_to_decide_position, index + 1, ids.size), style = PersonaTheme.type.caption,
            color = PersonaTheme.palette.ink2)
        JTextButton(stringResource(persona.core.designsystem.R.string.designsystem_decision_open_full), { full = true })
    } else Column(modifier.padding(horizontal = Tokens.Space.m).heightIn(max = height)
        .testTag("to-decide-panel").pointerInput(ids, index, direction) {
            if (ids.size < 2) return@pointerInput
            var distance = 0f
            detectHorizontalDragGestures(onDragStart = { distance = 0f }, onDragEnd = {
                val forward = if (direction == LayoutDirection.Ltr) -distance else distance
                val threshold = Tokens.MinTouch.android.toPx()
                when {
                    forward > threshold -> select(1)
                    forward < -threshold -> select(-1)
                }
            }) { _, delta -> distance += delta }
        }
    ) {
        if (ids.size > 1) ToDecidePosition(ids.size, index, select)
        key(request.id) {
            DecisionDock(kind, request.headline.text(), Modifier.weight(1f, fill = false), onExpand = { full = true },
                expires = RequestExpiry.label(request.expiresAt, now)?.text(),
                kicker = card.title?.takeIf(String::isNotBlank)) {
                actions { state.collapse(ids) }
                if (!full) editor(false)
            }
            if (!full) suggestion?.invoke(request)
        }
    }
    if (full) JSheet(onDismiss = { full = false }, title = request.headline.text()) {
        Column(Modifier.fillMaxHeight().imePadding()) {
            if (ids.size > 1) ToDecidePosition(ids.size, index, select)
            actions { full = false }
            editor(true)
        }
    }
}

@Composable
private fun ToDecidePosition(count: Int, index: Int, select: (Int) -> Unit) {
    Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs, Alignment.CenterHorizontally),
        verticalAlignment = Alignment.CenterVertically) {
        JIconButton(PersonaIcons.back, stringResource(R.string.chat_to_decide_previous), { select(-1) }, enabled = index > 0)
        Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xxs), verticalAlignment = Alignment.CenterVertically) {
            for (dot in toDecideDots(count, index)) Box(Modifier.size(Tokens.Space.xs).background(
                if (dot == index) PersonaTheme.palette.accent else PersonaTheme.palette.ink3, CircleShape,
            ))
        }
        Text(stringResource(R.string.chat_to_decide_position, index + 1, count), color = PersonaTheme.palette.ink2,
            style = PersonaTheme.type.caption, maxLines = 1)
        JIconButton(PersonaIcons.forward, stringResource(R.string.chat_to_decide_next), { select(1) }, enabled = index + 1 < count)
    }
}
