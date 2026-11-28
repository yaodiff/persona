package persona.feature.agents

import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyListScope
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.dp
import persona.core.contracts.*
import persona.core.designsystem.*
import persona.core.designsystem.dense
import persona.core.designsystem.icons.PersonaIcons
import persona.core.designsystem.icons.PhIcons
import persona.core.domain.sort.*
import persona.core.domain.answer.firstBySession
import persona.core.domain.answer.rowLine
import persona.core.domain.modelShortName
import java.time.Instant

/**
 * One herdr space, with the same actions as the home list. A page of its own ([spaceEntries]): beside the list in a
 * wide window, pushed on a phone; [onDismiss] goes back. Glass draws it as iOS's `SpaceView` ([spaceCards]); Brief and
 * Board keep its rows in tab order.
 */
@Composable
internal fun SpaceScreen(
    space: AgentSpace?, machines: List<Machine>, pending: List<Interaction>, preferences: AgentsPreferencesSnapshot,
    busy: Boolean, canStart: Boolean, toast: JToastState, now: Instant,
    onDismiss: () -> Unit, onOpen: (AgentSession) -> Unit, onNewTab: (AgentSpace) -> Unit,
    spaceMenu: @Composable (AgentSpace) -> Unit, sessionMenu: @Composable (AgentSession) -> List<JAction>,
    /** The swipe that closes a row's agent, when it can be closed (as in the list). */
    onClose: (AgentSession) -> (() -> Unit)? = { null },
    /** The name to show before the sessions are read; until then ([loaded] false) the page shows no empty state. */
    title: String = "",
    loaded: Boolean = true,
    /** Opens a computer's projects (iOS `MachineSpacesView`), from the computer at the top of the cards. */
    onMachine: ((String) -> Unit)? = null,
    /** All raw sessions on this hub, so parent rows can name unread folded side sessions (#13524). */
    sessions: List<Session> = emptyList(),
    /** Opens an unread folded side session directly in one tap (#13524). */
    onOpenSide: ((AgentSession, SessionKey) -> Unit)? = null,
) {
    val cards = remember(pending) { pending.firstBySession() }
    val visibleSessions = remember(sessions, preferences) { sessions.map { it.listSession(preferences).session } }
    val visibleSessionsByKey = remember(visibleSessions) { visibleSessions.associateBy { it.key } }
    val grid = AgentSpaceLayout.cards
    val machine = machines.firstOrNull { it.id == space?.machineId }
    Box(Modifier.fillMaxSize()) {
        // The cards name the computer in their first line, under a large title as on iOS.
        JListPage(title = space?.name ?: title.ifBlank { stringResource(R.string.agents_space) }, onBack = if (LocalIsSidebarRoot.current) null else onDismiss,
            subtitle = machine?.displayName?.takeUnless { grid }, largeTitle = grid,
            actions = { space?.let { spaceMenu(it) } }) {
            if (space == null) {
                if (loaded) item("empty") {
                    JEmptyState(stringResource(R.string.agents_space_empty), stringResource(R.string.agents_space_empty_hint), icon = PhIcons.Robot)
                }
            } else if (grid) {
                spaceCards(space, machine, pending, cards, preferences, busy, canStart, now, onOpen, onNewTab, sessionMenu, onMachine)
            } else {
                item("location") {
                    JRow(stringResource(R.string.agents_directory), subtitle = space.cwd, leadingIcon = PhIcons.FolderSimple)
                    if (busy) JLinearProgress(null)
                }
                // the agents of one tab follow each other in pane order: the launcher marked, the panes it split off under it (#2320)
                val tabs = agentTabs(space.members, preferences.mutes)
                itemsIndexed(space.members, key = { _, row -> row.id }) { index, row ->
                    val requests = pending.count { it.sessionKey == row.session.key }
                    val card = cards[row.session.key]
                    val tab = row.session.tabKey?.let(tabs::get)
                    val branch = tab?.isBranch(row) == true
                    val sideUnread = unreadSideSummary(row.session, visibleSessionsByKey[row.session.key], visibleSessions)
                    SlotRow(row, machines.firstOrNull { it.id == row.session.machineId }?.displayName,
                        AgentRowMetadata(), now, false, row.id in preferences.pins.sessions, preferences.mutes.mutes(row),
                        depth = if (branch) 1 else 0, position = GroupPosition.of(index, space.members.size), located = false,
                        launcher = tab?.launcher?.id == row.id, branch = branch,
                        onOpen = { onOpen(row) }, menuItems = { sessionMenu(row) },
                        slotLabel = row.session.slotName?.takeIf { it.isNotBlank() && it != row.session.displayTitle }
                            // herdr's default tab names are numbers; a bare "1" reads as a count, so say #1.
                            ?.let { if (it.all(Char::isDigit)) "#$it" else it }
                            ?: stringResource(R.string.agents_space_tab, row.session.slotIndex ?: 0), requestCount = requests, onClose = onClose(row), card = card,
                        sideUnread = sideUnread, onOpenSide = onOpenSide?.let { cb -> { side -> cb(row, side) } },
                        preferHerdrTabName = preferences.prefersHerdrTabName(row.session))
                }
                if (canStart) item("new-tab") {
                    JButton(stringResource(R.string.agents_space_new_tab), { onNewTab(space) }, Modifier.fillMaxWidth().padding(top = 16.dp.dense), enabled = !busy, icon = PhIcons.Plus)
                }
            }
        }
        JToastHost(toast, Modifier.align(Alignment.TopCenter).statusBarsPadding().padding(top = persona.core.designsystem.ToastTopBarClearance))
    }
}

