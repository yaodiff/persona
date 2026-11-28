package persona.feature.inbox

import persona.core.contracts.api.InboxBulkAction
import persona.core.designsystem.text
import androidx.compose.ui.platform.LocalResources
import persona.core.designsystem.*
import persona.core.designsystem.icons.PersonaIcons
import persona.core.designsystem.icons.PhIcons
import persona.core.domain.inbox.InboxGroups
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveableStateHolder
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import android.content.res.Resources
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.compose.LocalLifecycleOwner
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.lifecycle.repeatOnLifecycle
import persona.core.contracts.api.ConnectionState
import persona.core.domain.inbox.*
import persona.core.navigation.ChatRoute
import persona.core.navigation.ExtensionUiRoute
import persona.core.contracts.api.SUBSCRIPTIONS_UI
import persona.core.navigation.ConnectionSettingsRoute
import persona.core.navigation.Navigator
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.launch
import persona.core.inbox.*
import persona.core.moduleapi.HubModule
import persona.core.moduleapi.ModuleRegistry

/** [onOpenCard]: the app opens a request card outside any session on Agents (#5610); without it, Agents opens. */
@Composable
fun InboxScreen(repository: InboxRepository, navigator: Navigator, onOpenCard: ((persona.core.navigation.RequestRef) -> Unit)? = null) {
    // The page lists the current assistant's inbox (docs/assistant/managers.md "Clients"): the hub's list is every
    // manager's, each item with its own, so another assistant's item still opens from a notification or a link.
    val listed by repository.snapshot.collectAsStateWithLifecycle()
    val assistant = currentAssistant()
    val snapshot = remember(listed, assistant) { listed.ofAssistant(assistant) }
    val connection by repository.connectionState.collectAsStateWithLifecycle(ConnectionState.Connecting)
    val onlineSince = rememberOnlineSince(connection)
    val shownConnection = rememberSettledConnection(connection)
    val savedPreferences by repository.preferences.collectAsStateWithLifecycle(InboxPreferences())
    val subscriptionsShown by repository.subscriptionsShown.collectAsStateWithLifecycle(false)
    val inboxSourcesShown by repository.inboxSourcesShown.collectAsStateWithLifecycle(false)
    val feedback = persona.core.designsystem.LocalAppFeedback.current
    val res = LocalResources.current
    val scope = rememberCoroutineScope()
    val composers = rememberReplyComposers(repository)
    val conversations by repository.conversationInbox.collectAsStateWithLifecycle()
    val conversationList by (conversations?.state ?: remember { kotlinx.coroutines.flow.MutableStateFlow(InboxConversationList()) })
        .collectAsStateWithLifecycle()
    var filters by remember(repository) { mutableStateOf<InboxPreferences?>(null) }
    val currentFilters = filters ?: savedPreferences
    val query = currentFilters.query
    val source = currentFilters.source
    val kind = currentFilters.kind
    val expanded = currentFilters.expanded
    val grouping = currentFilters.grouping
    val folds = currentFilters.folds
    // The section bar (#9053, docs/assistant/inbox.md "Section bar"): Needs you each time the Inbox is entered (All when
    // a kept search comes with it), kept while in it (an item opened and closed, a refresh, a filter), never across
    // visits, so a remembered FYI or Done never hides new requests. InboxPreferences.section, which the old bays kept,
    // is no longer read. "" until the bar is first moved: the entry follows the stored search text until then. An item
    // opened from outside the list lands it where the item is ([InboxLanding]), from the first frame.
    val landing = LocalInboxLanding.current
    var barChosen by androidx.compose.runtime.saveable.rememberSaveable { mutableStateOf(landing?.pending?.choice.orEmpty()) }
    var barSaved by androidx.compose.runtime.saveable.rememberSaveable { mutableStateOf("") }
    val bar = if (barChosen.isEmpty()) InboxBar.entered(query) else InboxBar.State(barChosen, barSaved.ifEmpty { null })
    val chosen = bar.chosen
    fun moveBar(state: InboxBar.State) { barChosen = state.chosen; barSaved = state.saved.orEmpty() }
    var preferenceError by remember { mutableStateOf(false) }
    val preferenceWrites = remember(repository) { kotlinx.coroutines.sync.Mutex() }
    fun filter(value: InboxPreferences) {
        // Search text appearing sets the choice aside and shows All; cleared (typed away or by Clear filters), it comes back.
        if (value.query != query) moveBar(InboxBar.searched(bar, query, value.query))
        filters = value
        scope.launch(start = kotlinx.coroutines.CoroutineStart.UNDISPATCHED) {
            kotlinx.coroutines.withContext(kotlinx.coroutines.NonCancellable) {
                preferenceWrites.lock()
                try { repository.savePreferences(value); preferenceError = false }
                catch (_: Exception) { preferenceError = true }
                finally { preferenceWrites.unlock() }
            }
        }
    }
    // The landing taken: the list shows the item's choice, All with Collapsed open (a fold kept with the others, read from
    // the stored ones so an early landing never writes the defaults over them); Back from the item returns here. A list
    // already showing the item stays. One moved beside the item it opened goes back when that page closes. An entry
    // ([InboxLanding.enter]) puts the list back as it opens.
    val listActive = LocalInboxListActive.current
    // A hidden host stays composed and shares this landing. It must not take one meant for the list in front.
    // [overEntry]: a sidebar return entered (Needs you) and then landed, in one turn. Apply the landing on that
    // fresh bar, or a list left on All would stay there (InboxBar.landed). iOS lands on a new page
    // (InboxView.swift:209–212, land at 977–981).
    LaunchedEffect(landing?.pending, listActive) {
        if (!listActive) return@LaunchedEffect
        val page = landing?.pendingPage
        val overEntry = landing?.overEntry == true
        val to = landing?.take() ?: return@LaunchedEffect
        if (to == InboxLanding.ENTERED) { barChosen = ""; barSaved = ""; return@LaunchedEffect }
        // overEntry follows enter: the fresh bar is Needs you (All only while a search is kept), then the
        // item's choice is picked. landed() would leave All in place, which a new iOS page does not.
        val moved = if (overEntry) InboxBar.picked(InboxBar.entered(query), to.choice) else InboxBar.landed(bar, to)
        if (page != null && moved != bar) landing?.moved(page, bar)
        moveBar(moved)
        if (to.openCollapsed) {
            val stored = filters ?: repository.preferences.first()
            if (InboxBar.NOISE !in stored.expanded) filter(stored.copy(expanded = stored.expanded + InboxBar.NOISE))
        }
    }
    // The page that moved this list beside it closed: the choice it had comes back.
    LaunchedEffect(landing?.restore) { landing?.takeRestore()?.let(::moveBar) }
    // Drawn: an item opened from another item beside this list moves it ([InboxLanding.landBeside]).
    DisposableEffect(landing) {
        landing?.let { it.drawn++ }
        onDispose { landing?.let { it.drawn-- } }
    }
    val isGlass = PersonaTheme.style == AppStyle.Glass
    val listStates = rememberSaveableStateHolder()
    var error by remember { mutableStateOf<String?>(null) }
    LaunchedEffect(repository, snapshot.refreshes) {
        if (snapshot.refreshes > 0 && snapshot.error == null) error = null
    }
    var refreshing by remember { mutableStateOf(false) }
    // Silent reload skips while one is already running (InboxStore.reload, InboxStore.swift:243–244).
    var backgroundInFlight by remember { mutableStateOf(false) }
    val pending = remember { mutableStateMapOf<String, Boolean>() }
    // Not snapshot state. The second tap in this frame must see it before the list recomposes.
    val inFlight = remember { mutableSetOf<String>() }
    // Selection (#2576): a long-press on a row starts it, Back or the bar's ← ends it.
    var selecting by androidx.compose.runtime.saveable.rememberSaveable { mutableStateOf(false) }
    var picked by androidx.compose.runtime.saveable.rememberSaveable(stateSaver = androidx.compose.runtime.saveable.listSaver<Set<String>, String>(
        { it.toList() }, { it.toSet() })) { mutableStateOf(emptySet<String>()) }
    fun endSelection() { selecting = false; picked = emptySet() }
    androidx.activity.compose.BackHandler(selecting) { endSelection() }
    // Picked rows are the assistant's whose inbox showed them: another assistant ends the selection, so nothing it no
    // longer shows is acted on.
    var selectionOf by androidx.compose.runtime.saveable.rememberSaveable { mutableStateOf(assistant.id) }
    LaunchedEffect(assistant.id) { if (selectionOf != assistant.id) { selectionOf = assistant.id; endSelection() } }
    // Named on the history's requests and in the keys of what it read; "" (one manager, or a hub without a roster)
    // names none, as before.
    val historyManager = assistant.id.takeIf { assistant.roster?.isMulti == true }.orEmpty()
    // Done's history past the list's few days (#2576): merged in under Done; the list's own copy of an item wins.
    val history = remember(repository, historyManager) { InboxDoneHistory(repository, scope) }
    val listedIds = remember(snapshot.records) { snapshot.records.mapTo(HashSet()) { it.item.id } }
    val allRecords = if (history.records.isEmpty()) snapshot.records else snapshot.records + history.records.filter { it.item.id !in listedIds }
    val records = allRecords.associateBy { it.item.id }
    val interactions = snapshot.interactions.associateBy { it.id }
    val items = orderHubInbox(allRecords.map { record ->
        pending[record.item.id]?.let { done ->
            record.item.copy(doneAt = if (done) java.time.Instant.now().toString() else null)
        } ?: record.item
    })
    fun list(settings: InboxListSettings): InboxListResult = buildInboxList(
        items = items, interactions = snapshot.interactions, sessions = snapshot.sessions,
        mutes = snapshot.mutes, settings = settings,
    )
    // A choice draws its section open; All folds as the user left it.
    val listSettings = InboxListSettings(source, kind, query, if (chosen == InboxBar.ALL) expanded.toSet() else expanded.toSet() + chosen)
    val result = list(listSettings)
    // In progress: the hub says so (`status`, inbox phase 1), or, from an older hub, the AI twin is drafting a reply.
    fun working(row: InboxRow) = row.type == InboxRowType.Item && records[row.id]?.agentWorking == true
    // A hub that sends `status` calls the section In progress; an older hub's is the AI twin's "Persona is working on it" as before.
    val eventHub = snapshot.records.any { it.item.status != null }
    // The summary speaks for the whole inbox, so it reads the unfiltered list.
    val overall = if (query.isEmpty() && source.isEmpty() && kind.isEmpty()) result else list(InboxListSettings())
    val bySection = inboxDisplayGroups(result, persona.core.contracts.CoreText.plain(res.getString(
        if (eventHub) R.string.inbox_section_working else R.string.inbox_group_working)), ::working)
    // Done's counts from the hub (#3016) only count with no filter or search on, since they count everything.
    val filtering = query.isNotBlank() || source.isNotEmpty() || kind.isNotEmpty()
    // The bar's choices and counts over the rows as shown (an older hub's AI-twin rows In progress), and what an empty
    // choice says; the hub's day counts keep Done in it while the list holds no done item.
    val barView = InboxBar.bar(bySection, chosen, listSettings, if (filtering) null else history.days[historyManager]?.sumOf { it.count })
    // Done chosen on the hub's counts alone: drawn with its counted groups, which read their own pages as they open.
    val displayGroups = if (InboxBar.DONE !in barView.shown || bySection.any { it.id == InboxBar.DONE }) bySection
        else bySection + InboxGroup(InboxBar.DONE, persona.core.contracts.CoreText.of(persona.core.contracts.CoreText.Key.InboxDone),
            emptyList(), emptyList(), folded = false)
    // A choice draws its section alone; All draws every section, folding as before.
    val groups = if (chosen == InboxBar.ALL) displayGroups else displayGroups.filter { it.id == chosen }
    val sourceOptions = remember(snapshot.records, res) {
        snapshot.records.map { record ->
            val label = record.sourceLabel.ifBlank {
                if (record.feedId != null) res.getString(R.string.inbox_source_lark)
                else record.item.account.ifBlank { res.getString(R.string.inbox_source_mail) }
            }
            val type = when {
                record.isLark -> "lark"
                record.isMail -> "mail"
                else -> record.source
            }
            InboxSourceOption(record.item.account, label, type)
        }.filter { it.id.isNotEmpty() }.distinctBy { it.id }.distinctBy { it.label }
    }
    val sources = listOf("" to res.getString(R.string.inbox_all_sources)) + sourceOptions.map { it.id to it.label }
    var showFilters by remember { mutableStateOf(false) }
    var filterPage by remember { mutableStateOf<String?>(null) }
    var handingOff by remember { mutableStateOf<String?>(null) }
    var composeDraft by remember { mutableStateOf<MailDraft?>(null) }
    val mailTransport by repository.mailTransport.collectAsStateWithLifecycle()
    val mailAccess = LocalMailAccess.current
    val larkFeatures = LocalLarkFeatures.current
    val hubFeatures = LocalHubFeatures.current
    val sourceVisibility = inboxSourceVisibility(
        larkOffered = larkFeatures.chats || ModuleRegistry.isActive(HubModule.Lark, hubFeatures),
        mailOffered = mailAccess == MailAccess.Live,
        stored = snapshot.storedSources,
    )
    // The Kind sheet and the Glass strip offer the same fixed values with the same labels (#17814).
    val kindFilterOptions = visibleInboxFilterKinds(sourceVisibility).map { it to inboxFilterKindLabel(res, it) }
    // A hidden Mail kind choice must not remain as an invisible persisted filter (#17814), matching the web fallback.
    LaunchedEffect(sourceVisibility.mail, kind) {
        if (!sourceVisibility.mail && kind == "mail") filter(currentFilters.copy(kind = ""))
    }

    val haptics = rememberHaptics()
    // Tap-driven row motion only (#9600). A hub refresh changes the rows with the flag clear.
    val listTapMotion = remember { ListTapMotion() }
    /**
     * [pull] drives the list's pull indicator. A background refresh must not: iOS shows that indicator
     * only from `.refreshable` (`InboxView.swift:916`) and a center progress only while the list is empty
     * (`InboxView.swift:917`). The empty list already uses `JLoading` via `loadingFirst`.
     */
    fun refresh(collectNow: Boolean = true, pull: Boolean = true) {
        if (pull && refreshing) return
        if (!pull && (backgroundInFlight || refreshing)) return
        if (pull) refreshing = true else backgroundInFlight = true
        scope.launch {
            try { if (collectNow) repository.refreshNow() else repository.refresh(); error = null }
            catch (e: CancellationException) { throw e }
            catch (e: Exception) { error = res.getString(R.string.inbox_refresh_failed) }
            finally { if (pull) refreshing = false else backgroundInFlight = false }
        }
    }
    val failedCompletions = remember(repository) {
        mutableStateMapOf<String, Pair<persona.core.contracts.api.InboxDoneTarget, InboxBulkAction>>()
    }
    // Clean up all / Mark all as seen / Clear all, and their Undo: one request for them all (the hub skips the ones that took new
    // messages since this list), shown at once, then a notice with Undo when something was filed.
    fun markAll(
        targets: List<persona.core.contracts.api.InboxDoneTarget>, action: InboxBulkAction,
        undoable: Boolean = true, animate: Boolean = false, after: (() -> Unit)? = null,
    ) {
        val done = action != InboxBulkAction.Reopen
        val asked = targets.filter { it.id !in inFlight }
        // An empty ask must not arm: the flag would catch the next hub push.
        if (asked.isEmpty()) return
        // The guard is synchronous and not snapshot state, so a second tap in this frame does not
        // send again. The request still goes out if this screen leaves before the rows move.
        asked.forEach { inFlight.add(it.id) }
        fun applyVisual() {
            asked.forEach { failedCompletions.remove(it.id) }
            haptics(doneHaptic(done))
        }
        fun dropPending() { asked.forEach { pending.remove(it.id); inFlight.remove(it.id) } }
        suspend fun send() {
            try {
                val result = repository.bulk(asked, action)
                // The hub could not write some of them: they are back as they were.
                asked.filter { it.id in result.failed }.forEach { failedCompletions[it.id] = it to action }
                val notice = if (undoable) bulkNotice(res, result, cleaned = action == InboxBulkAction.Dismiss) else null
                if (notice != null) {
                    val back = result.changed.map { persona.core.contracts.api.InboxDoneTarget(it) }
                    val undo = if (back.isEmpty()) null else FeedbackAction(res.getString(R.string.inbox_undo)) { markAll(back, InboxBulkAction.Reopen, undoable = false) }
                    if (undo != null) feedback?.notice(notice, undo) else feedback?.notice(notice)
                }
            }
            catch (e: CancellationException) { throw e }
            catch (e: Exception) { asked.forEach { failedCompletions[it.id] = it to action } }
            finally { dropPending() }
        }
        // A scope that is already cancelled never runs its launch. Send anyway, and do not leave
        // the guard set: the screen is gone, so there is nothing to draw.
        val scopeJob = scope.coroutineContext[kotlinx.coroutines.Job]
        if (scopeJob != null && !scopeJob.isActive) {
            kotlinx.coroutines.CoroutineScope(kotlinx.coroutines.NonCancellable).launch { send() }
            return
        }
        if (!animate) {
            asked.forEach { pending[it.id] = done }
            applyVisual()
            after?.invoke()
            val job = scope.launch { kotlinx.coroutines.withContext(kotlinx.coroutines.NonCancellable) { send() } }
            if (job.isCancelled && !job.isCompleted) {
                dropPending()
                kotlinx.coroutines.CoroutineScope(kotlinx.coroutines.NonCancellable).launch { send() }
            }
            return
        }
        // The large title comes back on this frame, with no spring (InboxView.swift:152-153). The
        // removal waits until that frame has been laid out, then prepares `exitFade` for one frame
        // without arming placement yet so a hub push cannot steal the removal's tap (InboxView.swift:632-634).
        after?.invoke()
        val job = scope.launch {
            try {
                androidx.compose.runtime.withFrameNanos { }
                androidx.compose.runtime.withFrameNanos { }
                listTapMotion.prepareExitFade()
                androidx.compose.runtime.snapshots.Snapshot.sendApplyNotifications()
                androidx.compose.runtime.withFrameNanos { }
                listTapMotion.arm(ListTapMotionKind.State)
                asked.forEach { pending[it.id] = done }
                applyVisual()
            } finally {
                kotlinx.coroutines.withContext(kotlinx.coroutines.NonCancellable) { send() }
            }
        }
        if (job.isCancelled && !job.isCompleted) {
            dropPending()
            kotlinx.coroutines.CoroutineScope(kotlinx.coroutines.NonCancellable).launch { send() }
        }
    }
    /** Prepares exit fade for one frame, then arms and writes on the next frame so a hub push cannot steal the tap. */
    fun animateChange(block: () -> Unit) {
        listTapMotion.prepareExitFade()
        androidx.compose.runtime.snapshots.Snapshot.sendApplyNotifications()
        scope.launch {
            androidx.compose.runtime.withFrameNanos { }
            listTapMotion.arm(ListTapMotionKind.State)
            block()
        }
    }
    fun markDone(id: String, done: Boolean) {
        val record = records[id] ?: return
        if (id in pending) return
        val action = if (done) InboxBulkAction.Done else InboxBulkAction.Reopen
        pending[id] = done
        failedCompletions.remove(id)
        haptics(doneHaptic(done, single = true))
        scope.launch {
            try { repository.setDone(id, done) }
            catch (e: CancellationException) { throw e }
            catch (e: Exception) { failedCompletions[id] = persona.core.contracts.api.InboxDoneTarget(id, record.item.version) to action }
            finally { pending.remove(id) }
        }
    }
    // "Mute this repository / chat / sender" from a row's menu (iOS `InboxStore.mute`): what it covers is filed at once.
    fun mute(record: InboxRecord, option: InboxMuteOption) {
        scope.launch {
            try {
                repository.mute(record, option)
                error = null
                feedback?.notice(res.getString(R.string.inbox_muted_notice, option.name))
            } catch (e: CancellationException) { throw e }
            catch (e: Exception) { if (feedback?.failure(e) != true) error = res.getString(R.string.inbox_failed_restored) }
        }
    }
    var showMutes by remember { mutableStateOf(false) }
    var showRules by remember { mutableStateOf(false) }
    var ruleTarget by remember { mutableStateOf<InboxPolicyTarget?>(null) }
    val sectionReader = rememberInboxSectionReader(conversations, conversationList, records)
    if (showMutes) InboxMutesSheet(repository, onDismiss = { showMutes = false })
    if (showRules) InboxRulesSheet(repository, onDismiss = { showRules = false })
    ruleTarget?.let { InboxPolicySheet(repository, it, onDismiss = { ruleTarget = null }) }
    // The host stays composed, so a one-shot effect would not run when Inbox returns to the front.
    // iOS reloads every time the list appears (InboxView.swift:219–220) and shows a spinner only from
    // `.refreshable` (InboxView.swift:916) or an empty first load (InboxView.swift:917).
    val lifecycle = LocalLifecycleOwner.current.lifecycle
    LaunchedEffect(repository, lifecycle) {
        lifecycle.repeatOnLifecycle(Lifecycle.State.RESUMED) {
            refresh(collectNow = false, pull = false)
        }
    }
    // The title while the hub is out of reach opens why, since when and "Retry now" in place (iOS ConnectionTitle).
    val connectionDetails = rememberConnectionDetails(shownConnection, { navigator.navigate(ConnectionSettingsRoute) }, onRetry = { refresh(collectNow = false) })
    fun allTargets(shown: List<InboxGroup>) = bulkTargets(shown, records)
    // Clean up all keeps its scope whatever the bar shows: every open item under the filters and search.
    val bulk = allTargets(displayGroups)
    // Groups (#2576): one row is one item. Only open ones Persona isn't working on can be picked.
    fun rowRecords(row: InboxRow): List<InboxRecord> = listOfNotNull(records[row.id])
    fun selectable(row: InboxRow): Boolean = row.type == InboxRowType.Item && !working(row) &&
        rowRecords(row).any { !(pending[it.item.id] ?: it.item.isDone) }
    val zone = java.time.ZoneId.systemDefault()
    val locale = currentLocale
    val now = java.time.Instant.now()
    // Done's counts from the hub (#3016), per manager ("" the default): only with no filter or search on ([filtering]);
    // then each group shows "n" of what is loaded and Show older pages back from the newest.
    fun subsOf(group: InboxGroup, manager: String = ""): List<InboxShownGroup> = if (group.folded) emptyList() else
        inboxSectionGroups(group.id, group.rows, grouping, now, zone, res, locale, ::rowRecords,
            cardAt = { row -> interactions[row.id]?.createdAt?.let { runCatching { java.time.Instant.parse(it) }.getOrNull() } },
            doneDays = if (group.id == "done" && !filtering) history.days[manager] else null)
    // What "Select all" picks: the rows shown now (a folded group is picked with its own tick).
    fun shownRows(group: InboxGroup): List<InboxRow> = subsOf(group).takeIf { it.isNotEmpty() }
        ?.filter { InboxGroups.isOpen(group.id, it.key, folds) }?.flatMap { it.rows } ?: group.visibleRows
    val visiblePickable = if (!selecting) emptyList() else groups.flatMap(::shownRows).filter(::selectable).map { it.id }.distinct()
    val allPicked = visiblePickable.isNotEmpty() && visiblePickable.all { it in picked }
    val foldedPicked = picked.count { it !in visiblePickable }
    val pickedTargets = picked.mapNotNull { records[it] }
        .filterNot { pending[it.item.id] ?: it.item.isDone }.distinctBy { it.item.id }
        .map { persona.core.contracts.api.InboxDoneTarget(it.item.id, it.item.version) }
    var pickedMenu by remember { mutableStateOf(false) }
    var jumpMenu by remember { mutableStateOf<InboxJumpMenu?>(null) }
    var jumpTo by remember { mutableStateOf<String?>(null) }
    val listIndex = remember { InboxListIndex() }
    fun setFolds(value: List<String>) = filter(currentFilters.copy(folds = value))
    // Done's history: the first page when Done opens, then "Show older".
    val historyManagers = listOf(historyManager.ifEmpty { null })
    val doneOpen = groups.any { it.id == "done" && !it.folded }
    // Another assistant is another history (a new [history]): read it too when Done is already open.
    LaunchedEffect(doneOpen, history) { if (doneOpen && !history.started) history.start(historyManagers, zone) }
    // With no done item in the list, the hub's counts say whether Done has any (#9053): read once on entry, not its pages,
    // after the list is in (cached or read), so a list that holds done items asks nothing.
    val listIn = snapshot.refreshes > 0 || snapshot.records.isNotEmpty()
    val countDone = listIn && !filtering && result.groups.none { it.id == InboxBar.DONE }
    LaunchedEffect(countDone, history) { if (countDone) history.count(historyManagers, zone) }
    // Done's total with the hub's counts (#3016): what its groups add up to, folded or not, for the Done chip and Done's
    // title alike; with a filter or search on, or no counts, what is loaded.
    fun doneTotal(done: InboxGroup, manager: String): Int {
        val days = history.days[manager]?.takeIf { !filtering } ?: return done.count
        return inboxSectionGroups("done", done.rows, grouping, now, zone, res, locale, ::rowRecords, cardAt = { null }, doneDays = days)
            .takeIf { it.isNotEmpty() }?.sumOf { it.count } ?: done.count
    }
    // Show older at the end of Done: for the managers whose hub doesn't count, and for all of them while filtering.
    val olderManagers = if (filtering) historyManagers else historyManagers.filter { it.orEmpty() !in history.days }
    val showOlder = doneOpen && !history.counting && olderManagers.isNotEmpty() && (history.more || history.failed || history.loading)
    val scopeText = bulkScope(res, "", sources.firstOrNull { it.first == source && source.isNotEmpty() }?.second,
        if (kind == "agent") res.getString(R.string.inbox_kind_agent) else kindLabel(res, kind), query)
    var showMore by remember { mutableStateOf(false) }
    // Clean up all files every open item of the current view (filters and search) in one request, recorded as dismissed
    // (done_by=dismissed); over BULK_CONFIRM_AT items or more it asks first, for the items shown when it was asked.
    var confirmBulk by remember { mutableStateOf<List<persona.core.contracts.api.InboxDoneTarget>?>(null) }
    fun cleanAll() {
        // Clean up all (InboxView.swift:703). Asking first does not move rows yet.
        if (bulk.size >= BULK_CONFIRM_AT) confirmBulk = bulk else {
            markAll(bulk, InboxBulkAction.Dismiss, animate = true) // InboxView.swift:703
        }
    }
    if (showMore) {
        // By source / By time (#2576) sits here rather than in a row of its own over the list (#9053): QuickFilterBar has
        // no trailing control (only the Agents tab's home capsule has one), its five choices already run past a phone's
        // width, and "List controls" puts grouping in a grouping control or the ⋯, where the Agents list's QuickFilterBar
        // form keeps its own. Only where it applies: Done is always by time and In progress never grouped.
        val groupings = if (chosen == InboxBar.DONE || chosen == InboxBar.WORKING) emptyList() else listOf(
            JAction(res.getString(R.string.inbox_grouping_source), checked = grouping != InboxGroups.TIME, section = res.getString(R.string.inbox_grouping_title)) {
                filter(currentFilters.copy(grouping = InboxGroups.SOURCE)) },
            JAction(res.getString(R.string.inbox_grouping_time), checked = grouping == InboxGroups.TIME) {
                filter(currentFilters.copy(grouping = InboxGroups.TIME)) },
        )
        JActionSheet(title = stringResource(R.string.inbox_title), onDismiss = { showMore = false },
            actions = groupings + listOf(
                JAction(bulkLabel(res, bulk.size, scopeText), PhIcons.Checks, enabled = bulk.isNotEmpty(), separatorBefore = groupings.isNotEmpty()) { cleanAll() },
                JAction(res.getString(R.string.inbox_mutes_title), PhIcons.Prohibit, separatorBefore = true) { showMutes = true },
                JAction(res.getString(R.string.inbox_rules_title), PhIcons.Target) { showRules = true },
            ) + listOfNotNull(
                // source subscriptions (#2041); Inbox Sources once the hub says so (#5278)
                if (subscriptionsShown) {
                    val label = res.getString(if (inboxSourcesShown) R.string.inbox_sources else R.string.inbox_subscriptions)
                    JAction(label, PhIcons.ArrowsClockwise) {
                        navigator.navigate(ExtensionUiRoute(SUBSCRIPTIONS_UI, "pages", label))
                    }
                } else null,
            ),
            note = if (scopeText == null) stringResource(R.string.inbox_bulk_note_all) else stringResource(R.string.inbox_bulk_note_scope, scopeText))
    }
    // One picked row: what its long-press offered before the selection took it (Hand to AI, mutes, rules).
    if (pickedMenu) picked.singleOrNull()?.let { one ->
        val record = records[one]
        JActionSheet(title = record?.item?.title.orEmpty(), onDismiss = { pickedMenu = false }, actions = buildList {
            if (record?.isAssistantRow != true) {
                add(JAction(res.getString(R.string.inbox_hand_to_ai), PhIcons.Robot) { handingOff = one; endSelection() })
            }
            if (record != null) addAll(muteAndRuleActions(res, record, { mute(record, it); endSelection() },
                { ruleTarget = InboxPolicyTarget.of(record, it); endSelection() }))
        })
    }
    jumpMenu?.let { menu ->
        JActionSheet(title = stringResource(R.string.inbox_jump_to), onDismiss = { jumpMenu = null }, actions = menu.groups.map { group ->
            JAction("${group.title} · ${group.count}") {
                if (!InboxGroups.isOpen(menu.section, group.key, folds)) setFolds(InboxGroups.toggle(menu.section, group.key, folds))
                jumpTo = "group:${menu.prefix}${menu.section}/${group.key}"
            }
        } + listOf(
            JAction(res.getString(R.string.inbox_fold_all), PhIcons.CaretUp, separatorBefore = true) {
                setFolds(InboxGroups.setAll(menu.section, menu.groups.map { it.key }, open = false, flipped = folds)) },
            JAction(res.getString(R.string.inbox_unfold_all), PhIcons.CaretDown) {
                setFolds(InboxGroups.setAll(menu.section, menu.groups.map { it.key }, open = true, flipped = folds)) },
        ))
    }
    confirmBulk?.let { targets ->
        JDialog(onDismiss = { confirmBulk = null },
            title = stringResource(R.string.inbox_bulk_confirm_clean_title),
            text = bulkConfirmText(res, targets.size, scopeText),
            confirm = res.getQuantityString(R.plurals.inbox_bulk_confirm_clean, targets.size, targets.size) to {
                confirmBulk = null
                markAll(targets, InboxBulkAction.Dismiss, animate = true) // InboxView.swift:703, after the ask
            })
    }
    if (showFilters) {
        JActionSheet(title = stringResource(R.string.inbox_filter_title), onDismiss = { showFilters = false }, actions = listOf(
            JAction(stringResource(R.string.inbox_filter_source, sources.firstOrNull { it.first == source }?.second ?: stringResource(R.string.inbox_all_sources)), PhIcons.Tray) { filterPage = "source" },
            JAction(stringResource(R.string.inbox_filter_kind, kindLabel(res, kind).ifBlank { if (kind == "agent") res.getString(R.string.inbox_kind_agent) else res.getString(R.string.inbox_all_kinds) }), PhIcons.FunnelSimple) { filterPage = "kind" },
            JAction(stringResource(R.string.inbox_filter_clear), enabled = source.isNotEmpty() || kind.isNotEmpty()) {
                // Clear Filters (InboxView.swift:768).
                animateChange { filter(currentFilters.copy(source = "", kind = "")) }
            },
        ))
    }
    if (filterPage == "source") JSheet(onDismiss = { filterPage = null }, title = stringResource(R.string.inbox_source)) {
        // The source list can grow with connected accounts, so keep the choices scrollable.
        androidx.compose.foundation.lazy.LazyColumn {
            itemsIndexed(sources) { index, option ->
                JGroupItem(GroupPosition.of(index, sources.size)) {
                    JRow(option.second, onClick = {
                        // A source chip (InboxView.swift:1263, :1294). A section-bar choice stays instant
                        // (InboxView.swift:969): the list swaps at once, and that chip passes animated: false (:1292).
                        animateChange { filter(currentFilters.copy(source = option.first)); filterPage = null }
                    },
                        trailing = { JRadio(source == option.first, null) })
                }
            }
        }
    }
    if (filterPage == "kind") JActionSheet(title = stringResource(R.string.inbox_kind), onDismiss = { filterPage = null }, actions =
        (listOf("" to res.getString(R.string.inbox_all_kinds)) + kindFilterOptions).map { (value, label) ->
            JAction(label, checked = kind == value) {
                // A kind chip (InboxView.swift:1263, :1294).
                animateChange { filter(currentFilters.copy(kind = value)) }
            }
        })
    // Loading shows only while there is nothing to show yet: a row put in over a shown list pushes it down, then goes.
    val loadingFirst = snapshot.loading && snapshot.records.isEmpty() && snapshot.interactions.isEmpty()
    val failedEmpty = snapshot.records.isEmpty() && !loadingFirst && (error ?: snapshot.error) != null
    // One remembered list per filter, and in it one scroll per choice of the section bar (#9053). The query, the folds
    // and the choice stay out of the key: a new key recreates the page, so typing would drop the search field's focus
    // (and the keystroke), folding a section would jump to the top, and a search moving the bar to All would do both.
    listStates.SaveableStateProvider(listOf(source, kind).toString()) {
        val scrolls = InboxBar.CHOICES.map { rememberLazyListState() }
        // A search keeps the scroll of the choice it set aside, so the list stays put while typing.
        val scroll = scrolls[InboxBar.CHOICES.indexOf(if (query.isEmpty()) chosen else bar.saved ?: chosen).coerceAtLeast(0)]
        // "Jump to" scrolls once the page has been built with the group open (the list scrolls only by index).
        LaunchedEffect(jumpTo) {
            val key = jumpTo ?: return@LaunchedEffect
            androidx.compose.runtime.withFrameNanos { }
            androidx.compose.runtime.withFrameNanos { }
            // Jump to (InboxView.swift:924, :1099) scrolls with Theme.Motion.standard. The row swap itself stays instant.
            listIndex.positions[key]?.let { at ->
                listTapMotion.onProgrammaticScroll()
                scroll.animateScrollToItem((scroll.layoutInfo.totalItemsCount - listIndex.counted).coerceAtLeast(0) + at)
            }
            jumpTo = null
        }
        BindListTapMotion(listTapMotion)
        JListPage(
            title = if (selecting) androidx.compose.ui.res.pluralStringResource(R.plurals.inbox_selected_count, picked.size, picked.size)
                else stringResource(R.string.inbox_title),
            // An offline assistant's nothing is not "all caught up": the empty state says it is offline instead.
            summary = if (selecting || assistant.offline != null && snapshot.records.isEmpty() && snapshot.interactions.isEmpty()) null
                else inboxSummary(res, overall, ::working),
            subtitle = if (selecting) foldedPicked.takeIf { it > 0 }?.let { androidx.compose.ui.res.pluralStringResource(R.plurals.inbox_picked_folded, picked.size, picked.size, it) }
                else inboxTitle(shownConnection).takeIf { shownConnection != ConnectionState.Online },
            titleBusy = !selecting && shownConnection in setOf(ConnectionState.Connecting, ConnectionState.Updating),
            onTitleClick = if (selecting || shownConnection == ConnectionState.Online) null else connectionDetails,
            onBack = if (selecting) ({ endSelection() }) else null,
            largeTitle = !selecting,
            state = scroll, refreshing = refreshing, onRefresh = { refresh() },
            listScroll = listTapScroll(listTapMotion),
            actions = if (selecting) ({
                // Select all: the rows shown now; then Deselect all takes them back out.
                JTextButton(stringResource(if (allPicked) R.string.inbox_deselect_all else R.string.inbox_select_all), {
                    picked = if (allPicked) picked - visiblePickable.toSet() else picked + visiblePickable
                }, enabled = visiblePickable.isNotEmpty())
                JButton(stringResource(R.string.inbox_mark_done), {
                    // The title returns at once (InboxView.swift:152-153). The row then fades where
                    // that title left it (InboxView.swift:632-634).
                    markAll(pickedTargets, InboxBulkAction.Done, animate = true, after = { endSelection() })
                },
                    size = JButtonSize.Small, enabled = pickedTargets.isNotEmpty())
                if (picked.size == 1) JIconButton(PhIcons.DotsThree, stringResource(R.string.inbox_more), { pickedMenu = true })
            }) else ({
                if (mailTransport != null && mailAccess == MailAccess.Live) {
                    JIconButton(PhIcons.PencilSimple, stringResource(R.string.inbox_compose_mail), {
                        composeDraft = MailDraft(accountId = "")
                    })
                }
                // The dot is color only: TalkBack hears the active filter in the description (#3729).
                val filtered = source.isNotEmpty() || kind.isNotEmpty()
                JIconButton(PhIcons.FunnelSimple, stringResource(if (filtered) R.string.inbox_filter_on else R.string.inbox_filter),
                    { showFilters = true }, badge = filtered)
                JIconButton(PhIcons.DotsThree, stringResource(R.string.inbox_more), { showMore = true })
            }),
        ) {
          listIndex.positions.clear()
          val tapScope = ListTapLazyScope(this, listTapMotion, scroll)
          val keyed = KeyedListScope(tapScope, listIndex.positions)
          with(keyed) {
            item("filters") {
                // The section bar (#9053): Needs you's rows in its tone, In progress and FYI's in the neutral one, Done and All none.
                val bays = barView.choices.map { choice ->
                    Bay(inboxChoiceTitle(choice.id), choice.count, Status.NeedsYou.takeIf { choice.id == InboxBar.ACTION },
                        countTone = when (choice.tone) { InboxBar.Tone.Needs -> Status.NeedsYou; InboxBar.Tone.Neutral -> Status.Idle; null -> null })
                }
                if (isGlass) {
                    val capsuleGroupings = if (chosen == InboxBar.DONE || chosen == InboxBar.WORKING) emptyList() else listOf(
                        JAction(res.getString(R.string.inbox_grouping_source), checked = grouping != InboxGroups.TIME) {
                            filter(currentFilters.copy(grouping = InboxGroups.SOURCE))
                        },
                        JAction(res.getString(R.string.inbox_grouping_time), checked = grouping == InboxGroups.TIME) {
                            filter(currentFilters.copy(grouping = InboxGroups.TIME))
                        },
                    )
                    JInboxSectionCapsule(
                        bays = bays,
                        selected = barView.choices.indexOfFirst { it.id == chosen },
                        onSelect = { landing?.picked(); moveBar(InboxBar.picked(bar, barView.choices[it].id)) },
                        groupLabel = stringResource(R.string.inbox_grouping_title),
                        groupActions = capsuleGroupings,
                    )
                    Spacer(Modifier.height(Tokens.Space.xs))
                    GlassInboxFilterStrip(
                        sources = sourceOptions,
                        kinds = kindFilterOptions,
                        activeSource = source,
                        activeKind = kind,
                        onSelectSource = { next -> animateChange { filter(currentFilters.copy(source = next)) } },
                        onSelectKind = { next -> animateChange { filter(currentFilters.copy(kind = next)) } },
                        onClearAll = { animateChange { filter(currentFilters.copy(source = "", kind = "")) } },
                    )
                } else {
                    // A search or a link can choose All or Done past the edge of a narrow bar: it scrolls them into sight, while
                    // Needs you stays at the start so its count is in sight from every choice (DESIGN_SYSTEM 3.4), and the edge
                    // that cuts choices off fades so the strip reads as one that scrolls.
                    QuickFilterBar(bays, selected = barView.choices.indexOfFirst { it.id == chosen },
                        onSelect = { landing?.picked(); moveBar(InboxBar.picked(bar, barView.choices[it].id)) }, revealSelected = true, pinFirst = true,
                        fadeCut = true)
                }
                Spacer(Modifier.height(Tokens.Space.s))
                JSearchField(query, { filter(currentFilters.copy(query = it)) }, stringResource(R.string.inbox_search_hint))
            }
            // New items waiting outside the list for triage (#1773): one line over the first item until triage is ok.
            InboxTriageText.banner(res, snapshot.triage)?.let { banner -> item("triage") { InboxTriageNotice(banner) } }
            // Sources the hub could not read this round (#1849): what they show is the last round's.
            InboxTriageText.unread(res, snapshot.triage)?.let { text -> item("triage-unread") { InboxUnreadNotice(text) } }
            if (loadingFirst) item("loading") { JLoading(text = stringResource(R.string.inbox_loading)) }
            if (!failedEmpty) (error ?: snapshot.error?.takeIf { feedback == null || connection == ConnectionState.Online })?.let { message ->
                item("error") { JNotice(message, actionLabel = stringResource(R.string.inbox_retry),
                    onAction = { refresh(collectNow = false) }) }
            }
            if (failedCompletions.isNotEmpty()) item("completion-error") {
                JNotice(stringResource(R.string.inbox_failed_restored), actionLabel = stringResource(R.string.inbox_retry), onAction = {
                    failedCompletions.values.toList().groupBy({ it.second }, { it.first }).forEach { (action, targets) ->
                        markAll(targets, action, undoable = false)
                    }
                })
            }
            if (preferenceError) item("preference-error") { StatusText(stringResource(R.string.inbox_filters_not_saved), isError = true) }
            sectionReader.notice?.let { notice -> item("section-read-result") {
                JNotice(notice, actionLabel = sectionReader.retry?.let { stringResource(R.string.inbox_retry) },
                    onAction = sectionReader.retry)
            } }
            // Nothing for the choice to draw (#9053): All caught up only when nothing is open anywhere, which under All
            // still draws Done below it; a chosen section with nothing says so in its own words, the counts right above.
            barView.empty?.takeIf { !loadingFirst }?.let { empty -> item("empty") {
                val filtered = query.isNotEmpty() || source.isNotEmpty() || kind.isNotEmpty()
                when {
                    // The hub's list leaves out an assistant that is offline: its inbox is not empty, it is out of reach.
                    // With items kept, a search or filter that finds none is "no match", not offline.
                    assistant.offline != null && snapshot.records.isEmpty() && snapshot.interactions.isEmpty() -> JAssistantOffline(assistant)
                    !filtered && failedEmpty -> JEmptyState(stringResource(R.string.inbox_empty_failed_title), stringResource(R.string.inbox_empty_failed_text), action = {
                        JButton(stringResource(R.string.inbox_retry), { refresh() }, enabled = !refreshing, kind = JButtonKind.Secondary)
                    })
                    empty == InboxBar.Empty.Section -> InboxSectionEmpty(chosen)
                    // A source or kind that hides every open item is not "all caught up": the Inbox outside it may still
                    // hold some. The filter mark and Clear filters are the way back (#7550).
                    empty == InboxBar.Empty.Filtered -> JEmptyState(
                        stringResource(R.string.inbox_empty_filtered_title), stringResource(R.string.inbox_empty_filtered_text),
                        icon = PersonaIcons.filter,
                        action = {
                            JButton(stringResource(R.string.inbox_filter_clear), {
                                // Clear Filters / Clear Search (InboxView.swift:768).
                                animateChange { filter(currentFilters.copy(source = "", kind = "", query = "")) }
                            }, kind = JButtonKind.Secondary)
                        },
                    )
                    empty == InboxBar.Empty.NoMatch -> JEmptyState(stringResource(R.string.inbox_empty_search_title), stringResource(R.string.inbox_empty_search_text, query.trim()), icon = PhIcons.MagnifyingGlass)
                    snapshot.records.isEmpty() && snapshot.interactions.isEmpty() -> JEmptyState(stringResource(R.string.inbox_empty_title),
                        stringResource(inboxEmptyText(sourceVisibility)), icon = PhIcons.Tray)
                    else -> JEmptyState(stringResource(R.string.inbox_empty_caught_up_title), stringResource(R.string.inbox_empty_caught_up_text), icon = PhIcons.CheckCircle)
                }
            } }
            // Rows of a group (#2576: in a selection a tap picks, a long-press starts one).
            fun rows(prefix: String, shown: List<InboxRow>) = itemsIndexed(shown, key = { _, row -> "$prefix${row.type}:${row.id}" }, contentType = { _, row -> row.type }) { index, row ->
                val position = GroupPosition.of(index, shown.size)
                val selection = InboxRowSelection(checked = if (selecting) row.id in picked else null, selectable = selectable(row),
                    onToggle = { picked = if (row.id in picked) picked - row.id else picked + row.id },
                    onStart = { selecting = true; picked = setOf(row.id); haptics(Haptic.Tap) })
                if (row.type == InboxRowType.Interaction) {
                    interactions[row.id]?.let { interaction ->
                        val openInteraction: () -> Unit = {
                            if (!selecting) {
                                // A card outside any session (`personad ask` in a plain terminal) has no chat to open.
                                if (interaction.sessionId.isNotBlank()) navigator.navigate(ChatRoute(interaction.machineId, interaction.sessionId))
                                else onOpenCard?.invoke(persona.core.navigation.RequestRef(interaction.machineId, interaction.id))
                                    ?: navigator.selectTab(persona.core.navigation.AppTab.Agents)
                            }
                        }
                        if (isGlass) {
                            GlassInboxAgentRow(
                                interaction = interaction,
                                machineName = snapshot.machines[interaction.machineId],
                                position = position,
                                dimmed = selecting,
                                onClick = openInteraction,
                            )
                        } else {
                            MessageItem(sender = "Persona", title = interaction.sessionTitle.ifBlank { interaction.title },
                                preview = interaction.summary ?: interaction.title, position = position, status = Status.NeedsYou, dimmed = selecting,
                                onClick = openInteraction)
                        }
                    }
                } else records[row.id]?.let { record ->
                    val done = pending[record.item.id] ?: record.item.isDone
                    InboxItemRow(record, repository, navigator, composers, position, done, record.item.id in pending, onlineSince,
                        onDone = { markDone(record.item.id, !done) }, onHandOff = { handingOff = record.item.id },
                        onMute = { option -> mute(record, option) }, onRule = { ruleTarget = InboxPolicyTarget.of(record, it) },
                        selection = selection)
                }
            }
            val sections: (String, List<InboxGroup>) -> Unit = { prefix, shownGroups -> shownGroups.forEach { group ->
                val manager = prefix.removeSuffix(":")
                val subs = subsOf(group, manager)
                val sectionTargets = allTargets(listOf(group))
                val sectionPickable = if (selecting && subs.isEmpty()) group.rows.filter(::selectable).map { it.id } else emptyList()
                item("section:$prefix${group.id}", contentType = "section") {
                    val title = if (group.id == "working") group.title.text() else inboxGroupTitle(group)
                    val overflowAction = when {
                        selecting -> null
                        group.id == "done" && subs.isNotEmpty() -> JAction(stringResource(R.string.inbox_jump_to),
                            onClick = { jumpMenu = InboxJumpMenu(prefix, group.id, subs) })
                        group.id in setOf("action", "fyi") && sectionReader.hasUnread(group) ->
                            JAction(stringResource(R.string.inbox_conversations_read_all), enabled = !sectionReader.busy,
                                onClick = { sectionReader.read(group) })
                        group.id == "noise" -> JAction(stringResource(R.string.inbox_clear_all), enabled = !sectionReader.busy,
                            onClick = { markAll(allTargets(listOf(group)), InboxBulkAction.Done, animate = true) }) // InboxView.swift:1143
                        else -> null
                    }
                    // Inside a choice the section's title stays as a heading with its actions and does not fold, as
                    // In progress's always does (#9053); under All a tap folds it as before.
                    val foldable = chosen == InboxBar.ALL && group.id != "working"
                    JSectionHeader(
                        title = title,
                        // Done with the hub's counts (#3016): all of it, as its groups add up, not just what is loaded.
                        count = if (group.id == "done") doneTotal(group, manager) else group.count,
                        status = when (group.id) { "action" -> Status.NeedsYou; "working" -> Status.Working; else -> null },
                        trailing = if (!foldable) null else if (group.folded) stringResource(R.string.inbox_expand) else stringResource(R.string.inbox_collapse),
                        open = !group.folded, trailingIsFoldAction = true,
                        onClick = if (!foldable) null else ({
                            // The fold animates (InboxView.swift:1127, Done at :1029).
                            animateChange { filter(currentFilters.copy(expanded = if (group.id in expanded) expanded - group.id else expanded + group.id)) }
                        }),
                        // A section without groups is one group: its tick picks it all, folded or not.
                        leading = if (sectionPickable.isNotEmpty()) ({ InboxGroupTick(sectionPickable, picked, title) { picked = it } }) else null,
                        overflowActions = listOfNotNull(overflowAction),
                        action = when {
                            selecting -> null
                            group.id == "done" && subs.isNotEmpty() -> ({
                                JTextButton(stringResource(R.string.inbox_jump_to), { jumpMenu = InboxJumpMenu(prefix, group.id, subs) })
                            })
                            group.id in setOf("action", "fyi") && sectionReader.hasUnread(group) -> ({
                                JTextButton(stringResource(R.string.inbox_conversations_read_all), { sectionReader.read(group) },
                                    enabled = !sectionReader.busy)
                            })
                            group.id == "noise" -> ({
                                JTextButton(stringResource(R.string.inbox_clear_all), {
                                    markAll(allTargets(listOf(group)), InboxBulkAction.Done, animate = true) // InboxView.swift:1143
                                }, enabled = !sectionReader.busy)
                            })
                            else -> null
                        },
                    )
                }
                if (subs.isEmpty()) rows(prefix, group.visibleRows)
                else subs.forEach { sub ->
                    val open = InboxGroups.isOpen(group.id, sub.key, folds)
                    val clear = when (group.id) {
                        "fyi" -> Triple(res.getString(R.string.inbox_mark_all_seen), res.getString(R.string.inbox_group_fyi),
                            R.plurals.inbox_mark_all_in_seen)
                        "noise" -> Triple(res.getString(R.string.inbox_clear_all), res.getString(R.string.inbox_section_folded),
                            R.plurals.inbox_clear_all_in)
                        else -> null
                    }
                    val groupTargets = bulkTargets(listOf(group.copy(rows = sub.rows)), records)
                    // FYI and Collapsed group titles offer that group or the whole section (#10425). Needs you,
                    // In progress and Done keep their existing titles; one group keeps the section's direct action.
                    val clearChoices = if (selecting || clear == null || groupTargets.isEmpty() ||
                        sectionTargets.size <= groupTargets.size) emptyList() else {
                        listOf(
                            JAction(res.getQuantityString(clear.third, sub.count, sub.count, sub.title), subtitle = scopeText) {
                                markAll(groupTargets, InboxBulkAction.Done, animate = true)
                            },
                            JAction(res.getQuantityString(clear.third, group.count, group.count, clear.second), subtitle = scopeText) {
                                markAll(sectionTargets, InboxBulkAction.Done, animate = true)
                            },
                        )
                    }
                    // A group's title stays at the top while its rows scroll under it (#2059 "Jump to").
                    stickyHeader("group:$prefix${group.id}/${sub.key}", contentType = "group") { index ->
                        val pickable = sub.rows.filter(::selectable).map { it.id }
                        val n = sub.rows.count { it.id in picked }
                        // Opaque only while pinned over the rows: at rest it shows the page (Glass's light) as the other headers do.
                        val pinned by remember(index) { derivedStateOf { scroll.firstVisibleItemIndex >= index } }
                        JSectionHeader(sub.title, if (pinned) Modifier.background(PersonaTheme.palette.background) else Modifier,
                            count = sub.count, sub = true, open = open,
                            onClick = {
                                // A group's fold (InboxView.swift:458).
                                animateChange { setFolds(InboxGroups.toggle(group.id, sub.key, folds)) }
                            },
                            leading = if (selecting && pickable.isNotEmpty()) ({ InboxGroupTick(pickable, picked, sub.title) { picked = it } }) else null,
                            trailing = if (selecting && n > 0) androidx.compose.ui.res.pluralStringResource(R.plurals.inbox_group_picked, n, n) else null,
                            action = if (clearChoices.isEmpty() || clear == null) null else ({
                                var menu by remember(group.id, sub.key) { mutableStateOf(false) }
                                JTextButton(clear.first, { menu = true }, icon = PersonaIcons.expand)
                                // Keep the pull-down at every text size; multiline choices prevent its capped width
                                // from ellipsizing the count, destination, verb or filter scope at larger text.
                                JMenu(menu, { menu = false }, clearChoices,
                                    multilineActions = res.configuration.fontScale > 1f)
                            }))
                    }
                    if (open) rows("$prefix${group.id}/${sub.key}:", sub.rows)
                    // A counted group not all in (#3016): its own first page as it opens, then Show older inside it.
                    val pageId = "$manager/${sub.key}"
                    if (open && !sub.whole && pageId !in history.endedGroups) item("older:$prefix${group.id}/${sub.key}", contentType = "older") {
                        val end = InboxGroups.span(sub.key, now, zone).second
                        LaunchedEffect(pageId) { if (!history.fetched(pageId)) history.loadGroup(manager, sub.key, end) }
                        InboxOlderRow(pageId in history.loadingGroups, pageId in history.failedGroups) { history.loadGroup(manager, sub.key, end) }
                    }
                }
            } }
            sections(if (historyManager.isEmpty()) "" else "$historyManager:", groups)
            // Older done items, after the list's few days (#2576).
            if (showOlder) item("done-older") {
                InboxOlderRow(history.loading, history.failed) { history.load(olderManagers) }
            }
            tapScope.commit()
          }
          listIndex.counted = keyed.counted
        }
    }
    handingOff?.let { id -> records[id] }?.let { record ->
        InboxHandOffSheet(repository, record, navigator, onDismiss = { handingOff = null })
    }
    composeDraft?.let { draft ->
        mailTransport?.let { transport ->
            ComposeMailDialog(
                transport = transport,
                draft = draft,
                onDone = { composeDraft = null },
            )
        }
    }
}

