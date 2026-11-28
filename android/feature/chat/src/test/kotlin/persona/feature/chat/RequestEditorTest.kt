package persona.feature.chat

import persona.core.contracts.*
import persona.core.domain.answer.*
import kotlinx.serialization.json.*
import org.junit.Assert.*
import org.junit.Test

class RequestEditorTest {
    @Test fun requestDraftSurvivesReopeningAndSharesOriginalLabelsButNeverSecretsOrReplacedPrompts() {
        val disk = mutableMapOf<String, String>()
        fun journal() = RequestDraftJournal(disk::get) { key, value -> if (value == null) disk.remove(key) else disk[key] = value }
        val question = Question("选择方案", multiSelect = true, options = listOf(QuestionOption("保留 (Recommended)")))
        val request = Interaction("request", machineId = "machine", sessionId = "session", kind = "question", questions = listOf(question))
        val draft = AnswerDraft(picks = mapOf(question.question to setOf("保留 (Recommended)")), other = mapOf("补充" to "稍后再删"), page = 1)
        journal().save("pairing-a", request, draft)
        assertEquals(draft, journal().load("pairing-a", request))
        assertEquals(draft, journal().load("pairing-a", request.copy(expiresAt = "2026-12-01T00:00:00Z")))
        assertEquals(AnswerDraft(), journal().load("pairing-b", request))
        assertEquals(AnswerDraft(), journal().load("pairing-a", request.copy(questions = listOf(question.copy(question = "新问题")))))
        journal().save("pairing-a", request.copy(secret = true), draft)
        assertTrue(disk.isEmpty())
        val form = request.copy(kind = "elicitation", elicitation = Elicitation(requestedSchema = JsonCodec.parseToJsonElement(
            """{"type":"object","properties":{"note":{"type":"string"},"password":{"type":"string","format":"password"}}}""").jsonObject))
        journal().save("pairing-a", form, AnswerDraft(fields = buildJsonObject { put("note", "继续"); put("password", "private-value") }))
        assertEquals(buildJsonObject { put("note", "继续") }, journal().load("pairing-a", form).fields)
        assertFalse(disk.values.single().contains("private-value"))
        journal().clear("pairing-a", form)
        assertTrue(disk.isEmpty())
        val url = request.copy(kind = "elicitation", elicitation = Elicitation(mode = "url", url = "https://example.com/authorize"))
        journal().save("pairing-a", url, AnswerDraft(openedElicitationUrl = url.elicitation!!.url))
        assertNull(journal().load("pairing-a", url).openedElicitationUrl)
        assertTrue(disk.isEmpty())
    }

    /** A password card's typed password (#1581) never reaches the saved request drafts, alone or beside other draft text. */
    @Test fun aPasswordCardsTypedPasswordIsNeverJournaled() {
        val disk = mutableMapOf<String, String>()
        val journal = RequestDraftJournal(disk::get) { key, value -> if (value == null) disk.remove(key) else disk[key] = value }
        val card = Interaction("ix_secret", machineId = "hub", sessionId = "chat", kind = "permission", toolName = "secret_run",
            fields = listOf(InteractionField(key = "password", label = "Password", secret = true)))
        journal.save("pairing-a", card, AnswerDraft(password = "private-value"))
        assertTrue(disk.isEmpty())
        journal.save("pairing-a", card, AnswerDraft(password = "private-value").copy(message = "note"))
        assertFalse(disk.values.single().contains("private-value"))
        assertEquals("", journal.load("pairing-a", card).password)
    }

