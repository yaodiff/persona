# HarmonyOS 7 XiaoYi integration

Persona integrates with HarmonyOS 7 (API 26) and Huawei XiaoYi across four complementary tracks, all talking to the user's paired Persona Hub without introducing a separate backend.

## Architecture overview

| Track | Entry point | Purpose |
| --- | --- | --- |
| On-Device A2A Agent | `harmony/entry/src/main/ets/xiaoyi/agent/PersonaAgentExtensionAbility.ets` + `harmony/entry/src/main/resources/base/profile/agent_config.json` | Native `@kit.AgentFrameworkKit` A2A v1.0 server with streaming dialogue, multimodal drag/share parts, `CancelTask`, and `PerceptionSuggest` chips |
| In-XiaoYi ArkUI Cards | `harmony/entry/src/main/ets/xiaoyi/agent/PersonaAgentUIAbility.ets` + `harmony/entry/src/main/ets/pages/AgentCardPage.ets` | Embedded `AgentUIExtensionAbility` cards (`status`, `approval`, `answer`, `receipt`) with inline two-step confirmation (no sub-windows) |
| Intents Kit & Suggestions | `harmony/entry/src/main/ets/xiaoyi/intents/` + `harmony/entry/src/main/resources/base/profile/insight_intent.json` | Ten `@InsightIntentEntry` executors, API 26 `AppIntentEntity` queries (`PersonaMachineEntity`, `PersonaSessionEntity`), and opt-in `IntentSuggestionsService` |
| API 26 Skills & System Share | `harmony/entry/skills/persona-assistant/` + `harmony/entry/skills/persona-workspace/` + `harmony/entry/src/main/ets/xiaoyi/ShareReceiver.ets` | Declared `skillProfiles` (`SKILL.md` + ArkTS scripts) and `ohos.want.action.sendData` share-to-draft handoff |
| OpenClaw Outbound WS Bridge | `harmony/scripts/xiaoyi-openclaw-bridge.mjs` | Zero-dependency Node 24 outbound WebSocket bridge for immediate personal developer account testing via XiaoYi Open Platform OpenClaw mode |
| Cloud A2A v1.0 & Streamable HTTP MCP Server | `harmony/scripts/xiaoyi-cloud-server.mjs` | Zero-dependency Node 24 HTTPS-proxy-ready A2A v1.0 (`/.well-known/agent-card.json`, `/a2a` JSON-RPC + SSE) and Streamable HTTP MCP (`/mcp`) server backed by Persona Hub |

All HarmonyOS entry points share `harmony/entry/src/main/ets/xiaoyi/HeadlessHubService.ets`, `harmony/entry/src/main/ets/core/XiaoYiActions.ts`, and `harmony/entry/src/main/ets/core/XiaoYiRoutes.ts`. `CredentialVault` and `LocalStore` accept `common.Context` so extensions and intent executors read the paired Hub profile and Asset Store device token directly without launching the full UI.

## Security and privacy boundaries

