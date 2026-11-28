package persona.feature.settings

import android.content.res.Resources
import androidx.annotation.StringRes
import androidx.compose.runtime.Composable
import androidx.compose.ui.platform.LocalResources
import persona.core.contracts.api.ExtensionUi
import persona.core.contracts.api.ExtensionUiContext
import persona.core.contracts.api.ExtensionUiNotFound
import persona.core.contracts.api.ExtensionUiPlace
import persona.core.contracts.api.ExtensionUiResult
import persona.core.contracts.api.ExtensionUiUnsupported
import persona.core.contracts.api.ExtensionView
import persona.core.contracts.api.ExtensionViewAction
import persona.core.contracts.api.ExtensionViewItem
import persona.core.contracts.api.ExtensionViewPush
import persona.core.contracts.api.ExtensionViewSection
import persona.core.contracts.api.SUBSCRIPTIONS_UI
import persona.core.designsystem.ChartMath
import persona.core.designsystem.JCandle
import persona.core.designsystem.JChartData
import persona.core.designsystem.JChartKind
import persona.core.designsystem.JChartSeries
import persona.core.designsystem.JStat
import persona.core.designsystem.LocalizedFormats
import persona.core.designsystem.Tone
import persona.core.designsystem.UiText
import persona.core.designsystem.failureReasonText
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.collectLatest
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.flow.map
import kotlinx.coroutines.flow.update
import okhttp3.HttpUrl.Companion.toHttpUrlOrNull
import org.intellij.markdown.MarkdownElementTypes
import org.intellij.markdown.MarkdownTokenTypes
import org.intellij.markdown.ast.ASTNode
import org.intellij.markdown.ast.findChildOfType
import org.intellij.markdown.flavours.gfm.GFMFlavourDescriptor
import org.intellij.markdown.flavours.gfm.GFMTokenTypes
import org.intellij.markdown.html.entities.EntityConverter
import org.intellij.markdown.parser.LinkMap
import org.intellij.markdown.parser.MarkdownParser

// An extension's declarative UI (docs/extensions/extensions.md "Declarative UI"): drawing a view, its fields' values, its buttons. The
// renderer is in ExtensionViewContent.kt; the pages that hold one are in ExtensionUiScreen.kt.

internal object ExtensionViewText {
    /** The four tones an extension may give, as the app's role colours; anything else is neutral. */
    fun tone(tone: String?): Tone = when (tone) {
        "positive" -> Tone.Positive
        "caution" -> Tone.Caution
        "danger" -> Tone.Danger
        else -> Tone.Neutral
    }

    /** Links and `open_url` are https only (the hub checks it too): valid host, no userinfo, no control/whitespace. */
    fun isHttps(url: String?): Boolean {
        val raw = url ?: return false
        if (raw.isEmpty() || raw.any { it.isISOControl() || it.isWhitespace() || Character.getType(it) == Character.FORMAT.toInt() }) return false
        if (!raw.startsWith("https://", ignoreCase = true)) return false
        val auth = raw.substring(8).substringBefore('/').substringBefore('?').substringBefore('#')
        if (auth.isEmpty() || '@' in auth || '\\' in auth || '&' in auth || ';' in auth || auth.endsWith(':')) return false
        val parsed = raw.toHttpUrlOrNull() ?: return false
        return parsed.isHttps && parsed.host.isNotEmpty() && parsed.host != "." &&
            parsed.username.isEmpty() && parsed.password.isEmpty()
    }

    /**
     * The inbox item an inbox item's page asks to see at its source (`open_url: persona://inbox/<id>/source`, the one
     * app link the hub lets that slot open, docs/extensions/extensions.md "UI slots"): the item's id, else null.
     */
    fun inboxSource(url: String?): String? {
        val uri = runCatching { java.net.URI(url ?: return null) }.getOrNull() ?: return null
        if (uri.scheme?.lowercase() !in persona.core.contracts.ProtocolCompatibility.appSchemes || uri.rawAuthority != "inbox" || uri.rawQuery != null || uri.rawFragment != null) return null
        val segments = uri.rawPath?.removePrefix("/")?.split("/") ?: return null
        if (segments.size != 2 || segments[1] != "source") return null
        return runCatching { java.net.URLDecoder.decode(segments[0].replace("+", "%2B"), "UTF-8") }.getOrNull()?.takeIf { it.isNotBlank() }
    }

    /**
     * The extension whose settings an extension page asks to open (`persona://settings/extensions/<id>`, the id escaped
     * as one segment). Only the page's own extension is accepted; it opens Settings › Extensions › that extension.
     */
    fun settings(extension: String, url: String?): String? {
        val rawUrl = url ?: return null
        val uri = runCatching { java.net.URI(rawUrl) }.getOrNull() ?: return null
        if (uri.scheme?.lowercase() !in persona.core.contracts.ProtocolCompatibility.appSchemes ||
            uri.rawAuthority?.lowercase() != "settings" || uri.rawUserInfo != null ||
            uri.rawQuery != null || uri.rawFragment != null || '#' in rawUrl
        ) return null
        val parts = uri.rawPath?.removePrefix("/")?.split("/") ?: return null
        if (parts.size != 2 || parts[0] != "extensions") return null
        val id = runCatching { java.net.URLDecoder.decode(parts[1].replace("+", "%2B"), "UTF-8") }.getOrNull() ?: return null
        return id.takeIf { it.isNotBlank() && it == extension }
    }

    /** The most of a Set Up with … text taken (#5278): the pages name a source, they never carry its content. */
    const val ASK_MOST = 8000

    /**
     * The text of Inbox Sources' Set Up with … (`persona://ask?text=…`, an older name's scheme too), for the
     * assistant's main conversation. Null for any other link, an empty text, or more than [ASK_MOST] characters.
     */
    fun askText(url: String?): String? {
        val rawUrl = url ?: return null
        val uri = runCatching { java.net.URI(rawUrl) }.getOrNull() ?: return null
        if (uri.scheme?.lowercase() !in persona.core.contracts.ProtocolCompatibility.appSchemes) return null
        if (uri.rawAuthority?.lowercase() != "ask" || uri.rawUserInfo != null || uri.rawFragment != null || '#' in rawUrl) return null
        val path = uri.rawPath.orEmpty()
        if (path.isNotEmpty() && path != "/") return null
        val raw = uri.rawQuery ?: return null
        val encoded = raw.split('&').firstNotNullOfOrNull { part ->
            val name = decodeQuery(part.substringBefore('='))
            if (name == "text") part.substringAfter('=', "") else null
        } ?: return null
        val text = decodeQuery(encoded) ?: return null
        if (text.isBlank() || text.length > ASK_MOST) return null
        return text
    }

    /** [askText] when [extension] is the subscription pages; every other page refuses the link. */
    fun askFrom(extension: String, url: String?): String? = if (extension == SUBSCRIPTIONS_UI) askText(url) else null

    /**
     * The text of one of this extension's quick prompts (`persona://compose?prompt=<id>&text=…`, an older name's scheme
     * too). The hub has already checked that the id and text match one of the extension's enabled quick prompts; this
     * reads the same strict link shape and returns null for every other link.
     */
    fun composeText(url: String?): String? {
        val raw = url ?: return null
        val uri = runCatching { java.net.URI(raw) }.getOrNull() ?: return null
        if (uri.scheme?.lowercase() !in persona.core.contracts.ProtocolCompatibility.appSchemes ||
            uri.rawAuthority?.lowercase() != "compose" || uri.userInfo != null ||
            uri.rawPath?.let { it.isNotEmpty() && it != "/" } == true ||
            uri.rawQuery == null || uri.rawFragment != null || '#' in raw
        ) return null
        val fields = linkedMapOf<String, String>()
        for (part in uri.rawQuery.split('&')) {
            val name = decodeCompose(part.substringBefore('=')) ?: return null
            if (name !in setOf("prompt", "text") || name in fields) return null
            fields[name] = decodeCompose(part.substringAfter('=', "")) ?: return null
        }
        val prompt = fields["prompt"] ?: return null
        val text = fields["text"] ?: return null
        if (prompt.isEmpty() || prompt.codePointCount(0, prompt.length) > COMPOSE_PROMPT_MOST || prompt.any(Char::isWhitespace)) return null
        if (text.isBlank() || text.codePointCount(0, text.length) > COMPOSE_TEXT_MOST) return null
        return text
    }

    /** [composeText] for an extension page: the hub has tied the quick prompt to this extension already. */
    fun composeFrom(extension: String, url: String?): String? =
        if (extension.isNotBlank()) composeText(url) else null

    private const val COMPOSE_PROMPT_MOST = 64
    private const val COMPOSE_TEXT_MOST = 8_000

