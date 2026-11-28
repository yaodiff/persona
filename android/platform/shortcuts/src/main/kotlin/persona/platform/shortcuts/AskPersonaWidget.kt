package persona.platform.shortcuts

import android.appwidget.AppWidgetManager
import android.content.Context
import androidx.glance.appwidget.GlanceAppWidgetManager
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import androidx.compose.ui.unit.Dp
import androidx.glance.ColorFilter
import androidx.glance.GlanceId
import androidx.glance.GlanceModifier
import androidx.glance.Image
import androidx.glance.ImageProvider
import androidx.glance.LocalSize
import androidx.glance.action.clickable
import androidx.glance.appwidget.GlanceAppWidget
import androidx.glance.appwidget.GlanceAppWidgetReceiver
import androidx.glance.appwidget.SizeMode
import androidx.glance.appwidget.action.actionStartActivity
import androidx.glance.appwidget.appWidgetBackground
import androidx.glance.appwidget.cornerRadius
import androidx.glance.appwidget.provideContent
import androidx.glance.appwidget.updateAll
import androidx.glance.background
import androidx.glance.layout.Alignment
import androidx.glance.layout.Box
import androidx.glance.layout.Column
import androidx.glance.layout.Row
import androidx.glance.layout.Spacer
import androidx.glance.layout.fillMaxSize
import androidx.glance.layout.height
import androidx.glance.layout.padding
import androidx.glance.layout.size
import androidx.glance.layout.width
import androidx.glance.text.Text
import persona.core.designsystem.Tokens

/**
 * The standalone "Ask Persona" home-screen widget: a single-purpose everyday entry to Persona's main chat, aligned with
 * the single-responsibility widget design in `docs/apple/home-widgets.md` and distinct from the Agents fleet widget
 * ([PersonaWidget]). Reads only local pairing and appearance state (no extra permissions, no network calls). Tapping
 * opens the main chat with the composer focused when paired ([SurfaceLinks.ask]), or the pairing page otherwise
 * ([SurfaceLinks.app]).
 */
class AskPersonaWidget : GlanceAppWidget() {
    override val sizeMode: SizeMode = SizeMode.Exact

    override suspend fun provideGlance(context: Context, id: GlanceId) {
        val dependencies = SurfacesDependencies.of(context)
        val feed = dependencies.fleet()
        feed.settled()
        val assistant = dependencies.assistant()
        val sessions = dependencies.sessions()
        // Read inside the composition so Glance sessions update when pairing, binding, or appearance changes.
        provideContent {
            val paired by feed.paired.collectAsState()
            val assistantSnapshot by assistant.assistant.collectAsState()
            val sessionList by sessions.sessions.collectAsState()
            val revision by WidgetLook.revision.collectAsState()
            val look = remember(revision) { WidgetLook.current(context) }
            val activePairing = if (paired) feed.activePairing()?.takeIf(String::isNotBlank) else null
            val appWidgetId = remember(id) {
                runCatching { GlanceAppWidgetManager(context).getAppWidgetId(id) }
                    .getOrDefault(AppWidgetManager.INVALID_APPWIDGET_ID)
            }
            val binding = remember(activePairing, appWidgetId, revision) {
                ConversationWidgetPrefs.read(context, appWidgetId, activePairing)
            }
            val target = remember(paired, binding, assistantSnapshot, sessionList) {
                AskWidgetTarget.resolve(
                    paired = paired,
                    binding = binding,
                    assistant = assistantSnapshot,
                    sessions = sessionList,
                )
            }
            Content(context, look, paired, target, appWidgetId)
        }
    }

