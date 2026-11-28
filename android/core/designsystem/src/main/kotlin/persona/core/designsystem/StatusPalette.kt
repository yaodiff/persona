package persona.core.designsystem

import android.content.Context
import androidx.annotation.StringRes
import androidx.compose.runtime.Composable
import androidx.compose.runtime.Immutable
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.lerp

/** One status, three uses: the dot or holder, text in that colour, and a soft tint behind it. */
@Immutable
data class StatusColors(val dot: Color, val text: Color, val tint: Color, val onDot: Color = Color.White)

/**
 * Semantic colours of one style in one mode. Components read these; pages should not pick raw colours.
 * [surface] may be translucent (glass); [surfaceOpaque] is the same surface without transparency, for
 * places that cannot show what is behind them (Material dialogs, the solid glass fallback).
 */
@Immutable
data class PersonaPalette(
    val background: Color,
    val surface: Color,
    val surfaceOpaque: Color,
    val surfaceStrong: Color,
    val sunken: Color,
    val line: Color,
    val strokeTop: Color,
    val strokeBottom: Color,
    val ink: Color,
    val ink2: Color,
    val ink3: Color,
    val action: Color,
    val onAction: Color,
    val accent: Color,
    val accentSoft: Color,
    val marker: Color,
    val onMarker: Color,
    val needsYou: StatusColors,
    val working: StatusColors,
    val done: StatusColors,
    val idle: StatusColors,
    val offline: StatusColors,
    val danger: StatusColors,
    val positive: Color,
    val caution: Color,
    val cautionText: Color,
    val diffAdd: Color,
    val diffDel: Color,
    val terminalBackground: Color,
    val terminalText: Color,
    val scrim: Color,
    val dark: Boolean,
    val ai: Color = accent,
    /**
     * An added / removed line's tint behind its code, and the words that changed in it, deeper still (iOS `Theme.Diff.addFill` /
     * `addEmphasis`, `delFill` / `delEmphasis`: `Tokens.Common.<Light|Dark>.Diff`). Glass takes the tokens' own colours; Brief and
     * Board keep their own diff hue at the tokens' strength ([DiffTintAlpha]).
     */
    val diffAddFill: Color = diffAdd.copy(alpha = DiffTintAlpha.addFill),
    val diffDelFill: Color = diffDel.copy(alpha = DiffTintAlpha.delFill),
    val diffAddEmphasis: Color = diffAdd.copy(alpha = DiffTintAlpha.addEmphasis),
    val diffDelEmphasis: Color = diffDel.copy(alpha = DiffTintAlpha.delEmphasis),
    val positiveRole: StatusColors? = null,
    val cautionRole: StatusColors? = null,
    val customRoles: Boolean = false,
    /** The user's own messages: iOS Liquid Glass's pale blue (ColorSpec.blue), the same in every style (#2643). */
    val userBubble: Color = if (dark) Tokens.Semantic.Glass.Dark.userBubble else Tokens.Semantic.Glass.Light.userBubble,
    /** The words on [userBubble] when it is a deep colour of its own (a theme color like Coral orange); null: the page's ink. */
    val userBubbleText: Color? = null,
    /** Where an agent was opened (#3132, iOS `Theme.sourceFeishu` / `sourceTerminal`): a Feishu group's topic, a terminal pane. */
    val sourceFeishu: StatusColors = if (dark) StatusColors(Color(0xFF82AAFF), Color(0xFF82AAFF), Color(0x2982AAFF))
        else StatusColors(Color(0xFF1D5FD6), Color(0xFF1C52BA), Color(0x1A1D5FD6)),
    val sourceTerminal: StatusColors = if (dark) StatusColors(Color(0xFFBEA3FF), Color(0xFFBEA3FF), Color(0x29BEA3FF))
        else StatusColors(Color(0xFF6A46C2), Color(0xFF6A46C2), Color(0x1A6A46C2)),
    /**
     * Waiting in the background (#4338 A5, A11; iOS `StatusPalette.backgroundWaiting`): an agent that works but only waits on
     * background commands. Glass draws it lighter than Working (the generated `Tokens.Status.*.BackgroundWaiting`: Working's
     * dot at 0.6, its words the secondary gray); Brief and Board keep Working's colors.
     */
    val backgroundWaiting: StatusColors = working,
    /** A fixed colour for [codeString] / [codeNumber] in place of the role (only the default look keeps its original ones). */
    val codeStringOverride: Color? = null,
    val codeNumberOverride: Color? = null,
    /** Words on the accent are white, as on iOS (Glass); otherwise whichever of black and white reads better. */
    val whiteOnAccent: Boolean = false,
) {
    /** The reply's inline code ink, at the same step toward secondary text as on the web. */
    val replyCodeInk: Color get() = lerp(ink, ink2, 0.45f)

    /** Strings in a Lark code block (#934): the style's added-line green, so it follows light / dark and the style. */
    val codeString: Color get() = codeStringOverride ?: diffAdd
    /** Numbers in a Lark code block (#934): the style's caution text, so it follows light / dark, the style and theme packages. */
    val codeNumber: Color get() = codeNumberOverride ?: cautionText

    /** Neutral backing for state, advice and outcome content. Color belongs to its mark or words, not its area. */
    val statusSurface: Color get() = surfaceOpaque

    /**
     * A soft wash of [ai] for where the AI is now (a workflow's current phase, #3435), as soft as a theme color's
     * [accentSoft]: shows on a card in every style, where [statusSurface] is the card's own color in some.
     */
    val aiSoft: Color get() = ai.copy(alpha = if (dark) 0.16f else 0.12f)

    /** Media overlays stay readable over a photograph/video, independent of the page's appearance. */
    val mediaBackdrop: Color get() = Color.Black
    val onMediaBackdrop: Color get() = Color.White
    /** Transparent third-party logos keep their brand colors on a neutral light backing in either appearance. */
    val catalogLogoBackground: Color get() = Tokens.System.Light.page
    /** Outside the circle an avatar crop keeps (New from photo, #2407): dimmed in every style and appearance, as iOS `Theme.Crop.outside`. */
    val cropOutside: Color get() = mediaBackdrop.copy(alpha = 0.5f)
    /** The crop circle's edge, as iOS `Theme.Crop.edge`. */
    val cropEdge: Color get() = onMediaBackdrop.copy(alpha = 0.9f)
    val mediaScrim: Color get() = mediaBackdrop.copy(alpha = 0.85f)
    val mediaControlFill: Color get() = mediaBackdrop.copy(alpha = 0.6f)
    val mediaLabelFill: Color get() = mediaBackdrop.copy(alpha = 0.7f)
    val onAccent: Color get() = if (whiteOnAccent) Color.White else ExtensionThemeColors.on(accent)
    /** Words on a fill that stands for the accent (the compact sidebar's): white in Glass, as on iOS, else whichever reads. */
    fun onAccentFill(fill: Color): Color = if (whiteOnAccent) onAccent else ExtensionThemeColors.on(fill)

    fun source(kind: SourceKind): StatusColors = when (kind) {
        SourceKind.Feishu -> sourceFeishu
        SourceKind.Terminal -> sourceTerminal
        // an app's own chat (an Orca chat, #17054): the neutral idle tint, the colour of a quiet row's marks
        SourceKind.Desktop -> idle
    }

    fun status(status: Status): StatusColors = when (status) {
        Status.NeedsYou -> needsYou
        Status.Working -> working
        Status.Unread -> done
        Status.Idle -> idle
        Status.Offline -> offline
        Status.Unknown, Status.Muted -> idle
        Status.Waiting -> working.copy(dot = working.dot.copy(alpha = 0.55f))
    }

    /** An agent's colors (iOS `Session.stateTone`): [backgroundWaiting] for a working one that [waitsInBackground], else [status]'s. */
    fun status(status: Status, waitsInBackground: Boolean): StatusColors =
        if (waitsInBackground && status == Status.Working) backgroundWaiting else status(status)
}

