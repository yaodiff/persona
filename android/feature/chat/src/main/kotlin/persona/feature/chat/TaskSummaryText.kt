package persona.feature.chat

import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.LinkAnnotation
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.TextLinkStyles
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontStyle
import androidx.compose.ui.text.style.TextDecoration
import persona.core.contracts.compactFolderName
import persona.core.designsystem.PersonaTheme

/** Semantic tone extracted from a hub task verdict line so the client draws a themed icon instead of raw system emoji. */
internal enum class VerdictTone { Done, Cannot, Decide, Neutral }

internal data class ParsedVerdict(val tone: VerdictTone, val text: String)

private val leadingVerdictEmoji = Regex("""^([\u2705\u26D4]\uFE0F?|\uD83E\uDD14)\s*""")

// Hub assistant-reports verdict wording includes "below" hints meant for cards with an inline Accept button.
// Compact Needs-you tray rows have only a trailing Review action, so adapt those suffixes client-side.
private val zhBelowHint = Regex("""([，,；;]\s*)?按下面验收\s*$""") // i18n-ignore: matches hub zh-Hans verdict wording
private val enBelowCheckHint = Regex("""(?i)([;,]\s*)?check it below\.?\s*$""") // i18n-ignore: matches hub en verdict wording
private val enBelowVerifyHint = Regex("""(?i)please verify the result below\.?\s*$""") // i18n-ignore: matches hub en verdict wording
private val jaBelowHint = Regex("""([。・]\s*)?下の手順で確認してください[。.]?\s*$""") // i18n-ignore: matches hub ja verdict wording

private val localMarkdownLink = Regex("""\[([^\]\n]+)]\((?:file://|[A-Za-z]:[/\\]|/(?:Users|home|var|opt|etc|tmp|mnt|srv|root|private)/)[^)\n]*\)""")
private val truncatedMarkdownLink = Regex("""\[([^\]\n]+)]\((?:file://|https?://|[A-Za-z]:[/\\]|/)[^)\n]*$""", RegexOption.MULTILINE)
private val bareFileUri = Regex("""file://[^\s)\]"'」），,。；;]+""") // i18n-ignore: CJK punctuation delimiters
private val bareWindowsPath = Regex("""(?:^|(?<=[\s(\["'「（:：]))[A-Za-z]:\\[^\s)\]"'」），,。；;]+""") // i18n-ignore: CJK punctuation delimiters
private val barePosixPath = Regex("""(?:^|(?<=[\s(\["'「（:：]))/(?:Users|home|var|opt|etc|tmp|mnt|srv|root|private)/[^\s)\]"'」），,。；;]+""") // i18n-ignore: CJK punctuation delimiters

internal fun isWebLinkUrl(url: String?): Boolean {
    val trimmed = url?.trim().orEmpty()
    return trimmed.startsWith("https://", ignoreCase = true) || trimmed.startsWith("http://", ignoreCase = true)
}

/** Strips leading hub verdict emoji and classifies the verdict's semantic tone. */
internal fun parseTaskVerdict(raw: String): ParsedVerdict {
    val trimmed = raw.trim()
    val match = leadingVerdictEmoji.find(trimmed) ?: return ParsedVerdict(VerdictTone.Neutral, trimmed)
    val marker = match.groupValues[1]
    val tone = when {
        marker.startsWith('\u2705') -> VerdictTone.Done
        marker.startsWith('\u26D4') -> VerdictTone.Cannot
        marker.startsWith("\uD83E\uDD14") -> VerdictTone.Decide
        else -> VerdictTone.Neutral
    }
    return ParsedVerdict(tone, trimmed.substring(match.range.last + 1).trim())
}

/**
 * Formats a hub verdict for compact Needs-you tray rows where there is no inline accept button underneath:
 * removes leading system emoji and replaces misleading "below" pointers with [readyToVerify].
 */
internal fun trayVerdictText(raw: String, readyToVerify: String): ParsedVerdict {
    val parsed = parseTaskVerdict(raw)
    val text = parsed.text
    val adapted = when {
        zhBelowHint.containsMatchIn(text) -> zhBelowHint.replace(text) { m ->
            if (m.range.first == 0 || m.groupValues[1].isEmpty()) readyToVerify else "，$readyToVerify" // i18n-ignore: Chinese punctuation separator
        }
        enBelowVerifyHint.containsMatchIn(text) -> enBelowVerifyHint.replace(text, readyToVerify)
        enBelowCheckHint.containsMatchIn(text) -> enBelowCheckHint.replace(text) { m ->
            if (m.range.first == 0 || m.groupValues[1].isEmpty()) readyToVerify else ", $readyToVerify"
        }
        jaBelowHint.containsMatchIn(text) -> jaBelowHint.replace(text) { m ->
            if (m.range.first == 0 || m.groupValues[1].isEmpty()) readyToVerify else "・$readyToVerify" // i18n-ignore: Japanese middle-dot separator
        }
        else -> text
    }
    return parsed.copy(text = adapted.trim())
}

/** Compact single-line location metadata for an agent: computer · folder · runtime, never raw paths. */
internal fun agentMetadataLine(machineName: String?, cwd: String?, agentLabel: String?): String =
    listOfNotNull(
        machineName?.trim()?.takeIf(String::isNotEmpty),
        compactFolderName(cwd).takeIf(String::isNotEmpty),
        agentLabel?.trim()?.takeIf(String::isNotEmpty),
    ).distinct().joinToString(" · ")