/**
 * The current assistant's share of the hub's inbox: its items, and the cards of its agents (a card is its own
 * manager's; one that names none is the default's, as an item is).
 */
internal fun InboxSnapshot.ofAssistant(assistant: persona.core.contracts.api.CurrentAssistant): InboxSnapshot {
    val mine = records.filter { assistant.owns(it.manager) }
    val cards = interactions.filter { assistant.ownsOn(it.machineId, it.manager) }
    // The merged list's triage is the default manager's: said only on its own inbox.
    val default = assistant.roster?.defaultManagerId ?: assistant.choice.defaultId
    val sorting = triage.takeIf { assistant.id == default }
    return if (mine.size == records.size && cards.size == interactions.size && sorting == triage) this
    else copy(records = mine, interactions = cards, triage = sorting)
}

/** A section's title by its id; :core:domain's titles are Chinese, and a group this app does not know keeps its own. */
@Composable
private fun inboxGroupTitle(group: InboxGroup): String = when (group.id) {
    "action" -> stringResource(R.string.inbox_bay_needs_you)
    "working" -> stringResource(R.string.inbox_group_working)
    "fyi" -> stringResource(R.string.inbox_group_fyi)
    "noise" -> stringResource(R.string.inbox_section_folded)
    "done" -> stringResource(R.string.inbox_done)
    else -> group.title.text()
}