- **Lock-screen fail-closed approvals**: `ApproveFirstPending` and inline `AgentCardPage` confirmations query `screenLock.isScreenLocked()` and fail closed (`xiaoyi_unlock_required`) whenever the phone is locked or lock status cannot be verified.
- **No silent auto-run outside the app**: Background and card approvals allow only plain tool permissions (`allow: true`) or edit-mode plans (`mode: "acceptEdits"`). `bypassPermissions`, secret-field prompts, questions, and terminal screens always defer to the foreground Persona app.
- **Fail-closed recording-module guard**: When the paired Hub publishes the `modules` feature marker without `module.recording`, `StartPersonaRecordingIntent`, `persona://open?dest=recording`, `XiaoYiA2AEngine` (including omitting the `start_recording` suggestion chip), `AssistantSkill.startRecording()`, the in-app recording screens (`KnowledgeWorkspacePage` / `KnowledgeRecordingView`), and the OpenClaw bridge refuse to start a recording with `"Meeting recording is off on this hub."` while keeping existing recordings readable and stoppable.
- **Live opt-in XiaoYi Suggestions**: When enabled in XiaoYi Settings, `ClientModel` publishes debounced pending/working counts to `IntentSuggestionsService.scheduleStatusShare` so XiaoYi Suggestions stay current without leaking private titles or message bodies.
- **No cloud relay approvals**: `harmony/scripts/xiaoyi-openclaw-bridge.mjs` explicitly refuses approval mutations over the cloud OpenClaw WebSocket and directs the user to confirm on their unlocked phone.
- **Tenant isolation on Forget Hub**: Per-Hub XiaoYi state (`xiaoyi:` prefix in `harmony/entry/src/main/ets/core/StorageOwnership.ts`, including A2A context bindings, push outbox, and remembered Hub module flags) is wiped automatically by `LocalStore.purgeHub(hub)`, and `ClientModel.forgetProfile()` clears shared XiaoYi Suggestions.
- **SSH-tunnel detection**: Hubs configured with SSH-tunnel-only access (`routePolicy` `legacy` or `always`) fail cleanly in background extensions (`xiaoyi_ssh_open_app`) instead of hanging, while `fallback` Hubs continue using direct HTTPS whenever reachable.
- **No secrets in repository**: Tracked manifests keep the example bundle ID `com.example.persona.harmony` and placeholder `persona_harmony_agent`. Real Access Keys, Secret Keys, Agent IDs, and Hub tokens live exclusively in environment variables or local git-ignored config files.

## Immediate testing with a personal Huawei developer account (OpenClaw mode)

Once you have signed the XiaoYi Open Platform agreement and verified your developer account:

1. In the XiaoYi Open Platform console, create an **OpenClaw** agent, copy its `AK`, `SK`, and `Agent ID`, and add your own Huawei ID under **Whitelist Management**.
2. Export your local credentials (never commit them), run the 60-second preflight check (`--check --check-ws`), and start the zero-dependency Node 24 bridge on a machine that can reach your Persona Hub:

   ```sh
   export XIAOYI_AK="<your-ak>"
   export XIAOYI_SK="<your-sk>"
   export XIAOYI_AGENT_ID="<your-agent-id>"
   export PERSONA_HUB_URL="https://<hub-address>"
   export PERSONA_HUB_TOKEN="<your-persona-token>"
   node harmony/scripts/xiaoyi-openclaw-bridge.mjs --check --check-ws
   node harmony/scripts/xiaoyi-openclaw-bridge.mjs
   ```

   Alternatively, place those fields (`ak`, `sk`, `agentId`, `hubUrl`, `hubToken`, optional `manager` and `wsUrl`) in a `0600` JSON file outside the repository and run `node harmony/scripts/xiaoyi-openclaw-bridge.mjs --config /path/to/xiaoyi-openclaw.json --check --check-ws`. The bridge signs WebSocket upgrades with Huawei OpenClaw headers (`x-access-key`, `x-ts`, `x-sign`, `x-agent-id`, `x-hag-trace-id`), sends `clawd_bot_init` and periodic `heartbeat` frames, and wraps streaming replies in `agent_response` envelopes.
3. Open XiaoYi on your HarmonyOS phone (signed into the whitelisted Huawei ID) and invoke your test agent with everyday prompts such as `"Check status"`, `"Add a to-do: review weekly notes"`, `"Log water 350 ml"`, `"Send to agent: run unit tests"`, `"Search knowledge: release"`, or any Persona question.

## Cloud A2A v1.0 & Streamable HTTP MCP server

For XiaoYi Cloud Agent (A2A v1.0) and Cloud MCP (Streamable HTTP) integrations behind a TLS reverse proxy, run `harmony/scripts/xiaoyi-cloud-server.mjs` (zero npm dependencies, Node 24 built-ins only):

