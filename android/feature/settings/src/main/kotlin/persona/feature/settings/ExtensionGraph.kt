package persona.feature.settings

import androidx.compose.foundation.Canvas
import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.gestures.detectTapGestures
import androidx.compose.foundation.gestures.rememberTransformableState
import androidx.compose.foundation.gestures.transformable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxScope
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.requiredSizeIn
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.Stable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableFloatStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.listSaver
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.clipToBounds
import androidx.compose.ui.geometry.CornerRadius
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Rect
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.Path
import androidx.compose.ui.graphics.TransformOrigin
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.graphics.drawscope.withTransform
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.layout.onSizeChanged
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.isTraversalGroup
import androidx.compose.ui.semantics.selected
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.traversalIndex
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.IntOffset
import androidx.compose.ui.unit.dp
import androidx.compose.ui.zIndex
import persona.core.contracts.api.ExtensionViewAction
import persona.core.contracts.api.ExtensionViewBadge
import persona.core.contracts.api.ExtensionViewItem
import persona.core.contracts.api.ExtensionViewPush
import persona.core.contracts.generated.ExtensionAction
import persona.core.contracts.generated.ExtensionGraphComponent
import persona.core.contracts.generated.ExtensionGraphDirection
import persona.core.contracts.generated.ExtensionGraphNode
import persona.core.designsystem.GraphIcons
import persona.core.designsystem.GraphTokens
import persona.core.designsystem.JCard
import persona.core.designsystem.JChangingText
import persona.core.designsystem.JFullScreen
import persona.core.designsystem.JIcon
import persona.core.designsystem.JIconButton
import persona.core.designsystem.JSpinner
import persona.core.designsystem.JTopBar
import persona.core.designsystem.LocalAppFeedback
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import persona.core.designsystem.Tone
import persona.core.designsystem.floatingSurface
import persona.core.designsystem.hue
import persona.core.designsystem.stableAlpha
import persona.core.designsystem.tone
import kotlin.math.max
import kotlin.math.min
import kotlin.math.roundToInt

private const val GraphMinZoom = 0.25f
private const val GraphMaxZoom = 2f
private const val GraphOpeningMinZoom = 0.5f
private const val GraphZoomStep = 1.25f

/** A transform in viewport pixels. Kept separate from the graph data so a state-only redraw cannot move the canvas. */
internal data class ExtensionGraphTransform(val scale: Float, val pan: Offset)

/** Fit the world, or take its opening view centred on [focus] / its first root. All values are viewport pixels. */
internal fun fitExtensionGraph(
    viewport: Size,
    world: Size,
    padding: Float,
    opening: Boolean,
    preview: Boolean = false,
    focus: Rect? = null,
    firstRoot: Rect? = null,
): ExtensionGraphTransform {
    if (viewport.width <= 0f || viewport.height <= 0f || world.width <= 0f || world.height <= 0f) {
        return ExtensionGraphTransform(1f, Offset.Zero)
    }
    val availableWidth = (viewport.width - 2 * padding).coerceAtLeast(1f)
    val availableHeight = (viewport.height - 2 * padding).coerceAtLeast(1f)
    val minimum = if (opening) GraphOpeningMinZoom else GraphMinZoom
    val fitted = min(availableWidth / world.width, availableHeight / world.height)
    // A still card always contains the whole graph, even a 300-node one; interactive canvases keep their zoom floors.
    val scale = if (preview) fitted.coerceAtMost(1f) else fitted.coerceIn(minimum, 1f)
    val pan = when {
        opening && focus != null -> Offset(
            viewport.width / 2 - focus.center.x * scale,
            viewport.height / 2 - focus.center.y * scale,
        )
        opening && firstRoot != null -> Offset(
            padding - firstRoot.left * scale,
            padding - firstRoot.top * scale,
        )
        else -> Offset(
            (viewport.width - world.width * scale) / 2,
            (viewport.height - world.height * scale) / 2,
        )
    }
    return ExtensionGraphTransform(scale, pan)
}

/** Keep a dragged or zoomed graph reachable; a graph smaller than the viewport stays centred on that axis. */
internal fun clampExtensionGraphPan(pan: Offset, scale: Float, viewport: Size, world: Size, padding: Float): Offset {
    fun axis(value: Float, viewportSize: Float, worldSize: Float): Float {
        val drawn = worldSize * scale
        if (drawn <= viewportSize - 2 * padding) return (viewportSize - drawn) / 2
        return value.coerceIn(viewportSize - padding - drawn, padding)
    }
    return Offset(axis(pan.x, viewport.width, world.width), axis(pan.y, viewport.height, world.height))
}

