package persona.feature.files

import androidx.annotation.StringRes
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.contentOrNull
import kotlinx.serialization.json.intOrNull
import java.util.Locale

/**
 * Jupyter notebooks for the file viewer (docs/android/android.md "Files and Git in chat"; web `lib/notebook.ts`, iOS `Notebook.swift`):
 * nbformat 4 (and 3's worksheets) into cells, each output reduced to the one representation the viewer shows. Pure.
 */
internal object Notebooks {
    /** Lines of output kept per text output; beyond that the viewer says it cut them. */
    const val MAX_OUTPUT_LINES = 2000

    fun isNotebook(name: String) = FilesPaths.extension(name) == "ipynb"

    sealed interface Output {
        data class Stream(val stderr: Boolean, val text: String) : Output
        /** A picture: [base64] for PNG / JPEG / GIF / WebP; SVG isn't drawn, its text alternative shows instead. */
        data class Image(val mime: String, val base64: String) : Output
        /** HTML (a pandas table, a widget's fallback): shown as its text, tags removed. */
        data class Html(val text: String) : Output
        data class Markdown(val text: String) : Output
        data class Text(val text: String) : Output
        data class Error(val name: String, val value: String, val traceback: String) : Output
    }

    enum class CellType { Code, Markdown, Raw }

    data class Cell(val type: CellType, val source: String, val count: Int? = null, val outputs: List<Output> = emptyList(),
        /** Markdown cells: pictures pasted into the cell, by name, as base64 PNG / JPEG. */
        val attachments: Map<String, String> = emptyMap())

    data class Notebook(val cells: List<Cell>, val language: String, val kernel: String?, val format: String)

    class Invalid(@StringRes val reason: Int) : Exception()

    private val json = Json { ignoreUnknownKeys = true }

    /** Removes terminal color codes (tracebacks are full of them). */
    fun stripAnsi(s: String): String = s.replace(csi, "").replace(osc, "")

    private val csi = Regex("\u001b\\[[0-9;?]*[ -/]*[@-~]")
    private val osc = Regex("\u001b\\][^\u0007]*\u0007")

    /** Text cut to [max] lines, saying how many lines were left out ([more]: the line that says so, for a count). */
    fun capLines(s: String, more: (Int) -> String, max: Int = MAX_OUTPUT_LINES): String {
        var i = -1
        repeat(max) {
            i = s.indexOf('\n', i + 1)
            if (i < 0) return s
        }
        if (i + 1 >= s.length) return s
        val end = if (s.endsWith('\n')) s.length - 1 else s.length
        var rest = 1
        var pos = i + 1
        while (pos < end) {
            if (s[pos] == '\n') rest++
            pos++
        }
        return s.substring(0, i + 1) + more(rest)
    }

    /** HTML as the text it shows: tags dropped, block ends as line breaks, table cells apart, entities decoded. */
    fun htmlText(html: String): String = htmlText(html, more = null, maxLines = Int.MAX_VALUE, paddingBudget = PaddingBudget())

    private class PaddingBudget(var remaining: Int = MAX_ALIGN_PADDING_CHARS)

    private fun htmlText(html: String, more: ((Int) -> String)?, maxLines: Int, paddingBudget: PaddingBudget): String {
        val noScript = stripScriptAndStyle(html)
        val breaks = noScript.replace(blockBreak, "\n").replace(cellBreak, "\t")
        val stripped = stripTags(breaks)
        val text = if ('&' in stripped) {
            stripped.replace(htmlEntity) { m ->
                val num = m.groupValues[1]
                if (num.isNotEmpty()) {
                    val code = if (num.startsWith("x", true)) num.drop(1).toIntOrNull(16) else num.toIntOrNull()
                    code?.takeIf { it in 1..0xD7FF || it in 0xE000..Character.MAX_CODE_POINT }
                        ?.let { String(Character.toChars(it)) } ?: m.value
                } else {
                    when (m.groupValues[2]) {
                        "nbsp" -> " "
                        "lt" -> "<"
                        "gt" -> ">"
                        "quot" -> "\""
                        "apos" -> "'"
                        "amp" -> "&"
                        else -> m.value
                    }
                }
            }
        } else {
            stripped
        }

        val cap = if (more != null) maxLines.coerceAtLeast(0) else Int.MAX_VALUE
        val kept = ArrayList<String>()
        var totalLines = 0
        var pendingBlank = false
        var pos = 0
        val len = text.length
        while (pos < len) {
            var lineEnd = pos
            while (lineEnd < len && text[lineEnd] != '\n' && text[lineEnd] != '\r') lineEnd++
            var trimEnd = lineEnd
            while (trimEnd > pos && text[trimEnd - 1].isWhitespace()) trimEnd--
            if (trimEnd == pos) {
                if (totalLines > 0) pendingBlank = true
            } else {
                if (pendingBlank) {
                    if (kept.size < cap) kept.add("")
                    totalLines++
                    pendingBlank = false
                }
                if (kept.size < cap) kept.add(text.substring(pos, trimEnd))
                totalLines++
            }
            pos = if (lineEnd < len && text[lineEnd] == '\r' && lineEnd + 1 < len && text[lineEnd + 1] == '\n') {
                lineEnd + 2
            } else {
                lineEnd + 1
            }
        }
        if (totalLines == 0) return ""
        val aligned = alignColumns(kept, paddingBudget).joinToString("\n")
        val hidden = totalLines - kept.size
        return if (more != null && hidden > 0) {
            if (kept.isEmpty()) more(hidden) else "$aligned\n${more(hidden)}"
        } else {
            aligned.trim('\n')
        }
    }

