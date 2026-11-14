package persona.feature.terminal

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import persona.core.contracts.Machine
import persona.core.contracts.api.NewPaneTab
import persona.core.contracts.api.FinishedPanes
import persona.core.contracts.api.PaneItem
import persona.core.contracts.api.PanesResponse
import persona.core.contracts.api.SessionsRepository
import persona.core.contracts.api.TerminalAddress
import persona.core.contracts.api.TerminalPanes
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.*

/** One machine's listing: what was last read (kept when a later read fails, so the list does not blink out) and why the last read failed. */
data class MachinePanes(val response: PanesResponse? = null, val error: String? = null)

/** A machine of the "Terminal" page with its tree; [listing] is null until its first read came back. */
data class TerminalMachine(val machine: Machine, val listing: MachinePanes?, val spaces: List<TermSpace>)

/** The exact preview the user is confirming; new panes discovered later are never added to this batch. */
data class FinishedConfirmation(val machineId: String, val preview: FinishedPanes, val rows: List<TermPane>)

data class TerminalPanesUiState(
    val machines: List<TerminalMachine> = emptyList(),
    /** A read is on the way (the first one, "Refresh" or a pull). */
    val busy: Boolean = false,
    /** The workspace a "New tab" is being opened in ("machine\nworkspace"). */
    val opening: String? = null,
)

sealed interface TerminalPanesEvent {
    /** Open this pane's terminal: [terminal] is its address. */
    data class Open(val machineId: String, val terminal: String, val title: String) : TerminalPanesEvent
    data class NewTabFailed(val error: String) : TerminalPanesEvent
    data class CloseFailed(val error: String) : TerminalPanesEvent
    data class Closed(val count: Int, val kept: Int = 0) : TerminalPanesEvent
    data object NoFinished : TerminalPanesEvent
}

/**
 * "Terminal" (docs/integrations/terminal.md "Standalone terminal"): every online machine's panes, read when the page is in front and every few seconds
 * while it stays (like the web, agents' states are live from the sessions already), and "New tab" in a workspace.
 */
class TerminalPanesViewModel(private val panes: TerminalPanes, private val sessions: SessionsRepository) : ViewModel() {
    private val listing = MutableStateFlow<Map<String, MachinePanes>>(emptyMap())
    private val busy = MutableStateFlow(false)
    private val opening = MutableStateFlow<String?>(null)
    private val _events = MutableSharedFlow<TerminalPanesEvent>(extraBufferCapacity = 4)
    val events = _events.asSharedFlow()
    private val _confirmation = MutableStateFlow<FinishedConfirmation?>(null)
    val confirmation = _confirmation.asStateFlow()
    private val _closing = MutableStateFlow(false)
    val closing = _closing.asStateFlow()
    private val _closingMachine = MutableStateFlow<String?>(null)
    val closingMachine = _closingMachine.asStateFlow()

    val state: StateFlow<TerminalPanesUiState> = combine(sessions.machines, sessions.sessions, listing, busy, opening) { machines, all, read, reading, open ->
        // A machine of a hub without terminal_panes (another hub group) is left out: it has nothing to list.
        TerminalPanesUiState(machines.filter { panes.lists(it.id) }.map { machine ->
            val panes = read[machine.id]
            TerminalMachine(machine, panes, if (machine.online) terminalTree(machine.id, panes?.response, all, machine.home) else emptyList())
        }, reading, open)
    }.stateIn(viewModelScope, SharingStarted.Eagerly, TerminalPanesUiState())

    /** Runs while the page is in front: a read now, then every [REFRESH_MILLIS]. */
    suspend fun poll() {
        while (currentCoroutineContext().isActive) {
            load(showBusy = listing.value.isEmpty())
            delay(REFRESH_MILLIS)
        }
    }

    fun refresh() { viewModelScope.launch { load(showBusy = true) } }

