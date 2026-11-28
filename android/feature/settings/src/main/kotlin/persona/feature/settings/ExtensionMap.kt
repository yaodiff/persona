package persona.feature.settings

import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.gestures.detectTapGestures
import androidx.compose.foundation.gestures.rememberTransformableState
import androidx.compose.foundation.gestures.transformable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.aspectRatio
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clipToBounds
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.Path
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.selected
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.unit.IntSize
import persona.core.contracts.api.ExtensionViewItem
import persona.core.contracts.generated.ExtensionMapCoordinate
import persona.core.contracts.generated.ExtensionMapMarker
import persona.core.contracts.generated.ExtensionMapRoute
import persona.core.contracts.generated.ExtensionMapViewport
import persona.core.designsystem.JButton
import persona.core.designsystem.JButtonKind
import persona.core.designsystem.JFullScreen
import persona.core.designsystem.JRow
import persona.core.designsystem.JTopBar
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import persona.core.designsystem.Tone
import persona.core.designsystem.hue
import persona.core.designsystem.tone
import java.net.URI
import kotlin.math.abs

private fun ExtensionMapCoordinate.valid() = lat in -90.0..90.0 && lon in -180.0..180.0
private fun ExtensionMapMarker.point() = ExtensionMapCoordinate(lat, lon)
private fun longitudeDelta(value: Double, origin: Double) = (value - origin + 540.0) % 360.0 - 180.0

internal data class MapFrame(val center: ExtensionMapCoordinate, val spanLat: Double, val spanLon: Double) {
    fun position(point: ExtensionMapCoordinate, size: IntSize, zoom: Float = 1f, pan: Offset = Offset.Zero) = Offset(
        (size.width * (0.5 + zoom * longitudeDelta(point.lon, center.lon) / spanLon)).toFloat(),
        (size.height * (0.5 - zoom * (point.lat - center.lat) / spanLat)).toFloat(),
    ) + pan
}

internal fun mapFrame(points: List<ExtensionMapCoordinate>, viewport: ExtensionMapViewport?): MapFrame? {
    val valid = points.filter { it.valid() }
    val given = viewport?.center?.takeIf { it.valid() }
    val origin = given ?: valid.firstOrNull() ?: return null
    val offsets = valid.map { longitudeDelta(it.lon, origin.lon) }
    val lonMiddle = ((offsets.minOrNull() ?: 0.0) + (offsets.maxOrNull() ?: 0.0)) / 2
    val center = given ?: ExtensionMapCoordinate(
        (valid.minOf { it.lat } + valid.maxOf { it.lat }) / 2,
        longitudeDelta(origin.lon + lonMiddle, 0.0),
    )
    val latSpan = viewport?.spanLat?.takeIf { it.isFinite() && it > 0.0 }
        ?: ((valid.maxOfOrNull { abs(it.lat - center.lat) } ?: 0.0) * 2.4).coerceAtLeast(0.01)
    val lonSpan = viewport?.spanLon?.takeIf { it.isFinite() && it > 0.0 }
        ?: ((valid.maxOfOrNull { abs(longitudeDelta(it.lon, center.lon)) } ?: 0.0) * 2.4).coerceAtLeast(0.01)
    return MapFrame(center, latSpan.coerceIn(0.00001, 180.0), lonSpan.coerceIn(0.00001, 360.0))
}

internal fun mapExternalLink(url: String): String? = runCatching { URI(url) }.getOrNull()
    ?.takeIf { it.scheme == "https" && !it.host.isNullOrBlank() && it.rawUserInfo == null }?.toASCIIString()

internal fun mapAspect(aspect: String?) = when (aspect) {
    "square" -> 1f
    "landscape" -> 4f / 3f
    "wide" -> 16f / 9f
    "portrait" -> 3f / 4f
    else -> 4f / 3f
}

