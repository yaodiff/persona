package persona.feature.files

import org.xml.sax.Attributes
import org.xml.sax.SAXException
import org.xml.sax.helpers.DefaultHandler

/** A document that can't be read for comparing: what to say. */
internal class CompareFailure(val reason: Int) : Exception()

/**
 * A document's text cut into the units a person thinks in, for comparing two versions (web `lib/compare.ts` readers,
 * iOS `DocumentText`): a Word document by page (at its page breaks; a table row one line, its cells joined; deleted
 * runs left out), a deck by slide in the deck's order with its notes, a workbook by sheet. PDF is read on the device
 * (`PdfText`), not here.
 */
internal object DocumentText {
    /** Total extracted text characters kept per document for comparison (~16 MB UTF-16). */
    private const val MAX_COMPARE_CHARS = 8_000_000L
    /** Empty trailing padding cells added per short XLSX row so sparse far-right cells (e.g. XFD1) cannot amplify RAM. */
    private const val MAX_SHEET_PAD_COLUMNS = 128
    /** Maximum slides expanded from a deck's `presentation.xml` (guards against repeated `<p:sldId>` amplification). */
    private const val MAX_SLIDES = 2_000
    /** Maximum `<Relationship>` entries parsed from a single `.rels` part. */
    private const val MAX_RELATIONSHIPS = 10_000

    /** Stackless sentinel to abort SAX parsing immediately once [MAX_COMPARE_CHARS] or structural caps are exceeded. */
    private class CompareTooBigException : SAXException() {
        override fun fillInStackTrace(): Throwable = this
    }

    /** Stackless sentinel to stop streaming a slide's `.rels` part as soon as its first notesSlide target is found. */
    private class NotesSlideFoundException : SAXException() {
        override fun fillInStackTrace(): Throwable = this
    }

    fun units(ext: String, bytes: ByteArray, names: UnitNames, maxChars: Long = MAX_COMPARE_CHARS): List<DocUnit> = try {
        when (ext.lowercase()) {
            "docx" -> docx(Xlsx.unzip(bytes, "word/"), names, maxChars)
            "pptx" -> pptx(Xlsx.unzip(bytes, "ppt/"), names, maxChars)
            // A row's cells joined like a table's (each row padded to the sheet's width, capped), so a changed cell shows on its own.
            else -> xlsx(bytes, names, maxChars)
        }
    } catch (failure: SheetFailure) {
        throw CompareFailure(if (failure.reason == R.string.files_sheet_too_big) R.string.files_compare_too_big_file else R.string.files_compare_unreadable)
    }

    private fun xlsx(bytes: ByteArray, names: UnitNames, maxChars: Long): List<DocUnit> {
        var totalChars = 0L
        return Xlsx.read(bytes).map { sheet ->
            val padWidth = minOf(sheet.columns, MAX_SHEET_PAD_COLUMNS)
            val lines = ArrayList<String>()
            for (row in sheet.rows) {
                if (row.none(String::isNotBlank)) continue
                val width = maxOf(row.size, padWidth)
                var lineLen = maxOf(0, width - 1).toLong() * DocumentCompare.ROW_SEP.length
                for (cell in row) lineLen += cell.length
                if (totalChars + lineLen > maxChars) throw CompareFailure(R.string.files_compare_too_big_file)
                val sb = StringBuilder(lineLen.toInt())
                for (c in 0 until width) {
                    if (c > 0) sb.append(DocumentCompare.ROW_SEP)
                    if (c < row.size) sb.append(row[c])
                }
                lines.add(sb.toString())
                totalChars += lineLen
            }
            DocUnit(names.sheet(sheet.name), lines)
        }
    }

