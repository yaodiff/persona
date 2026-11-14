package persona.core.domain.views

import persona.core.contracts.ProtocolCompatibility

import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.contentOrNull
import persona.core.contracts.JsonCodec
import persona.core.contracts.generated.ExtensionAction
import persona.core.contracts.generated.ExtensionButtonComponent
import persona.core.contracts.generated.ExtensionEmptyComponent
import persona.core.contracts.generated.ExtensionFieldComponent
import persona.core.contracts.generated.ExtensionHtmlComponent
import persona.core.contracts.generated.ExtensionRowComponent
import persona.core.contracts.generated.ExtensionSegmentedComponent
import persona.core.contracts.generated.ExtensionView as WireView
import persona.core.contracts.api.ExtensionView
import java.net.URI

// An assistant's card (docs/assistant/assistant-views.md "Cards reuse extension View" "Actions and multiple pages"): the extensions' View with the
// assistant's own fields. Read with the generated wire types and checked as `CheckAssistantView` does in
// `sdk/go/wire/assistantview.go`; a card that fails is drawn as its fallback, never half.

/** What one of the card's actions does: exactly one of [reply], [open], [push] or [pop]. */
data class AssistantAction(
    val id: String,
    /** The page the action is on; null: the root. */
    val page: String?,
    val reply: String? = null,
    val open: String? = null,
    val push: String? = null,
    val pop: Boolean = false,
    val confirm: String? = null,
    val submit: Boolean = false,
) {
    /** A reply is the card's one answer; open, push and pop never lock the card. */
    val answers: Boolean get() = reply != null
}

/** An `html` component of the card: its [key] (none: no bridge), on [page] (null: the root). */
data class AssistantHtml(val key: String?, val page: String?, val html: String, val height: Int, val title: String?)

/** A card read and checked: its pages as the renderer draws them, its actions by id and its plain text. */
data class AssistantCard(
    val root: ExtensionView,
    val pages: Map<String, ExtensionView>,
    val fallback: String,
    val actions: Map<String, AssistantAction>,
    val html: List<AssistantHtml>,
) {
    fun page(name: String?): ExtensionView? = if (name == null) root else pages[name]

    /** Whether a picture of the reply draws it (Share as a picture, #7261): an HTML page on its first page only runs live. */
    val drawsInPicture: Boolean get() = html.none { it.page == null }

    /**
     * The field names [action] sends, as the hub's `viewReplyMessage` takes them: every field of its page when it
     * submits, a segmented's own name, and the page's search (`q`) and filter with a submit, the search's or the
     * filter's own action. A submit must send all of them.
     */
    fun fields(action: AssistantAction): List<String> {
        val page = page(action.page) ?: return emptyList()
        val search = page.search?.action?.id?.takeIf { it.isNotEmpty() }
        val filter = page.filter?.takeIf { it.name.isNotEmpty() }
        val filterId = filter?.action?.id?.takeIf { it.isNotEmpty() }
        return buildList {
            page.sections.flatMap { it.items }.filter { item ->
                (action.submit && item.type == "field" || item.type == "segmented" && item.action?.id == action.id) && !item.name.isNullOrEmpty()
            }.mapNotNullTo(this) { it.name }
            if (search != null && (action.submit || action.id == search || action.id == filterId)) add(SearchField)
            if (filter != null && (action.submit || action.id == filterId)) add(filter.name)
        }.distinct()
    }

    /** What the page's search box and filter hold before the user touches them, by field name. */
    fun searchValues(page: ExtensionView): Map<String, String> = buildMap {
        page.search?.let { put(SearchField, it.value.orEmpty()) }
        page.filter?.takeIf { it.name.isNotEmpty() }?.let { f -> put(f.name, f.value ?: f.choices.firstOrNull()?.value.orEmpty()) }
    }

    companion object {
        /** The search box's value goes under this name (as the hub reads it). */
        const val SearchField = "q"
    }
}

object AssistantCards {
    const val MostBytes = 64 * 1024
    const val MostPages = 8
    /** How deep pushed pages may go. */
    const val MostDepth = 4
    const val HtmlMostBytes = 64 * 1024
    const val ReplyMost = 2000