internal object Palettes {
    /**
     * Glass keeps the iPhone's colours as they are (#4209, decision 2 of #4194): iOS holds them to its own floors (white on
     * the accent 3.5:1, links 3:1, words on the bubble 4.5:1), not 4.5:1 for every word. Only the words on a state's fill
     * are chosen here: white on Needs You's blue, as on the accent, else whichever of black and white reads better.
     */
    private fun PersonaPalette.glassFills(): PersonaPalette {
        fun StatusColors.onFill() = copy(onDot = ExtensionThemeColors.on(dot))
        return copy(needsYou = needsYou.copy(onDot = Color.White), working = working.onFill(), done = done.onFill(),
            idle = idle.onFill(), offline = offline.onFill(), danger = danger.onFill(),
            positiveRole = positiveRole?.onFill(), cautionRole = cautionRole?.onFill(), whiteOnAccent = true)
    }

    // Liquid Glass on the system's white/black pages. The translucent fill retains the Android glass fallback.
    // State dots and texts come from the generated iPhone StatusPalette; every state keeps the neutral tint (Offline's).
    val GlassDark = PersonaPalette(
        background = Tokens.Semantic.Glass.Dark.pageBackground,
        surface = Tokens.Semantic.Glass.Dark.card.copy(alpha = 0x14 / 255f),
        surfaceOpaque = Tokens.Semantic.Glass.Dark.card,
        surfaceStrong = Tokens.Common.Dark.Fill.pressed, sunken = Tokens.Common.Dark.Fill.well,
        line = Tokens.Semantic.Glass.Dark.cardStroke,
        strokeTop = Color(0x40FFFFFF), strokeBottom = Color(0x0DFFFFFF),
        ink = Tokens.Semantic.Glass.Dark.text,
        ink2 = Tokens.Semantic.Glass.Dark.secondaryText, ink3 = Tokens.Semantic.Glass.Dark.secondaryText,
        action = Tokens.Semantic.Glass.Dark.text, onAction = Tokens.Semantic.Glass.Dark.pageBackground,
        accent = Tokens.Semantic.Glass.Dark.accent, accentSoft = Tokens.Semantic.Glass.Dark.accent.copy(alpha = 0.16f),
        marker = Tokens.Semantic.Glass.Dark.accent.copy(alpha = 0.16f), onMarker = Tokens.Semantic.Glass.Dark.text,
        needsYou = StatusColors(Tokens.Status.Dark.NeedsYou.dot, Tokens.Status.Dark.NeedsYou.text, Tokens.Status.Dark.Offline.tint, Color.Black),
        working = StatusColors(Tokens.Status.Dark.Working.dot, Tokens.Status.Dark.Working.text, Tokens.Status.Dark.Offline.tint, Color.Black),
        backgroundWaiting = StatusColors(Tokens.Status.Dark.BackgroundWaiting.dot, Tokens.Status.Dark.BackgroundWaiting.text, Tokens.Status.Dark.BackgroundWaiting.tint),
        done = StatusColors(Tokens.Status.Dark.Unread.dot, Tokens.Status.Dark.Unread.text, Tokens.Status.Dark.Offline.tint, Color.Black),
        idle = StatusColors(Tokens.Status.Dark.Idle.dot, Tokens.Status.Dark.Idle.text, Tokens.Status.Dark.Idle.tint),
        offline = StatusColors(Tokens.Status.Dark.Offline.dot, Tokens.Status.Dark.Offline.text, Tokens.Status.Dark.Offline.tint),
        danger = StatusColors(Tokens.Common.Dark.danger, Tokens.Common.Dark.danger, Tokens.Common.Dark.danger.copy(alpha = 0.12f), Color.Black),
        positive = Tokens.Common.Dark.positive, caution = Tokens.Common.Dark.caution, cautionText = Tokens.Common.Dark.cautionText,
        diffAdd = Tokens.Common.Dark.Diff.add, diffDel = Tokens.Common.Dark.Diff.del,
        diffAddFill = Tokens.Common.Dark.Diff.addFill, diffDelFill = Tokens.Common.Dark.Diff.delFill,
        diffAddEmphasis = Tokens.Common.Dark.Diff.addEmphasis, diffDelEmphasis = Tokens.Common.Dark.Diff.delEmphasis,
        terminalBackground = Tokens.Common.Dark.terminalBackground, terminalText = Tokens.Common.Dark.terminalText,
        scrim = Color.Black.copy(alpha = 0.45f), dark = true, ai = Tokens.Common.Dark.ai,
        positiveRole = StatusColors(Tokens.Common.Dark.positive, Tokens.Common.Dark.positive, Tokens.Common.Dark.positive.copy(alpha = 0.12f), Color.Black),
        cautionRole = StatusColors(Tokens.Common.Dark.caution, Tokens.Common.Dark.cautionText, Tokens.Common.Dark.caution.copy(alpha = 0.12f), Color.Black),
    ).glassFills()
    val GlassLight = PersonaPalette(
        background = Tokens.Semantic.Glass.Light.pageBackground,
        surface = Tokens.Semantic.Glass.Light.card.copy(alpha = 0x9E / 255f),
        surfaceOpaque = Tokens.Semantic.Glass.Light.card,
        surfaceStrong = Tokens.Common.Light.Fill.chip, sunken = Tokens.Common.Light.Fill.well,
        line = Tokens.Semantic.Glass.Light.cardStroke,
        strokeTop = Color(0xFFFFFFFF), strokeBottom = Color(0x80FFFFFF),
        ink = Tokens.Semantic.Glass.Light.text,
        ink2 = Tokens.Semantic.Glass.Light.secondaryText, ink3 = Tokens.Semantic.Glass.Light.secondaryText,
        action = Tokens.Semantic.Glass.Light.text, onAction = Tokens.Semantic.Glass.Light.pageBackground,
        accent = Tokens.Semantic.Glass.Light.accent, accentSoft = Tokens.Semantic.Glass.Light.accent.copy(alpha = 0.12f),
        marker = Tokens.Semantic.Glass.Light.accent.copy(alpha = 0.12f), onMarker = Tokens.Semantic.Glass.Light.text,
        needsYou = StatusColors(Tokens.Status.Light.NeedsYou.dot, Tokens.Status.Light.NeedsYou.text, Tokens.Status.Light.Offline.tint, Color.Black),
        working = StatusColors(Tokens.Status.Light.Working.dot, Tokens.Status.Light.Working.text, Tokens.Status.Light.Offline.tint, Color.Black),
        backgroundWaiting = StatusColors(Tokens.Status.Light.BackgroundWaiting.dot, Tokens.Status.Light.BackgroundWaiting.text, Tokens.Status.Light.BackgroundWaiting.tint),
        done = StatusColors(Tokens.Status.Light.Unread.dot, Tokens.Status.Light.Unread.text, Tokens.Status.Light.Offline.tint, Color.Black),
        idle = StatusColors(Tokens.Status.Light.Idle.dot, Tokens.Status.Light.Idle.text, Tokens.Status.Light.Idle.tint),
        offline = StatusColors(Tokens.Status.Light.Offline.dot, Tokens.Status.Light.Offline.text, Tokens.Status.Light.Offline.tint),
        danger = StatusColors(Tokens.Common.Light.danger, Tokens.Common.Light.danger, Tokens.Common.Light.danger.copy(alpha = 0.12f)),
        positive = Tokens.Common.Light.positive, caution = Tokens.Common.Light.caution, cautionText = Tokens.Common.Light.cautionText,
        diffAdd = Tokens.Common.Light.Diff.add, diffDel = Tokens.Common.Light.Diff.del,
        diffAddFill = Tokens.Common.Light.Diff.addFill, diffDelFill = Tokens.Common.Light.Diff.delFill,
        diffAddEmphasis = Tokens.Common.Light.Diff.addEmphasis, diffDelEmphasis = Tokens.Common.Light.Diff.delEmphasis,
        terminalBackground = Tokens.Common.Light.terminalBackground, terminalText = Tokens.Common.Light.terminalText,
        scrim = Color.Black.copy(alpha = 0.2f), dark = false, ai = Tokens.Common.Light.ai,
        positiveRole = StatusColors(Tokens.Common.Light.positive, Tokens.Common.Light.positive, Tokens.Common.Light.positive.copy(alpha = 0.12f)),
        cautionRole = StatusColors(Tokens.Common.Light.caution, Tokens.Common.Light.cautionText, Tokens.Common.Light.caution.copy(alpha = 0.12f), Color.Black),
        // the default look keeps the Lark code block's original colours (#4228, #934)
        codeStringOverride = Color(0xFF2E7D32), codeNumberOverride = Color(0xFFE65100),
    ).glassFills()

