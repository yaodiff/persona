package persona.core.rustcore

import java.io.File
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
import kotlinx.serialization.json.long
import kotlinx.serialization.json.longOrNull
import kotlinx.serialization.json.put
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.JsonCodec
import persona.core.rustcore.wire.Action
import persona.core.rustcore.wire.ChatEvent
import persona.core.rustcore.wire.ChatPatch
import persona.core.rustcore.wire.ChatSnapshot
import persona.core.rustcore.wire.Config
import persona.core.rustcore.wire.Effect
import persona.core.rustcore.wire.ListOp
import persona.core.rustcore.wire.Payload
import persona.core.rustcore.wire.Request
import persona.core.rustcore.wire.WireBatch

/**
 * Replays the in-scope wire cases (`spec/fixtures/wire`) through the real JNI binding as `Action.WirePayload`, the way
 * a client hands the hub's bytes to the core (docs/plans/plan-rust-core.md, decision 2). [RawJson] keeps each case's
 * `input` and each item's source text, so the decoder is fed the bytes in the file and every `ListOp.Insert`/`Update`
 * `json` is compared with the item's raw text, never with a re-serialised object.
 *
 * What a check proves, mirroring `persona-core/tests/transport_fixtures.rs`: an `expected.fields` pointer the decoder
 * produced (a page's `has_more` or `history`, an item's `kind` or fold range) is checked against what the binding
 * published; any other item pointer is compared with the kept JSON, which shows the bytes survived the boundary and
 * nothing more. All 17 cases are accepted payloads, so the refusal paths are separate inputs derived from the Swift
 * decoding rules, not from the corpus.
 *
 * The wire cases are single payloads without a starting state, so each one opens a fresh empty chat. Where a case's
 * page is not the chat's first one (an `after_seq` answer, a live push with a gap, a backlog catch-up, an opened fold's
 * page) the test first restores the snapshot the page answers, stated in [preludes]; that mirrors the continuity
 * corpus's `initial` state rather than inventing new semantics. A frame is routed by the chat id, which `payload_step`
 * compares with BOTH the frame's `machine_id` and its `session_id`; the corpus always sends `m1`/`s1`, so this test
 * uses CHAT = "s1", and a mismatch is covered by a dedicated case.
 */
class TransportBindingFixtureTest {
    /** Records effects and applies each drained patch to a list, as the client's list would. */
    private class Harness : CoreHost {
        val effects = CopyOnWriteArrayList<Effect>()
        val items = mutableListOf<String>()
        var patch = ChatPatch(0u, 0, null, null, false, false, null, emptyList())
        override fun changed(version: Long) = Unit
        override fun run(effect: Effect) {
            effects.add(effect)
        }

        fun drain(core: RustCore): ChatPatch {
            assertTrue("the worker did not finish", core.flush(5_000))
            patch = core.drain(CHAT)
            for (op in patch.ops) {
                when (op) {
                    is ListOp.Remove -> items.removeAt(op.index.toInt())
                    is ListOp.Insert -> items.add(op.index.toInt(), op.json)
                    is ListOp.Update -> items[op.index.toInt()] = op.json
                }
            }
            return patch
        }

        /** Opens the chat with the state the payload answers, drains the initial publish and clears the effects. */
        fun open(core: RustCore, prelude: Prelude) {
            val snapshot = ChatSnapshot(
                // The restored items keep their source text too, so a prelude and a page carrying the same item
                // are the same bytes to the core's diff.
                prelude.items.joinToString(",", "[", "]"),
                prelude.history,
                prelude.covered,
                prelude.hasMore,
                prelude.loadedOnce,
            )
            assertTrue(core.dispatch(Action.OpenChat(CHAT, snapshot)))
            drain(core)
            effects.clear()
        }

        fun requests(): List<Request> = effects.map { effect: Effect ->
            when (effect) {
                is Effect.Fetch -> {
                    assertEquals(CHAT, effect.chat)
                    effect.request
                }
                is Effect.Rejected -> error("rejected: ${effect.reason}")
            }
        }

        fun rejections(): List<String> = effects.mapNotNull { (it as? Effect.Rejected)?.reason }
    }

