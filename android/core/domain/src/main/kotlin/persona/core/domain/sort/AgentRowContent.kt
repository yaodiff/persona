package persona.core.domain.sort

import persona.core.contracts.AgentState
import persona.core.contracts.CoreText
import persona.core.contracts.CoreText.Key
import persona.core.contracts.Session
import persona.core.domain.engines.Engines
import persona.core.domain.modelShortName

// What an Agents row says beside its title and its latest words, as iOS's default row draws it (the Avatar style:
// `AgentRowStyles.swift`, `SlotRow` in `ChatsView.swift`; #4338 A9). The rows draw it; the rules are here, with their tests.

/**
 * An Agents row's words: line 2's state word, and line 3's parts in iOS's order (`SlotRow.placeLine`): where it was
 * opened, where it runs, the model, its to-do, the bell of a muted one.
 */
data class AgentRowContent(
    /**
     * Before the latest words: Needs you, Awaiting reply, Just finished or Waiting in the background; null where its mark
     * says it alone.
     * State Cards spell every state but Seen out at the card's foot instead ([cardStateWord]).
     */
    val stateWord: CoreText?,
    /** Where it was opened, the tag line 3 starts with (iOS `sourceLabel`): a Lark group's topic or a terminal pane. */
    val source: AgentSource?,
    /**
     * Where it runs, joined with " · " (iOS `placeText`): the tab (its name in a tab of several, else a source and the
     * agent's name there; never the agent's kind, which its mark shows), Started it, the host, space › tab, the computer.
     * Unlike iOS's subtitle it leaves out why a source is view only: Android's row keeps that among its critical facts,
     * which never give way to the rest of the line.
     */
    val place: List<CoreText>,
    /**
     * The model it runs on, by its short name ([rowModel]): never the raw id (`DESIGN_SYSTEM.md` 3.4, 5.6), which iOS's
     * row still shows.
     */
    val model: String?,
    /** Its to-do's title. */
    val todo: String?,
    /** Notifications muted: the bell at the line's end. */
    val muted: Boolean,
) {
    /** Line 3 shows (iOS `hasPlaceLine`): something is said on it. The model alone does not make one. */
    val hasPlaceLine: Boolean get() = source != null || place.isNotEmpty() || todo != null || muted
}

/** An agent's place in a tab of several (iOS `TabPanes.Role`): what it is called there, and whether it started the tab. */
data class AgentPane(val name: String, val launcher: Boolean)

/**
 * Where a row is drawn, which decides what its line 3 names (iOS `SlotRow`'s `space`, `machine`, `host` and `pane`).
 */
data class AgentRowContext(
    /**
     * Outside the computer tree (Pinned, By Status, By To-do): the row names its space, before the tab ("app › #2").
     * In the tree its heading is the space, and the tab is named alone.
     */
    val located: Boolean = false,
    /**
     * Its computer's name, given only while the list mixes computers ([mixesComputers]); named by a [located] row
     * alone, as iOS's `rowMachine`.
     */
    val machine: String? = null,
    /** Name the host it runs in (botmux, Orca, Tern; herdr never): By To-do while the list mixes hosts ([mixesHosts]). */
    val host: Boolean = false,
    /** Under a tab of several: the tab's row says which tab, so the row names itself there instead. */
    val pane: AgentPane? = null,
    /** The tab's name where the page writes it its own way (the space page), else the session's ([slotLabel]). */
    val slot: String? = null,
    /** Under a By Status section that names its state (iOS `inStateSection`): no state word. */
    val stateSection: Boolean = false,
    /** Hub setting `use_herdr_tab_name`: promotes the herdr tab name to title and moves the CLI title to the detail line. */
    val preferHerdrTabName: Boolean = false,
)

/**
 * What [AgentRowContent] says for this session, drawn in [context] in the list's [display]; [muted]: it or its space is
 * muted.
 */
