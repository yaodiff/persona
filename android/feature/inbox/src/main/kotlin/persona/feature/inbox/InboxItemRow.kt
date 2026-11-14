package persona.feature.inbox

import androidx.compose.runtime.*
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.stringResource
import androidx.compose.material3.Text
import persona.core.designsystem.*
import persona.core.designsystem.icons.PersonaIcons
import persona.core.designsystem.icons.PhIcon
import persona.core.designsystem.icons.PhIcons
import persona.core.domain.inbox.*
import persona.core.navigation.LocalDetailKey
import persona.core.navigation.Navigator

@Composable
internal fun InboxItemRow(record: InboxRecord, repository: InboxRepository, navigator: Navigator,
    composers: ReplyComposers, position: GroupPosition, done: Boolean, busy: Boolean, onlineSince: Long?, onDone: () -> Unit,
    onHandOff: () -> Unit,
    onMute: (InboxMuteOption) -> Unit = {},
    onRule: (InboxMuteOption) -> Unit = {},
    selection: InboxRowSelection? = null,
) {
    val item = record.item
    val groupEvent = record.feedId != null && record.isGroup != false && item.kind in setOf("group", "mention", "bot")
    val pages = LocalInboxItemPages.current
    val translated = rememberItemTranslation(record)
    var menu by remember { mutableStateOf(false) }
    val res = LocalResources.current
    val clock = LocalInboxClock.current
    val zone = LocalInboxZone.current
    val nowText = inboxNowForRow(record, res, currentLocale, now = clock(), zone = zone)
    val metaText = inboxRowThirdLine(record, res, translated?.preview?.ifBlank { null })
    val oldMark = if (!done) eventMark(record) else null
    val doneLabel = stringResource(if (done) R.string.inbox_move_back else R.string.inbox_done)
    val handOffLabel = stringResource(R.string.inbox_hand_to_ai)
    if (menu) JActionSheet(title = item.title, onDismiss = { menu = false }, actions = buildList {
        add(JAction(doneLabel, doneIcon(done), enabled = !busy, onClick = onDone))
        add(JAction(handOffLabel, PhIcons.Robot, onClick = onHandOff))
        // What the item can be muted by, the name with each (iOS `rowMenu`): its repository, chat or sender; then a rule for each.
        addAll(muteAndRuleActions(res, record, onMute, onRule))
    })
    val selecting = selection?.selecting == true
    JSwipeActions(end = inboxRowSwipe(doneLabel, done, busy, selecting, onDone)) {
        MessageItem(sender = item.sender.orEmpty().ifBlank { item.title }, title = translated?.subject?.ifBlank { null } ?: item.title,
            heading = item.title.takeIf { groupEvent },
            titleIcon = translated?.let { PhIcons.Translate }, titleIconLabel = translated?.let { stringResource(R.string.inbox_translated) },
            titleTag = stringResource(R.string.inbox_kind_dm).takeIf { item.kind == "dm" },
            unread = if (record.count > 1) record.count else 0,
            selected = isInboxItemSelected(LocalDetailKey.current, item.id, record.feedId),
            position = position, avatar = rememberLarkAvatar(record.avatar.takeIf { record.doc == null }, onlineSince), avatarIcon = record.doc?.let { docIcon(it.type) },
            // A hub connector's item is any connector's (#2726): a neutral mark and the hub's name for it, never a brand.
            source = when {
                record.isConnector -> MessageSource.Other
                record.feedId != null || record.source == "lark" -> MessageSource.Lark
                record.isMail -> MessageSource.Mail
                else -> MessageSource.Other
            },
            sourceLabel = null,
            group = record.groupName.takeIf { groupEvent },
            time = item.updatedAt?.let { displayTime(it, currentLocale, res, zone, clock()) },
            // Every row keeps title / AI summary / source-specific latest line, with or without where.
            preview = item.summary?.takeIf { it.isNotBlank() },
            meta = MessageItemMeta(metaText, nowText, inboxNowStatus(record.event.now), problem = record.event.now == "failed",
                key = record.event.now),
            status = when { done -> null; item.isWorking || record.agent?.state in setOf("running", "queued") -> Status.Working
                item.section == InboxTier.Action.wire -> Status.NeedsYou; else -> null },
            // The kind tag sits beside the title and a count above one is a badge; no separate source/count chip row.
            tags = emptyList(),
            // An extension may draw the page of its connector's items (inbox_item); the rest open their own page.
            // An event (inbox phase 1) opens its own page; an item from an older hub its source, as always.
            checked = selection?.checked?.takeIf { selection.selectable }, dimmed = selecting && selection?.selectable == false,
            onClick = { if (!busy) { selection?.click { navigator.navigate(inboxRowPage(record, pages)) } ?: navigator.navigate(inboxRowPage(record, pages)) } },
            onLongClick = { if (!busy) { selection?.longClick { menu = true } ?: run { menu = true } } }, extra = {
                record.localDelivery?.pendingStatus()?.let { PendingBadge(it) }
                    ?: record.localStatus?.let { InboxLocalStatus(it) }
                // Meta carries a recognized shared state; without one, the old mark still has its own place below it.
                if (nowText == null) oldMark?.let { EventRowMark(it) }
                if (!done) AgentDraftCard(record, composers, navigator, repository = repository)
            })
    }
}