    /** The state a case's payload answers, as the continuity corpus's `initial` object. Default is a fresh chat. */
    private data class Prelude(
        val items: List<String> = emptyList(),
        val history: String? = null,
        val covered: Long = 0,
        val hasMore: Boolean? = null,
        val loadedOnce: Boolean = false,
    )

    /** One in-scope case: its raw input, its expectations and the state the payload continues. */
    private class Case(val raw: RawJson.Case, val file: String) {
        val name: String get() = raw.name
        val input: JsonObject by lazy { JsonCodec.parseToJsonElement(raw.input).jsonObject }
        private val document: JsonObject get() = documents.getValue("$file/${raw.name}")
        val accepted: Boolean get() =
            document.getValue("expected").jsonObject.getValue("accepted").jsonPrimitive.content == "true"
        val fields: JsonObject get() =
            document.getValue("expected").jsonObject["fields"]?.jsonObject ?: JsonObject(emptyMap())
        val prelude: Prelude get() = preludes[raw.name] ?: Prelude()
        val isFrame: Boolean get() = raw.decoder == "event" || raw.decoder == "compat_event"
    }

    @Test fun exactCaseSet() {
        // Every case of the three files is either replayed here, a frame of another type, or owned by another
        // decoder: an added or dropped case changes one of these sets and fails the test.
        val all = listOf("items.json", "item_events.json", "compatibility.json").flatMap { corpus(it) }
        val others = all.filter { it.name !in IN_SCOPE }.map { it.name }.toSortedSet()
        assertEquals(OTHER_FRAMES.toSortedSet() + OUT_OF_SCOPE.toSortedSet(), others)
        assertEquals(IN_SCOPE.toSortedSet(), cases().map { it.name }.toSortedSet())
    }

    @Test fun httpPagesThroughTheBinding() = replay(cases().filter { !it.isFrame })

    @Test fun itemFramesThroughTheBinding() = replay(cases().filter { it.isFrame })

    private fun replay(cases: List<Case>) {
        for (case in cases) {
            val harness = Harness()
            RustCore(Config(), harness).use { core ->
                harness.open(core, case.prelude)
                assertTrue(case.name, core.dispatch(Action.WirePayload(CHAT, case.payload())))
                harness.drain(core)
                assertTrue("${case.name}: accepted", case.accepted)
                // The payload landed: every one of its items is in the published list at its seq position, byte for
                // byte. A payload can also replace an item the prelude already had, so compare with the expected
                // merge of prelude and payload rather than assuming a fresh list.
                assertEquals("${case.name}: the published items", case.mergedItems(), harness.items)
                // Each op carries the raw text of one of the payload's items (the merged list above also holds
                // prelude items the payload never carried, which is why this reads case.raw.items, not that list).
                for (op in harness.patch.ops) {
                    when (op) {
                        is ListOp.Insert -> assertTrue("${case.name}: insert bytes", op.json in case.raw.items)
                        is ListOp.Update -> assertTrue("${case.name}: update bytes", op.json in case.raw.items)
                        is ListOp.Remove -> Unit
                    }
                }
                case.checkFields(harness)
                assertEquals("${case.name}: requests", case.expectedRequests, harness.requests().map { it.json() })
                // Every field the fixture asserts was checked by a rule above; a pointer nobody checks is a silent
                // gap in this replay, not a pass.
                case.checkCoverage()
            }
        }
    }

    /**
     * Every expected pointer is either checked against the published state above or is a page field a patch cannot
     * re-encode, which the requests assert through the continuity result it drove. A pointer in neither set is a
     * silent gap in this replay, so a new expected field fails here until someone places it.
     */
    private fun Case.checkCoverage() {
        for (pointer in fields.keys - CHECKED_POINTERS) {
            assertTrue("$name $pointer: an expected field no rule checks", pointer in CONTINUITY_POINTERS)
        }
    }

    /** The published list after the payload: the prelude's items merged with the payload's, in seq order. */
    private fun Case.mergedItems(): List<String> {
        val want = prelude.items.toMutableList()
        for (raw in raw.items) {
            want.removeAll { itemId(it) == itemId(raw) }
            want.add(raw)
        }
        want.sortBy { itemSeq(it) }
        return want
    }