/** Zoom about the viewport centre, as [rememberTransformableState] supplies no pinch centroid. */
internal fun zoomExtensionGraph(
    current: ExtensionGraphTransform,
    factor: Float,
    viewport: Size,
    world: Size,
    padding: Float,
): ExtensionGraphTransform {
    val target = (current.scale * factor).coerceIn(GraphMinZoom, GraphMaxZoom)
    if (target == current.scale || viewport.width <= 0f || viewport.height <= 0f) return current
    val centre = Offset(viewport.width / 2, viewport.height / 2)
    val pan = centre + (current.pan - centre) * (target / current.scale)
    return ExtensionGraphTransform(target, clampExtensionGraphPan(pan, target, viewport, world, padding))
}

@Stable
private class ExtensionGraphUiState {
    var scale by mutableFloatStateOf(1f)
    var pan by mutableStateOf(Offset.Zero)
    var touched by mutableStateOf(false)
    var foldOn by mutableStateOf<List<String>>(emptyList())
    var foldOff by mutableStateOf<List<String>>(emptyList())

    fun transform(value: ExtensionGraphTransform, touched: Boolean = true) {
        scale = value.scale
        pan = value.pan
        if (touched) this.touched = true
    }

    companion object {
        val Saver = listSaver<ExtensionGraphUiState, Any>(
            save = { listOf(it.scale, it.pan.x, it.pan.y, it.touched, it.foldOn, it.foldOff) },
            restore = {
                ExtensionGraphUiState().apply {
                    scale = it[0] as Float
                    pan = Offset(it[1] as Float, it[2] as Float)
                    touched = it[3] as Boolean
                    @Suppress("UNCHECKED_CAST")
                    foldOn = it[4] as List<String>
                    @Suppress("UNCHECKED_CAST")
                    foldOff = it[5] as List<String>
                }
            },
        )
    }
}

private data class StructuralNode(val id: String, val group: String?)
private data class GraphStructure(
    val nodes: List<StructuralNode>,
    val edges: List<persona.core.contracts.generated.ExtensionGraphEdge>,
    val layout: String?,
    val direction: String?,
)

private fun ExtensionGraphComponent.structure() = GraphStructure(
    nodes.map { StructuralNode(it.id, it.group) }, edges.orEmpty(), layout, direction,
)

private data class GraphFrameGeometry(val title: String, val frame: Rect, val label: Rect)
private data class GraphGeometry(
    val nodes: Map<String, Rect>,
    val frames: List<GraphFrameGeometry>,
    val world: Size,
)

@Composable
private fun rememberGraphGeometry(layout: ExtensionGraphLayoutResult, direction: String?): GraphGeometry {
    val density = LocalDensity.current
    val fontScale = density.fontScale
    return remember(layout, direction, density, fontScale) {
        with(density) {
            val nodeWidth = GraphTokens.nodeWidth.toPx() * fontScale
            val nodeHeight = GraphTokens.nodeHeight.toPx() * fontScale
            val pitchX = nodeWidth + GraphTokens.horizontalGap.toPx()
            val framePadding = GraphTokens.framePadding.toPx()
            val titleHeight = GraphTokens.frameTitleHeight.toPx() * fontScale
            val verticalGap = if (direction == ExtensionGraphDirection.DOWN && layout.frames.isNotEmpty()) {
                // The unscaled fold chip must clear the group caption even at Fit's 25% floor and large text sizes.
                GraphTokens.foldVisual.toPx() * fontScale.coerceAtLeast(1f) / GraphMinZoom + titleHeight + 2 * framePadding
            } else GraphTokens.verticalGap.toPx()
            val pitchY = nodeHeight + verticalGap
            val inset = framePadding + titleHeight
            val nodes = layout.at.mapValues { (_, point) ->
                val left = inset + point.x.toFloat() * pitchX
                val top = inset + point.y.toFloat() * pitchY
                Rect(left, top, left + nodeWidth, top + nodeHeight)
            }
            val frames = layout.frames.mapNotNull { frame ->
                val members = frame.members.mapNotNull(nodes::get)
                if (members.isEmpty()) return@mapNotNull null
                val left = members.minOf { it.left } - framePadding
                val top = members.minOf { it.top } - framePadding - titleHeight
                val right = members.maxOf { it.right } + framePadding
                val bottom = members.maxOf { it.bottom } + framePadding
                GraphFrameGeometry(
                    frame.group,
                    Rect(left, top, right, bottom),
                    Rect(left + framePadding, top, right - framePadding, top + titleHeight),
                )
            }
            val right = (nodes.values.maxOfOrNull { it.right } ?: 0f)
                .coerceAtLeast(frames.maxOfOrNull { it.frame.right } ?: 0f) + inset
            val bottom = (nodes.values.maxOfOrNull { it.bottom } ?: 0f)
                .coerceAtLeast(frames.maxOfOrNull { it.frame.bottom } ?: 0f) + inset
            GraphGeometry(nodes, frames, if (nodes.isEmpty()) Size.Zero else Size(right, bottom))
        }
    }
}