/**
 * The space as iOS's `SpaceView` lays it out: the computer (it opens the computer's page) and the folder shortened with
 * ~, then a card per agent two to a row, the most urgent first ([spaceCardOrder]), and a dashed card that opens a new tab
 * there while one can be started. A card's long press offers the row's actions; it has no swipe.
 */
private fun LazyListScope.spaceCards(
    space: AgentSpace, machine: Machine?, pending: List<Interaction>, cards: Map<SessionKey, Interaction>,
    preferences: AgentsPreferencesSnapshot, busy: Boolean, canStart: Boolean, now: Instant,
    onOpen: (AgentSession) -> Unit, onNewTab: (AgentSpace) -> Unit, sessionMenu: @Composable (AgentSession) -> List<JAction>,
    onMachine: ((String) -> Unit)?,
) {
    item("location") {
        AgentSpaceLocation(machine?.displayName ?: space.machineId, shortPath(space.cwd, machine?.home),
            icon = machine?.deviceIcon ?: PersonaIcons.machine, onMachine = onMachine?.let { open -> { open(space.machineId) } })
        if (busy) JLinearProgress(null)
    }
    val order = spaceCardOrder(space.members)
    val tabs = agentTabs(order, preferences.mutes)
    // null: the new tab's card, last
    val cells: List<AgentSession?> = order + if (canStart) listOf(null) else emptyList()
    cells.chunked(2).forEachIndexed { index, pair ->
        item(pair.joinToString("|") { it?.id ?: "new-tab" }, contentType = "space-cards") {
            val cell: @Composable (AgentSession?) -> Unit = { row ->
                if (row == null) AgentNewTabCard(stringResource(R.string.agents_space_new_tab_card), { onNewTab(space) }, Modifier.fillMaxHeight(), enabled = !busy)
                else SpaceCard(row, row.session.tabKey?.let(tabs::get), pending.count { it.sessionKey == row.session.key }, cards[row.session.key],
                    preferences.mutes.mutes(row), now, { onOpen(row) }, sessionMenu,
                    preferHerdrTabName = preferences.prefersHerdrTabName(row.session))
            }
            AgentCardRow(Modifier.padding(top = if (index == 0) Tokens.Space.l else Tokens.Space.m),
                end = { if (pair.size > 1) cell(pair[1]) }) { cell(pair[0]) }
        }
    }
}

/** One agent's card ([AgentSlotCard]); its long press opens the row's actions. */
@Composable
private fun SpaceCard(
    row: AgentSession, tab: AgentTab?, requests: Int, card: Interaction?, muted: Boolean, now: Instant,
    onOpen: () -> Unit, sessionMenu: @Composable (AgentSession) -> List<JAction>,
    preferHerdrTabName: Boolean = false,
) {
    var menu by remember { mutableStateOf(false) }
    val session = row.session
    val primaryTab = session.displayTabName(preferHerdrTabName)
    val title = session.displayTitle(preferHerdrTabName)
    // iOS `stateLabel(muted:)`: Awaiting reply, Waiting in the background and Working (Background) included.
    val state = row.stateWord(muted).text()
    AgentSlotCard(title, row.state.designStatus, state, onOpen, Modifier.fillMaxHeight(),
        stateTone = row.rowStatus(muted), tab = if (primaryTab != null) session.displayTitleDetail(preferHerdrTabName) else session.slotLabel,
        pane = tab?.paneName(row), agent = session.agentLabel,
        starter = tab?.launcher?.id == row.id, pending = requests, preview = session.rowLine(card, now, muted)?.text(),
        model = modelShortName(session.model).ifEmpty { null }, waiting = row.waitsInBackground, onLongClick = { menu = true })
    if (menu) JActionSheet(sessionMenu(row), { menu = false }, title = title)
}
