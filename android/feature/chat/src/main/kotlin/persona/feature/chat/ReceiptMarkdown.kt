package persona.feature.chat

/**
 * A task receipt's summary (the agent's last words, Markdown) as text and the spans to style, for one Text that keeps
 * the card's four lines and ellipsis (#1508): a heading is a bold line, a list item a "• " line, code keeps its words,
 * a link its text. Marks a clip left unpaired are dropped. Pure, so tests need no Compose.
 */
internal object ReceiptMarkdown {
    enum class Mark { Strong, Emphasis, Strike, Code, Link }
    data class Span(val mark: Mark, val start: Int, val end: Int, val url: String? = null)
    data class Styled(val text: String, val spans: List<Span>)

    private val fence = Regex("""^\s*(`{3,}|~{3,})""")
    private val rule = Regex("""^\s*(?:[-*_]\s*){3,}$""")
    private val delimiterRow = Regex("""^\s*\|?\s*:?-+:?\s*(?:\|\s*:?-+:?\s*)*\|?\s*$""")
    private val heading = Regex("""^\s*#{1,6}\s+(.*?)(?:\s+#+)?\s*$""")
    private val quote = Regex("""^(?:\s*>)+\s?""")
    private val bullet = Regex("""^(\s*)[-*+]\s+(?:\[[ xX]]\s+)?""")
    private val ordered = Regex("""^(\s*)(\d{1,9}[.)])\s+""")

    fun parse(md: String): Styled {
        val out = StringBuilder()
        val spans = mutableListOf<Span>()
        var inFence: String? = null
        for (raw in md.lines()) {
            val f = fence.find(raw)
            if (f != null && (inFence == null || f.groupValues[1].first() == inFence.first())) {
                inFence = if (inFence == null) f.groupValues[1] else null
                continue
            }
            if (inFence != null) {
                if (raw.isBlank()) continue
                newLine(out)
                val start = out.length
                out.append(raw.trimEnd())
                spans += Span(Mark.Code, start, out.length)
                continue
            }
            if (raw.isBlank() || rule.matches(raw) || (raw.contains('|') && raw.contains('-') && delimiterRow.matches(raw))) continue
            var line = quote.replace(raw, "")
            newLine(out)
            val h = heading.matchEntire(line)
            if (h != null) {
                val start = out.length
                inline(h.groupValues[1], out, spans)
                spans += Span(Mark.Strong, start, out.length)
                continue
            }
            bullet.find(line)?.let {
                out.append("  ".repeat(it.groupValues[1].length / 2)).append("• ")
                line = line.substring(it.range.last + 1)
            } ?: ordered.find(line)?.let {
                out.append("  ".repeat(it.groupValues[1].length / 2)).append(it.groupValues[2]).append(' ')
                line = line.substring(it.range.last + 1)
            }
            if (line.trimStart().startsWith("|")) line = line.trim().removePrefix("|").removeSuffix("|").split('|').joinToString(" · ") { it.trim() }
            inline(line.trim(), out, spans)
        }
        return Styled(out.toString(), spans)
    }

    private fun newLine(out: StringBuilder) {
        if (out.isNotEmpty()) out.append('\n')
    }

    private fun inline(s: String, out: StringBuilder, spans: MutableList<Span>) {
        var i = 0
        while (i < s.length) {
            val c = s[i]
            when {
                c == '\\' && i + 1 < s.length && s[i + 1] in "!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~" -> {
                    out.append(s[i + 1]); i += 2
                }
                c == '`' -> {
                    val n = run { var k = i; while (k < s.length && s[k] == '`') k++; k - i }
                    val close = Regex("(?<!`)`{$n}(?!`)").find(s, i + n)
                    if (close != null) {
                        val start = out.length
                        out.append(s.substring(i + n, close.range.first).trim())
                        spans += Span(Mark.Code, start, out.length)
                        i = close.range.last + 1
                    } else i += n // unpaired: a clip cut the span
                }
                s.startsWith("**", i) || s.startsWith("__", i) || s.startsWith("~~", i) -> {
                    val mark = s.substring(i, i + 2)
                    val close = s.indexOf(mark, i + 2)
                    if (close > i + 2) {
                        val start = out.length
                        inline(s.substring(i + 2, close), out, spans)
                        spans += Span(if (mark == "~~") Mark.Strike else Mark.Strong, start, out.length)
                        i = close + 2
                    } else i += 2
                }
                (c == '*' || c == '_') && i + 1 < s.length && !s[i + 1].isWhitespace() &&
                    (c == '*' || i == 0 || !s[i - 1].isLetterOrDigit()) -> {
                    val close = s.indexOf(c, i + 1)
                    if (close > i + 1 && !s[close - 1].isWhitespace() && (c == '*' || close + 1 >= s.length || !s[close + 1].isLetterOrDigit())) {
                        val start = out.length
                        inline(s.substring(i + 1, close), out, spans)
                        spans += Span(Mark.Emphasis, start, out.length)
                        i = close + 1
                    } else {
                        out.append(c); i++
                    }
                }
                c == '[' || (c == '!' && s.startsWith("![", i)) -> {
                    val open = if (c == '!') i + 1 else i
                    val link = Regex("""\[([^\]\n]*)]\(([^)\s]*)[^)]*\)""").matchAt(s, open)
                    if (link != null) {
                        val start = out.length
                        inline(link.groupValues[1], out, spans)
                        if (c == '[' && link.groupValues[2].isNotEmpty()) spans += Span(Mark.Link, start, out.length, link.groupValues[2])
                        i = link.range.last + 1
                    } else {
                        val clipped = Regex("""\[([^\]\n]+)]\(\S*""").matchAt(s, open)
                        if (clipped != null && clipped.range.last == s.lastIndex) {
                            inline(clipped.groupValues[1], out, spans)
                            i = clipped.range.last + 1
                        } else {
                            out.append(c); i++
                        }
                    }
                }
                c == '<' -> {
                    val auto = Regex("""<(https?://[^\s<>]+)>""").matchAt(s, i)
                    if (auto != null) {
                        val start = out.length
                        out.append(auto.groupValues[1])
                        spans += Span(Mark.Link, start, out.length, auto.groupValues[1])
                        i = auto.range.last + 1
                    } else {
                        out.append(c); i++
                    }
                }
                else -> {
                    out.append(c); i++
                }
            }
        }
    }
}
