package persona.feature.files

import java.util.Locale

// Markdown files as the viewer renders them: README HTML turned into Markdown, bare links ending where the app's
// one rule says (whitespace or any non-ASCII character), and links and images that name files on the machine.

internal object FileMarkdownText {
    /**
     * READMEs use a little HTML (`<img>`, `<a>`, `<br>`, a centred `<p>`): lines that are HTML become Markdown, other
     * tags are dropped and their text kept (iOS `MarkdownHTML`). Fenced code is left alone.
     */
    fun simplifyHtml(markdown: String): String {
        if ('<' !in markdown) return markdown
        val out = ArrayList<String>()
        var fenceChar = '\u0000'
        var fenceLen = 0
        for (line in markdown.split('\n')) {
            if (fenceLen > 0) {
                if (isClosingFence(line, 0, line.length, fenceChar, fenceLen)) {
                    fenceChar = '\u0000'
                    fenceLen = 0
                }
                out += line
                continue
            }
            val open = openingFence(line, 0, line.length)
            if (open != 0) {
                fenceChar = if (open > 0) '`' else '~'
                fenceLen = if (open > 0) open else -open
                out += line
                continue
            }
            val trimmed = line.trim()
            if (trimmed.startsWith("<") && !trimmed.startsWith("<http", ignoreCase = true)) {
                inlineHtml(trimmed).takeIf { it.isNotEmpty() }?.let { out += it }
                continue
            }
            out += line
        }
        return out.joinToString("\n")
    }

    private fun effectiveLineEnd(text: String, lineStart: Int, lineEnd: Int): Int =
        if (lineEnd > lineStart && text[lineEnd - 1] == '\r') lineEnd - 1 else lineEnd

    /**
     * CommonMark §4.5 opening fence check on `text[lineStart until lineEnd]`: 0–3 leading spaces (no tab / 4+ spaces),
     * >= 3 of `` ` `` or `~`, and no backticks in a backtick fence's info string.
     * Returns `+len` for backtick fences, `-len` for tilde fences, or `0` if not an opening fence.
     */
    private fun openingFence(text: String, lineStart: Int, lineEnd: Int): Int {
        val end = effectiveLineEnd(text, lineStart, lineEnd)
        var p = lineStart
        while (p < end && p - lineStart < 3 && text[p] == ' ') p++
        if (p >= end) return 0
        val ch = text[p]
        if (ch != '`' && ch != '~') return 0
        var len = 0
        while (p + len < end && text[p + len] == ch) len++
        if (len < 3) return 0
        if (ch == '`') {
            var q = p + len
            while (q < end) {
                if (text[q] == '`') return 0
                q++
            }
            return len
        }
        return -len
    }

    /** CommonMark §4.5 closing fence check: 0–3 leading spaces, >= [minLen] of [ch], then only spaces/tabs until EOL. */
    private fun isClosingFence(text: String, lineStart: Int, lineEnd: Int, ch: Char, minLen: Int): Boolean {
        val end = effectiveLineEnd(text, lineStart, lineEnd)
        var p = lineStart
        while (p < end && p - lineStart < 3 && text[p] == ' ') p++
        if (p >= end || text[p] != ch) return false
        var len = 0
        while (p + len < end && text[p + len] == ch) len++
        if (len < minLen) return false
        var q = p + len
        while (q < end) {
            val c = text[q]
            if (c != ' ' && c != '\t') return false
            q++
        }
        return true
    }

    private val lineBreak = Regex("<br\\s*/?>", RegexOption.IGNORE_CASE)
    private val spaces = Regex("\\s+")
    private val htmlEntity = Regex("&(nbsp|lt|gt|quot|apos|amp|#([0-9]{1,7})|#x([0-9a-fA-F]{1,6}));", RegexOption.IGNORE_CASE)

    private fun isTagNameEnd(s: String, index: Int): Boolean =
        index < s.length && (s[index] == '>' || s[index] == '/' || s[index].isWhitespace())