    // Brief: paper and ink; the highlighter is the only colour and only marks advice and what needs you.
    val BriefLight = PersonaPalette(
        background = Color(0xFFFFFFFF), surface = Color(0xFFF4F5F7), surfaceOpaque = Color(0xFFF4F5F7),
        surfaceStrong = Color(0xFFE9ECF0), sunken = Color(0xFFF4F5F7), line = Color(0xFFE6E8EC),
        strokeTop = Color(0xFFE6E8EC), strokeBottom = Color(0xFFE6E8EC),
        ink = Color(0xFF141922), ink2 = Color(0xFF5C6573), ink3 = Color(0xFF5C6573),
        action = Color(0xFF141922), onAction = Color(0xFFFFFFFF), accent = Color(0xFF141922), accentSoft = Color(0xFFE9ECF0),
        marker = Color(0xFFFFE15A), onMarker = Color(0xFF141922),
        needsYou = StatusColors(Color(0xFFF2C200), Color(0xFF141922), Color(0xFFFFE15A), Color(0xFF141922)),
        working = StatusColors(Color(0xFF141922), Color(0xFF141922), Color(0xFFE9ECF0)),
        done = StatusColors(Color(0xFF1E8E5A), Color(0xFF157344), Color(0xFFE5F3EB), Color(0xFF030A06)),
        idle = StatusColors(Color(0xFFB2B8C2), Color(0xFF5C6573), Color(0xFFF4F5F7)),
        offline = StatusColors(Color(0xFFC7CCD4), Color(0xFF5C6573), Color(0xFFF4F5F7)),
        danger = StatusColors(Color(0xFFCC3A2E), Color(0xFFB8322A), Color(0xFFFBEAE8)),
        positive = Color(0xFF1E8E5A), caution = Color(0xFFE0A800), cautionText = Color(0xFF8A6400),
        diffAdd = Color(0xFF157344), diffDel = Color(0xFFB8322A),
        terminalBackground = Color(0xFF141922), terminalText = Color(0xFFE9E6DF), scrim = Color(0x66141922), dark = false,
        // paper and ink: a source tag is ink on a grey slip, like any other tag
        sourceFeishu = StatusColors(Color(0xFF5C6573), Color(0xFF5C6573), Color(0xFFE9ECF0)),
        sourceTerminal = StatusColors(Color(0xFF5C6573), Color(0xFF5C6573), Color(0xFFE9ECF0)),
    )
    val BriefDark = PersonaPalette(
        background = Color(0xFF0E1117), surface = Color(0xFF181C24), surfaceOpaque = Color(0xFF181C24),
        surfaceStrong = Color(0xFF242934), sunken = Color(0xFF0E1117), line = Color(0xFF232833),
        strokeTop = Color(0xFF232833), strokeBottom = Color(0xFF232833),
        ink = Color(0xFFECEFF4), ink2 = Color(0xFF9CA4B3), ink3 = Color(0xFF9CA4B3),
        action = Color(0xFFECEFF4), onAction = Color(0xFF0E1117), accent = Color(0xFFECEFF4), accentSoft = Color(0xFF242934),
        marker = Color(0xFFE9C846), onMarker = Color(0xFF15130A),
        needsYou = StatusColors(Color(0xFFE9C846), Color(0xFFF2D876), Color(0xFF3A3315), Color(0xFF15130A)),
        working = StatusColors(Color(0xFFECEFF4), Color(0xFFECEFF4), Color(0xFF242934), Color(0xFF0E1117)),
        done = StatusColors(Color(0xFF4CC38A), Color(0xFF6FD3A2), Color(0xFF15291F), Color(0xFF07170F)),
        idle = StatusColors(Color(0xFF4A5160), Color(0xFF9CA4B3), Color(0xFF181C24)),
        offline = StatusColors(Color(0xFF3A404C), Color(0xFF9CA4B3), Color(0xFF181C24)),
        danger = StatusColors(Color(0xFFFF6B5E), Color(0xFFFF8A80), Color(0xFF3A1A17), Color(0xFF2B0808)),
        positive = Color(0xFF4CC38A), caution = Color(0xFFE9C846), cautionText = Color(0xFFF2D876),
        diffAdd = Color(0xFF4CC38A), diffDel = Color(0xFFFF7A6E),
        terminalBackground = Color(0xFF07090D), terminalText = Color(0xFFE9E6DF), scrim = Color(0x99000000), dark = true,
        sourceFeishu = StatusColors(Color(0xFF9CA4B3), Color(0xFF9CA4B3), Color(0xFF242934)),
        sourceTerminal = StatusColors(Color(0xFF9CA4B3), Color(0xFF9CA4B3), Color(0xFF242934)),
    )