/** A choice's name in the section bar (#9053): the words the section's own heading uses, In progress for any hub. */
@Composable
private fun inboxChoiceTitle(id: String): String = stringResource(when (id) {
    InboxBar.ACTION -> R.string.inbox_bay_needs_you
    InboxBar.WORKING -> R.string.inbox_section_working
    InboxBar.FYI -> R.string.inbox_group_fyi
    InboxBar.DONE -> R.string.inbox_done
    else -> R.string.inbox_bay_all
})

/** A chosen section with nothing in it (#9053): its own words, never "All caught up" while other sections hold rows. */
@Composable
private fun InboxSectionEmpty(chosen: String) {
    val (title, text) = when (chosen) {
        InboxBar.WORKING -> R.string.inbox_empty_working_title to R.string.inbox_empty_section_text
        InboxBar.FYI -> R.string.inbox_empty_fyi_title to R.string.inbox_empty_section_text
        InboxBar.DONE -> R.string.inbox_empty_done_title to R.string.inbox_empty_done_text
        else -> R.string.inbox_empty_action_title to R.string.inbox_empty_section_text
    }
    JEmptyState(stringResource(title), stringResource(text), icon = if (chosen == InboxBar.DONE) PersonaIcons.inbox else PersonaIcons.success)
}

/** A tier's name, as the item's header tags it. */
@Composable
internal fun inboxTierTitle(tier: InboxTier): String = stringResource(when (tier) {
    InboxTier.Action -> R.string.inbox_bay_needs_you
    InboxTier.Fyi -> R.string.inbox_tier_fyi
    InboxTier.Noise -> R.string.inbox_section_folded
})

