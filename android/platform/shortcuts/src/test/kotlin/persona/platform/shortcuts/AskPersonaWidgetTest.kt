package persona.platform.shortcuts

import android.content.ComponentName
import android.content.Context
import android.content.pm.PackageManager
import android.content.res.Configuration
import android.text.TextUtils
import android.util.TypedValue
import android.view.View
import android.widget.TextView
import androidx.glance.appwidget.SizeMode
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.launch
import kotlinx.coroutines.test.UnconfinedTestDispatcher
import kotlinx.coroutines.test.runTest
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.JsonCodec
import persona.core.contracts.api.PersonaInfo
import persona.core.contracts.api.PersonaManifest
import persona.core.designsystem.AppStyle
import persona.core.designsystem.AppStyleStore
import persona.core.designsystem.PersonaDesign
import persona.core.designsystem.SystemSurfaces
import persona.core.designsystem.ThemeAccent
import persona.core.designsystem.ThemeColor
import persona.core.designsystem.ThemeColors
import java.io.File
import kotlin.math.roundToInt

/** Layout sizing, theme tokens, manifest registration and i18n coverage for [AskPersonaWidget] and [PersonaWidget]. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35])
class AskPersonaWidgetTest {
    private val context: Context get() = ApplicationProvider.getApplicationContext()

    @Test fun askWidgetUsesExactSizeModeAndPreservesExistingAgentsWidgetReceiver() {
        val askReceiver = AskPersonaWidgetReceiver()
        assertTrue(askReceiver.glanceAppWidget is AskPersonaWidget)
        assertEquals(SizeMode.Exact, askReceiver.glanceAppWidget.sizeMode)

        // Upgrade compatibility: already-placed Agents widgets stay bound to PersonaWidgetReceiver.
        val agentsReceiver = PersonaWidgetReceiver()
        assertTrue(agentsReceiver.glanceAppWidget is PersonaWidget)
        assertEquals("persona.platform.shortcuts.PersonaWidgetReceiver", PersonaWidgetReceiver::class.java.name)
        assertEquals("persona.platform.shortcuts.AskPersonaWidgetReceiver", AskPersonaWidgetReceiver::class.java.name)
    }

    @Test fun manifestRegistersBothWidgetsWithDistinctLabelsAndProviders() {
        val pm = context.packageManager
        val agentsInfo = pm.getReceiverInfo(
            ComponentName(context, PersonaWidgetReceiver::class.java),
            PackageManager.GET_META_DATA,
        )
        val askInfo = pm.getReceiverInfo(
            ComponentName(context, AskPersonaWidgetReceiver::class.java),
            PackageManager.GET_META_DATA,
        )
        assertTrue(agentsInfo.exported)
        assertTrue(askInfo.exported)
        assertEquals(R.string.shortcuts_widget_label, agentsInfo.labelRes)
        assertEquals(R.string.shortcuts_ask_label, askInfo.labelRes)
        assertEquals(R.xml.persona_widget_info, agentsInfo.metaData.getInt("android.appwidget.provider"))
        assertEquals(R.xml.ask_persona_widget_info, askInfo.metaData.getInt("android.appwidget.provider"))
    }

    @Test fun layoutAdaptsToCommonLauncherSizes() {
        // 2 × 1 tight resize (110 × 48 dp): 32 dp inner height fits one compact title line without clipping.
        val tiny = AskWidgetLayout.resolve(widthDp = 110f, heightDp = 48f)
        assertEquals(AskWidgetMode.Row, tiny.mode)
        assertEquals(AskWidgetBadge.MarkOnly, tiny.badge)
        assertEquals(AskWidgetTitleScale.Compact, tiny.titleScale)
        assertTrue(tiny.tightPadding)
        assertFalse(tiny.showSubtitle)
        assertEquals(1, tiny.titleMaxLines)

        // 2 × 1 standard (160 × 68 dp): 44 dp inner height fits two 20 dp compact title lines without subtitle.
        val twoByOne = AskWidgetLayout.resolve(widthDp = 160f, heightDp = 68f)
        assertEquals(AskWidgetMode.Row, twoByOne.mode)
        assertEquals(AskWidgetBadge.Compact, twoByOne.badge)
        assertEquals(AskWidgetTitleScale.Compact, twoByOne.titleScale)
        assertFalse(twoByOne.tightPadding)
        assertFalse(twoByOne.showSubtitle)
        assertEquals(2, twoByOne.titleMaxLines)

        // 2 × 1 tall launcher row (160 × 100 dp): room for subtle 1-line subtitle when paired, 2 lines when unpaired.
        val twoByOneTall = AskWidgetLayout.resolve(widthDp = 160f, heightDp = 100f, paired = true)
        assertEquals(AskWidgetMode.Row, twoByOneTall.mode)
        assertTrue(twoByOneTall.showSubtitle)
        assertEquals(1, twoByOneTall.subtitleMaxLines)
        val twoByOneUnpaired = AskWidgetLayout.resolve(widthDp = 160f, heightDp = 100f, paired = false)
        assertTrue(twoByOneUnpaired.showSubtitle)
        assertEquals(2, twoByOneUnpaired.subtitleMaxLines)

        // 4 × 1 wide bar (300 × 68 dp): standard 40dp badge, section title, 1-line subtitle.
        val fourByOne = AskWidgetLayout.resolve(widthDp = 300f, heightDp = 68f)
        assertEquals(AskWidgetMode.Row, fourByOne.mode)
        assertEquals(AskWidgetBadge.Standard, fourByOne.badge)
        assertEquals(AskWidgetTitleScale.Section, fourByOne.titleScale)
        assertTrue(fourByOne.showSubtitle)
        assertEquals(1, fourByOne.titleMaxLines)
        assertEquals(1, fourByOne.subtitleMaxLines)

        // 4 × 1 squeezed height (300 × 52 dp): drops subtitle before clipping.
        val fourByOneShort = AskWidgetLayout.resolve(widthDp = 300f, heightDp = 52f)
        assertEquals(AskWidgetMode.Row, fourByOneShort.mode)
        assertTrue(fourByOneShort.tightPadding)
        assertFalse(fourByOneShort.showSubtitle)

        // 2 × 2 square tile (160 × 160 dp): stacked hero badge above bottom-anchored section title + subtitle.
        val twoByTwo = AskWidgetLayout.resolve(widthDp = 160f, heightDp = 160f, paired = true)
        assertEquals(AskWidgetMode.Stacked, twoByTwo.mode)
        assertEquals(AskWidgetBadge.Standard, twoByTwo.badge)
        assertEquals(AskWidgetTitleScale.Section, twoByTwo.titleScale)
        assertTrue(twoByTwo.showSubtitle)
        assertEquals(1, twoByTwo.subtitleMaxLines)

        // 4 × 2 wide card (300 × 160 dp): stacked standard badge + hero title + subtitle (2 lines when unpaired).
        val fourByTwo = AskWidgetLayout.resolve(widthDp = 300f, heightDp = 160f, paired = true)
        assertEquals(AskWidgetMode.Stacked, fourByTwo.mode)
        assertEquals(AskWidgetBadge.Standard, fourByTwo.badge)
        assertEquals(AskWidgetTitleScale.Hero, fourByTwo.titleScale)
        assertTrue(fourByTwo.showSubtitle)
        assertEquals(1, fourByTwo.subtitleMaxLines)

        val fourByTwoUnpaired = AskWidgetLayout.resolve(widthDp = 300f, heightDp = 160f, paired = false)
        assertEquals(AskWidgetMode.Stacked, fourByTwoUnpaired.mode)
        assertTrue(fourByTwoUnpaired.showSubtitle)
        assertEquals(2, fourByTwoUnpaired.subtitleMaxLines)
    }

    @Test fun layoutDegradesGracefullyUnderLargeSystemFontScales() {
        // 4 × 2 steps hero title down to section title when system text is enlarged.
        val largeFourByTwo = AskWidgetLayout.resolve(widthDp = 300f, heightDp = 160f, fontScale = 1.5f)
        assertEquals(AskWidgetMode.Stacked, largeFourByTwo.mode)
        assertEquals(AskWidgetTitleScale.Section, largeFourByTwo.titleScale)
        assertTrue(largeFourByTwo.showSubtitle)

        // 2 × 2 at 2× font scale drops subtitle and stays single-line so mark and title never clip.
        val maxTwoByTwo = AskWidgetLayout.resolve(widthDp = 160f, heightDp = 130f, fontScale = 2.0f)
        assertEquals(AskWidgetMode.Stacked, maxTwoByTwo.mode)
        assertFalse(maxTwoByTwo.showSubtitle)
        assertEquals(1, maxTwoByTwo.titleMaxLines)

        // 4 × 1 at 1.5× font scale drops subtitle so the bar stays single-line and unclipped.
        val largeFourByOne = AskWidgetLayout.resolve(widthDp = 300f, heightDp = 68f, fontScale = 1.5f)
        assertEquals(AskWidgetMode.Row, largeFourByOne.mode)
        assertFalse(largeFourByOne.showSubtitle)
        assertEquals(1, largeFourByOne.titleMaxLines)

        // Declared minimum height (110 × 40 dp) at 2.0× font scale drops vertical padding and limits title to 1 line.
        val minAtDouble = AskWidgetLayout.resolve(widthDp = 110f, heightDp = 40f, fontScale = 2.0f)
        assertEquals(AskWidgetMode.Row, minAtDouble.mode)
        assertEquals(AskWidgetBadge.MarkOnly, minAtDouble.badge)
        assertEquals(AskWidgetTitleScale.Compact, minAtDouble.titleScale)
        assertEquals(AskWidgetPadding.FlushVertical, minAtDouble.padding)
        assertFalse(minAtDouble.showSubtitle)
        assertEquals(1, minAtDouble.titleMaxLines)

        // Wide minimum height (300 × 40 dp) at 2.0× steps title down from Section to Compact so 1 line fits.
        val wideMinAtDouble = AskWidgetLayout.resolve(widthDp = 300f, heightDp = 40f, fontScale = 2.0f)
        assertEquals(AskWidgetMode.Row, wideMinAtDouble.mode)
        assertEquals(AskWidgetTitleScale.Compact, wideMinAtDouble.titleScale)
        assertEquals(AskWidgetPadding.FlushVertical, wideMinAtDouble.padding)
        assertFalse(wideMinAtDouble.showSubtitle)
        assertEquals(1, wideMinAtDouble.titleMaxLines)

        // Small height (110 × 48 dp) at 2.0× uses compact vertical padding and stays single-line.
        val smallAtDouble = AskWidgetLayout.resolve(widthDp = 110f, heightDp = 48f, fontScale = 2.0f)
        assertEquals(AskWidgetMode.Row, smallAtDouble.mode)
        assertEquals(AskWidgetTitleScale.Compact, smallAtDouble.titleScale)
        assertEquals(AskWidgetPadding.CompactVertical, smallAtDouble.padding)
        assertFalse(smallAtDouble.showSubtitle)
        assertEquals(1, smallAtDouble.titleMaxLines)

        // Every supported launcher size and font scale fits within its declared vertical budget.
        val sizes = listOf(
            110f to 40f, 300f to 40f, 110f to 48f, 160f to 68f,
            300f to 68f, 160f to 100f, 160f to 130f, 160f to 160f, 300f to 160f,
        )
        for ((w, h) in sizes) {
            for (scale in listOf(1.0f, 1.3f, 1.5f, 2.0f)) {
                for (paired in listOf(true, false)) {
                    val spec = AskWidgetLayout.resolve(w, h, scale, paired)
                    val required = AskWidgetLayout.requiredHeightDp(spec, scale)
                    assertTrue(
                        "Expected ${w}x${h}dp @ ${scale}x (paired=$paired) to fit: required=$required, spec=$spec",
                        required <= h,
                    )
                }
            }
        }
    }

    @Test fun smallHeightAtDoubleFontScaleKeepsRenderedTitleVisible() {
        val scaledConfig = Configuration(context.resources.configuration).apply { fontScale = 2.0f }
        val scaledContext = context.createConfigurationContext(scaledConfig)
        val metrics = scaledContext.resources.displayMetrics
        fun dpToPx(dp: Float): Int = TypedValue.applyDimension(TypedValue.COMPLEX_UNIT_DIP, dp, metrics).roundToInt()

        val cases = listOf(110f to 40f, 300f to 40f, 110f to 48f)
        for (style in AppStyle.entries) {
            val look = WidgetLook.of(style)
            for ((widthDp, heightDp) in cases) {
                val spec = AskWidgetLayout.resolve(widthDp, heightDp, fontScale = 2.0f, paired = true)
                val titleStyle = when (spec.titleScale) {
                    AskWidgetTitleScale.Hero -> look.headline
                    AskWidgetTitleScale.Section -> look.sectionTitle
                    AskWidgetTitleScale.Compact -> look.itemTitle
                }
                val hPadDp = AskWidgetLayout.horizontalPaddingDp(spec.padding)
                val vPadDp = AskWidgetLayout.verticalPaddingDp(spec.padding)
                val gapDp = if (spec.padding == AskWidgetPadding.Normal) 12f else 8f
                val textWidthDp = (widthDp - hPadDp - AskWidgetLayout.badgeHeight(spec.badge) - gapDp).coerceAtLeast(1f)
                val innerHeightPx = dpToPx(heightDp - vPadDp)
                val containerHeightPx = dpToPx(heightDp)

                val view = TextView(scaledContext).apply {
                    text = scaledContext.getString(R.string.shortcuts_ask_label)
                    setTextSize(TypedValue.COMPLEX_UNIT_SP, titleStyle.fontSize!!.value)
                    maxLines = spec.titleMaxLines
                    ellipsize = TextUtils.TruncateAt.END
                }
                view.measure(
                    View.MeasureSpec.makeMeasureSpec(dpToPx(textWidthDp), View.MeasureSpec.AT_MOST),
                    View.MeasureSpec.makeMeasureSpec(0, View.MeasureSpec.UNSPECIFIED),
                )
                view.layout(0, 0, view.measuredWidth, view.measuredHeight)

                assertNotNull(view.layout)
                assertEquals(1, view.lineCount)
                assertTrue(view.measuredHeight > 0)
                assertTrue(
                    "Rendered title clipped for $style ${widthDp}x${heightDp}dp @ 2.0x: " +
                        "measured=${view.measuredHeight}px, vPad=${dpToPx(vPadDp)}px, container=${containerHeightPx}px",
                    view.measuredHeight + dpToPx(vPadDp) <= containerHeightPx,
                )
                assertTrue(
                    "First line bottom exceeded inner height for $style ${widthDp}x${heightDp}dp @ 2.0x: " +
                        "lineBottom=${view.layout.getLineBottom(0)}px, inner=${innerHeightPx}px",
                    view.layout.getLineBottom(0) <= innerHeightPx,
                )
            }
        }
    }

    @Test fun widgetLookFollowsAllThreeStylesLightDarkAndThemeColor() {
        for (style in AppStyle.entries) {
            val look = WidgetLook.of(style)
            assertEquals(SystemSurfaces.palette(style, false).surfaceOpaque, look.lightPalette.surfaceOpaque)
            assertEquals(SystemSurfaces.palette(style, true).surfaceOpaque, look.darkPalette.surfaceOpaque)
            assertNotEquals(look.lightPalette.surfaceOpaque, look.darkPalette.surfaceOpaque)
            assertEquals(SystemSurfaces.type(style).title.fontSize, look.headline.fontSize)
            assertEquals(SystemSurfaces.type(style).headline.fontSize, look.sectionTitle.fontSize)
            assertEquals(SystemSurfaces.type(style).caption.fontSize, look.caption.fontSize)
        }

        val store = AppStyleStore(context)
        try {
            store.setStyle(AppStyle.Board)
            store.setColor(ThemeColor.Coral)
            val before = WidgetLook.revision.value
            WidgetLook.invalidate()
            assertEquals(before + 1, WidgetLook.revision.value)
            val current = WidgetLook.current(context)
            assertEquals(AppStyle.Board, current.style)
            assertNotEquals(SystemSurfaces.palette(AppStyle.Board, false).accent, current.lightPalette.accent)
        } finally {
            store.setStyle(AppStyle.Default)
            store.setColor(ThemeColor.Default)
        }
    }

    @Test fun widgetLookAppliesPersonaPackageColorsAcrossStylesAndFallsBackWhenAbsent() {
        val colors = PersonaManifest.Colors(
            light = mapOf("accent" to "#94600A"),
            dark = mapOf("accent" to "#B47A0C"),
        )
        val expectedAccent = ThemeColors.accent(ThemeColor.Persona, colors)!!
        for (style in AppStyle.entries) {
            val baseLight = SystemSurfaces.palette(style, false)
            val baseDark = SystemSurfaces.palette(style, true)
            val look = WidgetLook.of(style, ThemeColor.Persona, colors)
            assertEquals(ThemeColors.palette(baseLight, expectedAccent), look.lightPalette)
            assertEquals(ThemeColors.palette(baseDark, expectedAccent), look.darkPalette)
            assertNotEquals(baseLight.accent, look.lightPalette.accent)
            assertNotEquals(baseDark.accent, look.darkPalette.accent)

            val missing = WidgetLook.of(style, ThemeColor.Persona, null)
            assertEquals(baseLight, missing.lightPalette)
            assertEquals(baseDark, missing.darkPalette)

            val empty = WidgetLook.of(style, ThemeColor.Persona, PersonaManifest.Colors())
            assertEquals(baseLight, empty.lightPalette)
            assertEquals(baseDark, empty.darkPalette)
        }
    }

    @Test fun widgetLookCurrentReadsInstalledPersonaPackageColorsFromDisk() {
        val colors = PersonaManifest.Colors(
            light = mapOf("accent" to "#94600A"),
            dark = mapOf("accent" to "#B47A0C"),
        )
        val pack = personaPack("aurora", "0123456789abcdef", colors)
        val root = File(context.filesDir, "personas")
        val store = AppStyleStore(context)
        try {
            root.deleteRecursively()
            root.mkdirs()
            File(root, "index.json").writeText(JsonCodec.encodeToString(listOf(pack)))
            store.setStyle(AppStyle.Board)
            store.setColor(ThemeColor.Persona)
            store.setPersonaId(pack.id)

            // Listed in index.json but not yet downloaded: falls back to the style's base palette.
            val beforeDownload = WidgetLook.current(context)
            assertEquals(SystemSurfaces.palette(AppStyle.Board, false).accent, beforeDownload.lightPalette.accent)
            assertEquals(SystemSurfaces.palette(AppStyle.Board, true).accent, beforeDownload.darkPalette.accent)

            // Once the package directory is on disk, ThemeColor.Persona applies its light/dark accents.
            val packDir = File(File(root, pack.id), pack.rev)
            assertTrue(packDir.mkdirs())
            val expected = WidgetLook.of(AppStyle.Board, ThemeColor.Persona, colors)
            val afterDownload = WidgetLook.current(context)
            assertEquals(expected.lightPalette, afterDownload.lightPalette)
            assertEquals(expected.darkPalette, afterDownload.darkPalette)
            assertNotEquals(SystemSurfaces.palette(AppStyle.Board, false).accent, afterDownload.lightPalette.accent)
            assertNotEquals(SystemSurfaces.palette(AppStyle.Board, true).accent, afterDownload.darkPalette.accent)

            // Switching back to a built-in face restores the style's base palette.
            store.setFace(PersonaDesign.Default)
            val builtIn = WidgetLook.current(context)
            assertEquals(SystemSurfaces.palette(AppStyle.Board, false).accent, builtIn.lightPalette.accent)
            assertEquals(SystemSurfaces.palette(AppStyle.Board, true).accent, builtIn.darkPalette.accent)
        } finally {
            root.deleteRecursively()
            store.setFace(PersonaDesign.Default)
            store.setStyle(AppStyle.Default)
            store.setColor(ThemeColor.Default)
        }
    }

    @OptIn(ExperimentalCoroutinesApi::class)
    @Test fun widgetAppearanceChangesTriggersOnPersonaPackageAndColorUpdates() = runTest {
        val colorsA = PersonaManifest.Colors(light = mapOf("accent" to "#94600A"), dark = mapOf("accent" to "#B47A0C"))
        val colorsB = PersonaManifest.Colors(light = mapOf("accent" to "#1F6F5B"), dark = mapOf("accent" to "#4AC2A3"))
        val colorsC = PersonaManifest.Colors(light = mapOf("accent" to "#7B2CBF"), dark = mapOf("accent" to "#C77DFF"))
        val aurora = personaPack("aurora", "0123456789abcdef", colorsA)
        val sage = personaPack("sage", "1122334455667788", colorsB)
        val sageUpdated = personaPack("sage", "8877665544332211", colorsC)
        val sageSameColors = personaPack("sage", "fedcba9876543210", colorsC)
        val plain = personaPack("plain", "abcdef0123456789", colors = null)

        val style = MutableStateFlow(AppStyle.Glass)
        val color = MutableStateFlow(ThemeColor.Style)
        val persona = MutableStateFlow<PersonaInfo?>(null)
        val emitted = mutableListOf<Pair<AppStyle, ThemeAccent?>>()
        val job = backgroundScope.launch(UnconfinedTestDispatcher(testScheduler)) {
            PersonaWidget.appearanceChanges(style, color, persona).collect { emitted += it }
        }
        try {
            // Initial snapshot is skipped; package switches under ThemeColor.Style do not trigger redraws.
            assertTrue(emitted.isEmpty())
            persona.value = aurora
            assertTrue(emitted.isEmpty())

            // Switching to ThemeColor.Persona resolves the selected package's colours.
            color.value = ThemeColor.Persona
            assertEquals(listOf(AppStyle.Glass to ThemeColors.accent(ThemeColor.Persona, colorsA)), emitted)

            // Switching to another persona package with different colours triggers a refresh.
            persona.value = sage
            assertEquals(2, emitted.size)
            assertEquals(AppStyle.Glass to ThemeColors.accent(ThemeColor.Persona, colorsB), emitted.last())

            // Updating the selected package's colours triggers a refresh; a new rev with identical colours does not.
            persona.value = sageUpdated
            assertEquals(3, emitted.size)
            assertEquals(AppStyle.Glass to ThemeColors.accent(ThemeColor.Persona, colorsC), emitted.last())
            persona.value = sageSameColors
            assertEquals(3, emitted.size)

            // Reverting to a built-in (null) triggers a refresh back to null accent; a colourless pack does not re-emit.
            persona.value = null
            assertEquals(4, emitted.size)
            assertEquals(AppStyle.Glass to null, emitted.last())
            persona.value = plain
            assertEquals(4, emitted.size)

            // Style and preset colour changes emit; subsequent package changes under a fixed preset do not.
            style.value = AppStyle.Brief
            assertEquals(5, emitted.size)
            assertEquals(AppStyle.Brief to null, emitted.last())
            color.value = ThemeColor.Coral
            assertEquals(6, emitted.size)
            assertEquals(AppStyle.Brief to ThemeColor.Coral.preset, emitted.last())
            persona.value = aurora
            assertEquals(6, emitted.size)
        } finally {
            job.cancel()
        }
    }

    private fun personaPack(id: String, rev: String, colors: PersonaManifest.Colors?) = PersonaInfo(
        id = id,
        rev = rev,
        manifest = PersonaManifest(
            format = PersonaManifest.FORMAT,
            id = id,
            name = id.replaceFirstChar { it.uppercase() },
            avatar = PersonaManifest.Avatar(mapOf("64" to "avatar/64.png")),
            dot = PersonaManifest.Dot(144, mapOf("idle" to PersonaManifest.DotState("dot/idle.png"))),
            colors = colors,
        ),
    )

    @Config(sdk = [35], qualifiers = "en")
    @Test fun englishWidgetResourcesDistinguishAgentsAndAskPersona() {
        assertEquals("Agents", context.getString(R.string.shortcuts_widget_label))
        assertEquals("Which agents need you, are working, or are done", context.getString(R.string.shortcuts_widget_description))
        assertEquals("Ask Persona", context.getString(R.string.shortcuts_ask_label))
        assertEquals("Open your main chat with Persona and start typing", context.getString(R.string.shortcuts_ask_widget_description))
        assertEquals("Main chat", context.getString(R.string.shortcuts_ask_widget_subtitle))
    }

    @Config(sdk = [35], qualifiers = "zh")
    @Test fun simplifiedChineseWidgetResourcesDistinguishAgentsAndAskPersona() {
        assertEquals("Agents", context.getString(R.string.shortcuts_widget_label))
        assertEquals("几个 Agent 需要你、在工作、做完了", context.getString(R.string.shortcuts_widget_description))
        assertEquals("问 Persona", context.getString(R.string.shortcuts_ask_label))
        assertEquals("直接打开与 Persona 的主对话并开始输入", context.getString(R.string.shortcuts_ask_widget_description))
        assertEquals("主对话", context.getString(R.string.shortcuts_ask_widget_subtitle))
    }

    @Config(sdk = [35], qualifiers = "ja")
    @Test fun japaneseWidgetResourcesDistinguishAgentsAndAskPersona() {
        assertEquals("Agents", context.getString(R.string.shortcuts_widget_label))
        assertEquals("あなたの対応待ち・作業中・完了したエージェント", context.getString(R.string.shortcuts_widget_description))
        assertEquals("Personaに質問", context.getString(R.string.shortcuts_ask_label))
        assertEquals("Personaとのメインチャットを開いてすぐに入力", context.getString(R.string.shortcuts_ask_widget_description))
        assertEquals("メインチャット", context.getString(R.string.shortcuts_ask_widget_subtitle))
    }
}
