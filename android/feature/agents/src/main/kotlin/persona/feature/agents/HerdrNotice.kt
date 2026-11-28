package persona.feature.agents

import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.onLongClick
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.style.TextOverflow
import persona.core.designsystem.JAction
import persona.core.designsystem.JActionSheet
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import persona.core.designsystem.icons.PhIcons
import persona.core.designsystem.failureDetail
import persona.core.designsystem.hubMessage
import persona.core.designsystem.hubOriginal
import persona.core.contracts.Machine
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.HerdrState
import persona.core.contracts.api.HubConnection
import persona.core.contracts.api.HubGroups
import persona.core.contracts.api.MachineManagement
import persona.core.contracts.api.SessionsRepository
import persona.core.domain.engines.Engines
import persona.core.designsystem.JNotice
import persona.core.designsystem.Status
import persona.core.designsystem.failureReasonText
import persona.core.designsystem.rememberCopy
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.async
import kotlinx.coroutines.awaitAll
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch

/**
 * Classic herdr is in the machine's engine set (`engine_hosts`; an older hub's single engine stands for its set), whether or not
 * it is the default host (#17037). A machine with no terminal engine (empty host set) keeps `engine` herdr and is not.
 */
internal val Machine.usesHerdr: Boolean get() = "herdr" in Engines.hosts(this)

internal fun Machine.herdrStopped(state: HerdrState?): Boolean =
    online && usesHerdr && state?.known == true && !state.running

/**
 * Why a start failed, kept on the row until the next start (iOS `HerdrFailure`): [text] in words, [detail] the hub's own
 * words and status for a report ("Copy details").
 */
internal data class HerdrFailure(val text: AgentText, val detail: String) {
    companion object {
        /** The node's own words are read first when the hub localized its summary (#5514); words it does not know show the summary. */
        fun of(error: Throwable): HerdrFailure = HerdrFailure(
            hubOriginal(error)?.let(::says)?.takeUnless { it.id == R.string.agents_text_raw }
                ?: hubMessage(error)?.let(::says) ?: AgentText(R.string.agents_text_raw, failureReasonText(error)),
            failureDetail(error))

        /** The node's and the hub's known failures of herdr.server.start (server/internal/node/herdrstart.go) in words. */
        fun says(message: String): AgentText {
            val m = message.trim()
            val stopped = m.indexOf("herdr server stopped right after starting")
            if (stopped >= 0) {
                val why = m.substring(stopped)
                Regex("exit status (-?\\d+)").find(why)?.let { return AgentText(R.string.agents_herdr_why_exited_code, it.groupValues[1]) }
                Regex("signal: ([\\w ]+)").find(why)?.let { return AgentText(R.string.agents_herdr_why_killed, it.groupValues[1].trim()) }
                return AgentText(R.string.agents_herdr_why_exited)
            }
            return when {
                "herdr is not installed" in m -> AgentText(R.string.agents_herdr_why_not_installed)
                "herdr did not answer on" in m -> Regex("within (\\d+)s").find(m)?.let { AgentText(R.string.agents_herdr_why_silent_for, it.groupValues[1]) }
                    ?: AgentText(R.string.agents_herdr_why_silent)
                "start herdr server:" in m -> AgentText(R.string.agents_herdr_why_command)
                "unknown method herdr.server.start" in m -> AgentText(R.string.agents_herdr_why_old_node)
                "machine is offline" in m -> AgentText(R.string.agents_herdr_why_offline)
                // iOS APIError.isTimeoutText: the hub gave up waiting (504, a deadline) before herdr answered.
                listOf("deadline exceeded", "timed out", "timeout", "超时").any { it in m.lowercase() } -> // i18n-ignore: matches the hub's error text
                    AgentText(R.string.agents_herdr_why_timeout)
                else -> AgentText.raw(m)
            }
        }
    }
}

