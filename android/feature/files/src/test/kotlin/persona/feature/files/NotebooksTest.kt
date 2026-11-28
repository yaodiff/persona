package persona.feature.files

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.feature.files.Notebooks.CellType
import persona.feature.files.Notebooks.Output

class NotebooksTest {
    private val more = { lines: Int -> "… 还有 $lines 行没有显示" }

    @Test fun readsCellsAndOutputs() {
        val nb = Notebooks.parse(SAMPLE, more)
        assertEquals("Python 3", nb.kernel)
        assertEquals("python", nb.language)
        assertEquals("4.5", nb.format)
        assertEquals(listOf(CellType.Markdown, CellType.Code, CellType.Code, CellType.Raw), nb.cells.map { it.type })
        assertEquals("# 标题\n正文 ![x](attachment:a.png)", nb.cells[0].source)
        assertEquals(mapOf("a.png" to "iVBORw0KGgo="), nb.cells[0].attachments)
        val code = nb.cells[1]
        assertEquals(3, code.count)
        assertEquals("print('hi')\n1 + 1", code.source)
        // Two stdout streams merge, stderr stays apart; the picture beats text/plain; the table HTML becomes text.
        assertEquals(listOf(
            Output.Stream(false, "hi\nthere\n"),
            Output.Stream(true, "warn\n"),
            Output.Image("image/png", "iVBORw0KGgo="),
            Output.Html("a  b\n1  2"),
            Output.Text("2"),
        ), code.outputs)
        val error = nb.cells[2].outputs.single() as Output.Error
        assertEquals("ZeroDivisionError", error.name)
        assertEquals("division by zero", error.value)
        assertEquals("Traceback\nline 1", error.traceback)
        assertEquals(null, nb.cells[2].count)
    }

    @Test fun readsNbformat3Worksheets() {
        val nb = Notebooks.parse("""{"nbformat":3,"nbformat_minor":0,"metadata":{"language":"Julia"},"worksheets":[{"cells":[
            {"cell_type":"heading","level":2,"source":"小节"},
            {"cell_type":"code","input":"x = 1","prompt_number":1,"outputs":[
              {"output_type":"stream","stream":"stderr","text":"warn\n"},
              {"output_type":"pyout","prompt_number":1,"text":["1"],"metadata":{}},
              {"output_type":"display_data","png":"iVBORw0KGgo=","text":["<Figure>"]},
              {"output_type":"display_data","html":["<b>hi</b>"]}]}]}]}""", more)
        assertEquals("julia", nb.language)
        assertEquals("## 小节", nb.cells[0].source)
        assertEquals(1, nb.cells[1].count)
        // nbformat 3's short keys and its stream field.
        assertEquals(listOf(Output.Stream(true, "warn\n"), Output.Text("1"), Output.Image("image/png", "iVBORw0KGgo="), Output.Html("hi")),
            nb.cells[1].outputs)
    }

    @Test fun latexBecomesDisplayMath() {
        val nb = Notebooks.parse("""{"nbformat":4,"cells":[{"cell_type":"code","source":"","outputs":[
            {"output_type":"execute_result","data":{"text/latex":"x^2"}}]}]}""", more)
        assertEquals(listOf(Output.Markdown("$$\nx^2\n$$")), nb.cells[0].outputs)
    }

    @Test fun notANotebookSaysWhy() {
        val bad = runCatching { Notebooks.parse("{oops", more) }.exceptionOrNull()
        assertTrue(bad is Notebooks.Invalid)
        val noCells = runCatching { Notebooks.parse("{}", more) }.exceptionOrNull()
        assertEquals(R.string.files_notebook_no_cells, (noCells as? Notebooks.Invalid)?.reason)
    }

