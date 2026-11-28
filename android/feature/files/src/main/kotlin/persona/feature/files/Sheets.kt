package persona.feature.files

import androidx.annotation.StringRes
import org.xml.sax.Attributes
import org.xml.sax.InputSource
import org.xml.sax.SAXException
import org.xml.sax.helpers.DefaultHandler
import java.io.ByteArrayInputStream
import java.io.ByteArrayOutputStream
import java.time.LocalDateTime
import java.util.Locale
import java.util.zip.ZipInputStream
import javax.xml.parsers.SAXParserFactory
import kotlin.math.abs
import kotlin.math.roundToLong

/**
 * One sheet of a table file (docs/android/android.md "Files and Git in chat"): its cells as text, row by row; rows can be shorter than
 * [columns]. The same model as iOS (`TableParsing.swift`) and the web (`lib/sheets.ts`).
 */
internal class TableSheet(val name: String, private val maxRows: Int = MAX_ROWS) {
    val rows = ArrayList<List<String>>()
    var columns = 0
        private set
    /** Rows past [MAX_ROWS] were left out. */
    var capped = false
        private set

    fun append(more: List<List<String>>) {
        val room = maxRows - rows.size
        if (more.size > room) capped = true
        for (row in more.take(room.coerceAtLeast(0))) {
            rows.add(row)
            columns = maxOf(columns, row.size)
        }
    }

    internal fun copyAs(newName: String): TableSheet = TableSheet(newName, maxRows).also { copy ->
        copy.rows.addAll(rows)
        copy.columns = columns
        copy.capped = capped
    }

    operator fun get(row: Int, column: Int): String = rows.getOrNull(row)?.getOrNull(column) ?: ""

    /** A row as tab-separated text (what "Copy row" puts on the clipboard). */
    fun rowText(row: Int): String = rows.getOrNull(row).orEmpty().joinToString("\t")

    companion object {
        /** More rows than this are not kept (a sheet this long is not read on a phone). */
        const val MAX_ROWS = 200_000

        /** The spreadsheet name of a column: A … Z, AA … */
        fun columnName(index: Int): String {
            val name = StringBuilder()
            var n = index + 1
            while (n > 0) {
                name.insert(0, ('A' + (n - 1) % 26))
                n = (n - 1) / 26
            }
            return name.toString()
        }
    }
}

/** Which files the table viewer opens: CSV / TSV as text, xlsx as a workbook. Old .xls and Numbers stay described. */
internal object TableFiles {
    private val delimited = setOf("csv", "tsv", "tab")
    private val workbooks = setOf("xlsx", "xlsm")

    fun isDelimited(name: String) = FilesPaths.extension(name) in delimited
    fun isTabSeparated(name: String) = FilesPaths.extension(name) in setOf("tsv", "tab")
    fun isWorkbook(name: String) = FilesPaths.extension(name) in workbooks
}

/**
 * CSV / TSV (RFC 4180: quoted fields with "" for a quote, separators and line breaks inside quotes). Blank lines are
 * skipped, a lone CR is ignored. Values stay exactly as written: no number or date guessing.
 */
internal object Delimited {
    /**
     * The separator for a file: tab for .tsv / .tab, else the commonest of comma, semicolon (what spreadsheets write
     * where the comma is the decimal mark) and tab on the first line; comma when there are none.
     */
    fun separator(name: String, text: String): Char {
        if (TableFiles.isTabSeparated(name)) return '\t'
        val first = text.take(4096).substringBefore('\n')
        return listOf(',', ';', '\t').maxBy { c -> first.count { it == c } }.takeIf { c -> first.contains(c) } ?: ','
    }