    /** A space conversation's machine and session, never a general app link. */
    fun spaceConversation(url: String?): Pair<String, String>? {
        val uri = runCatching { java.net.URI(url ?: return null) }.getOrNull() ?: return null
        if (uri.scheme?.lowercase() != "persona" || uri.rawAuthority != "tracks" || uri.rawQuery != null || uri.rawFragment != null || uri.userInfo != null) return null
        val parts = uri.rawPath?.removePrefix("/")?.split("/") ?: return null
        if (parts.size != 3 || parts.any { it.isEmpty() || it.length > 256 || !it.all { c -> c.isLetterOrDigit() || c in "_.:-" } }) return null
        if (parts[1] == "health") return null
        return parts[0] to parts[2]
    }

    /** Which `open_url` a button may bring back: https, this page's own settings, and the reserved pages' links. */
    fun opens(extension: String, url: String?): Boolean =
        isHttps(url) || settings(extension, url) != null || askFrom(extension, url) != null || composeFrom(extension, url) != null ||
            extension == "space-conversations" && spaceConversation(url) != null

    /** A query component, with `+` kept (the pages percent-encode; they do not use form encoding). */
    private fun decodeQuery(raw: String): String? =
        runCatching { java.net.URLDecoder.decode(raw.replace("+", "%2B"), "UTF-8") }.getOrNull()

    /** A compose query uses ordinary form encoding, where `+` is a space, as the hub's `url.Query()` reads it. */
    private fun decodeCompose(raw: String): String? =
        runCatching { java.net.URLDecoder.decode(raw, "UTF-8") }.getOrNull()

    /** A field's value before the user touches it: the extension's, never for a secret; a switch is off; no choices `[]`. */
    fun initial(item: ExtensionViewItem): String = when (item.kind) {
        "secret" -> ""
        "toggle" -> if (item.value == "true") "true" else "false"
        "multichoice" -> multi(item.value).let(::multiValue)
        else -> item.value.orEmpty()
    }

    /** The field kinds this app draws (UI version 6 added date, time, datetime, multichoice and multiline). */
    val FieldKinds = setOf("text", "secret", "number", "toggle", "choice", "date", "time", "datetime", "multichoice", "multiline")

    /** A multichoice's value read: the chosen values of its JSON array, none when it is not one. */
    fun multi(value: String?): List<String> = runCatching {
        (persona.core.contracts.JsonCodec.parseToJsonElement(value ?: return emptyList()) as? kotlinx.serialization.json.JsonArray)
            ?.mapNotNull { (it as? kotlinx.serialization.json.JsonPrimitive)?.takeIf { p -> p.isString }?.content }?.distinct()
    }.getOrNull().orEmpty()

    /** A multichoice's value written: the chosen values as a JSON array of strings. */
    fun multiValue(values: List<String>): String =
        kotlinx.serialization.json.JsonArray(values.distinct().map { kotlinx.serialization.json.JsonPrimitive(it) }).toString()

    /** A named colour (UI version 6), or none for a name this app does not know (the quiet ink). */
    fun hue(name: String?): persona.core.designsystem.JHue? = persona.core.designsystem.JHue.of(name)

    /** A trend line (UI version 6) for the design system; none with fewer than two values. */
    fun spark(spark: persona.core.contracts.api.ExtensionSpark?): persona.core.designsystem.JSpark? = spark?.values?.filter(Double::isFinite)
        ?.takeIf { it.size >= 2 }?.take(60)?.let { persona.core.designsystem.JSpark(it, hue(spark.color), spark.tone?.takeIf { t -> t.isNotBlank() }?.let(::tone), spark.fill == true) }

    /**
     * Which picture an image item is (UI version 6), to tie its viewer to: the extension's key, else its source (the
     * hub's path, the https address, the extension's ref); none without any (the viewer then stays with its place).
     */
    fun imageIdentity(item: ExtensionViewItem): String? = item.key?.takeIf { it.isNotBlank() }?.let { "key:$it" }
        ?: item.src?.let { s ->
            s.path?.takeIf { it.isNotBlank() }?.let { "path:$it" } ?: s.url?.takeIf { it.isNotBlank() }?.let { "url:$it" }
                ?: s.ref?.takeIf { it.isNotBlank() }?.let { "ref:$it" }
        }

    /** A picture's frame (UI version 6): `square` 1:1, `wide` 16:9, `portrait` 3:4, `auto` from the source's size; else 4:3. */
    fun aspect(item: ExtensionViewItem): Float = when (item.aspect) {
        "square" -> 1f
        "wide" -> 16f / 9f
        "portrait" -> 3f / 4f
        "auto" -> item.src?.let { s -> if ((s.width ?: 0) > 0 && (s.height ?: 0) > 0) s.width!!.toFloat() / s.height!! else null } ?: (4f / 3f)
        else -> 4f / 3f
    }

    /**
     * Where a picture loads from: the hub's signed [ExtensionHubPicture] path (with the device's credentials), else an
     * https url (without any); none for a ref the hub has not signed (an older hub) or anything else.
     */
    fun pictureModel(url: String?, path: String?): Any? = when {
        persona.core.hub.HttpExtensionManagement.isImagePath(path) -> ExtensionHubPicture(path!!)
        else -> picture(url)
    }

    /** A control's value now: the user's while their change is on its way, else the extension's ("true" / "false", a number). */
    fun controlValue(control: persona.core.contracts.api.ExtensionRowControl, pending: Map<String, String>): String =
        pending[control.name] ?: if (control.kind == "switch") (control.on == true).toString() else number(control.value ?: control.min ?: 0.0, control.step)

    /** A slider's or stepper's step: the extension's, else a hundredth of the range, else 1. */
    fun step(control: persona.core.contracts.api.ExtensionRowControl): Double = control.step?.takeIf { it > 0 && it.isFinite() }
        ?: control.max?.let { max -> control.min?.let { min -> ((max - min) / 100).takeIf { it > 0 } } } ?: 1.0

    /** A control's number as it goes back: as many decimals as its step has, no trailing zeros ("22.5", "3"). */
    fun number(value: Double, step: Double?): String {
        val places = step?.takeIf { it > 0 && it.isFinite() }?.let { java.math.BigDecimal(it.toString()).stripTrailingZeros().scale().coerceIn(0, 6) } ?: 6
        return java.math.BigDecimal(value).setScale(places, java.math.RoundingMode.HALF_UP).stripTrailingZeros().toPlainString().let { if (it == "-0") "0" else it }
    }

    /** How a control's value reads: the extension's words for its own value, else the number and the unit. */
    fun controlDisplay(control: persona.core.contracts.api.ExtensionRowControl, pending: Map<String, String>): String {
        if (control.name !in pending) control.display?.takeIf { it.isNotBlank() }?.let { return it }
        val value = controlValue(control, pending)
        return value + (control.unit?.takeIf { it.isNotBlank() }?.let { if (it == "%" || it.startsWith("°")) it else " $it" } ?: "")
    }

    /** A stepper's next value [by] steps away, held to its range. */
    fun stepped(control: persona.core.contracts.api.ExtensionRowControl, current: Double, by: Int): Double {
        val next = current + step(control) * by
        return next.coerceIn(control.min ?: Double.NEGATIVE_INFINITY, control.max ?: Double.POSITIVE_INFINITY)
    }

    /** A row's or tile's extra actions (UI version 6) that can be sent, at most four. */
    fun rowActions(actions: List<persona.core.contracts.api.ExtensionRowAction>?): List<persona.core.contracts.api.ExtensionRowAction> =
        actions.orEmpty().filter { it.title.isNotBlank() && (it.action.id.isNotEmpty() || it.action.push != null) }.take(4)

    /** A date field's value read (`YYYY-MM-DD`). */
    fun date(value: String?): java.time.LocalDate? = runCatching { java.time.LocalDate.parse(value?.take(10) ?: return null) }.getOrNull()

    /** A time field's value read (`HH:MM`), or a datetime's time. */
    fun time(value: String?): java.time.LocalTime? = runCatching {
        java.time.LocalTime.parse((value ?: return null).substringAfter('T').take(5))
    }.getOrNull()

    /**
     * A date, time or datetime field's value as it reads in the page's language ([locale]: `currentLocale`, not the
     * system's, #4105), the time in the language's 12 / 24 hour habit like the app's other times (#4128); the value
     * itself when it cannot be read.
     */
    fun whenText(kind: String?, value: String, locale: java.util.Locale): String {
        if (value.isBlank()) return ""
        val dateFormat = java.time.format.DateTimeFormatter.ofLocalizedDate(java.time.format.FormatStyle.MEDIUM).withLocale(locale)
        val timeFormat = LocalizedFormats.formatter(LocalizedFormats.TIME, locale)
        return when (kind) {
            "date" -> date(value)?.format(dateFormat)
            "time" -> time(value)?.format(timeFormat)
            "datetime" -> date(value)?.let { d -> time(value)?.let { t -> "${d.format(dateFormat)} ${t.format(timeFormat)}" } }
            else -> null
        } ?: value
    }