    @Composable
    private fun Content(
        context: Context,
        look: WidgetLook,
        paired: Boolean,
        target: AskWidgetTarget,
        appWidgetId: Int,
    ) {
        val size = LocalSize.current
        val scale = context.resources.configuration.fontScale
        val spec = AskWidgetLayout.resolve(size.width.value, size.height.value, scale, paired)
        val horizontalPadding = if (spec.padding == AskWidgetPadding.Normal) Tokens.Space.m else Tokens.Space.s
        val verticalPadding = when (spec.padding) {
            AskWidgetPadding.Normal -> Tokens.Space.m
            AskWidgetPadding.Tight -> Tokens.Space.s
            AskWidgetPadding.CompactVertical -> Tokens.Space.xs
            AskWidgetPadding.FlushVertical -> Tokens.Space.flush
        }
        val title = when {
            !paired -> context.getString(R.string.shortcuts_not_paired_short)
            !target.isMainChat && !target.title.isNullOrBlank() -> target.title
            else -> context.getString(R.string.shortcuts_ask_label)
        }
        val subtitle = when {
            !paired -> context.getString(R.string.shortcuts_widget_scan)
            target.bindingMissing -> context.getString(R.string.shortcuts_widget_binding_missing_short)
            !target.isMainChat -> context.getString(R.string.shortcuts_ask_label)
            else -> context.getString(R.string.shortcuts_ask_widget_subtitle)
        }
        val titleStyle = when (spec.titleScale) {
            AskWidgetTitleScale.Hero -> look.headline
            AskWidgetTitleScale.Section -> look.sectionTitle
            AskWidgetTitleScale.Compact -> look.itemTitle
        }
        val tapIntent = when {
            !paired -> SurfaceLinks.app(context)
            target.bindingMissing -> ConversationWidgetConfigureActivity.reconfigureIntent(context, appWidgetId)
            !target.isMainChat && target.sessionKey != null ->
                SurfaceLinks.askConversation(context, target.sessionKey, assistant = target.assistant)
            else -> SurfaceLinks.askWidget(context, paired = true)
        }
        val container = GlanceModifier.fillMaxSize().appWidgetBackground()
            .widgetCornerRadius()
            .background(look.card)
            .padding(horizontal = horizontalPadding, vertical = verticalPadding)
            .clickable(actionStartActivity(tapIntent))
        when (spec.mode) {
            AskWidgetMode.Row -> Row(container, verticalAlignment = Alignment.CenterVertically) {
                Mark(look, spec.badge)
                Spacer(GlanceModifier.width(if (spec.padding == AskWidgetPadding.Normal) Tokens.Space.m else Tokens.Space.s))
                Column(GlanceModifier.defaultWeight()) {
                    Text(title, style = titleStyle, maxLines = spec.titleMaxLines)
                    if (spec.showSubtitle) {
                        Spacer(GlanceModifier.height(Tokens.Space.xxs))
                        Text(subtitle, style = look.caption, maxLines = spec.subtitleMaxLines)
                    }
                }
            }
            AskWidgetMode.Stacked -> Column(container) {
                Mark(look, spec.badge)
                Spacer(GlanceModifier.defaultWeight())
                Text(title, style = titleStyle, maxLines = spec.titleMaxLines)
                if (spec.showSubtitle) {
                    Spacer(GlanceModifier.height(Tokens.Space.xs))
                    Text(subtitle, style = look.caption, maxLines = spec.subtitleMaxLines)
                }
            }
        }
    }

    @Composable
    private fun Mark(look: WidgetLook, badge: AskWidgetBadge) {
        when (badge) {
            AskWidgetBadge.MarkOnly -> Image(
                ImageProvider(persona.core.designsystem.R.drawable.ic_stat),
                contentDescription = null,
                modifier = GlanceModifier.size(look.markSize),
                colorFilter = ColorFilter.tint(look.accent),
            )
            AskWidgetBadge.Compact -> BadgeMark(look, look.compactBadgeSize, look.compactMarkSize)
            AskWidgetBadge.Standard -> BadgeMark(look, look.badgeSize, look.badgeMarkSize)
        }
    }

    @Composable
    private fun BadgeMark(look: WidgetLook, badgeSize: Dp, markSize: Dp) {
        Box(
            modifier = GlanceModifier.size(badgeSize).cornerRadius(badgeSize / 2).background(look.badge),
            contentAlignment = Alignment.Center,
        ) {
            Image(
                ImageProvider(persona.core.designsystem.R.drawable.ic_stat),
                contentDescription = null,
                modifier = GlanceModifier.size(markSize),
                colorFilter = ColorFilter.tint(look.accent),
            )
        }
    }

