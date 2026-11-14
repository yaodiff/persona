package persona.feature.chat

import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.unit.dp
import persona.core.contracts.Item
import persona.core.contracts.SessionKey
import persona.core.contracts.api.ChatRepository
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.isActive
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

@Composable
internal fun ChatSearchScreen(
    state: ChatSearchState, items: List<Item>, rounds: Int, hasOlder: Boolean,
    repository: ChatRepository, session: SessionKey, history: String? = null, onClose: () -> Unit,
) {
    val palette = PersonaTheme.palette
    val scope = rememberCoroutineScope()
    var loading by remember { mutableStateOf(false) }
    var pageJob by remember { mutableStateOf<Job?>(null) }
    var pages by remember { mutableIntStateOf(0) }
    var notice by remember { mutableStateOf<String?>(null) }
    var searching by remember { mutableStateOf(false) }
    val res = androidx.compose.ui.platform.LocalResources.current
    val foldKey = "${session.machineId}\u0000${session.sessionId}\u0000${history.orEmpty()}"
    val foldSig = items.mapNotNull { item -> item.fold?.let { "${item.id}:${it.throughSeq}" } }.joinToString(",")
    LaunchedEffect(foldKey, foldSig) {
        if (state.foldCacheKey != foldKey) state.resetFolds(foldKey)
        val wanted = items.filter { it.kind == "fold" && it.fold != null && state.needsFold(it) }
        if (wanted.isEmpty()) {
            state.foldsLoading = false
            return@LaunchedEffect
        }
        state.foldsLoading = true
        try {
            val outcome = fetchFoldsForSearch(
                wanted,
                load = { fold -> loadFoldForSearch(repository, session, fold) },
                onFold = { fold, loaded -> state.acceptFold(fold, loaded) },
                stopped = { !isActive },
                budget = state.foldBudgetLeft().coerceAtLeast(1),
            )
            if (isActive && (outcome.failed > 0 || outcome.capped)) state.foldsPartial = true
        } finally { if (isActive) state.foldsLoading = false }
    }
    val loaded = remember(items) { items.mapTo(HashSet()) { it.id } }
    val searched = remember(items, state.foldItems) {
        val extra = state.foldItems.filter { it.id !in loaded }
        if (extra.isEmpty()) items else items + extra
    }
    val results by produceState(ChatSearchResults(), state.query, searched) {
        searching = true
        value = ChatSearchResults()
        delay(200)
        value = withContext(Dispatchers.Default) { searchChat(res, searched, state.query) }
        searching = false
    }
    val loadFailed = stringResource(R.string.chat_search_load_failed)
    JFullScreen(onClose) {
        JPage(title = stringResource(R.string.chat_search_title), onBack = onClose, modifier = Modifier.imePadding(),
            topBar = {
                Column {
                    JTopBar(stringResource(R.string.chat_search_title), onBack = onClose)
                    JSearchField(state.query, { state.query = it }, stringResource(R.string.chat_search_field),
                        Modifier.padding(horizontal = pagePadding))
                    Text(pluralStringResource(if (results.truncated) R.plurals.chat_search_hits_more else R.plurals.chat_search_hits,
                        results.hits.size, results.hits.size, rounds),
                        Modifier.padding(horizontal = pagePadding, vertical = Tokens.Space.s),
                        style = PersonaTheme.type.caption, color = palette.ink2)
                    if (results.truncated) Text(stringResource(R.string.chat_search_truncated),
                        Modifier.padding(horizontal = pagePadding), color = palette.ink2, style = PersonaTheme.type.caption)
                    if (state.foldsLoading) Text(stringResource(R.string.chat_search_folds_loading),
                        Modifier.padding(horizontal = pagePadding), style = PersonaTheme.type.caption, color = palette.ink2)
                    else if (state.foldsPartial) Text(stringResource(R.string.chat_search_folds_partial),
                        Modifier.padding(horizontal = pagePadding), style = PersonaTheme.type.caption, color = palette.ink2)
                    if (searching) JLinearProgress(null)
                }
            },
            bottomBar = {
                JBottomBar { Column(Modifier.fillMaxWidth()) {
                    notice?.let { Text(it, style = PersonaTheme.type.caption, color = palette.ink2) }
                    if (loading) {
                        Text(pluralStringResource(R.plurals.chat_search_loading, pages, pages), style = PersonaTheme.type.caption, color = palette.ink2)
                        val stopped = stringResource(R.string.chat_search_stopped)
                        JTextButton(stringResource(R.string.chat_search_stop), { pageJob?.cancel(); notice = stopped })
                    } else if (hasOlder) JButton(stringResource(R.string.chat_search_load_more), onClick = {
                        loading = true
                        pages = 0
                        notice = null
                        pageJob = scope.launch {
                            try { notice = loadEarlierForSearch(res, repository, session) { pages = it } }
                            catch (e: CancellationException) { throw e }
                            catch (_: Exception) { notice = loadFailed }
                            finally { loading = false }
                        }
                    }, modifier = Modifier.fillMaxWidth(), kind = JButtonKind.Secondary)
                    else Text(stringResource(R.string.chat_search_at_start), style = PersonaTheme.type.caption, color = palette.ink2)
                } }
            }) { padding ->
            LazyColumn(Modifier.fillMaxSize(), contentPadding = PaddingValues(
                start = pagePadding, end = pagePadding, top = padding.calculateTopPadding(),
                bottom = padding.calculateBottomPadding() + Tokens.Space.m)) {
                if (!searching && results.hits.isEmpty()) item {
                    JEmptyState(stringResource(if (state.query.isBlank()) R.string.chat_search_enter else R.string.chat_search_no_match),
                        stringResource(R.string.chat_search_scope))
                }
                results.hits.groupBy { it.category }.forEach { (category, hits) ->
                    item(key = "category-$category") { JSectionHeader(category) }
                    itemsIndexed(hits, key = { index, hit -> "$category/${hit.itemId}/$index" }) { index, hit ->
                        JGroupItem(GroupPosition.of(index, hits.size)) {
                            JRow(category, onClick = { state.choose(hit) }, leadingIcon = PhIcons.MagnifyingGlass)
                            Text(buildAnnotatedString {
                                append(hit.snippet)
                                hit.ranges.forEach { addStyle(SpanStyle(background = palette.marker,
                                    color = palette.ink), it.first, it.last + 1) }
                            }, Modifier.fillMaxWidth().clickable { state.choose(hit) }.padding(Tokens.Space.m),
                                style = PersonaTheme.type.callout, color = palette.ink)
                        }
                    }
                }
                item {
                    Text(stringResource(R.string.chat_search_note),
                        Modifier.padding(vertical = Tokens.Space.m), style = PersonaTheme.type.caption, color = palette.ink2)
                }
            }
        }
    }
}

@Composable
internal fun ReturnChip(onClick: () -> Unit) {
    Row(Modifier.fillMaxWidth().padding(horizontal = Tokens.Space.m)) {
        JChip(stringResource(R.string.chat_search_return), onClick = onClick, icon = PhIcons.ArrowLeft)
    }
}

@Composable
internal fun ChatSearchHighlight(search: ChatSearchState, rowId: String, content: @Composable () -> Unit) {
    CompositionLocalProvider(LocalChatSearchItem provides search.selected?.itemId) {
        Box(if (search.highlight == rowId) Modifier.fillMaxWidth().border(2.dp, PersonaTheme.palette.marker,
            PersonaTheme.shapes.controlShape) else Modifier.fillMaxWidth()) { content() }
    }
}
