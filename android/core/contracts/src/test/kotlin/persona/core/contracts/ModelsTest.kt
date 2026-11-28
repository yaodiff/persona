package persona.core.contracts

import kotlinx.serialization.encodeToString
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.api.AgentLauncher
import persona.core.contracts.api.HubLauncher
import persona.core.contracts.api.UnavailableLauncher

class ModelsTest {
    @Test fun agentTaskResultsDecodeRunningAndKeepOriginalReportIdentity() {
        for (status in listOf("running", "waiting", "completed", "failed", "stopped")) {
            val item = JsonCodec.decodeFromString<Item>("""{"id":"report#0","seq":12,"role":"user","kind":"tool_result",
                "tool_use_id":"toolu_launch","text":"Full report","full_size":8192,
                "task":{"kind":"agent","status":"$status","task_id":"agent-a","tool_use_id":"toolu_launch","detail":"Preview"}}""")
            assertEquals(status, item.task?.status)
            assertEquals("toolu_launch", item.toolUseId)
            assertEquals("Full report", item.text)
            assertEquals(8192, item.fullSize)
            assertEquals(item, JsonCodec.decodeFromString<Item>(JsonCodec.encodeToString(item)))
        }
        assertNull(JsonCodec.decodeFromString<Item>("""{"id":"legacy","kind":"tool_result","text":"general-purpose says hello"}""").task)
    }

    @Test fun sessionDecodesWithMissingFieldsAndUnknownState() {
        val s = JsonCodec.decodeFromString<Session>(
            """{"machine_id":"server","session_id":"u1","cwd":"/home/x/code/foo","status":"done","state":"later","extra":1}""",
        )
        assertEquals("foo", (s.displayTitle).zhAny())
        // D5 preserves unknown states instead of inferring a known state from status.
        assertEquals(AgentState.Unknown, s.agentState)
        assertEquals(0, s.pendingInteractions)
    }

    @Test fun sessionDecodesUndeliveredMessagesAndAnAbsentListIsEmpty() {
        val session = JsonCodec.decodeFromString<Session>(
            """{"session_id":"s","assistant":true,"undelivered":[""" +
                """{"id":"kept-waiting","text":"hello","images":2,"at":"2026-11-17T10:00:00Z","state":"waiting",""" +
                """"attempts":2,"next_at":"2026-11-17T10:01:00Z","error":"The assistant could not start"},""" +
                """{"id":"kept-failed","text":"later","at":"2026-11-17T10:02:00Z","state":"failed"}]}""",
        )
        assertEquals(listOf("kept-waiting", "kept-failed"), session.undelivered.orEmpty().map { it.id })
        val waiting = session.undelivered.orEmpty().first()
        assertEquals("hello", waiting.text)
        assertEquals(2, waiting.images)
        assertEquals("2026-11-17T10:00:00Z", waiting.at)
        assertEquals("waiting", waiting.state)
        assertEquals(2, waiting.attempts)
        assertEquals("2026-11-17T10:01:00Z", waiting.nextAt)
        assertEquals("The assistant could not start", waiting.error)
        assertEquals("failed", session.undelivered.orEmpty().last().state)
        assertTrue(JsonCodec.decodeFromString<Session>("""{"session_id":"s"}""").undelivered.isNullOrEmpty())
    }

    @Test fun suggestionRiskDecodesAndAnAbsentFieldIsSafeByDefault() {
        for ((field, expected) in mapOf("\"suggestion_risky\":true" to true,
            "\"suggestion_risky\":false" to false, "" to false)) {
            val comma = if (field.isEmpty()) "" else ","
            val session = JsonCodec.decodeFromString<Session>(
                """{"session_id":"s","suggestion":"Review the plan"$comma$field}""",
            )
            assertEquals(field.ifEmpty { "absent" }, expected, session.suggestionRisky)
            assertEquals("Review the plan", session.suggestion)
        }
    }