    companion object {
        /** Redraws every placed Ask Persona widget (cheap when none is placed). */
        suspend fun refresh(context: Context) {
            WidgetLook.invalidate()
            val _ = runCatching { AskPersonaWidget().updateAll(context.applicationContext) }
        }
    }
}

internal data class AskWidgetTarget(
    val isMainChat: Boolean = true,
    val sessionKey: persona.core.contracts.SessionKey? = null,
    val title: String? = null,
    val assistant: Boolean? = true,
    val bindingMissing: Boolean = false,
) {
    companion object {
        fun resolve(
            paired: Boolean,
            binding: ConversationWidgetBinding?,
            assistant: persona.core.contracts.api.AssistantSnapshot,
            sessions: List<persona.core.contracts.Session>,
        ): AskWidgetTarget {
            if (!paired || binding == null || binding.isMain) return AskWidgetTarget(isMainChat = true)
            val key = binding.sessionKey ?: return AskWidgetTarget(isMainChat = false, bindingMissing = true)
            val liveSession = sessions.firstOrNull { it.key == key }
                ?: assistant.conversations.firstOrNull { it.key == key }
            val direction = assistant.directionTree?.leaders?.firstOrNull { dir ->
                dir.id == key.sessionId || dir.external?.target == key
            }
            val inventoryLoaded = sessions.isNotEmpty() || assistant.conversations.isNotEmpty() || assistant.directionTree != null
            val missing = (inventoryLoaded && liveSession == null && direction == null) || liveSession?.archived == true
            val title = direction?.name?.takeIf(String::isNotBlank)
                ?: assistant.directionNames[key.sessionId]?.takeIf(String::isNotBlank)
                ?: liveSession?.displayTitle?.takeIf(String::isNotBlank)
                ?: binding.titleSnapshot.takeIf(String::isNotBlank)
            val isAssistant = liveSession?.assistant ?: when {
                direction?.external?.target == key -> false
                direction != null || binding.kind == ConversationWidgetKind.Leader || binding.kind == ConversationWidgetKind.Assistant -> true
                binding.kind == ConversationWidgetKind.Agent -> false
                else -> null
            }
            return AskWidgetTarget(
                isMainChat = false,
                sessionKey = key,
                title = title,
                assistant = isAssistant,
                bindingMissing = missing,
            )
        }
    }
}

class AskPersonaWidgetReceiver : GlanceAppWidgetReceiver() {
    override val glanceAppWidget: GlanceAppWidget = AskPersonaWidget()

    override fun onDeleted(context: Context, appWidgetIds: IntArray) {
        ConversationWidgetPrefs.delete(context, appWidgetIds)
        super.onDeleted(context, appWidgetIds)
    }

    override fun onRestored(context: Context, oldWidgetIds: IntArray?, newWidgetIds: IntArray?) {
        ConversationWidgetPrefs.restore(context, oldWidgetIds, newWidgetIds)
        super.onRestored(context, oldWidgetIds, newWidgetIds)
    }
}

internal enum class AskWidgetMode { Row, Stacked }

internal enum class AskWidgetBadge { MarkOnly, Compact, Standard }

internal enum class AskWidgetTitleScale { Compact, Section, Hero }

internal enum class AskWidgetPadding { Normal, Tight, CompactVertical, FlushVertical }

internal data class AskWidgetSpec(
    val mode: AskWidgetMode,
    val badge: AskWidgetBadge,
    val titleScale: AskWidgetTitleScale,
    val padding: AskWidgetPadding,
    val showSubtitle: Boolean,
    val titleMaxLines: Int,
    val subtitleMaxLines: Int,
) {
    /** True whenever outer padding was reduced below the standard 12dp inset. */
    val tightPadding: Boolean get() = padding != AskWidgetPadding.Normal
}

/**
 * Pure layout sizing for [AskPersonaWidget] across common launcher cells (2 × 1, 2 × 2, 4 × 1, 4 × 2) and system font
 * scales: 1-row sizes lay out horizontally, 2-row and taller sizes stack the mark above bottom-anchored copy, and
 * larger text reduces padding, steps down title scale, drops the subtitle, or limits the title to one line before
 * anything clips.
 */