    /** Every field (and segmented choice) of the view by name, as the extension first sent it. */
    fun fields(view: ExtensionView?): Map<String, String> = view?.sections.orEmpty().flatMap { it.items }
        .filter { (it.type == "field" || it.type == "segmented" || it.type == "composer") && !it.name.isNullOrEmpty() }.associate { it.name!! to initial(it) } +
        listOfNotNull(view?.tabs?.takeIf { it.name.isNotEmpty() }?.let { it.name to it.value })

    /** A progress ratio, clamped to 0…1 (an extension's 1.2 is full). */
    fun ratio(item: ExtensionViewItem): Float = (item.ratio ?: 0.0).toFloat().coerceIn(0f, 1f)

    /** Rows, links, switches and choices line up in one group; the rest stand alone. */
    fun grouped(item: ExtensionViewItem): Boolean = item.type == "row" || item.type == "link" ||
        (item.type == "field" && item.kind in setOf("toggle", "choice", "date", "time", "datetime", "multichoice"))

    /**
     * Kinds this app draws (a newer hub's are skipped, not drawn half, and the rest of the view is drawn); a link that
     * is not https is not drawn, nor a chart without points or a segmented without choices to switch between.
     */
    fun drawable(item: ExtensionViewItem): Boolean = when (item.type) {
        "text", "metric", "progress", "row", "divider", "loading" -> true
        "markdown" -> !item.text.isNullOrBlank()
        "table" -> !item.columns.isNullOrEmpty()
        "empty" -> !item.title.isNullOrBlank()
        "button" -> item.action?.id?.isNotEmpty() == true
        "link" -> isHttps(item.url)
        "field" -> !item.name.isNullOrEmpty() && item.kind in FieldKinds
        "chart" -> chart(item) != null
        "stat_grid" -> !item.stats.isNullOrEmpty()
        "segmented" -> !item.name.isNullOrEmpty() && item.action?.id?.isNotEmpty() == true && item.choices.orEmpty().size >= 2
        "html" -> !item.html.isNullOrBlank()
        // UI version 4 (#2951)
        "header" -> !item.title.isNullOrBlank()
        "breadcrumb" -> !item.crumbs.isNullOrEmpty()
        "file_tree" -> item.entries != null
        "code" -> item.text != null
        "diff" -> !item.path.isNullOrBlank()
        "comment_thread" -> item.comments != null
        "composer" -> !item.name.isNullOrEmpty() && item.buttons.orEmpty().any { it.action.id.isNotEmpty() }
        // UI version 6 (#5464)
        "image" -> item.src != null && !item.alt.isNullOrBlank()
        "grid", "carousel" -> item.tiles.orEmpty().any { it.title.isNotBlank() }
        // UI version 10 (#9727): the shared layout needs at least one node.
        "graph" -> !item.nodes.isNullOrEmpty()
        // UI version 11 (#12544): the description also labels the poster or text fallback.
        "scene" -> !item.alt.isNullOrBlank()
        // UI version 12: a map needs a viewport, markers, routes, or a text fallback.
        "map" -> item.viewport != null || !item.markers.isNullOrEmpty() || !item.routes.isNullOrEmpty() || !item.alt.isNullOrBlank()
        else -> false
    }

    /** The values an action sends: the fields' when it submits, then the component's own, then those it carries. */
    fun sent(action: ExtensionViewAction, fields: Map<String, String>, own: Map<String, String>?): Map<String, String>? {
        val out = (if (action.submit) fields else emptyMap()) + own.orEmpty() + action.values.orEmpty()
        return out.takeIf { action.submit || own != null || !action.values.isNullOrEmpty() }
    }

    /**
     * A row's picture: an avatar or a thumbnail, https only (anything else is not loaded: the letters or the icon stand
     * in); the app loads it itself, with no credentials.
     */
    fun picture(url: String?): String? = url?.takeIf(::isHttps)

    /** An avatar's letters: the extension's one or two, else the first of the row's title. */
    fun initials(item: ExtensionViewItem): String =
        item.avatar?.text?.trim()?.takeIf { it.isNotEmpty() }?.take(2) ?: item.title.orEmpty().trim().take(1)

    /**
     * A table's rows as many cells as it has columns (a short row is filled, a long one cut), at most 100 rows of 8
     * columns: the hub refuses more, an older one may not.
     */
    fun tableRows(item: ExtensionViewItem): List<List<String>> {
        val width = item.columns.orEmpty().take(8).size
        return item.rows.orEmpty().take(100).map { row -> List(width) { row.getOrNull(it).orEmpty() } }
    }

    /**
     * Markdown an extension may write (docs/extensions/extensions.md "Declarative UI"): headings, paragraphs, lists, emphasis, code,
     * quotes and https links. Pictures become their words, other links their words; HTML is drawn as the text it is
     * (the renderer has no HTML).
     */
    fun safeMarkdown(text: String): String = SafeMarkdown.sanitize(text.take(20_000), ::isHttps)

    /**
     * AST-based sanitizer using the exact `org.intellij.markdown` GFM parser that `Markdown(...)` renders with:
     * preserves code blocks/spans, strips all images (inline and reference), keeps only links/autolinks/definitions
     * whose entity- and escape-decoded destination passes [allowedUrl], and converges to a fixed point so stripped
     * output can never recombine into new link or image syntax.
     */
    private object SafeMarkdown {
        private const val MAX_PASSES = 8
        private val flavour = GFMFlavourDescriptor()

        fun sanitize(input: String, allowedUrl: (String?) -> Boolean): String {
            if (input.isEmpty()) return input
            var current = input
            repeat(MAX_PASSES) {
                val next = sanitizePass(current, allowedUrl)
                if (next == current) return current
                current = next
            }
            // Fail-closed fallback if pathological nesting did not converge within MAX_PASSES.
            return if (hasUnsafeNode(current, allowedUrl)) escapeMarkdownLinks(current) else current
        }