    // Board: a grey board with white strips; the strip holder colour is the state.
    val BoardLight = PersonaPalette(
        background = Color(0xFFE6E9ED), surface = Color(0xFFFFFFFF), surfaceOpaque = Color(0xFFFFFFFF),
        surfaceStrong = Color(0xFFF3F5F7), sunken = Color(0xFFEEF1F4), line = Color(0xFFD5DAE0),
        strokeTop = Color(0xFFD5DAE0), strokeBottom = Color(0xFFD5DAE0),
        ink = Color(0xFF0E141B), ink2 = Color(0xFF526070), ink3 = Color(0xFF526070),
        action = Color(0xFF0E141B), onAction = Color(0xFFFFFFFF), accent = Color(0xFF1D4FC4), accentSoft = Color(0xFFE6EEFF),
        marker = Color(0xFFE6EEFF), onMarker = Color(0xFF0E141B),
        needsYou = StatusColors(Color(0xFFF5A000), Color(0xFF8A5A00), Color(0xFFFFF3DA), Color(0xFF2B1D00)),
        working = StatusColors(Color(0xFF2563EB), Color(0xFF1D4FC4), Color(0xFFE6EEFF)),
        done = StatusColors(Color(0xFF15A34A), Color(0xFF0C682F), Color(0xFFE2F6E9), Color(0xFF04210F)),
        idle = StatusColors(Color(0xFFA2ACB9), Color(0xFF526070), Color(0xFFEEF1F4), Color(0xFF0E141B)),
        offline = StatusColors(Color(0xFFC4CBD4), Color(0xFF526070), Color(0xFFEEF1F4), Color(0xFF0E141B)),
        danger = StatusColors(Color(0xFFDC2626), Color(0xFFB91C1C), Color(0xFFFDE8E8)),
        positive = Color(0xFF15A34A), caution = Color(0xFFF5A000), cautionText = Color(0xFF8A5A00),
        diffAdd = Color(0xFF0C682F), diffDel = Color(0xFFB91C1C),
        terminalBackground = Color(0xFF0E141B), terminalText = Color(0xFFE6E3DA), scrim = Color(0x660E141B), dark = false,
    )
    val BoardDark = PersonaPalette(
        background = Color(0xFF0E1114), surface = Color(0xFF1A1E23), surfaceOpaque = Color(0xFF1A1E23),
        surfaceStrong = Color(0xFF22272E), sunken = Color(0xFF13161A), line = Color(0xFF2A3037),
        strokeTop = Color(0xFF2A3037), strokeBottom = Color(0xFF2A3037),
        ink = Color(0xFFE8ECF1), ink2 = Color(0xFF98A3B2), ink3 = Color(0xFF98A3B2),
        action = Color(0xFFE8ECF1), onAction = Color(0xFF0E1114), accent = Color(0xFF4C8DFF), accentSoft = Color(0xFF15284D),
        marker = Color(0xFF15284D), onMarker = Color(0xFFE8ECF1),
        needsYou = StatusColors(Color(0xFFFFB224), Color(0xFFFFC75E), Color(0xFF3A2B0C), Color(0xFF2B1D00)),
        working = StatusColors(Color(0xFF4C8DFF), Color(0xFF7FAEFF), Color(0xFF15284D), Color(0xFF091A35)),
        done = StatusColors(Color(0xFF35C46C), Color(0xFF5ED68C), Color(0xFF103020), Color(0xFF04210F)),
        idle = StatusColors(Color(0xFF5E6976), Color(0xFF98A3B2), Color(0xFF22272E)),
        offline = StatusColors(Color(0xFF454D58), Color(0xFF98A3B2), Color(0xFF1A1E23)),
        danger = StatusColors(Color(0xFFFF5C5C), Color(0xFFFF8585), Color(0xFF3A1717), Color(0xFF2B0808)),
        positive = Color(0xFF35C46C), caution = Color(0xFFFFB224), cautionText = Color(0xFFFFC75E),
        diffAdd = Color(0xFF35C46C), diffDel = Color(0xFFFF6B6B),
        terminalBackground = Color(0xFF07090B), terminalText = Color(0xFFE6E3DA), scrim = Color(0x99000000), dark = true,
    )