    fun parse(text: String, separator: Char, maxRows: Int = TableSheet.MAX_ROWS + 1): List<List<String>> {
        val out = ArrayList<List<String>>()
        val field = StringBuilder()
        var row = ArrayList<String>()
        var inQuotes = false
        var quotePending = false
        var quoted = false
        fun endField() { row.add(field.toString()); field.setLength(0); quoted = false }
        fun endRow() { if (!(row.size == 1 && row[0].isEmpty())) out.add(row); row = ArrayList() }
        var i = if (text.startsWith('\uFEFF')) 1 else 0
        while (i < text.length && out.size < maxRows) {
            val c = text[i++]
            if (inQuotes) {
                if (quotePending) {
                    quotePending = false
                    if (c == '"') { field.append('"'); continue }
                    inQuotes = false // the closing quote: this character is outside the quotes
                } else if (c == '"') { quotePending = true; continue } else { field.append(c); continue }
            }
            when {
                c == '"' && field.isEmpty() && !quoted -> { inQuotes = true; quoted = true }
                c == separator -> endField()
                c == '\n' -> { endField(); endRow() }
                c == '\r' -> Unit
                else -> field.append(c)
            }
        }
        if (out.size < maxRows && (field.isNotEmpty() || row.isNotEmpty() || quoted)) { endField(); endRow() }
        return out
    }

    /** A delimited file as the one sheet it is. */
    fun sheet(name: String, text: String): TableSheet =
        TableSheet(name.substringBeforeLast('.')).apply { append(parse(text, separator(name, text))) }
}

/** A workbook that can't be read: what to say. */
internal class SheetFailure(@StringRes val reason: Int) : Exception()

/**
 * An Excel workbook (.xlsx, Office Open XML): each sheet's cells as the text Excel would show for the plain cases:
 * shared and inline strings, numbers without float noise, TRUE / FALSE, errors, and dates for the date formats.
 */
internal object Xlsx {
    /** Bytes inflated per workbook at most (a zip bomb stops here instead of filling the phone's memory). */
    private const val MAX_INFLATED = 96L shl 20
    /** Maximum worksheets expanded from `xl/workbook.xml`. */
    private const val MAX_SHEETS = 256
    /** Maximum `<Relationship>` entries parsed from `xl/_rels/workbook.xml.rels`. */
    private const val MAX_RELATIONSHIPS = 10_000
    /** Maximum single relationship `Target` length before calling [normalizePath] (prevents `split('/')` spikes). */
    private const val MAX_REL_TARGET_LENGTH = 4_096
    /** Maximum `<si>` entries parsed from `xl/sharedStrings.xml`. */
    private const val MAX_SHARED_STRINGS = 1_000_000
    /** Maximum `<xf>` / `<numFmt>` entries parsed from `xl/styles.xml`. */
    private const val MAX_STYLE_ENTRIES = 100_000
    /** Maximum Excel columns per row (`XFD`, 0-based `16_383`). */
    private const val MAX_COLUMNS = 16_384
    /** Maximum retained cell-array slots across an entire workbook (~16–32 MB of object references). */
    internal const val MAX_WORKBOOK_SLOTS = 4_000_000L
    /** Maximum retained characters across an entire workbook (~32 MB UTF-16). */
    internal const val MAX_WORKBOOK_CHARS = 16_000_000L

    /** Stackless sentinel to abort SAX parsing immediately once a structural or memory cap is exceeded. */
    private class SheetTooBigException : SAXException() {
        override fun fillInStackTrace(): Throwable = this
    }

    internal class WorkbookBudget(
        val maxSlots: Long = MAX_WORKBOOK_SLOTS,
        val maxChars: Long = MAX_WORKBOOK_CHARS,
        val maxSheets: Int = MAX_SHEETS,
        val maxSharedStrings: Int = MAX_SHARED_STRINGS,
        val maxStyleEntries: Int = MAX_STYLE_ENTRIES,
    ) {
        var slots: Long = 0L
        var chars: Long = 0L
    }

    private data class SheetRef(val name: String, val rId: String?)
    private data class CachedSheet(val sheet: TableSheet, val slots: Long, val chars: Long)

    fun read(bytes: ByteArray): List<TableSheet> = read(bytes, WorkbookBudget())