    /** Quote-aware O(N) attribute lookup inside an HTML opening tag (handles quoted and unquoted attribute values). */
    private fun attribute(name: String, tag: String): String? {
        val len = tag.length
        var p = 1
        while (p < len && !isTagNameEnd(tag, p)) p++
        while (p < len) {
            while (p < len && (tag[p].isWhitespace() || tag[p] == '/')) p++
            if (p >= len || tag[p] == '>') break
            val nameStart = p
            while (p < len && !tag[p].isWhitespace() && tag[p] != '=' && tag[p] != '>' && tag[p] != '/') p++
            val nameEnd = p
            while (p < len && tag[p].isWhitespace()) p++
            if (p >= len || tag[p] != '=') continue
            p++ // skip '='
            while (p < len && tag[p].isWhitespace()) p++
            if (p >= len || tag[p] == '>') break
            val q = tag[p]
            val valStart: Int
            val valEnd: Int
            if (q == '"' || q == '\'') {
                valStart = p + 1
                val close = tag.indexOf(q, valStart)
                if (close < 0) break
                valEnd = close
                p = close + 1
            } else {
                valStart = p
                while (p < len && !tag[p].isWhitespace() && tag[p] != '>') p++
                valEnd = p
            }
            if (nameEnd - nameStart == name.length && tag.regionMatches(nameStart, name, 0, name.length, ignoreCase = true)) {
                return tag.substring(valStart, valEnd)
            }
        }
        return null
    }

    /** Linear-time `<img>` -> `![alt](src)` replacement without regex backtracking on unclosed `<img`. */
    private fun replaceImages(s: String): String {
        var lt = s.indexOf('<')
        if (lt < 0) return s
        var out: StringBuilder? = null
        var pos = 0
        while (lt >= 0) {
            if (s.regionMatches(lt + 1, "img", 0, 3, ignoreCase = true) && isTagNameEnd(s, lt + 4)) {
                val gt = findTagClose(s, lt + 4)
                if (gt < 0) break
                if (out == null) out = StringBuilder(s.length)
                out.append(s, pos, lt)
                val tag = s.substring(lt, gt + 1)
                attribute("src", tag)?.let { src ->
                    out.append("![").append(attribute("alt", tag).orEmpty()).append("](").append(src).append(')')
                }
                pos = gt + 1
                lt = s.indexOf('<', pos)
            } else {
                lt = s.indexOf('<', lt + 1)
            }
        }
        if (out == null) return s
        out.append(s, pos, s.length)
        return out.toString()
    }

    /** Linear-time `<a ...>...</a>` -> `[text](href)` replacement without backtracking on unclosed `<a` or missing `</a>`. */
    private fun replaceAnchors(s: String): String {
        var lt = s.indexOf('<')
        if (lt < 0) return s
        var out: StringBuilder? = null
        var pos = 0
        while (lt >= 0) {
            if (s.regionMatches(lt + 1, "a", 0, 1, ignoreCase = true) && isTagNameEnd(s, lt + 2)) {
                val gt = findTagClose(s, lt + 2)
                if (gt < 0) break
                val close = findCloseTag(s, "</a", gt + 1) ?: break
                if (out == null) out = StringBuilder(s.length)
                out.append(s, pos, lt)
                val openTag = s.substring(lt, gt + 1)
                val text = stripTags(s.substring(gt + 1, close.first))
                val href = attribute("href", openTag)
                if (href != null) out.append('[').append(text).append("](").append(href).append(')') else out.append(text)
                pos = close.second
                lt = s.indexOf('<', pos)
            } else {
                lt = s.indexOf('<', lt + 1)
            }
        }
        if (out == null) return s
        out.append(s, pos, s.length)
        return out.toString()
    }

    /**
     * Finds the closing `>` of an HTML tag starting at [from] (just after the tag name).
     * Enters quote mode only at the start of an attribute value after `=`, so an apostrophe inside an unquoted
     * attribute value (e.g. `<img alt=don't src="a.png">`) cannot swallow the closing `>`.
     */
    private fun findTagClose(s: String, from: Int): Int {
        val len = s.length
        var p = from
        while (p < len) {
            while (p < len && (s[p].isWhitespace() || s[p] == '/')) p++
            if (p >= len) return -1
            if (s[p] == '>') return p
            while (p < len && !s[p].isWhitespace() && s[p] != '=' && s[p] != '>' && s[p] != '/') p++
            while (p < len && s[p].isWhitespace()) p++
            if (p >= len) return -1
            if (s[p] == '>') return p
            if (s[p] != '=') continue
            p++ // skip '='
            while (p < len && s[p].isWhitespace()) p++
            if (p >= len) return -1
            val c = s[p]
            if (c == '>') return p
            if (c == '"' || c == '\'') {
                val close = s.indexOf(c, p + 1)
                if (close < 0) return -1
                p = close + 1
            } else {
                while (p < len && !s[p].isWhitespace() && s[p] != '>') p++
            }
        }
        return -1
    }