    @Test fun zeroTimeIsUnset() {
        assertNull(parseTime("0001-01-01T00:00:00Z"))
        assertTrue(parseTime("2026-10-29T15:05:32.42Z") != null)
    }

    @Test fun offsetTimesParse() {
        // A hub in a local zone sends +08:00; the instant must match the same moment written with Z.
        assertEquals(java.time.Instant.parse("2026-11-02T00:30:00Z"), parseTime("2026-11-02T08:30:00+08:00"))
        assertEquals(java.time.Instant.parse("2026-11-02T00:30:00.5Z"), parseTime("2026-11-02T08:30:00.5+08:00"))
        assertNull(parseTime("0001-01-01T08:05:43+08:05"))
        // What a node sends (nanoseconds and the machine's own offset), and Z with nanoseconds: both one parse (#932).
        assertEquals(java.time.Instant.parse("2026-10-31T05:58:40.205893083Z"), parseTime("2026-10-31T13:58:40.205893083+08:00"))
        assertEquals(java.time.Instant.parse("2026-10-31T05:58:40.205893083Z"), parseTime("2026-10-31T05:58:40.205893083Z"))
        assertNull(parseTime("not a time"))
        // Fast-path edge cases: negative & sub-hour offsets, leap years, non-leap Feb 29, lowercase t/z, fractional lengths, 2000 boundary.
        assertEquals(java.time.Instant.parse("2024-02-29T23:59:59Z"), parseTime("2024-02-29T23:59:59Z"))
        assertEquals(java.time.Instant.parse("2000-02-29T00:00:00Z"), parseTime("2000-02-29t00:00:00z"))
        assertNull(parseTime("2025-02-29T00:00:00Z"))
        assertNull(parseTime("2100-02-29T00:00:00Z"))
        assertEquals(java.time.Instant.parse("2026-11-02T14:00:00.123456Z"), parseTime("2026-11-02T08:30:00.123456-05:30"))
        assertEquals(java.time.Instant.parse("2000-01-01T00:00:00Z"), parseTime("2000-01-01T08:00:00+08:00"))
        assertNull(parseTime("2000-01-01T07:59:59+08:00"))
        assertNull(parseTime("2026-13-02T08:30:00Z"))
        assertNull(parseTime("2026-11-02T08:30:00+25:00"))
    }

    @Test fun resolutionOmitsUnsetFields() {
        assertEquals("""{"allow":true}""", (JsonCodec.encodeToString(Resolution(allow = true))).zhAny())
        assertEquals("""{"approve":false,"feedback":"改"}""", (JsonCodec.encodeToString(Resolution(approve = false, feedback = "改"))).zhAny())
        assertEquals("""{"option":"2"}""", (JsonCodec.encodeToString(Resolution(option = "2"))).zhAny())
    }

    @Test fun sourceFieldsDecodeAndRoundTrip() {
        val history = JsonCodec.decodeFromString<Session>(
            """{"machine_id":"m","session_id":"opaque","source_kind":"codex_history","source_thread_id":"thread",""" +
                """"source_instance_id":"instance","parent_thread_id":"parent","read_only_reason":"外部进程正在使用此会话",""" +
                """"capabilities":{"read":true,"modes":false,"future":true},"attached":true,"agent_label":"DSH"}""",
        )
        assertEquals("codex_history", (history.sourceKind).zhAny())
        assertEquals("thread", (history.sourceThreadId).zhAny())
        assertEquals("instance", (history.sourceInstanceId).zhAny())
        assertEquals("parent", (history.parentThreadId).zhAny())
        assertEquals("外部进程正在使用此会话", (history.readOnlyReason).zhAny())
        assertEquals(SessionCapabilities(read = true, modes = false), history.capabilities)
        assertTrue(history.attached)
        assertEquals(history, JsonCodec.decodeFromString<Session>(JsonCodec.encodeToString(history)))
        // A malformed capability drops only that field (D5), never the session.
        val malformed = JsonCodec.decodeFromString<Session>("""{"session_id":"s","source_kind":"runtime","capabilities":{"send":"yes","read":true}}""")
        assertEquals(SessionCapabilities(read = true), malformed.capabilities)
        val old = JsonCodec.decodeFromString<Session>("""{"session_id":"s"}""")
        assertNull(old.sourceKind); assertNull(old.capabilities); assertEquals(false, old.attached)
    }

