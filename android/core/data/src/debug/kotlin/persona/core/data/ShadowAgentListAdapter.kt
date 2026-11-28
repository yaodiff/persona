package persona.core.data

import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicInteger
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import persona.core.contracts.CoreText
import persona.core.contracts.JsonCodec
import persona.core.domain.sort.AgentGrouping
import persona.core.domain.sort.AgentListResult
import persona.core.domain.sort.AgentQuickFilter
import persona.core.rustcore.RustCoreSort

/** Only these fields cross the diagnostics boundary; none can carry a session title, path, query or identifier. */
@Serializable
data class AgentListMismatch(
    val domain: String = "agents-list-sort",
    val rule: String,
    val inputShape: String,
)

/** A private FIFO worker; Kotlin's result has already returned before any JSON conversion or JNI call. */
object ShadowAgentListDiagnostics {
    private val worker = Executors.newSingleThreadExecutor { Thread(it, "PersonaSortShadow").apply { isDaemon = true } }
    private val mismatches = AtomicInteger()
    val count: Int get() = mismatches.get()
    fun report(mismatch: AgentListMismatch) { mismatches.incrementAndGet() }
    internal fun enqueue(work: () -> Unit) { worker.execute(work) }
    /** Test barrier only; a timeout is a failure, never evidence of parity. */
    fun awaitIdle() { worker.submit {}.get(30, TimeUnit.SECONDS) }
}

