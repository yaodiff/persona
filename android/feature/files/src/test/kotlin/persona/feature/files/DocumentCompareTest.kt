package persona.feature.files

import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.api.SessionGitFile
import java.io.ByteArrayOutputStream
import java.util.zip.ZipEntry
import java.util.zip.ZipOutputStream

/** Names the tests read easily; the app's come from its string resources. */
private object Names : UnitNames {
    override fun page(n: Int) = "P$n"
    override fun slide(n: Int) = "S$n"
    override fun sheet(name: String) = "T:$name"
    override val notes = "[notes]"
}

private fun zip(vararg parts: Pair<String, String>): ByteArray {
    val out = ByteArrayOutputStream()
    ZipOutputStream(out).use { z -> parts.forEach { (name, xml) -> z.putNextEntry(ZipEntry(name)); z.write(xml.toByteArray()); z.closeEntry() } }
    return out.toByteArray()
}

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class DocumentCompareTest {
    @Test fun whichFilesAreCompared() {
        assertEquals(CompareKind.Image, DocumentCompare.kind("art/Icon.PNG"))
        assertEquals(CompareKind.Document, DocumentCompare.kind("docs/spec.docx"))
        assertEquals(CompareKind.Document, DocumentCompare.kind("a.pdf"))
        assertEquals(CompareKind.Document, DocumentCompare.kind("book.xlsx"))
        assertNull(DocumentCompare.kind("main.kt"))
        assertNull(DocumentCompare.kind("Makefile"))
    }

    @Test fun theVersionsOfAChange() {
        val unstaged = SessionGitFile("art/icon.png", index = ".", worktree = "M")
        assertEquals(listOf("art/icon.png", "INDEX", "WORKTREE"), compareChange(unstaged, staged = false).let { listOf(it.beforePath, it.before, it.after) })
        assertEquals(listOf("HEAD", "INDEX"), compareChange(unstaged, staged = true).let { listOf(it.before, it.after) })
        // Staged and changed again: the page offers both halves, as the text diff does.
        val both = SessionGitFile("art/icon.png", index = "M", worktree = "M")
        assertTrue(compareChange(both, staged = false).bothHalves)
        assertTrue(!compareChange(unstaged, staged = false).bothHalves)
        // A rename reads its old version under the old name; a file deleted in the tree has no new one.
        val renamed = SessionGitFile("art/new.png", originalPath = "art/old.png", index = "R")
        assertEquals("art/old.png", compareChange(renamed, staged = true).beforePath)
        assertTrue(compareChange(SessionGitFile("a.png", worktree = "D"), staged = false).deleted)
        assertEquals(listOf("abc123^", "abc123"), compareCommit("abc123", SessionGitFile("a.png", index = "M")).let { listOf(it.before, it.after) })
    }

    @Test fun aLineDiffLikeGits() {
        val ops = requireNotNull(DocumentCompare.diff(listOf("a", "b", "c", "d"), listOf("a", "c", "x", "d")))
        assertEquals(listOf("=a", "-b", "=c", "+x", "=d"), ops.map { it.kind.mark + it.text })
        assertNull(DocumentCompare.diff(List(10) { "a$it" }, List(10) { "b$it" }, maxEdits = 5))
        assertNull(DocumentCompare.diff(List(40) { "a" }, List(40) { "a" }, limit = 50))
    }

    @Test fun hunksKeepTwoLinesAroundAndSayWhereTheyAre() {
        val a = DocumentCompare.flatten(listOf(DocUnit("P1", (1..10).map { "line $it" }), DocUnit("P2", listOf("end"))))
        val b = DocumentCompare.flatten(listOf(DocUnit("P1", (1..10).map { if (it == 8) "changed" else "line $it" }), DocUnit("P2", listOf("end", "more"))))
        val hunks = DocumentCompare.hunks(requireNotNull(DocumentCompare.diff(a, b)))
        assertEquals(2, hunks.size)
        assertEquals("P1", hunks[0].unit)
        assertEquals(5, hunks[0].skippedBefore) // lines 1–5; the page's header is not a line of the document
        assertEquals(listOf("=line 6", "=line 7", "-line 8", "+changed", "=line 9", "=line 10"), hunks[0].ops.map { it.kind.mark + it.text })
        assertEquals("P2", hunks[1].unit)
        assertEquals(listOf("=end", "+more"), hunks[1].ops.map { it.kind.mark + it.text })
        assertTrue(DocumentCompare.hunks(requireNotNull(DocumentCompare.diff(a, a))).isEmpty())
    }

    @Test fun aChangeAtTheTopOfAPageSaysThatPage() {
        val a = DocumentCompare.flatten(listOf(DocUnit("P1", listOf("a", "b", "c", "d", "e")), DocUnit("P2", listOf("first", "x", "y", "z", "w"))))
        val b = DocumentCompare.flatten(listOf(DocUnit("P1", listOf("a", "b", "c", "d", "e")), DocUnit("P2", listOf("FIRST", "x", "y", "z", "w"))))
        assertEquals(listOf("P2"), DocumentCompare.hunks(requireNotNull(DocumentCompare.diff(a, b))).map { it.unit })
    }

    @Test fun aLongDiffStaysSmall() {
        // Thousands of changes come back as too many, not as a frontier per edit that fills the phone's memory.
        assertNull(DocumentCompare.diff(List(20_000) { "a$it" }, List(20_000) { "b$it" }))
    }

    @Test fun theCountsAreTheDocumentsLinesNotTheUnitHeaders() {
        val outcome = compareUnits(listOf(DocUnit("S1", listOf("a"))), listOf(DocUnit("S1", listOf("a")), DocUnit("S2", listOf("b", "c"))))
        assertEquals(2, (outcome as DocOutcome.Changes).added)
    }

    @Test fun aChangedTableRowShowsOnlyItsChangedCells() {
        val ops = requireNotNull(DocumentCompare.diff(listOf("x", "1 | 2 | 3"), listOf("x", "1 | 5 | 3", "new")))
        val lines = DocumentCompare.pairRows(ops)
        assertEquals(CompareLine.Same("x"), lines[0])
        assertEquals(CompareLine.Row(listOf("1" to "1", "2" to "5", "3" to "3")), lines[1])
        assertEquals(CompareLine.Added("new"), lines[2])
    }

    @Test fun aWordDocumentByPageWithItsTablesAndWithoutDeletedText() {
        val w = "xmlns:w=\"http://schemas.openxmlformats.org/wordprocessingml/2006/main\""
        val doc = zip("word/document.xml" to """<w:document $w><w:body>
            <w:p><w:r><w:t>Title</w:t></w:r></w:p>
            <w:p><w:r><w:t>kept </w:t></w:r><w:del><w:r><w:delText>gone</w:delText></w:r></w:del><w:ins><w:r><w:t>added</w:t></w:r></w:ins></w:p>
            <w:tbl><w:tr><w:tc><w:p><w:r><w:t>a</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>b</w:t></w:r></w:p></w:tc></w:tr></w:tbl>
            <w:p><w:pPr><w:tabs><w:tab w:val="left" w:pos="720"/></w:tabs></w:pPr><w:r><w:t>Before</w:t><w:br w:type="page"/><w:t>Second</w:t></w:r></w:p>
            <w:p><w:r><w:t xml:space="preserve">Intro </w:t></w:r><w:r><mc:AlternateContent xmlns:mc="m"><mc:Choice><w:txbxContent><w:p><w:r><w:t>Box</w:t></w:r></w:p></w:txbxContent></mc:Choice><mc:Fallback><w:txbxContent><w:p><w:r><w:t>Box</w:t></w:r></w:p></w:txbxContent></mc:Fallback></mc:AlternateContent></w:r><w:r><w:t>end</w:t></w:r></w:p>
            <w:p><w:r><w:br w:type="page"/></w:r></w:p>
            <w:p><w:r><w:lastRenderedPageBreak/><w:t>Third</w:t></w:r></w:p>
            </w:body></w:document>""")
        // A tab stop is not text; a text box's words count once, in its paragraph; a page break splits where it is, and
        // Word's explicit break followed by its rendered one is one new page.
        assertEquals(listOf(DocUnit("P1", listOf("Title", "kept added", "a | b", "Before")), DocUnit("P2", listOf("Second", "Intro Boxend")),
            DocUnit("P3", listOf("Third"))), DocumentText.units("docx", doc, Names))
    }

    @Test fun aDeckSlideBySlideInItsOrderWithNotes() {
        val p = "xmlns:p=\"http://schemas.openxmlformats.org/presentationml/2006/main\" xmlns:a=\"http://schemas.openxmlformats.org/drawingml/2006/main\" " +
            "xmlns:r=\"http://schemas.openxmlformats.org/officeDocument/2006/relationships\""
        val rel = "xmlns=\"http://schemas.openxmlformats.org/package/2006/relationships\""
        fun slide(vararg paras: String) = "<p:sld $p><p:cSld><p:spTree><p:sp><p:txBody>" +
            paras.joinToString("") { "<a:p><a:r><a:t>$it</a:t></a:r></a:p>" } + "</p:txBody></p:sp></p:spTree></p:cSld></p:sld>"
        val deck = zip(
            "ppt/presentation.xml" to "<p:presentation $p><p:sldIdLst><p:sldId id=\"256\" r:id=\"rId2\"/><p:sldId id=\"257\" r:id=\"rId1\"/></p:sldIdLst></p:presentation>",
            "ppt/_rels/presentation.xml.rels" to "<Relationships $rel><Relationship Id=\"rId1\" Target=\"slides/slide1.xml\"/><Relationship Id=\"rId2\" Target=\"slides/slide2.xml\"/></Relationships>",
            "ppt/slides/slide1.xml" to slide("Later"),
            "ppt/slides/slide2.xml" to slide("First", "Point"),
            "ppt/slides/_rels/slide2.xml.rels" to "<Relationships $rel><Relationship Id=\"rId9\" Type=\"http://schemas.openxmlformats.org/officeDocument/2006/relationships/notesSlide\" Target=\"../notesSlides/notesSlide1.xml\"/></Relationships>",
            "ppt/notesSlides/notesSlide1.xml" to "<p:notes $p><p:cSld><p:spTree><p:sp><p:txBody><a:p><a:r><a:t>Say hi</a:t></a:r></a:p></p:txBody></p:sp></p:spTree></p:cSld></p:notes>",
        )
        assertEquals(listOf(DocUnit("S1", listOf("First", "Point", "[notes]", "Say hi")), DocUnit("S2", listOf("Later"))),
            DocumentText.units("pptx", deck, Names))
    }

    @Test fun pptxOutOfRangeIndentLevelsClampSafelyWithoutCrashOrTruncation() {
        val p = "xmlns:p=\"http://schemas.openxmlformats.org/presentationml/2006/main\" xmlns:a=\"http://schemas.openxmlformats.org/drawingml/2006/main\" " +
            "xmlns:r=\"http://schemas.openxmlformats.org/officeDocument/2006/relationships\""
        val rel = "xmlns=\"http://schemas.openxmlformats.org/package/2006/relationships\""
        val slideXml = """<p:sld $p><p:cSld><p:spTree><p:sp><p:txBody>
            <a:p><a:pPr lvl="-3"/><a:r><a:t>Negative</a:t></a:r></a:p>
            <a:p><a:pPr lvl="2"/><a:r><a:t>Normal</a:t></a:r></a:p>
            <a:p><a:pPr lvl="2000000000"/><a:r><a:t>Huge</a:t></a:r></a:p>
            <a:p><a:r><a:t>After</a:t></a:r></a:p>
        </p:txBody></p:sp></p:spTree></p:cSld></p:sld>"""
        val deck = zip(
            "ppt/presentation.xml" to "<p:presentation $p><p:sldIdLst><p:sldId id=\"256\" r:id=\"rId1\"/></p:sldIdLst></p:presentation>",
            "ppt/_rels/presentation.xml.rels" to "<Relationships $rel><Relationship Id=\"rId1\" Target=\"slides/slide1.xml\"/></Relationships>",
            "ppt/slides/slide1.xml" to slideXml,
        )
        assertEquals(
            listOf(DocUnit("S1", listOf("Negative", "    Normal", "  ".repeat(8) + "Huge", "After"))),
            DocumentText.units("pptx", deck, Names),
        )
    }

    @Test fun docxTableCellParagraphsJoinWithSpacesAndHandleNonPrefixedPageBreak() {
        val doc = zip("word/document.xml" to """<document xmlns="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><body>
            <tbl><tr ><tc>
                <p><r><t>Line 1</t></r></p>
                <p/>
                <p><r><t>Line 2</t></r></p>
            </tc><tc>
                <p><r><t>Single</t></r></p>
            </tc></tr></tbl>
            <p><r><t>Page one</t><br type="page"/><t>Page two</t></r></p>
        </body></document>""")
        assertEquals(
            listOf(
                DocUnit("P1", listOf("Line 1 Line 2 | Single", "Page one")),
                DocUnit("P2", listOf("Page two")),
            ),
            DocumentText.units("docx", doc, Names),
        )
    }

    @Test fun aWorkbookSheetBySheet() {
        val units = DocumentText.units("xlsx", SheetsTest.workbook(), Names)
        assertTrue(units.isNotEmpty())
        assertTrue(units.all { it.label.startsWith("T:") })
        assertTrue(units.first().lines.isNotEmpty())
        // A row's cells are joined like a table's, so a changed cell is shown on its own.
        assertTrue(units.first().lines.all { DocumentCompare.ROW_SEP in it })
    }

    @Test fun sparseWideXlsxCapsPaddingAndFailsCleanlyBeforeOom() {
        val workbook = """<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Wide" sheetId="1" r:id="rId1"/></sheets></workbook>"""
        val rels = """<Relationships><Relationship Id="rId1" Target="worksheets/sheet1.xml"/></Relationships>"""

        // Moderate row count with one stray cell at XFD1 (col 16384): succeeds with short rows padded to 128 cols, not 16384.
        val moderateXml = """<worksheet><sheetData>
            <row r="1"><c r="A1" t="inlineStr"><is><t>a</t></is></c><c r="XFD1" t="inlineStr"><is><t>far</t></is></c></row>
            <row r="2"><c r="A2" t="inlineStr"><is><t>b</t></is></c></row>
        </sheetData></worksheet>"""
        val moderate = DocumentText.units("xlsx", zip("xl/workbook.xml" to workbook, "xl/_rels/workbook.xml.rels" to rels, "xl/worksheets/sheet1.xml" to moderateXml), Names).single()
        assertEquals(2, moderate.lines.size)
        assertTrue(moderate.lines[0].endsWith("far"))
        assertEquals(128, moderate.lines[1].split(DocumentCompare.ROW_SEP).size)

        // Many rows with one stray cell at XFD1: hits the character budget cleanly with CompareFailure(too_big) instead of multi-GB OOM.
        val bombXml = buildString {
            append("<worksheet><sheetData>")
            append("<row r=\"1\"><c r=\"A1\"><v>1</v></c><c r=\"XFD1\"><v>1</v></c></row>")
            for (r in 2..30_000) append("<row r=\"$r\"><c r=\"A$r\"><v>1</v></c></row>")
            append("</sheetData></worksheet>")
        }
        val failure = runCatching {
            DocumentText.units("xlsx", zip("xl/workbook.xml" to workbook, "xl/_rels/workbook.xml.rels" to rels, "xl/worksheets/sheet1.xml" to bombXml), Names)
        }.exceptionOrNull() as? CompareFailure
        assertEquals(R.string.files_compare_too_big_file, failure?.reason)
    }

    @Test fun pptxRepeatedSlideReferencesFailCleanlyInsteadOfAmplifying() {
        val p = "xmlns:p=\"http://schemas.openxmlformats.org/presentationml/2006/main\" xmlns:a=\"http://schemas.openxmlformats.org/drawingml/2006/main\" " +
            "xmlns:r=\"http://schemas.openxmlformats.org/officeDocument/2006/relationships\""
        val rel = "xmlns=\"http://schemas.openxmlformats.org/package/2006/relationships\""
        val relsXml = "<Relationships $rel><Relationship Id=\"rId1\" Target=\"slides/slide1.xml\"/></Relationships>"
        val smallSlide = "<p:sld $p><p:cSld><p:spTree><p:sp><p:txBody><a:p><a:r><a:t>Hello</a:t></a:r></a:p></p:txBody></p:sp></p:spTree></p:cSld></p:sld>"

        // A few repeated slide references work via memoization.
        val smallDeck = zip(
            "ppt/presentation.xml" to "<p:presentation $p><p:sldIdLst>" + (1..3).joinToString("") { "<p:sldId id=\"${255 + it}\" r:id=\"rId1\"/>" } + "</p:sldIdLst></p:presentation>",
            "ppt/_rels/presentation.xml.rels" to relsXml,
            "ppt/slides/slide1.xml" to smallSlide,
        )
        assertEquals(listOf(DocUnit("S1", listOf("Hello")), DocUnit("S2", listOf("Hello")), DocUnit("S3", listOf("Hello"))),
            DocumentText.units("pptx", smallDeck, Names))

        // Over MAX_SLIDES repeated <p:sldId>: rejected immediately as too big.
        val manyRefsPres = buildString {
            append("<p:presentation ").append(p).append("><p:sldIdLst>")
            for (i in 1..2_500) append("<p:sldId id=\"").append(255 + i).append("\" r:id=\"rId1\"/>")
            append("</p:sldIdLst></p:presentation>")
        }
        val manyFailure = runCatching {
            DocumentText.units("pptx", zip("ppt/presentation.xml" to manyRefsPres, "ppt/_rels/presentation.xml.rels" to relsXml, "ppt/slides/slide1.xml" to smallSlide), Names)
        }.exceptionOrNull() as? CompareFailure
        assertEquals(R.string.files_compare_too_big_file, manyFailure?.reason)

        // Under MAX_SLIDES but amplified past MAX_COMPARE_CHARS: rejected cleanly without retaining tens of MB.
        val largeText = "a".repeat(20_000)
        val largeSlide = "<p:sld $p><p:cSld><p:spTree><p:sp><p:txBody><a:p><a:r><a:t>$largeText</a:t></a:r></a:p></p:txBody></p:sp></p:spTree></p:cSld></p:sld>"
        val amplifiedPres = buildString {
            append("<p:presentation ").append(p).append("><p:sldIdLst>")
            for (i in 1..500) append("<p:sldId id=\"").append(255 + i).append("\" r:id=\"rId1\"/>")
            append("</p:sldIdLst></p:presentation>")
        }
        val ampFailure = runCatching {
            DocumentText.units("pptx", zip("ppt/presentation.xml" to amplifiedPres, "ppt/_rels/presentation.xml.rels" to relsXml, "ppt/slides/slide1.xml" to largeSlide), Names)
        }.exceptionOrNull() as? CompareFailure
        assertEquals(R.string.files_compare_too_big_file, ampFailure?.reason)
    }

    @Test fun docxStreamingBoundsAbortSingleParagraphAndRowBombs() {
        val w = "xmlns:w=\"http://schemas.openxmlformats.org/wordprocessingml/2006/main\""
        // Full default 8M budget exceeded inside a single huge <w:t> paragraph (never waits for </w:p>).
        val hugePara = zip("word/document.xml" to "<w:document $w><w:body><w:p><w:r><w:t>${"x".repeat(8_050_000)}</w:t></w:r></w:p></w:body></w:document>")
        val hugeFailure = runCatching { DocumentText.units("docx", hugePara, Names) }.exceptionOrNull() as? CompareFailure
        assertEquals(R.string.files_compare_too_big_file, hugeFailure?.reason)

        // Whitespace-only single paragraph and empty <w:tc/> row bomb abort before </w:p> or </w:tr> even when add() would never run.
        val blankPara = zip("word/document.xml" to "<w:document $w><w:body><w:p><w:r><w:t>${" ".repeat(1_200)}</w:t></w:r></w:p></w:body></w:document>")
        val blankFailure = runCatching { DocumentText.units("docx", blankPara, Names, maxChars = 1_000L) }.exceptionOrNull() as? CompareFailure
        assertEquals(R.string.files_compare_too_big_file, blankFailure?.reason)

        val tcBomb = zip("word/document.xml" to "<w:document $w><w:body><w:tbl><w:tr>${"<w:tc/>".repeat(400)}</w:tr></w:tbl></w:body></w:document>")
        val tcFailure = runCatching { DocumentText.units("docx", tcBomb, Names, maxChars = 1_000L) }.exceptionOrNull() as? CompareFailure
        assertEquals(R.string.files_compare_too_big_file, tcFailure?.reason)

        // Nested table paragraphs inside one <w:tc> also respect the streaming budget.
        val nestedCell = zip("word/document.xml" to "<w:document $w><w:body><w:tbl><w:tr><w:tc><w:tbl><w:tr><w:tc>" +
            "<w:p><w:r><w:t>abcd</w:t></w:r></w:p>".repeat(250) +
            "</w:tc></w:tr></w:tbl></w:tc></w:tr></w:tbl></w:body></w:document>")
        val nestedFailure = runCatching { DocumentText.units("docx", nestedCell, Names, maxChars = 1_000L) }.exceptionOrNull() as? CompareFailure
        assertEquals(R.string.files_compare_too_big_file, nestedFailure?.reason)
    }

    @Test fun pptxStreamingBoundsMemoizedNotesAndNormalizedAliases() {
        val p = "xmlns:p=\"http://schemas.openxmlformats.org/presentationml/2006/main\" xmlns:a=\"http://schemas.openxmlformats.org/drawingml/2006/main\" " +
            "xmlns:r=\"http://schemas.openxmlformats.org/officeDocument/2006/relationships\""
        val rel = "xmlns=\"http://schemas.openxmlformats.org/package/2006/relationships\""
        val notesType = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/notesSlide"

        // Single-paragraph text bomb, blank <a:br/> bomb, and empty <a:tc/> row bomb abort inside SAX.
        val oneSlidePres = "<p:presentation $p><p:sldIdLst><p:sldId id=\"256\" r:id=\"rId1\"/></p:sldIdLst></p:presentation>"
        val oneSlideRel = "<Relationships $rel><Relationship Id=\"rId1\" Target=\"slides/slide1.xml\"/></Relationships>"
        val paraBomb = zip(
            "ppt/presentation.xml" to oneSlidePres,
            "ppt/_rels/presentation.xml.rels" to oneSlideRel,
            "ppt/slides/slide1.xml" to "<p:sld $p><p:cSld><p:spTree><p:sp><p:txBody><a:p><a:r><a:t>${" ".repeat(1_200)}</a:t></a:r></a:p></p:txBody></p:sp></p:spTree></p:cSld></p:sld>",
        )
        assertEquals(R.string.files_compare_too_big_file,
            (runCatching { DocumentText.units("pptx", paraBomb, Names, maxChars = 1_000L) }.exceptionOrNull() as? CompareFailure)?.reason)

        val brBomb = zip(
            "ppt/presentation.xml" to oneSlidePres,
            "ppt/_rels/presentation.xml.rels" to oneSlideRel,
            "ppt/slides/slide1.xml" to "<p:sld $p><p:cSld><p:spTree><p:sp><p:txBody><a:p>${"<a:br/>".repeat(1_200)}</a:p></p:txBody></p:sp></p:spTree></p:cSld></p:sld>",
        )
        assertEquals(R.string.files_compare_too_big_file,
            (runCatching { DocumentText.units("pptx", brBomb, Names, maxChars = 1_000L) }.exceptionOrNull() as? CompareFailure)?.reason)

        val tcBomb = zip(
            "ppt/presentation.xml" to oneSlidePres,
            "ppt/_rels/presentation.xml.rels" to oneSlideRel,
            "ppt/slides/slide1.xml" to "<p:sld $p><p:cSld><p:spTree><p:graphicFrame><a:tbl><a:tr>${"<a:tc/>".repeat(400)}</a:tr></a:tbl></p:graphicFrame></p:spTree></p:cSld></p:sld>",
        )
        assertEquals(R.string.files_compare_too_big_file,
            (runCatching { DocumentText.units("pptx", tcBomb, Names, maxChars = 1_000L) }.exceptionOrNull() as? CompareFailure)?.reason)

        // Distinct rIds resolving to the same normalized slide path share slideCache, and distinct slides sharing one notesSlide share notesCache.
        val sharedDeck = zip(
            "ppt/presentation.xml" to "<p:presentation $p><p:sldIdLst><p:sldId id=\"256\" r:id=\"rId1\"/><p:sldId id=\"257\" r:id=\"rId2\"/><p:sldId id=\"258\" r:id=\"rId3\"/></p:sldIdLst></p:presentation>",
            "ppt/_rels/presentation.xml.rels" to "<Relationships $rel>" +
                "<Relationship Id=\"rId1\" Target=\"slides/slide1.xml\"/>" +
                "<Relationship Id=\"rId2\" Target=\"slides/../slides/slide1.xml\"/>" +
                "<Relationship Id=\"rId3\" Target=\"slides/slide2.xml\"/>" +
                "</Relationships>",
            "ppt/slides/slide1.xml" to "<p:sld $p><p:cSld><p:spTree><p:sp><p:txBody><a:p><a:r><a:t>One</a:t></a:r></a:p></p:txBody></p:sp></p:spTree></p:cSld></p:sld>",
            "ppt/slides/slide2.xml" to "<p:sld $p><p:cSld><p:spTree><p:sp><p:txBody><a:p><a:r><a:t>Two</a:t></a:r></a:p></p:txBody></p:sp></p:spTree></p:cSld></p:sld>",
            "ppt/slides/_rels/slide1.xml.rels" to "<Relationships $rel><Relationship Id=\"n1\" Type=\"$notesType\" Target=\"../notesSlides/notesSlide1.xml\"/></Relationships>",
            "ppt/slides/_rels/slide2.xml.rels" to "<Relationships $rel><Relationship Id=\"n2\" Type=\"$notesType\" Target=\"../notesSlides/../notesSlides/notesSlide1.xml\"/></Relationships>",
            "ppt/notesSlides/notesSlide1.xml" to "<p:notes $p><p:cSld><p:spTree><p:sp><p:txBody><a:p><a:r><a:t>Shared note</a:t></a:r></a:p></p:txBody></p:sp></p:spTree></p:cSld></p:notes>",
        )
        assertEquals(
            listOf(
                DocUnit("S1", listOf("One", "[notes]", "Shared note")),
                DocUnit("S2", listOf("One", "[notes]", "Shared note")),
                DocUnit("S3", listOf("Two", "[notes]", "Shared note")),
            ),
            DocumentText.units("pptx", sharedDeck, Names),
        )

        // Distinct slides pointing to one shared notesSlide still respect the total character budget.
        val sharedNoteParts = mutableListOf<Pair<String, String>>()
        val presIds = buildString {
            append("<p:presentation ").append(p).append("><p:sldIdLst>")
            for (i in 1..20) append("<p:sldId id=\"").append(255 + i).append("\" r:id=\"rId").append(i).append("\"/>")
            append("</p:sldIdLst></p:presentation>")
        }
        val presRels = buildString {
            append("<Relationships ").append(rel).append('>')
            for (i in 1..20) append("<Relationship Id=\"rId").append(i).append("\" Target=\"slides/slide").append(i).append(".xml\"/>")
            append("</Relationships>")
        }
        sharedNoteParts += "ppt/presentation.xml" to presIds
        sharedNoteParts += "ppt/_rels/presentation.xml.rels" to presRels
        sharedNoteParts += "ppt/notesSlides/notesSlide1.xml" to "<p:notes $p><p:cSld><p:spTree><p:sp><p:txBody><a:p><a:r><a:t>${"n".repeat(100)}</a:t></a:r></a:p></p:txBody></p:sp></p:spTree></p:cSld></p:notes>"
        for (i in 1..20) {
            sharedNoteParts += "ppt/slides/slide$i.xml" to "<p:sld $p><p:cSld><p:spTree><p:sp><p:txBody><a:p><a:r><a:t>S$i</a:t></a:r></a:p></p:txBody></p:sp></p:spTree></p:cSld></p:sld>"
            sharedNoteParts += "ppt/slides/_rels/slide$i.xml.rels" to "<Relationships $rel><Relationship Id=\"n1\" Type=\"$notesType\" Target=\"../notesSlides/notesSlide1.xml\"/></Relationships>"
        }
        assertEquals(R.string.files_compare_too_big_file,
            (runCatching { DocumentText.units("pptx", zip(*sharedNoteParts.toTypedArray()), Names, maxChars = 1_000L) }.exceptionOrNull() as? CompareFailure)?.reason)

        // Over-limit .rels part is rejected while streaming instead of building an unbounded attribute map list.
        val hugeRels = buildString {
            append("<Relationships ").append(rel).append('>')
            for (i in 1..10_050) append("<Relationship Id=\"r").append(i).append("\" Target=\"slides/s").append(i).append(".xml\"/>")
            append("</Relationships>")
        }
        val relsBomb = zip("ppt/presentation.xml" to oneSlidePres, "ppt/_rels/presentation.xml.rels" to hugeRels)
        assertEquals(R.string.files_compare_too_big_file,
            (runCatching { DocumentText.units("pptx", relsBomb, Names) }.exceptionOrNull() as? CompareFailure)?.reason)
    }
}
