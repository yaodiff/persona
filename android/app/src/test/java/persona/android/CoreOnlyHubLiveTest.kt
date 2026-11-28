package persona.android

import androidx.navigation3.runtime.NavEntry
import androidx.navigation3.runtime.NavKey
import androidx.navigation3.runtime.entryProvider
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeout
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.put
import kotlinx.serialization.json.putJsonObject
import okhttp3.HttpUrl.Companion.toHttpUrl
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Assume.assumeTrue
import org.junit.Test
import persona.core.contracts.Endpoint
import persona.core.contracts.JsonCodec
import persona.core.hub.HttpDeviceSettings
import persona.core.hub.HttpHubManagers
import persona.core.hub.HttpInboxAssistantTransport
import persona.core.hub.HttpTasksApi
import persona.core.hub.HubConnectionConfig
import persona.core.hub.HubNetwork
import persona.core.hub.HubPairingClient
import persona.core.hub.OkHttpHubConnection
import persona.core.moduleapi.AudioBroker
import persona.core.moduleapi.HubModule
import persona.core.moduleapi.ModuleNavContext
import persona.core.moduleapi.ModuleRegistry
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator
import java.io.File

/**
 * Optional real-hub check of Android's production pairing, socket and module registry (#16786).
 *
 * scripts/ci/android-core-only-hub.py supplies a disposable loopback hub. This is deliberately a JVM integration
 * check, not a claim about Android UI rendering, permissions, media or other device-only behavior.
 */
