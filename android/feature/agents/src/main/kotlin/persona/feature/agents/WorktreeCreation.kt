package persona.feature.agents

import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Job
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import persona.core.contracts.api.HubCodedFailure
import persona.core.contracts.api.SparsePreset
import persona.core.contracts.api.WorktreeCreateOutcome
import persona.core.contracts.api.WorktreeCreateRequest
import persona.core.contracts.api.WorktreeCreated
import persona.core.contracts.api.WorktreeManagement
import persona.core.contracts.api.WorktreePrepare
import persona.core.contracts.api.WorktreeSource
import persona.core.contracts.api.hubCodedFailure
import persona.core.designsystem.UiText
import persona.core.designsystem.failureReasonText
import java.util.UUID

/** Where a create stands (the web's `CreationIntent.phase`). */
internal enum class CreationPhase { Sending, Pending, Unknown, Refused, Created }

/** One create: its receipt [id] and the exact [request] it stands for, kept while it is not refused. */
internal data class CreationIntent(
    val id: String,
    val request: WorktreeCreateRequest,
    val phase: CreationPhase,
    val created: WorktreeCreated? = null,
    val error: UiText? = null,
) {
    /** Sent and not refused: the form is locked, and the only next step is checking this very request. */
    val locked: Boolean get() = phase != CreationPhase.Refused
}

/** The repository read (`worktree/prepare`): reading, read, or failed (and whether the basic form may still work). */
internal sealed interface WorktreeRead {
    data object Reading : WorktreeRead
    data class Ready(val info: WorktreePrepare) : WorktreeRead
    data class Failed(val text: UiText, val basicAvailable: Boolean) : WorktreeRead
}

internal data class WorktreeSearch(val query: String = "", val searching: Boolean = false,
                                   val items: List<WorktreeSource> = emptyList(), val message: UiText? = null)

internal data class CreationState(
    val draft: WorktreeDraft = WorktreeDraft(),
    val read: WorktreeRead = WorktreeRead.Reading,
    val intent: CreationIntent? = null,
    val error: UiText? = null,
    val search: WorktreeSearch = WorktreeSearch(),
    val savingPreset: Boolean = false,
) {
    val info: WorktreePrepare? get() = (read as? WorktreeRead.Ready)?.info
    val locked: Boolean get() = intent?.locked == true
    val sending: Boolean get() = intent?.phase == CreationPhase.Sending
}

/**
 * New worktree for one source worktree (the web's `WorktreeCreationSession`, #17054). The session outlives its sheet (it
 * is kept by [WorktreeActionsRegistry]): closed and opened again, it shows the same draft and the same create. An
 * unchanged intent keeps one receipt id; a pending or uncertain one is checked through its receipt, or replayed with the
 * same id and body (the hub answers the first outcome), never sent again under a new id. Only a refusal, which made
 * nothing, frees the form for a new create.
 */
