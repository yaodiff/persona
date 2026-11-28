package persona.platform.shortcuts

import android.content.Context
import android.content.res.Resources
import androidx.core.content.edit
import org.json.JSONArray
import org.json.JSONObject
import persona.core.contracts.AgentState
import persona.core.contracts.DirectionTask
import persona.core.contracts.DirectionTaskState
import persona.core.contracts.GroupKeys
import persona.core.contracts.Interaction
import persona.core.contracts.ProtocolCompatibility
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.AssistantSnapshot
import persona.core.contracts.api.ManagerRoster
import persona.core.contracts.clipped
import persona.core.contracts.parseTime
import persona.core.designsystem.LocalizedFormats
import persona.core.designsystem.PersonaState
import persona.core.designsystem.resolve
import persona.core.domain.FilePaths
import persona.core.domain.answer.askWords
import persona.core.domain.messageText
import persona.core.domain.parseContexts
import persona.core.domain.plainMarkdown
import java.time.Instant
import java.time.ZoneId

/** High-level runtime phase of the Persona main conversation shown on [MainChatWidget]. */
enum class MainChatPhase(val wire: String) {
    Unpaired("unpaired"),
    Idle("idle"),
    Working("working"),
    NeedsYou("needs_you");

    val personaState: PersonaState
        get() = when (this) {
            Unpaired, Idle -> PersonaState.Idle
            Working -> PersonaState.Thinking
            NeedsYou -> PersonaState.NeedsYou
        }

    companion object {
        fun fromWire(value: String?): MainChatPhase = entries.firstOrNull { it.wire == value } ?: Idle
    }
}

/** Pending approval, question, or plan card surfaced on [MainChatWidget] with direct deep-link routing. */
data class MainChatPendingItem(
    val id: String,
    val machineId: String,
    val sessionId: String,
    val assistant: Boolean?,
    val kind: String,
    val title: String,
) {
    /** Reconstructs the minimal [Interaction] target for [SurfaceLinks.request]. */
    fun toInteraction(): Interaction = Interaction(
        id = id,
        machineId = machineId,
        sessionId = sessionId,
        kind = kind,
    )
}

/** Compact open to-do / task progress line rendered on large (4x3) [MainChatWidget] layouts when space allows. */
data class MainChatTodoItem(
    val id: String,
    val title: String,
    val state: String,
) {
    val taskState: DirectionTaskState get() = DirectionTaskState.of(state)
}

/** Immutable view snapshot for [MainChatWidget], persisted locally per pairing for instant launcher rendering. */
data class MainChatWidgetSnapshot(
    val paired: Boolean = false,
    val phase: MainChatPhase = MainChatPhase.Unpaired,
    val mainSession: SessionKey? = null,
    val summary: String = "",
    val updatedAt: String? = null,
    val pendingCount: Int = 0,
    val pending: MainChatPendingItem? = null,
    val todos: List<MainChatTodoItem> = emptyList(),
    val title: String? = null,
    val isMainChat: Boolean = true,
    val targetAssistant: Boolean? = true,
    val bindingMissing: Boolean = false,
)

/** Pure projection from repository state into [MainChatWidgetSnapshot]. */
object MainChatWidgetState {
    const val MAX_TODOS = 2