    private fun docx(parts: Map<String, ByteArray>, names: UnitNames, maxChars: Long): List<DocUnit> {
        val xml = parts["word/document.xml"] ?: throw CompareFailure(R.string.files_compare_unreadable)
        val pages = mutableListOf(mutableListOf<String>())
        // A page break seen: the next line starts a page (Word's explicit break and the rendered one after it are one).
        var newPage = false
        var totalChars = 0L
        var tooBig = false
        Xml.parse(xml, object : DefaultHandler() {
            var tables = 0
            var paragraphs = 0 // nested: a text box's paragraphs are inside their paragraph
            var cellParagraphs = 0 // separate counter inside table cells so paragraphs stay space-joined
            var runs = 0
            var skipped = 0 // inside w:del / w:instrText / mc:Fallback (a copy of the text box above it)
            val text = StringBuilder()
            var row: MutableList<String>? = null
            var rowChars = 0L
            val cell = StringBuilder()
            val cellPara = StringBuilder()
            var inText = false

            fun here() = if (tables > 0) cellPara else text

            fun requireBudget(extra: Int) {
                if (totalChars + rowChars + cell.length + cellPara.length + text.length + extra > maxChars) {
                    tooBig = true
                    throw CompareTooBigException()
                }
            }

            fun add(line: String) {
                requireBudget(line.length)
                totalChars += line.length
                if (newPage && pages.last().isNotEmpty()) pages += mutableListOf<String>()
                newPage = false
                pages.last() += line
            }

            /** A page break inside a paragraph: what came before it stays on this page. */
            fun breakPage() {
                if (tables > 0 || skipped > 0) return
                val line = if (text.isNotBlank()) text.toString().trimEnd() else null
                text.clear()
                if (line != null) add(line)
                newPage = true
            }

            fun flushCellParagraph() {
                val part = cellPara.toString().trim()
                cellPara.clear()
                if (part.isNotEmpty()) {
                    val addLen = (if (cell.isNotEmpty()) 1 else 0) + part.length
                    requireBudget(addLen)
                    if (cell.isNotEmpty()) cell.append(' ')
                    cell.append(part)
                }
            }

            override fun startElement(uri: String?, localName: String?, qName: String, attributes: Attributes) {
                when (Xml.local(qName)) {
                    "tbl" -> tables++
                    "tr" -> if (tables == 1) { row = mutableListOf(); rowChars = 0L }
                    "tc" -> if (tables == 1) { cell.clear(); cellPara.clear(); cellParagraphs = 0 }
                    "p" -> if (tables == 0) {
                        paragraphs++
                        if (paragraphs == 1) text.clear()
                    } else {
                        cellParagraphs++
                        if (cellParagraphs == 1) cellPara.clear()
                    }
                    "r" -> runs++
                    "del", "instrText", "Fallback" -> skipped++
                    "t" -> inText = skipped == 0
                    // w:tab in a run is a tab; in w:pPr/w:tabs it is a tab stop's definition.
                    "tab" -> if (skipped == 0 && runs > 0) {
                        requireBudget(1)
                        here().append('\t')
                    }
                    "br" -> if (attributes.localValue("type") == "page") breakPage()
                    "lastRenderedPageBreak" -> breakPage()
                }
            }

            override fun characters(ch: CharArray, start: Int, length: Int) {
                if (!inText || length <= 0) return
                requireBudget(length)
                here().appendRange(ch, start, start + length)
            }

            override fun endElement(uri: String?, localName: String?, qName: String) {
                when (Xml.local(qName)) {
                    "t" -> inText = false
                    "r" -> if (runs > 0) runs--
                    "del", "instrText", "Fallback" -> if (skipped > 0) skipped--
                    "tc" -> if (tables == 1) {
                        flushCellParagraph()
                        val cellText = cell.toString().trim()
                        cell.clear()
                        row?.let { r ->
                            val addLen = (if (r.isNotEmpty()) DocumentCompare.ROW_SEP.length else 0) + cellText.length
                            requireBudget(addLen)
                            r.add(cellText)
                            rowChars += addLen
                        }
                    }
                    "tr" -> if (tables == 1) {
                        val finished = row
                        row = null
                        rowChars = 0L
                        finished?.takeIf { cells -> cells.any { it.isNotEmpty() } }?.let { add(it.joinToString(DocumentCompare.ROW_SEP)) }
                    }
                    "tbl" -> if (tables > 0) {
                        tables--
                        if (tables == 0) {
                            row = null; rowChars = 0L
                            cell.clear(); cellPara.clear(); cellParagraphs = 0
                        }
                    }
                    "p" -> if (tables == 0) {
                        if (paragraphs > 0) paragraphs--
                        if (paragraphs == 0) {
                            val line = if (text.isNotBlank()) text.toString().trimEnd() else null
                            text.clear()
                            if (line != null) add(line)
                        }
                    } else {
                        if (cellParagraphs > 0) cellParagraphs--
                        if (cellParagraphs == 0) flushCellParagraph()
                    }
                }
            }
        })
        if (tooBig) throw CompareFailure(R.string.files_compare_too_big_file)
        // Numbered once the empty pages are gone: a break with nothing after it doesn't skip a number.
        return pages.filterIndexed { i, lines -> lines.isNotEmpty() || i == 0 }.mapIndexed { i, lines -> DocUnit(names.page(i + 1), lines) }
    }

