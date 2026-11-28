package persona.feature.settings

import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.gestures.rememberTransformableState
import androidx.compose.foundation.gestures.transformable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.sizeIn
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.clipToBounds
import androidx.compose.ui.layout.Layout
import androidx.compose.ui.layout.onSizeChanged
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.CustomAccessibilityAction
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.customActions
import androidx.compose.ui.semantics.isTraversalGroup
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.traversalIndex
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Constraints
import androidx.compose.ui.unit.IntRect
import androidx.compose.ui.unit.IntSize
import androidx.compose.ui.unit.dp
import persona.core.contracts.api.ExtensionViewAction
import persona.core.contracts.api.ExtensionViewItem
import persona.core.contracts.generated.ExtensionScenePin
import persona.core.contracts.generated.ExtensionSceneWarmth
import persona.core.designsystem.GraphIcons
import persona.core.designsystem.GraphTokens
import persona.core.designsystem.JFullScreen
import persona.core.designsystem.JIcon
import persona.core.designsystem.JIconButton
import persona.core.designsystem.JTopBar
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import persona.core.designsystem.Tone
import persona.core.designsystem.hue
import persona.core.designsystem.tone
import kotlin.math.abs
import kotlin.math.roundToInt

private const val OrbitDegreesPerDp = 0.45
private const val OrbitStep = 15.0
private const val ZoomStep = 1.25

private data class Anchored<T>(val position: SceneVector, val item: T)

private fun scenePosition(at: List<Double>, height: Double?): SceneVector? =
    ScenePoint.from(at)?.atHeight(height?.takeIf(Double::isFinite) ?: 0.0)

@Composable
internal fun ExtensionScene(
    item: ExtensionViewItem,
    state: ExtensionViewState,
    handle: ExtensionViewHandle,
    modifier: Modifier = Modifier,
    compact: Boolean = false,
) {
    val mesh = remember(item.shapes) { SceneMesh.from(item.shapes.orEmpty()) }
    if (mesh == null) {
        if (item.poster != null) {
            ExtensionImage(item.copy(type = "image", src = item.poster, fit = "fit"), handle, modifier, compact)
        } else {
            Text(item.alt.orEmpty(), modifier, style = PersonaTheme.type.body, color = PersonaTheme.palette.ink)
        }
        return
    }
    val initial = remember(item.camera) { SceneCamera.from(item.camera) }
    var savedCamera by rememberSaveable(item.key) { mutableStateOf<ArrayList<Double>?>(null) }
    val camera = savedCamera?.let { SceneCamera(it[0], it[1], it[2]) } ?: initial
    val adjust: (SceneCamera) -> Unit = { savedCamera = arrayListOf(it.yaw, it.pitch, it.zoom) }
    var fullScreen by rememberSaveable(item.key) { mutableStateOf(false) }
    val labels = remember(item.labels) {
        item.labels.orEmpty().take(32).mapNotNull { label ->
            scenePosition(label.at, label.height)?.let { Anchored(it, label.text) }
        }
    }
    val pins = remember(item.pins) {
        item.pins.orEmpty().take(100).mapNotNull { pin ->
            scenePosition(pin.at, pin.height)?.let { Anchored(it, pin) }
        }
    }
    val lamps = remember(pins) {
        pins.asSequence().filter { it.item.light?.on == true }.take(16)
            .map { SceneLamp(it.position, it.item.light?.warmth == ExtensionSceneWarmth.COOL) }.toList()
    }
    val onPin: (ExtensionScenePin, ExtensionViewAction) -> Unit = { pin, action ->
        if (fullScreen && (!action.confirm.isNullOrBlank() || action.push != null || action.open != null || action.pop)) {
            fullScreen = false
        }
        handle.press(ExtensionViewItem(type = "row", title = pin.label, key = "${item.key.orEmpty()}/${pin.id}"), action)
    }
    val theme = PersonaTheme
    val height = (LocalConfiguration.current.screenHeightDp.dp * 0.64f).coerceIn(GraphTokens.pageMinHeight, GraphTokens.pageMaxHeight)
    val openLabel = stringResource(R.string.settings_ext_graph_full_screen)
    val title = item.alt?.takeIf(String::isNotBlank) ?: stringResource(R.string.settings_ext_scene)
    val caption = item.caption?.takeIf(String::isNotBlank)
    Column(modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
        val canvasModifier = Modifier.fillMaxWidth().height(if (compact) GraphTokens.compactHeight else height)
            .clip(theme.shapes.cardShape)
            .then(if (compact) Modifier.clickable(role = Role.Button, onClickLabel = openLabel) { fullScreen = true } else Modifier)
        SceneViewport(
            title, mesh, if (compact) initial else camera, labels, pins, lamps, !compact && !fullScreen,
            !state.busy, adjust, onPin, canvasModifier, compact,
        )
        caption?.let { Text(it, style = theme.type.caption, color = theme.palette.ink2) }
        if (!compact) {
            SceneControls(camera, initial, adjust, { savedCamera = null }, { fullScreen = true })
        }
    }
    if (fullScreen) JFullScreen(onDismiss = { fullScreen = false }) {
        var previewCamera by rememberSaveable(item.key) { mutableStateOf<ArrayList<Double>?>(null) }
        val fullCamera = if (compact) previewCamera?.let { SceneCamera(it[0], it[1], it[2]) } ?: initial else camera
        val adjustFull: (SceneCamera) -> Unit = if (compact) {
            { previewCamera = arrayListOf(it.yaw, it.pitch, it.zoom) }
        } else adjust
        val resetFull = { if (compact) previewCamera = null else savedCamera = null }
        Column(Modifier.fillMaxSize().testTag("scene-full-screen")) {
            JTopBar(title, onBack = { fullScreen = false })
            SceneViewport(title, mesh, fullCamera, labels, pins, lamps, true, !state.busy, adjustFull, onPin,
                Modifier.weight(1f).fillMaxWidth())
            SceneControls(fullCamera, initial, adjustFull, resetFull, null)
        }
    }
}

