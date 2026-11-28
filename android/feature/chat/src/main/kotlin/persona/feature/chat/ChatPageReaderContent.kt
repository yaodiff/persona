package persona.feature.chat

import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.items
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.input.nestedscroll.nestedScroll
import androidx.compose.ui.layout.layoutId
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.Dp
import persona.core.chatlist.ChatList
import persona.core.chatlist.ChatListState
import persona.core.chatlist.scrollToRecord
import persona.core.contracts.Closeout
import persona.core.contracts.Item
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.UndeliveredMessage
import persona.core.contracts.WorkState
import persona.core.contracts.api.ChatRepository
import persona.core.contracts.api.ChannelSources
import persona.core.contracts.api.OutboxMessage
import persona.core.contracts.api.DeliveryState
import persona.core.contracts.api.MessageSendReceipt
import persona.core.contracts.api.ChatComposerService
import persona.core.designsystem.*
import persona.core.domain.lines.ChatLine
import persona.core.domain.lines.isUserMessage
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.launch

/** The Assistant feature supplies its source mark and label for an externally opened conversation. */
val LocalOpeningMessageSource = staticCompositionLocalOf<(Session) -> (@Composable () -> Unit)?> { { null } }

/** The reader inside a ChatPage's JPage content slot. This is the same reader/navigation/outbox code, moved behind a
 * narrow state-holder boundary to keep ChatPage itself below ART's method limit. No second UI state is created. */