@Composable
internal fun ExtensionMap(item: ExtensionViewItem, state: ExtensionViewState, handle: ExtensionViewHandle,
                          modifier: Modifier = Modifier, compact: Boolean = false) {
    val markers = remember(item.markers) { item.markers.orEmpty().filter { it.point().valid() } }
    val routes = remember(item.routes) {
        item.routes.orEmpty().map { route -> route.copy(points = route.points.filter { it.valid() }) }.filter { it.points.isNotEmpty() }
    }
    val frame = remember(markers, routes, item.viewport) { mapFrame(markers.map { it.point() } + routes.flatMap { it.points }, item.viewport) }
    val staticMap = item.static == true
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    val title = item.title?.takeIf { it.isNotBlank() } ?: stringResource(R.string.settings_ext_map)
    val description = item.alt?.takeIf { it.isNotBlank() } ?: title
    var expanded by remember(item.key) { mutableStateOf(false) }
    var chosenRouteID by remember(item.key) { mutableStateOf<String?>(null) }
    var stopID by remember(item.key) { mutableStateOf<String?>(null) }
    var scale by remember(item.key) { mutableStateOf(1f) }
    var pan by remember(item.key) { mutableStateOf(Offset.Zero) }
    // A keyless map is reused by lazy lists and live updates. When its routes or markers are replaced, including by an
    // empty set, the previous full-screen view, route choice, stop, zoom and pan do not belong to the new map: start
    // from its declared camera again. A map that keeps or adds IDs (a live update) keeps the owner's interaction.
    val routeIDs = routes.map { it.id }
    val markerIDs = markers.map { it.id }
    var priorRouteIDs by remember(item.key) { mutableStateOf<List<String>>(emptyList()) }
    var priorMarkerIDs by remember(item.key) { mutableStateOf<List<String>>(emptyList()) }
    LaunchedEffect(item.key, routeIDs, markerIDs) {
        val routesReplaced = priorRouteIDs.isNotEmpty() &&
            (routeIDs.isEmpty() || routeIDs.none { it in priorRouteIDs })
        val markersReplaced = priorMarkerIDs.isNotEmpty() &&
            (markerIDs.isEmpty() || markerIDs.none { it in priorMarkerIDs })
        val replaced = routesReplaced || markersReplaced
        val chosenGone = chosenRouteID != null && chosenRouteID !in routeIDs
        val stopGone = stopID != null && stopID !in markerIDs
        if (replaced || chosenGone || stopGone) {
            if (replaced) expanded = false
            chosenRouteID = null
            stopID = null
            scale = 1f
            pan = Offset.Zero
        }
        priorRouteIDs = routeIDs
        priorMarkerIDs = markerIDs
    }
    val selectedRoute = routes.firstOrNull { it.id == chosenRouteID } ?: routes.firstOrNull { it.selected == true } ?: routes.firstOrNull()
    val routeID = selectedRoute?.id
    val markerColors = markers.map { marker ->
        ExtensionViewText.hue(marker.color)?.let(palette::hue)?.dot
            ?: ExtensionViewText.tone(marker.tone).takeIf { it != Tone.Neutral }?.let(palette::tone)?.dot
            ?: when (marker.kind) {
                "origin" -> palette.done.dot
                "destination" -> palette.needsYou.dot
                "waypoint" -> palette.ink2
                else -> palette.accent
            }
    }
    val routeColors = routes.map { route ->
        ExtensionViewText.hue(route.color)?.let(palette::hue)?.dot
            ?: ExtensionViewText.tone(route.tone).takeIf { it != Tone.Neutral }?.let(palette::tone)?.dot
            ?: palette.accent
    }
    val openLabel = stringResource(R.string.settings_ext_map_expand)
    val close = { expanded = false }

    Column(modifier.fillMaxWidth().testTag("extension-map"), verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
        item.title?.takeIf { it.isNotBlank() }?.let { Text(it, style = type.callout, color = palette.ink) }
        if (frame == null) Text(description, style = type.body, color = palette.ink2)
        else MapCanvas(frame, routes, routeColors, routeID, markers, markerColors, stopID, description,
            Modifier.fillMaxWidth().aspectRatio(mapAspect(item.aspect)).then(if (staticMap) Modifier else Modifier.clickable(
                role = Role.Button, onClickLabel = openLabel,
            ) { expanded = true }))
        selectedRoute?.let { Text(routeWords(it), style = type.caption, color = palette.ink2) }
        item.caption?.takeIf { it.isNotBlank() }?.let { Text(it, style = type.caption, color = palette.ink3) }
        if (item.showList == true || item.showList == null && !compact && (routes.size > 1 || markers.size > 1)) {
            val limit = if (compact && !staticMap) 3 else Int.MAX_VALUE
            MapRoutes(routes.take(limit), routeID, staticMap) { chosenRouteID = it }
            MapStops(item, markers.take(limit), stopID, state, handle, staticMap) { stopID = it }
        }
        MapLinks(item, handle)
    }

    if (expanded && !staticMap && frame != null) JFullScreen(onDismiss = close) {
        Column(Modifier.fillMaxSize().testTag("extension-map-full-screen")) {
            JTopBar(title, onBack = close)
            Column(Modifier.weight(1f).verticalScroll(rememberScrollState()).padding(Tokens.Space.l),
                verticalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
                val transform = rememberTransformableState { zoom, movement, _ ->
                    scale = (scale * zoom).coerceIn(1f, 16f)
                    pan += movement
                }
                val hit = with(LocalDensity.current) { Tokens.Space.l.toPx() }
                MapCanvas(frame, routes, routeColors, routeID, markers, markerColors, stopID, description,
                    Modifier.fillMaxWidth().aspectRatio(mapAspect(item.aspect)).transformable(transform)
                        .pointerInput(frame, markers, scale, pan) {
                            detectTapGestures { touch ->
                                markers.minByOrNull { (frame.position(it.point(), size, scale, pan) - touch).getDistance() }
                                    ?.takeIf { (frame.position(it.point(), size, scale, pan) - touch).getDistance() <= hit }
                                    ?.let { stopID = it.id }
                            }
                        }, scale, pan)
                Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                    JButton(stringResource(R.string.settings_ext_map_zoom_in), { scale = (scale * 2f).coerceAtMost(16f) },
                        kind = JButtonKind.Secondary, enabled = scale < 16f)
                    JButton(stringResource(R.string.settings_ext_map_zoom_out), { scale = (scale / 2f).coerceAtLeast(1f) },
                        kind = JButtonKind.Secondary, enabled = scale > 1f)
                    JButton(stringResource(R.string.settings_ext_map_reset), { scale = 1f; pan = Offset.Zero }, kind = JButtonKind.Secondary)
                }
                if (routes.size > 1 || item.showList != false) {
                    MapRoutes(routes, routeID, staticMap = false) { chosenRouteID = it }
                }
                val stops = if (item.showList == false) markers.filter { it.id == stopID } else markers
                MapStops(item, stops, stopID, state, handle, staticMap = false, beforeNavigation = close) { stopID = it }
                MapLinks(item, handle, beforeOpen = close)
            }
        }
    }
}