private fun Modifier.graphPlacement(rect: Rect, state: ExtensionGraphUiState, density: Density): Modifier =
    size(with(density) { rect.width.toDp() }, with(density) { rect.height.toDp() }).graphicsLayer {
        translationX = state.pan.x + rect.left * state.scale
        translationY = state.pan.y + rect.top * state.scale
        scaleX = state.scale
        scaleY = state.scale
        transformOrigin = TransformOrigin(0f, 0f)
    }

/** UI version 10's graph, on a page or as a compact card's still preview. Both open the same full-screen canvas. */
@Composable
internal fun ExtensionGraph(
    item: ExtensionViewItem,
    state: ExtensionViewState,
    handle: ExtensionViewHandle,
    modifier: Modifier = Modifier,
    compact: Boolean = false,
) {
    val graph = remember(item) {
        ExtensionGraphComponent(
            key = item.key,
            nodes = item.nodes.orEmpty(),
            edges = item.edges,
            layout = item.layout,
            direction = item.direction,
            selected = item.selected,
            folded = item.folded,
        )
    }
    if (graph.nodes.isEmpty()) return
    val ui = rememberSaveable(graph.key, saver = ExtensionGraphUiState.Saver) { ExtensionGraphUiState() }
    var fullScreen by rememberSaveable(graph.key) { mutableStateOf(false) }
    // The full screen falls back to the work's first node (the canvas's own layout order), as iOS does, never a bare "Graph".
    val firstNodeTitle = remember(graph) {
        ExtensionGraphLayout.layout(graph).order.firstOrNull()?.let { id -> graph.nodes.firstOrNull { it.id == id }?.title }
            ?.takeIf { it.isNotBlank() }
    }
    val open = {
        // Pages keep exactly this position; a still preview takes a legal interactive opening view in the dialog.
        ui.touched = !compact
        fullScreen = true
    }
    val configuration = LocalConfiguration.current
    val pageHeight = (configuration.screenHeightDp.dp * 0.7f)
        .coerceIn(GraphTokens.pageMinHeight, GraphTokens.pageMaxHeight)
    ExtensionGraphCanvas(
        graph, state, handle, ui,
        modifier.fillMaxWidth().height(if (compact) GraphTokens.compactHeight else pageHeight),
        compact = compact,
        fullScreen = false,
        suspended = fullScreen,
        onFullScreen = open,
    )
    if (fullScreen) JFullScreen(onDismiss = {
        fullScreen = false
        if (compact) ui.touched = false
    }) {
        Column(Modifier.fillMaxSize().testTag("graph-full-screen")) {
            JTopBar(item.title?.takeIf { it.isNotBlank() } ?: firstNodeTitle ?: stringResource(R.string.settings_ext_graph), onBack = {
                fullScreen = false
                if (compact) ui.touched = false
            })
            ExtensionGraphCanvas(
                graph, state, handle, ui,
                Modifier.fillMaxWidth().weight(1f),
                compact = false,
                fullScreen = true,
                suspended = false,
                onFullScreen = {},
            )
        }
    }
}