    @Test fun capsLongOutput() {
        val text = (1..5).joinToString("\n")
        assertEquals("1\n2\n… 还有 3 行没有显示", Notebooks.capLines(text, more, 2))
        assertEquals(text, Notebooks.capLines(text, more, 10))
        // Exact line count with a trailing newline must not claim a hidden line.
        assertEquals("1\n2\n", Notebooks.capLines("1\n2\n", more, 2))
        assertEquals("1\n2\n… 还有 1 行没有显示", Notebooks.capLines("1\n2\n3\n", more, 2))
        assertEquals("1\n2\n… 还有 2 行没有显示", Notebooks.capLines("1\n2\n3\n4\n", more, 2))
        assertEquals("1\n2\n… 还有 1 行没有显示", Notebooks.capLines("1\n2\n\n", more, 2))
        assertEquals("1\r\n2\r\n", Notebooks.capLines("1\r\n2\r\n", more, 2))
        assertEquals("1\r\n2\r\n… 还有 1 行没有显示", Notebooks.capLines("1\r\n2\r\n3\r\n", more, 2))
    }

    @Test fun capsMergedConsecutiveStreamChunksAtMaxOutputLines() {
        val total = Notebooks.MAX_OUTPUT_LINES + 5
        val singleLineChunks = (1..total).joinToString(",") {
            """{"output_type":"stream","name":"stdout","text":"line $it\n"}"""
        }
        val nb = Notebooks.parse("""{"nbformat":4,"cells":[{"cell_type":"code","source":"","outputs":[$singleLineChunks]}]}""", more)
        val merged = nb.cells.single().outputs.single() as Output.Stream
        assertEquals(false, merged.stderr)
        val lines = merged.text.split('\n')
        assertEquals(Notebooks.MAX_OUTPUT_LINES + 1, lines.size)
        assertEquals("line 1", lines.first())
        assertEquals("line ${Notebooks.MAX_OUTPUT_LINES}", lines[Notebooks.MAX_OUTPUT_LINES - 1])
        assertEquals("… 还有 5 行没有显示", lines.last())

        // A long stream chunk followed by another same-stream chunk must merge before capping (no trailing lines after notice),
        // while alternating stdout -> stderr -> stdout stays separate and split ANSI sequences strip cleanly across chunks.
        val firstChunk = (1..(Notebooks.MAX_OUTPUT_LINES + 2)).joinToString("") { "a$it\\n" }
        val secondChunk = (1..3).joinToString("") { "b$it\\n" }
        val multi = Notebooks.parse(
            """{"nbformat":4,"cells":[{"cell_type":"code","source":"","outputs":[
                {"output_type":"stream","name":"stdout","text":"$firstChunk"},
                {"output_type":"stream","name":"stdout","text":"$secondChunk"},
                {"output_type":"stream","name":"stderr","text":"err1\nerr2\n"},
                {"output_type":"stream","name":"stdout","text":"\u001b[3"},
                {"output_type":"stream","name":"stdout","text":"1mred\u001b[0m\n"}
            ]}]}""",
            more,
        )
        val outputs = multi.cells.single().outputs
        assertEquals(3, outputs.size)
        val stdout = outputs[0] as Output.Stream
        assertEquals(false, stdout.stderr)
        assertTrue(stdout.text.endsWith("… 还有 5 行没有显示"))
        assertEquals(Output.Stream(true, "err1\nerr2\n"), outputs[1])
        assertEquals(Output.Stream(false, "red\n"), outputs[2])

        // Exact MAX_OUTPUT_LINES with trailing newline must remain uncapped.
        val exact = (1..Notebooks.MAX_OUTPUT_LINES).joinToString("") { "x$it\\n" }
        val exactNb = Notebooks.parse(
            """{"nbformat":4,"cells":[{"cell_type":"code","source":"","outputs":[
                {"output_type":"stream","name":"stdout","text":"$exact"}
            ]}]}""",
            more,
        )
        val exactOut = exactNb.cells.single().outputs.single() as Output.Stream
        assertTrue(!exactOut.text.contains("还有"))
    }

    @Test fun stripsAnsi() {
        assertEquals("Error red", Notebooks.stripAnsi("\u001b[0;31mError\u001b[0m red"))
    }

    @Test fun numericEntities() {
        assertEquals("it's — ok", Notebooks.htmlText("it&#39;s &#x2014; ok"))
    }