@Composable
private fun MapCanvas(frame: MapFrame, routes: List<ExtensionMapRoute>, routeColors: List<Color>, routeID: String?,
                      markers: List<ExtensionMapMarker>, markerColors: List<Color>, stopID: String?, description: String,
                      modifier: Modifier, zoom: Float = 1f, pan: Offset = Offset.Zero) {
    val palette = PersonaTheme.palette
    val pin = Tokens.Space.xs
    val ring = Tokens.Space.xxs
    Canvas(modifier.clipToBounds().background(palette.sunken).semantics { contentDescription = description }.testTag("extension-map-canvas")) {
        val bounds = IntSize(size.width.toInt(), size.height.toInt())
        fun project(point: ExtensionMapCoordinate) = frame.position(point, bounds, zoom, pan)
        for (i in 1..3) {
            val x = size.width * i / 4
            val y = size.height * i / 4
            drawLine(palette.line, Offset(x, 0f), Offset(x, size.height))
            drawLine(palette.line, Offset(0f, y), Offset(size.width, y))
        }
        fun drawRoute(route: ExtensionMapRoute, color: Color, active: Boolean) {
            val points = route.points
            if (points.size < 2) return
            val path = Path()
            val first = project(points.first())
            path.moveTo(first.x, first.y)
            for (index in 1 until points.size) {
                val pos = project(points[index])
                path.lineTo(pos.x, pos.y)
            }
            drawPath(path, if (active) color else color.copy(alpha = 0.55f),
                style = Stroke(width = if (active) pin.toPx() else ring.toPx()))
        }
        val selected = routes.indexOfFirst { it.id == routeID }
        routes.forEachIndexed { index, route -> if (index != selected) drawRoute(route, routeColors[index], false) }
        if (selected >= 0) drawRoute(routes[selected], routeColors[selected], true)
        markers.forEachIndexed { index, marker ->
            val pos = project(marker.point())
            val radius = if (marker.id == stopID) pin.toPx() + ring.toPx() else pin.toPx()
            drawCircle(palette.background, radius + ring.toPx(), pos)
            drawCircle(markerColors[index], radius, pos)
        }
    }
}