@Composable
private fun ExtensionGraphCanvas(
    graph: ExtensionGraphComponent,
    viewState: ExtensionViewState,
    handle: ExtensionViewHandle,
    ui: ExtensionGraphUiState,
    modifier: Modifier,
    compact: Boolean,
    fullScreen: Boolean,
    suspended: Boolean,
    onFullScreen: () -> Unit,
) {
    val folded = remember(graph.folded, ui.foldOn, ui.foldOff) {
        graph.folded.orEmpty().toSet() - ui.foldOff.toSet() + ui.foldOn.toSet()
    }
    val structure = remember(graph) { graph.structure() }
    // Node state, colour and action changes do not re-run layout; only ids, groups, edges, direction and folds can move it.
    val layout = remember(structure, folded) {
        ExtensionGraphLayout.layout(ExtensionGraphComponent(
            nodes = structure.nodes.map { ExtensionGraphNode(id = it.id, group = it.group) },
            edges = structure.edges,
            layout = structure.layout,
            direction = structure.direction,
        ), folded)
    }
    val geometry = rememberGraphGeometry(layout, graph.direction)
    val density = LocalDensity.current
    val viewportPadding = with(density) { GraphTokens.viewportPadding.toPx() }
    var viewport by remember { mutableStateOf(Size.Zero) }
    val selected = graph.selected?.let(geometry.nodes::get)
    val firstRoot = layout.order.firstOrNull { layout.depth[it] == 0 }?.let(geometry.nodes::get)
    LaunchedEffect(viewport, geometry.world, selected, firstRoot, compact, fullScreen, suspended, ui, ui.touched) {
        if (!suspended && !ui.touched) ui.transform(
            fitExtensionGraph(
                viewport, geometry.world, viewportPadding,
                opening = !compact, preview = compact, focus = selected, firstRoot = firstRoot,
            ),
            touched = false,
        )
    }
    fun fit() {
        ui.transform(fitExtensionGraph(viewport, geometry.world, viewportPadding, opening = false))
    }
    fun zoom(factor: Float) {
        ui.transform(zoomExtensionGraph(ExtensionGraphTransform(ui.scale, ui.pan), factor, viewport, geometry.world, viewportPadding))
    }
    val gestures = rememberTransformableState { zoomChange, panChange, _ ->
        if (!compact) {
            val transformed = zoomExtensionGraph(
                ExtensionGraphTransform(ui.scale, ui.pan), zoomChange, viewport, geometry.world, viewportPadding,
            )
            ui.touched = true
            ui.scale = transformed.scale
            ui.pan = clampExtensionGraphPan(
                transformed.pan + panChange, transformed.scale, viewport, geometry.world, viewportPadding,
            )
        }
    }
    val openLabel = stringResource(R.string.settings_ext_graph_open)
    val interactive = !compact
    Box(
        modifier
            .onSizeChanged { viewport = Size(it.width.toFloat(), it.height.toFloat()) }
            .clipToBounds()
            .background(PersonaTheme.palette.background)
            .then(if (interactive) Modifier.transformable(gestures) else Modifier)
            .then(if (interactive) Modifier.semantics { isTraversalGroup = true } else Modifier)
            .then(if (compact) Modifier.clickable(role = Role.Button, onClick = onFullScreen)
                .semantics(mergeDescendants = true) { contentDescription = openLabel } else Modifier)
            .testTag(if (compact) "graph-preview" else "graph-canvas"),
    ) {
        GraphLines(graph, layout, geometry, ui, Modifier.fillMaxSize())
        // Frame labels and nodes share the transform, but are composed in the shared reading order.
        geometry.frames.forEachIndexed { index, frame ->
            Box(Modifier.graphPlacement(frame.label, ui, density).zIndex(1f).testTag("graph-frame-label-$index")) {
                Text(
                    frame.title,
                    Modifier.padding(start = GraphTokens.frameTitleInset).clearAndSetSemantics {},
                    style = PersonaTheme.type.caption,
                    color = PersonaTheme.palette.ink3,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                )
            }
        }
        val nodes = remember(graph.nodes) { graph.nodes.associateBy { it.id } }
        layout.order.forEachIndexed { index, id ->
            val node = nodes[id] ?: return@forEachIndexed
            val rect = geometry.nodes[id] ?: return@forEachIndexed
            GraphNode(
                graph, node, viewState, handle, ui,
                interactive = interactive,
                selected = graph.selected == id,
                readingIndex = index * 2f,
                modifier = Modifier.graphPlacement(rect, ui, density).zIndex(2f),
            )
            if (id in layout.folds && interactive) {
                GraphFold(
                    id = id,
                    rect = rect,
                    right = graph.direction.isNullOrEmpty() || graph.direction == ExtensionGraphDirection.RIGHT,
                    folded = id in folded,
                    hidden = layout.folds.getValue(id),
                    state = ui,
                    readingIndex = index * 2f + 1f,
                    onFold = {
                        if (id in folded) {
                            ui.foldOn = ui.foldOn - id
                            ui.foldOff = (ui.foldOff + id).distinct()
                        } else {
                            ui.foldOff = ui.foldOff - id
                            ui.foldOn = (ui.foldOn + id).distinct()
                        }
                    },
                    modifier = Modifier.zIndex(3f),
                )
            }
        }
        if (interactive) GraphControls(ui, fullScreen, onFullScreen, layout.order.size * 2, ::fit, ::zoom)
    }
}