    fun of(style: AppStyle, dark: Boolean): PersonaPalette = when (style) {
        AppStyle.Glass -> if (dark) GlassDark else GlassLight
        AppStyle.Brief -> if (dark) BriefDark else BriefLight
        AppStyle.Board -> if (dark) BoardDark else BoardLight
    }
}

@Immutable
data class PersonaColors(
    val brand: Color,
    val danger: Color,
    val positive: Color,
    val caution: Color,
    val cautionText: Color,
    val ai: Color,
    val loop: Color,
    val countBadge: Color,
    val onCountBadge: Color,
    val codeBackground: Color,
    val terminalBackground: Color,
    val terminalText: Color,
)

/** The older colour set, now derived from the active style so existing call sites follow it. */
internal fun PersonaPalette.legacy() = PersonaColors(
    brand = accent, danger = danger.dot, positive = positive, caution = caution, cautionText = cautionText,
    ai = ai, loop = working.dot, countBadge = needsYou.dot, onCountBadge = needsYou.onDot,
    codeBackground = sunken, terminalBackground = terminalBackground, terminalText = terminalText,
)

/**
 * A role colour for something that is not an agent's state: good, needs a look, broken, or plain. Extensions' views
 * (docs/extensions/extensions.md "Declarative UI") name these four and never a colour.
 */