    private fun pptx(parts: Map<String, ByteArray>, names: UnitNames, maxChars: Long): List<DocUnit> {
        val presentation = parts["ppt/presentation.xml"] ?: throw CompareFailure(R.string.files_compare_unreadable)
        val targets = relationships(parts, "ppt/presentation.xml")
        val slides = ArrayList<String>()
        var tooManySlides = false
        Xml.parse(presentation, object : DefaultHandler() {
            override fun startElement(uri: String?, localName: String?, qName: String, attributes: Attributes) {
                if (Xml.local(qName) != "sldId") return
                val path = attributes.getValue("r:id")?.let(targets::get)?.first ?: return
                if (slides.size >= MAX_SLIDES) { tooManySlides = true; throw CompareTooBigException() }
                slides += path
            }
        })
        if (tooManySlides) throw CompareFailure(R.string.files_compare_too_big_file)
        val slideCache = HashMap<String, Pair<List<String>, Long>>()
        val notesCache = HashMap<String, Pair<List<String>, Long>>()
        var totalChars = 0L
        return slides.mapIndexed { i, path ->
            val (lines, chars) = slideCache.getOrPut(path) {
                val budget = (maxChars - totalChars).coerceAtLeast(0L)
                val body = parts[path]?.let { slideLines(it, budget) }.orEmpty()
                val bodyChars = body.sumOf { it.length.toLong() }
                val notesPath = notesSlidePath(parts, path)
                val (noteLines, noteChars) = if (notesPath != null) {
                    notesCache.getOrPut(notesPath) {
                        val parsed = parts[notesPath]?.let { slideLines(it, (budget - bodyChars).coerceAtLeast(0L)) }
                            .orEmpty().map { it.trimStart() }
                        parsed to parsed.sumOf { it.length.toLong() }
                    }
                } else {
                    emptyList<String>() to 0L
                }
                val combinedChars = bodyChars + if (noteLines.isNotEmpty()) names.notes.length.toLong() + noteChars else 0L
                if (totalChars + combinedChars > maxChars) throw CompareFailure(R.string.files_compare_too_big_file)
                val combined = if (noteLines.isNotEmpty()) body + listOf(names.notes) + noteLines else body
                combined to combinedChars
            }
            totalChars += chars
            if (totalChars > maxChars) throw CompareFailure(R.string.files_compare_too_big_file)
            DocUnit(names.slide(i + 1), lines)
        }
    }

    /** OOXML `ST_TextIndentLevelType` restricts paragraph indentation levels to 0..8. */
    private const val MAX_INDENT_LEVEL = 8

    private fun Attributes.localValue(localName: String): String? =
        (0 until length).firstOrNull { Xml.local(getQName(it)) == localName }?.let(::getValue)

    /** A slide's (or its notes') text: each paragraph's lines, indented by level; a table row one line. */
    private fun slideLines(xml: ByteArray, maxChars: Long = MAX_COMPARE_CHARS): List<String> {
        val out = ArrayList<String>()
        var chars = 0L
        var tooBig = false
        Xml.parse(xml, object : DefaultHandler() {
            val para = StringBuilder()
            var level = 0
            var inText = false
            var row: MutableList<String>? = null
            var rowChars = 0L
            var cell: StringBuilder? = null

            fun requireBudget(extra: Int) {
                if (chars + rowChars + (cell?.length ?: 0) + para.length + extra > maxChars) {
                    tooBig = true
                    throw CompareTooBigException()
                }
            }

            fun emit(line: String) {
                requireBudget(line.length)
                chars += line.length
                out += line
            }

            override fun startElement(uri: String?, localName: String?, qName: String, attributes: Attributes) {
                when (Xml.local(qName)) {
                    "p" -> { para.clear(); level = 0 }
                    "pPr" -> level = (attributes.getValue("lvl")?.toIntOrNull() ?: 0).coerceIn(0, MAX_INDENT_LEVEL)
                    "t" -> inText = true
                    "br" -> { requireBudget(1); para.append('\n') }
                    "tr" -> { row = mutableListOf(); rowChars = 0L }
                    "tc" -> cell = StringBuilder()
                }
            }

            override fun characters(ch: CharArray, start: Int, length: Int) {
                if (!inText || length <= 0) return
                requireBudget(length)
                para.appendRange(ch, start, start + length)
            }

            override fun endElement(uri: String?, localName: String?, qName: String) {
                when (Xml.local(qName)) {
                    "t" -> inText = false
                    "p" -> {
                        val c = cell
                        if (c != null) {
                            val part = para.toString().trim()
                            para.clear()
                            if (part.isNotEmpty()) {
                                val addLen = (if (c.isNotEmpty()) 1 else 0) + part.length
                                requireBudget(addLen)
                                if (c.isNotEmpty()) c.append(' ')
                                c.append(part)
                            }
                        } else {
                            val text = para.toString()
                            para.clear()
                            val indent = "  ".repeat(level)
                            var start = 0
                            while (start <= text.length) {
                                val nl = text.indexOf('\n', start)
                                val rawEnd = if (nl < 0) text.length else nl
                                val lineEnd = if (rawEnd > start && text[rawEnd - 1] == '\r') rawEnd - 1 else rawEnd
                                var trimmedEnd = lineEnd
                                while (trimmedEnd > start && text[trimmedEnd - 1].isWhitespace()) trimmedEnd--
                                var nonBlank = false
                                for (idx in start until trimmedEnd) {
                                    if (!text[idx].isWhitespace()) { nonBlank = true; break }
                                }
                                if (nonBlank) emit(indent + text.substring(start, trimmedEnd))
                                if (nl < 0) break
                                start = nl + 1
                            }
                        }
                    }
                    "tc" -> {
                        val cellText = cell?.toString().orEmpty()
                        cell = null
                        row?.let { r ->
                            val addLen = (if (r.isNotEmpty()) DocumentCompare.ROW_SEP.length else 0) + cellText.length
                            requireBudget(addLen)
                            r.add(cellText)
                            rowChars += addLen
                        }
                    }
                    "tr" -> {
                        val finished = row
                        row = null
                        rowChars = 0L
                        finished?.takeIf { it.any(String::isNotEmpty) }?.let { emit(it.joinToString(DocumentCompare.ROW_SEP)) }
                    }
                }
            }
        })
        if (tooBig) throw CompareFailure(R.string.files_compare_too_big_file)
        return out
    }

