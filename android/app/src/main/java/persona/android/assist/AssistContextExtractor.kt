package persona.android.assist

import android.app.assist.AssistContent
import android.app.assist.AssistStructure
import android.content.Intent
import android.os.Bundle
import android.text.InputType
import android.view.View
import java.io.ByteArrayOutputStream
import java.net.URI
import java.nio.ByteBuffer
import java.nio.charset.CharacterCodingException
import java.nio.charset.CodingErrorAction
import java.util.Locale
import persona.feature.chat.ChatLinkContext

/** Pure, testable representation of a node in an Android [AssistStructure]. */
internal data class AssistNodeData(
    val text: String? = null,
    val contentDescription: String? = null,
    val hint: String? = null,
    val idEntry: String? = null,
    val className: String? = null,
    val inputType: Int = InputType.TYPE_NULL,
    val isPassword: Boolean = false,
    val isAssistBlocked: Boolean = false,
    val isVisible: Boolean = true,
    val webDomain: String? = null,
    val webScheme: String? = null,
    val htmlTag: String? = null,
    val htmlAttributes: Map<String, String> = emptyMap(),
    val autofillHints: List<String> = emptyList(),
    val children: List<AssistNodeData> = emptyList(),
)

internal data class AssistWindowData(
    val title: String? = null,
    val root: AssistNodeData? = null,
)

internal data class AssistSnapshotData(
    val packageName: String? = null,
    val webUrl: String? = null,
    val intentTitle: String? = null,
    val windows: List<AssistWindowData> = emptyList(),
)

/** Bounded, non-sensitive foreground context extracted from assist metadata. */
internal data class SafeAssistContext(
    val title: String = "",
    val url: String = "",
    val packageName: String = "",
) {
    val isEmpty: Boolean get() = title.isBlank() && url.isBlank() && packageName.isBlank()
}

/**
 * Extracts bounded, non-sensitive foreground cues (valid `http(s)` URL without credentials, non-self package
 * name, and non-sensitive title) from Android assist callbacks while strictly skipping blocked, hidden,
 * editable, and password/sensitive nodes.
 */
internal object AssistContextExtractor {
    const val MAX_TITLE_CHARS = 120
    const val MAX_URL_CHARS = 512
    const val MAX_PACKAGE_CHARS = 128
    const val MAX_BODY_CHARS = 400
    const val MAX_DEPTH = 32
    const val MAX_NODES = 256
    private const val MAX_QUERY_KEY_DECODE_PASSES = 2

    private val packageRegex = Regex("^[A-Za-z][A-Za-z0-9_]*(\\.[A-Za-z0-9_]+)+$")
    private val componentTitleRegex = Regex("^([A-Za-z][A-Za-z0-9_]*\\.)+[A-Za-z0-9_]+/[A-Za-z0-9_.$]+$")
    private val maskedTextRegex = Regex("^[•*●▪▫·\\s]+$")
    private val whitespaceRegex = Regex("\\s+")
    private val tokenBoundaryRegex = Regex("(?<=[a-z0-9])(?=[A-Z])|[^A-Za-z0-9]+")

    private val ignoredPackages = setOf("android", "com.android.systemui")

    private val sensitiveTokens = setOf(
        "password", "passwd", "passcode", "pin", "pincode", "otp", "totp",
        "cvv", "cvc", "cvn", "ssn", "secret", "apikey", "privatekey",
        "creditcard", "cardnumber", "securitycode", "onetimecode",
    )

    private val sensitiveQueryParams = setOf(
        "token", "access_token", "id_token", "refresh_token", "auth", "authorization",
        "key", "api_key", "apikey", "secret", "password", "passwd", "passcode",
        "code", "session", "session_id", "sid", "jwt", "sig", "signature", "credential",
    )

    private val editableClassSuffixes = setOf(
        "EditText",
        "AutoCompleteTextView",
        "MultiAutoCompleteTextView",
        "TextInputEditText",
        "SearchAutoComplete",
    )

    fun isPasswordInputType(inputType: Int): Boolean {
        val clazz = inputType and InputType.TYPE_MASK_CLASS
        val variation = inputType and InputType.TYPE_MASK_VARIATION
        return when (clazz) {
            InputType.TYPE_CLASS_TEXT -> variation == InputType.TYPE_TEXT_VARIATION_PASSWORD ||
                variation == InputType.TYPE_TEXT_VARIATION_VISIBLE_PASSWORD ||
                variation == InputType.TYPE_TEXT_VARIATION_WEB_PASSWORD
            InputType.TYPE_CLASS_NUMBER -> variation == InputType.TYPE_NUMBER_VARIATION_PASSWORD
            else -> false
        }
    }