/** The shadow never supplies rows. Unsupported inputs and native failures are diagnostic outcomes, not Kotlin failures. */
class ShadowAgentListAdapter(
    private val kotlin: AgentListAdapter = KotlinAgentListAdapter(),
    private val projectRust: (String) -> String = RustCoreSort::project,
    private val sink: (AgentListMismatch) -> Unit = ShadowAgentListDiagnostics::report,
    private val progress: (compared: Int, mismatches: Int) -> Unit = { _, _ -> },
) : AgentListAdapter {
    private val count = AtomicInteger()
    private val unsupportedCount = AtomicInteger()
    private var compared = 0
    val mismatchCount: Int get() = count.get()

    override fun project(input: AgentListAdapter.Input): AgentListResult {
        val result = kotlin.project(input)
        ShadowAgentListDiagnostics.enqueue {
            try { compare(input, result) } catch (_: Throwable) { report("shadow-failure", input.shape()) }
        }
        return result
    }

    private fun compare(input: AgentListAdapter.Input, originalExpected: AgentListResult) {
        val shape = input.shape()
        unsupported(input)?.let { report(it, shape); return }
        val coreGrouping = when (input.settings.grouping) {
            AgentGrouping.Repo, AgentGrouping.MachineSpace -> AgentGrouping.Space
            else -> input.settings.grouping
        }
        val expected: AgentListResult
        val coreInput: AgentListAdapter.Input
        if (coreGrouping == input.settings.grouping) {
            expected = originalExpected
            coreInput = input
        } else {
            expected = kotlin.project(input.copy(settings = input.settings.copy(grouping = AgentGrouping.Space)))
            val spaceIds = expected.groups.map { it.id }.toSet()
            fun Set<String>.spaceOnly() = filter { it in spaceIds }.toSet()
            coreInput = input.copy(settings = input.settings.copy(grouping = coreGrouping,
                folded = input.settings.folded.spaceOnly(), expandedIdle = input.settings.expandedIdle.spaceOnly(),
                expandedQuiet = input.settings.expandedQuiet.spaceOnly()))
        }
        val raw = try { AgentListCoreCodec.encode(coreInput) } catch (_: Throwable) {
            report("input-conversion", shape); return
        }
        val projection = try { projectRust(raw) } catch (_: Throwable) {
            report("rust-failure", shape); return
        }
        if (projection.isEmpty()) { report("rust-failure", shape); return }
        val actual = try { JsonCodec.decodeFromString<CoreProjection>(projection).toAndroidIdentities() } catch (_: Throwable) {
            report("rust-output", shape); return
        }
        compareProjection(coreInput, expected, actual, shape)
        if (coreGrouping != input.settings.grouping) unsupportedCount.incrementAndGet()
        compared++
        if (compared % 10 == 0) reportProgress()
    }

    private fun compareProjection(input: AgentListAdapter.Input, expected: AgentListResult, actual: CoreProjection, shape: String) {
        AgentQuickFilter.values().forEach { filter ->
            filter.coreKey?.let { key ->
                if (actual.counts[key] == null || expected.counts[filter] != actual.counts.getValue(key)) report("quick-filter-counts", shape)
            }
        }
        if (input.settings.grouping == AgentGrouping.Space) compareGroups(expected, actual, shape)
        else if (expected.groups.map { it.id } != actual.groups.map { it.id }) report("grouping", shape)
        else expected.groups.zip(actual.groups).forEach { (kotlin, rust) ->
            if (kotlin.rows.map { it.id } != rust.rows) report("order", shape)
            if (kotlin.visibleRows.map { it.id } != rust.visibleRows) report("visible-rows", shape)
            if (kotlin.folded != rust.folded) report("folding", shape)
            if (CoreText.words(kotlin.title) != rust.title) report("grouping-titles", shape)
            if (kotlin.parent != rust.parent) report("quiet-folding", shape)
            if (kotlin.stateCounts?.toCore() != rust.stateCounts) report("grouping-counts", shape)
            if (kotlin.spoken?.let { CoreText.words(it) } != rust.spoken) report("grouping-summary", shape)
        }
        if (expected.unreadShown != actual.unreadShown) report("unread-shown", shape)
        if (input.settings.grouping == AgentGrouping.Space && expected.quietTitle?.let { CoreText.words(it) } != actual.quietTitle) report("quiet-title", shape)
    }

    private fun compareGroups(expected: AgentListResult, actual: CoreProjection, shape: String) {
        if (expected.groups.map { it.id } != actual.groups.map { it.id }) { report("grouping", shape); return }
        expected.groups.zip(actual.groups).forEach { (kotlin, rust) ->
            if (kotlin.rows.map { it.id } != rust.rows) report("order", shape)
            if (kotlin.visibleRows.map { it.id } != rust.visibleRows) report("visible-rows", shape)
            if (kotlin.folded != rust.folded) report("folding", shape)
            if (CoreText.words(kotlin.title) != rust.title) report("grouping-titles", shape)
            if (kotlin.parent != rust.parent) report("quiet-folding", shape)
            if (kotlin.stateCounts?.toCore() != rust.stateCounts) report("grouping-counts", shape)
            if (kotlin.spoken?.let { CoreText.words(it) } != rust.spoken) report("grouping-summary", shape)
        }
    }

    private fun report(rule: String, shape: String) {
        val report = AgentListMismatch(rule = rule, inputShape = shape)
        try { sink(report) } catch (_: Throwable) {}
        count.incrementAndGet()
        try { ShadowAgentListDiagnostics.report(report) } catch (_: Throwable) {}
    }

    private fun reportProgress() { try { progress(compared, mismatchCount) } catch (_: Throwable) {} }
}

@Serializable
internal data class CoreProjection(
    val counts: Map<String, Int>,
    val groups: List<CoreGroup>,
    @SerialName("unread_shown") val unreadShown: List<String>,
    @SerialName("quiet_title") val quietTitle: String? = null,
)

@Serializable
internal data class CoreGroup(
    val id: String,
    val title: String,
    val rows: List<String>,
    @SerialName("visible_rows") val visibleRows: List<String>,
    val folded: Boolean,
    val parent: String? = null,
    @SerialName("state_counts") val stateCounts: Map<String, Int>? = null,
    val spoken: String? = null,
)