internal class WorktreeCreation(
    val target: WorktreeTarget,
    private val api: WorktreeManagement,
    private val scope: CoroutineScope,
    var created: (WorktreeCreated) -> Unit = {},
    private val newId: () -> String = { UUID.randomUUID().toString() },
) {
    private val _state = MutableStateFlow(CreationState())
    val state: StateFlow<CreationState> = _state.asStateFlow()
    private var reading: Job? = null
    private var sending: Job? = null
    private var searching: Job? = null

    fun update(transform: (WorktreeDraft) -> WorktreeDraft) {
        if (_state.value.locked) return
        _state.update { it.copy(draft = transform(it.draft), error = null) }
    }

    /** Reads the repository (with the composer, unless the basic form was chosen); again with [force]. */
    fun load(force: Boolean = false) {
        if (!force && (_state.value.read is WorktreeRead.Ready || reading?.isActive == true)) return
        reading?.cancel()
        val basic = _state.value.draft.basic
        _state.update { it.copy(read = WorktreeRead.Reading) }
        reading = scope.launch {
            val read = try {
                WorktreeRead.Ready(api.prepareWorktree(target.machineId, target.workspaceId, target.kind, composer = !basic))
            } catch (cancelled: CancellationException) { throw cancelled }
            catch (error: Exception) {
                val failure = hubCodedFailure(error)
                val status = failure?.status
                // A computer whose Orca or node cannot run the composer (it answered, and not about access) may still
                // make a basic worktree; the basic read says so if it cannot either.
                WorktreeRead.Failed(
                    if (failure.isOutdated()) UiText.Res(R.string.agents_worktree_needs_update)
                    else UiText.Res(R.string.agents_worktree_failed_with, listOf(UiText.Res(R.string.agents_worktree_read_failed), failureReasonText(error))),
                    basicAvailable = !basic && status != null && status != 401 && status != 403,
                )
            }
            if (_state.value.draft.basic == basic) _state.update { it.copy(read = read) }
        }
    }

    /** The basic branch-and-path form, after the composer could not be read. */
    fun useBasic() {
        if (_state.value.locked) return
        _state.update { it.copy(draft = it.draft.copy(basic = true), error = null) }
        load(force = true)
    }

    fun submit() {
        if (sending?.isActive == true) return
        val now = _state.value
        var intent = now.intent
        if (intent?.phase == CreationPhase.Created) return
        val retry = intent != null && intent.phase != CreationPhase.Refused
        if (!retry) {
            val info = now.info ?: run { _state.update { it.copy(error = UiText.Res(R.string.agents_worktree_not_ready)) }; return }
            when (val body = WorktreeForms.body(target, now.draft, info)) {
                is WorktreeBody.Problem -> { _state.update { it.copy(error = body.text) }; return }
                // A refusal made nothing: the next create, changed or not, is a new one.
                is WorktreeBody.Ready -> intent = CreationIntent(newId(), body.request, CreationPhase.Sending)
            }
        }
        val current = requireNotNull(intent).copy(phase = CreationPhase.Sending, error = null)
        _state.update { it.copy(intent = current, error = null) }
        sending = scope.launch { deliver(current, retry) }
    }

    private suspend fun deliver(intent: CreationIntent, retry: Boolean) {
        val next = try {
            if (retry) {
                val state = receipt(intent.id)
                if (state == "unknown" || state == "failed") {
                    publish(intent.copy(phase = if (state == "failed") CreationPhase.Refused else CreationPhase.Unknown))
                    return
                }
            }
            when (val outcome = api.createWorktree(target.machineId, intent.request, intent.id)) {
                is WorktreeCreateOutcome.Created -> intent.copy(phase = CreationPhase.Created, created = outcome.worktree)
                WorktreeCreateOutcome.Pending -> intent.copy(phase = CreationPhase.Pending)
                WorktreeCreateOutcome.Unknown -> intent.copy(phase = CreationPhase.Unknown)
            }
        } catch (cancelled: CancellationException) {
            publish(intent.copy(phase = CreationPhase.Unknown))
            throw cancelled
        } catch (error: Exception) {
            val state = receipt(intent.id)
            // Only a failed receipt, or no receipt and a hub that says nothing reached the computer, proves nothing was made.
            val refused = state == "failed" || (state.isEmpty() && hubCodedFailure(error)?.delivered == "none")
            intent.copy(phase = when {
                refused -> CreationPhase.Refused
                state == "pending" -> CreationPhase.Pending
                else -> CreationPhase.Unknown
            }, error = failureReasonText(error))
        }
        publish(next)
        next.created?.let(created)
    }

    /** The receipt's state; "" when it cannot be read, which never proves anything. */
    private suspend fun receipt(id: String): String = try { api.worktreeReceipt(target.machineId, id) }
        catch (cancelled: CancellationException) { throw cancelled }
        catch (_: Exception) { "" }

    private fun publish(intent: CreationIntent) = _state.update { it.copy(intent = intent) }

    /** After a create: the same source, ready for another. */
    fun fresh() {
        if (_state.value.intent?.phase != CreationPhase.Created) return
        _state.update { it.copy(intent = null, error = null) }
    }

    fun query(text: String) {
        searching?.cancel()
        _state.update { it.copy(search = WorktreeSearch(query = text)) }
    }

    /** GitHub issues and pull requests matching the query, on a computer whose composer has them. */
    fun search() {
        val now = _state.value
        if (now.locked || now.info?.composer?.has("source.github") != true) return
        searching?.cancel()
        val query = now.search.query
        _state.update { it.copy(search = it.search.copy(searching = true, message = null)) }
        searching = scope.launch {
            val search = try {
                val found = api.worktreeSources(target.machineId, target.workspaceId, target.kind, query)
                WorktreeSearch(query, items = found.items, message = found.message?.let { UiText.Plain(it) })
            } catch (cancelled: CancellationException) { throw cancelled }
            catch (error: Exception) { WorktreeSearch(query, message = failureReasonText(error)) }
            if (_state.value.search.query == query) _state.update { it.copy(search = search) }
        }
    }

    fun pickSource(source: WorktreeSource?) {
        searching?.cancel()
        update { it.copy(provider = source) }
        _state.update { it.copy(search = it.search.copy(searching = false)) }
    }

    /** Saves a sparse preset and picks it, keeping every other choice of the form. */
    fun savePreset(name: String, directories: String, saved: () -> Unit = {}) {
        val now = _state.value
        if (now.locked || now.savingPreset || name.isBlank() || directories.isBlank() || now.info?.composer?.has("sparse_presets") != true) return
        _state.update { it.copy(savingPreset = true, error = null) }
        scope.launch {
            try {
                val preset = api.saveWorktreePreset(target.machineId, target.workspaceId, target.kind, name.trim(), directories)
                _state.update { state ->
                    val info = state.info
                    val composer = info?.composer
                    val read = if (info != null && composer != null) WorktreeRead.Ready(info.copy(composer = composer.copy(
                        presets = composer.presets.filter { it.id != preset.id } + preset))) else state.read
                    state.copy(read = read, savingPreset = false, draft = if (state.locked) state.draft else state.draft.copy(preset = preset.id))
                }
                saved()
            } catch (cancelled: CancellationException) { throw cancelled }
            catch (error: Exception) { _state.update { it.copy(savingPreset = false, error = failureReasonText(error)) } }
        }
    }

    companion object {
        /** A preset's name, else its folders. */
        fun presetLabel(preset: SparsePreset) = preset.name?.takeIf(String::isNotBlank) ?: preset.directories.joinToString(", ")
    }
}

/** An older hub or computer that has no such call: updating is the remedy, not trying again. */
internal fun HubCodedFailure?.isOutdated(): Boolean {
    if (this == null) return false
    return status == 404 || status == 405 || status == 501 || code == "unknown_method"
}