    /** A part's relationships: id → (the target part's path, the relationship's type), streamed with hard bounds. */
    private fun relationships(parts: Map<String, ByteArray>, part: String): Map<String, Pair<String, String>> {
        val folder = part.substringBeforeLast('/', "")
        val rels = parts["$folder/_rels/${part.substringAfterLast('/')}.rels"] ?: return emptyMap()
        val out = HashMap<String, Pair<String, String>>()
        var seen = 0
        var relChars = 0L
        var tooBig = false
        Xml.parse(rels, object : DefaultHandler() {
            override fun startElement(uri: String?, localName: String?, qName: String, attributes: Attributes) {
                if (Xml.local(qName) != "Relationship") return
                val id = attributes.getValue("Id") ?: return
                val target = attributes.getValue("Target") ?: return
                val type = attributes.getValue("Type").orEmpty()
                seen++
                relChars += id.length + target.length + type.length
                if (seen > MAX_RELATIONSHIPS || relChars > MAX_COMPARE_CHARS) {
                    tooBig = true
                    throw CompareTooBigException()
                }
                val normalized = Xlsx.normalizePath(if (target.startsWith("/")) target.drop(1) else "$folder/$target")
                out[id] = normalized to type
            }
        })
        if (tooBig) throw CompareFailure(R.string.files_compare_too_big_file)
        return out
    }

    /** Stream a slide's `.rels` part and stop as soon as the first `/notesSlide` relationship is seen. */
    private fun notesSlidePath(parts: Map<String, ByteArray>, part: String): String? {
        val folder = part.substringBeforeLast('/', "")
        val rels = parts["$folder/_rels/${part.substringAfterLast('/')}.rels"] ?: return null
        var seen = 0
        var relChars = 0L
        var tooBig = false
        var found: String? = null
        Xml.parse(rels, object : DefaultHandler() {
            override fun startElement(uri: String?, localName: String?, qName: String, attributes: Attributes) {
                if (Xml.local(qName) != "Relationship") return
                val id = attributes.getValue("Id") ?: return
                val target = attributes.getValue("Target") ?: return
                val type = attributes.getValue("Type").orEmpty()
                seen++
                relChars += id.length + target.length + type.length
                if (seen > MAX_RELATIONSHIPS || relChars > MAX_COMPARE_CHARS) {
                    tooBig = true
                    throw CompareTooBigException()
                }
                if (type.endsWith("/notesSlide")) {
                    found = Xlsx.normalizePath(if (target.startsWith("/")) target.drop(1) else "$folder/$target")
                    throw NotesSlideFoundException()
                }
            }
        })
        if (tooBig) throw CompareFailure(R.string.files_compare_too_big_file)
        return found
    }

    fun linesOf(text: String): List<String> = text.split(Regex("\r?\n")).map { it.trimEnd() }.filter { it.isNotBlank() }
}
