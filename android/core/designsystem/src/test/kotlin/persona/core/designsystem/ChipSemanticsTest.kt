package persona.core.designsystem

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.semantics.getOrNull
import androidx.compose.ui.test.SemanticsMatcher
import androidx.compose.ui.test.assert
import androidx.compose.ui.test.assertIsSelected
import androidx.compose.ui.test.getUnclippedBoundsInRoot
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollTo
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.designsystem.icons.PhIcons

/** Toggle chips retain checkbox semantics; expansion entries are buttons with an expanded / collapsed state. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class ChipSemanticsTest {
    @get:Rule val rule = createComposeRule()

    @Test fun toggleChipsKeepTheirExistingRoleAndSelection() {
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                Column {
                    JChip("Filter", Modifier.testTag("toggle"), selected = true, onClick = {})
                    JChip("Open menu", Modifier.testTag("action"), selected = true, onClick = {}, onLongClick = {}, role = Role.Button)
                }
            }
        }
        rule.onNodeWithTag("toggle")
            .assert(SemanticsMatcher.expectValue(SemanticsProperties.Role, Role.Checkbox)).assertIsSelected()
        rule.onNodeWithTag("action")
            .assert(SemanticsMatcher.expectValue(SemanticsProperties.Role, Role.Button))
            .assert(SemanticsMatcher.keyNotDefined(SemanticsProperties.Selected))
    }

    @Test fun glassTrayEntryIsACompactExpandableCapsuleWithFullTouchTarget() {
        var expanded by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                Column {
                    AssistantTrayEntry("Needs you", 3, AssistantTrayTone.Pending, expanded,
                        if (expanded) "Expanded" else "Collapsed", { expanded = !expanded }, Modifier.testTag("tray"))
                    JChip("Needs you", Modifier.testTag("chip"), selected = expanded, onClick = {}, count = 3,
                        dot = assistantTrayColor(AssistantTrayTone.Pending), role = Role.Button)
                    Box(Modifier.width(360.dp).padding(horizontal = Tokens.Space.m)) {
                        AssistantTrayBar(Modifier.testTag("bar")) {
                            AssistantTrayEntry("Needs you", 3, AssistantTrayTone.Pending, false, "Collapsed", {}, Modifier.testTag("bar-needs"))
                            AssistantTrayEntry("In progress", 4, AssistantTrayTone.Progress, false, "Collapsed", {}, Modifier.testTag("bar-progress"))
                            AssistantTrayEntry("Finished", 1, AssistantTrayTone.Finished, false, "Collapsed", {}, Modifier.testTag("bar-finished"))
                        }
                    }
                }
            }
        }
        val tray = rule.onNodeWithTag("tray")
        tray.assert(SemanticsMatcher.expectValue(SemanticsProperties.Role, Role.Button))
            .assert(SemanticsMatcher.expectValue(SemanticsProperties.StateDescription, "Collapsed"))
            .assert(SemanticsMatcher.keyNotDefined(SemanticsProperties.Selected))
        val trayNode = tray.fetchSemanticsNode()
        val chipWidth = rule.onNodeWithTag("chip").fetchSemanticsNode().boundsInRoot.width
        assertTrue("Glass capsule omits the leading dot and caret, so it is narrower than JChip", trayNode.boundsInRoot.width < chipWidth)
        val minTouchPx = with(rule.density) { Tokens.MinTouch.android.toPx() }
        assertTrue("Glass capsule stays visually compact", trayNode.boundsInRoot.height < minTouchPx)
        assertTrue("Glass capsule keeps Android's 48dp touch target", trayNode.touchBoundsInRoot.height >= minTouchPx)
        val barRight = rule.onNodeWithTag("bar").fetchSemanticsNode().boundsInRoot.right
        val finishedRight = rule.onNodeWithTag("bar-finished").fetchSemanticsNode().boundsInRoot.right
        assertTrue("All three default Glass entries fit within a 360dp phone width without clipping", finishedRight <= barRight)
        val fades = rule.onNodeWithTag("bar").fetchSemanticsNode().config.getOrNull(AssistantTrayFade)
        assertEquals("Fitting Glass tray bar draws no edge fades", AgentFilterFades(0f, 0f), fades)
        tray.performClick()
        tray.assert(SemanticsMatcher.expectValue(SemanticsProperties.StateDescription, "Expanded"))
            .assert(SemanticsMatcher.keyNotDefined(SemanticsProperties.Selected))
    }

    @Test fun briefAndBoardTrayEntriesStayCompactFitThreeOnPhoneAndKeepTouchTargets() {
        var style by mutableStateOf(AppStyle.Brief)
        var expanded by mutableStateOf(false)
        var labels by mutableStateOf(Triple("Needs you", "In progress", "Finished"))
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = style) {
                Column {
                    AssistantTrayEntry("Pending", 2, AssistantTrayTone.Pending, expanded,
                        if (expanded) "Expanded" else "Collapsed", { expanded = !expanded }, Modifier.testTag("tray"))
                    JChip("Pending", Modifier.testTag("chip"), selected = expanded, onClick = {}, count = 2,
                        dot = assistantTrayColor(AssistantTrayTone.Pending), role = Role.Button,
                        trailingIcon = if (expanded) PhIcons.CaretDown else PhIcons.CaretUp)
                    Box(Modifier.width(400.dp).padding(horizontal = Tokens.Space.m)) {
                        AssistantTrayBar(Modifier.testTag("bar")) {
                            AssistantTrayEntry(labels.first, 2, AssistantTrayTone.Pending, false, "Collapsed", {}, Modifier.testTag("bar-needs"))
                            AssistantTrayEntry(labels.second, 29, AssistantTrayTone.Progress, false, "Collapsed", {}, Modifier.testTag("bar-progress"))
                            AssistantTrayEntry(labels.third, 18, AssistantTrayTone.Finished, false, "Collapsed", {}, Modifier.testTag("bar-finished"))
                        }
                    }
                }
            }
        }
        val minTouchPx = with(rule.density) { Tokens.MinTouch.android.toPx() }
        val chipHeightPx = with(rule.density) { JChipTokens.height.toPx() }
        val locales = listOf(
            Triple("Needs you", "In progress", "Finished"),
            Triple("要你处理", "进行中", "已完成"),
            Triple("要対応", "進行中", "完了"),
        )
        for (s in listOf(AppStyle.Brief, AppStyle.Board)) {
            style = s
            expanded = false
            rule.waitForIdle()
            val tray = rule.onNodeWithTag("tray")
            tray.assert(SemanticsMatcher.expectValue(SemanticsProperties.Role, Role.Button))
                .assert(SemanticsMatcher.expectValue(SemanticsProperties.StateDescription, "Collapsed"))
                .assert(SemanticsMatcher.keyNotDefined(SemanticsProperties.Selected))
            val trayNode = tray.fetchSemanticsNode()
            val chipNode = rule.onNodeWithTag("chip").fetchSemanticsNode()
            assertTrue("$s tray entry is visually slimmer than JChip (${trayNode.boundsInRoot.height} < $chipHeightPx)",
                trayNode.boundsInRoot.height < chipHeightPx)
            assertTrue("$s tray entry is narrower than a boxed JChip with caret",
                trayNode.boundsInRoot.width < chipNode.boundsInRoot.width)
            assertTrue("$s tray entry keeps Android's 48dp touch target",
                trayNode.touchBoundsInRoot.height >= minTouchPx)
            tray.performClick()
            tray.assert(SemanticsMatcher.expectValue(SemanticsProperties.StateDescription, "Expanded"))
                .assert(SemanticsMatcher.keyNotDefined(SemanticsProperties.Selected))

            for (triplet in locales) {
                labels = triplet
                rule.waitForIdle()
                val barRight = rule.onNodeWithTag("bar").getUnclippedBoundsInRoot().right
                val finishedRight = rule.onNodeWithTag("bar-finished").getUnclippedBoundsInRoot().right
                assertTrue("$s ($triplet): three entries fit within 400dp phone width without clipping ($finishedRight <= $barRight)",
                    finishedRight <= barRight)
                val fades = rule.onNodeWithTag("bar").fetchSemanticsNode().config.getOrNull(AssistantTrayFade)
                assertEquals("$s ($triplet): fitting tray bar draws no edge fades", AgentFilterFades(0f, 0f), fades)
            }
        }
    }

    @Test fun assistantTrayBarFadesOverflowingEdgesInsteadOfHardClipping() {
        var style by mutableStateOf(AppStyle.Board)
        rule.setContent {
            PersonaTheme(darkTheme = true, reduceMotion = true, style = style) {
                Box(Modifier.width(300.dp).padding(horizontal = Tokens.Space.m)) {
                    AssistantTrayBar(Modifier.testTag("bar")) {
                        AssistantTrayEntry("Needs you", 0, AssistantTrayTone.Pending, false, "Collapsed", {}, Modifier.testTag("e-needs"))
                        AssistantTrayEntry("In progress", 29, AssistantTrayTone.Progress, false, "Collapsed", {}, Modifier.testTag("e-progress"))
                        AssistantTrayEntry("Finished", 18, AssistantTrayTone.Finished, false, "Collapsed", {}, Modifier.testTag("e-finished"))
                        AssistantTrayEntry("Held pushes", 4, AssistantTrayTone.Pending, false, "Collapsed", {}, Modifier.testTag("e-held"))
                    }
                }
            }
        }
        for (s in AppStyle.entries) {
            style = s
            rule.waitForIdle()
            val startFades = rule.onNodeWithTag("bar").fetchSemanticsNode().config.getOrNull(AssistantTrayFade)
            assertEquals("$s overflowing tray bar fades only trailing edge at start", AgentFilterFades(0f, 1f), startFades)

            rule.onNodeWithTag("e-held").performScrollTo()
            rule.waitForIdle()
            val endFades = rule.onNodeWithTag("bar").fetchSemanticsNode().config.getOrNull(AssistantTrayFade)
            assertEquals("$s scrolled-to-end tray bar fades only leading edge", AgentFilterFades(1f, 0f), endFades)

            rule.onNodeWithTag("e-needs").performScrollTo()
            rule.waitForIdle()
        }
    }

    @Test fun composerMetaChipKeepsCompactVisualHeightAndFullTouchTargetWhenInteractive() {
        var style by mutableStateOf(AppStyle.Brief)
        var taps = 0
        rule.setContent {
            PersonaTheme(darkTheme = true, reduceMotion = true, style = style) {
                ComposerMetaStrip {
                    ComposerMetaChip("Default: agy", PhIcons.Cube, Modifier.testTag("picker"), dropdown = true) { taps++ }
                    ComposerMetaChip("runtime · Opus", PhIcons.Cube, Modifier.testTag("selected"), selected = true, dropdown = true) {}
                    ComposerMetaChip("Full access", PhIcons.SlidersHorizontal, Modifier.testTag("static"))
                }
            }
        }
        val minTouchPx = with(rule.density) { Tokens.MinTouch.android.toPx() }
        val chipHeightPx = with(rule.density) { JChipTokens.height.toPx() }
        for (s in AppStyle.entries) {
            style = s
            rule.waitForIdle()
            val picker = rule.onNodeWithTag("picker")
            picker.assert(SemanticsMatcher.expectValue(SemanticsProperties.Role, Role.Button))
            val pickerNode = picker.fetchSemanticsNode()
            val staticNode = rule.onNodeWithTag("static").fetchSemanticsNode()
            assertTrue("$s picker is slimmer than JChip", pickerNode.boundsInRoot.height < chipHeightPx)
            assertEquals("$s interactive and static meta chips share visual height",
                pickerNode.boundsInRoot.height, staticNode.boundsInRoot.height, 1f)
            assertTrue("$s interactive meta chip keeps 48dp touch target", pickerNode.touchBoundsInRoot.height >= minTouchPx)
            val before = taps
            picker.performClick()
            assertEquals("$s picker tap increments counter", before + 1, taps)
        }
    }

    @Test fun assistantTrayColorFollowsStyleStatusPalette() {
        val observed = mutableMapOf<Triple<AppStyle, Boolean, AssistantTrayTone>, Pair<Color, Color>>()
        rule.setContent {
            for (style in AppStyle.entries) for (dark in listOf(false, true)) {
                PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                    val palette = PersonaTheme.palette
                    observed[Triple(style, dark, AssistantTrayTone.Pending)] =
                        assistantTrayColor(AssistantTrayTone.Pending) to palette.needsYou.dot
                    observed[Triple(style, dark, AssistantTrayTone.Progress)] =
                        assistantTrayColor(AssistantTrayTone.Progress) to palette.working.dot
                    observed[Triple(style, dark, AssistantTrayTone.Finished)] =
                        assistantTrayColor(AssistantTrayTone.Finished) to palette.positive
                    observed[Triple(style, dark, AssistantTrayTone.Agents)] =
                        assistantTrayColor(AssistantTrayTone.Agents) to palette.positive
                }
            }
        }
        rule.waitForIdle()
        for ((key, pair) in observed) {
            assertEquals("Tone color mismatch for $key", pair.second, pair.first)
        }
    }
}