    private val pageName = Regex("^[\\p{L}\\p{N}_-]{1,64}$")

    /** A ```persona-view block's text as a card, or null (not JSON, not a card, or one the rules refuse). */
    fun parse(raw: String): AssistantCard? {
        if (raw.toByteArray(Charsets.UTF_8).size > MostBytes || !shallow(raw)) return null
        return parse(runCatching { JsonCodec.parseToJsonElement(raw) }.getOrNull() ?: return null)
    }

    /** A card from its JSON (a live card's `view`, a block's text). */
    fun parse(json: JsonElement): AssistantCard? {
        var obj = json as? JsonObject ?: return null
        val fallback = (obj["fallback"] as? JsonPrimitive)?.takeIf { it.isString }?.contentOrNull?.takeIf { it.isNotBlank() } ?: return null
        // One component on its own is a view of one section.
        if (obj["type"] != null) {
            obj = JsonObject(mapOf("fallback" to JsonPrimitive(fallback),
                "sections" to kotlinx.serialization.json.JsonArray(listOf(JsonObject(mapOf(
                    "items" to kotlinx.serialization.json.JsonArray(listOf(JsonObject(obj - "fallback")))))))))
        }
        val wire = runCatching { JsonCodec.decodeFromJsonElement(WireView.serializer(), obj) }.getOrNull() ?: return null
        val pages = wire.pages.orEmpty()
        if (pages.size > MostPages || pages.any { (name, page) -> !pageName.matches(name) || !page.pages.isNullOrEmpty() }) return null
        val actions = LinkedHashMap<String, AssistantAction>()
        val html = ArrayList<AssistantHtml>()
        fun check(page: WireView, name: String?): Boolean {
            for (section in page.sections) for (item in section.items) {
                if (item is ExtensionFieldComponent && item.kind == "secret") return false
                if (item is ExtensionHtmlComponent) {
                    if (item.html.isBlank() || item.html.toByteArray(Charsets.UTF_8).size > HtmlMostBytes) return false
                    html += AssistantHtml(item.key?.takeIf { it.isNotBlank() }, name, item.html, (item.height ?: 320).coerceIn(160, 640),
                        item.title?.takeIf { it.isNotBlank() })
                }
            }
            for (a in actionsOf(page)) {
                if (a.id.isEmpty() || a.id in actions) return false
                actions[a.id] = action(a, name, pages.keys) ?: return false
            }
            return true
        }
        if (!check(wire, null)) return null
        for (name in pages.keys.sorted()) if (!check(pages.getValue(name), name)) return null
        val pageJson = obj["pages"] as? JsonObject
        fun view(page: JsonObject): ExtensionView? =
            runCatching { JsonCodec.decodeFromJsonElement(ExtensionView.serializer(), JsonObject(page - "pages")) }.getOrNull()
        val root = view(obj) ?: return null
        val drawn = pages.keys.associateWith { name -> view(pageJson?.get(name) as? JsonObject ?: return null) ?: return null }
        return AssistantCard(root, drawn, fallback.take(ReplyMost), actions, html)
    }

    /** Every action a page declares: rows', buttons', segmented controls', an empty state's, the search, filter and "Load more". */
    private fun actionsOf(page: WireView): List<ExtensionAction> = buildList {
        page.search?.action?.let(::add)
        page.filter?.action?.let(::add)
        for (section in page.sections) {
            for (item in section.items) when (item) {
                is ExtensionRowComponent -> item.action?.let(::add)
                is ExtensionButtonComponent -> add(item.action)
                is ExtensionSegmentedComponent -> add(item.action)
                is ExtensionEmptyComponent -> item.button?.action?.let(::add)
                else -> Unit
            }
            section.more?.action?.let(::add)
        }
    }

    private fun action(a: ExtensionAction, page: String?, pages: Set<String>): AssistantAction? {
        val reply = a.reply?.takeIf { it.isNotEmpty() }
        val open = a.open?.takeIf { it.isNotEmpty() }
        val push = a.push
        val pop = a.pop == true
        if (listOf(reply != null, open != null, push != null, pop).count { it } != 1) return null
        if (a.submit == true && reply == null) return null
        if (reply != null && reply.isBlank()) return null
        if (open != null && AssistantOpen.target(open) == null) return null
        if (push != null && (push.page.isNullOrEmpty() || !push.ui.isNullOrEmpty() || !push.state.isNullOrEmpty() || push.page !in pages)) return null
        return AssistantAction(a.id, page, reply, open, push?.page, pop, a.confirm?.takeIf { it.isNotBlank() }, a.submit == true)
    }