internal object AskWidgetLayout {
    fun resolve(widthDp: Float, heightDp: Float, fontScale: Float = 1f, paired: Boolean = true): AskWidgetSpec {
        val scale = fontScale.coerceAtLeast(MIN_FONT_SCALE)
        val minTitleH = COMPACT_LINE * scale
        val padding = when {
            heightDp >= TIGHT_HEIGHT && heightDp - NORMAL_PADDING >= minTitleH -> AskWidgetPadding.Normal
            heightDp - TIGHT_PADDING >= minTitleH -> AskWidgetPadding.Tight
            heightDp - COMPACT_VERTICAL_PADDING >= minTitleH -> AskWidgetPadding.CompactVertical
            else -> AskWidgetPadding.FlushVertical
        }
        val hPad = horizontalPaddingDp(padding)
        val vPad = verticalPaddingDp(padding)
        val innerW = (widthDp - hPad).coerceAtLeast(0f)
        val innerH = (heightDp - vPad).coerceAtLeast(0f)
        val wide = widthDp >= WIDE_MIN_WIDTH
        val stacked = heightDp >= STACKED_MIN_HEIGHT && innerH >= MARK_SIZE + STACKED_GAP + SECTION_LINE * scale
        if (stacked) {
            val titleScale = when {
                wide && scale <= HERO_MAX_FONT_SCALE && innerH >= MARK_SIZE + STACKED_GAP + HERO_LINE * scale -> AskWidgetTitleScale.Hero
                innerW >= NARROW_SECTION_MIN_W && innerH >= MARK_SIZE + STACKED_GAP + SECTION_LINE * scale -> AskWidgetTitleScale.Section
                else -> AskWidgetTitleScale.Compact
            }
            val titleH = titleHeight(titleScale, scale)
            val badge = when {
                innerH >= STANDARD_BADGE + STACKED_GAP + titleH -> AskWidgetBadge.Standard
                innerH >= COMPACT_BADGE + STACKED_GAP + titleH -> AskWidgetBadge.Compact
                else -> AskWidgetBadge.MarkOnly
            }
            val textRoom = (innerH - badgeHeight(badge) - STACKED_GAP).coerceAtLeast(0f)
            val captionH = CAPTION_LINE * scale
            val subtitleLines = when {
                !paired && textRoom >= titleH + STACKED_TEXT_GAP + captionH * 2f -> 2
                textRoom >= titleH + STACKED_TEXT_GAP + captionH -> 1
                else -> 0
            }
            val titleMaxLines = if (subtitleLines == 0 && !wide && textRoom >= titleH * 2f) 2 else 1
            return AskWidgetSpec(
                mode = AskWidgetMode.Stacked,
                badge = badge,
                titleScale = titleScale,
                padding = padding,
                showSubtitle = subtitleLines > 0,
                titleMaxLines = titleMaxLines,
                subtitleMaxLines = subtitleLines.coerceAtLeast(1),
            )
        }
        val titleScale = if (wide && innerH >= SECTION_LINE * scale) AskWidgetTitleScale.Section else AskWidgetTitleScale.Compact
        val titleH = titleHeight(titleScale, scale)
        val badge = when {
            wide && innerH >= STANDARD_BADGE -> AskWidgetBadge.Standard
            innerH >= COMPACT_BADGE && innerW >= COMPACT_ROW_BADGE_MIN_W -> AskWidgetBadge.Compact
            else -> AskWidgetBadge.MarkOnly
        }
        val captionH = CAPTION_LINE * scale
        val canFitSubtitle = innerH >= titleH + ROW_TEXT_GAP + captionH &&
            (wide || (heightDp >= NARROW_ROW_SUBTITLE_MIN_H && innerW >= NARROW_ROW_SUBTITLE_MIN_W))
        val subtitleLines = when {
            !canFitSubtitle -> 0
            !paired && innerH >= titleH + ROW_TEXT_GAP + captionH * 2f -> 2
            else -> 1
        }
        val titleMaxLines = if (subtitleLines == 0 && !wide && innerH >= titleH * 2f) 2 else 1
        return AskWidgetSpec(
            mode = AskWidgetMode.Row,
            badge = badge,
            titleScale = titleScale,
            padding = padding,
            showSubtitle = subtitleLines > 0,
            titleMaxLines = titleMaxLines,
            subtitleMaxLines = subtitleLines.coerceAtLeast(1),
        )
    }