    // #6294: what a runtime's session can do without a terminal, where its node says it; null where an older node doesn't.
    @Test fun optionalCapabilitiesDecodeWhereTheNodeSaysThem() {
        fun caps(json: String) = JsonCodec.decodeFromString<Session>("""{"session_id":"s","source_kind":"runtime","capabilities":$json}""").capabilities!!
        val said = caps("""{"read":true,"close":true,"rename":false,"model":true,"rewind":true,"rewind_code":false}""")
        assertEquals(SessionCapabilities(read = true, close = true, rename = false, model = true, rewind = true, rewindCode = false), said)
        val older = caps("""{"read":true,"send":true}""")
        assertEquals(listOf(null, null, null, null, null), listOf(older.close, older.rename, older.model, older.rewind, older.rewindCode))
        // A malformed one drops only itself (D5): null, the app's own rule, never a refusal.
        assertEquals(SessionCapabilities(read = true, rename = true), caps("""{"read":true,"close":"yes","rename":true,"rewind_code":1}"""))
        // They go back out under their wire names.
        val encoded = JsonCodec.encodeToString(said)
        assertTrue(encoded, encoded.contains(""""rewind_code":false"""))
        assertEquals(said, JsonCodec.decodeFromString<SessionCapabilities>(encoded))
    }

    @Test fun codexTakeoverCapabilityAndDtosDecodeSafelyAndRoundTrip() {
        val withTakeover = JsonCodec.decodeFromString<SessionCapabilities>(
            """{"read":true,"send":false,"takeover":{"running":true,"can_resume":false,"reason":"encrypted_context"}}""",
        )
        assertTrue(withTakeover.read && !withTakeover.send)
        assertEquals(CodexTakeoverCapability(running = true, canResume = false, reason = "encrypted_context"), withTakeover.takeover)
        val roundTripped = JsonCodec.decodeFromString<SessionCapabilities>(JsonCodec.encodeToString(withTakeover))
        assertEquals(withTakeover, roundTripped)

        // Malformed takeover drops only itself without discarding sibling capabilities.
        val malformed = JsonCodec.decodeFromString<SessionCapabilities>("""{"read":true,"takeover":"bad"}""")
        assertEquals(SessionCapabilities(read = true, takeover = null), malformed)

        // Older hubs omit takeover.
        val older = JsonCodec.decodeFromString<SessionCapabilities>("""{"read":true}""")
        assertNull(older.takeover)

        val info = JsonCodec.decodeFromString<CodexTakeoverInfo>(
            """{"running":false,"can_resume":true,"token":"tok-1","cwd":"/work","transcript_path":"/tmp/t","continuation_prompt":"Continue"}""",
        )
        assertEquals(CodexTakeoverInfo(running = false, canResume = true, token = "tok-1", cwd = "/work", transcriptPath = "/tmp/t", continuationPrompt = "Continue"), info)
        assertEquals("""{"token":"tok-1","confirm_stop":true}""", JsonCodec.encodeToString(CodexTakeoverRequest("tok-1", confirmStop = true)))
        val response = JsonCodec.decodeFromString<CodexTakeoverResponse>("""{"session":{"machine_id":"m","session_id":"s","status":"working"}}""")
        assertEquals(SessionKey("m", "s"), response.session.key)
    }