@Composable
private fun BoxScope.GraphControls(
    state: ExtensionGraphUiState,
    fullScreen: Boolean,
    onFullScreen: () -> Unit,
    firstReadingIndex: Int,
    fit: () -> Unit,
    zoom: (Float) -> Unit,
) {
    // Only this small group recomposes as the zoom changes; the canvas' nodes stay in their draw/placement phases.
    Row(
        Modifier.align(Alignment.BottomEnd)
            .padding(GraphTokens.controlsPadding)
            .floatingSurface(PersonaTheme.shapes.controlShape)
            .zIndex(4f),
    ) {
        JIconButton(GraphIcons.zoomOut, stringResource(R.string.settings_ext_graph_zoom_out), { zoom(1f / GraphZoomStep) },
            Modifier.testTag("graph-zoom-out").semantics { traversalIndex = firstReadingIndex.toFloat() },
            enabled = state.scale > GraphMinZoom, raised = false)
        JIconButton(GraphIcons.zoomIn, stringResource(R.string.settings_ext_graph_zoom_in), { zoom(GraphZoomStep) },
            Modifier.testTag("graph-zoom-in").semantics { traversalIndex = (firstReadingIndex + 1).toFloat() },
            enabled = state.scale < GraphMaxZoom, raised = false)
        JIconButton(GraphIcons.fit, stringResource(R.string.settings_ext_graph_fit), fit,
            Modifier.testTag("graph-fit").semantics { traversalIndex = (firstReadingIndex + 2).toFloat() }, raised = false)
        if (!fullScreen) JIconButton(GraphIcons.fullScreen, stringResource(R.string.settings_ext_graph_full_screen), onFullScreen,
            Modifier.testTag("graph-open-full").semantics { traversalIndex = (firstReadingIndex + 3).toFloat() }, raised = false)
    }
}

@Composable
private fun GraphLines(
    graph: ExtensionGraphComponent,
    layout: ExtensionGraphLayoutResult,
    geometry: GraphGeometry,
    ui: ExtensionGraphUiState,
    modifier: Modifier,
) {
    val palette = PersonaTheme.palette
    val edgeWidth = with(LocalDensity.current) { GraphTokens.edgeWidth.toPx() }
    val bend = with(LocalDensity.current) { GraphTokens.edgeBend.toPx() }
    val radius = with(LocalDensity.current) { PersonaTheme.shapes.control.toPx() }
    val right = graph.direction.isNullOrEmpty() || graph.direction == ExtensionGraphDirection.RIGHT
    Canvas(modifier.clearAndSetSemantics {}) {
        withTransform({
            translate(ui.pan.x, ui.pan.y)
            scale(ui.scale, ui.scale, Offset.Zero)
        }) {
            geometry.frames.forEach { frame ->
                drawRoundRect(palette.sunken, frame.frame.topLeft, frame.frame.size, CornerRadius(radius, radius))
            }
            layout.edges.forEach { edge ->
                val from = geometry.nodes[edge.from] ?: return@forEach
                val to = geometry.nodes[edge.to] ?: return@forEach
                val start: Offset
                val end: Offset
                val path = Path()
                if (right) {
                    start = Offset(from.right, from.center.y)
                    end = Offset(to.left, to.center.y)
                    val control = max(bend, (end.x - start.x) / 2)
                    path.moveTo(start.x, start.y)
                    path.cubicTo(start.x + control, start.y, end.x - control, end.y, end.x, end.y)
                } else {
                    start = Offset(from.center.x, from.bottom)
                    end = Offset(to.center.x, to.top)
                    val control = max(bend, (end.y - start.y) / 2)
                    path.moveTo(start.x, start.y)
                    path.cubicTo(start.x, start.y + control, end.x, end.y - control, end.x, end.y)
                }
                drawPath(path, palette.line, style = Stroke(edgeWidth))
            }
        }
    }
}

