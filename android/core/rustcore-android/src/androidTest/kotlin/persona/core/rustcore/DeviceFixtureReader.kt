package persona.core.rustcore

import androidx.test.platform.app.InstrumentationRegistry
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.int
import persona.core.contracts.JsonCodec

/** The exact spec fixtures copied into the instrumentation APK by its declared-input Gradle task. */
object DeviceFixtureReader {
    data class Case(val file: String, val value: JsonObject)

    fun read(domain: String): List<Case> {
        val manager = InstrumentationRegistry.getInstrumentation().context.assets
        val files = manager.list(domain).orEmpty().filter { it.endsWith(".json") }.sorted()
        check(files.isNotEmpty()) { "No packaged JSON fixtures for $domain" }
        return files.flatMap { file ->
            val fixture = manager.open("$domain/$file").bufferedReader().use {
                JsonCodec.parseToJsonElement(it.readText()).jsonObject
            }
            check(fixture.getValue("version").jsonPrimitive.int == 1) { "Unsupported fixture version: $file" }
            fixture.getValue("cases").jsonArray.map { Case(file, it.jsonObject) }
        }.also { cases ->
            val names = cases.map { it.value.getValue("name").jsonPrimitive.content }
            check(cases.isNotEmpty() && names.distinct().size == names.size) { "Missing or duplicate device fixtures" }
        }
    }
}