internal class HerdrStarter(
    private val api: MachineManagement,
    private val repository: SessionsRepository,
    private val connection: HubConnection,
    /** Each computer is read and started through its own hub group's connection (AgentGroups.kt stateFor). */
    private val hubGroups: HubGroups,
    private val scope: CoroutineScope,
    private val notice: (AgentText) -> Unit,
) {
    private val pending = MutableStateFlow<Set<String>>(emptySet())
    val starting = pending.asStateFlow()
    private val statuses = MutableStateFlow<Map<String, HerdrState>>(emptyMap())
    val states = statuses.asStateFlow()
    private val failed = MutableStateFlow<Map<String, HerdrFailure>>(emptyMap())
    /** The last start's failure by machine, shown on its row until the next start or until herdr runs. */
    val failures = failed.asStateFlow()
    private var generation = 0

    suspend fun refresh() {
        val before = generation
        val machines = repository.machines.value.filter { it.online && it.usesHerdr && reachable(it.id) }
        val results = coroutineScope {
            machines.map { machine -> async {
                try { machine.id to api.herdr(machine.id) }
                catch (cancelled: CancellationException) { throw cancelled }
                catch (_: Exception) { null }
            } }.awaitAll().filterNotNull().toMap()
        }
        // A status read begun before a start must not replace its acknowledgement.
        if (before == generation) statuses.value = results + statuses.value.filterKeys { it in pending.value }
        failed.update { all -> all.filterKeys { results[it]?.running != true } }
    }

    fun start(machineId: String) {
        if (machineId in pending.value || !reachable(machineId)) return
        if (repository.machines.value.none { it.id == machineId && it.herdrStopped(statuses.value[machineId]) }) return
        generation++
        pending.update { it + machineId }
        failed.update { it - machineId }
        scope.launch {
            try {
                val result = api.startHerdr(machineId)
                val latest = repository.machines.value.firstOrNull { it.id == machineId } ?: return@launch
                generation++
                statuses.update { it + (machineId to result) }
                if (!(result.known && result.running)) failed.update { it + (machineId to
                    HerdrFailure(AgentText(R.string.agents_herdr_why_no_answer), "herdr.server.start: running false")) }
                notice(if (result.known && result.running) AgentText(R.string.agents_herdr_started, latest.displayName) else AgentText(R.string.agents_herdr_unconfirmed))
            } catch (cancelled: CancellationException) {
                throw cancelled
            } catch (e: Exception) {
                failed.update { it + (machineId to HerdrFailure.of(e)) }
                notice(AgentText(R.string.agents_herdr_failed, failureReasonText(e)))
            } finally {
                pending.update { it - machineId }
            }
        }
    }

    private fun reachable(machineId: String) = hubGroups.groups.value.stateFor(machineId, connection.state.value) == ConnectionState.Online
}

@OptIn(ExperimentalFoundationApi::class)
@Composable
internal fun HerdrNotice(machineName: String, starting: Boolean, onStart: () -> Unit, failure: HerdrFailure? = null) {
    Column {
        JNotice(
            text = stringResource(if (starting) R.string.agents_herdr_starting else R.string.agents_herdr_stopped),
            title = machineName,
            status = Status.Unknown,
            actionLabel = if (starting) null else stringResource(R.string.agents_herdr_start),
            onAction = if (starting) null else onStart,
        )
        // "Didn't start: <why>" stays on the row until the next start (iOS HerdrNoticeRow); long press copies the hub's own words.
        if (!starting && failure != null) {
            val copy = rememberCopy()
            var menu by remember { mutableStateOf(false) }
            val copyLabel = stringResource(R.string.agents_herdr_copy_detail)
            Text(stringResource(R.string.agents_herdr_failure, failure.text.resolve()), style = PersonaTheme.type.caption,
                color = PersonaTheme.palette.danger.text, maxLines = 3, overflow = TextOverflow.Ellipsis,
                modifier = Modifier.padding(horizontal = Tokens.Space.l, vertical = Tokens.Space.xs)
                    .combinedClickable(onClick = {}, onLongClick = { menu = true }, onLongClickLabel = copyLabel)
                    .semantics { onLongClick(copyLabel) { menu = true; true } })
            if (menu) JActionSheet(listOf(JAction(copyLabel, icon = PhIcons.Copy, onClick = {
                copy(failure.detail, null)
            })), { menu = false })
        }
    }
}

/**
 * "Orca isn't running" on a computer whose engine set has Orca and whose node says it is installed and not running
 * (Engines.orcaStopped, #17037). Persona cannot open Orca for the user, so the row says what to do and has no button.
 */
@Composable
internal fun OrcaNotice(machineName: String) {
    JNotice(text = stringResource(R.string.agents_orca_stopped), title = machineName, status = Status.Unknown)
}