    @Test fun agentLabelNamesEveryAgent() {
        fun label(json: String) = JsonCodec.decodeFromString<Session>("""{"session_id":"s",$json}""").agentLabel
        assertEquals("Claude", label(""""status":"idle""""))
        assertEquals("Claude", label(""""agent":"claude""""))
        assertEquals("Codex", label(""""agent":"codex""""))
        assertEquals("Codex", label(""""source_kind":"codex_app_server""""))
        assertEquals("TRAE CLI", label(""""agent":"traex""""))
        assertEquals("TRAE CLI", label(""""agent":"traecli""""))
        assertEquals("TRAE CLI", label(""""agent":"trae-cli""""))
        assertEquals("DSH", label(""""agent":"dsh","agent_label":"DSH""""))
        assertEquals("pi", label(""""agent":"pi""""))
        val dsh = JsonCodec.decodeFromString<Session>("""{"session_id":"s","agent":"dsh"}""")
        assertTrue(!dsh.isClaude && !dsh.isCodex)
        assertTrue(JsonCodec.decodeFromString<Session>("""{"session_id":"s","agent":""}""").isClaude)
    }

    @Test fun namedTraeCliLaunchersKeepTheirConfigurationIdentifiersAndCustomTitles() {
        val builtIn = AgentLauncher(name = "traex", agentType = "traex")
        for (label in listOf(null, "TRAEX", "TraeCLI")) {
            assertEquals("TRAE CLI", builtIn.copy(displayName = label).title)
            assertEquals("TRAE CLI", UnavailableLauncher(name = "traex", displayName = label, agent = "traex").title)
            assertEquals("TRAE CLI", HubLauncher(name = "traex", displayName = label, agent = "traex").title)
        }
        assertEquals("traex", builtIn.name)
        assertEquals("My CLI", builtIn.copy(displayName = "My CLI").title)
        assertEquals("Claude Code", builtIn.copy(name = "claude", agentType = "claude").title)
        assertEquals("Codex", builtIn.copy(name = "codex", agentType = "codex").title)
        assertEquals("Claude Desktop", AgentLauncher(name = "claude-desktop", agentType = "claude").title)
        assertEquals("Codex App", AgentLauncher(name = "codex-app", agentType = "codex").title)
        assertEquals("Antigravity CLI", AgentLauncher(name = "agy", agentType = "agy").title)
        assertEquals("Custom Claude", HubLauncher(name = "traex", displayName = "Custom Claude").title)
    }

    @Test fun anOlderHubsBuiltinCliHasItsDisplayNameWithoutChangingAgentType() {
        val raw = """{"name":"traex","base_url":"(traex)"}"""
        val launcher = JsonCodec.decodeFromString<AgentLauncher>(raw)
        val settings = JsonCodec.decodeFromString<HubLauncher>(raw)
        assertEquals("traex", launcher.name)
        assertEquals("claude", launcher.agentType)
        assertEquals("TRAE CLI", launcher.title)
        assertEquals("TRAE CLI", settings.title)
        assertEquals("My CLI", launcher.copy(displayName = "My CLI").title)
        assertEquals("custom-cli", launcher.copy(name = "custom-cli", source = "persona").title)
        assertEquals("TRAE CLI", launcher.copy(baseUrl = null).title)
    }

    @Test fun aPaseoProviderIsNamedByItsOwnNameNeverTheEngines() {
        fun label(json: String) = JsonCodec.decodeFromString<Session>("""{"session_id":"paseo-1","agent":"paseo",$json}""").agentLabel
        // Paseo calls every provider but Claude Code and Codex "paseo" (#8143): the row's label, else its launcher.
        assertEquals("My Grok", label(""""agent_label":"My Grok","launcher":"grok""""))
        // The runtime's own name, which a node gives a row its bridge left unlabelled, is the engine's: the launcher instead.
        assertEquals("grok", label(""""agent_label":"Paseo","launcher":"grok""""))
        assertEquals("grok", label(""""agent_label":" ","launcher":"grok""""))
        assertEquals("grok", label(""""launcher":"grok""""))
        for (id in listOf("traex", "traecli", "trae-cli")) {
            assertEquals("TRAE CLI", label(""""agent_label":"TRAEX","launcher":"$id""""))
            assertEquals("TRAE CLI", label(""""launcher":"$id""""))
            assertEquals("My agent", label(""""agent_label":"My agent","launcher":"$id""""))
            assertEquals("TRAE CLI", AgentLauncher(name = id, agentType = "paseo").title)
            assertEquals("My agent", HubLauncher(name = id, agent = "paseo", displayName = "My agent").title)
        }
        // Nothing else to go by: as before.
        assertEquals("Paseo", label(""""agent_label":"Paseo""""))
        assertEquals("paseo", label(""""status":"idle""""))
        // Its providers running Claude Code and Codex are those agents', and another runtime keeps its own name.
        assertEquals("Claude", JsonCodec.decodeFromString<Session>("""{"session_id":"s","agent":"claude","launcher":"my-claude","space_kind":"paseo"}""").agentLabel)
        assertEquals("Codex", JsonCodec.decodeFromString<Session>("""{"session_id":"s","agent":"codex","launcher":"my-codex","space_kind":"paseo"}""").agentLabel)
        assertEquals("DSH", JsonCodec.decodeFromString<Session>("""{"session_id":"s","agent":"dsh","agent_label":"DSH","launcher":"dsh"}""").agentLabel)
        assertEquals("grok", unknownAgentName(PaseoAgent, "paseo", "grok"))
        assertEquals("pi", unknownAgentName("pi", null, "my-pi"))
    }

    @Test fun aPaseoCardNamesItsProviderOrSaysOnlyWhatItWants() {
        // A node that labels the card with the row's label (#8142): the provider's name.
        val named = JsonCodec.decodeFromString<Interaction>("""{"id":"p","kind":"question","agent":"paseo","agent_label":"Grok"}""")
        assertEquals("Grok", named.agentDisplayName)
        assertEquals("TRAE CLI", named.copy(agentLabelHint = "TRAEX").agentDisplayName)
        assertTrue(named.fromOtherAgent)
        assertEquals("Grok 在问你", (named.headline).zhAny())
        // An older node's card names only the engine: no name, never "Paseo" (nor "Claude"), and still another agent's card.
        for (engine in listOf(named.copy(agentLabelHint = "Paseo"), named.copy(agentLabelHint = "paseo"), named.copy(agentLabelHint = null))) {
            assertNull(engine.agentDisplayName)
            assertTrue(engine.fromOtherAgent)
            assertEquals("在问你", (engine.headline).zhAny())
            assertEquals("请求使用 read_file", (engine.copy(kind = "permission", toolName = "read_file").headline).zhAny())
        }
        // Whose card it is: only Claude Code's (or an older hub's, which names none) is Claude's.
        assertEquals(listOf(false, false, false, true, true),
            listOf(null, "", "claude", "codex", "dsh").map { Interaction(id = "c", kind = "question", agent = it).fromOtherAgent })
    }

    @Test fun interactionRetainsRememberedPermissionScope() {
        val bash = JsonCodec.decodeFromString<Interaction>("""{"id":"ix_scope","machine_id":"computer","session_id":"chat","kind":"permission","tool_name":"Bash","always_keys":["tool:Bash"]}""")
        assertEquals(listOf("tool:Bash"), bash.alwaysKeys)
        val legacy = JsonCodec.decodeFromString<Interaction>("""{"id":"ix_legacy","machine_id":"computer","session_id":"chat","kind":"permission","tool_name":"Bash"}""")
        assertEquals(null, legacy.alwaysKeys)
        assertEquals(null, legacy.alwaysHonored)
        val own = JsonCodec.decodeFromString<Interaction>("""{"id":"ix_own","machine_id":"computer","session_id":"chat","kind":"permission","tool_name":"iphone_do","always_honored":false}""")
        assertEquals(false, own.alwaysHonored)
    }

    @Test fun interactionDecodesOptionalPlanThen() {
        val plan = JsonCodec.decodeFromString<Interaction>(
            """{"id":"ix_plan_then","kind":"plan","plan":"1. Ship it","plan_then":"default"}""",
        )
        val encoded = JsonCodec.encodeToString(plan)
        assertTrue(encoded, encoded.contains(""""plan_then":"default""""))
        assertEquals(plan, JsonCodec.decodeFromString<Interaction>(encoded))
    }

    @Test fun interactionDecodesOptionalToolTitles() {
        val titled = JsonCodec.decodeFromString<Interaction>(
            """{"id":"ix_titles","kind":"permission","tool_name":"mcp__persona__report_to_supervisor",""" +
                """"tool_title":"Report to supervisor · Done","tool_name_title":"Report to supervisor"}""",
        )
        assertEquals("Report to supervisor · Done", titled.toolTitle)
        assertEquals("Report to supervisor", titled.toolNameTitle)
        val oldHub = JsonCodec.decodeFromString<Interaction>("""{"id":"ix_old","kind":"permission","tool_name":"Bash"}""")
        assertNull(oldHub.toolTitle)
        assertNull(oldHub.toolNameTitle)
    }

    @Test fun interactionNamesItsAgentAndWhatAlwaysAllows() {
        val codex = JsonCodec.decodeFromString<Interaction>(
            """{"id":"cx","kind":"permission","agent":"codex","reason":"访问网络","always_label":"以后不再问这个命令"}""",
        )
        assertEquals("Codex", (codex.agentDisplayName).zhAny())
        assertEquals("以后不再问这个命令", (codex.alwaysLabel).zhAny())
        assertEquals("访问网络", (codex.reason).zhAny())
        val dsh = JsonCodec.decodeFromString<Interaction>("""{"id":"d","kind":"question","agent":"dsh","agent_label":"DSH"}""")
        assertEquals("DSH", (dsh.agentDisplayName).zhAny())
        assertEquals("DSH 在问你", (dsh.headline).zhAny())
        assertEquals("DSH 请求使用 read_file：读取报表", (dsh.copy(kind = "permission", toolName = "read_file", summary = "读取报表").headline).zhAny())
        assertNull(JsonCodec.decodeFromString<Interaction>("""{"id":"c","kind":"question","agent":"claude"}""").agentDisplayName)
        assertEquals("Claude 在问你", (JsonCodec.decodeFromString<Interaction>("""{"id":"c","kind":"question"}""").headline).zhAny())
    }

    @Test fun questionOptionDecodesDeferActionAndDefaultsForOldHubs() {
        val marked = JsonCodec.decodeFromString<QuestionOption>(
            """{"label":"Leave it","description":"Handle it later","action":"defer"}""",
        )
        assertEquals("Leave it", marked.label)
        assertEquals("Handle it later", marked.description)
        assertEquals("defer", marked.action)
        assertEquals(marked, JsonCodec.decodeFromString<QuestionOption>(JsonCodec.encodeToString(marked)))
        assertNull(JsonCodec.decodeFromString<QuestionOption>("""{"label":"Continue"}""").action)
    }

    @Test fun sessionInfoDecodes() {
        val info = JsonCodec.decodeFromString<SessionInfo>(
            """{"session_id":"s1","cwd":"/x","model":"claude-opus","context_tokens":51234,"output_tokens":9,"turns":3,""" +
                """"compactions":1,"git_branch":"main","started_at":"2026-11-01T10:00:00+08:00","last_activity":"0001-01-01T00:00:00Z","new":1}""",
        )
        assertEquals(51234L, info.contextTokens)
        assertEquals("main", (info.gitBranch).zhAny())
        assertEquals(3, info.turns)
        assertEquals(1, info.compactions)
        assertEquals(java.time.Instant.parse("2026-11-01T02:00:00Z"), parseTime(info.startedAt))
        assertNull(info.lastActivity)
        assertEquals(SessionInfo(), JsonCodec.decodeFromString<SessionInfo>("{}"))
        // A runtime's session says nobody gives its counts (#8261): its zeros stand in for older apps.
        assertTrue(JsonCodec.decodeFromString<SessionInfo>("""{"session_id":"s1","turns":0,"usage_unknown":true}""").usageUnknown)
        assertFalse(info.usageUnknown)
    }

    @Test fun interactionHeadline() {
        val ix = JsonCodec.decodeFromString<Interaction>(
            """{"id":"ix_1","kind":"permission","tool_name":"Bash","summary":"rm -rf build"}""",
        )
        assertEquals("请求使用 Bash：rm -rf build", (ix.headline).zhAny())
    }

    @Test fun interactionHeadlineUsesNonblankToolTitleBeforeTheReadableName() {
        val ix = Interaction(
            "ix_title", kind = "permission", toolName = "mcp__persona__report_to_supervisor",
            toolTitle = "Report to supervisor · Done", summary = "Ready",
        )
        assertEquals("请求使用 Report to supervisor · Done：Ready", (ix.headline).zhAny())
        assertEquals("请求使用 Persona · report to supervisor：Ready", (ix.copy(toolTitle = " ").headline).zhAny())
        assertEquals("请求使用 Persona · report to supervisor：Ready",
            (ix.copy(toolTitle = null, toolNameTitle = "Report to supervisor").headline).zhAny())
        assertEquals("请求使用 工具", (Interaction("ix_generic", kind = "permission", toolTitle = " ").headline).zhAny())
    }

    @Test fun personaOwnMcpServerShowsAsPersona() {
        assertEquals("Persona · agent read", toolDisplayName("mcp__persona__agent_read"))
        assertEquals("Persona · agent send", toolDisplayName("mcp__plugin_persona__agent_send"))
        assertEquals("Persona · notify user", toolDisplayName("mcp_persona_notify_user"))
        assertEquals("Persona · notify user", toolDisplayName("mcp_persona_0c671287_notify_user"))
        for (server in ProtocolCompatibility.personaServers) {
            assertEquals("mcp__persona__task_review", canonicalMcpToolName("mcp_${server}_55a1c59d_task_review"))
        }
        assertEquals("Persona", toolDisplayName("mcp__persona"))
        assertEquals("github · create issue", toolDisplayName("mcp__github__create_issue"))
        assertEquals("persona_extra · x", toolDisplayName("mcp__persona_extra__x"))
        assertEquals("Bash", toolDisplayName("Bash"))
        assertEquals(null, toolDisplayName(null))
        assertEquals("Persona", mcpServerLabel("persona"))
        assertEquals("example-service", mcpServerLabel("example-service"))
    }

    /** Every screen that shows a request's headline names an MCP tool the way people read it, not mcp__x__y (#4806). */
    @Test fun interactionHeadlineNamesMcpTools() {
        val ix = Interaction("p", kind = "permission", toolName = "mcp__github__create_issue", summary = "Fix login")
        assertEquals("请求使用 github · create issue：Fix login", (ix.headline).zhAny())
        assertEquals("请求使用 Persona · mail send", (ix.copy(toolName = "mcp__persona__mail_send", summary = null).headline).zhAny())
        assertEquals("Codex 请求使用 github · create issue", (ix.copy(agent = "codex", summary = null).headline).zhAny())
    }

    @Test fun screenDialogDecodesCommandAskFieldsAndKeepsOldHubsCompatible() {
        val ix = JsonCodec.decodeFromString<Interaction>(
            """{"id":"ix_command","kind":"screen","dialog":{"question":"Do you want to proceed?",""" +
                """"command":"rm -rf build","description":"Remove generated files","warning":"Dangerous rm operation","""+
                """"risk":"delete","deadline":"Automatically denied in 1:44","deadline_action":"deny","""+
                """"deadline_at":"2026-11-12T11:00:00+08:00","rev":"abc","options":[{"key":"1","label":"Yes"},{"key":"2","label":"No"}]}}""",
        )
        val dialog = ix.dialog!!
        assertEquals("rm -rf build", dialog.command)
        assertEquals("Remove generated files", dialog.description)
        assertEquals("Dangerous rm operation", dialog.warning)
        assertEquals("delete", dialog.risk)
        assertEquals("Automatically denied in 1:44", dialog.deadline)
        assertEquals("deny", dialog.deadlineAction)
        assertEquals(java.time.Instant.parse("2026-11-12T03:00:00Z"), parseTime(dialog.deadlineAt))
        assertEquals(ix, JsonCodec.decodeFromString<Interaction>(JsonCodec.encodeToString(ix)))
        val old = JsonCodec.decodeFromString<Interaction>("""{"id":"old","kind":"screen","dialog":{"question":"Continue?"}}""").dialog!!
        assertNull(old.command); assertNull(old.description); assertNull(old.warning); assertNull(old.risk)
        assertNull(old.deadline); assertNull(old.deadlineAction); assertNull(old.deadlineAt)
    }

    @Test fun clarificationCapabilityAndCurrentDeadlineRoundTripWithoutChangingOldHubDefaults() {
        val updated = JsonCodec.decodeFromString<Interaction>(
            """{"id":"ix_clarified","machine_id":"computer","session_id":"chat","kind":"permission",""" +
                """"expires_at":"2026-11-20T03:00:00Z","can_clarify":true}""",
        )
        assertEquals(java.time.Instant.parse("2026-11-20T03:00:00Z"), parseTime(updated.expiresAt))
        val wire = JsonCodec.encodeToString(updated)
        assertTrue(wire, wire.contains(""""can_clarify":true"""))
        assertTrue(wire, wire.contains(""""expires_at":"2026-11-20T03:00:00Z""""))
        assertEquals(updated, JsonCodec.decodeFromString<Interaction>(wire))

        val oldHub = JsonCodec.decodeFromString<Interaction>(
            """{"id":"ix_old","kind":"permission","expires_at":"2026-11-19T03:00:00Z"}""",
        )
        val oldWire = JsonCodec.encodeToString(oldHub)
        assertFalse(oldWire, oldWire.contains("can_clarify"))
        assertEquals(oldHub, JsonCodec.decodeFromString<Interaction>(oldWire))
        // A malformed additive flag drops only itself, retaining the legacy card and its fail-closed default.
        val malformed = JsonCodec.decodeFromString<Interaction>(
            """{"id":"ix_malformed","kind":"permission","can_clarify":"yes"}""",
        )
        assertFalse(JsonCodec.encodeToString(malformed).contains("can_clarify"))
    }

    @Test fun noHandBackRoundTripsAndDefaultsFalseForOlderHubs() {
        val headless = JsonCodec.decodeFromString<Interaction>(
            """{"id":"ix_headless","machine_id":"computer","session_id":"chat","kind":"permission","no_hand_back":true}""",
        )
        assertTrue(headless.noHandBack)
        val wire = JsonCodec.encodeToString(headless)
        assertTrue(wire, wire.contains(""""no_hand_back":true"""))
        assertEquals(headless, JsonCodec.decodeFromString<Interaction>(wire))

        val legacy = JsonCodec.decodeFromString<Interaction>("""{"id":"ix_legacy","kind":"permission"}""")
        assertFalse(legacy.noHandBack)
        val legacyWire = JsonCodec.encodeToString(legacy)
        assertFalse(legacyWire, legacyWire.contains("no_hand_back"))
        assertEquals(legacy, JsonCodec.decodeFromString<Interaction>(legacyWire))

        val malformed = JsonCodec.decodeFromString<Interaction>(
            """{"id":"ix_malformed_hb","kind":"permission","no_hand_back":"yes"}""",
        )
        assertFalse(malformed.noHandBack)
        assertFalse(JsonCodec.encodeToString(malformed).contains("no_hand_back"))
    }
}