@Composable
internal fun ChatPageReaderContent(
    pagePaddingValues: PaddingValues,
    closeoutShown: Closeout?,
    closeoutWork: WorkState?,
    key: SessionKey,
    chatList: ChatListState,
    search: ChatSearchState,
    lines: List<ChatLine>,
    list: androidx.compose.foundation.lazy.LazyListState,
    listKeys: List<String>,
    foldsHeader: Boolean,
    headerScroll: androidx.compose.ui.input.nestedscroll.NestedScrollConnection,
    contentPlans: Map<String, LinePlan?>,
    renderStyle: ChatRenderStyle,
    planSheet: MutableState<Boolean>,
    sheetPlan: MutableState<LinePlan?>,
    glass: Boolean,
    agentChat: Boolean,
    scope: kotlinx.coroutines.CoroutineScope,
    timeline: ChatTimeline,
    timelineVersion: State<Int>,
    onFreeze: () -> Unit,
    newestVisibleItem: State<Item?>,
    readThrough: Item?,
    snapshot: persona.core.contracts.api.ChatSnapshot,
    deferCount: Boolean,
    placed: persona.core.domain.lines.PlacedMessages,
    pendingDay: java.time.LocalDate?,
    heldRows: List<persona.core.contracts.HeldAgentMessage>,
    heldActions: persona.core.contracts.api.HeldAgentActions?,
    undeliveredRows: List<UndeliveredMessage>,
    undeliveredBusy: Set<String>,
    onUndeliveredAction: ((UndeliveredMessage, DeliveryAction) -> Unit)?,
    composerDeliveries: Map<String, persona.core.contracts.api.ComposerDelivery>,
    deliveryActions: ChatDeliveryActions,
    readOnly: Boolean,
    onDeliveryAction: ((OutboxMessage, DeliveryAction) -> Unit)?,
    sendReceipts: List<MessageSendReceipt>,
    canSend: Boolean,
    onStatusDetails: () -> Unit,
    terminal: (() -> Unit)?,
    composerService: ChatComposerService?,
    composer: ChatComposerState,
    repository: ChatRepository,
    feedback: AppFeedback?,
    onNotice: (String) -> Unit,
    errorLine: String?,
    readyLine: String?,
    projectedFrom: List<Item>?,
    messages: List<OutboxMessage>,
    textUpdates: Map<String, ChatTextUpdate>,
    contentUserImages: Map<String, List<Item>>,
    contentHiddenImages: Set<String>,
    contentWorkGroups: Map<String, ChatLine.Work>,
    contentLastSteps: Map<String, String>,
    contentSourceLabels: Set<String>,
    waitingOnYou: Boolean,
    foldsBackground: Boolean,
    onTerminal: (() -> Unit)?,
    lastUserId: String?,
    turnFix: TurnErrorFix?,
    fixModel: ((persona.core.designsystem.ReadinessActionId) -> Unit)?,
    turnModelActions: TurnErrorModelActions?,
    openRecap: OpenRecap?,
    taskHost: AssistantTaskHost?,
    folding: Set<String>,
    foldErrors: Map<String, String>,
    onToggle: (String) -> Unit,
    onExpand: (Item) -> Unit,
    onFullText: (Item) -> Unit,
    loader: coil3.ImageLoader,
    session: Session?,
    machineName: String?,
    subagentCards: Boolean,
    messageMenu: MessageMenuState,
    turnLauncher: String?,
    earlierEngines: EarlierEnginePresentation?,
    onReadEarlierText: (SessionKey, Item) -> Unit,
    hasPendingCards: Boolean = false,
) {
    val horizontalPadding = pagePadding
    val res = LocalResources.current
    // The closeout card takes the room under the top bar; the list then starts right under the card.
    val padding = if (closeoutShown != null) PaddingValues(bottom = pagePaddingValues.calculateBottomPadding()) else pagePaddingValues
    val closeoutReplyId = remember(lines, closeoutShown != null) { if (closeoutShown != null) closeoutReplyId(lines) else null }
    val sourceFor = LocalOpeningMessageSource.current
    val externalSource = remember(session, renderStyle, sourceFor) {
        session?.takeIf { renderStyle == ChatRenderStyle.Assistant }?.let(sourceFor)
    }
    // A cache with an unknown boundary or a partial page may only contain a later user message.
    val opening = remember(externalSource, snapshot.olderBoundaryKnown, snapshot.hasOlder, snapshot.items) {
        if (externalSource != null && snapshot.olderBoundaryKnown && !snapshot.hasOlder)
            snapshot.items.firstOrNull(::isUserMessage)
        else null
    }
    val channels = LocalChannels.current
    val openingChannel = remember(opening?.clientMsgId, channels) { ChannelSources.of(opening?.clientMsgId, channels) }
    val openingSource: (@Composable () -> Unit)? = if (openingChannel != null) ({ JChannelSource(openingChannel) }) else externalSource
    val interleavedRows = remember(lines, placed.before) {
        if (placed.before.isEmpty()) null
        else buildList<TranscriptListRow>(lines.size + placed.before.values.sumOf { it.size }) {
            for (line in lines) {
                add(TranscriptListRow.Line(line))
                placed.before[line.id]?.asReversed()?.forEach { message ->
                    add(TranscriptListRow.Outbox(message))
                }
            }
        }
    }
    Column(Modifier.fillMaxSize()) {
        if (search.selected != null) ReturnChip { search.returnToOrigin(chatList) }
        search.notice?.let { Text(it, Modifier.padding(horizontal = horizontalPadding), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2) }
        closeoutShown?.let { closeout ->
            CloseoutCard(closeout, closeoutWork?.artifacts.orEmpty(), key.path, Modifier.padding(
                start = horizontalPadding, end = horizontalPadding, top = pagePaddingValues.calculateTopPadding() + Tokens.Space.s))
        }
        ChatReaderLayout(
            Modifier.weight(1f).then(if (foldsHeader) Modifier.nestedScroll(headerScroll) else Modifier),
            topInset = padding.calculateTopPadding(), bottomInset = padding.calculateBottomPadding(),
            collapse = {
                val topInset = padding.calculateTopPadding()
                WorkCollapseControl(lines, list, listKeys, topInset, onToggle,
                    Modifier.padding(horizontal = horizontalPadding)
                        .padding(top = topInset + Tokens.Space.s, bottom = Tokens.Space.s))
            },
            navigation = {
                ChatPageReaderNavigation(horizontalPadding, lines, contentPlans, renderStyle, list,
                    planSheet, sheetPlan, glass, agentChat, hasPendingCards, scope, timeline, timelineVersion, onFreeze,
                    chatList, snapshot.items, newestVisibleItem, readThrough, deferCount)
            },
        ) { listBottomInset ->
            val outboxBubble: @Composable (OutboxMessage) -> Unit = { message ->
                val delivery = composerDeliveries[message.clientMessageId]
                val deliveryState = effectiveOutboxState(message.state, delivery?.state)
                val busy = message.clientMessageId in deliveryActions.busyIds
                val onAction = onDeliveryAction?.let { action -> { messageAction: DeliveryAction -> action(message, messageAction) } }
                val failed = deliveryState == DeliveryState.Rejected || deliveryState == DeliveryState.Refused
                val receipt = sendReceipts.lastOrNull { it.clientMessageId == message.clientMessageId }
                Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                    // Glass: iOS's bubble, its mark and small menu in place of the status line, which still says why
                    // one did not go (#4339 C9); a delivery to check keeps the status line.
                    if (glass && drawsAsPendingBubble(deliveryState)) {
                        // A forward keeps its block here: the bubble draws it as its card (#3901).
                        val shown = if (carriesForward(message.text)) message.text else composerMessagePreview(message.text)
                        OutboxBubble(shown, message.clientMessageId, deliveryState, busy,
                            canSend = canSend, onAction = onAction)
                        if (receipt?.processing == true) Text(stringResource(persona.core.designsystem.R.string.designsystem_message_processing),
                            Modifier.align(Alignment.End), style = PersonaTheme.type.caption,
                            color = PersonaTheme.palette.ink2, maxLines = 1)
                        if (failed) PendingBadge(PendingStatus.Refused, Modifier.align(Alignment.End),
                            reason = message.copy(error = delivery?.refusal?.code ?: message.error).refusalReason())
                    } else {
                        UserBubble(message.text, message.clientMessageId, deliveryState)
                        OutboxStatus(message.copy(state = deliveryState,
                            error = delivery?.refusal?.code ?: message.error),
                            busy = busy, onAction = onAction, receipt = receipt, canSend = canSend, onDetails = onStatusDetails)
                    }
                    if (!readOnly && failed) {
                        // Edit puts the words back in the box and drops the refused bubble (nothing reached the computer).
                        ComposerRefusalActions(message, composerService, delivery, terminal, canSend = canSend,
                            edit = { onDeliveryAction?.invoke(message, DeliveryAction.Edit) }, notice = onNotice)
                    }
                }
            }
            ChatList(state = chatList, modifier = Modifier.fillMaxSize().endSelectionOnTapOutside(messageMenu), contentPadding = PaddingValues(
                start = horizontalPadding, end = horizontalPadding,
                top = padding.calculateTopPadding() + Tokens.Space.m.dense,
                bottom = listBottomInset + Tokens.Space.m.dense,
            )) {
                errorLine?.let { error -> item(key = ERROR_ROW, contentType = ERROR_ROW) { ChatSystemLine(error, failed = true) } }
                readyLine?.let { words -> item(key = READY_ROW, contentType = READY_ROW) { TypingLine(words) } }
                // Newest at the bottom (reverse layout). A failed send sits above the first line written after it.
                heldRows.asReversed().forEach { row ->
                    item(key = "held-${row.id}", contentType = "held") { HeldAgentBubble(row, heldActions, canSend, onNotice) }
                }
                undeliveredRows.asReversed().forEach { row ->
                    item(key = "undelivered-${row.id}", contentType = "undelivered") {
                        UndeliveredBubble(row, glass, row.id in undeliveredBusy, canSend,
                            if (onUndeliveredAction == null) null else ({ action -> onUndeliveredAction(row, action) }))
                    }
                }
                placed.rest.asReversed().forEach { message ->
                    item(key = "outbox-${message.clientMessageId}", contentType = "outbox") { outboxBubble(message) }
                }
                pendingDay?.let { day -> item(key = PENDING_DAY_ROW, contentType = "day") { DayLine(day, Modifier.testTag("chat-pending-day")) } }
                val transcriptLine: @Composable (ChatLine) -> Unit = { line ->
                    ChatSearchHighlight(search, line.contentKey) {
                        androidx.compose.runtime.CompositionLocalProvider(LocalAssistantTaskHost provides taskHost,
                            LocalTurnErrorFixAction provides fixModel, LocalTurnErrorModelActions provides turnModelActions,
                            LocalOpenRecap provides openRecap) {
                            val pictures = contentUserImages[line.id].orEmpty()
                            val isOpening = opening != null && when (line) {
                                is ChatLine.Row -> line.row.item?.id == opening.id || pictures.any { it.id == opening.id }
                                is ChatLine.UserImages -> line.images.any { it.id == opening.id }
                                else -> false
                            }
                            ChatLineContent(line, key, loader,
                                line.recordedDuration(snapshot.recordedTurnDurations),
                                onToggle, onExpand, folding, foldErrors,
                                lastUserId = lastUserId,
                                showToolViews = line !is ChatLine.Step || line.groupId !in contentWorkGroups,
                                turnFix = turnFix?.takeIf { line.contentKey == lines.firstOrNull()?.contentKey },
                                turnLauncher = turnLauncher,
                                onLastLine = { extent -> chatList.reportLastLine(line.contentKey, extent) }, onFullText = onFullText,
                                style = renderStyle, agentSession = session, textUpdates = textUpdates,
                                liveText = snapshot.liveText, streamingItemIds = snapshot.streamingItemIds,
                                userImages = pictures, openingSource = if (isOpening) openingSource else null,
                                imagesInUserBubble = line.id in contentHiddenImages,
                                workRunning = (line as? ChatLine.Step)?.let { contentWorkGroups[it.groupId]?.live } == true,
                                lastWorkStep = (line as? ChatLine.Step)?.let {
                                    // A nested subagent's card can come after the line's own last step (Glass).
                                    if (subagentCards) contentLastSteps[it.groupId] == it.step.id
                                    else contentWorkGroups[it.groupId]?.steps?.lastOrNull()?.id == it.step.id
                                } == true,
                                plan = (line as? ChatLine.Step)?.let { contentPlans[it.groupId] },
                                showSourceLabel = line.id in contentSourceLabels, waiting = waitingOnYou,
                                closeoutReply = closeoutReplyId, hideTaskPrompts = foldsBackground, onTerminal = onTerminal)
                        }
                    }
                }
                if (interleavedRows == null) {
                    items(lines, key = ChatLine::contentKey, contentType = ChatLine::contentType) { line ->
                        transcriptLine(line)
                    }
                } else {
                    items(interleavedRows, key = TranscriptListRow::key, contentType = TranscriptListRow::contentType) { row ->
                        when (row) {
                            is TranscriptListRow.Line -> transcriptLine(row.line)
                            is TranscriptListRow.Outbox -> outboxBubble(row.message)
                        }
                    }
                }
                if (earlierEngines != null) earlierEngineHistory(earlierEngines, scope, loader, renderStyle, onReadEarlierText)
                if (snapshot.loadingOlder) item(key = "loading-older", contentType = "loading") { JLoading() }
                if (snapshot.hasOlder && !snapshot.loadingOlder) item(key = "older", contentType = "older") {
                    JTextButton(stringResource(R.string.chat_screen_load_older), {
                        val resources = res
                        scope.launch { try { repository.loadOlder(key) } catch (e: CancellationException) { throw e } catch (e: Exception) { if (feedback?.failure(e) != true) onNotice(operationFeedback(e).text.resolve(resources)) } }
                    })
                }
            }
            val settling = snapshot.loading || projectedFrom == null || (snapshot.items.isNotEmpty() && projectedFrom !== snapshot.items)
            val readerPadding = PaddingValues(top = padding.calculateTopPadding(), bottom = listBottomInset)
            if (settling && lines.isEmpty() && messages.isEmpty() && heldRows.isEmpty() && undeliveredRows.isEmpty()) {
                // An Agent's chat on Glass: iOS's skeleton rows where its newest lines will be; Brief and Board keep the spinner.
                if (glass && agentChat) ChatSkeleton(Modifier.align(Alignment.BottomCenter).padding(start = horizontalPadding, end = horizontalPadding,
                    bottom = listBottomInset + Tokens.Space.m))
                else JLoading(Modifier.align(Alignment.Center).padding(readerPadding).padding(horizontal = horizontalPadding))
            }
            else if (!settling && lines.isEmpty() && messages.isEmpty() && heldRows.isEmpty() && undeliveredRows.isEmpty() && snapshot.error == null && earlierEngines == null)
                JEmptyState(stringResource(R.string.chat_screen_empty_title),
                    emptyChatHint(res, session, machineName, readOnly),
                    Modifier.align(Alignment.Center).padding(readerPadding).padding(horizontal = horizontalPadding))
        }
    }
}

