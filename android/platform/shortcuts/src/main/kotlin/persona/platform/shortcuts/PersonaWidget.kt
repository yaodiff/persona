package persona.platform.shortcuts

import persona.core.designsystem.resolve
import android.content.Context
import android.os.Build
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.compositeOver
import androidx.compose.ui.text.font.FontWeight as ComposeWeight
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
import androidx.glance.color.ColorProvider
import androidx.glance.layout.Alignment
import androidx.glance.layout.Box
import androidx.glance.layout.Column
import androidx.glance.layout.Row
import androidx.glance.layout.Spacer
import androidx.glance.layout.fillMaxSize
import androidx.glance.layout.fillMaxWidth
import androidx.glance.layout.height
import androidx.glance.layout.padding
import androidx.glance.layout.size
import androidx.glance.layout.width
import androidx.glance.text.FontWeight
import androidx.glance.text.Text
import androidx.glance.text.TextStyle
import androidx.glance.unit.ColorProvider as GlanceColor
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.combine
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.flow.drop
import kotlinx.coroutines.flow.update
import persona.core.contracts.FleetItem
import persona.core.contracts.FleetState
import persona.core.contracts.api.PersonaInfo
import persona.core.contracts.api.PersonaManifest
import persona.core.designsystem.AppStyle
import persona.core.designsystem.AppStyleStore
import persona.core.designsystem.PersonaPalette
import persona.core.designsystem.PersonaStore
import persona.core.designsystem.StatusColors
import persona.core.designsystem.SystemSurfaces
import persona.core.designsystem.ThemeAccent
import persona.core.designsystem.ThemeColor
import persona.core.designsystem.ThemeColors
import persona.core.designsystem.Tokens
import persona.core.domain.fleet.FleetSummary
import persona.core.domain.fleet.FleetText

/**
 * The Agents home-screen widget (iOS Agents widget, the Fleet activity's lock-screen view): how many agents need you,
 * are working or finished, and the first few of them. It reads [persona.platform.notify.FleetFeed], whose last state
 * is on disk, so it draws while the process was not running. Tapping an agent opens it, anywhere else opens Agents;
 * nothing here answers. Colours and type come from the design system: the user's interface style, light or dark with
 * the system. Kept under its original class and receiver name so already-placed widgets upgrade seamlessly.
 */
class PersonaWidget : GlanceAppWidget() {
    /**
     * Laid out for the size the launcher gives it (each orientation), so the rows follow the real height at any width.
     * Fixed responsive buckets did not: the taller ones were all 250dp wide, and a narrower widget (2 × 2 on a phone)
     * always got the smallest layout, the summary alone however tall it was.
     */
    override val sizeMode: SizeMode = SizeMode.Exact

    override suspend fun provideGlance(context: Context, id: GlanceId) {
        val dependencies = SurfacesDependencies.of(context)
        val feed = dependencies.fleet()
        feed.settled()
        val sessions = dependencies.sessions()
        // Read inside the composition: while Glance keeps this session, updateAll() only recomposes it.
        provideContent {
            val fleet by feed.state.collectAsState()
            val paired by feed.paired.collectAsState()
            val revision by WidgetLook.revision.collectAsState()
            val look = remember(revision) { WidgetLook.current(context) }
            Content(context, look, paired, fleet) { key -> sessions.assistant(key) }
        }
    }