        private fun sanitizePass(s: String, allowedUrl: (String?) -> Boolean): String {
            val tree = MarkdownParser(flavour).buildMarkdownTreeFromString(s)
            val linkMap = LinkMap.buildLinkMap(tree, s)
            val out = StringBuilder(s.length)
            var changed = false
            var lastEnd = 0

            fun appendGap(until: Int) {
                if (until > lastEnd) out.append(s, lastEnd, until)
                lastEnd = until
            }

            /** Inside an already-kept safe link's text, strip all nested images/links and neutralize autolinks. */
            fun visitInsideSafeLink(node: ASTNode) {
                when (node.type) {
                    MarkdownElementTypes.CODE_SPAN -> return
                    MarkdownElementTypes.IMAGE,
                    MarkdownElementTypes.INLINE_LINK,
                    MarkdownElementTypes.FULL_REFERENCE_LINK,
                    MarkdownElementTypes.SHORT_REFERENCE_LINK -> {
                        val inner = if (node.type == MarkdownElementTypes.IMAGE) {
                            node.children.firstOrNull {
                                it.type == MarkdownElementTypes.INLINE_LINK ||
                                    it.type == MarkdownElementTypes.FULL_REFERENCE_LINK ||
                                    it.type == MarkdownElementTypes.SHORT_REFERENCE_LINK
                            }
                        } else node
                        val isUnresolvedRef = (node.type == MarkdownElementTypes.FULL_REFERENCE_LINK ||
                            node.type == MarkdownElementTypes.SHORT_REFERENCE_LINK) &&
                            lookupRef(
                                node.findChildOfType(MarkdownElementTypes.LINK_LABEL),
                                node.findChildOfType(MarkdownElementTypes.LINK_TEXT),
                                s,
                                linkMap,
                            ) == null
                        if (isUnresolvedRef) {
                            for (child in node.children) visitInsideSafeLink(child)
                            return
                        }
                        appendGap(node.startOffset)
                        inner?.let(::labelNodeOf)?.let { appendStrippedLabel(out, it, s, linkMap) }
                        lastEnd = node.endOffset
                        changed = true
                        return
                    }
                    MarkdownElementTypes.AUTOLINK -> {
                        appendGap(node.startOffset)
                        val inside = if (node.endOffset - node.startOffset >= 2) {
                            s.substring(node.startOffset + 1, node.endOffset - 1)
                        } else ""
                        out.append(neutralizeUrl(inside))
                        lastEnd = node.endOffset
                        changed = true
                        return
                    }
                    GFMTokenTypes.GFM_AUTOLINK,
                    MarkdownTokenTypes.EMAIL_AUTOLINK -> {
                        appendGap(node.startOffset)
                        out.append(neutralizeUrl(s.substring(node.startOffset, node.endOffset)))
                        lastEnd = node.endOffset
                        changed = true
                        return
                    }
                }
                for (child in node.children) visitInsideSafeLink(child)
            }

            fun visit(node: ASTNode) {
                when (node.type) {
                    MarkdownElementTypes.CODE_FENCE,
                    MarkdownElementTypes.CODE_BLOCK,
                    MarkdownElementTypes.CODE_SPAN -> return

                    MarkdownElementTypes.IMAGE -> {
                        appendGap(node.startOffset)
                        val inner = node.children.firstOrNull {
                            it.type == MarkdownElementTypes.INLINE_LINK ||
                                it.type == MarkdownElementTypes.FULL_REFERENCE_LINK ||
                                it.type == MarkdownElementTypes.SHORT_REFERENCE_LINK
                        }
                        val labelNode = inner?.let(::labelNodeOf)
                        if (labelNode != null) appendStrippedLabel(out, labelNode, s, linkMap)
                        lastEnd = node.endOffset
                        changed = true
                        return
                    }

                    MarkdownElementTypes.LINK_DEFINITION -> {
                        if (!isSafeDefinition(node, s, allowedUrl)) {
                            appendGap(node.startOffset)
                            var end = node.endOffset
                            // Drop trailing newline when this unsafe definition occupies a standalone line.
                            val atLineStart = node.startOffset == 0 || s[node.startOffset - 1] == '\n'
                            if (atLineStart) {
                                if (end < s.length && s[end] == '\r') end++
                                if (end < s.length && s[end] == '\n') end++
                            }
                            lastEnd = end
                            changed = true
                        }
                        return
                    }

                    MarkdownElementTypes.INLINE_LINK -> {
                        val destNode = inlineLinkDestinationNode(node)
                        val safe = destNode != null && isSafeDestination(s.substring(destNode.startOffset, destNode.endOffset), allowedUrl)
                        val textNode = node.findChildOfType(MarkdownElementTypes.LINK_TEXT)
                        if (safe) {
                            appendGap(node.startOffset)
                            if (escapeTrailingUnescapedBang(out)) changed = true
                            if (textNode != null) {
                                // Emit '[', sanitize inside link text, then keep ']' + '(dest)'.
                                out.append(s, node.startOffset, textNode.startOffset + 1)
                                lastEnd = textNode.startOffset + 1
                                for (child in textNode.children) {
                                    if (child.startOffset >= textNode.startOffset + 1 && child.endOffset <= textNode.endOffset - 1) {
                                        visitInsideSafeLink(child)
                                    }
                                }
                                appendGap(node.endOffset)
                            } else {
                                appendGap(node.endOffset)
                            }
                        } else {
                            appendGap(node.startOffset)
                            if (textNode != null) appendStrippedLabel(out, textNode, s, linkMap)
                            lastEnd = node.endOffset
                            changed = true
                        }
                        return
                    }

                    MarkdownElementTypes.FULL_REFERENCE_LINK -> {
                        val textNode = node.findChildOfType(MarkdownElementTypes.LINK_TEXT)
                        val labelNode = node.findChildOfType(MarkdownElementTypes.LINK_LABEL)
                        val lookup = lookupRef(labelNode, textNode, s, linkMap)
                        if (lookup == null) {
                            // Unresolved `[a][b]` is plain text in CommonMark; still visit children for nested images/links.
                            for (child in node.children) visit(child)
                            return
                        }
                        if (isSafeDefinition(lookup.node, s, allowedUrl)) {
                            appendGap(node.startOffset)
                            if (escapeTrailingUnescapedBang(out)) changed = true
                            if (textNode != null) {
                                out.append(s, node.startOffset, textNode.startOffset + 1)
                                lastEnd = textNode.startOffset + 1
                                for (child in textNode.children) {
                                    if (child.startOffset >= textNode.startOffset + 1 && child.endOffset <= textNode.endOffset - 1) {
                                        visitInsideSafeLink(child)
                                    }
                                }
                                appendGap(node.endOffset)
                            } else {
                                appendGap(node.endOffset)
                            }
                        } else {
                            appendGap(node.startOffset)
                            if (textNode != null) appendStrippedLabel(out, textNode, s, linkMap)
                            lastEnd = node.endOffset
                            changed = true
                        }
                        return
                    }

                    MarkdownElementTypes.SHORT_REFERENCE_LINK -> {
                        val labelNode = node.findChildOfType(MarkdownElementTypes.LINK_LABEL)
                        val lookup = labelNode?.let { linkMap.getLinkInfo(s.substring(it.startOffset, it.endOffset)) }
                        if (lookup == null) {
                            for (child in node.children) visit(child)
                            return
                        }
                        if (isSafeDefinition(lookup.node, s, allowedUrl)) {
                            appendGap(node.startOffset)
                            if (escapeTrailingUnescapedBang(out)) changed = true
                            out.append(s, node.startOffset, labelNode.startOffset + 1)
                            lastEnd = labelNode.startOffset + 1
                            for (child in labelNode.children) {
                                if (child.startOffset >= labelNode.startOffset + 1 && child.endOffset <= labelNode.endOffset - 1) {
                                    visitInsideSafeLink(child)
                                }
                            }
                            appendGap(node.endOffset)
                        } else {
                            appendGap(node.startOffset)
                            appendStrippedLabel(out, labelNode, s, linkMap)
                            lastEnd = node.endOffset
                            changed = true
                        }
                        return
                    }

                    MarkdownElementTypes.AUTOLINK -> {
                        val inside = if (node.endOffset - node.startOffset >= 2) {
                            s.substring(node.startOffset + 1, node.endOffset - 1)
                        } else ""
                        val isEmail = node.findChildOfType(MarkdownTokenTypes.EMAIL_AUTOLINK) != null
                        if (isEmail || !allowedUrl(inside)) {
                            appendGap(node.startOffset)
                            out.append(neutralizeUrl(inside))
                            lastEnd = node.endOffset
                            changed = true
                        }
                        return
                    }

                    GFMTokenTypes.GFM_AUTOLINK,
                    MarkdownTokenTypes.EMAIL_AUTOLINK -> {
                        val raw = s.substring(node.startOffset, node.endOffset)
                        if (node.type == MarkdownTokenTypes.EMAIL_AUTOLINK || !allowedUrl(raw)) {
                            appendGap(node.startOffset)
                            out.append(neutralizeUrl(raw))
                            lastEnd = node.endOffset
                            changed = true
                        }
                        return
                    }
                }
                for (child in node.children) visit(child)
            }

            visit(tree)
            if (!changed) return s
            appendGap(s.length)
            return out.toString()
        }

        private fun hasUnsafeNode(s: String, allowedUrl: (String?) -> Boolean): Boolean {
            val tree = MarkdownParser(flavour).buildMarkdownTreeFromString(s)
            val linkMap = LinkMap.buildLinkMap(tree, s)
            fun check(node: ASTNode): Boolean {
                when (node.type) {
                    MarkdownElementTypes.CODE_FENCE,
                    MarkdownElementTypes.CODE_BLOCK,
                    MarkdownElementTypes.CODE_SPAN -> return false
                    MarkdownElementTypes.IMAGE -> return true
                    MarkdownElementTypes.LINK_DEFINITION -> return !isSafeDefinition(node, s, allowedUrl)
                    MarkdownElementTypes.INLINE_LINK -> {
                        val destNode = inlineLinkDestinationNode(node)
                        if (destNode == null || !isSafeDestination(s.substring(destNode.startOffset, destNode.endOffset), allowedUrl)) return true
                    }
                    MarkdownElementTypes.FULL_REFERENCE_LINK -> {
                        val info = lookupRef(
                            node.findChildOfType(MarkdownElementTypes.LINK_LABEL),
                            node.findChildOfType(MarkdownElementTypes.LINK_TEXT),
                            s,
                            linkMap,
                        )
                        if (info != null && !isSafeDefinition(info.node, s, allowedUrl)) return true
                    }
                    MarkdownElementTypes.SHORT_REFERENCE_LINK -> {
                        val label = node.findChildOfType(MarkdownElementTypes.LINK_LABEL)
                        val info = label?.let { linkMap.getLinkInfo(s.substring(it.startOffset, it.endOffset)) }
                        if (info != null && !isSafeDefinition(info.node, s, allowedUrl)) return true
                    }
                    MarkdownElementTypes.AUTOLINK -> {
                        val inside = if (node.endOffset - node.startOffset >= 2) s.substring(node.startOffset + 1, node.endOffset - 1) else ""
                        if (node.findChildOfType(MarkdownTokenTypes.EMAIL_AUTOLINK) != null || !allowedUrl(inside)) return true
                    }
                    GFMTokenTypes.GFM_AUTOLINK,
                    MarkdownTokenTypes.EMAIL_AUTOLINK -> {
                        if (node.type == MarkdownTokenTypes.EMAIL_AUTOLINK || !allowedUrl(s.substring(node.startOffset, node.endOffset))) return true
                    }
                }
                return node.children.any(::check)
            }
            return check(tree)
        }