    @Test fun framesOfOtherTypesAreIgnoredAndRefusedFramesAreDropped() {
        // Every compat event of another type: Frame::Other is not an error and changes nothing (payload_step's None).
        for (name in OTHER_FRAMES) {
            val raw = corpus("compatibility.json").first { it.name == name }
            assertUnchanged(name, Payload.Frame(raw.input))
        }
        // A frame the decoder refuses is dropped without an effect, as iOS's ServerEvent.decode returns nil. The
        // wrong types below follow apple/Shared/LiveConnection.swift's Envelope: session_id is a required String
        // and items an optional array; neither is a wire corpus case (all of those are accepted payloads).
        for (body in listOf(
            """{"type":"items","machine_id":"m1","session_id":7,"items":[]}""",
            """{"type":"items","machine_id":"m1","session_id":"s1","items":{}}""",
            "not json at all",
        )) {
            assertUnchanged("a refused frame is dropped: $body", Payload.Frame(body))
        }
    }

    private fun assertUnchanged(label: String, payload: Payload) {
        val harness = Harness()
        RustCore(Config(), harness).use { core ->
            harness.open(core, Prelude())
            val before = harness.drain(core)
            assertTrue(label, core.dispatch(Action.WirePayload(CHAT, payload)))
            val after = harness.drain(core)
            assertTrue("$label: no effect", harness.effects.isEmpty())
            assertEquals("$label: no list change", emptyList<String>(), harness.items)
            assertEquals("$label: no new version", before.version, after.version)
        }
    }

    @Test fun aFrameForAnotherChatIsRejected() {
        val harness = Harness()
        RustCore(Config(), harness).use { core ->
            harness.open(core, Prelude())
            // The frame names m1/s1; this chat is neither, so payload_step reports the routing bug instead of
            // applying another chat's items here.
            assertTrue(core.dispatch(Action.OpenChat("elsewhere", ChatSnapshot("", null, 0, null, false))))
            assertTrue(
                core.dispatch(
                    Action.WirePayload(
                        "elsewhere",
                        Payload.Frame("""{"type":"items","machine_id":"m1","session_id":"s1","items":[]}"""),
                    ),
                ),
            )
            harness.drain(core)
            assertEquals(listOf("payload frame is for another chat"), harness.rejections())
        }
    }

    /**
     * Refusals derived from reading the Swift decoders (`APIClient.ItemsResponse`, `NewerResponse`, `foldedWork.Range`
     * and `ServerEvent.Envelope`): a known key of the wrong type refuses the whole payload. There is no wire fixture
     * for refusals, so these inputs are built here. The reason must name a code and protocol keys only: the marker
     * stands for payload content (an id or message text) and must never appear in it.
     */
    @Test fun refusedPayloadsNameOnlyTheCodeAndKeys() {
        val marker = "content-that-must-not-leak"
        val refusals = listOf(
            // ItemsResponse.has_more is a required Bool.
            Payload.Latest("""{"items":[],"has_more":"$marker"}""") to "wrong_type: has_more",
            // ItemsResponse.to_seq is an optional Int; a string is not one.
            Payload.Latest("""{"items":[],"has_more":false,"to_seq":"$marker"}""") to "wrong_type: to_seq",
            // ItemsResponse.items is required; null is not an array.
            Payload.Latest("""{"items":null,"has_more":false}""") to "missing_key: items",
            // Item.id is a required String (apple/Shared/Models.swift, Item).
            Payload.Latest("""{"items":[{"id":7,"seq":1,"role":"user","kind":"text","text":"$marker"}],"has_more":false}""") to
                "bad_item: items[0] wrong_type id",
            // Item.kind is a required String; iOS fails the whole response, it does not skip the item.
            Payload.Latest("""{"items":[{"id":"$marker","seq":1,"role":"user"}],"has_more":false}""") to
                "bad_item: items[0] missing_key kind",
            // NewerResponse.more_after is an optional Bool.
            Payload.Newer("""{"items":[],"more_after":"$marker"}""") to "wrong_type: more_after",
            // foldedWork.Range.more is an optional Bool.
            Payload.Range(null, """{"items":[],"more":1}""") to "wrong_type: more",
            // A fold's through_seq is a required Int (Models.swift, Fold).
            Payload.Latest(
                """{"items":[{"id":"fold#0","seq":1,"role":"assistant","kind":"fold",""" +
                    """"fold":{"items":1,"tools":0,"thoughts":0,"through_seq":"$marker","through_id":"1#0"}}],"has_more":false}""",
            ) to "bad_item: items[0] wrong_type fold.through_seq",
        )
        for ((payload, reason) in refusals) {
            val harness = Harness()
            RustCore(Config(), harness).use { core ->
                harness.open(core, Prelude())
                assertTrue(core.dispatch(Action.WirePayload(CHAT, payload)))
                harness.drain(core)
                val rejected = harness.rejections().singleOrNull() ?: error("effects: ${harness.effects}")
                assertEquals("refused payload: $reason", rejected)
                assertFalse("the reason repeats payload content", rejected.contains(marker))
                assertTrue("the reason is a code and key names: $rejected", REASON.matches(rejected))
                assertTrue("a refused payload changes no item", harness.items.isEmpty())
            }
        }
    }