@OptIn(ExperimentalFoundationApi::class)
@Composable
private fun GraphNode(
    graph: ExtensionGraphComponent,
    node: ExtensionGraphNode,
    viewState: ExtensionViewState,
    handle: ExtensionViewHandle,
    ui: ExtensionGraphUiState,
    interactive: Boolean,
    selected: Boolean,
    readingIndex: Float,
    modifier: Modifier,
) {
    val palette = PersonaTheme.palette
    val hue = ExtensionViewText.hue(node.color)
    val tone = ExtensionViewText.tone(node.tone).takeIf { it != Tone.Neutral }
    val role = hue?.let(palette::hue)?.dot ?: tone?.let(palette::tone)?.dot
    val action = node.action?.toViewAction()?.takeIf {
        it.id.isNotEmpty() || it.push != null || it.open != null || it.pop
    }
    val parent = graph.edges.orEmpty().firstOrNull { it.to == node.id }?.from?.let { parentId ->
        graph.nodes.firstOrNull { it.id == parentId }?.title
    }
    val readout = graphNodeReadout(node, parent)
    val feedback = LocalAppFeedback.current
    val press: (() -> Unit)? = if (interactive && action != null && !viewState.busy) ({
        handle.press(ExtensionViewItem(type = "row", title = node.title, key = "${graph.key.orEmpty()}/${node.id}"), action)
    }) else null
    val showReadout = { feedback?.notice(readout); Unit }
    val longPress: (() -> Unit)? = if (interactive && action != null) showReadout else null
    val contentModifier = modifier
        .testTag("graph-node-${node.id}")
        .then(if (interactive && action != null) Modifier.clip(PersonaTheme.shapes.cardShape).combinedClickable(
            role = Role.Button, onClick = press ?: {}, onLongClick = longPress,
        ) else Modifier)
        .then(if (interactive && action == null) Modifier.pointerInput(readout, feedback) {
            // An informational long press must not give an actionless node button semantics.
            detectTapGestures(onLongPress = { showReadout() })
        } else Modifier)
        .semantics(mergeDescendants = true) {
            if (interactive) {
                contentDescription = readout
                traversalIndex = readingIndex
                this.selected = selected
            }
        }
        .stableAlpha(if (interactive && viewState.busy && action != null) 0.45f else 1f)
    Box(contentModifier) {
        JCard(
            Modifier.fillMaxSize(),
            padding = Tokens.Space.flush,
        ) {
            Box(Modifier.fillMaxSize().clearAndSetSemantics {}) {
                if (role != null) Box(
                    Modifier.align(Alignment.CenterStart).fillMaxHeight().width(GraphTokens.leadingWidth).background(role),
                )
                Row(
                    Modifier.fillMaxSize().padding(
                        start = GraphTokens.contentPadding + GraphTokens.leadingWidth,
                        end = GraphTokens.contentPadding,
                        bottom = if (node.progress != null) GraphTokens.progressHeight else Tokens.Space.flush,
                    ),
                    verticalAlignment = Alignment.CenterVertically,
                ) {
                    Box(Modifier.size(GraphTokens.iconSlot), contentAlignment = Alignment.Center) {
                        val icon = node.icon?.takeIf { it.isNotBlank() }
                        if (icon != null) JIcon(ExtensionIcons.of(icon), null, size = GraphTokens.iconSlot,
                            tint = role ?: palette.ink2, decorative = true)
                        else Box(Modifier.size(GraphTokens.stateDot).clip(CircleShape).background(role ?: palette.ink3))
                    }
                    Spacer(Modifier.width(Tokens.Space.s))
                    Column(Modifier.weight(1f)) {
                        Row(verticalAlignment = Alignment.CenterVertically) {
                            JChangingText(node.title, style = PersonaTheme.type.bodyStrong, color = palette.ink,
                                modifier = Modifier.weight(1f), maxLines = 1, overflow = TextOverflow.Ellipsis)
                            if (viewState.acting == action?.id && action != null) JSpinner(Modifier.size(GraphTokens.iconSlot))
                            else node.badge?.takeIf { it.text.isNotBlank() }?.let {
                                Spacer(Modifier.width(Tokens.Space.xs))
                                ExtensionBadge(ExtensionViewBadge(it.text, it.tone, it.color))
                            }
                        }
                        node.subtitle?.takeIf { it.isNotBlank() }?.let {
                            JChangingText(it, style = PersonaTheme.type.caption, color = palette.ink2,
                                maxLines = 1, overflow = TextOverflow.Ellipsis)
                        }
                    }
                }
                node.progress?.let { progress ->
                    Box(Modifier.align(Alignment.BottomCenter).fillMaxWidth().height(GraphTokens.progressHeight)
                        .background(palette.surfaceStrong)) {
                        val ratio = (if (progress.isFinite()) progress else 0.0).toFloat().coerceIn(0f, 1f)
                        Box(Modifier.fillMaxWidth(ratio).fillMaxHeight()
                            .background(role ?: palette.accent))
                    }
                }
            }
        }
        if (selected) Box(
            Modifier.matchParentSize().border(GraphTokens.selectedWidth, palette.accent, PersonaTheme.shapes.cardShape),
        )
    }
}