private fun compactPathToken(token: String): String {
    val noEllipsis = token.trimEnd('.', '…')
    val withoutUri = noEllipsis.removePrefix("file:///").removePrefix("file://")
    val withoutQuery = withoutUri.substringBefore('#').substringBefore('?')
    return compactFolderName(withoutQuery).ifEmpty { token }
}

private fun compactLinkLabel(label: String): String {
    val trimmed = label.trim()
    val looksLikeAbsolutePath = trimmed.startsWith("file://", ignoreCase = true) ||
        (trimmed.length >= 3 && trimmed[0].isLetter() && trimmed[1] == ':' && (trimmed[2] == '\\' || trimmed[2] == '/')) ||
        barePosixPath.matches(trimmed)
    return if (looksLikeAbsolutePath) compactPathToken(trimmed) else trimmed
}

/**
 * Strips raw file:/// URIs, truncated Markdown link URLs, and host absolute paths from an agent summary
 * before inline Markdown parsing so primary task surfaces stay readable and never expose host paths.
 */
internal fun sanitizeSummaryMarkdown(raw: String): String {
    var out = raw.trim()
    if (out.isEmpty()) return ""
    out = localMarkdownLink.replace(out) { compactLinkLabel(it.groupValues[1]) }
    out = truncatedMarkdownLink.replace(out) { compactLinkLabel(it.groupValues[1]) }
    out = bareFileUri.replace(out) { compactPathToken(it.value) }
    out = bareWindowsPath.replace(out) { compactPathToken(it.value) }
    out = barePosixPath.replace(out) { compactPathToken(it.value) }
    return out
}

/** Clean plain-text agent summary for compact rows: strips raw Markdown syntax, file:/// links, and host paths. */
internal fun plainSummaryText(raw: String?): String =
    raw?.let(::sanitizeSummaryMarkdown)?.takeIf(String::isNotEmpty)?.let { ReceiptMarkdown.parse(it).text }.orEmpty()

/**
 * Inserts zero-width break opportunities inside long non-URL code/identifier tokens (e.g. dotted method chains)
 * so Android wraps cleanly at delimiters instead of splitting words mid-identifier.
 * Returns the transformed string and an index mapping table (`originalIndex -> outputIndex`).
 */
internal fun insertIdentifierBreakHints(text: String): Pair<String, IntArray> {
    val out = StringBuilder(text.length + 16)
    val map = IntArray(text.length + 1)
    var i = 0
    while (i < text.length) {
        if (text[i].isWhitespace()) {
            map[i] = out.length
            out.append(text[i])
            i++
            continue
        }
        var end = i
        while (end < text.length && !text[end].isWhitespace()) end++
        val token = text.substring(i, end)
        val isUrl = isWebLinkUrl(token)
        val needsHints = !isUrl && token.length >= 16 && token.any { it == '.' || it == '/' || it == '(' || it == '_' }
        for (k in i until end) {
            map[k] = out.length
            val ch = text[k]
            out.append(ch)
            if (needsHints && k + 1 < end && (ch == '.' || ch == '/' || ch == '(' || ch == '_') && text[k + 1].isLetterOrDigit()) {
                out.append('\u200B')
            }
        }
        i = end
    }
    map[text.length] = out.length
    return out.toString() to map
}

/**
 * Clean, styled agent summary for Task Detail and receipts: strips raw Markdown marks, local file:/// links,
 * and absolute paths; styles inline code cleanly without heavy background boxes; keeps http(s) links tappable;
 * and inserts soft wrap hints inside long dotted identifiers.
 */
@Composable
internal fun summaryWords(md: String): AnnotatedString {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    return remember(md, palette, type) {
        val sanitized = sanitizeSummaryMarkdown(md)
        val styled = ReceiptMarkdown.parse(sanitized)
        val (hinted, indexMap) = insertIdentifierBreakHints(styled.text)
        val link = TextLinkStyles(SpanStyle(color = palette.accent, textDecoration = TextDecoration.Underline))
        buildAnnotatedString {
            append(hinted)
            for (span in styled.spans) {
                val start = indexMap[span.start]
                val end = indexMap[span.end]
                if (end <= start) continue
                when (span.mark) {
                    ReceiptMarkdown.Mark.Strong -> addStyle(SpanStyle(fontWeight = type.bodyStrong.fontWeight, color = palette.ink), start, end)
                    ReceiptMarkdown.Mark.Emphasis -> addStyle(SpanStyle(fontStyle = FontStyle.Italic), start, end)
                    ReceiptMarkdown.Mark.Strike -> addStyle(SpanStyle(textDecoration = TextDecoration.LineThrough), start, end)
                    ReceiptMarkdown.Mark.Code -> addStyle(SpanStyle(fontFamily = type.code.fontFamily, color = palette.ink), start, end)
                    ReceiptMarkdown.Mark.Link -> if (isWebLinkUrl(span.url)) addLink(LinkAnnotation.Url(span.url.orEmpty(), link), start, end)
                }
            }
            val linked = styled.spans.filter { it.mark == ReceiptMarkdown.Mark.Link && isWebLinkUrl(it.url) }
            ChatLinks.matches(styled.text).filter { m -> linked.none { m.start < it.end && it.start < m.end } }
                .forEach {
                    val start = indexMap[it.start]
                    val end = indexMap[it.end]
                    if (end > start) addLink(LinkAnnotation.Url(it.url, link), start, end)
                }
        }
    }
}