    fun isSensitiveNode(node: AssistNodeData): Boolean {
        if (node.isPassword || isPasswordInputType(node.inputType)) return true
        val tag = node.htmlTag?.lowercase(Locale.ROOT)
        if (tag == "input") {
            val type = node.htmlAttributes["type"]?.lowercase(Locale.ROOT)
            if (type == "password") return true
        }
        val autocomplete = node.htmlAttributes["autocomplete"]?.lowercase(Locale.ROOT).orEmpty()
        if ("password" in autocomplete || "one-time-code" in autocomplete || "cc-" in autocomplete) return true
        if (node.autofillHints.any(::containsSensitiveToken)) return true
        if (containsSensitiveToken(node.idEntry)) return true
        if (containsSensitiveToken(node.hint)) return true
        if (containsSensitiveToken(node.contentDescription)) return true
        return false
    }

    fun isEditableNode(node: AssistNodeData): Boolean {
        if (node.inputType != InputType.TYPE_NULL) return true
        val tag = node.htmlTag?.lowercase(Locale.ROOT)
        if (tag == "input" || tag == "textarea") return true
        val simpleName = node.className?.substringAfterLast('.')?.substringAfterLast('$').orEmpty()
        return simpleName in editableClassSuffixes
    }

    fun extract(snapshot: AssistSnapshotData, ownPackage: String): SafeAssistContext {
        val pkg = sanitizePackageName(snapshot.packageName, ownPackage)
        var url = sanitizeWebUrl(snapshot.webUrl)
        var title = sanitizeTitle(snapshot.intentTitle)

        var visited = 0
        var fallbackTitle = ""
        var fallbackUrl = ""

        fun walk(node: AssistNodeData, depth: Int) {
            if (depth > MAX_DEPTH || visited >= MAX_NODES) return
            visited++
            if (!node.isVisible || node.isAssistBlocked || isSensitiveNode(node)) return

            if (url.isEmpty() && fallbackUrl.isEmpty() && !node.webDomain.isNullOrBlank()) {
                val scheme = node.webScheme?.lowercase(Locale.ROOT)?.takeIf { it == "http" || it == "https" } ?: "https"
                fallbackUrl = sanitizeWebUrl("$scheme://${node.webDomain.trim()}")
            }

            if (title.isEmpty() && fallbackTitle.isEmpty() && !isEditableNode(node)) {
                val candidate = sanitizeTitle(node.text)
                if (candidate.length >= 2 && !candidate.startsWith("http://", ignoreCase = true) &&
                    !candidate.startsWith("https://", ignoreCase = true) && !containsSensitiveToken(candidate)) {
                    fallbackTitle = candidate
                }
            }

            for (child in node.children) {
                if (visited >= MAX_NODES) break
                walk(child, depth + 1)
            }
        }

        for (window in snapshot.windows) {
            if (title.isEmpty()) {
                val windowTitle = sanitizeTitle(window.title)
                if (windowTitle.isNotEmpty() && !containsSensitiveToken(windowTitle)) {
                    title = windowTitle
                }
            }
            window.root?.let { walk(it, 0) }
            if (visited >= MAX_NODES) break
        }

        if (url.isEmpty()) url = fallbackUrl
        if (title.isEmpty()) title = fallbackTitle

        return SafeAssistContext(title = title, url = url, packageName = pkg)
    }

    fun toLinkContext(
        safe: SafeAssistContext,
        id: String,
        fallbackTitle: String,
    ): ChatLinkContext? {
        if (safe.isEmpty) return null
        val lines = buildList {
            if (safe.title.isNotBlank()) add(safe.title)
            if (safe.url.isNotBlank()) add(safe.url)
            if (safe.packageName.isNotBlank()) add(safe.packageName)
        }
        val body = truncate(lines.joinToString("\n"), MAX_BODY_CHARS)
        val cardTitle = safe.title.ifBlank { safe.packageName.ifBlank { fallbackTitle } }
        return ChatLinkContext(
            kind = "context",
            title = truncate(cardTitle, MAX_TITLE_CHARS),
            url = safe.url,
            text = body,
            id = "assist-ctx-$id",
        )
    }