    /** The same input as bytes and as the equivalent hand-built event must produce the same patch and effects. */
    @Test fun bytePayloadsAndHandBuiltEventsAgree() {
        for (name in listOf(
            "items_all_kinds",
            "items_http_after",
            "items_live_lazy",
            "items_backlog_latest",
            "items_empty_advance",
        )) {
            val case = cases().first { it.name == name }
            val bytes = runOnce(case, handBuilt = false)
            val hand = runOnce(case, handBuilt = true)
            assertEquals("$name: the patch from bytes and from a hand-built event", hand.patch, bytes.patch)
            assertEquals("$name: effects", hand.effects, bytes.effects)
            assertEquals("$name: items", hand.items, bytes.items)
            // The comparison is meaningful: something was published.
            assertNotEquals("$name: an empty patch", 0u, bytes.patch.version)
        }
        // An opened fold's page: open the fold from its prelude, then answer with the range payload.
        val range = cases().first { it.name == "items_fold_range" }
        val bytes = runOnce(range, handBuilt = false, openFold = true)
        val hand = runOnce(range, handBuilt = true, openFold = true)
        assertEquals("items_fold_range: patch", hand.patch, bytes.patch)
        assertEquals("items_fold_range: effects", hand.effects, bytes.effects)
        assertEquals("items_fold_range: items", hand.items, bytes.items)
        // The two paths really took different decoders: the fold was opened, its page published its work, and the
        // page's `more: true` asks for the range's next page from the item that just arrived.
        assertEquals(
            listOf<Request>(Request.FetchRange("fold:entry_thinking#0", 2, 4), Request.FetchRange("fold:entry_thinking#0", 2, 4)),
            bytes.effects.map { (it as Effect.Fetch).request },
        )
        // The opened work is published next to the fold that stands for it: the fold itself is retained opened, and
        // the range's single non-fold item joins the list.
        assertEquals(
            listOf("entry_text#0", "fold:entry_thinking#0", "entry_recap#0"),
            bytes.items.map { itemId(it) },
        )
    }

    private class Run(val patch: ChatPatch, val effects: List<Effect>, val items: List<String>)

    /** Opens a chat, optionally opens the fold, dispatches one case as bytes or as a hand-built event, and drains. */
    private fun runOnce(case: Case, handBuilt: Boolean, openFold: Boolean = false): Run {
        val harness = Harness()
        RustCore(Config(), harness).use { core ->
            harness.open(core, case.prelude)
            if (openFold) {
                // The fold item the range opens sits in the case's prelude; open it by id, as the client would.
                val fold = case.prelude.items.first { itemId(it).startsWith("fold:") }
                assertTrue(core.dispatch(Action.Chat(CHAT, ChatEvent.OpenFold(itemId(fold)))))
                harness.drain(core)
                assertEquals("the fold was opened", 1, harness.effects.size)
            }
            val action = if (handBuilt) Action.Chat(CHAT, case.event()) else Action.WirePayload(CHAT, case.payload())
            assertTrue(case.name, core.dispatch(action))
            val patch = harness.drain(core)
            return Run(patch, harness.effects.toList(), harness.items.toList())
        }
    }

    private fun Case.payload(): Payload = when (raw.decoder) {
        "items" -> Payload.Latest(raw.input)
        "items_after" -> Payload.Newer(raw.input)
        "items_range" -> Payload.Range(null, raw.input)
        else -> Payload.Frame(raw.input)
    }