    /** Returns `(startIndex, endExclusiveIndex)` of the next closing tag matching [closePrefix] + optional whitespace + `>`. */
    private fun findCloseTag(s: String, closePrefix: String, from: Int): Pair<Int, Int>? {
        var search = from
        val len = s.length
        while (search < len) {
            val idx = s.indexOf(closePrefix, search, ignoreCase = true)
            if (idx < 0) return null
            var p = idx + closePrefix.length
            while (p < len && s[p].isWhitespace()) p++
            if (p < len && s[p] == '>') return idx to (p + 1)
            search = p
        }
        return null
    }

    /** Linear-time quote-aware `<...>` removal (zero allocation when no closed tag appears). */
    private fun stripTags(s: String): String {
        var lt = s.indexOf('<')
        if (lt < 0) return s
        var out: StringBuilder? = null
        var pos = 0
        while (lt >= 0) {
            var nameEnd = lt + 1
            if (nameEnd < s.length && s[nameEnd] == '/') nameEnd++
            while (nameEnd < s.length && !isTagNameEnd(s, nameEnd)) nameEnd++
            val gt = findTagClose(s, nameEnd)
            if (gt < 0) break
            if (out == null) out = StringBuilder(s.length)
            out.append(s, pos, lt)
            pos = gt + 1
            lt = s.indexOf('<', pos)
        }
        if (out == null) return s
        out.append(s, pos, s.length)
        return out.toString()
    }

    private fun decodeCodePoint(cp: Int?): String? {
        if (cp == null || cp <= 0 || cp > Character.MAX_CODE_POINT) return null
        if (cp in Character.MIN_SURROGATE.code..Character.MAX_SURROGATE.code) return null
        return String(Character.toChars(cp))
    }

