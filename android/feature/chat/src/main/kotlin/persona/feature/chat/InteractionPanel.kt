package persona.feature.chat

import persona.core.designsystem.dense
import persona.core.designsystem.text
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.LiveRegionMode
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.liveRegion
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import persona.core.contracts.Session
import persona.core.domain.sort.canAnswerInteraction
import persona.core.contracts.CoreText
import persona.core.contracts.DialogOption
import persona.core.contracts.Interaction
import persona.core.contracts.Machine
import persona.core.contracts.Question
import persona.core.contracts.Resolution
import persona.core.contracts.ScreenDialog
import persona.core.contracts.parseTime
import persona.core.designsystem.*
import persona.core.designsystem.icons.PersonaIcons
import persona.core.designsystem.icons.PhIcons
import persona.core.domain.answer.*
import java.time.Duration
import java.time.Instant
import kotlinx.coroutines.delay

/**
 * A conversation's pending cards above its composer, which stays usable (iOS `PendingCards`, `DESIGN_SYSTEM.md` section 4.8):
 * each card whole in [DecisionStack] while they fit in [maxHeight], scrolling under "N pending" past it (or with the
 * [keyboard] up), permissions first; each folds on its own from its header ([folds]; [onFold] hears it), and opens to a
 * full page. [labels] go over a card that is not the conversation's own (its task); [first] leads in every order. A place
 * that holds one task's question and names its count in the label ("N more") passes `counted = false`.
 */
@Composable
internal fun InteractionPanel(
    requests: List<Interaction>, machine: Machine?, maxHeight: Dp, busyId: String?, uncertainIds: Set<String>,
    onResolve: (Interaction, Resolution) -> Unit, onTerminal: (() -> Unit)?, onOverlay: (Boolean) -> Unit,
    linkVisible: Boolean = false,
    folds: RequestFolds = rememberRequestFolds(),
    onFold: (Interaction, Boolean) -> Unit = { _, _ -> },
    /** Set when the session's source does not let the phone answer: everything is shown, nothing can be sent. */
    blockedReason: String? = null,
    answerSession: Session? = null,
    /** False for a session without a terminal (a Codex thread, an agent runtime's): no terminal to explain either. */
    terminalHelp: Boolean = true,
    onDiscardLocal: ((Interaction) -> Unit)? = null,
    modifier: Modifier = Modifier,
    keyboard: Boolean = false,
    labels: Map<String, StackLabel> = emptyMap(),
    first: String? = null,
    counted: Boolean = true,
    /** When non-null, a lone unfolded card shows a top-edge grabber allowing the user to resize its cap (#12306). */
    resize: CardResize? = null,
    /** Drawn under a card that is not folded: the rule a question a supervisor passed up suggests (#4929); null: nothing. */
    suggestion: (@Composable (Interaction) -> Unit)? = null,
    commandInputErrors: Map<String, String> = emptyMap(),
) {
    // The card open on its full page, by its id: cards come and go around it (answered on another device, expired).
    var full by rememberSaveable { mutableStateOf<String?>(null) }
    val ids = requests.map { it.id }
    LaunchedEffect(LocalChatLinkOpening.current) { full = null }
    LaunchedEffect(ids) { if (full != null && full !in ids) full = null }
    LaunchedEffect(full) { onOverlay(full != null) }
    DisposableEffect(Unit) { onDispose { onOverlay(false) } }
    if (requests.isEmpty()) return
    val now by produceState(Instant.now(), requests.mapNotNull { it.expiresAt }) {
        while (true) { value = Instant.now(); delay(1_000) }
    }
    fun blockFor(request: Interaction): String? =
        blockedReason.takeUnless { answerSession?.canAnswerInteraction(request) == true }
    val body: @Composable (Interaction, Boolean) -> Unit = { request, complete ->
        key(request.id, request.dialog?.rev, complete) {
            RequestEditor(request, machine, busyId != null, request.id in uncertainIds, complete,
                onTerminal = onTerminal, onExpand = { full = request.id }, linkVisible = linkVisible, blockedReason = blockFor(request),
                terminalHelp = terminalHelp, refusal = commandInputErrors[request.id]) { action, value, planRead ->
                AnswerBuilder.build(request, action, value, AnswerContext(fullPlanRead = planRead)).resolution?.let {
                    onResolve(request, it)
                }
            }
        }
    }
    val closeCard: @Composable (Interaction) -> Unit = { request ->
        val requestBlock = resolveRequestApprovalBlockedReason(request, blockFor(request))
        val stoppedWork = requestBlock != null && request.isManagerWorkCard
        val localOnly = request.isLocalDiscard(requestBlock)
        if ((localOnly && onDiscardLocal != null) || stoppedWork || (request.kind == "question" && request.canCancel == true)) {
            val canClose = busyId == null && !linkVisible && if (localOnly) onDiscardLocal != null else
                request.id !in uncertainIds && !RequestExpiry.expired(request.expiresAt, now)
            JIconButton(PhIcons.X, stringResource(if (localOnly) R.string.chat_card_discard_local else R.string.chat_question_close), {
                if (canClose) {
                    if (localOnly) onDiscardLocal?.invoke(request)
                    else if (!RequestExpiry.expired(request.expiresAt, Instant.now())) {
                        val action = if (stoppedWork) AnswerAction.Dismiss else AnswerAction.CancelQuestion
                        AnswerBuilder.build(request, action).resolution?.let { onResolve(request, it) }
                    }
                }
            }, enabled = canClose)
        }
    }
    DecisionStack(requests.size, maxHeight, modifier, keyboard = keyboard, keys = ids, counted = counted) { scrolling ->
        for (request in PendingStack.order(requests, scrolling, first)) key(request.id) {
            val folded = folds.isFolded(request.id)
            val headline = request.cardTitle()
            // A card with its line is at most the stack's height; its form scrolls inside it.
            Column(Modifier.heightIn(max = maxHeight).testTag("pending-card-${request.id}"), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                labels[request.id]?.let { StackLabelLine(it) }
                DecisionDock(
                    kind = when (request.kind) {
                        "permission" -> if (request.toolName == "Bash" || request.isSecretRun || request.isCommandInputPermission)
                            DecisionKind.Command else DecisionKind.Tool
                        "plan" -> DecisionKind.Plan
                        "question" -> DecisionKind.Question
                        "elicitation" -> DecisionKind.Form
                        "screen" -> DecisionKind.Screen
                        else -> DecisionKind.Other
                    },
                    headline = headline, expires = RequestExpiry.label(request.expiresAt, now)?.text(),
                    modifier = Modifier.weight(1f, fill = false),
                    collapsed = folded, onCollapsedChange = { fold -> folds.set(request.id, fold); onFold(request, fold) },
                    preview = request.foldPreview()?.text()?.takeUnless { headline.contains(it) }, onExpand = { full = request.id },
                    headerActions = { closeCard(request) },
                    // an approval says it waits for the user, in the chat as on the web and iOS (#4883)
                    kicker = when {
                        request.isCommandInputPermission -> stringResource(R.string.chat_command_input_kicker)
                        request.kind == "permission" && !request.isSecretRun -> stringResource(DecisionKind.Tool.labelRes)
                        else -> null
                    },
                    grabber = if (!folded && resize != null && resize.id == request.id) ({ CardGrabber(resize) }) else null,
                ) {
                    if (full != request.id) body(request, false)
                }
                if (!folded) suggestion?.invoke(request)
            }
        }
    }
    requests.firstOrNull { it.id == full }?.let { request ->
        JSheet(onDismiss = { full = null }, title = request.cardTitle()) {
            Column(Modifier.fillMaxHeight().imePadding()) {
                Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween) {
                    JTextButton(stringResource(R.string.chat_request_collapse_full), { full = null })
                    closeCard(request)
                }
                body(request, true)
            }
        }
    }
}