    /** The same payload as the client built it before transport decoding moved into the core. */
    private fun Case.event(): ChatEvent {
        fun field(key: String): JsonElement? = input[key]?.takeUnless { it is JsonNull }
        // The hand-built batch carries the same item bytes as the wire payload: joining the raw source texts keeps
        // the two paths' patches identical, which is what this comparison is for.
        val items = raw.items.joinToString(",", "[", "]")
        val batch = WireBatch(
            items,
            field("history")?.jsonPrimitive?.contentOrNull,
            field("from_seq")?.jsonPrimitive?.longOrNull,
            field("to_seq")?.jsonPrimitive?.longOrNull,
            field("backlog")?.jsonPrimitive?.booleanOrNull == true,
            field("has_more")?.jsonPrimitive?.booleanOrNull,
            field("more_after")?.jsonPrimitive?.booleanOrNull == true,
        )
        return when (raw.decoder) {
            "items" -> ChatEvent.Latest(batch)
            "items_after" -> ChatEvent.After(batch)
            "items_range" -> ChatEvent.Range(null, items, field("more")?.jsonPrimitive?.booleanOrNull == true)
            // A live frame is a push; the fixture's backlog flag rides in the batch, as with ws_items.
            else -> ChatEvent.Pushed(batch)
        }
    }

    /**
     * Checks each expected pointer against what the binding published, as the Rust replay's projection does. An item
     * pointer is an index into the PAYLOAD's items, not into the merged list (a prelude can hold items the payload
     * does not), so it reads the kept JSON of that item in the published list; finding it there by id is itself the
     * byte-preservation check.
     */
    private fun Case.checkFields(harness: Harness) {
        for ((pointer, want) in fields) {
            val label = "$name $pointer"
            val got = when {
                // The frame's type is what the decoder recognised: the harness published it as an items frame.
                pointer == "/type" -> JsonPrimitive("items").takeIf { isFrame }
                // The page's own pagination and history are what the decoder read and the reducer applied.
                pointer == "/has_more" -> harness.patch.has_more?.let(::JsonPrimitive)
                pointer == "/history" -> harness.patch.history?.let(::JsonPrimitive)
                // The whole item array: as many of the payload's items arrived as the merge added or replaced.
                pointer == "/items" -> {
                    assertEquals("$label: the payload's item count", raw.items.size, addedItems(harness))
                    want
                }
                pointer.startsWith("/items/") -> itemField(pointer, harness)
                // The remaining page fields (from_seq, to_seq, more_after, backlog, more) are not re-encodable from
                // a patch: they are asserted through the continuity result they drove, in expectedRequests.
                else -> continue
            } ?: JsonNull
            if (pointer == "/items/0/kind" && name == "item_unknown_kind") {
                // iOS's ItemKind.init reads an unknown kind as unknown; the core keeps the hub's string.
                assertEquals("$label: the iOS expectation moved", JsonPrimitive("unknown"), want)
                assertEquals("$label: the core's value", JsonPrimitive("future_kind"), got)
            } else {
                assertEquals(label, want, got)
            }
        }
    }

    /** How many of the payload's items the merge added or replaced, counted independently of the raw reader. */
    private fun Case.addedItems(harness: Harness): Int =
        harness.items.count { itemId(it) in inputIds() } - prelude.items.count { itemId(it) in inputIds() }

    /** The payload items' ids as the parsed input reports them, a second opinion next to [RawJson]. */
    private fun Case.inputIds(): Set<String> =
        (input["items"] as? JsonArray)?.map { it.jsonObject.getValue("id").jsonPrimitive.content }?.toSet()
            ?: emptySet()