1. Configure credentials via environment variables or a `0600` JSON file outside the repository (`--config /path/to/cloud-config.json` or `XIAOYI_CONFIG_FILE`):
   - Required: `XIAOYI_CLOUD_TOKEN` / `cloudToken` (minimum 24 characters), `PERSONA_HUB_URL` / `hubUrl`, `PERSONA_HUB_TOKEN` / `hubToken`.
   - Optional: `XIAOYI_CLOUD_HOST` / `host` (default `127.0.0.1`), `XIAOYI_CLOUD_PORT` / `port` (default `18790`), `XIAOYI_PUBLIC_BASE_URL` / `publicBaseUrl` (no trailing slash, advertised in the AgentCard `url` and `interfaces`), `PERSONA_MANAGER` / `manager`, `XIAOYI_ASK_TIMEOUT_MS` / `askTimeoutMs` (default `24000`).
2. Run preflight validation (`--check`) and start the server:

   ```sh
   node harmony/scripts/xiaoyi-cloud-server.mjs --check --config /path/to/cloud-config.json
   node harmony/scripts/xiaoyi-cloud-server.mjs --config /path/to/cloud-config.json
   ```

3. Exposed endpoints:
   - `GET /healthz`: unauthenticated liveness probe (`{"ok":true,"service":"persona-xiaoyi-cloud"}`).
   - `GET /.well-known/agent-card.json`: A2A v1.0 AgentCard with Bearer security scheme and 6 skills (`ask_persona`, `check_status`, `send_agent_task`, `add_todo`, `log_water`, `search_knowledge`).
   - `POST /a2a` (Bearer auth required): A2A JSON-RPC 2.0 (`message/send`, `SendMessage`, `tasks/send`, SSE `message/stream` / `SendStreamingMessage` / `tasks/sendSubscribe`, `tasks/get`, `tasks/cancel`).
   - `POST /mcp` (Bearer auth required): Streamable HTTP MCP JSON-RPC 2.0 (`initialize` protocol `2025-03-26`, `notifications/initialized` returning HTTP `202`, `ping`, `tools/list`, `tools/call` across all 6 tools).
4. Security boundaries:
   - Fail-closed Bearer verification requires `>= 24` characters and uses constant-time SHA-256 `crypto.timingSafeEqual`; unauthorized requests receive HTTP `401` with `WWW-Authenticate: Bearer`.
   - Request bodies are capped at `1 MiB` (`413 Payload Too Large`), and neither `cloudToken` nor `hubToken` is ever logged or printed in `--check` output.
   - Cloud approval requests always refuse (`approval_requires_phone`, A2A state `input-required` / MCP `isError: true`) so approvals stay on the user's unlocked phone.
   - Recording requests enforce the `#15985` Hub `module.recording` guard and fail closed with `"Meeting recording is off on this hub."` when recording is disabled.
5. **XiaoYi Console Skill draft import**: In the XiaoYi Open Platform console, import the `SKILL.md` drafts from `harmony/entry/skills/persona-assistant/` and `harmony/entry/skills/persona-workspace/` alongside your `https://<public-host>/a2a` or `https://<public-host>/mcp` endpoint and Bearer credential.

## HarmonyOS developer publishing checklist (On-Device A2A, Intents Kit, Skills)

1. Replace the development bundle ID (`com.example.persona.harmony`) and `agentId` (`persona_harmony_agent`) through your private signing/release configuration before AppGallery Connect submission.
2. Register the 10 intents from `harmony/entry/src/main/resources/base/profile/insight_intent.json` and the on-device AgentCard from `harmony/entry/src/main/resources/base/profile/agent_config.json` in Xiaoyi Open Platform (**Intents Framework & MCP** and **On-Device Agent**).
3. Verify the in-app diagnostics screen at **Settings › Advanced › XiaoYi** (`persona://open?dest=settings_xiaoyi`): confirm paired Hub, Asset Store credential readability, direct HTTPS reachability, and `skillManager.getSkillInfosForSelf()` reporting both `persona-assistant` and `persona-workspace`.

## Verification

Run the portable HarmonyOS suites and HAP build from `harmony/`:

```sh
node --test tests/xiaoyi-*.test.mjs tests/storage-ownership.test.mjs tests/ability-lifecycle.test.mjs tests/phone-settings-navigation.test.mjs
node tests/run-domain.mjs
hvigorw assembleHap --no-daemon
hvigorw --mode module -p module=entry@ohosTest assembleHap --no-daemon
```
