package persona.core.rustcore

import java.io.File

/**
 * A byte-preserving reader for the wire corpus (the JSON files of `spec/fixtures/wire`, each a top-level array of
 * cases). The point of the transport binding is that an item reaches the client exactly as the hub wrote it, so the
 * test feeds the decoder
 * the `input` value's source text and compares each item with its own source text; neither may go through a parse and
 * re-serialisation, which would normalise whitespace, escapes and key order and make the comparison circular.
 *
 * Only the values this test asks for are located: a case's `name`, `decoder` and `input`, and the `items` elements of
 * that input. Everything else is skipped as JSON, with strings and escapes honoured, so an `items` or `name` key inside
 * some other value is never mistaken for the real one.
 */
internal object RawJson {
    /** One case: the raw text of its `input` object and the raw text of each element of its `items` array. */
    class Case(val name: String, val decoder: String, val input: String) {
        /** The `items` elements' source text, empty when the key is absent or `null` (iOS reads both as no items). */
        val items: List<String> by lazy {
            val array = value(input, "items") ?: return@lazy emptyList()
            if (array == "null") emptyList() else elements(array)
        }
    }

    /** Every case of one corpus file, in file order. */
    fun cases(file: File): List<Case> = elements(file.readText()).map { case ->
        Case(
            string(case, "name") ?: error("${file.name}: a case without a name"),
            string(case, "decoder") ?: error("${file.name}: a case without a decoder"),
            value(case, "input") ?: error("${file.name}: a case without an input"),
        )
    }

    /** The raw text of `key`'s value in the raw JSON object `json`, or null when the key is absent. */
    fun value(json: String, key: String): String? = Cursor(json).member(key)

    /** A string member's decoded content, for the case metadata only. */
    private fun string(json: String, key: String): String? = value(json, key)?.let { raw -> Cursor(raw).string() }

    /** The raw text of each element of a raw JSON array. */
    private fun elements(json: String): List<String> {
        val cursor = Cursor(json)
        cursor.expect('[')
        val out = mutableListOf<String>()
        if (cursor.peek() == ']') {
            cursor.skip()
            cursor.end()
            return out
        }
        while (true) {
            out.add(cursor.any())
            when (cursor.peek()) {
                ',' -> cursor.skip()
                ']' -> {
                    cursor.skip()
                    cursor.end()
                    return out
                }
                else -> cursor.fail("an array separator")
            }
        }
    }

    /** A position in a raw JSON text. Nothing is decoded unless the caller asks for a string's content. */
    private class Cursor(private val json: String) {
        private var at = 0

        fun peek(): Char {
            whitespace()
            return json.getOrElse(at) { fail("more JSON") }
        }

        fun skip() {
            at++
        }

        fun expect(char: Char) {
            if (peek() != char) fail("'$char'")
            at++
        }

        fun end() {
            whitespace()
            if (at != json.length) fail("the end of the value")
        }

        fun fail(what: String): Nothing = error("Malformed fixture JSON at $at: expected $what")

        /** One whole value's raw text. */
        fun any(): String {
            whitespace()
            val start = at
            when (peek()) {
                '{' -> objectBody()
                '[' -> arrayBody()
                '"' -> stringBody()
                else -> literal()
            }
            return json.substring(start, at)
        }

        /** The value of `key` in the object the cursor is on, without disturbing other members. */
        fun member(key: String): String? {
            expect('{')
            var found: String? = null
            if (peek() == '}') {
                skip()
                return null
            }
            while (true) {
                val name = string()
                expect(':')
                val value = any()
                // Foundation keeps a repeated key's first value; so does this lookup, though the corpus has none.
                if (name == key && found == null) found = value
                when (peek()) {
                    ',' -> skip()
                    '}' -> {
                        skip()
                        return found
                    }
                    else -> fail("an object separator")
                }
            }
        }

        /** A string token's decoded content. The corpus's metadata is ASCII; escapes are honoured anyway. */
        fun string(): String {
            whitespace()
            val start = at
            stringBody()
            val raw = json.substring(start + 1, at - 1)
            if (raw.none { it == '\\' }) return raw
            val out = StringBuilder(raw.length)
            var i = 0
            while (i < raw.length) {
                val char = raw[i]
                if (char != '\\') {
                    out.append(char)
                    i++
                    continue
                }
                val escape = raw.getOrElse(i + 1) { fail("an escape") }
                when (escape) {
                    '"', '\\', '/' -> out.append(escape)
                    'b' -> out.append(Char(8))
                    'f' -> out.append(Char(12))
                    'n' -> out.append(Char(10))
                    'r' -> out.append(Char(13))
                    't' -> out.append(Char(9))
                    'u' -> {
                        out.append(raw.substring(i + 2, i + 6).toInt(16).toChar())
                        i += 4
                    }
                    else -> fail("a valid escape")
                }
                i += 2
            }
            return out.toString()
        }

        private fun whitespace() {
            while (at < json.length && (json[at] == ' ' || json[at] == '\t' || json[at] == '\n' || json[at] == '\r')) {
                at++
            }
        }

        private fun stringBody() {
            if (peek() != '"') fail("a string")
            at++
            while (true) {
                val char = json.getOrElse(at) { fail("a string's end") }
                if (char == '"') {
                    at++
                    return
                }
                at += if (char == '\\') 2 else 1
            }
        }

        private fun objectBody() {
            expect('{')
            if (peek() == '}') {
                skip()
                return
            }
            while (true) {
                stringBody()
                expect(':')
                any()
                when (peek()) {
                    ',' -> skip()
                    '}' -> {
                        skip()
                        return
                    }
                    else -> fail("an object separator")
                }
            }
        }

        private fun arrayBody() {
            expect('[')
            if (peek() == ']') {
                skip()
                return
            }
            while (true) {
                any()
                when (peek()) {
                    ',' -> skip()
                    ']' -> {
                        skip()
                        return
                    }
                    else -> fail("an array separator")
                }
            }
        }

        private fun literal() {
            // true, false, null or a number: everything up to the next structural character.
            val start = at
            while (at < json.length && json[at] !in ",]} \t\n\r") at++
            if (at == start) fail("a JSON value")
        }
    }
}