    /** `/items/<n>/<field…>`, read from the published copy of the payload's n-th item, found by its id. */
    private fun Case.itemField(pointer: String, harness: Harness): JsonElement? {
        val parts = pointer.removePrefix("/items/").split("/")
        // The index is the parsed input's, so it cannot drift from the raw reader's; the id is looked up in the
        // published list, and the byte comparison is against the raw text, which the parse never re-serialised.
        val id = inputIds().let { ids ->
            val item = (input["items"] as JsonArray)[parts[0].toInt()].jsonObject
            item.getValue("id").jsonPrimitive.content.also { check(it in ids) }
        }
        val raw = raw.items.firstOrNull { itemId(it) == id } ?: error("$name: no raw item $id")
        val published = harness.items.firstOrNull { itemId(it) == id }
            ?: error("$name: the item $id is not in the published list")
        // The published copy is the payload's item byte for byte, so a field of it is the hub's field as kept.
        assertEquals("$name: $id kept byte for byte", raw, published)
        if (parts.size == 1) return JsonCodec.parseToJsonElement(published)
        return at(JsonCodec.parseToJsonElement(published).jsonObject, "/" + parts.drop(1).joinToString("/"))
    }

    /** The requests a payload must start, from the gap it left; the same rules the continuity corpus asserts. */
    private val Case.expectedRequests: List<JsonObject>
        get() = when (name) {
            // A live push from 0 through 8 on a chat already covered through 8 brings seq 1 and 8: nothing is missing.
            "items_live_lazy" -> emptyList()
            // An empty live advance from 8 through 10 on the same baseline progresses coverage, no fill.
            "items_empty_advance" -> emptyList()
            // The catch-up backlog push carries from_seq 10 on a chat covered through 10: nothing to fill.
            "items_backlog_catchup" -> emptyList()
            // The capped batch (more_after, from_seq 17) arrives on a chat covered through 17 with loaded_once set:
            // the reducer first drops the changed history's state, then counts this a fill, not a reload, because
            // the restored baseline had already loaded once. The gap is filled from the batch's own end, seq 18.
            "items_backlog_capped" -> listOf(fetchAfter(18))
            // An after answer whose from_seq 0 is behind the restored coverage 8 still merges; no new request.
            else -> emptyList()
        }

    companion object {
        /** Routed chat id: the corpus frames name machine m1 and session s1, and the id is compared with both. */
        private const val CHAT = "s1"

        private val IN_SCOPE = listOf(
            "items_all_kinds",
            "items_folded_work",
            "items_folded_tasks",
            "items_inline_image",
            "items_task_notifications",
            "items_reply_models",
            "items_http_after",
            "items_fold_range",
            "items_recorded_turn_duration",
            "items_live_lazy",
            "items_empty_advance",
            "items_backlog_latest",
            "items_backlog_catchup",
            "items_backlog_capped",
            "items_live_recorded_turn_duration",
            "items_legacy_null",
            "item_unknown_kind",
        )

        private val OTHER_FRAMES = listOf(
            "unknown_event_ignored",
            "hello_unknown_field",
            "hello_missing_features",
            "fleet_bad_optional_stats",
            "fleet_unknown_values",
            "hello_wrong_known_type",
            "session_bad_optional_field",
            "interaction_unknown_kind",
        )

        /** Cases of the same files another decoder owns (`compat_session`): not this replay's. */
        private val OUT_OF_SCOPE = listOf(
            "session_unknown_enums",
            "session_missing_fields",
            "session_zero_time",
            "date_at_cutoff",
        )

        /** Codes, key names, positions and indices only: never a value from the payload. */
        private val REASON = Regex(
            "refused payload: [a-z_]+(: ([a-z_.]+|line [0-9]+ column [0-9]+|byte [0-9]+|items\\[[0-9]+] [a-z_]+( [a-z_.]+)?))?",
        )

        /** Page fields a patch cannot re-encode; each drives the continuity result asserted as the requests. */
        private val CONTINUITY_POINTERS = setOf("/from_seq", "/to_seq", "/more_after", "/backlog", "/more")

        /** Every expected pointer the corpus holds that this replay checks directly. */
        private val CHECKED_POINTERS: Set<String> by lazy {
            cases().flatMap { case ->
                case.fields.keys.filter { pointer ->
                    pointer == "/type" || pointer == "/has_more" || pointer == "/history" ||
                        pointer == "/items" || pointer.startsWith("/items/")
                }
            }.toSet()
        }

        private fun root(): File = File(
            System.getProperty("persona.repo.root") ?: error("persona.repo.root is not set"),
            "spec/fixtures/wire",
        )

        private fun corpus(file: String): List<RawJson.Case> = RawJson.cases(File(root(), file))

        /** The corpus documents, for `accepted` and `fields`, which are expectations, not payload bytes. */
        private val documents: Map<String, JsonObject> by lazy {
            listOf("items.json", "item_events.json", "compatibility.json").flatMap { file ->
                JsonCodec.parseToJsonElement(File(root(), file).readText()).jsonArray.map {
                    val case = it.jsonObject
                    "$file/${case.getValue("name").jsonPrimitive.content}" to case
                }
            }.toMap()
        }

        private fun cases(): List<Case> = listOf("items.json", "item_events.json", "compatibility.json")
            .flatMap { file -> corpus(file).map { Case(it, file) } }
            .filter { it.name in IN_SCOPE }

        /**
         * The snapshot each payload answers, mirroring how the continuity corpus states a step's `initial`. A case
         * without an entry opens a fresh empty chat: its page is the chat's first backlog. Items are the raw source
         * texts of the corpus cases named, so the restored state is byte-identical to what a page would have carried.
         */
        private val preludes: Map<String, Prelude> by lazy {
            val raws = cases().associateBy { it.name }
            fun itemsOf(vararg names: String): List<String> = names.flatMap { raws.getValue(it).raw.items }
            mapOf(
                // The catch-up answers a request made after the live page up to seq 8 arrived (its own items are
                // seq 1 and 8, so the baseline has them and coverage is 8).
                "items_http_after" to Prelude(itemsOf("items_live_lazy"), "history_example", 8, null, true),
                // The empty live advance continues the same baseline: from 8, through 10.
                "items_empty_advance" to Prelude(itemsOf("items_live_lazy"), "history_example", 8, null, true),
                // The live page from 0 through 8 on the same baseline is a repeat of what is already covered.
                "items_live_lazy" to Prelude(itemsOf("items_backlog_latest"), "history_example", 8, null, true),
                // The backlog catch-up was requested from coverage 10 and brings seq 11, with the first backlog's
                // item already in the list.
                "items_backlog_catchup" to Prelude(itemsOf("items_backlog_latest"), "history_example", 10, null, true),
                // The capped batch continues it from 17, beyond coverage: the core asks for the latest page.
                "items_backlog_capped" to Prelude(itemsOf("items_backlog_catchup"), "history_example", 17, null, true),
                // The opened fold's page answers FetchRange(2, 4): the fold of items_folded_work sits at seq 2.
                "items_fold_range" to Prelude(itemsOf("items_folded_work"), null, 7, true, true),
            )
        }
    }
}

