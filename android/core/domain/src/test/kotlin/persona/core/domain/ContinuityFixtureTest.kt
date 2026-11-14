package persona.core.domain

import persona.core.contracts.Item
import persona.core.contracts.JsonCodec
import persona.core.domain.ContinuityReducer.Gap
import persona.core.domain.ContinuitySync.Action
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.booleanOrNull
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.contentOrNull
import kotlinx.serialization.json.decodeFromJsonElement
import kotlinx.serialization.json.intOrNull
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.put
import org.junit.Assert.assertEquals
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.Parameterized

/** Every fixture step compares complete state and production request decisions, without timers or I/O. */
@RunWith(Parameterized::class)
class ContinuityFixtureTest(private val name: String, private val fixture: FixtureReader.Case) {
    @Test fun replay() {
        val case = fixture.value
        val input = case.getValue("input").jsonObject
        val initial = input.getValue("initial").jsonObject
        val reducer = ContinuityReducer()
        reducer.restore(initial.items(), initial.text("history"), initial.number("covered")!!,
            initial.boolean("has_more"), initial.flag("loaded_once"))
        val sync = ContinuitySync(reducer)
        val events = input.getValue("events").jsonArray
        val expected = case.getValue("expected").jsonArray
        assertEquals("$name: step count", events.size, expected.size)
        events.forEachIndexed { index, raw ->
            val event = raw.jsonObject
            var gap = Gap.None
            var actions = emptyList<Action>()
            when (val kind = event.text("kind")) {
                "http_latest", "ws_items" -> {
                    gap = reducer.apply(event.items(), event.text("history"), event.number("from_seq"),
                        event.number("to_seq"), kind == "http_latest" || event.flag("backlog"),
                        event.boolean("has_more"), event.flag("more_after"))
                    actions = sync.settle(gap)
                }
                "http_reload" -> sync.reload(event.items(), event.text("history"), event.number("to_seq"),
                    requireNotNull(event.boolean("has_more")))
                "http_after" -> {
                    val result = sync.after(event.items(), event.text("history"), event.number("from_seq"),
                        event.number("to_seq"), event.flag("more_after"))
                    gap = result.gap
                    actions = result.actions
                }
                "http_older" -> sync.older(event.items(), requireNotNull(event.boolean("has_more")))
                "continue_fill" -> actions = sync.continueFill()
                "load_older" -> actions = sync.loadOlder()
                "reconnect" -> actions = sync.reconnect()
                "open_fold" -> actions = sync.openFold(requireNotNull(event.text("fold_id")))
                "http_range" -> actions = sync.range(event.items(), event.flag("more"))
                else -> error("Unsupported continuity event $kind in ${fixture.file}/$name")
            }
            val state = expected[index].jsonObject
            val label = "${fixture.file}/$name step $index"
            assertEquals("$label items", state.items(), reducer.items)
            assertEquals("$label item_ids", state.getValue("item_ids").jsonArray.map { it.jsonPrimitive.content }, reducer.items.map { it.id })
            assertEquals("$label covered", state.number("covered"), reducer.covered)
            assertEquals("$label history", state.text("history"), reducer.history)
            assertEquals("$label has_more", state.boolean("has_more"), reducer.hasMore)
            assertEquals("$label loaded_once", state.boolean("loaded_once"), reducer.loadedOnce)
            assertEquals("$label gap", state.text("gap"), when (gap) {
                Gap.None -> "none"
                Gap.Fill -> "fill"
                Gap.Reload -> "reload"
            })
            assertEquals("$label actions", state.getValue("actions").jsonArray.toList(), actions.map { it.json() })
        }
    }

    companion object {
        @JvmStatic @Parameterized.Parameters(name = "{0}")
        fun cases(): List<Array<Any>> {
            val cases = FixtureReader.read("continuity")
            check(cases.isNotEmpty()) { "Continuity fixtures are required" }
            return cases.map { arrayOf(it.value.getValue("name").jsonPrimitive.content, it) }
        }
    }
}

private fun Action.json(): JsonObject = buildJsonObject {
    when (val action = this@json) {
        is Action.FetchAfter -> { put("kind", "fetch_after"); put("after_seq", action.afterSeq) }
        is Action.FetchLatest -> { put("kind", "fetch_latest"); put("fold_work", action.foldWork) }
        is Action.Subscribe -> {
            put("kind", "subscribe"); put("fold_work", action.foldWork)
            action.sinceSeq?.let { put("since_seq", it) }
        }
        is Action.FetchOlder -> {
            put("kind", "fetch_older"); put("before_seq", action.beforeSeq); put("fold_work", action.foldWork)
        }
        is Action.FetchRange -> {
            put("kind", "fetch_range"); put("from_seq", action.fromSeq); put("through_seq", action.throughSeq)
        }
    }
}

private fun JsonObject.text(key: String): String? = get(key)?.jsonPrimitive?.contentOrNull
private fun JsonObject.number(key: String): Int? = get(key)?.jsonPrimitive?.intOrNull
private fun JsonObject.boolean(key: String): Boolean? = get(key)?.jsonPrimitive?.booleanOrNull
private fun JsonObject.flag(key: String): Boolean = boolean(key) == true
private fun JsonObject.items(): List<Item> = get("items")?.let { JsonCodec.decodeFromJsonElement<List<Item>>(it) } ?: emptyList()