    fun build(
        paired: Boolean,
        assistant: AssistantSnapshot,
        sessions: List<Session>,
        interactions: List<Interaction>,
        resources: Resources,
        targetBinding: ConversationWidgetBinding? = null,
    ): MainChatWidgetSnapshot {
        if (!paired) {
            return MainChatWidgetSnapshot(
                paired = false,
                phase = MainChatPhase.Unpaired,
                title = targetBinding?.takeUnless { it.isMain }?.titleSnapshot?.takeIf(String::isNotBlank),
                isMainChat = targetBinding == null || targetBinding.isMain,
            )
        }
        if (targetBinding != null && !targetBinding.isMain) {
            return buildTarget(
                binding = targetBinding,
                assistant = assistant,
                sessions = sessions,
                interactions = interactions,
                resources = resources,
            )
        }

        val liveByKey = sessions.associateBy { it.key }
        val mainKey = assistant.mainSession
        val mainSession = mainKey?.let { key ->
            liveByKey[key] ?: assistant.conversations.firstOrNull { it.key == key }
        }

        val tree = assistant.directionTree
        val hasSelectedGroupAnchor = mainKey != null ||
            assistant.conversations.isNotEmpty() ||
            tree?.directions?.any { direction -> direction.tasks.any { task -> task.agents.isNotEmpty() } } == true
        val selectedNamespace: String? = when {
            mainKey != null -> GroupKeys.namespaceOf(mainKey.machineId)
            assistant.conversations.isNotEmpty() -> GroupKeys.namespaceOf(assistant.conversations.first().machineId)
            else -> tree?.directions.orEmpty()
                .asSequence()
                .flatMap { it.tasks.asSequence() }
                .flatMap { it.agents.asSequence() }
                .map { GroupKeys.namespaceOf(it.machineId) }
                .firstOrNull()
        }

        val leaderAgentKeys = tree?.leaders.orEmpty()
            .flatMap { direction -> direction.tasks }
            .flatMap { task -> task.agents }
            .flatMap { agent -> agent.flatten() }
            .flatMap { agent -> listOf(agent.key, agent.openKey) }
            .toSet()

        val mainOwnedKeys = LinkedHashSet<SessionKey>()
        if (mainKey != null) {
            val _ = mainOwnedKeys.add(mainKey)
        }
        tree?.secretary?.tasks.orEmpty()
            .flatMap { it.agents }
            .flatMap { it.flatten() }
            .forEach { agent ->
                val _ = mainOwnedKeys.add(agent.key)
                val _ = mainOwnedKeys.add(agent.openKey)
            }
        if (hasSelectedGroupAnchor) {
            sessions.forEach { session ->
                if (session.startedByPersona &&
                    !session.isSpace &&
                    session.key !in leaderAgentKeys &&
                    GroupKeys.namespaceOf(session.machineId) == selectedNamespace &&
                    ownsManager(assistant, session.manager)
                ) {
                    val _ = mainOwnedKeys.add(session.key)
                }
            }
        }

        val matchingPending = interactions
            .asSequence()
            .filterNot { it.isDelegating }
            .filter { ix ->
                if (ix.sessionId.isBlank() || !hasSelectedGroupAnchor) return@filter false
                if (GroupKeys.namespaceOf(ix.machineId) != selectedNamespace) return@filter false
                if (ix.sessionKey !in mainOwnedKeys && ix.homeKey !in mainOwnedKeys) return@filter false
                val ixManager = ix.manager?.takeIf(String::isNotBlank)
                if (ixManager != null) {
                    ownsManager(assistant, ixManager)
                } else {
                    val isMainChat = mainKey != null && (ix.sessionKey == mainKey || ix.homeKey == mainKey)
                    val ownerSession = liveByKey[ix.sessionKey] ?: liveByKey[ix.homeKey] ?: mainSession?.takeIf { isMainChat }
                    if (isMainChat) {
                        ownerSession?.manager?.takeIf(String::isNotBlank)?.let { ownsManager(assistant, it) } ?: true
                    } else {
                        ownsManager(assistant, ownerSession?.manager)
                    }
                }
            }
            .distinctBy { it.id }
            .sortedWith(
                compareByDescending<Interaction> { mainKey != null && (it.sessionKey == mainKey || it.homeKey == mainKey) }
                    .thenByDescending { parseTime(it.createdAt) ?: Instant.MIN }
                    .thenBy { it.id },
            )
            .toList()

        val leadPending = matchingPending.firstOrNull()?.let { ix ->
            val session = liveByKey[ix.sessionKey] ?: liveByKey[ix.homeKey]
            val isAssistant = session?.assistant ?: (mainKey != null && (ix.sessionKey == mainKey || ix.homeKey == mainKey))
            MainChatPendingItem(
                id = ix.id,
                machineId = ix.machineId,
                sessionId = ix.sessionId,
                assistant = isAssistant,
                kind = ix.kind,
                title = MainChatText.pendingTitle(ix, resources),
            )
        }

        val mainNeedsYou = mainSession?.foldedState == AgentState.NeedsYou ||
            mainSession?.agentState == AgentState.NeedsYou ||
            (mainSession?.pendingInteractions ?: 0) > 0
        val pendingCount = when {
            matchingPending.isNotEmpty() -> maxOf(matchingPending.size, mainSession?.pendingInteractions ?: 0)
            mainNeedsYou -> maxOf(mainSession?.pendingInteractions ?: 0, 1)
            else -> 0
        }

        val mainWorking = mainSession?.foldedState == AgentState.Working ||
            mainSession?.agentState == AgentState.Working ||
            mainSession?.starting == true ||
            mainSession?.compaction?.running == true

        val phase = when {
            pendingCount > 0 -> MainChatPhase.NeedsYou
            mainWorking -> MainChatPhase.Working
            else -> MainChatPhase.Idle
        }

        val rawSummary = when {
            mainSession?.awaitsReply == true && !mainSession.asks.isNullOrBlank() -> mainSession.asks
            !mainSession?.lastText.isNullOrBlank() -> mainSession.lastText
            !mainSession?.reasonPreview.isNullOrBlank() -> mainSession.reasonPreview
            else -> null
        }
        val summary = MainChatText.sanitize(rawSummary, maxChars = SUMMARY_MAX_CHARS)
        val updatedAt = mainSession?.updatedAt ?: mainSession?.activityAt ?: mainSession?.stateSince

        val todos = pickUrgentTodos(tree?.secretary?.tasks.orEmpty(), tree?.leaders.orEmpty().flatMap { it.tasks })

        return MainChatWidgetSnapshot(
            paired = true,
            phase = phase,
            mainSession = mainKey,
            summary = summary,
            updatedAt = updatedAt,
            pendingCount = pendingCount,
            pending = leadPending,
            todos = todos,
        )
    }

