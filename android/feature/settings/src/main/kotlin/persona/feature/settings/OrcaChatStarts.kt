package persona.feature.settings

import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import persona.core.contracts.Machine
import persona.core.designsystem.JSwitchRow
import persona.core.designsystem.LocalAppFeedback

/**
 * "Start new Agents as Orca chats" on a machine's Engine page (#17650, docs/integrations/engines.md "Orca chats as New
 * Agent's default"), as the web's Engine section has it. The hub sends the switch on the machine ([Machine.orcaChatSwitch])
 * only while Orca is in its engine set, so it shows exactly there; where a start goes is the hub's to decide. A change
 * shows at once and settles on the hub's answer; a failed save puts the old value back. A push of the machine moves the
 * switch while nothing is being saved here.
 */
class OrcaChatStartsModel(initial: Boolean?, private val put: suspend (machineId: String, on: Boolean) -> Boolean) {
    /** [shown]: the switch as drawn (null: no switch); [saving]: a save is under way. */
    data class State(val shown: Boolean? = null, val saving: Boolean = false)

    private val mutable = MutableStateFlow(State(shown = initial))
    val state: StateFlow<State> = mutable.asStateFlow()

    /** The machine as the hub pushed it. */
    fun synchronize(machine: Machine) {
        if (!mutable.value.saving) mutable.update { it.copy(shown = machine.orcaChatSwitch) }
    }

    /** Saves [on] for [machineId]: null once saved, else what went wrong (the old value is back). */
    suspend fun change(machineId: String, on: Boolean): Throwable? {
        val before = mutable.value.shown ?: return null
        if (mutable.value.saving || before == on) return null
        mutable.update { it.copy(shown = on, saving = true) }
        return try {
            val saved = put(machineId, on)
            mutable.update { it.copy(shown = saved) } // until the machine's push says so too
            null
        } catch (cancelled: CancellationException) {
            mutable.update { it.copy(shown = before) }
            throw cancelled
        } catch (error: Exception) {
            mutable.update { it.copy(shown = before) }
            error
        } finally {
            mutable.update { it.copy(saving = false) }
        }
    }
}

/** The switch's row; nothing where the hub sends no switch. */
@Composable
internal fun OrcaChatStartsRow(machine: Machine, model: OrcaChatStartsModel, modifier: Modifier = Modifier) {
    LaunchedEffect(machine) { model.synchronize(machine) }
    val state by model.state.collectAsState()
    val on = state.shown ?: return
    val scope = rememberCoroutineScope()
    val feedback = LocalAppFeedback.current
    JSwitchRow(
        stringResource(R.string.settings_engine_orca_chats),
        on,
        { next -> scope.launch { model.change(machine.id, next)?.let { feedback?.failure(it) } } },
        modifier.testTag("orca-chat-starts"),
        subtitle = stringResource(R.string.settings_engine_orca_chats_detail),
        enabled = !state.saving,
        subtitleLines = Int.MAX_VALUE,
        titleLines = 2,
    )
}