/**
 * Resolves the displayed delivery state for an outbox bubble. A composer overlay may advance a refused message while
 * its Clear & Send retry is in flight or freshly accepted, but a stale Unknown / WaitingForNetwork overlay from an
 * earlier attempt must never mask an authoritative Refused / Rejected outbox state.
 */
internal fun effectiveOutboxState(outboxState: DeliveryState, composerState: DeliveryState?): DeliveryState =
    if (outboxState in setOf(DeliveryState.Refused, DeliveryState.Rejected) &&
        composerState in setOf(DeliveryState.Sending, DeliveryState.Sent, DeliveryState.Held))
        composerState!! else outboxState

private sealed interface TranscriptListRow {
    val key: String
    val contentType: String

    data class Line(val line: ChatLine) : TranscriptListRow {
        override val key: String get() = line.contentKey
        override val contentType: String get() = line.contentType()
    }

    data class Outbox(val message: OutboxMessage) : TranscriptListRow {
        override val key: String get() = "outbox-${message.clientMessageId}"
        override val contentType: String get() = "outbox"
    }
}

/** The same plan-return and latest-message navigation slot, with the page's plan and timeline state holders. */
@Composable
private fun ChatPageReaderNavigation(
    horizontalPadding: Dp,
    lines: List<ChatLine>,
    contentPlans: Map<String, LinePlan?>,
    renderStyle: ChatRenderStyle,
    list: androidx.compose.foundation.lazy.LazyListState,
    planSheet: MutableState<Boolean>,
    sheetPlan: MutableState<LinePlan?>,
    glass: Boolean,
    agentChat: Boolean,
    hasPendingCards: Boolean,
    scope: kotlinx.coroutines.CoroutineScope,
    timeline: ChatTimeline,
    timelineVersion: State<Int>,
    onFreeze: () -> Unit,
    chatList: ChatListState,
    items: List<Item>,
    newestVisibleItem: State<Item?>,
    readThrough: Item?,
    deferCount: Boolean,
) {
    val showJump = !chatList.followsEnd
    // A plan outside the viewport (or inside a folded Glass work line, matching iOS ChatPlan.latest) stays reachable
    // from the conversation's footer.
    val anchor = remember(lines, contentPlans, renderStyle) { latestPlanLine(lines, contentPlans) }
    val opensSheet = glass && agentChat
    val glassPlan = remember(items, opensSheet) { if (opensSheet) latestChatPlan(items) else null }
    val planOut by remember(anchor, glassPlan, opensSheet) { derivedStateOf {
        if (opensSheet) {
            glassPlan != null && list.layoutInfo.totalItemsCount > 0 && (
                anchor == null || anchor.plan.id != glassPlan.id ||
                    list.layoutInfo.visibleItemsInfo.none { it.key == anchor.lineId }
            )
        } else {
            anchor != null && list.layoutInfo.totalItemsCount > 0 &&
                list.layoutInfo.visibleItemsInfo.none { it.key == anchor.lineId }
        }
    } }
    val activePlan = if (opensSheet) glassPlan else anchor?.plan
    val showPlanPill = activePlan != null && planOut && (!opensSheet || !hasPendingCards)
    // An open sheet keeps the last plan if its work line folds away.
    if (planSheet.value && activePlan != null) SideEffect { sheetPlan.value = activePlan }
    if (showJump || showPlanPill) Row(
        Modifier.fillMaxWidth().padding(horizontal = horizontalPadding).padding(bottom = Tokens.Space.s)
            .then(if (showPlanPill) Modifier.layoutId(PlanReaderFooter) else Modifier),
        horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Box(Modifier.weight(1f), contentAlignment = Alignment.Center) {
            if (showPlanPill && activePlan != null) {
                val progress = remember(activePlan.todos) { planProgress(activePlan.todos) }
                PlanPill(progress.step, progress.total, progress.done, onClick = {
                    if (opensSheet) { sheetPlan.value = activePlan; planSheet.value = true }
                    else anchor?.let { target ->
                        scope.launch {
                            onFreeze()
                            chatList.scrollToRecord(target.lineId)
                        }
                    }
                }, modifier = Modifier.heightIn(min = Tokens.MinTouch.android),
                    spoken = if (opensSheet) planStepText(LocalResources.current, activePlan.todos) else null,
                    onClickLabel = if (opensSheet) stringResource(R.string.chat_plan_pill_opens) else null)
            }
        }
        if (showJump) ChatJumpToLatest(
            timeline, timelineVersion, items, newestVisibleItem, readThrough,
            deferCount = deferCount, agentChat = agentChat, onClick = chatList::returnToLatest)
    }
}