    @Composable
    private fun Content(context: Context, look: WidgetLook, paired: Boolean, fleet: FleetState,
                        assistant: (persona.core.contracts.SessionKey) -> Boolean?) {
        val height = LocalSize.current.height.value
        val scale = context.resources.configuration.fontScale
        val rows = WidgetLayout.rows(height, scale)
        Column(
            GlanceModifier.fillMaxSize().appWidgetBackground().widgetCornerRadius()
                .background(look.card).padding(Tokens.Space.m)
                .clickable(actionStartActivity(SurfaceLinks.fleet(context, paired, fleet, assistant))),
        ) {
            // Unpaired, the line under the headline may take two lines.
            if (WidgetLayout.mark(height, scale, captionLines = if (paired) 1 else 2)) {
                Row(GlanceModifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
                    Image(ImageProvider(persona.core.designsystem.R.drawable.ic_stat), contentDescription = null,
                        modifier = GlanceModifier.size(look.markSize), colorFilter = ColorFilter.tint(look.accent))
                    Spacer(GlanceModifier.width(Tokens.Space.xs))
                    Text(context.getString(R.string.shortcuts_widget_label), style = look.caption, maxLines = 1)
                }
                Spacer(GlanceModifier.height(Tokens.Space.xs))
            }
            if (!paired) {
                Text(context.getString(R.string.shortcuts_not_paired_short), style = look.headline, maxLines = 1)
                Text(context.getString(R.string.shortcuts_widget_scan), style = look.caption, maxLines = 2)
                return@Column
            }
            Row(verticalAlignment = Alignment.CenterVertically) {
                if (!FleetSummary.isEmpty(fleet)) {
                    Dot(look.phase(FleetSummary.phase(fleet)), look.dotSize)
                    Spacer(GlanceModifier.width(Tokens.Space.s))
                }
                Text(FleetText.headline(fleet).resolve(context), style = look.headline, maxLines = 1)
            }
            val rest = FleetText.rest(fleet).resolve(context)
            if (rest.isNotEmpty()) Text(rest, style = look.caption, maxLines = 1)
            if (FleetSummary.isEmpty(fleet)) {
                Text(context.getString(R.string.shortcuts_widget_nobody), style = look.caption, maxLines = 1)
                return@Column
            }
            val shown = fleet.items.take(rows)
            if (shown.isEmpty()) return@Column
            Spacer(GlanceModifier.height(Tokens.Space.xs))
            shown.forEach { Item(context, look, it, assistant) }
            val hidden = FleetSummary.hidden(fleet, shown.size)
            if (hidden > 0) Text(FleetText.more(hidden).resolve(context), style = look.micro, maxLines = 1)
        }
    }

    @Composable
    private fun Item(context: Context, look: WidgetLook, item: FleetItem, assistant: (persona.core.contracts.SessionKey) -> Boolean?) {
        val open = SurfaceLinks.row(context, item, assistant)
        val modifier = GlanceModifier.fillMaxWidth().padding(vertical = Tokens.Space.xs).let { base ->
            if (open != null) base.clickable(actionStartActivity(open)) else base
        }
        Row(modifier, verticalAlignment = Alignment.CenterVertically) {
            Dot(look.kind(item.kind), look.smallDotSize)
            Spacer(GlanceModifier.width(Tokens.Space.s))
            Column(GlanceModifier.defaultWeight()) {
                Text(item.title, style = look.itemTitle, maxLines = 1)
                // What it is doing, then which computer it runs on ("Working · MacBook · weather").
                Text(listOf(FleetText.line(item).resolve(context), item.place).filter { it.isNotEmpty() }.joinToString(" · "),
                    style = look.micro, maxLines = 1)
            }
        }
    }

    @Composable
    private fun Dot(color: GlanceColor, size: androidx.compose.ui.unit.Dp) {
        Box(GlanceModifier.size(size).cornerRadius(size / 2).background(color)) {}
    }

    companion object {
        /** Redraws every placed widget (cheap when none is placed). */
        suspend fun refresh(context: Context) {
            WidgetLook.invalidate()
            val _ = runCatching { PersonaWidget().updateAll(context.applicationContext) }
        }

        /**
         * Emits whenever widget appearance changes: interface style, theme color, or (when [ThemeColor.Persona] is
         * selected) the active persona package's colours. Skips the initial snapshot so process start does not redraw.
         */
        fun appearanceChanges(
            style: Flow<AppStyle>,
            color: Flow<ThemeColor>,
            persona: Flow<PersonaInfo?>,
        ): Flow<Pair<AppStyle, ThemeAccent?>> =
            combine(style, color, persona) { nextStyle, nextColor, pack ->
                nextStyle to ThemeColors.accent(nextColor, pack?.manifest?.colors)
            }.distinctUntilChanged().drop(1)
    }
}

class PersonaWidgetReceiver : GlanceAppWidgetReceiver() {
    override val glanceAppWidget: GlanceAppWidget = PersonaWidget()
}

/** System widget corner radius on Android 12+, falling back to the design system's panel radius on Android 11. */
internal fun GlanceModifier.widgetCornerRadius(): GlanceModifier =
    if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) cornerRadius(android.R.dimen.system_app_widget_background_radius)
    else cornerRadius(Tokens.Radius.Standard.panel)

/**
 * What fits in the widget's height, measured on the launcher at the system's default font size: the padding and spacers
 * take 32dp, the mark, headline and other counts about 64dp of text (a caption line about 19dp of it), each agent 8dp of
 * padding and about 37dp of text, "+N more" about 17dp. Text grows with the system font size; whatever does not fit would
 * be cut off at the bottom.
 */