    private fun buildTarget(
        binding: ConversationWidgetBinding,
        assistant: AssistantSnapshot,
        sessions: List<Session>,
        interactions: List<Interaction>,
        resources: Resources,
    ): MainChatWidgetSnapshot {
        val targetKey = binding.sessionKey ?: return MainChatWidgetSnapshot(
            paired = true,
            phase = MainChatPhase.Idle,
            title = binding.titleSnapshot.takeIf(String::isNotBlank),
            isMainChat = false,
            bindingMissing = true,
        )
        val liveByKey = sessions.associateBy { it.key }
        val targetSession = liveByKey[targetKey] ?: assistant.conversations.firstOrNull { it.key == targetKey }
        val tree = assistant.directionTree
        val direction = tree?.leaders?.firstOrNull { dir ->
            dir.id == targetKey.sessionId || dir.external?.target == targetKey
        }
        val isLeader = direction != null ||
            binding.kind == ConversationWidgetKind.Leader ||
            targetKey.sessionId in assistant.leaders

        val missing = (targetSession == null && direction == null) || targetSession?.archived == true
        val resolvedTitle = when {
            direction?.name?.isNotBlank() == true -> direction.name
            !assistant.directionNames[targetKey.sessionId].isNullOrBlank() -> assistant.directionNames[targetKey.sessionId]
            !targetSession?.displayTitle.isNullOrBlank() -> targetSession?.displayTitle
            else -> binding.titleSnapshot.takeIf(String::isNotBlank)
        }
        val targetAssistant = targetSession?.assistant ?: when {
            direction?.external?.target == targetKey -> false
            isLeader || binding.kind == ConversationWidgetKind.Assistant -> true
            binding.kind == ConversationWidgetKind.Agent -> false
            else -> null
        }

        if (missing) {
            return MainChatWidgetSnapshot(
                paired = true,
                phase = MainChatPhase.Idle,
                mainSession = targetKey,
                title = resolvedTitle,
                isMainChat = false,
                targetAssistant = targetAssistant,
                bindingMissing = true,
            )
        }

        val ownedKeys = LinkedHashSet<SessionKey>()
        val _ = ownedKeys.add(targetKey)
        if (direction != null) {
            direction.tasks
                .flatMap { it.agents }
                .flatMap { it.flatten() }
                .forEach { agent ->
                    val _ = ownedKeys.add(agent.key)
                    val _ = ownedKeys.add(agent.openKey)
                }
        }

        val matchingPending = interactions
            .asSequence()
            .filterNot { it.isDelegating }
            .filter { ix ->
                ix.sessionId.isNotBlank() && (ix.sessionKey in ownedKeys || ix.homeKey in ownedKeys)
            }
            .distinctBy { it.id }
            .sortedWith(
                compareByDescending<Interaction> { it.sessionKey == targetKey || it.homeKey == targetKey }
                    .thenByDescending { parseTime(it.createdAt) ?: Instant.MIN }
                    .thenBy { it.id },
            )
            .toList()

        val leadPending = matchingPending.firstOrNull()?.let { ix ->
            val session = liveByKey[ix.sessionKey] ?: liveByKey[ix.homeKey]
            val isAssistant = session?.assistant ?: if (ix.sessionKey == targetKey || ix.homeKey == targetKey) targetAssistant else false
            MainChatPendingItem(
                id = ix.id,
                machineId = ix.machineId,
                sessionId = ix.sessionId,
                assistant = isAssistant,
                kind = ix.kind,
                title = MainChatText.pendingTitle(ix, resources),
            )
        }

        val targetNeedsYou = targetSession?.foldedState == AgentState.NeedsYou ||
            targetSession?.agentState == AgentState.NeedsYou ||
            (targetSession?.pendingInteractions ?: 0) > 0 ||
            (direction?.counts?.needsYou ?: 0) > 0
        val pendingCount = when {
            matchingPending.isNotEmpty() -> maxOf(matchingPending.size, targetSession?.pendingInteractions ?: 0, direction?.counts?.needsYou ?: 0)
            targetNeedsYou -> maxOf(targetSession?.pendingInteractions ?: 0, direction?.counts?.needsYou ?: 0, 1)
            else -> 0
        }

        val targetWorking = targetSession?.foldedState == AgentState.Working ||
            targetSession?.agentState == AgentState.Working ||
            targetSession?.starting == true ||
            targetSession?.compaction?.running == true ||
            direction?.running == true

        val phase = when {
            pendingCount > 0 -> MainChatPhase.NeedsYou
            targetWorking -> MainChatPhase.Working
            else -> MainChatPhase.Idle
        }

        val rawSummary = when {
            targetSession?.awaitsReply == true && !targetSession.asks.isNullOrBlank() -> targetSession.asks
            !targetSession?.lastText.isNullOrBlank() -> targetSession.lastText
            !targetSession?.reasonPreview.isNullOrBlank() -> targetSession.reasonPreview
            else -> null
        }
        val summary = MainChatText.sanitize(rawSummary, maxChars = SUMMARY_MAX_CHARS)
        val updatedAt = targetSession?.updatedAt ?: targetSession?.activityAt ?: targetSession?.stateSince
        val todos = if (direction != null) pickUrgentTodos(direction.tasks, emptyList()) else emptyList()

        return MainChatWidgetSnapshot(
            paired = true,
            phase = phase,
            mainSession = targetKey,
            summary = summary,
            updatedAt = updatedAt,
            pendingCount = pendingCount,
            pending = leadPending,
            todos = todos,
            title = resolvedTitle,
            isMainChat = false,
            targetAssistant = targetAssistant,
            bindingMissing = false,
        )
    }