private fun Map<persona.core.contracts.AgentState, Int>.toCore(): Map<String, Int> = buildMap {
    // Core's state_counts carries only its five published keys; Android's enum map is reduced to that contract.
    for (key in listOf("needs_you", "working", "unread", "idle", "unknown")) {
        val state = when (key) {
            "needs_you" -> persona.core.contracts.AgentState.NeedsYou
            "working" -> persona.core.contracts.AgentState.Working
            "unread" -> persona.core.contracts.AgentState.Unread
            "idle" -> persona.core.contracts.AgentState.Idle
            else -> persona.core.contracts.AgentState.Unknown
        }
        this[key] = this@toCore[state] ?: 0
    }
}

private val AgentQuickFilter.coreKey: String? get() = when (this) {
    AgentQuickFilter.NeedsYou -> "needsYou"
    AgentQuickFilter.Unread -> "unread"
    AgentQuickFilter.Working -> "working"
    AgentQuickFilter.BackgroundWaiting -> "backgroundWaiting"
    else -> null
}

private fun CoreProjection.toAndroidIdentities(): CoreProjection {
    fun String.androidId(): String {
        val slash = lastIndexOf('/')
        val pipe = indexOf('|')
        if (pipe <= 0 || slash <= pipe) return this
        return substring(pipe + 1, slash) + "~" + substring(0, pipe) + substring(slash)
    }
    fun String.androidPath(): String {
        val slash = indexOf('/')
        if (slash < 0) return this
        val pipe = indexOf('|')
        if (pipe <= 0 || pipe > slash) return this
        return substring(pipe + 1, slash) + "~" + substring(0, pipe) + substring(slash)
    }
    return copy(
        groups = groups.map { it.copy(id = it.id.androidPath(), rows = it.rows.map(String::androidId),
            visibleRows = it.visibleRows.map(String::androidId)) },
        unreadShown = unreadShown.map(String::androidId),
    )
}

private fun unsupported(input: AgentListAdapter.Input): String? = when {
    input.settings.heldSeen.isNotEmpty() -> "held-set-scope"
    input.settings.grouping !in listOf(AgentGrouping.State, AgentGrouping.Space, AgentGrouping.Machine,
        AgentGrouping.Repo, AgentGrouping.MachineSpace) -> "unsupported-grouping"
    input.settings.quick !in listOf(AgentQuickFilter.All, AgentQuickFilter.NeedsYou, AgentQuickFilter.Unread,
        AgentQuickFilter.Working, AgentQuickFilter.BackgroundWaiting) -> "unsupported-quick-filter"
    input.settings.queryAncestors.isNotEmpty() -> "unsupported-query-ancestors"
    input.settings.includeCodexHistory -> "unsupported-history"
    input.settings.hideBotmux -> "unsupported-botmux"
    input.settings.hierarchy && input.sessions.any { it.hierarchyKnown || it.session.foldedUnder != null || it.session.children != null } ->
        "unsupported-hierarchy"
    input.settings.grouping in listOf(AgentGrouping.Repo, AgentGrouping.MachineSpace) &&
        input.settings.folded.any { it.startsWith("machine/") } -> "unsupported-group-state"
    input.sessions.any { it.session.machineId.contains('~') } != input.machines.any { it.id.contains('~') } ->
        "unsupported-partial-identity"
    else -> null
}

private fun AgentListAdapter.Input.shape(): String = listOf(
    "sessions=${sessions.size}",
    "machines=${machines.size}",
    "grouping=${settings.grouping.name}",
    "quick=${settings.quick.name}",
    "sort=${settings.sort.name}",
    "queryWords=${settings.query.split(Regex("\\s+")).count(String::isNotEmpty)}",
    "machineFilter=${settings.machineFilter.isNotEmpty()}",
    "pins=${pins.sessions.size}",
    "mutes=${mutes.sessions.size}",
    "spacePins=${pins.spaces.size}",
    "spaceMutes=${mutes.spaces.size}",
    "held=${held != null}",
    "folded=${settings.folded.size}",
    "kept=${settings.kept.size}",
).joinToString(" ")