        private fun lookupRef(labelNode: ASTNode?, textNode: ASTNode?, s: String, linkMap: LinkMap): LinkMap.LinkInfo? {
            val rawLabel = labelNode?.let { s.substring(it.startOffset, it.endOffset) }
            val key = if (rawLabel != null && rawLabel != "[]") rawLabel else textNode?.let { s.substring(it.startOffset, it.endOffset) }
            return key?.let(linkMap::getLinkInfo)
        }

        private fun labelNodeOf(node: ASTNode): ASTNode? =
            node.findChildOfType(MarkdownElementTypes.LINK_TEXT) ?: node.findChildOfType(MarkdownElementTypes.LINK_LABEL)

        /**
         * In `org.intellij.markdown`, `[t](<url>)` emits the destination as an `AUTOLINK` child (and if followed by a
         * title, emits a trailing `LINK_DESTINATION` for the title), whereas `[t](url)` emits `LINK_DESTINATION`.
         * Taking the first direct `LINK_DESTINATION` or `AUTOLINK` child always selects the actual destination.
         */
        private fun inlineLinkDestinationNode(node: ASTNode): ASTNode? =
            node.children.firstOrNull {
                it.type == MarkdownElementTypes.LINK_DESTINATION || it.type == MarkdownElementTypes.AUTOLINK
            }

        private fun isSafeDefinition(defNode: ASTNode, s: String, allowedUrl: (String?) -> Boolean): Boolean {
            val destNode = defNode.findChildOfType(MarkdownElementTypes.LINK_DESTINATION) ?: return false
            return isSafeDestination(s.substring(destNode.startOffset, destNode.endOffset), allowedUrl)
        }

        /**
         * Validates a CommonMark destination against both raw-authority tricks (entity/escape smuggling like `&sol;@` or `\/@`)
         * and the entity- + backslash-decoded URL so neither entity-decoding nor non-decoding renderers can be tricked.
         */
        private fun isSafeDestination(raw: String, allowedUrl: (String?) -> Boolean): Boolean {
            val trimmed = raw.trim()
            val unwrapped = if (trimmed.length >= 2 && trimmed.startsWith('<') && trimmed.endsWith('>')) {
                trimmed.substring(1, trimmed.length - 1)
            } else {
                trimmed
            }
            if (!unwrapped.startsWith("https://", ignoreCase = true)) return false
            val rawAuth = unwrapped.substring(8).substringBefore('/').substringBefore('?').substringBefore('#')
            if ('\\' in rawAuth || '@' in rawAuth || '&' in rawAuth || ';' in rawAuth) return false
            val decoded = EntityConverter.replaceEntities(unwrapped, processEntities = true, processEscapes = true)
            val unescaped = EntityConverter.replaceEntities(unwrapped, processEntities = false, processEscapes = true)
            return allowedUrl(decoded) && allowedUrl(unescaped)
        }

        /** Emits the label/alt inside `[...]` as plain text, escaping brackets and neutralizing nested images/links/autolinks. */
        private fun appendStrippedLabel(out: StringBuilder, bracketed: ASTNode, s: String, linkMap: LinkMap) {
            val innerStart = bracketed.startOffset + 1
            val innerEnd = bracketed.endOffset - 1
            if (innerEnd <= innerStart) return
            fun emit(node: ASTNode) {
                when (node.type) {
                    MarkdownElementTypes.CODE_SPAN -> {
                        out.append(s, node.startOffset, node.endOffset)
                        return
                    }
                    MarkdownElementTypes.IMAGE -> {
                        val inner = node.children.firstOrNull {
                            it.type == MarkdownElementTypes.INLINE_LINK ||
                                it.type == MarkdownElementTypes.FULL_REFERENCE_LINK ||
                                it.type == MarkdownElementTypes.SHORT_REFERENCE_LINK
                        }
                        inner?.let(::labelNodeOf)?.let { appendStrippedLabel(out, it, s, linkMap) }
                        return
                    }
                    MarkdownElementTypes.INLINE_LINK -> {
                        labelNodeOf(node)?.let { appendStrippedLabel(out, it, s, linkMap) }
                        return
                    }
                    MarkdownElementTypes.FULL_REFERENCE_LINK,
                    MarkdownElementTypes.SHORT_REFERENCE_LINK -> {
                        val resolved = lookupRef(
                            node.findChildOfType(MarkdownElementTypes.LINK_LABEL),
                            node.findChildOfType(MarkdownElementTypes.LINK_TEXT),
                            s,
                            linkMap,
                        )
                        if (resolved != null) {
                            labelNodeOf(node)?.let { appendStrippedLabel(out, it, s, linkMap) }
                            return
                        }
                    }
                    MarkdownElementTypes.AUTOLINK -> {
                        val inside = if (node.endOffset - node.startOffset >= 2) s.substring(node.startOffset + 1, node.endOffset - 1) else ""
                        out.append(neutralizeUrl(inside))
                        return
                    }
                    GFMTokenTypes.GFM_AUTOLINK,
                    MarkdownTokenTypes.EMAIL_AUTOLINK -> {
                        out.append(neutralizeUrl(s.substring(node.startOffset, node.endOffset)))
                        return
                    }
                }
                if (node.children.isEmpty()) {
                    var i = node.startOffset
                    while (i < node.endOffset) {
                        val c = s[i]
                        if (c == '[' || c == ']') {
                            if (trailingBackslashCount(out) % 2 == 0) out.append('\\')
                            out.append(c)
                        } else {
                            out.append(c)
                        }
                        i++
                    }
                } else {
                    for (child in node.children) {
                        if (child.startOffset >= innerStart && child.endOffset <= innerEnd) emit(child)
                    }
                }
            }
            for (child in bracketed.children) {
                if (child.startOffset >= innerStart && child.endOffset <= innerEnd) emit(child)
            }
        }

        private fun trailingBackslashCount(out: StringBuilder): Int {
            var backslashes = 0
            var k = out.length - 1
            while (k >= 0 && out[k] == '\\') { backslashes++; k-- }
            return backslashes
        }

        private fun escapeTrailingUnescapedBang(out: StringBuilder): Boolean {
            val len = out.length
            if (len == 0 || out[len - 1] != '!') return false
            var backslashes = 0
            var k = len - 2
            while (k >= 0 && out[k] == '\\') { backslashes++; k-- }
            if (backslashes % 2 == 0) {
                out.insert(len - 1, '\\')
                return true
            }
            return false
        }

        /** Breaks CommonMark `<scheme:...>` and GFM bare URL/www/email autolinks while preserving visible characters. */
        private fun neutralizeUrl(raw: String): String {
            val sb = StringBuilder(raw.length + 4)
            var i = 0
            while (i < raw.length) {
                val c = raw[i]
                val escape = c == ':' || c == '@' ||
                    (i >= 3 && c == '.' && raw.regionMatches(i - 3, "www", 0, 3, ignoreCase = true))
                if (escape && trailingBackslashCount(sb) % 2 == 0) sb.append('\\')
                sb.append(c)
                i++
            }
            return sb.toString()
        }

        private fun escapeMarkdownLinks(s: String): String {
            val sb = StringBuilder(s.length + 16)
            for (c in s) {
                if (c in "\\[]!<>:@.()`~") sb.append('\\')
                sb.append(c)
            }
            return sb.toString()
        }
    }

    /** Lazy-list keys for a section's items: the extension's [ExtensionViewItem.key] when it gives one (and unique). */
    fun itemKeys(items: List<ExtensionViewItem>): List<String?> {
        val seen = HashSet<String>()
        return items.map { item -> item.key?.takeIf { it.isNotBlank() && seen.add(it) } }
    }

    /** Fields aren't drawn on a card (the hub refuses them there too): it opens a page for that. */
    fun drawableOnCard(item: ExtensionViewItem): Boolean = item.type != "field" && item.type != "table" && item.type != "html" && drawable(item)

    /** How deep pages pushed on each other may go (docs/extensions/extensions.md "Page navigation"). */
    const val MaxDepth = 8

    /**
     * The page a push opens on top of [uiId]'s ([depth] pages up): the push's UI of the same extension (this one when
     * empty), its state and title; none past [MaxDepth].
     */
    fun pushed(extensionId: String, uiId: String, title: String, depth: Int, push: ExtensionViewPush, manager: String? = null): persona.core.navigation.ExtensionUiRoute? {
        if (depth + 1 >= MaxDepth) return null
        // the page it opens is the same manager's extension (several managers: the same id is under each)
        return persona.core.navigation.ExtensionUiRoute(extensionId, push.ui?.takeIf { it.isNotBlank() } ?: uiId,
            push.title?.takeIf { it.isNotBlank() } ?: title, push.state, depth + 1, manager)
    }