    private fun pickUrgentTodos(
        secretaryTasks: List<DirectionTask>,
        leaderTasks: List<DirectionTask>,
    ): List<MainChatTodoItem> {
        val combined = (secretaryTasks + leaderTasks).distinctBy { it.id }
        return combined
            .withIndex()
            .sortedWith(compareBy<IndexedValue<DirectionTask>> { urgencyRank(it.value.state) }.thenBy { it.index })
            .mapNotNull { (_, task) ->
                val cleanTitle = MainChatText.sanitize(task.title, maxChars = TODO_MAX_CHARS)
                if (cleanTitle.isEmpty()) null else MainChatTodoItem(task.id, cleanTitle, task.state.wire)
            }
            .take(MAX_TODOS)
    }

    private fun urgencyRank(state: DirectionTaskState): Int = when (state) {
        DirectionTaskState.NeedsYou -> 0
        DirectionTaskState.Stuck -> 1
        DirectionTaskState.Review -> 2
        DirectionTaskState.Working -> 3
        DirectionTaskState.Planned -> 4
    }

    private fun ownsManager(assistant: AssistantSnapshot, stamp: String?): Boolean {
        val normalizedStamp = stamp?.takeIf(String::isNotBlank)
        val selectedManager = assistant.manager?.takeIf(String::isNotBlank)
        val selectedOwner = selectedManager ?: assistant.managerId.ifBlank { ManagerRoster.ASSISTANT_MANAGER }
        return if (normalizedStamp == null) {
            selectedManager == null || selectedOwner == ManagerRoster.ASSISTANT_MANAGER
        } else {
            normalizedStamp == selectedOwner
        }
    }