    /** JSON nested at most 32 deep (the hub's limit): a deeper block is refused before it is parsed. */
    private fun shallow(raw: String): Boolean {
        var depth = 0
        var quoted = false
        var escaped = false
        for (c in raw) {
            if (quoted) {
                when {
                    escaped -> escaped = false
                    c == '\\' -> escaped = true
                    c == '"' -> quoted = false
                }
                continue
            }
            when (c) {
                '"' -> quoted = true
                '{', '[' -> if (++depth > 32) return false
                '}', ']' -> depth--
            }
        }
        return true
    }
}

/** Where a card's `open` goes: a web page, or one of the app's own targets (`CheckAssistantOpen`). */
sealed interface AssistantOpenTarget {
    data class Web(val url: String) : AssistantOpenTarget
    data class Session(val machine: String, val session: String) : AssistantOpenTarget
    data class Task(val id: String) : AssistantOpenTarget
    data class Knowledge(val path: String) : AssistantOpenTarget
    data class File(val machine: String, val path: String) : AssistantOpenTarget
}

object AssistantOpen {
    /**
     * [value] as a target, or null: https without credentials, or `persona://`
     * `session/<machine>/<session>`, `task/<id>`, `kb/<relative path>`, `file/<machine>/<absolute path>` with each
     * segment escaped once and nothing else. Never anything to hand to another app.
     */
    fun target(value: String): AssistantOpenTarget? {
        if (value.any { it.isISOControl() } || value.contains('\\')) return null
        val uri = runCatching { URI(value) }.getOrNull() ?: return null
        if (uri.isOpaque || uri.rawUserInfo != null || uri.host.isNullOrEmpty()) return null
        val scheme = uri.scheme ?: return null
        if (scheme == "https") return AssistantOpenTarget.Web(value)
        if (scheme !in ProtocolCompatibility.appSchemes || uri.rawQuery != null || uri.rawFragment != null || uri.port != -1) return null
        val host = uri.host
        val parts = uri.rawPath.orEmpty().removePrefix("/").split('/')
        val decoded = parts.map { part ->
            val v = unescape(part) ?: return null
            if (v.isEmpty() || v.any { it.isISOControl() } || v.contains('\\')) return null
            if (host != "file" && v.contains('/')) return null
            v
        }
        return when (host) {
            "session" -> if (decoded.size == 2) AssistantOpenTarget.Session(decoded[0], decoded[1]) else null
            "task" -> if (decoded.size == 1) AssistantOpenTarget.Task(decoded[0]) else null
            "kb" -> if (decoded.none { it == "." || it == ".." }) AssistantOpenTarget.Knowledge(decoded.joinToString("/")) else null
            "file" -> if (decoded.size == 2 && !decoded[0].contains('/') && decoded[1].startsWith("/")) AssistantOpenTarget.File(decoded[0], decoded[1]) else null
            else -> null
        }
    }

    /** One path segment's escapes undone (UTF-8), or null for a broken escape; `+` stays a plus. */
    private fun unescape(part: String): String? {
        val bytes = java.io.ByteArrayOutputStream()
        var i = 0
        while (i < part.length) {
            val c = part[i]
            if (c == '%') {
                val hex = part.substring(i + 1, minOf(i + 3, part.length))
                if (hex.length != 2 || !hex.all { it in '0'..'9' || it.lowercaseChar() in 'a'..'f' }) return null
                bytes.write(hex.toInt(16))
                i += 3
            } else {
                bytes.write(c.toString().toByteArray(Charsets.UTF_8))
                i++
            }
        }
        val out = bytes.toByteArray()
        val decoder = Charsets.UTF_8.newDecoder()
        return runCatching { decoder.decode(java.nio.ByteBuffer.wrap(out)).toString() }.getOrNull()
    }
}