    /**
     * The whole page a push on a card, a machine's section or a launcher's section opens (it has no stack of its own):
     * the push's UI, else the place's own.
     */
    fun opened(place: ExtensionUiPlace, push: ExtensionViewPush): persona.core.navigation.ExtensionUiRoute =
        persona.core.navigation.ExtensionUiRoute(place.extension, push.ui?.takeIf { it.isNotBlank() } ?: place.id,
            push.title?.takeIf { it.isNotBlank() } ?: place.title.ifBlank { place.id }, push.state,
            manager = place.manager?.takeIf { it.isNotBlank() && it != persona.core.contracts.api.AssistantManagerChoice.current.value.defaultId })

    /** Whether an `extension_ui` event asks to draw [extension]'s UI [ui] again (`ui.invalidate`, all of it when no id). */
    fun invalidates(event: persona.core.contracts.ExtensionUiEvent, extension: String, ui: String): Boolean =
        event.invalidate && event.extension == extension && (event.id.isNullOrEmpty() || event.id == ui)

    /** How long the search box waits after typing stops before asking the extension. */
    const val SearchPauseMillis = 300L

    /** How a chart's value is written: the extension's own words, else short in [locale] with the unit (`1.2M tokens`). */
    fun display(value: Double, display: String?, unit: String?, locale: java.util.Locale = java.util.Locale.ENGLISH): String =
        display?.takeIf { it.isNotBlank() } ?: (ChartMath.compact(value, locale) + (unit?.takeIf { it.isNotBlank() }?.let { " $it" } ?: ""))

    /**
     * A chart item as the design system's chart: the first series' labels along the bottom, every series cut to as
     * many points (the hub checks they match), a role colour for a series with a tone; numbers in [locale]'s words.
     * None when there is nothing to draw.
     */
    fun chart(item: ExtensionViewItem, locale: java.util.Locale = java.util.Locale.ENGLISH): JChartData? {
        val kind = when (item.kind) {
            "bar" -> JChartKind.Bar; "line" -> JChartKind.Line
            // UI version 6
            "area" -> JChartKind.Area; "candlestick" -> JChartKind.Candlestick; "donut" -> JChartKind.Donut; "pie" -> JChartKind.Pie
            else -> return null
        }
        val single = kind == JChartKind.Candlestick || kind == JChartKind.Donut || kind == JChartKind.Pie
        val series = item.series.orEmpty().take(if (single) 1 else 4)
        val first = series.firstOrNull()?.points.orEmpty().take(if (kind == JChartKind.Candlestick) 240 else if (single) 8 else 120)
        val labels = first.map { it.label }
        if (labels.isEmpty()) return null
        // a candlestick's points all have their four numbers (the hub checks it; an older one may not)
        val candles = if (kind == JChartKind.Candlestick) first.map { p ->
            JCandle(p.open ?: return null, p.high ?: return null, p.low ?: return null, p.close ?: p.value, p.volume)
        } else emptyList()
        val volumes = candles.all { it.volume != null }
        return JChartData(kind, labels, series.map { s ->
            val points = s.points.take(labels.size)
            JChartSeries(s.name?.takeIf { it.isNotBlank() }, points.map { it.value }, points.map { display(it.value, it.display, item.unit, locale) },
                s.tone?.takeIf { it.isNotBlank() }?.let(::tone), hue(s.color))
        }, stacked = item.stacked == true && (kind == JChartKind.Bar || kind == JChartKind.Area),
            axis = { v -> if (kind == JChartKind.Candlestick) price(v, locale) else ChartMath.compact(v, locale) },
            candles = if (volumes) candles else candles.map { it.copy(volume = null) },
            risingRed = item.rising == "red", center = item.center?.takeIf { it.isNotBlank() && kind == JChartKind.Donut })
    }

    /** A price on a candlestick's scale and in its numbers: up to two decimals ("182.35", "1,234.5"), large ones short. */
    fun price(value: Double, locale: java.util.Locale = java.util.Locale.ENGLISH): String = if (!value.isFinite()) "—"
    else if (kotlin.math.abs(value) >= 100_000) ChartMath.compact(value, locale)
    else java.math.BigDecimal(value).setScale(2, java.math.RoundingMode.HALF_UP).stripTrailingZeros().toPlainString()

    /** A stat_grid's numbers, up to six. */
    fun stats(item: ExtensionViewItem): List<JStat> = item.stats.orEmpty().take(6).map { JStat(it.label, it.value, it.detail, tone(it.tone), spark(it.spark)) }

    /** When a view wants drawing again, clamped to the hub's bounds (30 s to a day); null: it does not. */
    fun refreshAt(state: ExtensionViewState): Long? {
        val seconds = state.view?.refreshAfter?.takeIf { it > 0 } ?: return null
        val drawn = state.drawnAt ?: return null
        return drawn + seconds.coerceIn(30, 24 * 3600) * 1000L
    }

    fun percent(item: ExtensionViewItem): String = "${Math.round(ratio(item) * 100)}%"

    /** A confirm dialog's title: what is about to happen. */
    fun confirmTitle(res: Resources, item: ExtensionViewItem): String = item.title?.takeIf { it.isNotBlank() } ?: res.getString(R.string.settings_ext_confirm_title)

    /** Its button: the pressed button's own words, so "Disable" reads "Disable". */
    fun confirmLabel(res: Resources, item: ExtensionViewItem): String = item.title?.takeIf { it.isNotBlank() && it.length <= 8 } ?: res.getString(R.string.settings_ext_confirm_ok)

    /** A gateway's host for a line of text (`https://gw.example.com/v1` → `gw.example.com`). */
    fun host(url: String?): String? = url?.takeIf { it.isNotBlank() }?.let {
        runCatching { java.net.URI(it).host }.getOrNull()?.takeIf { h -> h.isNotBlank() } ?: it
    }

    /** A number field typed into: digits, one point, a leading minus. */
    fun isNumber(text: String): Boolean = text.isEmpty() || text.matches(Regex("-?\\d*(\\.\\d*)?"))
}

internal data class ExtensionViewState(
    val view: ExtensionView? = null,
    /** The fields as they are now, by name: what a submitting button sends. */
    val values: Map<String, String> = emptyMap(),
    val loading: Boolean = false,
    /** The action on its way (its id): its button spins, the others wait. */
    val acting: String? = null,
    /** Reading the view failed: the page shows it with "Retry". */
    val error: ExtensionFailure? = null,
    /** Why reading it last failed (an inbox item's page tells a refusal from a breakdown); none once drawn. */
    val failure: Throwable? = null,
    /**
     * The extension said this launcher's gateway is not its own, or the hub does not know the machine of a machine's
     * section: the section is not drawn at all.
     */
    val unsupported: Boolean = false,
    /** When the view was last drawn (or tried), for `refresh_after`. */
    val drawnAt: Long? = null,
    /** What the search box holds now (UI version 3); null: as the view says. */
    val query: String? = null,
    /** A section's "Load more" on its way (the section's index): its row spins, the page stays usable. */
    val loadingMore: Int? = null,
    /**
     * UI version 6: the user's values of controls (by name) whose change is on its way or about to go: drawn in place of
     * the extension's until the answer is back, even across a redraw.
     */
    val controls: Map<String, String> = emptyMap(),
    /** The controls whose change is on its way (one request each at a time). */
    val sending: Set<String> = emptySet(),
) {
    val busy: Boolean get() = loading || acting != null
}

/**
 * What pressing a button brought back for the page to show: a notice, a link to open, a failure, and since UI version 3
 * a page to draw on top ([push]) or going back ([pop]).
 */
/** A picture the extension serves through the hub (UI version 6): its signed [path], loaded with the device's credentials. */
internal data class ExtensionHubPicture(val path: String)

internal data class ExtensionActionOutcome(
    val toast: String? = null,
    val openUrl: String? = null,
    val error: ExtensionFailure? = null,
    val push: ExtensionViewPush? = null,
    val pop: Boolean = false,
)

/**
 * One view an extension draws: at [place], with [context] (a launcher's page, a machine's, a card's size). [now] is the
 * clock `refresh_after` counts on; [opens] is which `open_url` a button may bring back (https, and the subscription
 * pages' Set Up with …; an inbox item's page passes its own, which also allows its source link).
 */