@Composable
private fun SceneViewport(
    title: String,
    mesh: SceneMesh,
    camera: SceneCamera,
    labels: List<Anchored<String>>,
    pins: List<Anchored<ExtensionScenePin>>,
    lamps: List<SceneLamp>,
    interactive: Boolean,
    allowPinActions: Boolean,
    onCamera: (SceneCamera) -> Unit,
    onPin: (ExtensionScenePin, ExtensionViewAction) -> Unit,
    modifier: Modifier,
    compact: Boolean = false,
) {
    val palette = PersonaTheme.palette
    val density = LocalDensity.current
    var viewport by remember { mutableStateOf(IntSize.Zero) }
    val padding = with(density) { GraphTokens.viewportPadding.toPx() }
    val edgeWidth = with(density) { Tokens.Size.line.toPx() }
    val projection = remember(mesh.bounds, camera, viewport, padding) {
        SceneProjection.fit(camera, mesh.bounds, viewport.width.toFloat(), viewport.height.toFloat(), padding)
    }
    val rotation = rememberTransformableState { zoom, pan, _ ->
        if (interactive) {
            val degreePerPixel = OrbitDegreesPerDp / density.density
            onCamera(camera.orbit(-pan.x * degreePerPixel, pan.y * degreePerPixel).zoomBy(zoom.toDouble()))
        }
    }
    val left = stringResource(R.string.settings_ext_scene_turn_left)
    val right = stringResource(R.string.settings_ext_scene_turn_right)
    val tiltUp = stringResource(R.string.settings_ext_scene_tilt_up)
    val tiltDown = stringResource(R.string.settings_ext_scene_tilt_down)
    val gestures = if (interactive) Modifier.transformable(rotation, canPan = { true }) else Modifier
    Box(modifier.clipToBounds().background(palette.surfaceOpaque).then(gestures)
        .onSizeChanged { viewport = it }.semantics { isTraversalGroup = true }.testTag("extension-scene")) {
        Canvas(Modifier.fillMaxSize().semantics {
            contentDescription = title
            traversalIndex = -1f
            if (interactive) customActions = listOf(
                CustomAccessibilityAction(left) { onCamera(camera.orbit(OrbitStep, 0.0)); true },
                CustomAccessibilityAction(right) { onCamera(camera.orbit(-OrbitStep, 0.0)); true },
                CustomAccessibilityAction(tiltUp) { onCamera(camera.orbit(0.0, OrbitStep)); true },
                CustomAccessibilityAction(tiltDown) { onCamera(camera.orbit(0.0, -OrbitStep)); true },
            )
        }) { drawExtensionScene(mesh, projection, lamps, palette.dark, edgeWidth) }
        val anchors = remember(labels, pins, projection) { (labels.map { it.position } + pins.map { it.position }).map(projection::position) }
        Layout(
            modifier = Modifier.fillMaxSize(),
            content = {
                labels.forEachIndexed { index, label ->
                    Text(label.item, Modifier.clip(PersonaTheme.shapes.controlShape).background(palette.surfaceOpaque)
                        .padding(horizontal = Tokens.Space.xs, vertical = Tokens.Space.xxs)
                        .semantics { traversalIndex = index.toFloat() }, style = PersonaTheme.type.caption,
                        color = palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
                }
                pins.forEachIndexed { index, pin ->
                    ScenePin(pin.item, labels.size + index, interactive && allowPinActions, onPin)
                }
            },
        ) { children, limits ->
            val labelLimit = (limits.maxWidth / 2).coerceAtLeast(Tokens.MinTouch.android.roundToPx())
                .coerceAtMost(limits.maxWidth)
            val measured = children.map { it.measure(Constraints(maxWidth = labelLimit, maxHeight = limits.maxHeight)) }
            layout(limits.maxWidth, limits.maxHeight) {
                // Give pin readouts room before adding the floor labels in the shorter preview.
                val order = if (compact) (labels.size until measured.size) + labels.indices else measured.indices
                val placed = if (compact) ArrayList<IntRect>(measured.size) else null
                for (index in order) {
                    val child = measured[index]
                    val at = anchors[index]
                    val x = (at.x.roundToInt() - child.width / 2).coerceIn(0, (limits.maxWidth - child.width).coerceAtLeast(0))
                    val rightEdge = x + child.width
                    val maxTop = (limits.maxHeight - child.height).coerceAtLeast(0)
                    val desired = (at.y.roundToInt() - child.height / 2).coerceIn(0, maxTop)
                    val blockers = placed?.filter { it.left < rightEdge && it.right > x }
                    val y = if (blockers.isNullOrEmpty()) desired else nearestFreeTop(desired, maxTop, child.height, blockers)
                    child.place(x, y)
                    placed?.add(IntRect(x, y, rightEdge, y + child.height))
                }
            }
        }
    }
}

private fun nearestFreeTop(desired: Int, maxTop: Int, height: Int, blockers: List<IntRect>): Int {
    var best = desired
    var distance = Int.MAX_VALUE
    var start = 0
    fun consider(end: Int) {
        val upper = minOf(maxTop, end - height)
        if (start > upper) return
        val position = desired.coerceIn(start, upper)
        val offset = abs(desired - position)
        if (offset < distance) {
            best = position
            distance = offset
        }
    }
    for (rect in blockers.sortedBy(IntRect::top)) {
        consider(rect.top)
        start = maxOf(start, rect.bottom)
    }
    consider(maxTop + height)
    return best
}

@Composable
private fun ScenePin(
    pin: ExtensionScenePin,
    order: Int,
    canAct: Boolean,
    onPin: (ExtensionScenePin, ExtensionViewAction) -> Unit,
) {
    val palette = PersonaTheme.palette
    val tone = ExtensionViewText.tone(pin.tone)
    val indicator = ExtensionViewText.hue(pin.color)?.let { palette.hue(it).text }
        ?: if (tone == Tone.Neutral) palette.ink2 else palette.tone(tone).text
    val state = pin.state?.takeIf(String::isNotBlank)
    val readout = listOfNotNull(pin.label, state).joinToString(", ")
    // A confirm-only action is valid too: confirmation precedes the named action, and a blank id with nonblank
    // confirm reaches the same confirmation dialog as ordinary row actions (review P2).
    val action = pin.action?.toViewAction()?.takeIf {
        it.id.isNotBlank() || !it.confirm.isNullOrBlank() || it.push != null || it.open != null || it.pop
    }
    val click = if (canAct && action != null) Modifier.clickable(role = Role.Button) { onPin(pin, action) } else Modifier
    Box(Modifier.sizeIn(minWidth = Tokens.MinTouch.android, minHeight = Tokens.MinTouch.android)
        .semantics { traversalIndex = order.toFloat(); contentDescription = readout }
        .then(click).testTag("scene-pin-${pin.id}"), contentAlignment = Alignment.Center) {
        Row(Modifier.clip(CircleShape).background(palette.surfaceOpaque).border(Tokens.Size.line, palette.line, CircleShape)
            .padding(horizontal = Tokens.Space.s, vertical = Tokens.Space.xs).clearAndSetSemantics {},
            horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs), verticalAlignment = Alignment.CenterVertically) {
            if (pin.icon.isNullOrBlank()) Box(Modifier.size(Tokens.Space.xs).background(indicator, CircleShape))
            else JIcon(ExtensionIcons.of(pin.icon), null, tint = indicator, size = Tokens.Size.iconXS, decorative = true)
            Column {
                Text(pin.label, style = PersonaTheme.type.caption,
                    color = palette.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
                if (state != null) Text(state, style = PersonaTheme.type.micro,
                    color = palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
            }
        }
    }
}

@Composable
private fun SceneControls(
    camera: SceneCamera,
    initial: SceneCamera,
    onCamera: (SceneCamera) -> Unit,
    reset: () -> Unit,
    fullScreen: (() -> Unit)?,
) {
    Row(Modifier.fillMaxWidth().padding(horizontal = Tokens.Space.xs), horizontalArrangement = Arrangement.End) {
        JIconButton(GraphIcons.zoomOut, stringResource(R.string.settings_ext_graph_zoom_out),
            { onCamera(camera.zoomBy(1 / ZoomStep)) }, Modifier.testTag("scene-zoom-out"), enabled = camera.zoom > 0.5)
        JIconButton(GraphIcons.zoomIn, stringResource(R.string.settings_ext_graph_zoom_in),
            { onCamera(camera.zoomBy(ZoomStep)) }, Modifier.testTag("scene-zoom-in"), enabled = camera.zoom < 4.0)
        JIconButton(GraphIcons.fit, stringResource(R.string.settings_ext_scene_reset), reset,
            Modifier.testTag("scene-reset"), enabled = camera != initial)
        if (fullScreen != null) {
            JIconButton(GraphIcons.fullScreen, stringResource(R.string.settings_ext_graph_full_screen), fullScreen,
                Modifier.testTag("scene-open-full"))
        }
    }
}