    fun previewFinished(machineId: String) = cleanup(machineId) {
        val preview = panes.closeFinished(machineId, preview = true)
        if (preview.closed.isEmpty()) _events.emit(TerminalPanesEvent.NoFinished)
        else {
            val bySession = sessions.sessions.value.filter { it.machineId == machineId }.associateBy { it.sessionId }
            val listed = listing.value[machineId]?.response?.hosts.orEmpty().flatMap { it.panes }.associateBy { it.pane }
            val rows = preview.closed.map { candidate ->
                val item = (listed[candidate.pane] ?: PaneItem(candidate.pane)).copy(
                    agent = null, sessionId = null, agentSession = candidate.sessionId)
                pane(machineId, item, bySession[candidate.sessionId], null)
            }
            _confirmation.value = FinishedConfirmation(machineId, preview, rows)
        }
    }

    fun dismissFinished() { if (!_closing.value) _confirmation.value = null }

    fun confirmFinished() {
        val confirmed = _confirmation.value ?: return
        cleanup(confirmed.machineId) {
            val result = panes.closeFinished(confirmed.machineId, panes = confirmed.preview.closed.map { it.pane })
            _confirmation.value = null
            _events.emit(TerminalPanesEvent.Closed(result.closed.size, result.kept.size))
            afterClose(confirmed.machineId)
        }
    }

    fun close(machineId: String, pane: String) = cleanup(machineId) {
        panes.close(machineId, pane)
        _events.emit(TerminalPanesEvent.Closed(1))
        afterClose(machineId)
    }

    private suspend fun afterClose(machineId: String) {
        load(listOf(machineId), showBusy = false)
        // A later list failure cannot turn a confirmed close into a failed close.
        try { sessions.refresh() }
        catch (e: CancellationException) { throw e }
        catch (_: Exception) { }
    }

    private fun cleanup(machineId: String, block: suspend () -> Unit) {
        if (_closing.value) return
        _closing.value = true
        _closingMachine.value = machineId
        viewModelScope.launch {
            try { block() }
            catch (e: CancellationException) { throw e }
            catch (e: Exception) { _events.emit(TerminalPanesEvent.CloseFailed(e.message.orEmpty())) }
            finally { _closing.value = false; _closingMachine.value = null }
        }
    }

    /** "New tab": a shell tab in [space], opened as a terminal once the hub answers. */
    fun newTab(machineId: String, space: TermSpace) = open(machineId, openingKey(machineId, space), space.label) {
        panes.newTab(machineId, space.id, space.host.ifEmpty { null })
    }

    /** Whether this machine's own hub can open a home-directory terminal. */
    fun opensNewSpaces(machineId: String) = panes.opensNewSpaces(machineId)

    /** "New Terminal": the first shell on a computer whose terminal host has no workspace yet. */
    fun newTerminal(machineId: String, kind: String) = open(machineId, homeOpeningKey(machineId, kind), "") {
        panes.newTab(machineId, "", kind.ifEmpty { null }, cwd = "~")
    }

    private fun open(machineId: String, id: String, title: String, request: suspend () -> NewPaneTab) {
        if (opening.value != null) return
        opening.value = id
        viewModelScope.launch {
            try {
                val tab = request()
                _events.emit(TerminalPanesEvent.Open(machineId, TerminalAddress.key(machineId, tab.terminal).sessionId, title))
                load(listOf(machineId), showBusy = false)
            } catch (e: CancellationException) { throw e }
            catch (e: Exception) { _events.emit(TerminalPanesEvent.NewTabFailed(e.message.orEmpty())) }
            finally { opening.value = null }
        }
    }

    private suspend fun load(ids: List<String> = online(), showBusy: Boolean) {
        if (ids.isEmpty()) return
        if (showBusy) busy.value = true
        try {
            coroutineScope {
                ids.map { id ->
                    async {
                        val read = try { MachinePanes(panes.panes(id)) }
                            catch (e: CancellationException) { throw e }
                            catch (e: Exception) { MachinePanes(listing.value[id]?.response, e.message.orEmpty().ifEmpty { e.javaClass.simpleName }) }
                        listing.update { it + (id to read) }
                    }
                }.awaitAll()
            }
        } finally { if (showBusy) busy.value = false }
    }

    private fun online() = sessions.machines.value.filter { it.online && panes.lists(it.id) }.map { it.id }

    companion object {
        /** How often the layout is read again while the page is in front (the web's REFRESH_MS). */
        const val REFRESH_MILLIS = 8_000L
    }
}
