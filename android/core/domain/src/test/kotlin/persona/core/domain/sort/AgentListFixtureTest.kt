package persona.core.domain.sort

import persona.core.contracts.JsonCodec
import persona.core.contracts.Machine
import persona.core.contracts.Session
import persona.core.contracts.parseTime
import kotlinx.serialization.json.*
import org.junit.Assert.assertEquals
import org.junit.Assume.assumeTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.Parameterized

@RunWith(Parameterized::class)
class AgentListFixtureTest internal constructor(private val fixture: ListFixture) {
    companion object {
        @JvmStatic @Parameterized.Parameters(name = "{0}")
        fun cases(): List<Array<Any>> = listFixtures("spec/fixtures/sort", "spec/fixtures/sort/next/held_set.json")
    }

    @Test fun matchesExpected() {
        val input = fixture.input
        val actual = when (fixture.operation) {
            "session_state" -> obj("sessions" to input.sessions().map {
                obj("id" to it.id, "state" to it.state.wire, "is_live" to it.isLive,
                    "is_quiet" to it.isQuiet, "is_waiting" to it.isWaiting, "sort_date" to it.sortDate?.toString())
            })
            "hub_state" -> obj("state" to deriveHubState(
                JsonCodec.decodeFromJsonElement(Session.serializer(), input.getValue("session")),
                parseTime(input.optional("seen_at")),
            ).wire)
            "agents_list" -> {
                val settings = input.obj("settings")
                assumeTrue("iOS-only search settings: ${iosOnlySettings(settings)}", iosOnlySettings(settings).isEmpty())
                val grouping = AgentGrouping.entries.single { it.name.equals(settings.string("grouping"), ignoreCase = true) }
                // The raw root and descendants where a case has them: this list hangs subtasks under their parent and
                // keeps a parent a search finds through them itself (codexTaskForest), as iOS's CodexTasks does before
                // its core gets the prepared `search_subtasks`.
                val sessions = if ("native_sessions" in input) input.objects("native_sessions").map(AgentSession::fromJson) else input.sessions()
                val result = buildAgentList(sessions, input.objects("machines").map {
                    JsonCodec.decodeFromJsonElement(Machine.serializer(), it)
                }, input.obj("pins").preferences(), input.obj("mutes").preferences(), AgentListSettings(
                    grouping = grouping,
                    quick = AgentQuickFilter.entries.single { it.name.equals(settings.string("quick"), ignoreCase = true) },
                    machineFilter = settings.string("machine_filter"), query = settings.string("query"),
                    kept = settings.strings("kept").toSet(), folded = settings.strings("folded").toSet(),
                    heldSeen = (settings["held_seen"] as? JsonObject).orEmpty().mapValues { (_, place) ->
                        AgentSeenPlace.entries.single { it.wire == place.jsonPrimitive.content }
                    },
                    expandedIdle = settings.strings("expanded_idle").toSet(), expandedQuiet = settings.strings("expanded_quiet").toSet(),
                ), held = settings.optional("held"))
                val output = linkedMapOf<String, Any?>(
                    "counts" to result.counts.filterKeys { it.source == null }.mapKeys { it.key.name.replaceFirstChar(Char::lowercase) },
                    "groups" to result.groups.map { group ->
                        linkedMapOf<String, Any?>(
                            "id" to group.id, "title" to group.title, "rows" to group.rows.map { it.id },
                            "visible_rows" to group.visibleRows.map { it.id }, "folded" to group.folded,
                        ).apply {
                            group.parent?.let { put("parent", it) }
                            group.stateCounts?.let { put("state_counts", it.mapKeys { entry -> entry.key.wire }) }
                            group.spoken?.let { put("spoken", it) }
                            if ("held_seen" in settings) put("held_seen", group.heldSeen)
                        }
                    },
                    "unread_shown" to result.unreadShown,
                )
                if (grouping == AgentGrouping.Space) output["quiet_title"] = result.quietTitle
                json(output)
            }
            else -> error("Unknown operation: ${fixture.operation}")
        }
        assertEquals(fixture.name, fixture.expected, actual)
    }

    /**
     * The settings of iOS's Agents search this list has no counterpart for, as a case sets them (spec/fixtures/sort/README.md):
     * a search scope other than All, Search as Code, and the dedicated machine, harness, project, model and pinned settings.
     * Their cases are skipped here, not failed.
     */
    private fun iosOnlySettings(settings: JsonObject): List<String> = buildList {
        settings.optional("search_scope")?.takeIf { it != "all" }?.let { add("search_scope=$it") }
        if (settings.optional("search_as_code") == "true") add("search_as_code")
        for (key in listOf("machine_title_filter", "harness_filter", "project_filter", "model_filter"))
            if (!settings.optional(key).isNullOrEmpty()) add(key)
        if (settings.optional("pinned_only") == "true") add("pinned_only")
    }
}
