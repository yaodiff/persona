package persona.core.domain.sort

import persona.core.domain.zh
import java.io.File
import persona.core.contracts.JsonCodec
import kotlinx.serialization.json.*

internal data class ListFixture(val name: String, val operation: String, val input: JsonObject, val expected: JsonElement) {
    override fun toString() = name
}

/** The list cases in each directory or JSON file, under an input domain declared by the build. */
internal fun listFixtures(vararg paths: String): List<Array<Any>> {
    val root = System.getProperty("persona.repo.root") ?: error("persona.repo.root is required")
    val files = paths.flatMap { path ->
        val location = File(root, path)
        val fixtures = if (location.isDirectory) {
            location.listFiles { file -> file.isFile && file.extension == "json" }.orEmpty().sortedBy { it.name }
        } else {
            listOf(location).filter { it.isFile && it.extension == "json" }
        }
        check(fixtures.isNotEmpty()) { "No fixtures: $location" }
        fixtures
    }
    // Only list fixtures ({schema_version, operation, cases}); the directory also holds other shared rules that their own
    // tests read (spec/fixtures/inbox/handoff_memory.json, #2725).
    return files.mapNotNull { file -> JsonCodec.parseToJsonElement(file.readText()).jsonObject.takeIf { "cases" in it }?.let { file to it } }
        .also { check(it.isNotEmpty()) { "No list fixtures: ${paths.joinToString()}" } }
        .flatMap { (file, document) ->
            check(document.getValue("schema_version").jsonPrimitive.int == 1)
            val cases = document.objects("cases")
            check(cases.isNotEmpty()) { "No cases: $file" }
            cases.map { case -> arrayOf<Any>(ListFixture(
                "${file.name}/${case.string("name")}", document.string("operation"),
                case.getValue("input").jsonObject, case.getValue("expected"),
            )) }
        }
}

internal fun JsonObject.optional(key: String): String? = (get(key) as? JsonPrimitive)?.contentOrNull
internal fun JsonObject.string(key: String): String = optional(key).orEmpty()
internal fun JsonObject.objects(key: String): List<JsonObject> = (get(key) as? JsonArray).orEmpty().map { it.jsonObject }
internal fun JsonObject.strings(key: String): List<String> = (get(key) as? JsonArray).orEmpty().map { it.jsonPrimitive.content }
internal fun JsonObject.obj(key: String): JsonObject = get(key) as? JsonObject ?: JsonObject(emptyMap())
internal fun JsonObject.preferences() = AgentPreferences(strings("spaces"), strings("sessions"), strings("allowed").toSet())
internal fun JsonObject.sessions() = objects("sessions").map(AgentSession::fromJson)

internal fun json(value: Any?): JsonElement = when (value) {
    null -> JsonNull
    is JsonElement -> value
    is persona.core.contracts.CoreText -> JsonPrimitive(value.zh())
    is String -> JsonPrimitive(value)
    is Number -> JsonPrimitive(value)
    is Boolean -> JsonPrimitive(value)
    is Map<*, *> -> JsonObject(value.entries.associate { it.key.toString() to json(it.value) })
    is Iterable<*> -> JsonArray(value.map(::json))
    else -> error("Unsupported projection: $value")
}
internal fun obj(vararg pairs: Pair<String, Any?>): JsonElement = json(mapOf(*pairs))