    internal fun read(bytes: ByteArray, budget: WorkbookBudget): List<TableSheet> {
        val files = unzip(bytes)
        val workbook = files["xl/workbook.xml"] ?: throw SheetFailure(R.string.files_sheet_no_workbook)
        val sheets = sheetRefs(workbook, budget)
        val targets = files["xl/_rels/workbook.xml.rels"]?.let { workbookRelationships(it, budget) }.orEmpty()
        val strings = files["xl/sharedStrings.xml"]?.let { sharedStrings(it, budget) }.orEmpty()
        val dates = files["xl/styles.xml"]?.let { dateStyles(it, budget) }.orEmpty()
        val cache = HashMap<String, CachedSheet>()
        val out = sheets.mapIndexedNotNull { i, s ->
            val path = s.rId?.let(targets::get) ?: "xl/worksheets/sheet${i + 1}.xml"
            val cached = cache[path]
            if (cached != null) {
                if (cached.slots > budget.maxSlots - budget.slots || cached.chars > budget.maxChars - budget.chars) {
                    throw SheetFailure(R.string.files_sheet_too_big)
                }
                budget.slots += cached.slots
                budget.chars += cached.chars
                cached.sheet.copyAs(s.name)
            } else {
                val xml = files[path] ?: return@mapIndexedNotNull null
                val slotsBefore = budget.slots
                val charsBefore = budget.chars
                val parsed = cells(xml, s.name, strings, dates, TableSheet.MAX_ROWS, budget)
                cache[path] = CachedSheet(parsed, budget.slots - slotsBefore, budget.chars - charsBefore)
                parsed
            }
        }
        if (out.isEmpty()) throw SheetFailure(R.string.files_sheet_no_sheets)
        return out
    }

    private fun sheetRefs(xml: ByteArray, budget: WorkbookBudget): List<SheetRef> {
        val out = ArrayList<SheetRef>()
        var metaChars = 0L
        var tooBig = false
        Xml.parse(xml, object : DefaultHandler() {
            override fun startElement(uri: String?, localName: String?, qName: String, attributes: Attributes) {
                if (Xml.local(qName) != "sheet") return
                if (out.size >= budget.maxSheets) { tooBig = true; throw SheetTooBigException() }
                val name = attributes.getValue("name") ?: "Sheet${out.size + 1}"
                val rId = attributes.getValue("r:id")
                val entryChars = name.length.toLong() + (rId?.length ?: 0).toLong()
                if (entryChars > budget.maxChars - metaChars) { tooBig = true; throw SheetTooBigException() }
                metaChars += entryChars
                out.add(SheetRef(name, rId))
            }
        })
        if (tooBig) throw SheetFailure(R.string.files_sheet_too_big)
        return out
    }

    private fun workbookRelationships(xml: ByteArray, budget: WorkbookBudget): Map<String, String> {
        val out = HashMap<String, String>()
        var seen = 0
        var relChars = 0L
        var tooBig = false
        Xml.parse(xml, object : DefaultHandler() {
            override fun startElement(uri: String?, localName: String?, qName: String, attributes: Attributes) {
                if (Xml.local(qName) != "Relationship") return
                val id = attributes.getValue("Id") ?: return
                val target = attributes.getValue("Target") ?: return
                seen++
                val entryChars = id.length.toLong() + target.length.toLong()
                if (seen > MAX_RELATIONSHIPS || target.length > MAX_REL_TARGET_LENGTH || entryChars > budget.maxChars - relChars) {
                    tooBig = true
                    throw SheetTooBigException()
                }
                relChars += entryChars
                out[id] = normalizePath(if (target.startsWith("/")) target.drop(1) else "xl/$target")
            }
        })
        if (tooBig) throw SheetFailure(R.string.files_sheet_too_big)
        return out
    }

    /** A part's name with "." and ".." resolved ("xl/../xl/worksheets/a.xml" → "xl/worksheets/a.xml"). */
    fun normalizePath(path: String): String {
        val parts = ArrayList<String>()
        for (seg in path.split('/')) when (seg) { "", "." -> Unit; ".." -> parts.removeLastOrNull(); else -> parts.add(seg) }
        return parts.joinToString("/")
    }