enum class Tone { Neutral, Positive, Caution, Danger }

/** Where an agent was opened, each on its own tint ([PersonaPalette.source]): a Feishu group's topic or a terminal pane. */
enum class SourceKind { Feishu, Terminal, Desktop }

/** [tone]'s colours: positive is the done green, caution the needs-you amber, danger red, neutral the idle grey. */
fun PersonaPalette.tone(tone: Tone): StatusColors = when (tone) {
    Tone.Positive -> positiveRole ?: done
    Tone.Caution -> cautionRole ?: needsYou
    Tone.Danger -> danger
    Tone.Neutral -> idle
}

/** An agent's state (docs/protocol/agent-states.md). Pages show [localizedLabel] (or [labelRes] outside composition). */
enum class Status {
    NeedsYou, Working, Unread, Idle, Offline, Unknown, Waiting, Muted;

    @get:StringRes val labelRes: Int get() = when (this) {
        NeedsYou -> R.string.designsystem_status_needs_you
        Working -> R.string.designsystem_status_working
        Unread -> R.string.designsystem_status_unread
        Idle -> R.string.designsystem_status_idle
        Offline -> R.string.designsystem_status_offline
        Unknown -> R.string.designsystem_status_unknown
        Waiting -> R.string.designsystem_status_waiting
        Muted -> R.string.designsystem_status_muted
    }