class CoreOnlyHubLiveTest {
    @Test fun pairsReadsCoreAndFollowsTheRealHello() = runBlocking {
        val path = System.getenv("PERSONA_ANDROID_CORE_HUB_FIXTURE")
        assumeTrue("Set PERSONA_ANDROID_CORE_HUB_FIXTURE to run against the isolated hub", !path.isNullOrBlank())
        val file = File(requireNotNull(path))
        val fixture = JsonCodec.parseToJsonElement(file.readText()).jsonObject
        val base = fixture.string("base_url").toHttpUrl()
        require(base.scheme == "http" && base.host in setOf("127.0.0.1", "::1", "localhost") &&
            base.username.isEmpty() && base.password.isEmpty() && base.query == null && base.fragment == null) {
            "The live hub must be a credential-free loopback HTTP address"
        }
        require(fixture["confirmed_loopback"]?.jsonPrimitive?.content == "true") {
            "The driver must confirm the sandbox's loopback listener before pairing"
        }
        val edition = fixture.string("edition")
        require(edition in setOf("core-only", "full")) { "Unknown hub edition" }
        val expectModules = edition == "full"
        val runId = fixture.string("run_id")
        println("Android hub edition probe: pairing to $base; confirmed disposable sandbox loopback listener; edition=$edition")

        withTimeout(90_000) {
            val paired = HubPairingClient(fixture.string("hub_id"), null, listOf(base.toString()), "core-edition-live").use { client ->
                client.pair(fixture.string("pairing_token"), "Android core edition JVM probe") {}
            }
            assertFalse("A current hub must issue a device credential, not fall back to the pairing token",
                paired.token == fixture.string("pairing_token"))
            assertNotNull("The hub did not return a device credential id", paired.deviceId)
            assertTrue("The issued value is not an Android device credential", paired.token.startsWith("jd_"))

            val config = HubConnectionConfig(paired.hubId, paired.fingerprint, paired.token,
                paired.candidates.map { Endpoint(it, "lan") }, "core-edition-live", deviceId = paired.deviceId)
            OkHttpHubConnection(config, HubNetwork("loopback")).use { connection ->
                val socketHello = requireNotNull(withTimeout(60_000) { connection.currentHello.first { it != null } })
                val features = connection.serverFeatures.value
                assertEquals(socketHello.hello.features.toSet(), features)
                assertTrue("The real hello lacks the module registry marker", ModuleRegistry.MODULES_MARKER in features)

                // The live driver runs the default app build. An empty carried list would make the off assertions vacuous.
                val carried = Modules.carried.modules.map { it.id }.toSet()
                assertEquals("The live check must carry every module", Modules.known.map { it.first }.toSet(), carried)
                assertTrue("The live check requires the default module build", carried.isNotEmpty())
                val active = Modules.carried.active(features).map { it.id }.toSet()
                assertEquals(if (expectModules) carried else emptySet(), active)
                val allSettingsPages = Modules.carried.modules.flatMap { module -> module.settingsPages().map { it.id } }.toSet()
                assertEquals(if (expectModules) allSettingsPages else emptySet(),
                    Modules.carried.settingsPages(features).map { it.id }.toSet())
                for (module in HubModule.entries) {
                    if (expectModules) assertTrue("$edition ${module.feature}", ModuleRegistry.isActive(module, features))
                    else assertFalse("$edition ${module.feature}", ModuleRegistry.isActive(module, features))
                }

                // Register exactly what the real registry makes active. Core-only must leave every debug page the
                // modules expose to the shell's unavailable-page fallback; the full control registers those same pages.
                val debugPages = Modules.carried.debugPages()
                assertTrue("The carried modules expose no pages to check", debugPages.isNotEmpty())
                val unregistered = unregistered(Modules.carried, features, debugPages.values)
                assertEquals(if (expectModules) emptySet() else debugPages.values.toSet(), unregistered.toSet())

                // Use the issued credential through the production connection and typed adapters for core reads.
                val roster = HttpHubManagers(connection).roster()
                assertTrue("The core manager roster is empty", roster.installed.isNotEmpty())
                assertNotNull("The built-in core manager is absent", roster.manager(roster.defaultManagerId))
                val sessions = HubPairingClient(connection).use { it.json("/api/v1/sessions", paired.token) }
                assertTrue("The core sessions response is not a list", sessions["sessions"] is JsonArray)
                val inbox = HttpInboxAssistantTransport("core-edition-live", connection).inbox()
                val inboxItems = requireNotNull(inbox["items"]) { "The Inbox response has no items list" }
                assertTrue("The Inbox items value is not a list", inboxItems is JsonArray)
                assertTrue("The fresh hub's Inbox is not empty", inboxItems.jsonArray.isEmpty())
                val todos = HttpTasksApi(connection).todos()
                assertTrue("The fresh hub's To-dos list is not empty", todos.isEmpty())
                val devices = HttpDeviceSettings(paired.address, paired.token, paired.deviceId!!,
                    connection.http.client).devices()
                assertEquals("The issued credential is not the current device", paired.deviceId, devices.currentId)
                assertTrue("The issued credential is not listed", devices.devices.any { it.id == paired.deviceId })

                val proof = buildJsonObject {
                    put("schema", 1)
                    put("run_id", runId)
                    put("edition", edition)
                    put("device_credential_issued", true)
                    put("current_device_confirmed", true)
                    put("modules_marker", true)
                    putJsonObject("core_reads") {
                        put("installed_managers", roster.installed.size)
                        put("sessions", sessions.getValue("sessions").jsonArray.size)
                        put("inbox_items", inboxItems.jsonArray.size)
                        put("todos", todos.size)
                        put("paired_devices", devices.devices.size)
                    }
                    put("carried_modules", kotlinx.serialization.json.JsonArray(carried.sorted().map { kotlinx.serialization.json.JsonPrimitive(it) }))
                    put("active_modules", kotlinx.serialization.json.JsonArray(active.sorted().map { kotlinx.serialization.json.JsonPrimitive(it) }))
                    put("unregistered_module_debug_pages", unregistered.size)
                    putJsonObject("hub_modules") {
                        HubModule.entries.forEach { module -> put(module.id, ModuleRegistry.isActive(module, features)) }
                    }
                    put("module_flags", kotlinx.serialization.json.JsonArray(features.filter {
                        it == ModuleRegistry.MODULES_MARKER || it.startsWith("module.")
                    }.sorted().map { kotlinx.serialization.json.JsonPrimitive(it) }))
                }
                file.resolveSibling("android-proof.json").writeText(proof.toString() + "\n")
                println("Android hub edition probe PASS: edition=$edition; device credential issued; " +
                    "core managers=${roster.installed.size}; active modules=${active.sorted()}; " +
                    "unregistered module debug pages=${unregistered.size}")
            }
        }
    }

    private fun unregistered(registry: ModuleRegistry, features: Set<String>, keys: Collection<NavKey>): List<NavKey> {
        val missing = mutableListOf<NavKey>()
        val navigator = object : Navigator {
            override fun navigate(key: NavKey) = Unit
            override fun back() = Unit
            override fun selectTab(tab: AppTab) = Unit
        }
        val provider = entryProvider<NavKey>(fallback = { key -> missing += key; NavEntry(key) {} }) {
            registry.active(features).forEach {
                it.navEntries(this, ModuleNavContext(navigator, null, unavailable = {}, audioFocus = AudioBroker.withoutSystem()))
            }
        }
        keys.forEach { val _ = provider(it) }
        return missing
    }

    private companion object {
        fun JsonObject.string(key: String): String = getValue(key).jsonPrimitive.content
    }
}