    /** The parts of an Office archive a reader uses (the XML under [folder]), inflated; the comparing reads word/ and ppt/. */
    internal fun unzip(bytes: ByteArray, folder: String = "xl/"): Map<String, ByteArray> {
        val out = HashMap<String, ByteArray>()
        var total = 0L
        try {
            ZipInputStream(ByteArrayInputStream(bytes)).use { zip ->
                val buffer = ByteArray(64 * 1024)
                while (true) {
                    val entry = zip.nextEntry ?: break
                    if (entry.isDirectory || !entry.name.startsWith(folder) || !entry.name.endsWith(".xml") && !entry.name.endsWith(".rels")) continue
                    val data = ByteArrayOutputStream()
                    while (true) {
                        val n = zip.read(buffer)
                        if (n < 0) break
                        total += n
                        if (total > MAX_INFLATED) throw SheetFailure(R.string.files_sheet_too_big)
                        data.write(buffer, 0, n)
                    }
                    out[entry.name] = data.toByteArray()
                }
            }
        } catch (e: SheetFailure) {
            throw e
        } catch (e: Exception) {
            if (out.isEmpty()) throw SheetFailure(R.string.files_sheet_not_xlsx)
        }
        if (out.isEmpty()) throw SheetFailure(R.string.files_sheet_not_xlsx)
        return out
    }

    /** A number as Excel shows it in a General cell: integers without ".0", others to 15 significant digits. */
    fun number(raw: String): String {
        val v = raw.toDoubleOrNull()?.takeIf { it.isFinite() } ?: return raw
        if (v == Math.rint(v) && abs(v) < 1e15) return v.toLong().toString()
        var s = String.format(Locale.ROOT, "%.15g", v)
        if (s.contains('e')) {
            // %g writes e+05; Excel writes E+05 with the mantissa's trailing zeros dropped.
            val (m, e) = s.split('e')
            return m.trimEnd('0').trimEnd('.') + "E" + e
        }
        if (s.contains('.')) s = s.trimEnd('0').trimEnd('.')
        return s
    }

    /**
     * An Excel date serial (the 1900 system) as yyyy-MM-dd, with the time when it has one. Excel counts a 1900-02-29
     * that never was (serial 60, after Lotus 1-2-3): days from 61 on count from 1899-12-30, earlier ones from 12-31.
     */
    fun date(raw: String): String {
        val v = raw.toDoubleOrNull()?.takeIf { it.isFinite() && it >= 0 && it < 2_958_466 } ?: return raw
        val totalSeconds = (v * 86_400).roundToLong()
        val days = totalSeconds / 86_400L
        val secOfDay = totalSeconds % 86_400L
        val t = LocalDateTime.of(2000, 1, 1, 0, 0).plusSeconds(secOfDay)
        if (days == 0L) return time(t)
        val day = if (days == 60L) {
            "1900-02-29"
        } else {
            val d = (if (days < 60L) LocalDateTime.of(1899, 12, 31, 0, 0) else LocalDateTime.of(1899, 12, 30, 0, 0)).plusDays(days)
            "%04d-%02d-%02d".format(Locale.ROOT, d.year, d.monthValue, d.dayOfMonth)
        }
        return if (secOfDay == 0L) day else "$day ${time(t)}"
    }

    private fun time(t: LocalDateTime) =
        if (t.second == 0) "%02d:%02d".format(Locale.ROOT, t.hour, t.minute) else "%02d:%02d:%02d".format(Locale.ROOT, t.hour, t.minute, t.second)