    private fun inlineHtml(html: String): String {
        var s = replaceImages(html)
        s = replaceAnchors(s)
        s = lineBreak.replace(s, " ")
        s = stripTags(s)
        if ('&' in s) {
            s = s.replace(htmlEntity) { m ->
                val raw = m.groupValues[1]
                when {
                    raw.startsWith("#x", ignoreCase = true) -> decodeCodePoint(m.groupValues[3].toIntOrNull(16)) ?: m.value
                    raw.startsWith("#") -> decodeCodePoint(m.groupValues[2].toIntOrNull(10)) ?: m.value
                    else -> when (raw.lowercase(Locale.ROOT)) {
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
        }
        return s.split(spaces).filter { it.isNotEmpty() }.joinToString(" ")
    }

    private val bareScheme = Regex("https?://", RegexOption.IGNORE_CASE)

    private class IntList(initialCapacity: Int = 8) {
        private var data = IntArray(initialCapacity)
        var size = 0
            private set
        var cursor = 0

        fun add(value: Int) {
            if (size == data.size) data = data.copyOf(data.size * 2)
            data[size++] = value
        }

        fun pop(): Int = data[--size]
        fun isEmpty(): Boolean = size == 0
        fun clear() {
            size = 0
            cursor = 0
        }

        operator fun get(index: Int): Int = data[index]
    }

    private fun Char.isAsciiPunctuation(): Boolean =
        this in '!'..'/' || this in ':'..'@' || this in '['..'`' || this in '{'..'~'

    /** Single-pass CommonMark §4.5 fenced code block scanner; unclosed fences protect through EOF. */
    private fun findFencedRanges(text: String): ArrayList<IntRange> {
        val out = ArrayList<IntRange>()
        val len = text.length
        var lineStart = 0
        var fenceStart = -1
        var fenceChar = '\u0000'
        var fenceLen = 0
        while (lineStart < len) {
            val nl = text.indexOf('\n', lineStart)
            val lineEnd = if (nl < 0) len else nl
            val nextLineStart = if (nl < 0) len else nl + 1
            if (fenceLen > 0) {
                if (isClosingFence(text, lineStart, lineEnd, fenceChar, fenceLen)) {
                    out.add(fenceStart until nextLineStart)
                    fenceStart = -1
                    fenceChar = '\u0000'
                    fenceLen = 0
                }
            } else {
                val open = openingFence(text, lineStart, lineEnd)
                if (open != 0) {
                    fenceStart = lineStart
                    fenceChar = if (open > 0) '`' else '~'
                    fenceLen = if (open > 0) open else -open
                }
            }
            lineStart = nextLineStart
        }
        if (fenceLen > 0) {
            out.add(fenceStart until len)
        }
        return out
    }

    private fun isBlankLine(text: String, start: Int, end: Int): Boolean {
        for (i in start until end) {
            val c = text[i]
            if (c != ' ' && c != '\t' && c != '\r') return false
        }
        return true
    }

    /**
     * Strictly O(L) CommonMark §6.1 inline code span scanner on `[segStart, segEnd)`.
     * Pre-indexes maximal backtick runs by exact length and advances a monotonic cursor per length so
     * unmatched runs skip in O(1) without allocating repeated marker strings.
     */
    private fun findInlineCodeSpans(text: String, segStart: Int, segEnd: Int): List<IntRange> {
        val firstTick = text.indexOf('`', segStart)
        if (firstTick < 0 || firstTick >= segEnd) return emptyList()
        val runsByLen = HashMap<Int, IntList>()
        var p = firstTick
        while (p < segEnd) {
            if (text[p] == '`') {
                val start = p
                while (p < segEnd && text[p] == '`') p++
                runsByLen.getOrPut(p - start) { IntList() }.add(start)
            } else {
                p++
            }
        }
        val spans = ArrayList<IntRange>()
        var i = segStart
        while (i < segEnd) {
            val c = text[i]
            if (c == '\\' && i + 1 < segEnd && text[i + 1].isAsciiPunctuation()) {
                i += 2
                continue
            }
            if (c == '`') {
                var run = 1
                while (i + run < segEnd && text[i + run] == '`') run++
                val list = runsByLen[run]
                var close = -1
                if (list != null) {
                    val after = i + run
                    while (list.cursor < list.size && list[list.cursor] < after) list.cursor++
                    if (list.cursor < list.size) close = list[list.cursor]
                }
                if (close >= 0) {
                    spans.add(i until (close + run))
                    i = close + run
                } else {
                    i += run
                }
            } else {
                i++
            }
        }
        return spans
    }

    /**
     * Strictly O(L) single-line balanced `(...)` pre-matcher for `[segStart, segEnd)`, skipping inline code spans
     * and backslash-escaped punctuation, and resetting the stack at newlines.
     */
    private fun preMatchParens(text: String, segStart: Int, segEnd: Int, codeSpans: List<IntRange>): HashMap<Int, Int>? {
        val firstParen = text.indexOf('(', segStart)
        if (firstParen < 0 || firstParen >= segEnd) return null
        var map: HashMap<Int, Int>? = null
        val stack = IntList()
        var codeIdx = 0
        var i = segStart
        while (i < segEnd) {
            if (codeIdx < codeSpans.size && i == codeSpans[codeIdx].first) {
                i = codeSpans[codeIdx++].last + 1
                continue
            }
            val c = text[i]
            when {
                c == '\\' && i + 1 < segEnd && text[i + 1].isAsciiPunctuation() -> i += 2
                c == '\n' -> {
                    stack.clear()
                    i++
                }
                c == '(' -> {
                    stack.add(i)
                    i++
                }
                c == ')' -> {
                    if (!stack.isEmpty()) {
                        if (map == null) map = HashMap()
                        map[stack.pop()] = i
                    }
                    i++
                }
                else -> i++
            }
        }
        return map
    }

    private fun mergeRangesInto(local: ArrayList<IntRange>, out: ArrayList<IntRange>) {
        if (local.isEmpty()) return
        if (local.size > 1) {
            local.sortWith(compareBy<IntRange> { it.first }.thenByDescending { it.last })
        }
        var curFirst = local[0].first
        var curLast = local[0].last
        for (idx in 1 until local.size) {
            val r = local[idx]
            if (r.first <= curLast + 1) {
                if (r.last > curLast) curLast = r.last
            } else {
                out.add(curFirst..curLast)
                curFirst = r.first
                curLast = r.last
            }
        }
        out.add(curFirst..curLast)
    }

    private fun scanInlineProtected(text: String, segStart: Int, segEnd: Int, out: ArrayList<IntRange>) {
        val codeSpans = findInlineCodeSpans(text, segStart, segEnd)
        val matchParen = preMatchParens(text, segStart, segEnd, codeSpans)
        val local = ArrayList<IntRange>(codeSpans.size)
        val brackets = IntList()
        var codeIdx = 0
        var i = segStart
        while (i < segEnd) {
            if (codeIdx < codeSpans.size && i == codeSpans[codeIdx].first) {
                val span = codeSpans[codeIdx++]
                local.add(span)
                i = span.last + 1
                continue
            }
            val c = text[i]
            if (c == '\\' && i + 1 < segEnd && text[i + 1].isAsciiPunctuation()) {
                i += 2
                continue
            }
            if (c == '<' &&
                (text.regionMatches(i + 1, "https://", 0, 8, ignoreCase = true) ||
                    text.regionMatches(i + 1, "http://", 0, 7, ignoreCase = true))
            ) {
                var p = i + 1
                var closeGt = -1
                while (p < segEnd) {
                    val ch = text[p]
                    if (ch == '>') {
                        closeGt = p
                        break
                    }
                    if (ch == '<' || ch.isWhitespace()) break
                    p++
                }
                if (closeGt >= 0) {
                    local.add(i..closeGt)
                    i = closeGt + 1
                    while (codeIdx < codeSpans.size && codeSpans[codeIdx].first < i) codeIdx++
                    continue
                }
                i++
                continue
            }
            if (c == '!' && i + 1 < segEnd && text[i + 1] == '[') {
                brackets.add(i)
                i += 2
                continue
            }
            if (c == '[') {
                brackets.add(i)
                i++
                continue
            }
            if (c == ']' && !brackets.isEmpty()) {
                val openStart = brackets.pop()
                if (i + 1 < segEnd && text[i + 1] == '(') {
                    val closeParen = matchParen?.get(i + 1) ?: -1
                    if (closeParen >= 0) {
                        local.add(openStart..closeParen)
                        i = closeParen + 1
                        while (codeIdx < codeSpans.size && codeSpans[codeIdx].first < i) codeIdx++
                        continue
                    }
                }
                i++
                continue
            }
            i++
        }
        mergeRangesInto(local, out)
    }

    private fun scanGapParagraphs(text: String, gapStart: Int, gapEnd: Int, out: ArrayList<IntRange>) {
        var paraStart = -1
        var lineStart = gapStart
        while (lineStart < gapEnd) {
            val nl = text.indexOf('\n', lineStart)
            val lineEnd = if (nl < 0 || nl >= gapEnd) gapEnd else nl
            val nextLineStart = if (nl < 0 || nl >= gapEnd) gapEnd else nl + 1
            if (isBlankLine(text, lineStart, lineEnd)) {
                if (paraStart >= 0) {
                    scanInlineProtected(text, paraStart, lineStart, out)
                    paraStart = -1
                }
            } else if (paraStart < 0) {
                paraStart = lineStart
            }
            lineStart = nextLineStart
        }
        if (paraStart >= 0) {
            scanInlineProtected(text, paraStart, gapEnd, out)
        }
    }

    /**
     * Strictly O(N) scanner for Markdown spans where bare URLs must not be rewritten: CommonMark fenced code
     * (` ``` ` / `~~~`), inline code spans (any exact backtick run length), Markdown links/images (`!?[...](...)`
     * with nested brackets, backslash escapes, and balanced parens), and single-token autolinks (`<http(s)://...>`).
     * Returns sorted, non-overlapping ranges.
     */
    private fun findProtectedRanges(text: String): List<IntRange> {
        val fenced = findFencedRanges(text)
        val out = ArrayList<IntRange>(fenced.size)
        var pos = 0
        for (f in fenced) {
            if (pos < f.first) scanGapParagraphs(text, pos, f.first, out)
            out.add(f)
            pos = f.last + 1
        }
        if (pos < text.length) scanGapParagraphs(text, pos, text.length, out)
        return out
    }

    /** Strictly O(N) removal of trailing sentence punctuation and unpaired closing `)` / `]`. */
    private fun trimTrailingUrlNoise(url: String): String {
        var openParen = 0
        var closeParen = 0
        var openBracket = 0
        var closeBracket = 0
        for (idx in 0 until url.length) {
            when (url[idx]) {
                '(' -> openParen++
                ')' -> closeParen++
                '[' -> openBracket++
                ']' -> closeBracket++
            }
        }
        var end = url.length
        while (end > 0) {
            when (url[end - 1]) {
                '.', ',', ';', ':', '!', '?' -> end--
                ')' -> if (closeParen > openParen) {
                    closeParen--
                    end--
                } else {
                    break
                }
                ']' -> if (closeBracket > openBracket) {
                    closeBracket--
                    end--
                } else {
                    break
                }
                else -> break
            }
        }
        return if (end == url.length) url else url.substring(0, end)
    }

    /**
     * Bare web links written as `<url>`, so a link ends at whitespace or the first non-ASCII character, minus the
     * sentence's trailing punctuation and unpaired closing brackets (the chat's rule). The autolinking parser would
     * otherwise run a link on into the Chinese text after it.
     */
    fun links(text: String): String {
        if (text.indexOf("http", ignoreCase = true) < 0) return text
        val skip = findProtectedRanges(text)
        return buildString {
            var cursor = 0
            var searchFrom = 0
            var skipIdx = 0
            while (searchFrom < text.length) {
                val match = bareScheme.find(text, searchFrom) ?: break
                val start = match.range.first
                while (skipIdx < skip.size && skip[skipIdx].last < start) skipIdx++
                if (skipIdx < skip.size && start in skip[skipIdx]) {
                    searchFrom = skip[skipIdx].last + 1
                    continue
                }
                val limit = if (skipIdx < skip.size) skip[skipIdx].first else text.length
                val schemeEnd = match.range.last + 1
                if (schemeEnd >= limit) {
                    searchFrom = limit
                    continue
                }
                var rawEnd = schemeEnd
                while (rawEnd < limit) {
                    val ch = text[rawEnd]
                    if (ch !in '\u0021'..'\u007e' || ch == '<' || ch == '>' || ch == '"') break
                    rawEnd++
                }
                if (rawEnd <= schemeEnd) {
                    searchFrom = rawEnd
                    continue
                }
                val schemeLen = schemeEnd - start
                val url = trimTrailingUrlNoise(text.substring(start, rawEnd))
                if (url.length <= schemeLen) {
                    searchFrom = rawEnd
                    continue
                }
                append(text, cursor, start)
                append('<').append(url).append('>')
                cursor = start + url.length
                searchFrom = rawEnd
            }
            append(text, cursor, text.length)
        }
    }

    /** What the viewer renders for a Markdown file's text. */
    fun prepare(text: String): String = links(simplifyHtml(text))

    private val scheme = Regex("^[a-zA-Z][a-zA-Z0-9+.-]*:")

    /**
     * The file a link or an image in a Markdown file names, resolved against the file's folder [base]; null for web
     * links, other schemes and in-page anchors. Queries, anchors and percent-escapes are dropped from the path.
     */
    fun target(link: String, base: String): String? {
        val raw = link.trim().removePrefix("<").removeSuffix(">")
        if (raw.isEmpty() || raw.startsWith("#") || raw.startsWith("//") || scheme.containsMatchIn(raw)) return null
        val path = raw.substringBefore('#').substringBefore('?').let { decode(it) }
        if (path.isEmpty()) return null
        return FilesPaths.resolve(path, base)
    }

    private fun decode(value: String): String =
        if ('%' !in value) value else runCatching { java.net.URLDecoder.decode(value.replace("+", "%2B"), "UTF-8") }.getOrDefault(value)
}