internal fun Interaction.isLocalDiscard(blocked: String?): Boolean =
    (blocked == null || !isManagerWorkCard) && (blocked != null || (kind == "question" && canCancel != true))

@Composable
private fun Interaction.cardTitle(): String = when {
    isCommandInputPermission -> stringResource(R.string.chat_command_input_waiting)
    dialog?.commandAsk != null -> stringResource(R.string.chat_command_ask_title, agentDisplayName ?: "Claude")
    else -> headline.text()
}

/** What a folded card says after its title: what it asks in its own words, else a plan's first line (iOS `previewText`). */
internal fun Interaction.foldPreview(): CoreText? = dialog?.commandAsk?.let { CoreText.plain(it.description ?: it.command) }
    ?: askWords() ?: plan?.lineSequence()
    ?.map { it.trim().trimStart('#', '-', '*', ' ').trim() }?.firstOrNull { it.isNotEmpty() }?.let(CoreText::plain)

/** The task a card comes from, over it (iOS `TaskQuestions`): its name, and how many more its tasks ask. */
@Composable
private fun StackLabelLine(label: StackLabel) {
    val palette = PersonaTheme.palette
    val tone = palette.needsYou.text
    Row(Modifier.fillMaxWidth().padding(horizontal = Tokens.Space.m + Tokens.Space.xs), verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
        JIcon(PersonaIcons.askFirst, null, size = Tokens.Size.markS, tint = tone)
        Text(label.title, Modifier.weight(1f), style = PersonaTheme.type.caption, color = tone, maxLines = 1, overflow = TextOverflow.Ellipsis)
        if (label.more > 0) Text(pluralStringResource(R.plurals.chat_stack_more, label.more, label.more),
            style = PersonaTheme.type.caption, color = tone, maxLines = 1)
    }
}