    /** xl/sharedStrings.xml: each `<si>`'s text, its runs joined; phonetic guides (`<rPh>`) left out. */
    private fun sharedStrings(xml: ByteArray, budget: WorkbookBudget): List<String> {
        val strings = ArrayList<String>()
        val current = StringBuilder()
        var seen = 0
        var sstChars = 0L
        var inText = false
        var phonetic = 0
        var tooBig = false
        Xml.parse(xml, object : DefaultHandler() {
            override fun startElement(uri: String?, localName: String?, qName: String, attributes: Attributes) {
                when (Xml.local(qName)) {
                    "si" -> {
                        if (++seen > budget.maxSharedStrings) { tooBig = true; throw SheetTooBigException() }
                        current.setLength(0)
                    }
                    "rPh" -> phonetic++
                    "t" -> inText = phonetic == 0
                }
            }
            override fun endElement(uri: String?, localName: String?, qName: String) {
                when (Xml.local(qName)) {
                    "si" -> {
                        val s = current.toString()
                        current.setLength(0)
                        sstChars += s.length.toLong()
                        strings.add(s)
                    }
                    "rPh" -> if (phonetic > 0) phonetic--
                    "t" -> inText = false
                }
            }
            override fun characters(ch: CharArray, start: Int, length: Int) {
                if (!inText || length <= 0) return
                if (length.toLong() > budget.maxChars - sstChars - current.length.toLong()) { tooBig = true; throw SheetTooBigException() }
                current.appendRange(ch, start, start + length)
            }
        })
        if (tooBig) throw SheetFailure(R.string.files_sheet_too_big)
        return strings
    }

    /** xl/styles.xml: which cell styles (`cellXfs` index) show a number as a date or time. */
    private fun dateStyles(xml: ByteArray, budget: WorkbookBudget): List<Boolean> {
        val custom = HashMap<Int, Boolean>()
        val formats = ArrayList<Int>()
        var seenNumFmts = 0
        var inCellXfs = false
        var styleChars = 0L
        var tooBig = false
        Xml.parse(xml, object : DefaultHandler() {
            override fun startElement(uri: String?, localName: String?, qName: String, attributes: Attributes) {
                when (Xml.local(qName)) {
                    "numFmt" -> {
                        val id = attributes.getValue("numFmtId")?.toIntOrNull()
                        val code = attributes.getValue("formatCode")
                        if (id != null && code != null) {
                            seenNumFmts++
                            val codeLen = code.length.toLong()
                            if (seenNumFmts > budget.maxStyleEntries || codeLen > budget.maxChars - styleChars) {
                                tooBig = true
                                throw SheetTooBigException()
                            }
                            styleChars += codeLen
                            custom[id] = isDateCode(code)
                        }
                    }
                    "cellXfs" -> inCellXfs = true
                    "xf" -> if (inCellXfs) {
                        if (formats.size >= budget.maxStyleEntries) { tooBig = true; throw SheetTooBigException() }
                        formats.add(attributes.getValue("numFmtId")?.toIntOrNull() ?: 0)
                    }
                }
            }
            override fun endElement(uri: String?, localName: String?, qName: String) { if (Xml.local(qName) == "cellXfs") inCellXfs = false }
        })
        if (tooBig) throw SheetFailure(R.string.files_sheet_too_big)
        return formats.map { id -> id in 14..22 || id in 45..47 || custom[id] == true }
    }

    /** A custom number format is a date when it has date letters outside quotes, brackets ([Red], "text"), escapes (\x), and padding (_x, *x). */
    private fun isDateCode(code: String): Boolean {
        var skip: Char? = null
        var i = 0
        while (i < code.length) {
            val ch = code[i++]
            if (skip != null) { if (ch == skip) skip = null; continue }
            when (ch) {
                '"' -> skip = '"'
                '[' -> skip = ']'
                '\\', '_', '*' -> i++
                'y', 'Y', 'm', 'M', 'd', 'D', 'h', 'H', 's', 'S' -> return true
            }
        }
        return false
    }

    /** The column of a reference like "AB12" (0-based), or null. Saturates on extreme letter runs so 32-bit int never wraps. */
    fun column(ref: String): Int? {
        var n = 0
        var any = false
        for (c in ref) {
            if (c !in 'A'..'Z') break
            if (n > (Int.MAX_VALUE - 26) / 26) return Int.MAX_VALUE
            n = n * 26 + (c - 'A' + 1)
            any = true
        }
        return if (any) n - 1 else null
    }

    /** Sentinel thrown once a worksheet hits [TableSheet.MAX_ROWS] so SAX stops immediately without retaining cell text. */
    private class SheetCappedException : SAXException() {
        override fun fillInStackTrace(): Throwable = this
    }