@Composable
private fun GraphFold(
    id: String,
    rect: Rect,
    right: Boolean,
    folded: Boolean,
    hidden: Int,
    state: ExtensionGraphUiState,
    readingIndex: Float,
    onFold: () -> Unit,
    modifier: Modifier,
) {
    val label = if (folded) pluralStringResource(R.plurals.settings_ext_graph_show_hidden, hidden, hidden)
        else stringResource(R.string.settings_ext_graph_fold)
    val density = LocalDensity.current
    val textScale = density.fontScale.coerceAtLeast(1f)
    val target = Tokens.MinTouch.android * textScale
    val visual = GraphTokens.foldVisual * textScale
    val half = with(density) { (target / 2).toPx() }
    val visualHalf = with(density) { (visual / 2).toPx() }
    val anchor = if (right) Offset(rect.right, rect.center.y) else Offset(rect.center.x, rect.bottom)
    Box(
        modifier.offset {
            val at = state.pan + anchor * state.scale
            // The visible chip lives in the pitch gap, never over the node's badge, subtitle or progress.
            val centre = if (right) Offset(at.x + visualHalf, at.y) else Offset(at.x, at.y + visualHalf)
            IntOffset((centre.x - half).roundToInt(), (centre.y - half).roundToInt())
        }.size(target).testTag("graph-fold-$id").clickable(role = Role.Button, onClick = onFold)
            .semantics {
                contentDescription = label
                traversalIndex = readingIndex
            },
        contentAlignment = Alignment.Center,
    ) {
        Box(
            // The fixed 48 dp constraints belong to the hit target only. The visible chip keeps its 24 dp minimum and
            // may widen into a capsule for a folded descendant count instead of being coerced to the target's size.
            Modifier.requiredSizeIn(minWidth = visual, minHeight = visual).clip(CircleShape)
                .background(PersonaTheme.palette.surfaceOpaque)
                .border(Tokens.Size.line, PersonaTheme.palette.line, CircleShape)
                .clearAndSetSemantics { this[SemanticsProperties.TestTag] = "graph-fold-visual-$id" },
            contentAlignment = Alignment.Center,
        ) {
            if (folded) Text("+$hidden", style = PersonaTheme.type.micro.copy(fontWeight = FontWeight.SemiBold),
                color = PersonaTheme.palette.ink2, maxLines = 1)
            else JIcon(if (right) GraphIcons.foldRight else GraphIcons.foldDown, null,
                size = GraphTokens.foldIcon, tint = PersonaTheme.palette.ink2, decorative = true)
        }
    }
}

@Composable
private fun graphNodeReadout(node: ExtensionGraphNode, parent: String?): String {
    val parts = mutableListOf(node.title)
    node.badge?.text?.takeIf { it.isNotBlank() }?.let(parts::add)
    node.subtitle?.takeIf { it.isNotBlank() }?.let(parts::add)
    node.progress?.let { parts += stringResource(R.string.settings_ext_graph_progress, gaugePercent(it)) }
    parent?.takeIf { it.isNotBlank() }?.let { parts += stringResource(R.string.settings_ext_graph_under, it) }
    return parts.joinToString(", ")
}

internal fun ExtensionAction.toViewAction() = ExtensionViewAction(
    id = id,
    confirm = confirm,
    submit = submit == true,
    push = push?.let { ExtensionViewPush(state = it.state, title = it.title, ui = it.ui) },
    open = open,
    pop = pop == true,
    values = values,
)
