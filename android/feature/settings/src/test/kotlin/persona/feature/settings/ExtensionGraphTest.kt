package persona.feature.settings

import androidx.compose.runtime.mutableStateOf
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Rect
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.semantics.getOrNull
import androidx.compose.ui.test.assert
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.assertHasClickAction
import androidx.compose.ui.test.assertHasNoClickAction
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.assertIsEnabled
import androidx.compose.ui.test.assertIsNotEnabled
import androidx.compose.ui.test.hasAnyAncestor
import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.isHeading
import androidx.compose.ui.test.junit4.StateRestorationTester
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.dp
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.api.ExtensionView
import persona.core.contracts.api.ExtensionViewItem
import persona.core.contracts.api.ExtensionViewSection
import persona.core.contracts.generated.ExtensionAction
import persona.core.contracts.generated.ExtensionBadge
import persona.core.contracts.generated.ExtensionGraphDirection
import persona.core.contracts.generated.ExtensionGraphEdge
import persona.core.contracts.generated.ExtensionGraphNode
import persona.core.contracts.generated.ExtensionPush
import persona.core.designsystem.GraphTokens
import persona.core.designsystem.PersonaTheme
import kotlin.math.abs

/** Graph transform bounds, action dispatch, folding, full-screen continuity and compact-preview boundaries. */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "w400dp-h1000dp-mdpi")
class ExtensionGraphTest {
    @get:Rule val rule = createComposeRule()

    private val nodes = listOf(
        ExtensionGraphNode(
            id = "root", title = "Plan", subtitle = "3 steps", progress = 0.25,
            badge = ExtensionBadge(text = "Active"),
            action = ExtensionAction(
                id = "open-plan", confirm = "Open the plan?", submit = true,
                push = ExtensionPush(state = "step=1", title = "Plan", ui = "plan"),
                values = mapOf("node" to "root"),
            ),
        ),
        ExtensionGraphNode(id = "design", title = "Design", subtitle = "Ready", group = "Work"),
        ExtensionGraphNode(id = "build", title = "Build", subtitle = "Waiting", group = "Work"),
    )
    private val item = ExtensionViewItem(
        type = "graph", title = "Delivery graph", key = "delivery", nodes = nodes,
        edges = listOf(ExtensionGraphEdge("root", "design"), ExtensionGraphEdge("root", "build")),
        selected = "design",
    )

    @Test fun openingAndExplicitFitUseTheirOwnZoomBounds() {
        val viewport = Size(400f, 300f)
        val world = Size(800f, 600f)
        val opening = fitExtensionGraph(viewport, world, 16f, opening = true)
        assertEquals(0.5f, opening.scale)
        val fit = fitExtensionGraph(viewport, world, 16f, opening = false)
        assertEquals(268f / 600f, fit.scale, 1e-6f)
        assertEquals(1f, fitExtensionGraph(viewport, Size(100f, 50f), 16f, opening = true).scale)

        val focused = fitExtensionGraph(
            viewport, world, 16f, opening = true,
            focus = Rect(200f, 100f, 400f, 200f),
        )
        val centre = Offset(300f, 150f) * focused.scale + focused.pan
        assertEquals(200f, centre.x, 1e-5f)
        assertEquals(150f, centre.y, 1e-5f)
    }

    @Test fun zoomClampsAndPanKeepsEveryLargeGraphEdgeReachable() {
        var transform = ExtensionGraphTransform(1f, Offset.Zero)
        repeat(20) {
            transform = zoomExtensionGraph(transform, 2f, Size(400f, 300f), Size(800f, 600f), 16f)
        }
        assertEquals(2f, transform.scale)
        repeat(40) {
            transform = zoomExtensionGraph(transform, 0.5f, Size(400f, 300f), Size(800f, 600f), 16f)
        }
        assertEquals(0.25f, transform.scale)

        val clamped = clampExtensionGraphPan(
            Offset(1000f, -1000f), 2f, Size(400f, 300f), Size(800f, 600f), 16f,
        )
        assertEquals(16f, clamped.x)
        assertEquals(300f - 16f - 1200f, clamped.y)
        assertEquals(
            Offset(100f, 75f),
            clampExtensionGraphPan(Offset.Zero, 0.25f, Size(400f, 300f), Size(800f, 600f), 16f),
        )
    }

