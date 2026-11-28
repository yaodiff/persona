package persona.feature.settings

import persona.core.contracts.Machine
import persona.core.designsystem.failureReasonText
import persona.core.domain.engines.Engines
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update

internal data class EngineSelectionState(
    val selected: String = Engines.DEFAULT,
    val saving: Boolean = false,
    /** Why the last save failed. */
    val error: SettingsUiText? = null,
    /** The user picked something; pushes about the machine no longer move the choice. */
    val edited: Boolean = false,
)

/**
 * The engine picker (iOS `EngineSelectionView`): the choice stays on the phone until the hub accepts the PUT, and the
 * machine the hub returns is applied at once ([applied]) so the page shows it before the `machines` push. Outside [pairing]
 * only a change is sent (#17052): a Save that repeats what the machine has would rewrite a set chosen elsewhere. A pick
 * inside a set of several engines keeps the set with it as the default ([putDefault], `Engines.saveOf`).
 */
internal class EngineSelectionModel(
    machine: Machine,
    private val put: suspend (machineId: String, engine: String) -> Machine,
    private val applied: suspend (Machine) -> Unit,
    private val putDefault: suspend (machineId: String, hosts: List<String>, default: String) -> Machine = { id, _, default -> put(id, default) },
    private val pairing: Boolean = false,
) {
    private val mutable = MutableStateFlow(EngineSelectionState(selected = Engines.initialChoice(machine)))
    val state = mutable.asStateFlow()

    /** The machine as last known: the one the page opened on, then each push and the hub's answer to a save. */
    @Volatile private var machine: Machine = machine

    /** What the last save the hub took sent: a new default inside the set, or an engine. */
    @Volatile var lastSaved: Engines.Save? = null
        private set

    fun choose(id: String) = mutable.update { it.copy(selected = id, edited = true, error = null) }

    /** Live detection and current-engine changes move the picker until the user starts editing it. */
    fun synchronize(machine: Machine) {
        this.machine = machine
        mutable.update { if (it.edited || it.saving) it else it.copy(selected = Engines.initialChoice(machine)) }
    }

    /** True when the hub took the choice; false with the reason in the state otherwise (the page stays). */
    suspend fun save(machineId: String): Boolean {
        val current = state.value
        if (current.saving || !Engines.canSave(current.selected, machine, pairing)) return false
        mutable.update { it.copy(saving = true, error = null) }
        return try {
            val save = Engines.saveOf(current.selected, machine)
            val saved = when (save) {
                is Engines.Save.Default -> putDefault(machineId, save.hosts, save.default)
                is Engines.Save.Engine -> put(machineId, save.id)
            }
            machine = saved
            lastSaved = save
            try { applied(saved) } catch (cancelled: CancellationException) { throw cancelled } catch (_: Exception) { }
            mutable.update { it.copy(selected = Engines.current(saved), edited = false) }
            true
        } catch (cancelled: CancellationException) {
            throw cancelled
        } catch (error: Exception) {
            mutable.update { it.copy(error = SettingsUiText.Res(R.string.settings_engine_save_failed, listOf(failureReasonText(error)))) }
            false
        } finally {
            mutable.update { it.copy(saving = false) }
        }
    }
}