    /** One worksheet's `<sheetData>`: cells placed by their reference (B3), gaps left empty. */
    internal fun cells(
        xml: ByteArray,
        name: String,
        strings: List<String>,
        dates: List<Boolean>,
        maxRows: Int = TableSheet.MAX_ROWS,
        budget: WorkbookBudget = WorkbookBudget(),
    ): TableSheet {
        val sheet = TableSheet(name, maxRows)
        var row = ArrayList<String>()
        var rowChars = 0L
        var rowIndex = -1
        var nextColumn = 0
        var cellColumn = -1
        var cellType = ""
        var cellStyle = 0
        val value = StringBuilder()
        var inValue = false
        var inInline = false
        var phonetic = 0
        var tooBig = false
        fun abortTooBig(): Nothing {
            value.setLength(0)
            inValue = false
            inInline = false
            phonetic = 0
            tooBig = true
            throw SheetTooBigException()
        }
        fun flushRow() {
            if (rowIndex < 0 || sheet.capped) return
            val allocatedSize = row.size
            while (row.lastOrNull() == "") row.removeAt(row.size - 1)
            val rowSlots = maxOf(1, allocatedSize).toLong()
            if (rowSlots > budget.maxSlots - budget.slots || rowChars > budget.maxChars - budget.chars) abortTooBig()
            val stored: List<String> = if (row.isEmpty()) emptyList() else row.also { if (it.size < allocatedSize) it.trimToSize() }
            sheet.append(listOf(stored))
            budget.slots += rowSlots
            budget.chars += rowChars
            row = ArrayList()
            rowChars = 0L
            nextColumn = 0
            cellColumn = -1
        }
        fun stopIfCapped() {
            if (!sheet.capped) return
            value.setLength(0)
            inValue = false
            inInline = false
            phonetic = 0
            throw SheetCappedException()
        }
        Xml.parse(xml, object : DefaultHandler() {
            override fun startElement(uri: String?, localName: String?, qName: String, attributes: Attributes) {
                if (sheet.capped) return
                when (Xml.local(qName)) {
                    "row" -> {
                        flushRow()
                        if (sheet.rows.size >= maxRows) {
                            sheet.append(listOf(emptyList()))
                            stopIfCapped()
                        }
                        val r = attributes.getValue("r")?.toIntOrNull()?.takeIf { it > 0 }?.minus(1) ?: (rowIndex + 1)
                        // Rows the file skips are empty rows (bounded: a row far down doesn't allocate the gap past the cap).
                        if (r > rowIndex + 1) {
                            val gap = r - rowIndex - 1
                            val room = (maxRows - sheet.rows.size).coerceAtLeast(0)
                            val keptGap = minOf(gap, room).toLong()
                            if (keptGap > budget.maxSlots - budget.slots) abortTooBig()
                            sheet.append(List(minOf(gap, room + 1)) { emptyList() })
                            budget.slots += keptGap
                        }
                        if (sheet.rows.size >= maxRows) {
                            if (!sheet.capped) sheet.append(listOf(emptyList()))
                            stopIfCapped()
                        }
                        rowIndex = r
                        nextColumn = 0
                        cellColumn = -1
                    }
                    "c" -> {
                        cellColumn = attributes.getValue("r")?.let(::column) ?: nextColumn
                        nextColumn = if (cellColumn in 0 until MAX_COLUMNS) cellColumn + 1 else MAX_COLUMNS
                        cellType = attributes.getValue("t") ?: ""
                        cellStyle = attributes.getValue("s")?.toIntOrNull() ?: 0
                        value.setLength(0)
                        inValue = false
                        inInline = false
                        phonetic = 0
                    }
                    "v" -> if (cellColumn in 0 until MAX_COLUMNS) inValue = true
                    "is" -> if (cellColumn in 0 until MAX_COLUMNS) { inInline = true; phonetic = 0 }
                    "rPh" -> if (inInline) phonetic++
                    "t" -> if (inInline && phonetic == 0 && cellColumn in 0 until MAX_COLUMNS) inValue = true
                }
            }

            override fun endElement(uri: String?, localName: String?, qName: String) {
                if (sheet.capped) return
                when (Xml.local(qName)) {
                    "v", "t" -> inValue = false
                    "rPh" -> if (inInline && phonetic > 0) phonetic--
                    "is" -> { inInline = false; phonetic = 0 }
                    "c" -> {
                        val col = cellColumn
                        cellColumn = -1
                        if (col !in 0 until MAX_COLUMNS) {
                            value.setLength(0)
                            return
                        }
                        val raw = value.toString()
                        value.setLength(0)
                        val shown = when (cellType) {
                            "s" -> raw.toIntOrNull()?.let(strings::getOrNull) ?: ""
                            "b" -> if (raw == "1") "TRUE" else "FALSE"
                            "str", "inlineStr", "e" -> raw
                            else -> when {
                                raw.isEmpty() -> ""
                                dates.getOrNull(cellStyle) == true -> date(raw)
                                else -> number(raw)
                            }
                        }
                        if (shown.isEmpty() && col >= row.size) return
                        val neededSize = maxOf(row.size, col + 1).toLong()
                        val oldLen = if (col < row.size) row[col].length.toLong() else 0L
                        val nextRowChars = rowChars + (shown.length.toLong() - oldLen)
                        if (neededSize > budget.maxSlots - budget.slots || nextRowChars > budget.maxChars - budget.chars) {
                            abortTooBig()
                        }
                        while (row.size <= col) row.add("")
                        row[col] = shown
                        rowChars = nextRowChars
                    }
                }
            }

            override fun characters(ch: CharArray, start: Int, length: Int) {
                if (sheet.capped || !inValue || length <= 0) return
                if (length.toLong() > budget.maxChars - budget.chars - rowChars - value.length.toLong()) abortTooBig()
                value.appendRange(ch, start, start + length)
            }
        })
        if (tooBig) throw SheetFailure(R.string.files_sheet_too_big)
        try {
            flushRow()
        } catch (_: SheetTooBigException) {
            throw SheetFailure(R.string.files_sheet_too_big)
        }
        return sheet
    }
}