@Composable
private fun InboxLocalStatus(label: String) {
    // A pending reply shows as its badge from [InboxRecord.localDelivery]; the real repository words the rest in the
    // shown language already, and the demo one writes the tokens above, which turn into the shown language here.
    val draft = label.removePrefix(LOCAL_DRAFT).takeIf { label.startsWith(LOCAL_DRAFT) }
    val shown = when {
        draft != null -> stringResource(R.string.inbox_local_draft, draft)
        label == LOCAL_UNCONFIRMED -> stringResource(R.string.inbox_local_unconfirmed)
        else -> label
    }
    Text(shown, style = PersonaTheme.type.micro,
        color = if (draft != null) PersonaTheme.palette.cautionText else PersonaTheme.palette.ink3)
}


@Composable
internal fun ReplyStatus(state: ReplyComposerState) {
    state.delivery.pendingStatus()?.let { PendingBadge(it) }
        ?: if (state.sending) PendingBadge(PendingStatus.Sending) else Unit
    state.error?.let { JNotice(androidx.compose.ui.res.stringResource(it.text), status = Status.NeedsYou) }
}

/**
 * A row's one swipe, from the end as iOS's trailing full swipe (`InboxView`): Done in the positive tone, and Move Back with the
 * undo arrow in the accent tone on a done item. None while a write for the row is out ([busy]) or items are being picked
 * ([selecting]). The hand-off is not a swipe: it is in the long-press sheet.
 */
internal fun inboxRowSwipe(label: String, done: Boolean, busy: Boolean, selecting: Boolean, onDone: () -> Unit): SwipeAction? =
    if (busy || selecting) null
    else SwipeAction(label, doneIcon(done), Status.Unread, onDone, tone = if (done) SwipeTone.Accent else SwipeTone.Positive)

/** Done is the check mark and Move Back the undo arrow, on the swipe and in the long-press sheet alike (iOS `Theme.Icon.done` / `undo`). */
internal fun doneIcon(done: Boolean): PhIcon = if (done) PersonaIcons.undo else PersonaIcons.done

/**
 * As iOS InboxStore: one item filed or put back gives a light tap (`setDone(_ item:)`); many at once (Mark all as seen, Clear
 * all) buzz success when filed and tap when put back (`run`), so a count of them is felt.
 */
internal fun doneHaptic(done: Boolean, single: Boolean = false): Haptic = if (done && !single) Haptic.Success else Haptic.Tap