    fun snapshotFromAssist(
        data: Bundle?,
        structure: AssistStructure?,
        content: AssistContent?,
    ): AssistSnapshotData {
        val pkg = runCatching { structure?.activityComponent?.packageName }.getOrNull()
            ?: runCatching { content?.intent?.component?.packageName }.getOrNull()
            ?: runCatching { data?.getString(Intent.EXTRA_ASSIST_PACKAGE) }.getOrNull()
        val webUrl = runCatching { content?.webUri?.toString() }.getOrNull()
        val intentTitle = runCatching {
            content?.intent?.getStringExtra(Intent.EXTRA_TITLE)
                ?: content?.intent?.getStringExtra(Intent.EXTRA_SUBJECT)
        }.getOrNull()

        val windows = mutableListOf<AssistWindowData>()
        if (structure != null) {
            val budget = intArrayOf(MAX_NODES)
            val count = runCatching { structure.windowNodeCount }.getOrDefault(0).coerceAtMost(8)
            for (i in 0 until count) {
                if (budget[0] <= 0) break
                val window = runCatching { structure.getWindowNodeAt(i) }.getOrNull() ?: continue
                val title = runCatching { window.title?.toString() }.getOrNull()
                val root = runCatching { window.rootViewNode }.getOrNull()?.let { convertNode(it, 0, budget) }
                windows += AssistWindowData(title = title, root = root)
            }
        }
        return AssistSnapshotData(packageName = pkg, webUrl = webUrl, intentTitle = intentTitle, windows = windows)
    }

    fun snapshotFromIntent(intent: Intent?): AssistSnapshotData {
        if (intent == null) return AssistSnapshotData()
        val assistContext = runCatching { intent.getBundleExtra(Intent.EXTRA_ASSIST_CONTEXT) }.getOrNull()
        val pkg = runCatching {
            intent.getStringExtra(Intent.EXTRA_ASSIST_PACKAGE)
                ?: assistContext?.getString(Intent.EXTRA_ASSIST_PACKAGE)
        }.getOrNull()
        val title = runCatching {
            intent.getStringExtra(Intent.EXTRA_TITLE)
                ?: intent.getStringExtra(Intent.EXTRA_SUBJECT)
                ?: assistContext?.getString(Intent.EXTRA_TITLE)
        }.getOrNull()
        val url = runCatching {
            intent.dataString ?: assistContext?.getString("url")
        }.getOrNull()
        return AssistSnapshotData(packageName = pkg, webUrl = url, intentTitle = title)
    }

    private fun convertNode(node: AssistStructure.ViewNode, depth: Int, budget: IntArray): AssistNodeData? {
        if (depth > MAX_DEPTH || budget[0] <= 0) return null
        budget[0]--
        val visible = runCatching { node.visibility == View.VISIBLE }.getOrDefault(true)
        val blocked = runCatching { node.isAssistBlocked }.getOrDefault(false)
        val inputType = runCatching { node.inputType }.getOrDefault(InputType.TYPE_NULL)
        val htmlInfo = runCatching { node.htmlInfo }.getOrNull()
        val htmlTag = runCatching { htmlInfo?.tag }.getOrNull()
        val htmlAttrs = runCatching {
            htmlInfo?.attributes?.associate { pair ->
                pair.first.lowercase(Locale.ROOT) to pair.second.orEmpty()
            }.orEmpty()
        }.getOrDefault(emptyMap())
        val autofillHints = runCatching { node.autofillHints?.toList().orEmpty() }.getOrDefault(emptyList())
        val idEntry = runCatching { node.idEntry }.getOrNull()
        val hint = runCatching { node.hint }.getOrNull()
        val contentDescription = runCatching { node.contentDescription?.toString() }.getOrNull()
        val className = runCatching { node.className }.getOrNull()

        val probe = AssistNodeData(
            contentDescription = contentDescription,
            hint = hint,
            idEntry = idEntry,
            className = className,
            inputType = inputType,
            isAssistBlocked = blocked,
            isVisible = visible,
            htmlTag = htmlTag,
            htmlAttributes = htmlAttrs,
            autofillHints = autofillHints,
        )
        // Stop before reading text or descending if the node is hidden, blocked, or sensitive.
        if (!visible || blocked || isSensitiveNode(probe)) {
            return probe
        }

        val text = if (isEditableNode(probe)) null else runCatching { node.text?.toString() }.getOrNull()
        val webDomain = runCatching { node.webDomain }.getOrNull()
        val webScheme = runCatching { node.webScheme }.getOrNull()
        val childCount = runCatching { node.childCount }.getOrDefault(0)
        val children = mutableListOf<AssistNodeData>()
        for (i in 0 until childCount) {
            if (budget[0] <= 0) break
            val child = runCatching { node.getChildAt(i) }.getOrNull() ?: continue
            convertNode(child, depth + 1, budget)?.let { children += it }
        }
        return probe.copy(
            text = text,
            webDomain = webDomain,
            webScheme = webScheme,
            children = children,
        )
    }

