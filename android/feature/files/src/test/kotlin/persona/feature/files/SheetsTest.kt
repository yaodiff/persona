package persona.feature.files

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.robolectric.RuntimeEnvironment
import org.robolectric.annotation.Config
import java.io.ByteArrayOutputStream
import java.util.zip.ZipEntry
import java.util.zip.ZipOutputStream

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class SheetsTest {
    private val res get() = RuntimeEnvironment.getApplication().resources

    @Test fun delimitedFollowsRfc4180() {
        val rows = Delimited.parse("a,\"b,1\",\"say \"\"hi\"\"\"\r\n\"two\nlines\",,x\n\n3", ',')
        assertEquals(listOf(listOf("a", "b,1", "say \"hi\""), listOf("two\nlines", "", "x"), listOf("3")), rows)
    }

    @Test fun delimitedKeepsValuesAsWrittenAndSkipsTheBom() {
        assertEquals(listOf(listOf("001", "1e3", " 2 ")), Delimited.parse("﻿001,1e3, 2 ", ','))
        assertEquals(listOf(listOf("a", "")), Delimited.parse("a,", ','))
        // A lone empty field is a blank line, skipped (as iOS does).
        assertEquals(emptyList<List<String>>(), Delimited.parse("\"\"", ','))
    }

    @Test fun separatorByNameOrFirstLine() {
        assertEquals('\t', Delimited.separator("x.tsv", "a,b"))
        assertEquals(';', Delimited.separator("x.csv", "a;b;c\n1,5;2"))
        assertEquals(',', Delimited.separator("x.csv", "a,b;c"))
        assertEquals('\t', Delimited.separator("x.csv", "a\tb\tc,d"))
        assertEquals(',', Delimited.separator("x.csv", "plain"))
    }

    @Test fun sheetCapsRows() {
        val sheet = Delimited.sheet("big.csv", (0..TableSheet.MAX_ROWS + 5).joinToString("\n"))
        assertEquals(TableSheet.MAX_ROWS, sheet.rows.size)
        assertTrue(sheet.capped)
        assertEquals("big", sheet.name)
    }

    @Test fun columnNames() {
        assertEquals(listOf("A", "Z", "AA", "AZ", "BA", "ZZ", "AAA"), listOf(0, 25, 26, 51, 52, 701, 702).map(TableSheet::columnName))
        assertEquals(27, Xlsx.column("AB12"))
        assertEquals(null, Xlsx.column("12"))
        assertEquals(Int.MAX_VALUE, Xlsx.column("FXSHRXX1"))
    }

    @Test fun numbersAsExcelShowsThem() {
        assertEquals("42", Xlsx.number("42.0"))
        assertEquals("0.1", Xlsx.number("0.1000000000000000055511151231257827"))
        assertEquals("3.14159", Xlsx.number("3.14159"))
        assertEquals("-7", Xlsx.number("-7"))
        assertEquals("1E+20", Xlsx.number("1e20"))
        assertEquals("abc", Xlsx.number("abc"))
    }

    @Test fun datesFromSerials() {
        assertEquals("2024-01-15", Xlsx.date("45306"))
        assertEquals("2024-01-15 12:30", Xlsx.date("45306.5208333333"))
        assertEquals("06:00", Xlsx.date("0.25"))
        // Excel's 1900 leap-year bug: serial 60 is a 1900-02-29 that never was; days before it are off by one.
        assertEquals("1900-01-01", Xlsx.date("1"))
        assertEquals("1900-02-28", Xlsx.date("59"))
        assertEquals("1900-02-29", Xlsx.date("60"))
        assertEquals("1900-03-01", Xlsx.date("61"))
        // Boundary & rollover cases: serial 0 is midnight time-only, negative serials are invalid, and rounding past
        // 23:59:59.5 carries cleanly across the 1900-02-29 boundary instead of emitting "1900-02-29 00:00".
        assertEquals("00:00", Xlsx.date("0"))
        assertEquals("00:00", Xlsx.date("0.000001"))
        assertEquals("-0.5", Xlsx.date("-0.5"))
        assertEquals("1900-01-01", Xlsx.date("0.999999"))
        assertEquals("1900-02-29", Xlsx.date("59.999999"))
        assertEquals("1900-02-29 12:00", Xlsx.date("60.5"))
        assertEquals("1900-03-01", Xlsx.date("60.999999"))
        assertEquals("2024-01-16", Xlsx.date("45306.999999"))
    }

    @Test fun escapedAndPaddedCustomNumberFormatsAreNotTreatedAsDates() {
        val parts = mapOf(
            "xl/workbook.xml" to """<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Units" sheetId="1" r:id="rId1"/></sheets></workbook>""",
            "xl/_rels/workbook.xml.rels" to """<Relationships><Relationship Id="rId1" Target="worksheets/sheet1.xml"/></Relationships>""",
            "xl/styles.xml" to """<styleSheet>
                <numFmts>
                    <numFmt numFmtId="164" formatCode="#,##0\m"/>
                    <numFmt numFmtId="165" formatCode="0.0\s"/>
                    <numFmt numFmtId="166" formatCode="#,##0_m"/>
                    <numFmt numFmtId="167" formatCode="*d0"/>
                    <numFmt numFmtId="168" formatCode="yyyy\-mm\-dd"/>
                    <numFmt numFmtId="169" formatCode="0\"/>
                </numFmts>
                <cellXfs>
                    <xf numFmtId="0"/>
                    <xf numFmtId="164"/>
                    <xf numFmtId="165"/>
                    <xf numFmtId="166"/>
                    <xf numFmtId="167"/>
                    <xf numFmtId="168"/>
                    <xf numFmtId="169"/>
                </cellXfs>
            </styleSheet>""",
            "xl/worksheets/sheet1.xml" to """<worksheet><sheetData>
                <row r="1">
                    <c r="A1" s="1"><v>45306</v></c>
                    <c r="B1" s="2"><v>12.5</v></c>
                    <c r="C1" s="3"><v>100</v></c>
                    <c r="D1" s="4"><v>100</v></c>
                    <c r="E1" s="5"><v>45306</v></c>
                    <c r="F1" s="6"><v>7</v></c>
                </row>
            </sheetData></worksheet>""",
        )
        val out = ByteArrayOutputStream()
        ZipOutputStream(out).use { zip -> parts.forEach { (name, xml) -> zip.putNextEntry(ZipEntry(name)); zip.write(xml.toByteArray()); zip.closeEntry() } }
        val sheet = Xlsx.read(out.toByteArray()).single()
        assertEquals(listOf("45306", "12.5", "100", "100", "2024-01-15", "7"), sheet.rows.single())
    }

    @Test fun inlineStringsIgnorePhoneticRubyRuns() {
        val parts = mapOf(
            "xl/workbook.xml" to """<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Inline" sheetId="1" r:id="rId1"/></sheets></workbook>""",
            "xl/_rels/workbook.xml.rels" to """<Relationships><Relationship Id="rId1" Target="worksheets/sheet1.xml"/></Relationships>""",
            "xl/worksheets/sheet1.xml" to """<worksheet><sheetData>
                <row r="1">
                    <c r="A1" t="inlineStr"><is><r><t>東</t></r><r><t>京</t></r><rPh sb="0" eb="2"><t>とうきょう</t></rPh></is></c>
                    <c r="B1" t="inlineStr"><is><t>渋谷</t><rPh sb="0" eb="2"><t>しぶや</t></rPh></is></c>
                </row>
            </sheetData></worksheet>""",
        )
        val out = ByteArrayOutputStream()
        ZipOutputStream(out).use { zip -> parts.forEach { (name, xml) -> zip.putNextEntry(ZipEntry(name)); zip.write(xml.toByteArray()); zip.closeEntry() } }
        val sheet = Xlsx.read(out.toByteArray()).single()
        assertEquals(listOf("東京", "渋谷"), sheet.rows.single())
    }

    @Test fun cappedWorksheetStopsParsingWithoutRetainingTrailingCellValues() {
        // Exact limit: not capped, all rows preserved.
        val exactXml = buildString {
            append("<worksheet><sheetData>")
            for (r in 1..4) append("<row r=\"$r\"><c r=\"A$r\"><v>$r</v></c></row>")
            append("</sheetData></worksheet>")
        }.toByteArray()
        val exact = Xlsx.cells(exactXml, "Exact", emptyList(), emptyList(), maxRows = 4)
        assertFalse(exact.capped)
        assertEquals(listOf(listOf("1"), listOf("2"), listOf("3"), listOf("4")), exact.rows)

        // Past the row cap: stops immediately without parsing trailing rows or retaining their cell text.
        val payload = "x".repeat(4096)
        val overCapXml = buildString {
            append("<worksheet><sheetData>")
            append("<row r=\"1\"><c r=\"A1\" t=\"inlineStr\"><is><t>keep-1</t></is></c></row>")
            append("<row r=\"2\"><c r=\"A2\" t=\"inlineStr\"><is><t>keep-2</t></is></c></row>")
            append("<row r=\"3\"><c r=\"A3\" t=\"inlineStr\"><is><t>keep-3</t></is></c></row>")
            for (r in 4..4000) {
                append("<row r=\"$r\"><c r=\"A$r\" t=\"inlineStr\"><is><t>").append(payload).append("</t></is></c></row>")
            }
            append("</sheetData></worksheet>")
        }.toByteArray()
        val capped = Xlsx.cells(overCapXml, "Over", emptyList(), emptyList(), maxRows = 3)
        assertTrue(capped.capped)
        assertEquals(listOf(listOf("keep-1"), listOf("keep-2"), listOf("keep-3")), capped.rows)

        // Exact sparse jump to `r == maxRows + 1`: gap fills `maxRows`, marks capped, and aborts SAX before reading row 4's cells.
        var sharedStringLookups = 0
        val trackingStrings = object : AbstractList<String>() {
            override val size: Int = 1
            override fun get(index: Int): String {
                sharedStringLookups++
                return "must-not-be-read"
            }
        }
        val exactSparseJumpXml = """<worksheet><sheetData>
            <row r="1"><c r="A1" t="inlineStr"><is><t>row-1</t></is></c></row>
            <row r="4"><c r="A4" t="s"><v>0</v></c></row>
        </sheetData></worksheet>""".toByteArray()
        val sparseCapped = Xlsx.cells(exactSparseJumpXml, "SparseExact", trackingStrings, emptyList(), maxRows = 3)
        assertTrue(sparseCapped.capped)
        assertEquals(listOf(listOf("row-1"), emptyList(), emptyList()), sparseCapped.rows)
        assertEquals("SAX must abort before reading row maxRows + 1 cells", 0, sharedStringLookups)

        // End-to-end workbook capped via a sparse row jump past TableSheet.MAX_ROWS: row 1 stays intact and huge trailing cells do not blow up memory.
        val jumpXml = buildString {
            append("<worksheet><sheetData>")
            append("<row r=\"1\"><c r=\"A1\" t=\"inlineStr\"><is><t>first</t></is></c></row>")
            append("<row r=\"200005\">")
            for (c in 1..2000) append("<c t=\"inlineStr\"><is><t>").append(payload).append("</t></is></c>")
            append("</row></sheetData></worksheet>")
        }
        val parts = mapOf(
            "xl/workbook.xml" to """<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Jump" sheetId="1" r:id="rId1"/></sheets></workbook>""",
            "xl/_rels/workbook.xml.rels" to """<Relationships><Relationship Id="rId1" Target="worksheets/sheet1.xml"/></Relationships>""",
            "xl/worksheets/sheet1.xml" to jumpXml,
        )
        val out = ByteArrayOutputStream()
        ZipOutputStream(out).use { zip -> parts.forEach { (name, xml) -> zip.putNextEntry(ZipEntry(name)); zip.write(xml.toByteArray()); zip.closeEntry() } }
        val jumped = Xlsx.read(out.toByteArray()).single()
        assertTrue(jumped.capped)
        assertEquals(TableSheet.MAX_ROWS, jumped.rows.size)
        assertEquals(listOf("first"), jumped.rows.first())

        // Non-positive row attributes `r="0"` and `r="-5"` fall back to sequential row numbering instead of corrupting rowIndex.
        val badRowXml = """<worksheet><sheetData>
            <row r="1"><c r="A1"><v>10</v></c></row>
            <row r="0"><c r="A2"><v>20</v></c></row>
            <row r="-5"><c r="A3"><v>30</v></c></row>
        </sheetData></worksheet>""".toByteArray()
        val badRow = Xlsx.cells(badRowXml, "BadRow", emptyList(), emptyList(), maxRows = 10)
        assertFalse(badRow.capped)
        assertEquals(listOf(listOf("10"), listOf("20"), listOf("30")), badRow.rows)
    }

    @Test fun readsAWorkbook() {
        val sheets = Xlsx.read(workbook())
        assertEquals(listOf("数据", "Empty"), sheets.map { it.name })
        val s = sheets[0]
        assertEquals(listOf("名称", "数量", "日期", "通过"), s.rows[0])
        assertEquals(listOf("苹果", "12", "2024-01-15", "TRUE"), s.rows[1])
        // Row 3 is missing in the file: an empty row in its place; cell C4 is placed by its reference.
        assertEquals(emptyList<String>(), s.rows[2])
        assertEquals(listOf("内联", "0.5", "", "FALSE"), s.rows[3])
        assertEquals(4, s.columns)
        assertTrue(sheets[1].rows.isEmpty())
    }

    @Test fun partPathsResolveDots() {
        assertEquals("xl/worksheets/a.xml", Xlsx.normalizePath("xl/../xl/./worksheets/a.xml"))
        assertEquals("xl/worksheets/a.xml", Xlsx.normalizePath("xl//worksheets/a.xml"))
    }

    @Test fun partsWithADtdAreNotParsed() {
        assertTrue(Xml.declaresDtd("<?xml?><!DOCTYPE x [<!ENTITY a \"b\">]><x>&a;</x>".toByteArray()))
        assertTrue(Xml.declaresDtd("<!doctype x>".toByteArray()))
        assertFalse(Xml.declaresDtd("<sst><si><t>DOCTYPE</t></si></sst>".toByteArray()))
        val bomb = "<!DOCTYPE s [<!ENTITY a \"aaaaaaaaaa\"><!ENTITY b \"&a;&a;&a;&a;&a;&a;&a;&a;&a;&a;\">]><sst><si><t>&b;</t></si></sst>"
        val found = Xml.elements(bomb.toByteArray(), setOf("si"))
        assertTrue(found.isEmpty())
    }

    @Test fun aNonWorkbookSaysWhy() {
        val failure = runCatching { Xlsx.read("not a zip".toByteArray()) }.exceptionOrNull()
        assertTrue(failure is SheetFailure)
    }

    @Test fun sparseFarRightColumnsAndRepeatedSheetRefsAbortBeforeOom() {
        fun zipParts(parts: Map<String, String>): ByteArray {
            val out = ByteArrayOutputStream()
            ZipOutputStream(out).use { z -> parts.forEach { (k, v) -> z.putNextEntry(ZipEntry(k)); z.write(v.toByteArray()); z.closeEntry() } }
            return out.toByteArray()
        }

        // Empty cells at XFD do not allocate 16,384 slots per row.
        val emptyXfdXml = buildString {
            append("<worksheet><sheetData>")
            for (r in 1..500) append("<row r=\"$r\"><c r=\"A$r\"><v>$r</v></c><c r=\"XFD$r\"/></row>")
            append("</sheetData></worksheet>")
        }.toByteArray()
        val emptyXfd = Xlsx.cells(emptyXfdXml, "EmptyFar", emptyList(), emptyList())
        assertEquals(500, emptyXfd.rows.size)
        assertEquals(1, emptyXfd.columns)

        // Many rows each placing a non-empty cell at XFD$r hit MAX_WORKBOOK_SLOTS (~245 rows × 16,384 > 4M) and fail cleanly instead of GB OOM.
        val xfdBombXml = buildString {
            append("<worksheet><sheetData>")
            for (r in 1..300) append("<row r=\"$r\"><c r=\"XFD$r\"><v>1</v></c></row>")
            append("</sheetData></worksheet>")
        }
        val xfdBombZip = zipParts(mapOf(
            "xl/workbook.xml" to """<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Bomb" sheetId="1" r:id="rId1"/></sheets></workbook>""",
            "xl/_rels/workbook.xml.rels" to """<Relationships><Relationship Id="rId1" Target="worksheets/sheet1.xml"/></Relationships>""",
            "xl/worksheets/sheet1.xml" to xfdBombXml,
        ))
        val xfdFailure = runCatching { Xlsx.read(xfdBombZip) }.exceptionOrNull() as? SheetFailure
        assertEquals(R.string.files_sheet_too_big, xfdFailure?.reason)

        // Repeated <sheet r:id="rId1"/> within limits memoize cleanly with distinct tab names.
        val sharedSheetZip = zipParts(mapOf(
            "xl/workbook.xml" to """<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="First" sheetId="1" r:id="rId1"/><sheet name="Alias" sheetId="2" r:id="rId2"/></sheets></workbook>""",
            "xl/_rels/workbook.xml.rels" to """<Relationships><Relationship Id="rId1" Target="worksheets/sheet1.xml"/><Relationship Id="rId2" Target="worksheets/../worksheets/sheet1.xml"/></Relationships>""",
            "xl/worksheets/sheet1.xml" to """<worksheet><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>ok</t></is></c></row></sheetData></worksheet>""",
        ))
        val aliased = Xlsx.read(sharedSheetZip)
        assertEquals(listOf("First", "Alias"), aliased.map { it.name })
        assertEquals(listOf(listOf("ok")), aliased[0].rows)
        assertEquals(listOf(listOf("ok")), aliased[1].rows)

        // Over MAX_SHEETS (256) repeated <sheet> entries abort cleanly while streaming workbook.xml.
        val tooManySheets = buildString {
            append("""<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets>""")
            for (i in 1..300) append("""<sheet name="S$i" sheetId="$i" r:id="rId1"/>""")
            append("</sheets></workbook>")
        }
        val manySheetsZip = zipParts(mapOf(
            "xl/workbook.xml" to tooManySheets,
            "xl/_rels/workbook.xml.rels" to """<Relationships><Relationship Id="rId1" Target="worksheets/sheet1.xml"/></Relationships>""",
            "xl/worksheets/sheet1.xml" to """<worksheet><sheetData><row r="1"><c r="A1"><v>1</v></c></row></sheetData></worksheet>""",
        ))
        assertEquals(R.string.files_sheet_too_big, (runCatching { Xlsx.read(manySheetsZip) }.exceptionOrNull() as? SheetFailure)?.reason)

        // Repeated <sheet r:id="rId1"/> under MAX_SHEETS still enforces cumulative slot/char budgets across copies.
        val amplifiedWorkbook = buildString {
            append("""<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets>""")
            for (i in 1..20) append("""<sheet name="S$i" sheetId="$i" r:id="rId1"/>""")
            append("</sheets></workbook>")
        }
        val mediumSheet = buildString {
            append("<worksheet><sheetData>")
            for (r in 1..100) append("<row r=\"$r\"><c r=\"A$r\" t=\"inlineStr\"><is><t>abcdefghij</t></is></c></row>")
            append("</sheetData></worksheet>")
        }
        val ampZip = zipParts(mapOf(
            "xl/workbook.xml" to amplifiedWorkbook,
            "xl/_rels/workbook.xml.rels" to """<Relationships><Relationship Id="rId1" Target="worksheets/sheet1.xml"/></Relationships>""",
            "xl/worksheets/sheet1.xml" to mediumSheet,
        ))
        val ampFailure = runCatching { Xlsx.read(ampZip, Xlsx.WorkbookBudget(maxSlots = 1_000L, maxChars = 10_000L)) }.exceptionOrNull() as? SheetFailure
        assertEquals(R.string.files_sheet_too_big, ampFailure?.reason)

        // Single huge inline cell or huge sharedStrings or >10,000 .rels abort immediately while streaming.
        val hugeCellXml = "<worksheet><sheetData><row r=\"1\"><c r=\"A1\" t=\"inlineStr\"><is><t>${"x".repeat(2_000)}</t></is></c></row></sheetData></worksheet>".toByteArray()
        assertEquals(R.string.files_sheet_too_big,
            (runCatching { Xlsx.cells(hugeCellXml, "BigCell", emptyList(), emptyList(), budget = Xlsx.WorkbookBudget(maxChars = 1_000L)) }.exceptionOrNull() as? SheetFailure)?.reason)

        val hugeRels = buildString {
            append("<Relationships>")
            for (i in 1..10_050) append("""<Relationship Id="r$i" Target="worksheets/s$i.xml"/>""")
            append("</Relationships>")
        }
        val relsBombZip = zipParts(mapOf(
            "xl/workbook.xml" to """<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="S1" sheetId="1" r:id="r1"/></sheets></workbook>""",
            "xl/_rels/workbook.xml.rels" to hugeRels,
            "xl/worksheets/s1.xml" to "<worksheet><sheetData/></worksheet>",
        ))
        assertEquals(R.string.files_sheet_too_big, (runCatching { Xlsx.read(relsBombZip) }.exceptionOrNull() as? SheetFailure)?.reason)

        // Overwrite-to-empty XFD$r still charges allocated slots (preventing backing-array OOM) and drops backing arrays.
        val overwriteEmptyXfdXml = buildString {
            append("<worksheet><sheetData>")
            for (r in 1..4) append("<row r=\"$r\"><c r=\"XFD$r\"><v>1</v></c><c r=\"XFD$r\"/></row>")
            append("</sheetData></worksheet>")
        }.toByteArray()
        assertEquals(R.string.files_sheet_too_big,
            (runCatching { Xlsx.cells(overwriteEmptyXfdXml, "OverwriteEmpty", emptyList(), emptyList(), budget = Xlsx.WorkbookBudget(maxSlots = 40_000L)) }.exceptionOrNull() as? SheetFailure)?.reason)
        val trimmedRow = Xlsx.cells("<worksheet><sheetData><row r=\"1\"><c r=\"A1\"><v>1</v></c><c r=\"XFD1\"><v>2</v></c><c r=\"XFD1\"/></row></sheetData></worksheet>".toByteArray(),
            "Trimmed", emptyList(), emptyList())
        assertEquals(listOf(listOf("1")), trimmedRow.rows)
        assertEquals(1, trimmedRow.columns)

        // Omitted optional `r` attributes preserve column position across empty `<c/>` cells.
        val omittedRefXml = """<worksheet><sheetData>
            <row r="1"><c t="inlineStr"><is><t>A</t></is></c><c/><c t="inlineStr"><is><t>C</t></is></c></row>
            <row r="2"><c r="B2"><v>2</v></c><c><v>3</v></c></row>
        </sheetData></worksheet>""".toByteArray()
        val omittedRef = Xlsx.cells(omittedRefXml, "OmittedRef", emptyList(), emptyList())
        assertEquals(listOf(listOf("A", "", "C"), listOf("", "2", "3")), omittedRef.rows)

        // Out-of-range columns do not buffer text or consume character budget.
        val outOfRangeCellXml = "<worksheet><sheetData><row r=\"1\"><c r=\"XFE1\" t=\"inlineStr\"><is><t>${"z".repeat(2_000)}</t></is></c><c r=\"A1\"><v>7</v></c></row></sheetData></worksheet>".toByteArray()
        val outOfRangeSheet = Xlsx.cells(outOfRangeCellXml, "OOR", emptyList(), emptyList(), budget = Xlsx.WorkbookBudget(maxChars = 100L))
        assertEquals(listOf(listOf("7")), outOfRangeSheet.rows)

        // Single huge relationship Target (> 4,096 chars) aborts before normalizePath().
        val longTargetZip = zipParts(mapOf(
            "xl/workbook.xml" to """<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="S1" sheetId="1" r:id="r1"/></sheets></workbook>""",
            "xl/_rels/workbook.xml.rels" to """<Relationships><Relationship Id="r1" Target="${"a/".repeat(2_500)}sheet1.xml"/></Relationships>""",
            "xl/worksheets/sheet1.xml" to "<worksheet><sheetData/></worksheet>",
        ))
        assertEquals(R.string.files_sheet_too_big, (runCatching { Xlsx.read(longTargetZip) }.exceptionOrNull() as? SheetFailure)?.reason)

        // workbook.xml metaChars cap aborts cleanly.
        val hugeNameZip = zipParts(mapOf(
            "xl/workbook.xml" to """<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="${"n".repeat(1_500)}" sheetId="1" r:id="r1"/></sheets></workbook>""",
            "xl/worksheets/sheet1.xml" to "<worksheet><sheetData/></worksheet>",
        ))
        assertEquals(R.string.files_sheet_too_big, (runCatching { Xlsx.read(hugeNameZip, Xlsx.WorkbookBudget(maxChars = 1_000L)) }.exceptionOrNull() as? SheetFailure)?.reason)

        // Nested <si> entries and sharedStrings maxChars cap abort cleanly.
        val nestedSiZip = zipParts(mapOf(
            "xl/workbook.xml" to """<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="S1" sheetId="1" r:id="r1"/></sheets></workbook>""",
            "xl/sharedStrings.xml" to "<sst>${"<si>".repeat(25)}${"</si>".repeat(25)}</sst>",
            "xl/worksheets/sheet1.xml" to "<worksheet><sheetData/></worksheet>",
        ))
        assertEquals(R.string.files_sheet_too_big, (runCatching { Xlsx.read(nestedSiZip, Xlsx.WorkbookBudget(maxSharedStrings = 20)) }.exceptionOrNull() as? SheetFailure)?.reason)

        val hugeSstZip = zipParts(mapOf(
            "xl/workbook.xml" to """<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="S1" sheetId="1" r:id="r1"/></sheets></workbook>""",
            "xl/sharedStrings.xml" to "<sst><si><t>${"s".repeat(1_500)}</t></si></sst>",
            "xl/worksheets/sheet1.xml" to "<worksheet><sheetData/></worksheet>",
        ))
        assertEquals(R.string.files_sheet_too_big, (runCatching { Xlsx.read(hugeSstZip, Xlsx.WorkbookBudget(maxChars = 1_000L)) }.exceptionOrNull() as? SheetFailure)?.reason)

        // styles.xml evaluates each custom numFmt once (no quadratic blowup when many <xf> reference one long formatCode) and enforces maxStyleEntries on duplicate numFmtId.
        val longFmt = "0".repeat(20_000) + "yyyy"
        val manyXfStyles = buildString {
            append("""<styleSheet><numFmts><numFmt numFmtId="164" formatCode="$longFmt"/></numFmts><cellXfs>""")
            for (i in 1..2_000) append("""<xf numFmtId="164"/>""")
            append("</cellXfs></styleSheet>")
        }
        val manyXfZip = zipParts(mapOf(
            "xl/workbook.xml" to """<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="S1" sheetId="1" r:id="r1"/></sheets></workbook>""",
            "xl/styles.xml" to manyXfStyles,
            "xl/worksheets/sheet1.xml" to """<worksheet><sheetData><row r="1"><c r="A1" s="1999"><v>45306</v></c></row></sheetData></worksheet>""",
        ))
        assertEquals(listOf(listOf("2024-01-15")), Xlsx.read(manyXfZip).single().rows)

        val dupNumFmtStyles = "<styleSheet><numFmts>${"""<numFmt numFmtId="164" formatCode="0"/>""".repeat(25)}</numFmts></styleSheet>"
        val dupNumFmtZip = zipParts(mapOf(
            "xl/workbook.xml" to """<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="S1" sheetId="1" r:id="r1"/></sheets></workbook>""",
            "xl/styles.xml" to dupNumFmtStyles,
            "xl/worksheets/sheet1.xml" to "<worksheet><sheetData/></worksheet>",
        ))
        assertEquals(R.string.files_sheet_too_big, (runCatching { Xlsx.read(dupNumFmtZip, Xlsx.WorkbookBudget(maxStyleEntries = 20)) }.exceptionOrNull() as? SheetFailure)?.reason)

        // Sparse empty gap rows exceeding maxSlots and post-parse flushRow() exceeding maxSlots both fail cleanly.
        val gapBombXml = """<worksheet><sheetData><row r="1"><c r="A1"><v>1</v></c></row><row r="500"><c r="A500"><v>2</v></c></row></sheetData></worksheet>""".toByteArray()
        assertEquals(R.string.files_sheet_too_big,
            (runCatching { Xlsx.cells(gapBombXml, "GapBomb", emptyList(), emptyList(), budget = Xlsx.WorkbookBudget(maxSlots = 100L)) }.exceptionOrNull() as? SheetFailure)?.reason)

        val exhausted = Xlsx.WorkbookBudget(maxSlots = 1L).apply { slots = 1L }
        assertEquals(R.string.files_sheet_too_big,
            (runCatching { Xlsx.cells("<worksheet><sheetData><row r=\"1\"/></sheetData></worksheet>".toByteArray(), "LastRow", emptyList(), emptyList(), budget = exhausted) }.exceptionOrNull() as? SheetFailure)?.reason)
    }

    @Test fun numericCellsAlignRight() {
        listOf("1,234.5", "-3", "12%", "$4", "¥4", "1e5", "(12)").forEach { assertTrue(it, looksNumeric(it)) }
        listOf("", "abc", "1-2", "v1.2").forEach { assertFalse(it, looksNumeric(it)) }
    }

    @Test fun wideCharactersCountTwo() {
        assertEquals(4, textCells("名称"))
        assertEquals(3, textCells("abc"))
    }

    @Test fun summary() {
        val sheet = Delimited.sheet("a.csv", "a,b\n1,2,3")
        assertEquals("2 行 × 3 列", sheetSummary(res, sheet, false))
        assertEquals("2 行 × 3 列 · 只读了前 2 MB", sheetSummary(res, sheet, true))
    }

    companion object {
        /** A small xlsx: shared and inline strings, a date style, booleans, a skipped row, an empty second sheet. */
        fun workbook(): ByteArray {
            val parts = mapOf(
                "[Content_Types].xml" to "<Types/>",
                "xl/workbook.xml" to """<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="数据" sheetId="1" r:id="rId1"/><sheet name="Empty" sheetId="2" r:id="rId2"/></sheets></workbook>""",
                "xl/_rels/workbook.xml.rels" to """<Relationships><Relationship Id="rId1" Target="worksheets/sheet1.xml"/><Relationship Id="rId2" Target="/xl/worksheets/sheet2.xml"/></Relationships>""",
                "xl/sharedStrings.xml" to """<sst><si><t>名称</t></si><si><t>数量</t></si><si><r><t>日</t></r><r><t>期</t></r><rPh><t>にち</t></rPh></si><si><t>通过</t></si><si><t>苹果</t></si></sst>""",
                "xl/styles.xml" to """<styleSheet><numFmts><numFmt numFmtId="164" formatCode="&quot;d&quot;0.0"/></numFmts><cellXfs><xf numFmtId="0"/><xf numFmtId="14"/><xf numFmtId="164"/></cellXfs></styleSheet>""",
                "xl/worksheets/sheet1.xml" to """<worksheet><sheetData>
                    <row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c><c r="C1" t="s"><v>2</v></c><c r="D1" t="s"><v>3</v></c></row>
                    <row r="2"><c r="A2" t="s"><v>4</v></c><c r="B2" s="2"><v>12</v></c><c r="C2" s="1"><v>45306</v></c><c r="D2" t="b"><v>1</v></c></row>
                    <row r="4"><c r="A4" t="inlineStr"><is><t>内联</t></is></c><c r="B4"><v>0.5</v></c><c r="D4" t="b"><v>0</v></c></row>
                    </sheetData></worksheet>""",
                "xl/worksheets/sheet2.xml" to "<worksheet><sheetData/></worksheet>",
            )
            val out = ByteArrayOutputStream()
            ZipOutputStream(out).use { zip -> parts.forEach { (name, xml) -> zip.putNextEntry(ZipEntry(name)); zip.write(xml.toByteArray()); zip.closeEntry() } }
            return out.toByteArray()
        }
    }
}