    @Test fun nodeActionsAndFoldControlsStaySeparate() {
        var pressed: Pair<ExtensionViewItem, persona.core.contracts.api.ExtensionViewAction>? = null
        mount(item) { view, action -> pressed = view to action }
        rule.onNodeWithContentDescription("Plan, Active, 3 steps, 25%").assertIsDisplayed()
            .assertHasClickAction().performClick()
        rule.onNodeWithContentDescription("Design, Ready, under Plan").assertExists()
            .assertHasNoClickAction()
        val nodeBounds = rule.onNodeWithTag("graph-node-root").fetchSemanticsNode().boundsInRoot
        val foldVisual = rule.onNodeWithTag("graph-fold-visual-root", useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
        val visualSize = with(rule.density) { GraphTokens.foldVisual.toPx() }
        assertEquals("the fold chip is not stretched to its hit target", visualSize, foldVisual.width, 1f)
        assertEquals("the fold chip is not stretched to its hit target", visualSize, foldVisual.height, 1f)
        assertTrue("fold visual stays outside the node: $nodeBounds / $foldVisual", foldVisual.left >= nodeBounds.right - 1f)
        val selected = rule.onNodeWithTag("graph-node-design").fetchSemanticsNode().config[SemanticsProperties.Selected]
        assertEquals(true, selected)
        val (view, action) = pressed!!
        assertEquals("row", view.type)
        assertEquals("delivery/root", view.key)
        assertEquals("open-plan", action.id)
        assertEquals("Open the plan?", action.confirm)
        assertTrue(action.submit)
        assertEquals("step=1", action.push?.state)
        assertEquals(mapOf("node" to "root"), action.values)

        pressed = null
        rule.onNodeWithTag("graph-fold-root").performClick()
        assertEquals(null, pressed)
        rule.onNodeWithTag("graph-node-design").assertDoesNotExist()
        rule.onNodeWithTag("graph-node-build").assertDoesNotExist()
        rule.onNodeWithContentDescription("Show 2 hidden").performClick()
        rule.onNodeWithTag("graph-node-design").assertExists()
        rule.onNodeWithTag("graph-node-build").assertExists()
    }

    @Test fun zoomAndFoldsContinueIntoFullScreenAndBack() {
        mount(item)
        rule.onNodeWithTag("graph-zoom-in").performClick()
        val before = rule.onNodeWithTag("graph-node-root").fetchSemanticsNode().boundsInRoot.width
        rule.onNodeWithTag("graph-open-full").performClick()
        val inDialog = hasTestTag("graph-node-root") and hasAnyAncestor(isDialog())
        rule.onNode(inDialog).assertIsDisplayed()
        val after = rule.onNode(inDialog).fetchSemanticsNode().boundsInRoot.width
        assertTrue("zoom survives full screen: $before vs $after", abs(before - after) < 1f)
        rule.onNode(hasTestTag("graph-fold-root") and hasAnyAncestor(isDialog())).performClick()
        rule.onNodeWithContentDescription("Back").performClick()
        rule.onNodeWithTag("graph-node-design").assertDoesNotExist()
        rule.onNodeWithContentDescription("Show 2 hidden").assertExists()
    }

    @Test fun untitledFullScreenIsTitledByItsFirstNode() {
        mount(item.copy(title = null))
        rule.onNodeWithTag("graph-open-full").performClick()
        rule.onNode(hasText("Plan") and isHeading() and hasAnyAncestor(isDialog())).assertIsDisplayed()
    }

    @Test fun transformAndFoldsSurviveSavedStateRestoration() {
        val restoration = StateRestorationTester(rule)
        restoration.setContent {
            PersonaTheme(reduceMotion = true) {
                ExtensionGraph(item, ExtensionViewState(), ExtensionViewHandle(
                    press = { _, _ -> }, edit = { _, _ -> }, choose = {}, open = {},
                ), Modifier.width(400.dp))
            }
        }
        rule.onNodeWithTag("graph-zoom-in").performClick()
        val zoomedWidth = rule.onNodeWithTag("graph-node-root").fetchSemanticsNode().boundsInRoot.width
        rule.onNodeWithTag("graph-fold-root").performClick()
        restoration.emulateSavedInstanceStateRestore()
        rule.onNodeWithTag("graph-node-design").assertDoesNotExist()
        rule.onNodeWithContentDescription("Show 2 hidden").assertExists()
        assertEquals(
            zoomedWidth,
            rule.onNodeWithTag("graph-node-root").fetchSemanticsNode().boundsInRoot.width,
            1f,
        )
    }

    @Test fun compactPreviewHasNoInnerActionsAndOnlyOpensFullScreen() {
        var pressed = false
        mount(item, compact = true) { _, _ -> pressed = true }
        rule.onNodeWithTag("graph-node-root").assertHasNoClickAction()
        rule.onAllNodesWithTag("graph-zoom-in").assertCountEquals(0)
        rule.onAllNodesWithTag("graph-fold-root").assertCountEquals(0)
        rule.onNodeWithContentDescription("Open graph full screen").assertHasClickAction().performClick()
        rule.onNodeWithTag("graph-full-screen").assertIsDisplayed()
        rule.onNode(hasTestTag("graph-zoom-in") and hasAnyAncestor(isDialog())).assertIsEnabled()
        assertEquals(false, pressed)
    }

    @Test fun untouchedRedrawRefitsButTouchedRedrawKeepsItsPosition() {
        val current = mutableStateOf(item)
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                ExtensionGraph(current.value, ExtensionViewState(), ExtensionViewHandle(
                    press = { _, _ -> }, edit = { _, _ -> }, choose = {}, open = {},
                ), Modifier.width(400.dp))
            }
        }
        val before = rule.onNodeWithTag("graph-node-design").fetchSemanticsNode().boundsInRoot.center
        current.value = item.copy(selected = "build")
        rule.waitForIdle()
        val refitted = rule.onNodeWithTag("graph-node-design").fetchSemanticsNode().boundsInRoot.center
        assertTrue("an untouched graph takes its new opening view", abs(before.x - refitted.x) + abs(before.y - refitted.y) > 1f)