    @Test fun htmlText() {
        assertEquals("a & b\nc", Notebooks.htmlText("<style>x{}</style><p>a &amp; b</p><div>c</div>"))
        // A table's columns line up in the mono face, CJK counting two.
        assertEquals("    地区  数量\n0   华东  1200\n10  华南  -80", Notebooks.htmlText(
            "<table><tr><th></th><th>地区</th><th>数量</th></tr><tr><td>0</td><td>华东</td><td>1200</td></tr><tr><td>10</td><td>华南</td><td>-80</td></tr></table>"))
    }

    @Test fun htmlEntitiesDoNotDoubleDecode() {
        assertEquals(
            "&lt; &quot; &lt; &#60; &#60; && &foo; &amp &#; &#x; &#99999999999999999999; &#1114112; &#0; &#x0; &#xD800; &#xDFFF; \uD83D\uDE00",
            Notebooks.htmlText("&#38;lt; &#x26;quot; &amp;lt; &#38;#60; &amp;#60; &&amp; &foo; &amp &#; &#x; &#99999999999999999999; &#1114112; &#0; &#x0; &#xD800; &#xDFFF; &#x1F600;"),
        )
    }

    @Test fun htmlScriptAndStyleTagBoundariesAndWhitespaceEndTags() {
        // Custom elements with script-/style- prefixes or letter suffixes must NOT be stripped.
        assertEquals("visible", Notebooks.htmlText("<script-tag>visible</script-tag><script>hidden</script>"))
        assertEquals("shown", Notebooks.htmlText("<style-guide>shown</style-guide><style>hidden</style>"))
        assertEquals("keep1 keep2", Notebooks.htmlText("<scriptx>keep1</scriptx> <stylesheet>keep2</stylesheet>"))
        // Valid HTML5 closing tags with whitespace before '>' must strip the entire block.
        assertEquals("after", Notebooks.htmlText("<SCRIPT type=\"text/javascript\">alert(1)</ScRiPt \n>after"))
        assertEquals("ok", Notebooks.htmlText("<style>/* </script> */</style\t>ok"))
        assertEquals("done", Notebooks.htmlText("<script>x</script-fake>y</script >done"))
    }

    @Test fun htmlTableAlignmentBoundsMemoryAndCapsBeforeAlign() {
        // Previously: 1 wide cell (100k chars) + 5,000 short 2-col rows allocated ~1 GB of padding spaces in alignColumns.
        val wide = "x".repeat(100_000)
        val rows = buildString {
            append("<table><tr><td>").append(wide).append("</td><td>end</td></tr>")
            repeat(5_000) { append("<tr><td>a</td><td>b</td></tr>") }
            append("</table>")
        }
        val nb = Notebooks.parse(
            """{"nbformat":4,"cells":[{"cell_type":"code","source":"","outputs":[
                {"output_type":"display_data","data":{"text/html":"$rows"}}
            ]}]}""",
            more,
        )
        val htmlOut = nb.cells.single().outputs.single() as Output.Html
        val outLines = htmlOut.text.split('\n')
        assertEquals(Notebooks.MAX_OUTPUT_LINES + 1, outLines.size)
        assertEquals("$wide  end", outLines.first())
        assertEquals("… 还有 3001 行没有显示", outLines.last())
        assertTrue("capped table output must remain bounded: ${htmlOut.text.length}", htmlOut.text.length < 350_000)

        // Direct htmlText() without line cap also bounds total alignment padding (200k char budget).
        val direct = Notebooks.htmlText(rows)
        assertTrue("direct htmlText padding must remain bounded: ${direct.length}", direct.length < 400_000)
        assertTrue(direct.startsWith("$wide  end\n"))

        // Multiple HTML table outputs in one notebook share the notebook-level padding budget.
        val mediumTable = buildString {
            append("<table><tr><td>").append("w".repeat(80)).append("</td><td>z</td></tr>")
            repeat(1_000) { append("<tr><td>a</td><td>b</td></tr>") }
            append("</table>")
        }
        val manyOutputs = (1..20).joinToString(",") {
            """{"output_type":"display_data","data":{"text/html":"$mediumTable"}}"""
        }
        val multiNb = Notebooks.parse(
            """{"nbformat":4,"cells":[{"cell_type":"code","source":"","outputs":[$manyOutputs]}]}""",
            more,
        )
        val totalChars = multiNb.cells.single().outputs.sumOf { (it as Output.Html).text.length }
        assertTrue("shared notebook padding budget must bound multi-output total: $totalChars", totalChars < 400_000)

        // Capping right on a collapsed blank line preserves the blank line and exact hidden count.
        val blankAtCap = buildString {
            repeat(Notebooks.MAX_OUTPUT_LINES - 1) { i -> append("<p>r$i</p>") }
            append("<p></p><p></p><p>after</p>")
        }
        val blankNb = Notebooks.parse(
            """{"nbformat":4,"cells":[{"cell_type":"code","source":"","outputs":[
                {"output_type":"display_data","data":{"text/html":"$blankAtCap"}}
            ]}]}""",
            more,
        )
        val blankLines = (blankNb.cells.single().outputs.single() as Output.Html).text.split('\n')
        assertEquals(Notebooks.MAX_OUTPUT_LINES + 1, blankLines.size)
        assertEquals("", blankLines[Notebooks.MAX_OUTPUT_LINES - 1])
        assertEquals("… 还有 1 行没有显示", blankLines.last())

        // Many columns (> MAX_ALIGN_COLUMNS) preserve every cell without quadratic padding.
        val wideCols = buildString {
            append("<table>")
            repeat(100) { r ->
                append("<tr>")
                repeat(100) { c -> append("<td>").append(if (r == 0) "col$c" else "v").append("</td>") }
                append("</tr>")
            }
            append("</table>")
        }
        val wideColsOut = Notebooks.htmlText(wideCols)
        assertEquals(100, wideColsOut.lines().size)
        assertTrue(wideColsOut.lines().first().contains("col99"))
    }