    /** Per-repository initialization/restoration readiness tracked independently for the active pairing. */
    internal data class MainChatRepoReadiness(
        val assistant: Boolean = false,
        val sessions: Boolean = false,
        val interactions: Boolean = false,
    ) {
        val allReady: Boolean get() = assistant && sessions && interactions
    }

    /** Updates per-repository readiness monotonically for the active pairing. */
    internal fun repositoryReadiness(
        assistant: AssistantSnapshot,
        sessions: List<Session>,
        interactions: List<Interaction>,
        previous: MainChatRepoReadiness = MainChatRepoReadiness(),
    ): MainChatRepoReadiness = MainChatRepoReadiness(
        assistant = previous.assistant || assistant != AssistantSnapshot(),
        sessions = previous.sessions || sessions.isNotEmpty(),
        interactions = previous.interactions || interactions.isNotEmpty(),
    )

    /**
     * True once live repositories are authoritative for [activePairing] — either because [SurfacesRuntime]
     * explicitly settled this pairing (once all three repositories initialized or after the bounded online
     * warmup window) or because all three live flows already carry non-placeholder repository state.
     */
    internal fun isLiveSettled(
        paired: Boolean,
        activePairing: String?,
        settledPairing: String?,
        assistant: AssistantSnapshot,
        sessions: List<Session>,
        interactions: List<Interaction>,
    ): Boolean {
        if (!paired) return true
        val scope = activePairing?.takeIf(String::isNotBlank) ?: return false
        if (settledPairing == scope) return true
        return repositoryReadiness(assistant, sessions, interactions).allReady
    }

    /**
     * Selects the snapshot to display on [MainChatWidget]: renders [live] whenever unpaired or live state
     * has settled for the active pairing, falling back to a pairing-verified [persisted] snapshot only
     * during cold-start warmup before live repositories settle.
     */
    internal fun selectSnapshot(
        live: MainChatWidgetSnapshot,
        persisted: MainChatWidgetSnapshot?,
        liveSettled: Boolean,
    ): MainChatWidgetSnapshot =
        if (!live.paired || liveSettled) live else (persisted?.takeIf { it.paired } ?: live)

    private const val SUMMARY_MAX_CHARS = 400
    private const val TODO_MAX_CHARS = 80
}

/**
 * Plain-language text sanitizer and formatter for [MainChatWidget].
 * Strips context blocks and internal tags through [ProtocolCompatibility], Markdown syntax,
 * raw file paths, UUIDs, internal session/task IDs, and raw model IDs per `DESIGN_SYSTEM.md` §5.6.
 */
object MainChatText {
    private val crossSessionTag = Regex("<cross-session-message[^>]*>([\\s\\S]*?)</cross-session-message>", RegexOption.IGNORE_CASE)
    private val codeFenceLine = Regex("^```[A-Za-z0-9_.+-]*\\s*$", RegexOption.MULTILINE)
    private val singleStarEmphasis = Regex("(?<!\\*)\\*([^\\s*][^*]*?[^\\s*]|[^\\s*])\\*(?!\\*)")
    private val singleUnderscoreEmphasis = Regex("(?<![\\w])_([^\\s_][^_]*?[^\\s_]|[^\\s_])_(?![\\w])")
    private val uuidPattern = Regex("\\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\\b")
    private val prefixedInternalId = Regex(
        "\\b(?:sess|session|conv|conversation|req|ix|todo|plan|wf|msg|tab|pane|slot|run|thread|mach|machine)[_-][A-Za-z0-9_-]{4,}\\b",
        RegexOption.IGNORE_CASE,
    )
    private val compositeSessionId = Regex(
        "\\b[a-z0-9][a-z0-9._-]*/(?:[0-9a-fA-F]{8,}|(?:sess|session|conv|thread)[_-][A-Za-z0-9_-]+)\\b",
        RegexOption.IGNORE_CASE,
    )
    private val rawModelId = Regex(
        "\\b(?:claude-(?:opus|sonnet|haiku|[0-9])[A-Za-z0-9._-]*|gpt-[0-9][A-Za-z0-9._-]*|o[1-9](?:-[A-Za-z0-9._-]+)+|gemini-[0-9][A-Za-z0-9._-]+|deepseek-[A-Za-z0-9._-]+)\\b",
        RegexOption.IGNORE_CASE,
    )
    private val windowsPath = Regex("(?<![\\w])[A-Za-z]:\\\\[^\\s,;:'\"()<>]+")
    private val homePath = Regex("(?<![\\w/:.-])~/[^\\s,;:'\"()<>]+")
    private val absolutePath = Regex("(?<![\\w/:.-])/(?:[^\\s/,;:'\"()<>]+/)+[^\\s,;:'\"()<>]*")
    private val whitespace = Regex("\\s+")
    private val spaceBeforePunct = Regex("\\s+([,.;:!?\uFF0C\u3002\uFF1B\uFF1A\uFF01\uFF1F])")
    private val openParenLeadingPunct = Regex("([(\uFF08])(?:\\s*[,;\uFF0C\uFF1B\u3001])+\\s*")
    private val repeatedMiddleDot = Regex("(?:\\s*·\\s*){2,}")