internal class ExtensionViewModel(
    private val api: ExtensionUi,
    val extension: String,
    val ui: String,
    private val context: ExtensionUiContext? = null,
    private val now: () -> Long = System::currentTimeMillis,
    private val opens: (String) -> Boolean = { url -> ExtensionViewText.opens(extension, url) },
    /** A pushed page's first state (the push's); later drawings send the view's own. */
    private val initialState: String? = null,
) {
    private val mutable = MutableStateFlow(ExtensionViewState())
    val state = mutable.asStateFlow()

    /** The view is on screen ([shown]); an `ui.invalidate` while it is not marks it [stale], drawn when it comes back. */
    private var shown = false
    private var stale = false
    /** Answers to the search box come back in order; an older one that arrives late is dropped. */
    private var asked = 0

    constructor(api: ExtensionUi, place: ExtensionUiPlace, context: ExtensionUiContext? = null) : this(api, place.extension, place.id, context)

    private fun currentState(): String? = state.value.view?.state ?: initialState

    /**
     * Draws it (again), with the extension's state of the last view. [keepTyped]: a redraw the user did not ask for
     * (`refresh_after`, `ui.invalidate`, back from a page pushed on it, its inbox item changed) keeps what is typed in the
     * fields the new view still has, as on the web and iOS; the first draw, Retry, pull to refresh and a press that answers
     * nothing take the extension's values.
     */
    suspend fun load(keepTyped: Boolean = false) {
        if (state.value.loading) return
        mutable.update { it.copy(loading = true) }
        try {
            val view = api.renderUi(extension, ui, context, currentState())
            // drawn again for the same page: the diffs loaded since stay (UI version 5)
            mutable.update {
                val fresh = ExtensionViewText.fields(view)
                it.copy(view = ExtensionCodeReview.keepLoaded(it.view, view), values = if (keepTyped) fresh + it.values.filterKeys { name -> name in fresh } else fresh,
                    error = null, failure = null, unsupported = false)
            }
        } catch (cancelled: CancellationException) { throw cancelled }
        catch (_: ExtensionUiUnsupported) { mutable.update { it.copy(unsupported = true, error = null) } }
        catch (_: ExtensionUiNotFound) { mutable.update { it.copy(unsupported = true, error = null) } }
        catch (error: Exception) { mutable.update { it.copy(error = ExtensionFailure(R.string.settings_ext_load_failed, failureReasonText(error)), failure = error) } }
        finally { mutable.update { it.copy(loading = false, drawnAt = now()) } }
    }

    /**
     * The extension's data changed (`ui.invalidate`, docs/extensions/extensions.md "Declarative UI"): drawn again if on screen, else when it
     * comes back.
     */
    suspend fun invalidate() {
        if (shown) load(keepTyped = true) else stale = true
    }

    /**
     * The view came on screen or left it (the page resumed or paused): one that was invalidated meanwhile, or that a
     * page was pushed on ([covered]: the user comes back from it, maybe after saving there), is drawn now.
     */
    suspend fun shown(on: Boolean) {
        shown = on
        if (on && stale && state.value.view != null) {
            stale = false
            load(keepTyped = true)
        }
    }

    /** A page of the extension was pushed on this one: it is drawn again when it shows (docs/extensions/extensions.md "Page navigation"). */
    fun covered() { stale = true }

    /**
     * Draws it again when its `refresh_after` is up, for as long as it is called (the page runs it while it is
     * resumed): coming back after the time is up draws it at once. A view without `refresh_after` just waits.
     */
    suspend fun keepFresh() {
        state.map(ExtensionViewText::refreshAt).distinctUntilChanged().collectLatest { at ->
            at ?: return@collectLatest
            delay((at - now()).coerceAtLeast(0))
            load(keepTyped = true)
        }
    }

    /**
     * A segmented control was switched to [value]: shown at once, and its action goes to the extension with
     * `{name: value}` (and the page's fields when it submits). If that fails the old choice comes back.
     */
    suspend fun choose(item: ExtensionViewItem, value: String): ExtensionActionOutcome {
        val name = item.name?.takeIf { it.isNotEmpty() } ?: return ExtensionActionOutcome()
        val action = item.action?.takeIf { it.id.isNotEmpty() } ?: return ExtensionActionOutcome()
        val before = state.value.values[name]
        if (before == value || state.value.busy) return ExtensionActionOutcome()
        edit(name, value)
        val outcome = press(action, mapOf(name to value))
        if (outcome.error != null) mutable.update { s -> s.copy(values = s.values + (name to (before ?: ExtensionViewText.initial(item)))) }
        return outcome
    }

    fun edit(name: String, value: String) = mutable.update { it.copy(values = it.values + (name to value)) }

    /** The latest value waiting for a control whose change is on its way (sent once that one is back). */
    private val queued = HashMap<String, Pair<persona.core.contracts.api.ExtensionRowControl, String>>()

    /** Shows [value] for [name] at once, before its change goes (a stepper's taps, a slider let go). */
    fun hold(name: String, value: String) = mutable.update { it.copy(controls = it.controls + (name to value)) }

    /**
     * A control was changed to [value] (UI version 6): shown at once, sent with `{name: value}`, one request at a time
     * (a change while one is on its way waits, only the latest). The answer's view or items say what is so (the
     * extension's value); nothing back draws the page again; a failure puts back the extension's last value.
     */
    suspend fun change(control: persona.core.contracts.api.ExtensionRowControl, value: String): ExtensionActionOutcome {
        val name = control.name.takeIf { it.isNotEmpty() } ?: return ExtensionActionOutcome()
        if (control.action.id.isEmpty()) return ExtensionActionOutcome()
        hold(name, value)
        if (name in state.value.sending) { queued[name] = control to value; return ExtensionActionOutcome() }
        mutable.update { it.copy(sending = it.sending + name) }
        var next: Pair<persona.core.contracts.api.ExtensionRowControl, String>? = control to value
        var outcome = ExtensionActionOutcome()
        try {
            while (next != null) {
                val (c, v) = next
                outcome = sendControl(c, v)
                next = queued.remove(name)
                if (outcome.error != null) next = null
            }
        } finally {
            queued.remove(name)
            mutable.update { it.copy(sending = it.sending - name, controls = it.controls - name) }
        }
        return outcome
    }

    private suspend fun sendControl(control: persona.core.contracts.api.ExtensionRowControl, value: String): ExtensionActionOutcome = try {
        val action = control.action
        val values = (if (action.submit) state.value.values else emptyMap()) + action.values.orEmpty() + (control.name to value)
        val result = api.uiAction(extension, ui, action.id, context, currentState(), values)
        val nextView = result.view
        when {
            nextView != null -> mutable.update { s ->
                val fresh = ExtensionViewText.fields(nextView)
                s.copy(view = ExtensionCodeReview.keepLoaded(s.view, nextView), values = fresh + s.values.filterKeys { it in fresh }, error = null, drawnAt = now())
            }
            result.items.isNotEmpty() -> mutable.update { s -> s.copy(view = s.view?.let { ExtensionCodeReview.patchView(it, result.items) }) }
            result.toast == null && result.openUrl == null && result.push == null && !result.pop -> redraw()
        }
        ExtensionActionOutcome(result.toast?.takeIf { it.isNotBlank() }, result.openUrl?.takeIf(opens), push = result.push, pop = result.pop)
    } catch (cancelled: CancellationException) { throw cancelled }
    catch (error: Exception) { ExtensionActionOutcome(error = ExtensionFailure(R.string.settings_ext_action_failed, failureReasonText(error))) }

    /** Draws the view again without holding the page (a control's answer said nothing). */
    private suspend fun redraw() {
        val view = try { api.renderUi(extension, ui, context, currentState()) }
        catch (cancelled: CancellationException) { throw cancelled }
        catch (_: Exception) { return }
        mutable.update { s ->
            val fresh = ExtensionViewText.fields(view)
            s.copy(view = ExtensionCodeReview.keepLoaded(s.view, view), values = fresh + s.values.filterKeys { it in fresh }, drawnAt = now())
        }
    }

    private var resigned = Long.MIN_VALUE / 2

    /**
     * A picture the extension serves (UI version 6), from the hub. One whose signature has expired draws the page again
     * (which signs its pictures anew), at most once every 30 seconds.
     */
    suspend fun picture(path: String): ByteArray = try { api.uiImage(path) }
    catch (expired: persona.core.contracts.api.ExtensionImageExpired) {
        if (now() - resigned > 30_000) { resigned = now(); redraw() }
        throw expired
    }

    /**
     * An action that does not hold the page (search, filter, "Load more"): sent with [values]; a new view replaces this one,
     * keeping what is being typed into its fields; [before] marks the state while it goes, [loadingMore] the section
     * spinning. An answer that a later one has overtaken is dropped.
     */
    internal suspend fun quietly(id: String, values: Map<String, String>, loadingMore: Int? = null,
                                 before: (ExtensionViewState) -> ExtensionViewState): ExtensionActionOutcome {
        val ask = ++asked
        mutable.update { before(it).copy(loadingMore = loadingMore ?: it.loadingMore) }
        return try {
            val result = api.uiAction(extension, ui, id, context, currentState(), values)
            if (ask == asked) result.view?.let { view ->
                mutable.update { s ->
                    // What was typed stays; the view's new fields come in with its values.
                    val fresh = ExtensionViewText.fields(view)
                    s.copy(view = ExtensionCodeReview.keepLoaded(s.view, view), values = fresh + s.values.filterKeys { it in fresh }, error = null, drawnAt = now())
                }
            }
            ExtensionActionOutcome(result.toast?.takeIf { it.isNotBlank() }, result.openUrl?.takeIf(opens), push = result.push, pop = result.pop)
        } catch (cancelled: CancellationException) { throw cancelled }
        catch (error: Exception) { ExtensionActionOutcome(error = ExtensionFailure(R.string.settings_ext_action_failed, failureReasonText(error))) }
        finally { if (loadingMore != null) mutable.update { it.copy(loadingMore = null) } }
    }

    /**
     * An action that does not hold the page (UI version 5: a diff's hidden lines, a viewed mark, a diff loaded as it
     * comes near): sent with the action's own [values]; its components go in place, a new view replaces the page keeping
     * what is typed. Says what was answered; null when it failed.
     */
    suspend fun fetch(action: ExtensionViewAction, values: Map<String, String>): ExtensionUiResult? = try {
        val result = api.uiAction(extension, ui, action.id, context, currentState(), values + action.values.orEmpty())
        val view = result.view
        when {
            view != null -> mutable.update { s ->
                val fresh = ExtensionViewText.fields(view)
                s.copy(view = ExtensionCodeReview.keepLoaded(s.view, view), values = fresh + s.values.filterKeys { it in fresh }, drawnAt = now())
            }
            result.items.isNotEmpty() -> mutable.update { s -> s.copy(view = s.view?.let { ExtensionCodeReview.patchView(it, result.items) }) }
        }
        result
    } catch (cancelled: CancellationException) { throw cancelled }
    catch (_: Exception) { null }

    /**
     * A button or a row was pressed (after its confirm, which the page asks): sends it, with the fields when it
     * submits. A new view replaces the page; nothing back draws it again.
     */
    suspend fun press(action: ExtensionViewAction, values: Map<String, String>? = null): ExtensionActionOutcome {
        // A push is not sent: the page draws the extension's page on top (UI version 3).
        action.push?.let { return ExtensionActionOutcome(push = it) }
        if (state.value.busy || action.id.isEmpty()) return ExtensionActionOutcome()
        mutable.update { it.copy(acting = action.id) }
        return try {
            val current = state.value
            val result = api.uiAction(extension, ui, action.id, context, current.view?.state ?: initialState,
                ExtensionViewText.sent(action, current.values, values))
            val nextView = result.view
            when {
                nextView != null -> mutable.update { it.copy(view = ExtensionCodeReview.keepLoaded(it.view, nextView), values = ExtensionViewText.fields(nextView), error = null, drawnAt = now()) }
                // UI version 5: components in place, or lines for a diff, say enough
                result.items.isNotEmpty() -> mutable.update { s -> s.copy(view = s.view?.let { ExtensionCodeReview.patchView(it, result.items) }) }
                result.lines != null -> Unit
                result.toast == null && result.openUrl == null && result.push == null && !result.pop -> { mutable.update { it.copy(acting = null) }; load() }
            }
            ExtensionActionOutcome(result.toast?.takeIf { it.isNotBlank() }, result.openUrl?.takeIf(opens), push = result.push, pop = result.pop)
        } catch (cancelled: CancellationException) { throw cancelled }
        catch (_: ExtensionUiUnsupported) {
            mutable.update { it.copy(unsupported = true) }
            ExtensionActionOutcome()
        } catch (error: Exception) {
            ExtensionActionOutcome(error = ExtensionFailure(R.string.settings_ext_action_failed, failureReasonText(error)))
        } finally { mutable.update { it.copy(acting = null) } }
    }
}