    private const val MAX_ALIGN_COLUMN_WIDTH = 80
    private const val MAX_ALIGN_COLUMNS = 64
    private const val MAX_ALIGN_PADDING_CHARS = 200_000

    private val blockBreak = Regex("(?i)<br\\s*/?>|</(p|div|tr|li|h[1-6]|thead|tbody|table)>")
    private val cellBreak = Regex("(?i)</t[dh]>")
    private val htmlEntity = Regex("&(?:#([xX]?[0-9a-fA-F]+)|(nbsp|lt|gt|quot|apos|amp));")
    private val whitespace = Regex("\\s+")

    /** Linear-time removal of `<script>...</script>` and `<style>...</style>` blocks without regex backtracking. */
    private fun stripScriptAndStyle(html: String): String {
        var pos = 0
        var lt = html.indexOf('<')
        if (lt < 0) return html
        var out: StringBuilder? = null
        var noMoreScriptEnd = false
        var noMoreStyleEnd = false
        val len = html.length
        while (lt >= 0) {
            val tag = when {
                !noMoreScriptEnd && html.regionMatches(lt + 1, "script", 0, 6, ignoreCase = true) && isTagNameEnd(html, lt + 7) -> "script"
                !noMoreStyleEnd && html.regionMatches(lt + 1, "style", 0, 5, ignoreCase = true) && isTagNameEnd(html, lt + 6) -> "style"
                else -> null
            }
            if (tag != null) {
                val closePrefix = if (tag == "script") "</script" else "</style"
                val closeEnd = findCloseTagEnd(html, closePrefix, lt + 1 + tag.length)
                if (closeEnd >= 0) {
                    if (out == null) out = StringBuilder(len)
                    out.append(html, pos, lt)
                    pos = closeEnd
                    lt = html.indexOf('<', pos)
                    continue
                } else {
                    if (tag == "script") noMoreScriptEnd = true else noMoreStyleEnd = true
                }
            }
            lt = html.indexOf('<', lt + 1)
        }
        if (out == null) return html
        out.append(html, pos, len)
        return out.toString()
    }

    private fun isTagNameEnd(s: String, index: Int): Boolean =
        index < s.length && (s[index] == '>' || s[index] == '/' || s[index].isWhitespace())

    /** Returns the index just after a matching `</script ...>` / `</style ...>` end tag, or -1 if none exists at or after [from]. */
    private fun findCloseTagEnd(html: String, closePrefix: String, from: Int): Int {
        val len = html.length
        var search = from
        while (search < len) {
            val idx = html.indexOf(closePrefix, search, ignoreCase = true)
            if (idx < 0) return -1
            var p = idx + closePrefix.length
            while (p < len && html[p].isWhitespace()) p++
            if (p < len && html[p] == '>') return p + 1
            search = idx + closePrefix.length
        }
        return -1
    }

    /** Linear-time removal of `<...>` tags matching `<[^>]*>` without quadratic backtracking on unclosed `<`. */
    private fun stripTags(s: String): String {
        var lt = s.indexOf('<')
        if (lt < 0) return s
        var gt = s.indexOf('>', lt + 1)
        if (gt < 0) return s
        val out = StringBuilder(s.length)
        var pos = 0
        while (true) {
            out.append(s, pos, lt)
            pos = gt + 1
            lt = s.indexOf('<', pos)
            if (lt < 0) break
            gt = s.indexOf('>', lt + 1)
            if (gt < 0) break
        }
        out.append(s, pos, s.length)
        return out.toString()
    }