    /** The name in the page's language. */
    val localizedLabel: String @Composable get() = stringResource(labelRes)

    /** The name in [context]'s language, for notifications and other code outside composition. */
    fun label(context: Context): String = context.getString(labelRes)
}

@Immutable
data class StatusTone(val dot: Color, val text: Color, val tint: Color)

/** Status colours of the active style. Prefer [PersonaTheme.palette] in new code. */
fun statusTone(status: Status, darkTheme: Boolean): StatusTone =
    Palettes.of(ActiveStyle.value, darkTheme).status(status).let { StatusTone(it.dot, it.text, it.tint) }

/** A diff's words that changed in a line (UI version 5, #3136): the add / delete colour, deeper than the line's own tint. */
val PersonaPalette.diffAddWord: Color get() = diffAdd.copy(alpha = 0.30f)
val PersonaPalette.diffDelWord: Color get() = diffDel.copy(alpha = 0.30f)

/** The strength of a diff line's tint and of its changed words' (the tokens' alpha, the same in light and dark), for the styles with a diff hue of their own. */
internal object DiffTintAlpha {
    val addFill: Float get() = Tokens.Common.Light.Diff.addFill.alpha
    val delFill: Float get() = Tokens.Common.Light.Diff.delFill.alpha
    val addEmphasis: Float get() = Tokens.Common.Light.Diff.addEmphasis.alpha
    val delEmphasis: Float get() = Tokens.Common.Light.Diff.delEmphasis.alpha
}
