package persona.core.hub

import kotlinx.coroutines.CancellationException
import kotlinx.serialization.json.*
import okhttp3.HttpUrl
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import persona.core.contracts.JsonCodec
import persona.core.contracts.ProtocolCompatibility
import persona.core.contracts.api.*
import java.util.UUID
import java.util.concurrent.TimeUnit

/**
 * The hub's worktree actions (#17054, [WorktreeManagement]): the same routes and bodies as the web's `lib/worktree.ts` and
 * `lib/worktree-actions-state.ts`. The hub gives create and delete 150 s on the node, prepare and rename 30 s; each call
 * waits a little longer than that before it calls the answer lost.
 */
internal class HttpWorktreeManagement(
    private val base: HttpUrl,
    private val token: String,
    client: OkHttpClient,
    private val isCurrent: () -> Boolean,
) : WorktreeManagement {
    private val http = client.newBuilder().followRedirects(false).followSslRedirects(false)
        .retryOnConnectionFailure(false).readTimeout(45, TimeUnit.SECONDS).build()
    private val slow = http.newBuilder().readTimeout(165, TimeUnit.SECONDS).build()

    private fun url(vararg segments: String) = base.newBuilder().apply { segments.forEach(::addPathSegment) }.build()
    private fun checkScope() { if (!isCurrent()) throw CancellationException("Pairing changed") }
    private suspend fun read(request: Request, client: OkHttpClient = http): JsonObject {
        checkScope()
        return client.newCall(request).awaitResponse().use { response ->
            checkScope()
            if (!response.isSuccessful) throw HubFailure.from(response)
            val text = requireNotNull(response.body).string()
            if (text.isBlank()) JsonObject(emptyMap()) else JsonCodec.parseToJsonElement(text) as? JsonObject
                ?: throw HubFailure(HubError.Protocol)
        }
    }
    private fun get(vararg segments: String) = Request.Builder().url(url(*segments)).header("Authorization", "Bearer $token").build()
    private suspend fun action(machineId: String, action: String, body: JsonObject, client: OkHttpClient = http,
                               operationId: String? = null): JsonObject {
        val request = Request.Builder().url(url("api", "v1", "machines", machineId, "worktree", action))
            .header("Authorization", "Bearer $token")
            .apply { operationId?.let { id -> ProtocolCompatibility.operationHeaders(id).forEach { (name, value) -> header(name, value) } } }
            .post(body.toString().toRequestBody("application/json".toMediaType())).build()
        return read(request, client)
    }

    override suspend fun fullAccess(machineId: String): Boolean = try {
        // A full-device-only route on every hub; no role is guessed from the token itself.
        val body = read(get("api", "v1", "auth", "tokens"))
        val tokens = body["tokens"] as? JsonArray
        val current = (body["current"] as? JsonPrimitive)?.takeIf { it.isString }?.content
        tokens != null && current != null && (current.isEmpty() || tokens.any { token ->
            val entry = token as? JsonObject ?: return@any false
            entry["id"]?.jsonPrimitive?.contentOrNull == current && entry["role"]?.jsonPrimitive?.contentOrNull == "full"
        })
    } catch (cancelled: CancellationException) { throw cancelled }
    catch (_: Exception) { false }

    override suspend fun prepareWorktree(machineId: String, workspaceId: String, kind: String, composer: Boolean): WorktreePrepare =
        JsonCodec.decodeFromJsonElement(action(machineId, "prepare", buildJsonObject {
            put("workspace_id", workspaceId)
            put("kind", kind)
            // Only Orca reads the composer probe; an older node ignores it and answers the basic form.
            if (kind == "orca") put("composer", composer)
        }))

    override suspend fun createWorktree(machineId: String, request: WorktreeCreateRequest, operationId: String): WorktreeCreateOutcome {
        UUID.fromString(operationId)
        val body = action(machineId, "create", createBody(request), slow, operationId)
        val created = runCatching { JsonCodec.decodeFromJsonElement<WorktreeCreated>(body) }.getOrNull()
        return when {
            created != null && created.workspaceId.isNotBlank() && created.path.isNotBlank() && "branch" in body ->
                WorktreeCreateOutcome.Created(created)
            body["state"]?.jsonPrimitive?.contentOrNull == "pending" -> WorktreeCreateOutcome.Pending
            else -> WorktreeCreateOutcome.Unknown
        }
    }

    override suspend fun worktreeReceipt(machineId: String, operationId: String): String {
        UUID.fromString(operationId)
        return read(get("api", "v1", "ops", operationId))["state"]?.jsonPrimitive?.contentOrNull.orEmpty()
    }

    override suspend fun worktreeSources(machineId: String, workspaceId: String, kind: String, query: String): WorktreeSources {
        val body = action(machineId, "source", buildJsonObject {
            put("workspace_id", workspaceId)
            put("kind", kind)
            put("query", query)
        })
        val items = JsonCodec.decodeFromJsonElement<List<WorktreeSource>>(body["items"] ?: JsonArray(emptyList()))
        // errors is a list of words or an object of {message} by provider; warning one line.
        val errors = when (val value = body["errors"]) {
            is JsonArray -> value.mapNotNull { (it as? JsonPrimitive)?.contentOrNull }
            is JsonObject -> value.values.mapNotNull { (it as? JsonObject)?.get("message")?.jsonPrimitive?.contentOrNull }
            else -> emptyList()
        }
        val message = (listOfNotNull(body["warning"]?.jsonPrimitive?.contentOrNull) + errors).filter(String::isNotBlank)
            .joinToString(" · ").ifEmpty { null }
        return WorktreeSources(items, message)
    }

    override suspend fun saveWorktreePreset(machineId: String, workspaceId: String, kind: String, name: String, directories: String): SparsePreset {
        val body = action(machineId, "preset", buildJsonObject {
            put("workspace_id", workspaceId)
            put("kind", kind)
            put("name", name)
            put("directories", directories)
        })
        return JsonCodec.decodeFromJsonElement<SparsePreset>(body["preset"] ?: throw HubFailure(HubError.Protocol))
            .takeIf { it.id.isNotBlank() } ?: throw HubFailure(HubError.Protocol)
    }

    override suspend fun renameWorktree(machineId: String, workspaceId: String, kind: String, name: String): String =
        action(machineId, "rename", buildJsonObject {
            put("workspace_id", workspaceId)
            put("kind", kind)
            put("name", name)
        })["state"]?.jsonPrimitive?.contentOrNull.orEmpty()

    override suspend fun previewWorktreeDeletion(machineId: String, workspaceId: String, kind: String): WorktreeDeletionPreview =
        JsonCodec.decodeFromJsonElement(action(machineId, "delete", buildJsonObject {
            put("workspace_id", workspaceId)
            put("kind", kind)
            put("preview", true)
        }))

    override suspend fun deleteWorktree(machineId: String, deletion: WorktreeDeletion): WorktreeDeletionResult =
        JsonCodec.decodeFromJsonElement(action(machineId, "delete", buildJsonObject {
            put("workspace_id", deletion.workspaceId)
            put("kind", deletion.kind)
            put("preview_token", deletion.previewToken)
            if (deletion.reconcile) put("reconcile", true)
            if (deletion.force) put("force", true)
            if (deletion.deleteBranch) put("delete_branch", true)
        }, slow))

    internal companion object {
        /** The fields a request sets, as the web's `creationBody` writes them: nothing empty or false is sent. */
        fun createBody(request: WorktreeCreateRequest) = buildJsonObject {
            put("workspace_id", request.workspaceId)
            request.kind?.takeIf(String::isNotEmpty)?.let { put("kind", it) }
            if (request.composer) put("composer", true)
            fun text(key: String, value: String?) { value?.takeIf(String::isNotEmpty)?.let { put(key, it) } }
            text("name", request.name)
            text("branch_override", request.branchOverride)
            text("base_ref", request.baseRef)
            text("parent_workspace_id", request.parentWorkspaceId)
            if (request.noParent) put("no_parent", true)
            text("note", request.note)
            text("sparse_preset_id", request.sparsePresetId)
            text("source_id", request.sourceId)
            text("source_url", request.sourceUrl)
            if (request.autoName) put("auto_name", true)
            text("branch", request.branch)
            text("path", request.path)
        }
    }
}