    fun sanitize(raw: String?, maxChars: Int = 400): String {
        if (raw.isNullOrBlank()) return ""
        var text: String = raw

        // Strip closed context blocks iteratively (handles repeated or nested tags cleanly).
        var previous: String
        do {
            previous = text
            text = parseContexts(text).second
            text = ProtocolCompatibility.contextBlock.replace(text, " ")
            text = ProtocolCompatibility.fileTag.replace(text, " ")
        } while (text != previous)

        text = ProtocolCompatibility.unclosedContextBlock.replace(text, " ")
        text = ProtocolCompatibility.strayClosingTag.replace(text, " ")
        text = ProtocolCompatibility.internalTagBlock.replace(text, " ")
        text = ProtocolCompatibility.internalTagSingle.replace(text, " ")
        text = crossSessionTag.replace(text, "$1")
        text = messageText(text, files = false).text
        text = codeFenceLine.replace(text, "")
        text = plainMarkdown(text)
        text = singleStarEmphasis.replace(text, "$1")
        text = singleUnderscoreEmphasis.replace(text, "$1")

        // Remove raw paths detected by FilePaths as well as general absolute/home/Windows paths.
        for (path in FilePaths.find(text)) {
            text = text.replace(path, " ")
        }
        text = windowsPath.replace(text, " ")
        text = homePath.replace(text, " ")
        text = absolutePath.replace(text, " ")

        // Remove internal UUIDs, session/task/plan IDs, and raw model IDs.
        text = uuidPattern.replace(text, " ")
        text = compositeSessionId.replace(text, " ")
        text = prefixedInternalId.replace(text, " ")
        text = rawModelId.replace(text, " ")

        text = text.replace("|", " ")
        text = whitespace.replace(text, " ").trim()
        text = spaceBeforePunct.replace(text, "$1")
        text = openParenLeadingPunct.replace(text, "$1")
        text = repeatedMiddleDot.replace(text, " · ")
        text = text.trim('·', ':', '-', '—', ' ')

        if (text.isEmpty() || maxChars <= 0) return ""
        return if (text.length <= maxChars) text else text.clipped(maxChars).trimEnd() + "…"
    }

    /** Plain single-line pending interaction title, falling back to DESIGN_SYSTEM.md §5.6 wording. */
    fun pendingTitle(interaction: Interaction, resources: Resources): String {
        val candidate = when (interaction.kind) {
            "plan" -> interaction.summary?.takeIf(String::isNotBlank)
                ?: interaction.plan?.lineSequence()?.map(String::trim)?.firstOrNull(String::isNotEmpty)
            else -> interaction.askWords()?.resolve(resources)
        }
        val cleaned = sanitize(candidate, maxChars = PENDING_MAX_CHARS)
        if (cleaned.isNotEmpty()) return cleaned
        return pendingFallback(interaction.kind, resources)
    }

    fun pendingFallback(kind: String, resources: Resources): String = resources.getString(
        when (kind) {
            "plan" -> R.string.shortcuts_main_chat_pending_plan
            "permission" -> R.string.shortcuts_main_chat_pending_permission
            "question" -> R.string.shortcuts_main_chat_pending_question
            "screen" -> R.string.shortcuts_main_chat_pending_screen
            else -> R.string.shortcuts_main_chat_pending_default
        },
    )

    fun todoStateLabel(state: DirectionTaskState, resources: Resources): String = resources.getString(
        when (state) {
            DirectionTaskState.NeedsYou -> R.string.shortcuts_main_chat_todo_needs_you
            DirectionTaskState.Review -> R.string.shortcuts_main_chat_todo_review
            DirectionTaskState.Stuck -> R.string.shortcuts_main_chat_todo_stuck
            DirectionTaskState.Working -> R.string.shortcuts_main_chat_todo_working
            DirectionTaskState.Planned -> R.string.shortcuts_main_chat_todo_planned
        },
    )