        rule.onNodeWithTag("graph-zoom-in").performClick()
        val moved = rule.onNodeWithTag("graph-node-root").fetchSemanticsNode().boundsInRoot
        current.value = item.copy(selected = "root")
        rule.waitForIdle()
        assertEquals(moved, rule.onNodeWithTag("graph-node-root").fetchSemanticsNode().boundsInRoot)
    }

    @Test fun changingGraphKeyStartsWithANewOpeningTransform() {
        val current = mutableStateOf(item)
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                ExtensionGraph(current.value, ExtensionViewState(), ExtensionViewHandle(
                    press = { _, _ -> }, edit = { _, _ -> }, choose = {}, open = {},
                ), Modifier.width(400.dp))
            }
        }
        val initial = rule.onNodeWithTag("graph-node-root").fetchSemanticsNode().boundsInRoot.width
        rule.onNodeWithTag("graph-zoom-in").performClick()
        assertTrue(rule.onNodeWithTag("graph-node-root").fetchSemanticsNode().boundsInRoot.width > initial)
        current.value = item.copy(key = "another-graph")
        rule.waitForIdle()
        assertEquals(initial, rule.onNodeWithTag("graph-node-root").fetchSemanticsNode().boundsInRoot.width, 1f)
    }

    @Test fun zoomButtonsStopAtTwentyFiveAndTwoHundredPercent() {
        mount(item)
        repeat(8) { rule.onNodeWithTag("graph-zoom-in").performClick() }
        rule.onNodeWithTag("graph-zoom-in").assertIsNotEnabled()
        repeat(10) { rule.onNodeWithTag("graph-zoom-out").performClick() }
        rule.onNodeWithTag("graph-zoom-out").assertIsNotEnabled()
        rule.onNodeWithTag("graph-fit").performClick()
        rule.onNodeWithTag("graph-zoom-in").assertIsEnabled()
        rule.onNodeWithTag("graph-zoom-out").assertIsEnabled()
    }

    @Test fun accessibilityUsesSharedReadingOrderInsteadOfDrawingOrder() {
        val readingItem = item.copy(
            nodes = listOf(
                ExtensionGraphNode(id = "root", title = "Root"),
                ExtensionGraphNode(id = "first", title = "First"),
                ExtensionGraphNode(id = "second", title = "Second"),
                ExtensionGraphNode(id = "grand", title = "Grandchild"),
            ),
            edges = listOf(
                ExtensionGraphEdge("root", "first"),
                ExtensionGraphEdge("root", "second"),
                ExtensionGraphEdge("first", "grand"),
            ),
            selected = null,
        )
        mount(readingItem)
        val rootNode = rule.onNodeWithTag("graph-node-root").fetchSemanticsNode()
        val canvas = generateSequence(rootNode.parent) { it.parent }.firstOrNull {
            it.config.getOrNull(SemanticsProperties.IsTraversalGroup) == true
        }
        assertTrue("the canvas is an accessibility traversal group", canvas != null)
        val expected = mapOf("root" to 0f, "first" to 2f, "grand" to 4f, "second" to 6f)
        expected.forEach { (id, index) ->
            val actual = rule.onNodeWithTag("graph-node-$id").fetchSemanticsNode().config[SemanticsProperties.TraversalIndex]
            assertEquals("shared reading order for $id", index, actual)
        }
        assertEquals(1f, rule.onNodeWithTag("graph-fold-root").fetchSemanticsNode().config[SemanticsProperties.TraversalIndex])
        assertEquals(3f, rule.onNodeWithTag("graph-fold-first").fetchSemanticsNode().config[SemanticsProperties.TraversalIndex])
        assertEquals(8f, rule.onNodeWithTag("graph-zoom-out").fetchSemanticsNode().config[SemanticsProperties.TraversalIndex])
    }

    @Test fun downwardFoldVisualStaysOutsideNodeAndProgress() {
        mount(item.copy(direction = ExtensionGraphDirection.DOWN))
        val node = rule.onNodeWithTag("graph-node-root").fetchSemanticsNode().boundsInRoot
        val visual = rule.onNodeWithTag("graph-fold-visual-root", useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
        val child = rule.onNodeWithTag("graph-node-design").fetchSemanticsNode().boundsInRoot
        assertTrue("downward fold stays below the node: $node / $visual", visual.top >= node.bottom - 1f)
        assertTrue("downward fold stays above the next row: $child / $visual", visual.bottom <= child.top + 1f)
    }

    @Test fun compactPreviewFitsEvenAThirtyNodeChainWithoutAZoomFloor() {
        val chain = (0 until 30).map { ExtensionGraphNode(id = "n$it", title = "Node $it") }
        val graph = item.copy(
            nodes = chain,
            edges = (1 until chain.size).map { ExtensionGraphEdge("n${it - 1}", "n$it") },
            selected = null,
        )
        mount(graph, compact = true)
        val preview = rule.onNodeWithContentDescription("Open graph full screen").fetchSemanticsNode().boundsInRoot
        chain.forEach { node ->
            val bounds = rule.onNodeWithTag("graph-node-${node.id}").fetchSemanticsNode().boundsInRoot
            assertTrue("${node.id} is clipped: $bounds not in $preview",
                bounds.left >= preview.left - 1f && bounds.top >= preview.top - 1f &&
                    bounds.right <= preview.right + 1f && bounds.bottom <= preview.bottom + 1f)
        }
    }

    @Test fun inPlaceCardHostUsesTheStillPreviewPath() {
        val view = ExtensionView(sections = listOf(ExtensionViewSection(items = listOf(item))))
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                ExtensionViewColumn(ExtensionViewState(view = view), ExtensionViewHandle(
                    press = { _, _ -> }, edit = { _, _ -> }, choose = {}, open = {},
                ))
            }
        }
        rule.onNodeWithTag("graph-preview").assertIsDisplayed()
        rule.onNodeWithTag("graph-node-root").assertHasNoClickAction()
        rule.onAllNodesWithTag("graph-zoom-in").assertCountEquals(0)
        rule.onAllNodesWithTag("graph-fold-root").assertCountEquals(0)
        rule.onNodeWithContentDescription("Open graph full screen").assertHasClickAction()
    }

    @Test fun foldControlStaysAboveNeighbourAtMinimumZoom() {
        mount(item)
        repeat(10) { rule.onNodeWithTag("graph-zoom-out").performClick() }
        rule.onNodeWithTag("graph-zoom-out").assertIsNotEnabled()
        rule.onNodeWithTag("graph-fold-root").performClick()
        rule.onNodeWithTag("graph-node-design").assertDoesNotExist()
        rule.onNodeWithTag("graph-node-build").assertDoesNotExist()
        rule.onNodeWithContentDescription("Show 2 hidden").assertExists()
    }

    @Test fun downwardFoldClearsGroupCaptionAtMinimumZoom() {
        mount(item.copy(direction = ExtensionGraphDirection.DOWN))
        repeat(10) { rule.onNodeWithTag("graph-zoom-out").performClick() }
        val visual = rule.onNodeWithTag("graph-fold-visual-root", useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
        val label = rule.onNodeWithTag("graph-frame-label-0", useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
        assertTrue("fold visual clears the group caption: $visual / $label", visual.bottom <= label.top + 1f)
    }

    @Config(qualifiers = "w400dp-h1000dp-mdpi", fontScale = 0.85f)
    @Test fun downwardFoldClearsGroupCaptionWithSmallTextAtMinimumZoom() {
        mount(item.copy(direction = ExtensionGraphDirection.DOWN))
        repeat(10) { rule.onNodeWithTag("graph-zoom-out").performClick() }
        val visual = rule.onNodeWithTag("graph-fold-visual-root", useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
        val label = rule.onNodeWithTag("graph-frame-label-0", useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
        assertTrue("small-text fold visual clears the group caption: $visual / $label", visual.bottom <= label.top + 1f)
    }

    @Test fun compactFullScreenStartsWithInteractiveOpeningZoom() {
        val chain = (0 until 30).map { ExtensionGraphNode(id = "n$it", title = "Node $it") }
        val graph = item.copy(
            nodes = chain,
            edges = (1 until chain.size).map { ExtensionGraphEdge("n${it - 1}", "n$it") },
            selected = "n15",
        )
        mount(graph, compact = true)
        rule.onNodeWithContentDescription("Open graph full screen").performClick()
        val inDialog = hasTestTag("graph-node-n15") and hasAnyAncestor(isDialog())
        val zoomOut = hasTestTag("graph-zoom-out") and hasAnyAncestor(isDialog())
        val zoomIn = hasTestTag("graph-zoom-in") and hasAnyAncestor(isDialog())
        rule.onNode(zoomOut).assertIsEnabled()
        val before = rule.onNode(inDialog).fetchSemanticsNode().boundsInRoot.width
        rule.onNode(zoomIn).performClick()
        val after = rule.onNode(inDialog).fetchSemanticsNode().boundsInRoot.width
        assertEquals("one zoom step, not a jump from thumbnail scale", before * 1.25f, after, 1f)
    }

    @Test fun lazyCardHostUsesTheStillPreviewPath() {
        val view = ExtensionView(sections = listOf(ExtensionViewSection(items = listOf(item))))
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                LazyColumn {
                    extensionViewItems("machine", ExtensionViewState(view = view), ExtensionViewHandle(
                        press = { _, _ -> }, edit = { _, _ -> }, choose = {}, open = {},
                    ), compactPreview = true)
                }
            }
        }
        rule.onNodeWithTag("graph-preview").assertIsDisplayed()
        rule.onNodeWithTag("graph-node-root").assertHasNoClickAction()
        rule.onAllNodesWithTag("graph-zoom-in").assertCountEquals(0)
        rule.onAllNodesWithTag("graph-fold-root").assertCountEquals(0)
    }

    private fun mount(
        graph: ExtensionViewItem,
        compact: Boolean = false,
        press: (ExtensionViewItem, persona.core.contracts.api.ExtensionViewAction) -> Unit = { _, _ -> },
    ) {
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                ExtensionGraph(
                    graph, ExtensionViewState(), ExtensionViewHandle(
                        press = press, edit = { _, _ -> }, choose = {}, open = {},
                    ),
                    Modifier.width(400.dp).testTag("fixture"), compact = compact,
                )
            }
        }
    }
}