    @Test fun unclosedHtmlTagsAndLargeCapLinesFinishQuickly() {
        // Previously quadratic regex backtracking on repeated unclosed <script> or < characters.
        val unclosedScripts = "<script>".repeat(20_000) + "<style>drop</style>ok"
        assertEquals("ok", Notebooks.htmlText(unclosedScripts))
        val unclosedLt = "<".repeat(50_000) + "tail"
        assertEquals(unclosedLt, Notebooks.htmlText(unclosedLt))

        // capLines counts hidden lines in O(1) extra memory and handles max = 0 and large inputs.
        assertEquals("… 还有 2 行没有显示", Notebooks.capLines("a\nb\n", more, 0))
        assertEquals("", Notebooks.capLines("", more, 0))
        val many = (1..25_000).joinToString("\n")
        val capped = Notebooks.capLines(many, more, 3)
        assertEquals("1\n2\n3\n… 还有 24997 行没有显示", capped)
    }

    companion object {
        val SAMPLE = """{
          "nbformat": 4, "nbformat_minor": 5,
          "metadata": {"kernelspec": {"display_name": "Python 3", "language": "python", "name": "python3"}},
          "cells": [
            {"cell_type": "markdown", "source": ["# 标题\n", "正文 ![x](attachment:a.png)"],
             "attachments": {"a.png": {"image/png": "iVBORw0KGgo="}}},
            {"cell_type": "code", "execution_count": 3, "source": ["print('hi')\n", "1 + 1"], "outputs": [
              {"output_type": "stream", "name": "stdout", "text": ["hi\n"]},
              {"output_type": "stream", "name": "stdout", "text": "there\n"},
              {"output_type": "stream", "name": "stderr", "text": "\u001b[31mwarn\u001b[0m\n"},
              {"output_type": "display_data", "data": {"image/png": "iVBORw0K\nGgo=\n", "text/plain": "<Figure>"}},
              {"output_type": "display_data", "data": {"text/html": "<table><tr><th>a</th><th>b</th></tr><tr><td>1</td><td>2</td></tr></table>", "text/plain": "df"}},
              {"output_type": "execute_result", "execution_count": 3, "data": {"text/plain": "2"}}
            ]},
            {"cell_type": "code", "execution_count": null, "source": "1/0", "outputs": [
              {"output_type": "error", "ename": "ZeroDivisionError", "evalue": "division by zero", "traceback": ["\u001b[31mTraceback\u001b[0m", "line 1"]}
            ]},
            {"cell_type": "raw", "source": "raw text"}
          ]
        }"""
    }
}