    /**
     * Formats [updatedAt] as a wall-clock time on the same calendar day (`14:05` / `2:05 PM`) or a short
     * localized month-day (`Nov 22`) on earlier days. Wall-clock times stay accurate on idle home-screen
     * widgets without periodic polling.
     */
    fun formatHeaderTime(
        updatedAt: String?,
        context: Context,
        now: Instant = Instant.now(),
        zone: ZoneId = ZoneId.systemDefault(),
    ): String? {
        val instant = parseTime(updatedAt) ?: return null
        val locale = LocalizedFormats.locale(context)
        val sameDay = instant.atZone(zone).toLocalDate() == now.atZone(zone).toLocalDate()
        val skeleton = if (sameDay) LocalizedFormats.TIME else LocalizedFormats.MONTH_DAY
        return LocalizedFormats.format(instant, skeleton, locale, zone)
    }

    private const val PENDING_MAX_CHARS = 96
}

/**
 * Pairing-scoped disk cache for [MainChatWidgetSnapshot] in SharedPreferences (`main_chat_widget`),
 * enabling instant local-first widget rendering before live hub streams settle and preventing any
 * cross-hub leakage across unpair or re-pair.
 */
internal object MainChatWidgetStore {
    const val PREFS_NAME = "main_chat_widget"
    private const val KEY_PAIRING = "pairing"
    private const val KEY_SNAPSHOT = "snapshot"

    private data class StoredRecord(
        val pairingScope: String,
        val snapshot: MainChatWidgetSnapshot,
    )

    private fun snapshotKey(conversationKey: String): String =
        if (conversationKey.isBlank() || conversationKey == ConversationWidgetPrefs.MAIN_CONVERSATION_KEY) KEY_SNAPSHOT
        else "${KEY_SNAPSHOT}_$conversationKey"