    @Test fun elicitationValidatesTypedFieldsAndAllOutcomesWhileSourcesAndCommandsRequireEvidence() {
        val request = JsonCodec.decodeFromString(Interaction.serializer(), """{
            "id":"form","kind":"elicitation","mcp_server_name":"example-service","agent_type":"Explore",
            "elicitation":{"mode":"form","requested_schema":{"type":"object","required":["count","enabled","choice","tags","note"],"properties":{
                "count":{"type":"integer","minimum":1,"maximum":3},"enabled":{"type":"boolean"},
                "choice":{"type":"string","enum":["a","b"]},"tags":{"type":"array","items":{"type":"string","enum":["x","y"]},"minItems":1},
                "note":{"type":"string","minLength":2,"maxLength":5}
            }}}} """)
        val fields = buildJsonObject {
            put("count", "2"); put("enabled", false); put("choice", "a"); put("tags", JsonArray(listOf(JsonPrimitive("x")))); put("note", "继续")
        }
        val draft = AnswerDraft(fields = fields)
        val accepted = AnswerBuilder.build(request, AnswerAction.SubmitElicitation, draft).resolution!!
        assertEquals("accept", accepted.action)
        assertEquals(JsonPrimitive(2), accepted.content?.get("count"))
        assertEquals(JsonPrimitive(false), accepted.content?.get("enabled"))
        assertEquals("decline", AnswerBuilder.build(request, AnswerAction.DeclineElicitation).resolution?.action)
        assertEquals("cancel", AnswerBuilder.build(request, AnswerAction.CancelElicitation).resolution?.action)
        assertFalse(AnswerBuilder.build(request, AnswerAction.SubmitElicitation, draft, AnswerContext(locked = true)).allowed)
        assertFalse(AnswerBuilder.build(request, AnswerAction.SubmitElicitation, draft, AnswerContext(entry = AnswerEntry.Notification)).allowed)
        listOf(JsonObject(fields - "enabled"), JsonObject(fields + ("count" to JsonPrimitive("4"))),
            JsonObject(fields + ("tags" to JsonArray(listOf(JsonPrimitive("other")))))).forEach {
            assertFalse(AnswerBuilder.build(request, AnswerAction.SubmitElicitation, draft.copy(fields = it)).allowed)
        }
        val unsupported = request.copy(elicitation = request.elicitation!!.copy(requestedSchema = JsonObject(
            request.elicitation!!.requestedSchema!! + ("oneOf" to JsonArray(emptyList())))))
        assertFalse(AnswerBuilder.build(unsupported, AnswerAction.SubmitElicitation, draft).allowed)
        assertEquals("来自子 Agent · Explore\n来自 MCP 服务器 example-service", request.requestSourceLabel()?.shown)
        assertNull(Interaction("unknown", toolName = "mcp__untrusted__tool").requestSourceLabel())
        val screen = Interaction("screen-request", kind = "screen", toolUseId = "tool-id")
        val tool = Item("item", kind = "tool_use", toolUseId = "tool-id", toolInput = buildJsonObject { put("command", "printf '完整命令\\n'\nprintf '第二行'") })
        assertEquals(RequestCommand("screen-request", "tool-id", tool.toolInput!!.jsonObject["command"]!!.jsonPrimitive.content), screen.requestCommand(listOf(tool)))
        assertNull(screen.requestCommand(listOf(tool.copy(toolUseId = "other"))))
        assertNull(screen.copy(toolUseId = null).requestCommand(listOf(tool)))
        assertNull(screen.copy(secret = true).requestCommand(listOf(tool)))
    }

    @Test fun untickingAllPreCheckedDialogOptionsSurvivesSaveAndReload() {
        val disk = mutableMapOf<String, String>()
        val journal = RequestDraftJournal(disk::get) { key, value -> if (value == null) disk.remove(key) else disk[key] = value }
        val request = Interaction(
            id = "screen-checkboxes",
            machineId = "machine",
            sessionId = "session",
            kind = "screen",
            dialog = ScreenDialog(
                multi = true,
                options = listOf(
                    DialogOption(key = "a", label = "Keep A", kind = "plain", checked = true),
                    DialogOption(key = "b", label = "Keep B", kind = null, checked = true),
                    DialogOption(key = "c", label = "Keep C", kind = "plain", checked = false),
                    DialogOption(key = "other", label = "Type something", kind = "type_something"),
                ),
            ),
        )
        fun toggle(current: AnswerDraft, key: String): AnswerDraft {
            val res = AnswerBuilder.build(request, AnswerAction.ScreenOption, current.copy(option = key))
            assertTrue(res.allowed)
            assertNull(res.resolution)
            return res.draft
        }

        // Initial load starts with what the computer already ticked.
        var draft = journal.load("pairing-a", request)
        assertEquals(setOf("a", "b"), draft.selected)

        // Partial untick through AnswerBuilder (which leaves transient option = "a" on draft) survives reload.
        journal.save("pairing-a", request, toggle(draft, "a"))
        draft = journal.load("pairing-a", request)
        assertEquals(setOf("b"), draft.selected)

        // Full untick survives save + reload instead of snapping back to checked.
        journal.save("pairing-a", request, toggle(draft, "b"))
        draft = journal.load("pairing-a", request)
        assertEquals(emptySet<String>(), draft.selected)

        // Unticking all pre-checked options alongside custom text stays unticked and submits empty options + text.
        journal.save("pairing-a", request, draft.copy(text = "custom note"))
        val reloaded = journal.load("pairing-a", request)
        assertEquals(emptySet<String>(), reloaded.selected)
        assertEquals("custom note", reloaded.text)
        val submitted = AnswerBuilder.build(request, AnswerAction.ScreenSubmit, reloaded)
        assertTrue(submitted.allowed)
        assertEquals(Resolution(options = emptyList(), text = "custom note"), submitted.resolution)

        // Legacy entries written without a "selected" key still fall back to computer-checked options.
        val storedKey = disk.keys.single()
        val storedObj = JsonCodec.parseToJsonElement(disk.getValue(storedKey)).jsonObject
        val legacyDraft = JsonObject(storedObj.getValue("draft").jsonObject - "selected")
        disk[storedKey] = JsonObject(storedObj + ("draft" to legacyDraft)).toString()
        val legacy = journal.load("pairing-a", request)
        assertEquals(setOf("a", "b"), legacy.selected)
        assertEquals("custom note", legacy.text)

        // Corrupted "selected" values (non-string items, non-array, null) fall back to defaults instead of decoding garbage.
        for (bad in listOf(
            JsonArray(listOf(JsonNull)),
            JsonArray(listOf(JsonPrimitive(1))),
            JsonArray(listOf(JsonPrimitive(true))),
            JsonPrimitive("a"),
            JsonNull,
        )) {
            val badDraft = JsonObject(storedObj.getValue("draft").jsonObject + ("selected" to bad))
            disk[storedKey] = JsonObject(storedObj + ("draft" to badDraft)).toString()
            assertEquals(setOf("a", "b"), journal.load("pairing-a", request).selected)
        }

        // Re-ticking back to the computer's initial pre-checked state via ScreenOption (transient option non-empty) clears disk.
        val backToA = toggle(reloaded.copy(text = ""), "a")
        val backToInitial = toggle(backToA, "b")
        assertEquals("b", backToInitial.option)
        journal.save("pairing-a", request, backToInitial)
        assertTrue(disk.isEmpty())
        assertEquals(setOf("a", "b"), journal.load("pairing-a", request).selected)

        // A multi-select dialog with no pre-checked options clears disk when toggled on and back off via ScreenOption.
        val unchecked = request.copy(
            id = "screen-unchecked",
            dialog = ScreenDialog(
                multi = true,
                options = listOf(DialogOption(key = "x", label = "Pick X", kind = "plain", checked = false)),
            ),
        )
        val ticked = AnswerBuilder.build(unchecked, AnswerAction.ScreenOption, AnswerDraft(option = "x")).draft
        journal.save("pairing-a", unchecked, ticked)
        assertEquals(setOf("x"), journal.load("pairing-a", unchecked).selected)
        val unticked = AnswerBuilder.build(unchecked, AnswerAction.ScreenOption, ticked.copy(option = "x")).draft
        assertEquals("x", unticked.option)
        journal.save("pairing-a", unchecked, unticked)
        assertTrue(disk.isEmpty())
    }