private fun fetchAfter(afterSeq: Long): JsonObject = buildJsonObject {
    put("kind", "fetch_after")
    put("after_seq", afterSeq)
}

private fun Request.json(): JsonObject = buildJsonObject {
    when (val request = this@json) {
        is Request.FetchAfter -> {
            put("kind", "fetch_after")
            put("after_seq", request.after_seq)
        }
        is Request.FetchLatest -> {
            put("kind", "fetch_latest")
            put("fold_work", request.fold_work)
        }
        is Request.Subscribe -> {
            put("kind", "subscribe")
            put("fold_work", request.fold_work)
            request.since_seq?.let { put("since_seq", it) }
        }
        is Request.FetchOlder -> {
            put("kind", "fetch_older")
            put("before_seq", request.before_seq)
            put("fold_work", request.fold_work)
        }
        is Request.FetchRange -> {
            put("kind", "fetch_range")
            put("from_seq", request.from_seq)
            put("through_seq", request.through_seq)
        }
    }
}

/** The item's id, read from its raw JSON without re-serialising it. */
private fun itemId(raw: String): String =
    JsonCodec.parseToJsonElement(raw).jsonObject.getValue("id").jsonPrimitive.content

private fun itemSeq(raw: String): Long =
    JsonCodec.parseToJsonElement(raw).jsonObject.getValue("seq").jsonPrimitive.long

/** The value at an RFC 6901 pointer, or null when it is absent (the fixtures equate absent and null). */
private fun at(document: JsonObject, pointer: String): JsonElement? {
    var current: JsonElement = document
    for (token in pointer.removePrefix("/").split("/")) {
        val key = token.replace("~1", "/").replace("~0", "~")
        current = when (val node = current) {
            is JsonObject -> node[key] ?: return null
            is JsonArray -> node.getOrNull(key.toInt()) ?: return null
            else -> return null
        }
    }
    return current
}
