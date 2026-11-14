package persona.core.rustcore

import java.util.concurrent.CopyOnWriteArrayList
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonNull
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.booleanOrNull
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.contentOrNull
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.longOrNull
import kotlinx.serialization.json.put
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.Parameterized
import persona.core.contracts.JsonCodec
import persona.core.domain.FixtureReader
import persona.core.rustcore.wire.Action
import persona.core.rustcore.wire.ChatEvent
import persona.core.rustcore.wire.ChatPatch
import persona.core.rustcore.wire.ChatSnapshot
import persona.core.rustcore.wire.Config
import persona.core.rustcore.wire.Effect
import persona.core.rustcore.wire.ListOp
import persona.core.rustcore.wire.Request
import persona.core.rustcore.wire.WireBatch

/**
 * Replays spec/fixtures/continuity through the real JNI binding: actions encoded by the generated codecs, effects
 * through [CoreHost], and the list rebuilt from drained patches, compared with every step's expected state and requests.
 */
@RunWith(Parameterized::class)
class ContinuityBindingFixtureTest(private val name: String, private val fixture: FixtureReader.Case) {
    private class Recorder : CoreHost {
        val effects = CopyOnWriteArrayList<Effect>()
        override fun changed(version: Long) = Unit
        override fun run(effect: Effect) {
            effects.add(effect)
        }
    }

    @Test fun replay() {
        val case = fixture.value
        val input = case.getValue("input").jsonObject
        val initial = input.getValue("initial").jsonObject
        val host = Recorder()
        RustCore(Config(), host).use { core ->
            val snapshot = ChatSnapshot(
                initial.itemsJson(), initial.text("history"), initial.number("covered")!!,
                initial.boolean("has_more"), initial.flag("loaded_once"),
            )
            assertTrue(core.dispatch(Action.OpenChat(CHAT, snapshot)))
            val list = mutableListOf<String>()
            core.step(list)
            host.effects.clear()
                    val events = input.getValue("events").jsonArray
            val expected = case.getValue("expected").jsonArray
            assertEquals("$name: step count", events.size, expected.size)
            events.forEachIndexed { index, raw ->
                val event = raw.jsonObject
                val label = "${fixture.file}/$name step $index"
                val action = when (val kind = event.text("kind")) {
                    "http_latest" -> chat(ChatEvent.Latest(event.batch()))
                    "ws_items" -> chat(ChatEvent.Pushed(event.batch()))
                    "http_after" -> chat(ChatEvent.After(event.batch()))
                    "http_reload" -> chat(
                        ChatEvent.Reloaded(
                            event.itemsJson(), event.text("history"), event.number("to_seq"),
                            requireNotNull(event.boolean("has_more")),
                        ),
                    )
                    "http_older" -> chat(ChatEvent.Older(event.itemsJson(), requireNotNull(event.boolean("has_more"))))
                    "http_range" -> chat(ChatEvent.Range(null, event.itemsJson(), event.flag("more")))
                    "reconnect" -> chat(ChatEvent.Reconnected)
                    "load_older" -> chat(ChatEvent.LoadOlder)
                    "open_fold" -> chat(ChatEvent.OpenFold(requireNotNull(event.text("fold_id"))))
                    "continue_fill" -> Action.ContinueFill(CHAT)
                    else -> error("$label: unknown event $kind")
                }
                assertTrue("$label dispatch", core.dispatch(action))
                val patch = core.step(list)
                val requests = host.effects.map { effect ->
                    when (effect) {
                        is Effect.Fetch -> {
                            assertEquals(CHAT, effect.chat)
                            effect.request.json()
                        }
                        is Effect.Rejected -> error("$label: rejected: ${effect.reason}")
                    }
                }
                host.effects.clear()
                val want = expected[index].jsonObject
                assertEquals("$label items", want.getValue("items"), JsonArray(list.map(JsonCodec::parseToJsonElement)))
                assertEquals("$label covered", want.number("covered"), patch.covered)
                assertEquals("$label history", want.text("history"), patch.history)
                assertEquals("$label has_more", want.boolean("has_more"), patch.has_more)
                assertEquals("$label loaded_once", want.boolean("loaded_once"), patch.loaded_once)
                assertEquals("$label actions", want.getValue("actions").jsonArray.toList(), requests)
            }
        }
    }

    /** Waits for the worker, drains the chat and applies its list operations. */
    private fun RustCore.step(list: MutableList<String>): ChatPatch {
        assertTrue("$name: the worker did not finish", flush(5_000))
        val patch = drain(CHAT)
        for (op in patch.ops) {
            when (op) {
                is ListOp.Remove -> list.removeAt(op.index.toInt())
                is ListOp.Insert -> list.add(op.index.toInt(), op.json)
                is ListOp.Update -> list[op.index.toInt()] = op.json
            }
        }
        return patch
    }

    companion object {
        private const val CHAT = "fixture/continuity"

        @JvmStatic @Parameterized.Parameters(name = "{0}")
        fun cases(): List<Array<Any>> = FixtureReader.read("continuity").map {
            arrayOf(it.value.getValue("name").jsonPrimitive.content, it)
        }
    }
}

private fun chat(event: ChatEvent): Action = Action.Chat("fixture/continuity", event)

private fun Request.json(): JsonObject = buildJsonObject {
    when (val request = this@json) {
        is Request.FetchAfter -> { put("kind", "fetch_after"); put("after_seq", request.after_seq) }
        is Request.FetchLatest -> { put("kind", "fetch_latest"); put("fold_work", request.fold_work) }
        is Request.Subscribe -> {
            put("kind", "subscribe"); put("fold_work", request.fold_work)
            request.since_seq?.let { put("since_seq", it) }
        }
        is Request.FetchOlder -> {
            put("kind", "fetch_older"); put("before_seq", request.before_seq); put("fold_work", request.fold_work)
        }
        is Request.FetchRange -> {
            put("kind", "fetch_range"); put("from_seq", request.from_seq); put("through_seq", request.through_seq)
        }
    }
}

private fun JsonObject.batch() = WireBatch(
    itemsJson(), text("history"), number("from_seq"), number("to_seq"), flag("backlog"), boolean("has_more"),
    flag("more_after"),
)
private fun JsonObject.itemsJson(): String = (get("items") ?: JsonArray(emptyList())).toString()
private fun JsonObject.value(key: String): JsonPrimitive? = (get(key) as? JsonElement)?.takeIf { it != JsonNull }?.jsonPrimitive
private fun JsonObject.text(key: String): String? = value(key)?.contentOrNull
private fun JsonObject.number(key: String): Long? = value(key)?.longOrNull
private fun JsonObject.boolean(key: String): Boolean? = value(key)?.booleanOrNull
private fun JsonObject.flag(key: String): Boolean = boolean(key) == true