    internal fun horizontalPaddingDp(padding: AskWidgetPadding): Float = when (padding) {
        AskWidgetPadding.Normal -> NORMAL_PADDING
        AskWidgetPadding.Tight, AskWidgetPadding.CompactVertical, AskWidgetPadding.FlushVertical -> TIGHT_PADDING
    }

    internal fun verticalPaddingDp(padding: AskWidgetPadding): Float = when (padding) {
        AskWidgetPadding.Normal -> NORMAL_PADDING
        AskWidgetPadding.Tight -> TIGHT_PADDING
        AskWidgetPadding.CompactVertical -> COMPACT_VERTICAL_PADDING
        AskWidgetPadding.FlushVertical -> 0f
    }

    internal fun titleHeight(scale: AskWidgetTitleScale, fontScale: Float): Float = when (scale) {
        AskWidgetTitleScale.Hero -> HERO_LINE * fontScale
        AskWidgetTitleScale.Section -> SECTION_LINE * fontScale
        AskWidgetTitleScale.Compact -> COMPACT_LINE * fontScale
    }

    internal fun captionHeight(fontScale: Float): Float = CAPTION_LINE * fontScale

    internal fun badgeHeight(badge: AskWidgetBadge): Float = when (badge) {
        AskWidgetBadge.Standard -> STANDARD_BADGE
        AskWidgetBadge.Compact -> COMPACT_BADGE
        AskWidgetBadge.MarkOnly -> MARK_SIZE
    }

    internal fun requiredHeightDp(spec: AskWidgetSpec, fontScale: Float): Float {
        val scale = fontScale.coerceAtLeast(MIN_FONT_SCALE)
        val titleH = titleHeight(spec.titleScale, scale) * spec.titleMaxLines
        val subtitleH = if (spec.showSubtitle) captionHeight(scale) * spec.subtitleMaxLines else 0f
        val vPad = verticalPaddingDp(spec.padding)
        return when (spec.mode) {
            AskWidgetMode.Row -> {
                val textH = titleH + if (spec.showSubtitle) ROW_TEXT_GAP + subtitleH else 0f
                vPad + maxOf(badgeHeight(spec.badge), textH)
            }
            AskWidgetMode.Stacked -> {
                val textH = titleH + if (spec.showSubtitle) STACKED_TEXT_GAP + subtitleH else 0f
                vPad + badgeHeight(spec.badge) + STACKED_GAP + textH
            }
        }
    }

    private const val MIN_FONT_SCALE = 0.5f
    private const val TIGHT_HEIGHT = 64f
    private const val COMPACT_VERTICAL_PADDING = 8f
    private const val TIGHT_PADDING = 16f
    private const val NORMAL_PADDING = 24f
    private const val WIDE_MIN_WIDTH = 220f
    private const val STACKED_MIN_HEIGHT = 120f
    private const val STACKED_GAP = 8f
    private const val STACKED_TEXT_GAP = 4f
    private const val ROW_TEXT_GAP = 2f
    private const val STANDARD_BADGE = 40f
    private const val COMPACT_BADGE = 28f
    private const val MARK_SIZE = 16f
    private const val HERO_LINE = 28f
    private const val SECTION_LINE = 23f
    private const val COMPACT_LINE = 20f
    private const val CAPTION_LINE = 18f
    private const val HERO_MAX_FONT_SCALE = 1.15f
    private const val NARROW_SECTION_MIN_W = 110f
    private const val COMPACT_ROW_BADGE_MIN_W = 116f
    private const val NARROW_ROW_SUBTITLE_MIN_H = 76f
    private const val NARROW_ROW_SUBTITLE_MIN_W = 120f
}