fun AgentSession.rowContent(
    context: AgentRowContext = AgentRowContext(),
    muted: Boolean = false,
    display: AgentListDisplay = AgentListDisplay.Avatar,
): AgentRowContent {
    val s = session
    val place = mutableListOf<CoreText>()
    val pane = context.pane
    val primaryTab = s.displayTabName(context.preferHerdrTabName)
    s.displayTitleDetail(context.preferHerdrTabName)?.let { place += CoreText.plain(it) }
    val source = s.source?.let { CoreText.joined(" · ", listOfNotNull(it.label, it.agentName?.let(CoreText::plain))) }
    (pane?.name?.let(CoreText::plain) ?: source)?.let {
        place += it
        if (pane?.launcher == true) place += CoreText.of(Key.AgentsStartedTab)
    }
    // the host by its name ("Orca", "Herdr App"), never its id (#17054)
    if (context.host) s.hostKind.takeIf { it.isNotEmpty() && it != "herdr" }?.let { place += CoreText.plain(Engines.displayName(it)) }
    val slot = if (pane == null && primaryTab == null) context.slot ?: s.slotLabel else null
    val space = if (context.located) s.spaceName?.takeIf(String::isNotBlank) ?: s.folderName.takeIf(String::isNotBlank) else null
    when {
        space != null -> place += CoreText.plain(slot?.let { "$space › $it" } ?: space)
        slot != null -> place += CoreText.plain(slot)
    }
    if (context.located) context.machine?.takeIf(String::isNotBlank)?.let { place += CoreText.plain(it) }
    return AgentRowContent(
        stateWord = when {
            seenPlace != null -> CoreText.of(Key.StateIdle)
            context.stateSection -> null
            display.rowParts.stateAtFoot -> cardStateWord(muted)
            else -> rowStateWord(muted)
        },
        source = s.agentSource?.takeIf { !s.assistant },
        place = place,
        model = s.rowModel,
        todo = s.todo?.title?.takeIf(String::isNotBlank),
        muted = muted,
    )
}

/**
 * The row's state in words, as iOS's default row: only what its dot doesn't say alone, Needs you (unless muted),
 * Awaiting reply, Just finished (#9786: Unread's color, as Awaiting reply) and Waiting in the background. Working, Unread
 * and the rest are the dot's.
 */
fun AgentSession.rowStateWord(muted: Boolean): CoreText? = when {
    muted -> null
    state == AgentState.NeedsYou -> CoreText.of(Key.StateNeedsYou)
    asksYou -> CoreText.of(Key.StateAwaitingReply)
    showsJustFinished -> CoreText.of(Key.StateJustFinished)
    waitsInBackground -> CoreText.of(Key.StateBackgroundWaiting)
    else -> null
}

/**
 * The model a row names: the session's model, else its launcher's (as iOS's `Session.modelShortName`), by its short name
 * (`modelShortName`, the web's `modelChipLabel`); null for none.
 */
val Session.rowModel: String? get() = modelShortName(model ?: launcher).takeIf(String::isNotEmpty)

/**
 * The list holds agents of more than one computer (iOS `AgentListProjection.byHost`), so a row outside the tree names
 * its own: the computers of the tree, else those the live rows run on. Never while one computer is picked.
 */
fun AgentListResult.mixesComputers(machineFilter: String): Boolean = machineFilter.isEmpty() &&
    if (machines.isNotEmpty()) machines.size > 1
    else groups.asSequence().flatMap { it.rows }.filter { it.isLive }.map { it.session.machineId }.distinct().take(2).count() > 1

/** Its live agents run in more than one host (iOS `todoSections`' `mixedHosts`): By To-do then names each row's. */
fun AgentListResult.mixesHosts(): Boolean =
    groups.asSequence().flatMap { it.rows }.filter { it.isLive }.map { it.session.hostKind }.filter(String::isNotEmpty).distinct().take(2).count() > 1