    /** Rows of tab-separated cells (an HTML table's text) padded into columns for the mono face; other lines as they are. */
    private fun alignColumns(lines: List<String>, paddingBudget: PaddingBudget): List<String> {
        if (lines.isEmpty()) return lines
        var maxCols = 0
        val rows = lines.map { line ->
            if ('\t' !in line) {
                if (maxCols < 1) maxCols = 1
                listOf(line.trim())
            } else {
                val cells = line.split('\t').map(String::trim)
                if (cells.size > maxCols) maxCols = cells.size
                cells
            }
        }
        val alignedCols = minOf(maxCols, MAX_ALIGN_COLUMNS)
        val widths = IntArray(alignedCols)
        for (row in rows) {
            if (row.size <= 1) continue
            val limit = minOf(row.size, alignedCols)
            for (i in 0 until limit) {
                val cell = row[i]
                val w = if (cell.length >= MAX_ALIGN_COLUMN_WIDTH * 2) MAX_ALIGN_COLUMN_WIDTH else minOf(textCells(cell), MAX_ALIGN_COLUMN_WIDTH)
                if (w > widths[i]) widths[i] = w
            }
        }
        return rows.map { row ->
            if (row.size == 1) {
                row[0]
            } else {
                val sb = StringBuilder()
                for (i in row.indices) {
                    val cell = row[i]
                    sb.append(cell)
                    if (i != row.lastIndex) {
                        val extra = if (i < alignedCols && paddingBudget.remaining > 0) {
                            val w = if (cell.length >= MAX_ALIGN_COLUMN_WIDTH * 2) MAX_ALIGN_COLUMN_WIDTH else minOf(textCells(cell), MAX_ALIGN_COLUMN_WIDTH)
                            val needed = (widths[i] - w).coerceAtLeast(0)
                            val granted = minOf(needed, paddingBudget.remaining)
                            paddingBudget.remaining -= granted
                            granted
                        } else {
                            0
                        }
                        repeat(extra + 2) { sb.append(' ') }
                    }
                }
                sb.toString().trimEnd()
            }
        }
    }

    private fun str(v: JsonElement?): String = when (v) {
        is JsonArray -> v.joinToString("") { (it as? JsonPrimitive)?.takeIf { p -> p.isString }?.content.orEmpty() }
        is JsonPrimitive -> if (v.isString) v.content else ""
        else -> ""
    }

    private val images = listOf("image/png", "image/jpeg", "image/gif", "image/webp")

    /** The one representation of a rich output the viewer shows, best first (as Jupyter's own front ends choose). */
    private fun rich(data: JsonObject, more: (Int) -> String, paddingBudget: PaddingBudget): Output? {
        for (mime in images) data[mime]?.let { return Output.Image(mime, str(it).replace(whitespace, "")) }
        data["text/html"]?.let { return Output.Html(htmlText(str(it), more, MAX_OUTPUT_LINES, paddingBudget)) }
        data["text/markdown"]?.let { return Output.Markdown(str(it)) }
        data["text/latex"]?.let { v ->
            val tex = str(v).trim()
            return Output.Markdown(if (tex.startsWith("$")) tex else "$$\n$tex\n$$")
        }
        data["application/json"]?.let { return Output.Text(capLines(prettyJson.encodeToString(JsonElement.serializer(), it), more)) }
        data["text/plain"]?.let { return Output.Text(capLines(stripAnsi(str(it)), more)) }
        return null
    }

    private val prettyJson = Json { prettyPrint = true }

    /** nbformat 3 keeps a rich output's representations on the output itself, under short names (png, html, text…). */
    private val v3Keys = mapOf("png" to "image/png", "jpeg" to "image/jpeg", "gif" to "image/gif", "html" to "text/html",
        "markdown" to "text/markdown", "latex" to "text/latex", "json" to "application/json", "text" to "text/plain")

    private fun v3Bundle(o: JsonObject) = JsonObject(o.mapNotNull { (k, v) -> (v3Keys[k] ?: k.takeIf { '/' in it })?.let { it to v } }.toMap())