    @Test fun clearingAnElicitationSchemaDefaultSurvivesSaveAndReload() {
        val disk = mutableMapOf<String, String>()
        val journal = RequestDraftJournal(disk::get) { key, value -> if (value == null) disk.remove(key) else disk[key] = value }
        val form = Interaction(
            id = "elicitation-default",
            machineId = "machine",
            sessionId = "session",
            kind = "elicitation",
            elicitation = Elicitation(
                mode = "form",
                requestedSchema = JsonCodec.parseToJsonElement(
                    """{
                        "type":"object",
                        "properties":{
                            "note":{"type":"string","default":"initial"},
                            "count":{"type":"integer","default":3},
                            "enabled":{"type":"boolean","default":true},
                            "choice":{"type":"string","enum":["x","y"],"default":"x"},
                            "secret":{"type":"string","format":"password","default":"never-stored"}
                        }
                    }""",
                ).jsonObject,
            ),
        )
        val expectedDefaults = buildJsonObject {
            put("note", "initial")
            put("count", 3)
            put("enabled", true)
            put("choice", "x")
        }
        assertEquals(expectedDefaults, journal.load("pairing-a", form).fields)

        // Clearing a default field to an empty string survives save + reload, and secret fields never touch disk.
        val clearedNote = buildJsonObject {
            put("note", "")
            put("count", 3)
            put("enabled", true)
            put("choice", "x")
            put("secret", "top-secret-token")
        }
        journal.save("pairing-a", form, AnswerDraft(fields = clearedNote))
        assertFalse(disk.values.single().contains("top-secret-token"))
        assertEquals(JsonObject(clearedNote - "secret"), journal.load("pairing-a", form).fields)

        // Removing all fields (empty JsonObject) survives save + reload without snapping back to defaults.
        journal.save("pairing-a", form, AnswerDraft(fields = JsonObject(emptyMap())))
        assertEquals(JsonObject(emptyMap()), journal.load("pairing-a", form).fields)

        // Missing or null "fields" in stored JSON falls back consistently to schema defaults.
        val storedKey = disk.keys.single()
        val storedObj = JsonCodec.parseToJsonElement(disk.getValue(storedKey)).jsonObject
        val nullFields = JsonObject(storedObj.getValue("draft").jsonObject + ("fields" to JsonNull))
        disk[storedKey] = JsonObject(storedObj + ("draft" to nullFields)).toString()
        assertEquals(expectedDefaults, journal.load("pairing-a", form).fields)
        val missingFields = JsonObject(storedObj.getValue("draft").jsonObject - "fields")
        disk[storedKey] = JsonObject(storedObj + ("draft" to missingFields)).toString()
        assertEquals(expectedDefaults, journal.load("pairing-a", form).fields)

        // Restoring exact defaults (even with transient non-persisted action fields or a secret value) clears disk.
        journal.save(
            "pairing-a",
            form,
            AnswerDraft(
                fields = JsonObject(expectedDefaults + ("secret" to JsonPrimitive("top-secret-token"))),
                option = "stale",
                optionIndex = 2,
                keys = listOf("enter"),
            ),
        )
        assertTrue(disk.isEmpty())
    }
}
