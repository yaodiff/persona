package persona.core.hub

import persona.core.contracts.ProtocolCompatibility

import persona.core.contracts.CoreText.Key
import persona.core.contracts.CoreText
import persona.core.contracts.JsonCodec
import persona.core.contracts.Session
import persona.core.contracts.api.*
import kotlinx.coroutines.CancellationException
import kotlinx.serialization.encodeToString
import kotlinx.serialization.json.*
import okhttp3.HttpUrl.Companion.toHttpUrl
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import java.util.Base64
import java.util.UUID
import java.util.concurrent.TimeUnit

class HttpAgentCreationService(
    override val scope: String,
    baseUrl: String,
    private val token: String,
    client: OkHttpClient,
    private val isCurrent: () -> Boolean = { true },
) : AgentCreationService {
    private val base = baseUrl.toHttpUrl()
    private val http = client.newBuilder().followRedirects(false).followSslRedirects(false)
        .retryOnConnectionFailure(false).build()
    // Match the start endpoint's timeout budget; actual startup latency has not been measured.
    private val startHttp = http.newBuilder().readTimeout(130, TimeUnit.SECONDS).build()
    // The hub gives the node 70 s to ask the launcher's CLI for its models (it starts it once, then caches the list).
    private val modelsHttp = http.newBuilder().readTimeout(75, TimeUnit.SECONDS).build()
    private fun url(vararg segments: String) = base.newBuilder().apply { segments.forEach(::addPathSegment) }.build()
    private fun checkScope() { if (!isCurrent()) throw CancellationException("Pairing changed") }
    private suspend fun read(request: Request, client: OkHttpClient = http): JsonObject {
        checkScope()
        return client.newCall(request).awaitResponse().use { response ->
            checkScope()
            if (!response.isSuccessful) throw HubFailure.from(response)
            JsonCodec.parseToJsonElement(requireNotNull(response.body).string()).jsonObject
        }
    }
    private fun request(vararg segments: String) = Request.Builder().url(url(*segments)).header("Authorization", "Bearer $token")
    override suspend fun launchers(machineId: String): List<AgentLauncher> {
        val body = read(request("api", "v1", "machines", machineId, "launchers").build())
        return JsonCodec.decodeFromJsonElement(body["launchers"] ?: JsonArray(emptyList()))
    }
    override suspend fun directories(machineId: String, path: String): AgentDirectories = JsonCodec.decodeFromJsonElement(read(
        request("api", "v1", "machines", machineId, "dirs")
            .url(url("api", "v1", "machines", machineId, "dirs").newBuilder().addQueryParameter("path", path).build()).build(),
    ))
    override suspend fun start(request: AgentStartRequest, operationId: String): AgentStartResult {
        UUID.fromString(operationId)
        // An empty launcher follows the hub's "New Agent" model configuration (docs/assistant/model-config.md).
        require(request.machineId.isNotBlank() && request.cwd.isNotBlank())
        val body = compatibleRequestBody("/api/v1/sessions", JsonCodec.encodeToString(request).toRequestBody("application/json".toMediaType()))!!
        // A lost response must be reconciled through operation(), never by posting again.
        val result = try {
            outcome(read(request("api", "v1", "sessions").apply { ProtocolCompatibility.operationHeaders(operationId).forEach { (name, value) -> header(name, value) } }.post(body).build(), startHttp))
        } catch (cancelled: CancellationException) {
            throw cancelled
        } catch (failure: HubFailure) {
            // The manager refused the provider on this computer before anything started (docs/integrations/launchers.md
            // "Device scope and on-demand delivery"): its words, in the request's language, are the answer.
            if (failure.status == 409 && failure.code in ScopeRefusals && failure.message.isNotBlank())
                AgentStartResult.Rejected(CoreText.plain(failure.message)) else AgentStartResult.Unknown
        } catch (_: Exception) {
            AgentStartResult.Unknown
        }
        if (result != AgentStartResult.Unknown) return result
        return try { operation(operationId) }
        catch (cancelled: CancellationException) { throw cancelled }
        catch (_: Exception) { AgentStartResult.Unknown }
    }
    override suspend fun operation(operationId: String): AgentStartResult {
        UUID.fromString(operationId)
        val receipt = try { read(request("api", "v1", "ops", operationId).build()) }
        catch (failure: HubFailure) {
            // Old hubs do not record session starts; a missing receipt cannot prove non-execution.
            if (failure.status == 404) return AgentStartResult.Unknown
            throw failure
        }
        val state = receipt["state"]?.jsonPrimitive?.content
        if (receipt["kind"]?.jsonPrimitive?.content?.let { it != "start" } == true) return AgentStartResult.Unknown
        if (state == "pending") return AgentStartResult.Pending
        if (state == "unknown") return AgentStartResult.Unknown
        val result = receipt["result"] as? JsonObject ?: return AgentStartResult.Unknown
        val encoded = result["body"]?.jsonPrimitive?.content ?: return AgentStartResult.Unknown
        val body = JsonCodec.parseToJsonElement(String(Base64.getDecoder().decode(encoded), Charsets.UTF_8)).jsonObject
        return when (state) {
            "done" -> outcome(body)
            "failed" -> AgentStartResult.Rejected(body["error"]?.jsonPrimitive?.content?.let(CoreText::plain) ?: CoreText.of(Key.StartRejected))
            else -> AgentStartResult.Unknown
        }
    }
    override suspend fun newAgentModel(): ModelPurposeDetail? = try {
        JsonCodec.decodeFromJsonElement<ModelPurposeDetail>(read(request("api", "v1", "model-config", "purposes", "agent.new").build()))
    } catch (failure: HubFailure) {
        // Hubs without unified model configuration: the launcher is picked here as before.
        if (failure.status == 404) null else throw failure
    }
    override suspend fun launcherModels(machineId: String, launcher: String, refresh: Boolean): LauncherModels? = try {
        val url = url("api", "v1", "machines", machineId, "launchers", launcher, "models").newBuilder()
            .apply { if (refresh) addQueryParameter("refresh", "1") }.build()
        JsonCodec.decodeFromJsonElement<LauncherModels>(read(request().url(url).build(), modelsHttp))
    } catch (failure: HubFailure) {
        // Hubs without the list: no model picker, the launcher's own model as before.
        if (failure.status == 404) null else throw failure
    }
    override suspend fun worktreeStatus(machineId: String, cwd: String, kind: String?): WorktreeStatus? = try {
        JsonCodec.decodeFromJsonElement<WorktreeStatus>(worktree(machineId, "status", buildJsonObject {
            put("cwd", cwd)
            kind?.let { put("kind", it) }
        }))
    } catch (cancelled: CancellationException) { throw cancelled }
    catch (_: Exception) { null } // an older node or hub, offline, an error: no worktree offered

    override suspend fun addWorktree(machineId: String, cwd: String, nameHint: String, branch: String, kind: String?): WorktreeAdded =
        JsonCodec.decodeFromJsonElement(worktree(machineId, "add", buildJsonObject {
            put("cwd", cwd)
            if (nameHint.isNotEmpty()) put("name_hint", nameHint)
            branch.trim().takeIf(String::isNotEmpty)?.let { put("branch", it) }
            kind?.let { put("kind", it) }
        }))

    override suspend fun removeWorktree(machineId: String, path: String, deleteBranch: Boolean, force: Boolean, kind: String?): WorktreeRemoved =
        JsonCodec.decodeFromJsonElement(worktree(machineId, "remove", buildJsonObject {
            put("path", path)
            if (deleteBranch) put("delete_branch", true)
            if (force) put("force", true)
            kind?.let { put("kind", it) }
        }))

    // The catalog covers every computer of the hub: only [machineId]'s rows. An older hub (404), an offline catalog or
    // any other failure shows no workspace choice, never an error.
    override suspend fun workspaces(machineId: String): List<AgentWorkspace> = try {
        val body = read(request("api", "v1", "workspaces").build())
        // Every computer's workspaces: decoded off the caller's (the form's main) thread.
        kotlinx.coroutines.withContext(kotlinx.coroutines.Dispatchers.Default) {
            JsonCodec.decodeFromJsonElement<List<AgentWorkspace>>(body["workspaces"] ?: JsonArray(emptyList()))
                .filter { it.machineId == machineId }
        }
    } catch (cancelled: CancellationException) { throw cancelled }
    catch (_: Exception) { emptyList() }

    // git can take a while on a big repository: the start's budget, not the list's.
    private suspend fun worktree(machineId: String, action: String, body: JsonObject) = read(
        request("api", "v1", "machines", machineId, "worktree", action).post(body.toString().toRequestBody("application/json".toMediaType())).build(),
        startHttp)

    private fun outcome(body: JsonObject): AgentStartResult {
        val session = body["session"] as? JsonObject
        if (session != null) {
            val decoded = JsonCodec.decodeFromJsonElement<Session>(session)
            if (decoded.machineId.isNotBlank() && decoded.sessionId.isNotBlank()) return AgentStartResult.Created(decoded)
        }
        return if (body["state"]?.jsonPrimitive?.content == "pending") AgentStartResult.Pending else AgentStartResult.Unknown
    }
    private companion object {
        /** A provider outside the computer's scope, or skipped there for its own service: nothing was started. */
        val ScopeRefusals = setOf("launcher_out_of_scope", "launcher_own_service",
            // a start that asked for ultracode where it can't run (#582): nothing started; the words say why
            "node_outdated", "ultracode_unsupported")
    }
}