/** SAX over the workbook's XML, with DTDs and external entities off (a workbook is untrusted input). */
internal object Xml {
    fun local(qName: String) = qName.substringAfter(':')

    /**
     * Whether a part declares a DTD or entities. Workbook parts never do; Android's SAX (Expat) ignores the features
     * that would turn DTDs off, so such a part isn't parsed at all (no entity expansion to blow up).
     */
    fun declaresDtd(xml: ByteArray): Boolean {
        val markers = listOf("<!DOCTYPE", "<!ENTITY").map { it.toByteArray() }
        for (i in xml.indices) {
            if (xml[i] != '<'.code.toByte()) continue
            for (m in markers) if (i + m.size <= xml.size && (m.indices).all { k -> xml[i + k].toInt().toChar().uppercaseChar() == m[k].toInt().toChar() }) return true
        }
        return false
    }

    fun parse(xml: ByteArray, handler: DefaultHandler) {
        if (declaresDtd(xml)) return
        val factory = SAXParserFactory.newInstance().apply { isNamespaceAware = false }
        for ((feature, on) in listOf(
            "http://apache.org/xml/features/disallow-doctype-decl" to true,
            "http://xml.org/sax/features/external-general-entities" to false,
            "http://xml.org/sax/features/external-parameter-entities" to false,
        )) {
            val _ = runCatching { factory.setFeature(feature, on) }
        }
        try {
            factory.newSAXParser().parse(InputSource(ByteArrayInputStream(xml)), handler)
        } catch (_: Exception) {
            // A cut or broken part: keep what was read before the error.
        }
    }

    /** Every element with one of the names, as its attributes by qualified name (so `r:id`). */
    fun elements(xml: ByteArray, names: Set<String>): List<Map<String, String>> {
        val found = ArrayList<Map<String, String>>()
        parse(xml, object : DefaultHandler() {
            override fun startElement(uri: String?, localName: String?, qName: String, attributes: Attributes) {
                if (local(qName) in names) found.add((0 until attributes.length).associate { attributes.getQName(it) to attributes.getValue(it) })
            }
        })
        return found
    }
}