@Composable
internal fun StatusText(text: String, isError: Boolean = false) {
    JNotice(text, Modifier.padding(vertical = Tokens.Space.s), status = if (isError) Status.NeedsYou else Status.Unknown)
}

/**
 * The domain list already places hub-working items in In progress. Move legacy agent-working rows into that same
 * section, removing each from its tier first, so mixed old and new hubs still show every item exactly once.
 */
internal fun inboxDisplayGroups(
    inbox: InboxListResult,
    workingTitle: persona.core.contracts.CoreText,
    working: (InboxRow) -> Boolean,
): List<InboxGroup> {
    val workingRows = inbox.groups.filter { it.id != "done" }.flatMap { it.rows }.filter(working)
    val tiers = inbox.groups.mapNotNull { group ->
        if (group.id == "done") group else {
            val rows = group.rows.filterNot(working)
            if (rows.isEmpty()) null else group.copy(rows = rows, visibleRows = group.visibleRows.filterNot(working))
        }
    }
    return buildList {
        tiers.firstOrNull { it.id == "action" }?.let(::add)
        if (workingRows.isNotEmpty()) add(InboxGroup("working", workingTitle, workingRows, workingRows, false))
        addAll(tiers.filter { it.id != "action" })
    }
}

/**
 * The page's one line, for the whole inbox: give it the unfiltered list, since a search or a filter that shows nothing
 * is not "All caught up". Rows an agent is working on are Persona's, not the user's to answer.
 */
