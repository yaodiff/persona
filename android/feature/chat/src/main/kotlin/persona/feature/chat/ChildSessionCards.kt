package persona.feature.chat

import androidx.compose.runtime.*
import androidx.compose.ui.platform.LocalResources
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.lifecycle.compose.LocalLifecycleOwner
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.repeatOnLifecycle
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.flow.*
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.core.designsystem.LocalHubFeatures
import persona.core.domain.lines.ChildSessionCards
import persona.core.domain.lines.ChildSessionDetail
import persona.core.domain.lines.ChildSessionDetailReader
import persona.core.domain.lines.LineItem
import persona.core.domain.lines.SubagentStatus
import persona.core.domain.lines.ToolCall
import persona.core.domain.sort.childSessions
import java.time.Duration

internal data class ChatChildren(val rows: Map<String, Session>, val repository: ChatRepository)
internal val LocalChatChildren = staticCompositionLocalOf<ChatChildren?> { null }

data class ChildSessionHome(val key: SessionKey, val childSessionId: String? = null,
    val rows: List<Session> = emptyList())

internal fun childSessionHome(key: SessionKey, row: Session?, parentRow: Session? = null): ChildSessionHome {
    val parent = row?.takeIf { it.key == key }?.foldedUnder?.takeIf { it.isNotBlank() && it != key.sessionId }
        ?.let { SessionKey(key.machineId, it) }?.takeIf { it == parentRow?.key }
    return if (parent == null) ChildSessionHome(key) else ChildSessionHome(parent, key.sessionId)
}

/** Exact lookups are page metadata, never additions to the root list's membership. */
internal suspend fun resolveChildSessionHome(key: SessionKey, rows: List<Session>, connected: Boolean = true,
    lookup: suspend (SessionKey) -> Session?): ChildSessionHome {
    suspend fun row(target: SessionKey): Session? = rows.firstOrNull { it.key == target } ?: if (!connected) null else try {
        lookup(target)?.takeIf { it.key == target }
    } catch (cancelled: CancellationException) { throw cancelled }
    catch (_: Exception) { null }
    val child = row(key)
    val parentKey = child?.foldedUnder?.takeIf { it.isNotBlank() && it != key.sessionId }?.let { SessionKey(key.machineId, it) }
    val parent = parentKey?.let { row(it) }
    val home = childSessionHome(key, child, parent)
    return home.copy(rows = listOfNotNull(child, parent).filter { found -> rows.none { it.key == found.key } })
}

/** Keep cached pages mounted while looking up metadata; only a confirmed child and parent change the page key. */
@Composable
fun rememberChildSessionHome(key: SessionKey, rows: List<Session>, actions: ChatActions?, features: Collection<String>,
    connected: Boolean): ChildSessionHome {
    val listed = rows.firstOrNull { it.key == key }
    val parent = listed?.foldedUnder?.let { id -> rows.firstOrNull { it.machineId == key.machineId && it.sessionId == id } }
    val local = childSessionHome(key, listed, parent)
    val missing = listed == null || !listed.foldedUnder.isNullOrBlank() && parent == null
    val needsLookup = missing && connected && actions != null && "sessions_lookup" in features
    var lookedUp by remember(key, actions, listed?.foldedUnder) { mutableStateOf<ChildSessionHome?>(null) }
    LaunchedEffect(key, listed?.foldedUnder, needsLookup, actions, features) {
        if (needsLookup) {
            lookedUp = resolveChildSessionHome(key, rows, connected) { target ->
                actions?.completeRow(Session(target.machineId, target.sessionId))
            }
        }
    }
    return if (missing) lookedUp ?: local else local
}

/** Bounded hubs keep children outside the root window; a failed refresh keeps the last confirmed cards. */
@Composable
internal fun rememberChatChildren(key: SessionKey, parent: Session?, rows: List<Session>, chrome: ChatChromeBindings,
    repository: ChatRepository): ChatChildren {
    val features = chrome.connection?.lastHello?.features ?: LocalHubFeatures.current
    val list = remember(key, chrome.actions, chrome.connection) { ChildSessionList(key) }
    val held by list.state.collectAsStateWithLifecycle()
    val local = remember(rows, key) { childSessions(rows, key) }
    val children = remember(held, local, chrome.lookupRows, key) {
        childSessionRows(key, local, held, chrome.lookupRows)
    }
    val hasChildren = (parent?.children?.total ?: 0) > 0 || children.isNotEmpty()
    val latestParent by rememberUpdatedState(parent)
    val latestLocal by rememberUpdatedState(local)
    // Retain invalidations while covered/backgrounded without doing HTTP work there.
    val invalidation = produceState<String?>(null, chrome.connection) {
        value = null
        chrome.connection?.events?.filterIsInstance<SessionsListEvent>()?.collect { value = it.revision }
    }
    val connected = chrome.connection?.state?.collectAsStateWithLifecycle()?.value == ConnectionState.Online
    val lifecycle = LocalLifecycleOwner.current.lifecycle
    LaunchedEffect(key, parent?.key, hasChildren, features, chrome.actions, chrome.connection, connected, chrome.covered, lifecycle) {
        if (parent == null || !hasChildren || "sessions_hierarchy" !in features || chrome.covered || !connected) return@LaunchedEffect
        lifecycle.repeatOnLifecycle(Lifecycle.State.RESUMED) {
            val actions = chrome.actions ?: return@repeatOnLifecycle
            val events = snapshotFlow { invalidation.value }.filterNotNull()
            list.watch(events, { needsChildSessionRead(latestParent, childSessionRows(key, latestLocal, list.state.value, emptyList())) }) {
                actions.childPage(parent, it)
            }
        }
    }
    return ChatChildren(children.associateBy { it.sessionId }, repository)
}