    fun save(
        context: Context,
        pairingScope: String?,
        snapshot: MainChatWidgetSnapshot,
        conversationKey: String = ConversationWidgetPrefs.MAIN_CONVERSATION_KEY,
    ) {
        if (!snapshot.paired) {
            clear(context)
            return
        }
        val scope = pairingScope?.takeIf(String::isNotBlank) ?: return
        val prefs = context.applicationContext.getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE)
        val storedPairing = prefs.getString(KEY_PAIRING, null)?.takeIf(String::isNotBlank)
        prefs.edit {
            if (storedPairing != null && storedPairing != scope) {
                val _ = clear()
            }
            val _ = putString(KEY_PAIRING, scope)
            val _ = putString(snapshotKey(conversationKey), encode(snapshot, scope))
        }
    }

    /**
     * Reads the persisted snapshot only when both [KEY_PAIRING] and the stored JSON `"pairing"` field
     * match [activePairing]. Legacy unscoped records, corrupt payloads, or snapshots belonging to a
     * different pairing are cleared immediately and never returned.
     */
    fun read(
        context: Context,
        activePairing: String?,
        conversationKey: String = ConversationWidgetPrefs.MAIN_CONVERSATION_KEY,
    ): MainChatWidgetSnapshot? {
        val prefs = context.applicationContext.getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE)
        val storedPairing = prefs.getString(KEY_PAIRING, null)?.takeIf(String::isNotBlank)
        val scope = activePairing?.takeIf(String::isNotBlank)
        if (scope == null || storedPairing != scope) {
            if (storedPairing != null || prefs.all.isNotEmpty()) clear(context)
            return null
        }
        val key = snapshotKey(conversationKey)
        val raw = prefs.getString(key, null) ?: return null
        val record = decodeRecord(raw)
        if (record == null || record.pairingScope != scope) {
            if (key == KEY_SNAPSHOT) clear(context) else prefs.edit { val _ = remove(key) }
            return null
        }
        return record.snapshot
    }

    fun clear(context: Context) {
        val prefs = context.applicationContext.getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE)
        prefs.edit { val _ = clear() }
    }

    internal fun encode(snapshot: MainChatWidgetSnapshot, pairingScope: String): String {
        val root = JSONObject()
        val _ = root.put("pairing", pairingScope)
        val _ = root.put("paired", snapshot.paired)
        val _ = root.put("phase", snapshot.phase.wire)
        snapshot.mainSession?.let {
            val _ = root.put("main_machine", it.machineId)
            val _ = root.put("main_session", it.sessionId)
        }
        val _ = root.put("summary", snapshot.summary)
        snapshot.updatedAt?.let { val _ = root.put("updated_at", it) }
        val _ = root.put("pending_count", snapshot.pendingCount)
        snapshot.pending?.let { pending ->
            val obj = JSONObject()
            val _ = obj.put("id", pending.id)
            val _ = obj.put("machine_id", pending.machineId)
            val _ = obj.put("session_id", pending.sessionId)
            pending.assistant?.let { val _ = obj.put("assistant", it) }
            val _ = obj.put("kind", pending.kind)
            val _ = obj.put("title", pending.title)
            val _ = root.put("pending", obj)
        }
        if (snapshot.todos.isNotEmpty()) {
            val arr = JSONArray()
            snapshot.todos.forEach { todo ->
                val obj = JSONObject()
                val _ = obj.put("id", todo.id)
                val _ = obj.put("title", todo.title)
                val _ = obj.put("state", todo.state)
                val _ = arr.put(obj)
            }
            val _ = root.put("todos", arr)
        }
        snapshot.title?.let { val _ = root.put("title", it) }
        val _ = root.put("is_main_chat", snapshot.isMainChat)
        snapshot.targetAssistant?.let { val _ = root.put("target_assistant", it) }
        if (snapshot.bindingMissing) {
            val _ = root.put("binding_missing", true)
        }
        return root.toString()
    }

    internal fun decode(raw: String, expectedPairing: String): MainChatWidgetSnapshot? {
        val scope = expectedPairing.takeIf(String::isNotBlank) ?: return null
        val record = decodeRecord(raw) ?: return null
        return if (record.pairingScope == scope) record.snapshot else null
    }

    private fun decodeRecord(raw: String): StoredRecord? = runCatching {
        val root = JSONObject(raw)
        val pairingScope = root.optString("pairing", "").takeIf(String::isNotBlank) ?: return null
        val paired = root.optBoolean("paired", false)
        val phase = MainChatPhase.fromWire(root.optString("phase", MainChatPhase.Unpaired.wire))
        val machine = root.optString("main_machine", "").takeIf { it.isNotEmpty() }
        val session = root.optString("main_session", "").takeIf { it.isNotEmpty() }
        val mainKey = if (machine != null && session != null) SessionKey(machine, session) else null
        val pending = root.optJSONObject("pending")?.let { obj ->
            val id = obj.optString("id", "")
            val mid = obj.optString("machine_id", "")
            val sid = obj.optString("session_id", "")
            val title = obj.optString("title", "")
            if (id.isNotEmpty() && title.isNotEmpty()) {
                MainChatPendingItem(
                    id = id,
                    machineId = mid,
                    sessionId = sid,
                    assistant = if (obj.has("assistant")) obj.optBoolean("assistant") else null,
                    kind = obj.optString("kind", "unknown"),
                    title = title,
                )
            } else null
        }
        val todos = mutableListOf<MainChatTodoItem>()
        root.optJSONArray("todos")?.let { arr ->
            for (i in 0 until arr.length()) {
                val obj = arr.optJSONObject(i) ?: continue
                val id = obj.optString("id", "")
                val title = obj.optString("title", "")
                if (id.isNotEmpty() && title.isNotEmpty()) {
                    todos += MainChatTodoItem(id, title, obj.optString("state", DirectionTaskState.Working.wire))
                }
            }
        }
        StoredRecord(
            pairingScope = pairingScope,
            snapshot = MainChatWidgetSnapshot(
                paired = paired,
                phase = if (paired) phase else MainChatPhase.Unpaired,
                mainSession = mainKey,
                summary = root.optString("summary", ""),
                updatedAt = root.optString("updated_at", "").takeIf { it.isNotEmpty() },
                pendingCount = root.optInt("pending_count", 0).coerceAtLeast(0),
                pending = pending,
                todos = todos.take(MainChatWidgetState.MAX_TODOS),
                title = root.optString("title", "").takeIf { it.isNotEmpty() },
                isMainChat = root.optBoolean("is_main_chat", true),
                targetAssistant = if (root.has("target_assistant")) root.optBoolean("target_assistant") else true,
                bindingMissing = root.optBoolean("binding_missing", false),
            ),
        )
    }.getOrNull()
}