internal fun inboxSummary(res: Resources, inbox: InboxListResult, working: (InboxRow) -> Boolean): String {
    val open = inbox.groups.filter { it.id != "done" }
    val busy = open.sumOf { group -> group.rows.count(working) }
    val needs = needsYouCount(inbox, working)
    return if (needs == 0) res.getString(R.string.inbox_summary_clear) else res.getString(R.string.inbox_summary, needs, busy)
}

private fun kindLabel(res: Resources, kind: String) = when (kind) {
    "mention" -> res.getString(R.string.inbox_mention_me); "dm" -> res.getString(R.string.inbox_kind_dm)
    "group" -> res.getString(R.string.inbox_kind_group); "bot" -> res.getString(R.string.inbox_bot)
    "mail" -> res.getString(R.string.inbox_source_mail); "moment" -> res.getString(R.string.inbox_moments); else -> ""
}

/** Labels for the fixed Kind filter values shared by the Kind sheet and Glass strip (#17814). */
internal fun inboxFilterKindLabel(res: Resources, kind: String): String = when (kind) {
    "agent" -> res.getString(R.string.inbox_kind_agent)
    else -> kindLabel(res, kind)
}

/** The row's kind: an @ of everyone is "@all", a mail the user was only copied on is "Cc" (the old app's labels). */
internal fun kindLabel(res: Resources, record: InboxRecord): String = when {
    // A connector names its own kinds ("Direct message", "@me", "Transfer", "Notice"…): its label, never one this app guesses from the kind.
    record.isConnector -> record.connector?.label.orEmpty()
    record.item.kind == "mention" && record.read -> res.getString(R.string.inbox_kind_group)
    record.item.kind == "mention" && record.atAll -> res.getString(R.string.inbox_kind_at_all)
    record.item.kind == "mail" && record.mail?.direct == false -> res.getString(R.string.inbox_kind_cc)
    record.doc != null || record.item.kind == "doc" -> docKindLabel(res, record.doc)
    record.moment != null || record.item.kind == "moment" ->
        res.getString(if (record.moment?.notice?.startsWith("atIn") == true) R.string.inbox_mention_me else R.string.inbox_moments)
    else -> kindLabel(res, record.item.kind)
}