/** The existing task card, with its child's own metadata and a transcript read only while open. */
@Composable
internal fun ChildSessionCard(sessionId: String, assistant: Boolean) {
    val children = LocalChatChildren.current ?: return
    val row = children.rows[sessionId] ?: return
    val callId = ChildSessionCards.call(sessionId)
    val folds = LocalSubagentFolds.current
    val opened = folds?.isOpen(callId) == true
    val source = remember(row.key, opened, children.repository) {
        if (opened) children.repository.observe(row.key) else flowOf(ChatSnapshot())
    }
    val snapshot by source.collectAsStateWithLifecycle(ChatSnapshot())
    val detailReader = remember(row.key, opened, children.repository) {
        ChildSessionDetailReader { from, to -> children.repository.loadRange(row.key, from, to) }
    }
    var detail by remember(row.key, opened) { mutableStateOf(ChildSessionDetail.of(snapshot.items)) }
    LaunchedEffect(detailReader, snapshot.items, snapshot.hasOlder, opened) {
        detail = ChildSessionDetail.of(snapshot.items)
        if (opened) detail = detailReader.read(snapshot)
    }
    val result = detail.result
    val prompt = detail.prompt
    val lastTool = detail.lastTool
    val title = persona.core.domain.lines.Subagents.compact(row.title, 96)
        .ifEmpty { LocalResources.current.getString(R.string.chat_subagent_untitled) }
    val status = ChildSessionCards.status(row)
    val agent = Subagent(sessionId, sessionId = sessionId, description = title, toolUseId = callId,
        running = status == SubagentStatus.Running, startedAt = row.startedAt, updatedAt = row.updatedAt,
        lastText = row.lastText, model = row.model, lastTool = lastTool?.let { WorkflowTool(name = it) })
    val use = Item(callId, role = "assistant", kind = "tool_use", toolName = "Agent", toolUseId = callId,
        toolInput = buildJsonObject { put("description", title); put("prompt", prompt.orEmpty()) })
    val words = result?.let { it.copy(kind = "tool_result") } ?: row.lastText?.let {
        Item("$callId-result", role = "user", kind = "tool_result", text = it)
    }
    val call = ToolCall(LineItem(use), words?.let(::LineItem), emptyList())
    val res = LocalResources.current
    val start = parseTime(row.startedAt)
    val end = parseTime(row.updatedAt)
    val took = if (status != SubagentStatus.Running && start != null && end != null && end >= start)
        workDuration(res, Duration.between(start, end).seconds.coerceAtLeast(1)) else null
    val effort = row.effort?.takeIf { it in setOf("minimal", "low", "medium", "high", "xhigh", "max", "ultra") }
        ?.let { SessionModes.effortLabel(it, res) }
    val model = listOfNotNull(persona.core.domain.modelShortName(row.model).ifEmpty { null }, effort).joinToString(" · ").ifEmpty { null }
    val harness = if (row.isClaude) res.getString(R.string.chat_subagent_harness_claude) else row.agentLabel
    val meta = SubagentCardText.Meta(if (assistant) null else harness, model, null, null, took)
    val actions = remember(row.key, children.repository) {
        ChatRowActions(row.key, fullText = { children.repository.fullText(row.key, it.id) }, openPath = null,
            photos = null, pictures = { emptyList() }, openImages = { _, _ -> }, menu = null, retryLineId = null, onRetry = {})
    }
    CompositionLocalProvider(LocalChatSubagents provides ChatSubagents(emptyMap(), null, mapOf(callId to agent)),
        LocalChatRowActions provides actions) {
        SubagentCard(call, live = status == SubagentStatus.Running, assistant = assistant, statusOverride = status, metaOverride = meta)
    }
}