    private fun output(o: JsonObject, more: (Int) -> String, paddingBudget: PaddingBudget): Output? = when (str(o["output_type"])) {
        // v3 names the stream "stream", v4 "name"; ANSI stripping and line capping run after consecutive chunks merge.
        "stream" -> Output.Stream(str(o["name"] ?: o["stream"]) == "stderr", str(o["text"]))
        "execute_result", "display_data" -> rich(o["data"] as? JsonObject ?: v3Bundle(o), more, paddingBudget)
        "pyout" -> rich(v3Bundle(o), more, paddingBudget)
        "error", "pyerr" -> Output.Error(str(o["ename"]), str(o["evalue"]),
            // whole: a traceback cut short loses the line that failed
            stripAnsi((o["traceback"] as? JsonArray)?.joinToString("\n") { str(it) } ?: str(o["traceback"])))
        else -> null
    }

    /** Consecutive stream outputs of the same name, as Jupyter shows them: one block, capped once merged. */
    private fun mergeStreams(out: List<Output>, more: (Int) -> String): List<Output> {
        val merged = ArrayList<Output>()
        var i = 0
        while (i < out.size) {
            val first = out[i]
            if (first !is Output.Stream) {
                merged.add(first)
                i++
                continue
            }
            val text = StringBuilder(first.text)
            var j = i + 1
            while (j < out.size) {
                val next = out[j] as? Output.Stream ?: break
                if (next.stderr != first.stderr) break
                text.append(next.text)
                j++
            }
            merged.add(first.copy(text = capLines(stripAnsi(text.toString()), more)))
            i = j
        }
        return merged
    }

    /**
     * A notebook's JSON into cells; throws [Invalid] with a readable reason for what is not one. [more] says how many
     * lines of a long output were left out ("… 3 more lines not shown").
     */
    fun parse(text: String, more: (Int) -> String): Notebook {
        val nb = try {
            json.parseToJsonElement(text)
        } catch (e: Exception) {
            throw Invalid(R.string.files_notebook_invalid_json)
        } as? JsonObject ?: throw Invalid(R.string.files_notebook_invalid)
        val meta = nb["metadata"] as? JsonObject ?: JsonObject(emptyMap())
        val kernelspec = meta["kernelspec"] as? JsonObject ?: JsonObject(emptyMap())
        val langInfo = meta["language_info"] as? JsonObject ?: JsonObject(emptyMap())
        val language = (str(kernelspec["language"]).ifEmpty { null } ?: str(langInfo["name"]).ifEmpty { null } ?: str(meta["language"]).ifEmpty { "python" })
            .lowercase(Locale.ROOT)
        val raw = when {
            nb["cells"] is JsonArray -> nb["cells"] as JsonArray
            nb["worksheets"] is JsonArray -> ((nb["worksheets"] as JsonArray).firstOrNull() as? JsonObject)?.get("cells") as? JsonArray ?: JsonArray(emptyList())
            else -> throw Invalid(R.string.files_notebook_no_cells)
        }
        val paddingBudget = PaddingBudget()
        val cells = raw.mapNotNull { it as? JsonObject }.map { c ->
            val kind = str(c["cell_type"])
            val type = when (kind) { "code" -> CellType.Code; "markdown", "heading" -> CellType.Markdown; else -> CellType.Raw }
            var source = str(c["source"] ?: c["input"])
            if (kind == "heading") source = "#".repeat(((c["level"] as? JsonPrimitive)?.intOrNull ?: 1).coerceIn(1, 6)) + " " + source
            val attachments = (c["attachments"] as? JsonObject).orEmpty().mapNotNull { (name, bundle) ->
                ((bundle as? JsonObject)?.let { b -> rich(b, more, paddingBudget) } as? Output.Image)?.let { name to it.base64 }
            }.toMap()
            val outputs = if (type == CellType.Code) mergeStreams((c["outputs"] as? JsonArray).orEmpty().mapNotNull { (it as? JsonObject)?.let { o -> output(o, more, paddingBudget) } }, more) else emptyList()
            val count = ((c["execution_count"] ?: c["prompt_number"]) as? JsonPrimitive)?.intOrNull
            Cell(type, source, count, outputs, attachments)
        }
        val format = "${(nb["nbformat"] as? JsonPrimitive)?.contentOrNull ?: "?"}.${(nb["nbformat_minor"] as? JsonPrimitive)?.contentOrNull ?: "0"}"
        return Notebook(cells, language, str(kernelspec["display_name"]).ifEmpty { null }, format)
    }
}