/**
 * The search box was typed into (the page waits for a pause first): the view's search action with `{q}`, and the
 * filter's choice beside it. The page stays usable meanwhile; an answer overtaken by a later one is dropped.
 */
internal suspend fun ExtensionViewModel.search(query: String): ExtensionActionOutcome {
    val view = state.value.view ?: return ExtensionActionOutcome()
    val search = view.search?.action?.takeIf { it.id.isNotEmpty() } ?: return ExtensionActionOutcome()
    val values = buildMap {
        put("q", query)
        view.filter?.takeIf { it.name.isNotEmpty() }?.let { f -> (state.value.values[f.name] ?: f.value)?.let { put(f.name, it) } }
    }
    return quietly(search.id, values) { it.copy(query = query) }
}

/** The filter beside the search box was set to [value]: its action with `{name: value}` and the search's `q`. */
internal suspend fun ExtensionViewModel.filter(value: String): ExtensionActionOutcome {
    val view = state.value.view ?: return ExtensionActionOutcome()
    val filter = view.filter?.takeIf { it.name.isNotEmpty() && it.action.id.isNotEmpty() } ?: return ExtensionActionOutcome()
    val q = state.value.query ?: view.search?.value
    val values = buildMap {
        put(filter.name, value)
        q?.let { put("q", it) }
    }
    edit(filter.name, value)
    return quietly(filter.action.id, values) { it }
}

/** The end of section [index] came on screen, or its "Load more" was tapped: its action, once until the answer is back. */
internal suspend fun ExtensionViewModel.more(index: Int, section: ExtensionViewSection): ExtensionActionOutcome {
    val action = section.more?.action?.takeIf { it.id.isNotEmpty() } ?: return ExtensionActionOutcome()
    if (state.value.loadingMore != null || state.value.loading) return ExtensionActionOutcome()
    return quietly(action.id, emptyMap(), loadingMore = index) { it }
}

internal data class ExtensionPlacesState(
    val places: List<ExtensionUiPlace>? = null,
    val loading: Boolean = false,
    val error: ExtensionFailure? = null,
)

/** Where extensions draw at one placement: a launcher's sections. */
internal class ExtensionPlacesModel(private val api: ExtensionUi, private val placement: String, private val context: ExtensionUiContext? = null) {
    private val mutable = MutableStateFlow(ExtensionPlacesState())
    val state = mutable.asStateFlow()

    suspend fun load() {
        if (state.value.loading) return
        mutable.update { it.copy(loading = true) }
        try {
            val places = api.uiPlaces(placement, context?.launcher, context?.machine).filter { it.placement.isEmpty() || it.placement == placement }
            mutable.update { it.copy(places = places, error = null) }
        } catch (cancelled: CancellationException) { throw cancelled }
        catch (error: Exception) { mutable.update { it.copy(error = ExtensionFailure(R.string.settings_ext_content_failed, failureReasonText(error))) } }
        finally { mutable.update { it.copy(loading = false) } }
    }
}

/**
 * Why a view could not be read or a button did not work: the sentence (a string with one `%1$s`) and the reason in it,
 * put into words where it is shown, in the language shown then.
 */
data class ExtensionFailure(@param:StringRes val sentence: Int, val reason: UiText) {
    constructor(@StringRes sentence: Int, reason: String) : this(sentence, UiText.Plain(reason))

    fun text(res: Resources): String = res.getString(sentence, reason.resolve(res))
}

@Composable
internal fun ExtensionFailure.text(): String = text(LocalResources.current)

/** A tab was chosen (UI version 4): its action with `{name: value}` and the search's `q`; shown at once. */
internal suspend fun ExtensionViewModel.tab(value: String): ExtensionActionOutcome {
    val view = state.value.view ?: return ExtensionActionOutcome()
    val tabs = view.tabs?.takeIf { it.name.isNotEmpty() && it.action.id.isNotEmpty() } ?: return ExtensionActionOutcome()
    if ((state.value.values[tabs.name] ?: tabs.value) == value) return ExtensionActionOutcome()
    val q = state.value.query ?: view.search?.value
    val values = buildMap {
        put(tabs.name, value)
        q?.let { put("q", it) }
        view.filter?.takeIf { it.name.isNotEmpty() }?.let { f -> (state.value.values[f.name] ?: f.value)?.let { put(f.name, it) } }
    }
    edit(tabs.name, value)
    return quietly(tabs.action.id, values) { it }
}

/**
 * A code hosting component's action (UI version 4) with its own values (a path, a line, the text typed): sent like a
 * press, the action's own values on top.
 */
internal suspend fun ExtensionViewModel.act(action: ExtensionViewAction, values: Map<String, String>): ExtensionActionOutcome = press(action, values)