@Composable
private fun routeWords(route: ExtensionMapRoute): String = route.summary?.takeIf { it.isNotBlank() } ?: listOfNotNull(
    route.durationSeconds?.takeIf { it >= 0 }?.let { stringResource(R.string.settings_ext_map_minutes, (it + 59L) / 60L) },
    route.distanceMeters?.takeIf { it >= 0 }?.let {
        if (it < 1000) stringResource(R.string.settings_ext_map_meters, it)
        else stringResource(R.string.settings_ext_map_kilometers, PersonaViewBlocks.number(it / 1000.0))
    },
).joinToString(" · ")

@Composable
private fun MapRoutes(routes: List<ExtensionMapRoute>, routeID: String?, staticMap: Boolean, selectRoute: (String) -> Unit) {
    routes.forEachIndexed { position, route ->
        JRow(route.label?.takeIf { it.isNotBlank() } ?: stringResource(R.string.settings_ext_map_route, position + 1),
            modifier = Modifier.testTag("extension-map-route-${route.id}").semantics { selected = route.id == routeID },
            subtitle = routeWords(route).takeIf { it.isNotBlank() }, selected = route.id == routeID,
            onClick = if (staticMap) null else ({ selectRoute(route.id) }))
    }
}

@Composable
private fun MapStops(item: ExtensionViewItem, markers: List<ExtensionMapMarker>, stopID: String?, state: ExtensionViewState,
                     handle: ExtensionViewHandle, staticMap: Boolean, beforeNavigation: (() -> Unit)? = null,
                     selectStop: (String) -> Unit) {
    for (marker in markers) {
        val action = marker.action?.toViewAction()
        JRow(marker.label, subtitle = if (marker.id == stopID) marker.detail ?: marker.subtitle else marker.subtitle,
            leadingIcon = marker.icon?.takeIf { it.isNotBlank() }?.let(ExtensionIcons::of),
            selected = marker.id == stopID, enabled = !state.busy,
            onClick = if (staticMap) null else ({
                selectStop(marker.id)
                action?.let {
                    if (!it.confirm.isNullOrBlank() || it.push != null || it.open != null || it.pop) {
                        beforeNavigation?.invoke()
                    }
                    handle.press(item, it)
                }
            }))
    }
}

@Composable
private fun MapLinks(item: ExtensionViewItem, handle: ExtensionViewHandle, beforeOpen: (() -> Unit)? = null) {
    for (link in item.links.orEmpty()) {
        val url = mapExternalLink(link.url) ?: continue
        if (link.title.isNotBlank()) JRow(link.title, onClick = {
            beforeOpen?.invoke()
            handle.open(url)
        })
    }
}