/**
 * The connection state the inbox title shows. Every reconnect passes through Connecting and Updating, usually for a
 * moment; shown at once, they put a spinner and a line under the title and push the list down, then take them away.
 * Coming from Online they show once they have lasted [delayMillis]; every other change shows at once.
 */
@Composable
internal fun rememberSettledConnection(connection: ConnectionState, delayMillis: Long = 1_000): ConnectionState {
    val brief = connection == ConnectionState.Connecting || connection == ConnectionState.Updating
    var shown by remember { mutableStateOf(if (brief) ConnectionState.Online else connection) }
    LaunchedEffect(connection) {
        if (brief && shown == ConnectionState.Online) kotlinx.coroutines.delay(delayMillis)
        shown = connection
    }
    return shown
}

@Composable
internal fun inboxTitle(state: ConnectionState): String = stringResource(when (state) {
    ConnectionState.Online -> R.string.inbox_title
    ConnectionState.Connecting -> R.string.inbox_conn_connecting
    ConnectionState.Updating -> R.string.inbox_conn_updating
    ConnectionState.WaitingForNetwork -> R.string.inbox_conn_waiting_network
    ConnectionState.NeedsLocalNetworkPermission -> R.string.inbox_conn_local_network
    ConnectionState.CertificateMismatch -> R.string.inbox_conn_certificate
    ConnectionState.Revoked -> R.string.inbox_conn_revoked
})

/** [InboxRecord.localStatus] tokens the repositories write (`RealInboxRepository`, [InboxExperienceRepository]). */
internal const val LOCAL_DRAFT = "草稿 · " // i18n-ignore: token the repositories write, shown via inbox_local_draft
internal const val LOCAL_UNCONFIRMED = "结果待确认 · 打开核对" // i18n-ignore: token, shown via inbox_local_unconfirmed