internal object WidgetLayout {
    /** How many agents to list under the summary, room left for "+N more": none on the smallest widget, at most three. */
    fun rows(heightDp: Float, fontScale: Float = 1f): Int =
        ((heightDp - summary(fontScale) - MORE * fontScale) / (ROW_PADDING + ROW * fontScale)).toInt().coerceIn(0, 3)

    /**
     * Whether the widget header fits above the headline and the [captionLines] under it; a widget squeezed below that
     * keeps those and drops the header.
     */
    fun mark(heightDp: Float, fontScale: Float = 1f, captionLines: Int = 1): Boolean =
        heightDp >= summary(fontScale) + (captionLines - 1) * CAPTION * fontScale

    private fun summary(fontScale: Float) = PADDING + SUMMARY * fontScale

    private const val PADDING = 32f
    private const val SUMMARY = 64f
    private const val ROW_PADDING = 8f
    private const val ROW = 37f
    private const val MORE = 17f
    private const val CAPTION = 19f
}

/** The design system's tokens for one style, as Glance's day / night colour pairs and text styles. */
internal class WidgetLook private constructor(
    internal val lightPalette: PersonaPalette,
    internal val darkPalette: PersonaPalette,
    val style: AppStyle,
) {
    private fun pair(pick: (PersonaPalette) -> Color) = ColorProvider(day = pick(lightPalette), night = pick(darkPalette))
    private fun status(pick: (PersonaPalette) -> StatusColors) = pair { pick(it).dot }
    private val type = SystemSurfaces.type(style)

    val card = pair { it.surfaceOpaque }
    val accent = pair { it.accent }
    /** Soft accent well flattened onto [card], so Glance renders identical tints across API levels and launchers. */
    val badge = pair { it.accentSoft.compositeOver(it.surfaceOpaque) }
    private val ink = pair { it.ink }
    private val ink2 = pair { it.ink2 }
    private val needsYou = status { it.needsYou }
    private val working = status { it.working }
    private val done = status { it.done }

    val headline = text(type.title, ink)
    val sectionTitle = text(type.headline, ink)
    val itemTitle = text(type.callout.copy(fontWeight = ComposeWeight.Medium), ink)
    val caption = text(type.caption, ink2)
    val micro = text(type.micro, ink2)
    val markSize = Tokens.Space.Standard.l
    val badgeSize = Tokens.Size.slimBar
    val badgeMarkSize = Tokens.Size.icon
    val compactBadgeSize = Tokens.Size.wellXS
    val compactMarkSize = Tokens.Space.Standard.l
    val dotSize = Tokens.Space.Standard.s + Tokens.Space.Standard.xxs
    val smallDotSize = Tokens.Space.Standard.s

    fun phase(phase: String): GlanceColor = when (phase) {
        FleetSummary.NEEDS_YOU -> needsYou
        FleetSummary.WORKING -> working
        else -> done
    }

    fun kind(kind: String): GlanceColor = when (kind) {
        "working" -> working
        "unread" -> done
        else -> needsYou
    }

    private fun text(style: androidx.compose.ui.text.TextStyle, color: GlanceColor) = TextStyle(
        color = color, fontSize = style.fontSize,
        fontWeight = when {
            (style.fontWeight?.weight ?: 400) >= 600 -> FontWeight.Bold
            (style.fontWeight?.weight ?: 400) >= 500 -> FontWeight.Medium
            else -> FontWeight.Normal
        },
    )

    companion object {
        private val changes = MutableStateFlow(0)
        val revision: StateFlow<Int> = changes.asStateFlow()

        /** Signals active Glance compositions to re-read [AppStyleStore] on refresh. */
        fun invalidate() {
            changes.update { it + 1 }
        }

        fun current(context: Context): WidgetLook = AppStyleStore(context).let { store ->
            val personaColors = if (store.color.value == ThemeColor.Persona) {
                PersonaStore.currentColors(context, store.personaId.value)
            } else null
            of(store.style.value, store.color.value, personaColors)
        }

        fun of(
            style: AppStyle,
            color: ThemeColor = ThemeColor.Style,
            personaColors: PersonaManifest.Colors? = null,
        ): WidgetLook {
            val accent = ThemeColors.accent(color, personaColors)
            val light = SystemSurfaces.palette(style, false).let { base -> accent?.let { ThemeColors.palette(base, it) } ?: base }
            val dark = SystemSurfaces.palette(style, true).let { base -> accent?.let { ThemeColors.palette(base, it) } ?: base }
            return WidgetLook(light, dark, style)
        }
    }
}