    internal fun sanitizeWebUrl(raw: String?): String {
        val trimmed = raw?.trim().orEmpty()
        if (trimmed.isEmpty()) return ""
        val uri = runCatching { URI(trimmed) }.getOrNull() ?: return ""
        val scheme = uri.scheme?.lowercase(Locale.ROOT) ?: return ""
        if (scheme != "http" && scheme != "https") return ""
        val authority = uri.rawAuthority?.takeIf { it.isNotBlank() && '@' !in it } ?: return ""
        if (uri.host.isNullOrBlank() || uri.rawUserInfo != null) return ""

        val filteredQuery = uri.rawQuery?.let(::filterQuery)
        val base = buildString {
            append(scheme)
            append("://")
            append(authority)
            append(uri.rawPath.orEmpty())
        }
        val rebuiltRaw = if (filteredQuery != null) "$base?$filteredQuery" else base
        val rebuilt = runCatching { URI(rebuiltRaw).toASCIIString() }.getOrNull() ?: return ""

        if (rebuilt.length <= MAX_URL_CHARS) return rebuilt
        val withoutQuery = runCatching { URI(base).toASCIIString() }.getOrNull().orEmpty()
        return if (withoutQuery.length <= MAX_URL_CHARS) withoutQuery else withoutQuery.take(MAX_URL_CHARS)
    }

    internal fun filterQuery(rawQuery: String): String? =
        rawQuery.split('&')
            .filter { part ->
                if (part.isEmpty()) return@filter false
                val decodedKey = decodeQueryKey(part.substringBefore('=')) ?: return@filter false
                decodedKey !in sensitiveQueryParams
            }
            .joinToString("&")
            .ifEmpty { null }

    internal fun decodeQueryKey(rawKey: String): String? {
        if (rawKey.isEmpty()) return null
        var current = rawKey
        repeat(MAX_QUERY_KEY_DECODE_PASSES) {
            val decoded = strictPercentDecode(current) ?: return null
            if (decoded.any(Char::isISOControl)) return null
            if ('%' !in decoded) return decoded.trim().lowercase(Locale.ROOT).ifEmpty { null }
            current = decoded
        }
        // Fail closed on multi-layered percent encoding (e.g. %252574oken).
        return null
    }

    internal fun strictPercentDecode(raw: String): String? {
        val out = ByteArrayOutputStream(raw.length)
        var i = 0
        while (i < raw.length) {
            when (raw[i]) {
                '%' -> {
                    if (i + 2 >= raw.length) return null
                    val hi = hexDigit(raw[i + 1])
                    val lo = hexDigit(raw[i + 2])
                    if (hi < 0 || lo < 0) return null
                    out.write((hi shl 4) or lo)
                    i += 3
                }
                '+' -> {
                    out.write(' '.code)
                    i++
                }
                else -> {
                    val start = i
                    i++
                    while (i < raw.length && raw[i] != '%' && raw[i] != '+') {
                        i++
                    }
                    out.write(raw.substring(start, i).toByteArray(Charsets.UTF_8))
                }
            }
        }
        return try {
            val decoder = Charsets.UTF_8.newDecoder()
                .onMalformedInput(CodingErrorAction.REPORT)
                .onUnmappableCharacter(CodingErrorAction.REPORT)
            decoder.decode(ByteBuffer.wrap(out.toByteArray())).toString()
        } catch (_: CharacterCodingException) {
            null
        }
    }

    private fun hexDigit(ch: Char): Int = when (ch) {
        in '0'..'9' -> ch - '0'
        in 'a'..'f' -> ch - 'a' + 10
        in 'A'..'F' -> ch - 'A' + 10
        else -> -1
    }

    internal fun sanitizePackageName(raw: String?, ownPackage: String): String {
        val trimmed = raw?.trim().orEmpty()
        if (trimmed.isEmpty() || trimmed == ownPackage || trimmed in ignoredPackages) return ""
        if (trimmed.length > MAX_PACKAGE_CHARS || !packageRegex.matches(trimmed)) return ""
        return trimmed
    }

    internal fun sanitizeTitle(raw: String?): String {
        if (raw.isNullOrBlank()) return ""
        val cleaned = raw.map { ch -> if (ch.isISOControl()) ' ' else ch }.joinToString("")
            .replace(whitespaceRegex, " ").trim()
        if (cleaned.isEmpty() || componentTitleRegex.matches(cleaned) || maskedTextRegex.matches(cleaned)) return ""
        return truncate(cleaned, MAX_TITLE_CHARS)
    }

    internal fun containsSensitiveToken(value: String?): Boolean {
        if (value.isNullOrBlank()) return false
        val tokens = tokenBoundaryRegex.split(value)
            .map { it.lowercase(Locale.ROOT) }
            .filter(String::isNotEmpty)
        if (tokens.any { it in sensitiveTokens }) return true
        for (i in 0 until tokens.lastIndex) {
            if ((tokens[i] + tokens[i + 1]) in sensitiveTokens) return true
        }
        return false
    }

    private fun truncate(value: String, maxChars: Int): String {
        if (value.length <= maxChars) return value
        if (maxChars <= 1) return value.take(maxChars)
        return value.take(maxChars - 1).trimEnd() + "…"
    }
}
