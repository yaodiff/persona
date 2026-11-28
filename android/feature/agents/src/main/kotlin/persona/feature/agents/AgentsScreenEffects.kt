package persona.feature.agents

import persona.core.contracts.clipped
import persona.core.designsystem.dense
import persona.core.designsystem.text
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.LazyListScope
import androidx.compose.foundation.lazy.LazyListState
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.lazy.rememberLazyListState
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.flow.map
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.withTimeoutOrNull
import kotlinx.coroutines.launch
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.input.nestedscroll.nestedScroll
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.onClick
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.unit.dp
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.compose.LocalLifecycleOwner
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.lifecycle.repeatOnLifecycle
import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcon
import persona.core.designsystem.icons.PhIcons
import persona.core.designsystem.icons.PersonaIcons
import persona.core.domain.sort.canAnswerInteraction
import persona.core.domain.sort.*
import persona.core.domain.answer.firstBySession
import persona.core.domain.answer.rowLine
import persona.core.domain.answer.askingLine
import persona.core.domain.showsStarCard
import persona.core.domain.showsStarPill
import persona.core.navigation.*
import persona.feature.chat.ChatSourceNote
import persona.feature.chat.CodexTakeoverBusyPill
import persona.feature.chat.CodexTakeoverClient
import persona.feature.chat.CodexTakeoverDialogs
import persona.feature.chat.codexTakeoverMenuActions
import persona.feature.chat.rememberCodexTakeoverCoordinator
import kotlinx.coroutines.delay
import java.time.Instant

/**
 * The Agents screen's own effects, in their original order (moved as is, #17579): a created agent opens, a notification's
 * card opens its sheet, the workspace catalog and herdr are watched while the screen is started, the clock ticks, and
 * failures and notices become toasts. ART stops compiling a method past 10,000 code units, and each effect's keys and
 * captured values counted against AgentsScreen.
 */
@Composable
internal fun AgentsScreenEffects(
    model: AgentsViewModel, navigator: Navigator, creationModel: NewAgentViewModel?, creation: NewAgentState?,
    newAgentState: MutableState<Boolean>, historyMachineState: MutableState<String?>,
    historySessionState: MutableState<String?>, cardRequests: CardOpenRequests, everyRequest: List<Interaction>,
    allRequestsState: MutableState<Boolean>, requestDetailState: MutableState<Interaction?>,
    nowState: MutableState<Instant>, machines: List<Machine>, connection: ConnectionState, feedback: AppFeedback?,
    toast: JToastState, archives: AgentArchivesState?, archiveState: AgentArchiveSnapshot, showArchives: Boolean,
    notice: AgentText?,
) {
    var newAgent by newAgentState
    var historyMachine by historyMachineState
    var historySession by historySessionState
    var allRequests by allRequestsState
    var requestDetail by requestDetailState
    var now by nowState
    LaunchedEffect(creation?.created) {
        creation?.created?.let { session ->
            newAgent = false
            historyMachine = null
            historySession = null
            navigator.navigate(ChatRoute(session.machineId, session.sessionId))
            creationModel?.opened()
            model.refresh()
        }
    }
    val cardRequest by cardRequests.requests.collectAsStateWithLifecycle()
    LaunchedEffect(cardRequest, everyRequest) {
        val asked = cardRequest ?: return@LaunchedEffect
        // The list may still be loading on a cold start: wait for the card, then open its sheet over the list.
        val card = everyRequest.firstOrNull { it.id == asked.card.requestId && it.machineId == asked.card.machineId }
            ?: return@LaunchedEffect
        if (cardRequests.take(asked)) { allRequests = false; requestDetail = card }
    }
    val lifecycle = LocalLifecycleOwner.current
    val workspaceService = LocalAgentCreationService.current
    LaunchedEffect(lifecycle, model, workspaceService) {
        lifecycle.lifecycle.repeatOnLifecycle(Lifecycle.State.STARTED) {
            model.watchWorkspaceCatalog(workspaceService)
        }
    }
    LaunchedEffect(lifecycle) {
        lifecycle.lifecycle.repeatOnLifecycle(Lifecycle.State.STARTED) {
            while (true) { now = Instant.now(); delay(1_000) }
        }
    }
    LaunchedEffect(lifecycle, model.herdr, machines, connection) {
        lifecycle.lifecycle.repeatOnLifecycle(Lifecycle.State.STARTED) {
            while (true) { model.herdr?.refresh(); delay(5_000) }
        }
    }
    val failure by model.failure.collectAsStateWithLifecycle()
    val failureResources = agentResources()
    LaunchedEffect(failure) {
        failure?.let {
            if (feedback?.failure(it) != true) toast.show(persona.core.designsystem.FeedbackNotice(persona.core.designsystem.operationFeedback(it).text.resolve(failureResources), style = persona.core.designsystem.FeedbackStyle.Error))
            model.clearFailure()
        }
    }
    LaunchedEffect(archiveState.error, showArchives) {
        if (!showArchives) archiveState.error?.let {
            if (feedback?.failure(it) != true) toast.show(persona.core.designsystem.FeedbackNotice(persona.core.designsystem.operationFeedback(it).text.resolve(failureResources), style = persona.core.designsystem.FeedbackStyle.Error))
            archives?.clearError()
        }
    }
    val noticeResources = agentResources()
    LaunchedEffect(notice) {
        notice?.let { val text = it.resolve(noticeResources); if (feedback?.notice(text) != true) toast.show(persona.core.designsystem.FeedbackNotice(text)); model.clearNotice() }
    }
}