@Composable
internal fun InteractionForm(
    interaction: Interaction, machine: Machine?, draft: AnswerDraft, update: (AnswerDraft) -> Unit,
    busy: Boolean, uncertain: Boolean, full: Boolean,
    onAnswer: (AnswerAction, AnswerDraft, Boolean) -> Unit, onTerminal: (() -> Unit)?, onExpand: () -> Unit,
    items: List<persona.core.contracts.Item> = emptyList(),
    readOnly: Boolean = false,
    linkVisible: Boolean = false,
    blockedReason: String? = null,
    /** False for a session without a terminal: no "handle in terminal" note (there is no terminal to go to). */
    showTerminalHelp: Boolean = true,
    refusal: String? = null,
) {
    val scroll = rememberScrollState()
    var planRead by rememberSaveable(interaction.id, interaction.plan) { mutableStateOf(false) }
    val atPlanEnd by remember { derivedStateOf { scroll.value >= scroll.maxValue } }
    LaunchedEffect(full, atPlanEnd) {
        if (full) {
            withFrameNanos { }; withFrameNanos { }
            if (scroll.value >= scroll.maxValue) planRead = true
        }
    }
    val secret = interaction.secret == true
    val command = interaction.commandInput
    // Secrets (a terminal's password, a password card's) live only in this visible form, never in saved state or request drafts.
    var terminalHelp by remember { mutableStateOf(false) }
    val now by produceState(Instant.now(), interaction.expiresAt) {
        while (true) { value = Instant.now(); delay(1_000) }
    }
    val expired = RequestExpiry.expired(interaction.expiresAt, now)
    var password by rememberTransientSecret(interaction.id, clearWhen = expired || uncertain)
    var fieldValues by rememberTransient(interaction.id, clearWhen = expired || uncertain, empty = emptyMap<String, String>())
    // A stopped Agent cannot receive a message, but its manager can still settle explicit defer choices and dismiss
    // its own work card. Other blocked cards, including permissions, keep the original read-only behavior (#6839).
    val blocked = blockedReason != null
    val stoppedWork = blocked && interaction.isManagerWorkCard
    val unavailable = expired || uncertain || readOnly || linkVisible
    val context = AnswerContext(state = if (unavailable || (blocked && !stoppedWork)) "unknown" else "pending", sending = busy,
        fullPlanRead = planRead)
    val editable = !busy && !expired && !uncertain && !readOnly && !linkVisible && (!blocked || stoppedWork)
    fun act(action: AnswerAction, value: AnswerDraft = draft) {
        if (RequestExpiry.expired(interaction.expiresAt, Instant.now())) return
        // A stopped card's page turn is navigation only, as on Web. AnswerBuilder may resolve Advance when every
        // question is answered, so only the last page's Submit may produce that Resolution (#6839).
        if (stoppedWork && action == AnswerAction.Advance) {
            if (!editable || value.page >= interaction.questions.orEmpty().lastIndex) return
            update(value.copy(page = value.page + 1))
            return
        }
        if (stoppedWork && !interaction.stoppedWorkAnswerAllowed(action, value)) return
        val result = AnswerBuilder.build(interaction, action, value, context)
        if (!result.allowed || (stoppedWork && !interaction.stoppedWorkResultAllowed(result))) return
        if (result.resolution == null) update(result.draft)
        else { password = ""; fieldValues = emptyMap(); onAnswer(action, result.draft, planRead) }
    }
    fun enabled(action: AnswerAction): Boolean {
        if (stoppedWork && action == AnswerAction.Advance) {
            return editable && draft.page < interaction.questions.orEmpty().lastIndex
        }
        if (stoppedWork && !interaction.stoppedWorkAnswerAllowed(action, draft)) return false
        val result = AnswerBuilder.build(interaction, action, draft, context)
        return result.allowed && (!stoppedWork || interaction.stoppedWorkResultAllowed(result))
    }
    val questions = interaction.questions.orEmpty()
    val recommendations = interaction.recommendedLabels()
    val recommendationOnOptions = recommendations.isNotEmpty() && recommendations.all { it.isNotEmpty() }
    val question = questions.getOrNull(draft.page)
    val fallback = remember(interaction.screen, secret) {
        if (secret) null else ScreenAnswers.parse(interaction.screen.orEmpty())
    }
    val options = interaction.dialog?.options?.map {
        ParsedScreenOption(it.key, it.label, it.detail, when (it.kind) {
            "type_something" -> ScreenOptionKind.TypeSomething
            "chat" -> ScreenOptionKind.Chat
            null, "", "plain" -> ScreenOptionKind.Plain
            else -> ScreenOptionKind.Unknown
        })
    } ?: fallback?.options.orEmpty()
    val commandPair = interaction.dialog?.commandAllowDeny()
    val actions: @Composable () -> Unit = {
        when (interaction.kind) {
            // A password card (#1137, #1581): its password, Decline and Run, nothing else.
            "permission" -> if (interaction.isSecretRun) SecretRunActions(password, { password = it }, editable,
                canDecline = enabled(AnswerAction.Deny),
                canRun = AnswerBuilder.build(interaction, AnswerAction.Allow, AnswerDraft(password = password), context).allowed,
                onDecline = { act(AnswerAction.Deny) }, onRun = { act(AnswerAction.Allow, AnswerDraft(password = password)) },
                // Room between the dock's divider and the field's label.
                modifier = Modifier.padding(top = Tokens.Space.s))
            else if (interaction.isSecretHandoff) SecretHandoffActions(interaction,
                canDeny = enabled(AnswerAction.Deny), canAllow = enabled(AnswerAction.Allow), canAlways = enabled(AnswerAction.Always),
                onDeny = { act(AnswerAction.Deny) }, onAllow = { act(AnswerAction.Allow) }, onAlways = { act(AnswerAction.Always) },
                modifier = Modifier.padding(top = Tokens.Space.s))
            else if (command != null) DecisionButtons(
                primary = JAction(stringResource(R.string.chat_tray_send),
                    enabled = fieldValues["input"].orEmpty().isNotBlank() &&
                        AnswerBuilder.build(interaction, AnswerAction.Allow, draft.copy(values = fieldValues), context).allowed,
                ) { act(AnswerAction.Allow, draft.copy(values = fieldValues)) },
                secondary = JAction(stringResource(R.string.chat_command_input_not_now), enabled = enabled(AnswerAction.Deny)) {
                    act(AnswerAction.Deny)
                },
            )
            // A card with fields (#4641): its form, Deny and Fill In & Allow, no Always.
            else if (interaction.hasFieldsForm) PermissionFieldsActions(interaction.fields.orEmpty(), fieldValues, { fieldValues = it }, editable,
                canDeny = enabled(AnswerAction.Deny),
                canAllow = AnswerBuilder.build(interaction, AnswerAction.Allow, draft.copy(values = fieldValues), context).allowed,
                onDeny = { act(AnswerAction.Deny) }, onAllow = { act(AnswerAction.Allow, draft.copy(values = fieldValues)) },
                modifier = Modifier.padding(top = Tokens.Space.s)) else {
                // "Always" is Claude's, or another agent's when its TUI says what it would allow (as on iOS).
                DecisionButtons(
                    primary = JAction(stringResource(R.string.chat_request_allow), enabled = enabled(AnswerAction.Allow)) { act(AnswerAction.Allow) },
                    secondary = JAction(stringResource(R.string.chat_request_deny), enabled = enabled(AnswerAction.Deny)) { act(AnswerAction.Deny) },
                    more = buildList {
                        interaction.ownerApprovalPhrase?.let { phrase ->
                            add(JAction(stringResource(R.string.chat_request_owner_reply, phrase),
                                enabled = enabled(AnswerAction.OwnerReply)) { act(AnswerAction.OwnerReply) })
                        }
                        if (interaction.offersAlways) add(JAction(interaction.alwaysActionLabel.text(),
                            enabled = enabled(AnswerAction.Always)) { act(AnswerAction.Always) })
                    },
                )
                interaction.alwaysScopeDetail?.takeIf { interaction.offersAlways }?.let {
                    Text(stringResource(R.string.chat_request_always_allows, it.text()), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
                }
            }
            "plan" -> {
                val recordedApproval = when (val mode = interaction.recordedPlanThen) {
                    "default" -> mode to AnswerAction.ApproveStepwise
                    "acceptEdits", "auto" -> mode to AnswerAction.ApproveEdits
                    SessionModes.BYPASS -> mode to AnswerAction.ApproveAll
                    else -> null
                }
                if (recordedApproval != null) {
                    val (mode, action) = recordedApproval
                    val modeLabel = SessionModes.modeLabel(mode, LocalResources.current) ?: mode
                    Text(modeLabel, style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
                    DecisionButtons(
                        primary = JAction(stringResource(R.string.chat_tray_approve), enabled = enabled(action)) { act(action) },
                        secondary = JAction(stringResource(R.string.chat_plan_reject), enabled = enabled(AnswerAction.Reject)) { act(AnswerAction.Reject) },
                    )
                } else {
                    DecisionButtons(
                        primary = JAction(stringResource(R.string.chat_plan_approve_edits), enabled = enabled(AnswerAction.ApproveEdits)) { act(AnswerAction.ApproveEdits) },
                        secondary = JAction(stringResource(R.string.chat_plan_reject), enabled = enabled(AnswerAction.Reject)) { act(AnswerAction.Reject) },
                        // Web's three ways to approve (Interactions.tsx MODES): "Confirm each step" keeps Claude asking before each edit.
                        more = listOf(JAction(stringResource(R.string.chat_plan_approve_all), enabled = enabled(AnswerAction.ApproveAll)) { act(AnswerAction.ApproveAll) },
                            JAction(stringResource(R.string.chat_plan_approve_stepwise), enabled = enabled(AnswerAction.ApproveStepwise)) { act(AnswerAction.ApproveStepwise) }),
                    )
                }
                if (recordedApproval == null || recordedApproval.first == SessionModes.BYPASS) {
                    Text(stringResource(if (planRead) R.string.chat_plan_approve_all_note else R.string.chat_plan_read_first_note),
                        style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
                }
            }
            "question" -> if (question != null) {
                // As on iOS (needsFooter): one single-choice question is answered by tapping an option, so it has no
                // pages to turn; its only footer is a Submit for the "Other answer" text, once there is some.
                if (questions.size > 1 || question.multiSelect) Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween) {
                    JTextButton(stringResource(R.string.chat_question_previous), { update(draft.copy(page = draft.page - 1)) }, enabled = draft.page > 0 && editable)
                    val spokenPage = stringResource(R.string.chat_question_position, draft.page + 1, questions.size)
                    Text("${draft.page + 1} / ${questions.size}", Modifier.padding(top = Tokens.Space.m).semantics { contentDescription = spokenPage })
                    val action = if (draft.page == questions.lastIndex) AnswerAction.Submit else AnswerAction.Advance
                    JTextButton(stringResource(if (draft.page == questions.lastIndex) R.string.chat_question_submit else R.string.chat_question_next), { act(action) }, enabled = enabled(action))
                } else if (!draft.other[question.question].isNullOrBlank()) Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.End) {
                    JTextButton(stringResource(R.string.chat_question_submit), { act(AnswerAction.Submit) }, enabled = enabled(AnswerAction.Submit))
                }
            }
            "elicitation" -> ElicitationActions(interaction, draft, editable, ::act)
            "screen" -> if (secret) {
                JButton(stringResource(R.string.chat_screen_submit_password), onClick = { act(AnswerAction.TerminalText, AnswerDraft(text = password)) },
                    enabled = editable && password.isNotEmpty())
            } else {
                // The options read as "n of total"; "Say it in the chat instead" is a separate button, not one of them.
                // A multi-select dialog ticks these and sends them together with the submit row, below.
                val multi = interaction.dialog?.multi == true
                val plain = options.filter { it.kind != ScreenOptionKind.Chat }
                fun positionOf(option: ParsedScreenOption) = plain.indexOfFirst { it === option } + 1 to plain.size
                if (commandPair != null) {
                    val allow = draft.copy(option = commandPair.first.key)
                    val deny = draft.copy(option = commandPair.second.key)
                    DecisionButtons(
                        primary = JAction(stringResource(R.string.chat_request_allow),
                            enabled = AnswerBuilder.build(interaction, AnswerAction.ScreenOption, allow, context).allowed) {
                            act(AnswerAction.ScreenOption, allow)
                        },
                        secondary = JAction(stringResource(R.string.chat_request_deny),
                            enabled = AnswerBuilder.build(interaction, AnswerAction.ScreenOption, deny, context).allowed) {
                            act(AnswerAction.ScreenOption, deny)
                        },
                    )
                } else options.forEach { option ->
                    val value = draft.copy(option = option.key)
                    val allowed = option.kind != ScreenOptionKind.Unknown &&
                        AnswerBuilder.build(interaction, AnswerAction.ScreenOption, value, context).allowed
                    // The terminal's "chat about this" hands the question back to the conversation (iOS "Say it in the chat instead").
                    if (option.kind == ScreenOptionKind.Chat) JButton(stringResource(R.string.chat_screen_chat_instead), { act(AnswerAction.ScreenOption, value) },
                        kind = JButtonKind.Secondary, size = JButtonSize.Small, icon = PhIcons.ChatCircle, enabled = allowed)
                    // A multi-select dialog's free text is its field in the body, not a row to tick.
                    else if (!(multi && option.kind == ScreenOptionKind.TypeSomething)) {
                        val display = AnswerBuilder.display(option.label)
                        DecisionOption(numberedOption(option.key, display.text), { act(AnswerAction.ScreenOption, value) },
                            detail = option.detail, recommended = display.recommended,
                            selected = if (multi) option.key in draft.selected else draft.option == option.key,
                            enabled = allowed, multiSelect = multi, position = positionOf(option), fullText = full)
                    }
                }
                if (multi) JButton(interaction.dialog?.submit?.takeIf(String::isNotBlank)
                    ?: stringResource(R.string.chat_question_submit), { act(AnswerAction.ScreenSubmit) },
                    enabled = enabled(AnswerAction.ScreenSubmit))
            }
        }
    }
    val content: @Composable () -> Unit = {
        if (interaction.kind == "question") DelegateNotes(interaction, recommendationOnOptions)
        // In the conversation's dock the session is the page itself and the countdown is in the dock's header.
        if (full) Text(interactionSource(interaction, machine), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
        interaction.requestSourceLabel()?.let { Text(it.text(), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2) }
        blockedReason?.let { Text(it, style = PersonaTheme.type.callout, color = PersonaTheme.palette.cautionText) }
        // Always present (empty and sizeless when idle) so a screen reader announces "Sending" / "Delivery not confirmed" after an answer;
        // the countdown stays outside, or it would be read out every minute.
        Column(Modifier.testTag("request-status").semantics(mergeDescendants = true) { liveRegion = LiveRegionMode.Polite }) {
            if (uncertain) PendingBadge(PendingStatus.Unconfirmed)
            if (!readOnly && (uncertain || busy)) Text(stringResource(if (uncertain) R.string.chat_request_uncertain else R.string.chat_request_busy),
                style = PersonaTheme.type.callout, color = PersonaTheme.palette.ink2)
            if (refusal != null) Text(refusal, style = PersonaTheme.type.callout, color = PersonaTheme.palette.cautionText)
        }
        if (readOnly || (full && !uncertain && !busy)) Text(
            if (readOnly) stringResource(R.string.chat_request_read_only)
            else RequestExpiry.label(interaction.expiresAt, now)?.text() ?: stringResource(R.string.chat_request_waiting),
            style = PersonaTheme.type.callout, color = PersonaTheme.palette.ink2)
        Spacer(Modifier.height(Tokens.Space.s))
        when (interaction.kind) {
            "permission" -> if (interaction.isSecretRun) SecretRunSummary(interaction)
            else if (command != null) {
                CommandInputSummary(command)
                if (!readOnly) {
                    Spacer(Modifier.height(Tokens.Space.s))
                    CommandInputField(command, fieldValues["input"].orEmpty(), { fieldValues = mapOf("input" to it) }, editable)
                }
            }
            else if (interaction.isSecretHandoff) SecretHandoffSummary(interaction) else {
                // Another agent's card says why it asks, in its own words.
                interaction.reason?.takeIf { interaction.fromOtherAgent && it.isNotBlank() }?.let {
                    ChatLinkText(it, style = PersonaTheme.type.callout.copy(color = PersonaTheme.palette.ink2))
                }
                PermissionRequestSummary(interaction)
                interaction.toolInput?.let { input ->
                    var showInput by remember(interaction) { mutableStateOf(false) }
                    JTextButton(stringResource(if (showInput) R.string.chat_permission_hide_input else R.string.chat_permission_show_input), { showInput = !showInput })
                    if (showInput) JCodeBlock(input.toString())
                }
                // A card with fields is denied plainly, as on iOS: its form is the only thing to type into (#4641).
                if (!interaction.hasFieldsForm) JTextField(draft.message, { if (it.length <= 4000) update(draft.copy(message = it)) },
                    label = stringResource(R.string.chat_permission_deny_reason), modifier = Modifier.fillMaxWidth(), enabled = editable)
                if (draft.message.isNotEmpty() && !interaction.hasFieldsForm) Text(stringResource(R.string.chat_permission_clear_reason), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
            }
            "plan" -> {
                if (full) ReadingContent { ChatMarkdown(interaction.plan.orEmpty()) }
                else {
                    Column(Modifier.heightIn(max = 140.dp).verticalScroll(rememberScrollState(), enabled = false)) {
                        ReadingContent { ChatMarkdown(interaction.plan.orEmpty()) }
                    }
                    JTextButton(stringResource(R.string.chat_plan_show_full), onExpand)
                }
                JTextField(draft.feedback, { update(draft.copy(feedback = it)) }, label = stringResource(R.string.chat_plan_feedback),
                    modifier = Modifier.fillMaxWidth(), enabled = editable)
                if (draft.feedback.isNotEmpty()) Text(stringResource(R.string.chat_plan_clear_feedback), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
            }
            "question" -> if (readOnly) {
                questions.forEach { past ->
                    ChatLinkText(past.question, style = PersonaTheme.type.body)
                    past.options.forEach { option ->
                        Text(AnswerBuilder.display(option.label).text, style = PersonaTheme.type.callout)
                        option.description?.let { Text(it, style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2) }
                    }
                }
            } else if (question != null) {
                // The question's header and "Multiple choice", numbered options: as the terminal shows them (iOS QuestionForm).
                val tags = listOfNotNull(question.header?.takeIf(String::isNotBlank), stringResource(R.string.chat_question_multi).takeIf { question.multiSelect })
                if (tags.isNotEmpty()) Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) { tags.forEach { JTag(it) } }
                ChatLinkText(question.question, style = PersonaTheme.type.body)
                question.options.forEachIndexed { index, option ->
                    val display = AnswerBuilder.display(option.label)
                    val assistantPick = option.label in recommendations.getOrNull(draft.page).orEmpty()
                    DecisionOption(numberedOption("${index + 1}", display.text), { act(AnswerAction.PickOption, draft.copy(optionIndex = index)) },
                        detail = option.description, recommended = display.recommended || assistantPick,
                        recommendedText = if (assistantPick) stringResource(R.string.chat_delegate_recommended) else null,
                        selected = option.label in draft.picks[question.question].orEmpty(),
                        enabled = editable && (!stoppedWork || question.isDeferChoice(option.label)),
                        multiSelect = question.multiSelect, position = index + 1 to question.options.size, fullText = full)
                }
                var otherOpen by rememberSaveable(interaction.id, question.question) { mutableStateOf(false) }
                if (otherOpen || question.question in draft.other) {
                    JTextField(draft.other[question.question].orEmpty(), {
                        update(draft.copy(other = draft.other + (question.question to it), picks = draft.picks - question.question))
                    }, label = stringResource(R.string.chat_question_other), modifier = Modifier.fillMaxWidth(), enabled = editable && !stoppedWork)
                } else JTextButton(stringResource(R.string.chat_question_other_open), { otherOpen = true }, enabled = editable && !stoppedWork)
            }
            "elicitation" -> ElicitationFields(interaction, draft, update, editable)
            "screen" -> if (secret) {
                Text(stringResource(R.string.chat_screen_password_needed))
                JTextField(password, { password = it }, label = stringResource(R.string.chat_screen_password),
                    visualTransformation = PasswordVisualTransformation(),
                    keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Password, autoCorrectEnabled = false),
                    singleLine = true, modifier = Modifier.fillMaxWidth(), enabled = editable)
                Text(stringResource(R.string.chat_screen_password_note), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
            } else {
                val ask = interaction.dialog?.commandAsk
                if (ask != null) CommandAskContent(ask, now)
                else {
                    interaction.dialog?.header?.takeIf(String::isNotBlank)?.let { JTag(it) }
                    ChatLinkText(interaction.dialog?.question ?: fallback?.question ?: interaction.headline.text())
                    val command = interaction.requestCommand(items)
                    if (command != null) {
                        Text(stringResource(R.string.chat_screen_full_command), style = PersonaTheme.type.headline)
                        JCodeBlock(command.command)
                        Text(stringResource(R.string.chat_screen_command_ids, command.requestId, command.toolUseId), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
                    } else (interaction.dialog?.detail ?: fallback?.detail)?.let { ChatLinkText(it) }
                }
                options.mapNotNull { it.detail }.forEach { ChatLinkText(it, style = PersonaTheme.type.caption.copy(color = PersonaTheme.palette.ink2)) }
                if (options.any { it.kind == ScreenOptionKind.TypeSomething }) JTextField(draft.text,
                    { update(draft.copy(text = it)) }, label = stringResource(R.string.chat_screen_type_answer), modifier = Modifier.fillMaxWidth(), enabled = editable)
                if (options.isEmpty()) Text(stringResource(R.string.chat_screen_unreadable))
            }
            else -> Text(stringResource(R.string.chat_request_unsupported))
        }
        // The assistant's own secret cards have no terminal to go to and nothing to hand back (#1137, #3962).
        if (!interaction.isSecretRun && !interaction.isSecretHandoff) Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            if (onTerminal != null || showTerminalHelp) JTextButton(if (onTerminal != null) RequestExpiry.terminalLabel.text() else stringResource(R.string.chat_request_terminal_help),
                { password = ""; if (onTerminal != null) onTerminal() else terminalHelp = true })
            if (!interaction.noHandBack && ApprovalPolicy.actions(AnswerEntry.Conversation, interaction.kind, false).any { it.id == ApprovalAction.Dismiss }) {
                JTextButton(stringResource(R.string.chat_request_dismiss), { act(AnswerAction.Dismiss) }, enabled = enabled(AnswerAction.Dismiss))
            }
        }
    }
    // Large accessibility text scrolls together with vertically arranged actions.
    val largeText = LocalDensity.current.fontScale >= 1.5f
    Column(if (full) Modifier.fillMaxHeight() else Modifier) {
        Column(Modifier.weight(1f, fill = full).verticalScroll(scroll)) {
            content()
            if (largeText && !readOnly) actions()
        }
        if (!largeText && !readOnly) {
            JDivider()
            // Bound long option lists so the request body remains reachable on compact screens.
            BoxWithConstraints {
                Column(Modifier.heightIn(max = maxHeight * 0.6f).verticalScroll(rememberScrollState())) { actions() }
            }
        }
    }
    if (terminalHelp) TerminalHelpDialog { terminalHelp = false }
}

/** Claude Code's command prompt read off its terminal (#5265), with the same field and answer rules as iOS and web. */
internal data class CommandAsk(
    val command: String,
    val description: String?,
    val warning: String?,
    val risk: String?,
    val deadline: String?,
    val deadlineAt: Instant?,
    val deadlineAllows: Boolean,
) {
    val original: String? get() = listOfNotNull(warning, deadline).joinToString("\n").ifEmpty { null }
    /** The same approximate three-row threshold as iOS, without a second layout pass after a theme change. */
    val folds: Boolean get() = command.split('\n').sumOf { maxOf(1, (it.length + 37) / 38) } > 3

    /** Whole seconds rounded up; zero once the hub's deadline has passed. */
    fun countdownSeconds(now: Instant): Long? {
        val end = deadlineAt ?: return null
        val millis = runCatching { Duration.between(now, end).toMillis() }.getOrElse { return null }
        if (millis <= 0) return 0
        return millis / 1_000 + if (millis % 1_000 == 0L) 0 else 1
    }
}

internal val ScreenDialog.commandAsk: CommandAsk?
    get() {
        fun text(value: String?) = value?.trim()?.takeIf(String::isNotEmpty)
        val command = text(command) ?: return null
        return CommandAsk(command, text(description), text(warning), text(risk), text(deadline),
            deadlineAt = parseTime(deadlineAt), deadlineAllows = deadlineAction == "allow")
    }

/** Allow / Deny only replace a command prompt's exactly two plain Yes / No choices. */
internal fun ScreenDialog.commandAllowDeny(): Pair<DialogOption, DialogOption>? {
    if (commandAsk == null || multi) return null
    val choices = options.filter { it.kind.orEmpty().let { kind -> kind.isEmpty() || kind == "plain" } }
    if (choices.size != 2 || choices.size != options.size) return null
    if (choices.map { it.label.trim().lowercase() } != listOf("yes", "no")) return null
    return choices[0] to choices[1]
}

@Composable
private fun CommandAskContent(ask: CommandAsk, now: Instant) {
    var showAll by rememberSaveable(ask.command) { mutableStateOf(false) }
    var showOriginal by rememberSaveable(ask.command) { mutableStateOf(false) }
    ask.description?.let { Text(it, style = PersonaTheme.type.body, color = PersonaTheme.palette.ink) }
    JCodeBlock(ask.command, modifier = Modifier.testTag("screen-command"), maxLines = if (showAll) Int.MAX_VALUE else 3)
    if (ask.folds) JTextButton(stringResource(
        if (showAll) R.string.chat_command_ask_collapse else R.string.chat_command_ask_show_all),
        { showAll = !showAll }, modifier = Modifier.testTag("screen-command-all"))
    if (ask.warning != null) {
        Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s), modifier = Modifier.testTag("screen-command-risk")) {
            JIcon(PersonaIcons.errorFilled, null, size = Tokens.Size.markS, tint = PersonaTheme.palette.cautionText)
            Text(stringResource(if (ask.risk == "delete") R.string.chat_command_ask_risk_delete else R.string.chat_command_ask_risk_other),
                style = PersonaTheme.type.callout, color = PersonaTheme.palette.cautionText)
        }
        val original = ask.original
        if (original != null) {
            JTextButton(stringResource(if (showOriginal) R.string.chat_command_ask_hide_original else R.string.chat_command_ask_show_original),
                { showOriginal = !showOriginal }, modifier = Modifier.testTag("screen-command-original"))
            if (showOriginal) JSelectableText(original, style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
        }
    }
    ask.countdownSeconds(now)?.let { left ->
        val line = if (left <= 0) stringResource(if (ask.deadlineAllows) R.string.chat_command_ask_countdown_over_allow
            else R.string.chat_command_ask_countdown_over_deny)
        else {
            val time = "${left / 60}:${(left % 60).toString().padStart(2, '0')}"
            stringResource(if (ask.deadlineAllows) R.string.chat_command_ask_countdown_allow
                else R.string.chat_command_ask_countdown_deny, time)
        }
        Text(line, Modifier.testTag("screen-command-countdown"), style = PersonaTheme.type.callout, color = PersonaTheme.palette.ink2)
    }
}

internal val Interaction.isManagerWorkCard: Boolean
    get() = kind == "question" && toolName == "persona_triage"

private fun Question.isDeferChoice(label: String): Boolean =
    !multiSelect && options.any { it.label == label && it.action == "defer" }

/** The stopped-Agent subset of Web's workCardLocalResolution: bare dismiss, or answers made only from explicit,
 * single-choice defer options. Missing actions from an older hub never turn into local answers. */
private fun Interaction.stoppedWorkAnswerAllowed(action: AnswerAction, draft: AnswerDraft): Boolean {
    val questions = questions.orEmpty()
    fun deferredPick(question: Question): Boolean {
        if (!draft.other[question.question].isNullOrBlank()) return false
        val picks = draft.picks[question.question].orEmpty()
        return picks.isNotEmpty() && picks.all(question::isDeferChoice)
    }
    return when (action) {
        AnswerAction.Dismiss -> true
        AnswerAction.PickOption -> questions.getOrNull(draft.page)?.let { question ->
            question.options.getOrNull(draft.optionIndex)?.let { question.isDeferChoice(it.label) } == true
        } == true
        AnswerAction.Advance -> questions.getOrNull(draft.page)?.let(::deferredPick) == true
        AnswerAction.Submit -> questions.isNotEmpty() && questions.all(::deferredPick)
        else -> false
    }
}

/** Validate the actual post-action wire result, as Web's workCardLocalResolution does. In particular, Advance can
 * resolve immediately when a retained draft completes every question; a defer on the current page is not enough. */
private fun Interaction.stoppedWorkResultAllowed(result: AnswerResult): Boolean {
    val resolution = result.resolution ?: return true
    if (resolution == Resolution(dismiss = true)) return true
    val answers = resolution.answers ?: return false
    if (answers.isEmpty() || resolution != Resolution(answers = answers)) return false
    val questions = questions.orEmpty()
    return answers.all { (questionText, answer) ->
        questions.firstOrNull { it.question == questionText }?.isDeferChoice(answer) == true
    }
}

/** "1. Option": the number or key the terminal shows beside an option, when it is a short one. */
internal fun numberedOption(key: String, text: String): String =
    if (key.length in 1..2 && key.all(Char::isLetterOrDigit) && !text.startsWith("$key.")) "$key. $text" else text

@Composable
internal fun TerminalHelpDialog(onDismiss: () -> Unit) {
    JDialog(onDismiss = onDismiss, title = stringResource(R.string.chat_terminal_help_title),
        text = stringResource(R.string.chat_terminal_help_text),
        confirm = stringResource(R.string.chat_terminal_help_ok) to onDismiss, dismiss = null)
}
