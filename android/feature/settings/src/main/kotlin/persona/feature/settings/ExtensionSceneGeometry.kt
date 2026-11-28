package persona.feature.settings

import persona.core.contracts.generated.ExtensionSceneBox
import persona.core.contracts.generated.ExtensionSceneCamera
import persona.core.contracts.generated.ExtensionSceneCylinder
import persona.core.contracts.generated.ExtensionSceneMaterial
import persona.core.contracts.generated.ExtensionScenePrism
import persona.core.contracts.generated.ExtensionSceneShape
import kotlin.math.PI
import kotlin.math.cos
import kotlin.math.hypot
import kotlin.math.min
import kotlin.math.sin

/** Rounded corners at or below this radius render square, matching the iOS geometry's negligible-radius cutoff. */
private const val NEGLIGIBLE_ROUND_METERS = 0.002

internal data class ScenePoint(val x: Double, val y: Double) {
    fun atHeight(height: Double) = SceneVector(x, y, height)

    companion object {
        fun from(coordinates: List<Double>): ScenePoint? {
            if (coordinates.size != 2 || coordinates.any { !it.isFinite() }) return null
            return ScenePoint(coordinates[0], coordinates[1])
        }
    }
}

/**
 * A material pattern's axes and bounds for one solid. Cap triangles share it so their seams line up after ear
 * clipping; a box keeps the axes of its own yaw instead of falling back to world x/y.
 */
internal data class SceneSurfacePattern(
    val origin: ScenePoint,
    val xAxis: ScenePoint,
    val yAxis: ScenePoint,
    val minX: Double,
    val maxX: Double,
    val minY: Double,
    val maxY: Double,
) {
    fun at(x: Double, y: Double, z: Double) = SceneVector(
        origin.x + xAxis.x * x + yAxis.x * y,
        origin.y + xAxis.y * x + yAxis.y * y,
        z,
    )

    companion object {
        fun box(center: ScenePoint, halfWidth: Double, halfDepth: Double, yawDegrees: Double): SceneSurfacePattern {
            val yaw = yawDegrees.radians()
            val cos = cos(yaw)
            val sin = sin(yaw)
            return SceneSurfacePattern(
                origin = center,
                xAxis = ScenePoint(cos, sin),
                yAxis = ScenePoint(-sin, cos),
                minX = -halfWidth,
                maxX = halfWidth,
                minY = -halfDepth,
                maxY = halfDepth,
            )
        }

        fun worldFootprint(points: List<ScenePoint>): SceneSurfacePattern {
            val minX = points.minOf { it.x }
            val maxX = points.maxOf { it.x }
            val minY = points.minOf { it.y }
            val maxY = points.maxOf { it.y }
            return SceneSurfacePattern(
                origin = ScenePoint(minX, minY),
                xAxis = ScenePoint(1.0, 0.0),
                yAxis = ScenePoint(0.0, 1.0),
                minX = 0.0,
                maxX = maxX - minX,
                minY = 0.0,
                maxY = maxY - minY,
            )
        }
    }
}

internal data class SceneVector(val x: Double, val y: Double, val z: Double) {
    operator fun minus(other: SceneVector) = SceneVector(x - other.x, y - other.y, z - other.z)
    fun dot(other: SceneVector) = x * other.x + y * other.y + z * other.z
}

internal data class SceneCamera(val yaw: Double, val pitch: Double, val zoom: Double) {
    fun orbit(yawDelta: Double, pitchDelta: Double) =
        SceneCamera(wrapYaw(yaw + yawDelta), (pitch + pitchDelta).coerceIn(MinPitch, MaxPitch), zoom)

    fun zoomBy(factor: Double) = copy(zoom = (zoom * factor).coerceIn(MinZoom, MaxZoom))

    companion object {
        private const val MinPitch = 10.0
        private const val MaxPitch = 90.0
        private const val MinZoom = 0.5
        private const val MaxZoom = 4.0

        fun from(camera: ExtensionSceneCamera?) = SceneCamera(
            wrapYaw(camera?.yaw?.takeIf(Double::isFinite) ?: 225.0),
            (camera?.pitch?.takeIf(Double::isFinite) ?: 35.0).coerceIn(MinPitch, MaxPitch),
            (camera?.zoom?.takeIf(Double::isFinite) ?: 1.0).coerceIn(MinZoom, MaxZoom),
        )

        private fun wrapYaw(angle: Double) = (angle % 360 + 360) % 360
    }
}

internal data class SceneBounds(val center: SceneVector, val corners: List<SceneVector>) {
    companion object {
        fun of(vertices: List<SceneVector>): SceneBounds {
            val xs = listOf(vertices.minOf { it.x }, vertices.maxOf { it.x })
            val ys = listOf(vertices.minOf { it.y }, vertices.maxOf { it.y })
            val zs = listOf(vertices.minOf { it.z }, vertices.maxOf { it.z })
            return SceneBounds(
                SceneVector(xs.average(), ys.average(), zs.average()),
                xs.flatMap { x -> ys.flatMap { y -> zs.map { z -> SceneVector(x, y, z) } } },
            )
        }
    }
}

internal data class SceneProjection(
    val center: SceneVector,
    val right: SceneVector,
    val up: SceneVector,
    val view: SceneVector,
    val width: Float,
    val height: Float,
    val scale: Double,
) {
    fun position(point: SceneVector): ScenePoint {
        val fromCenter = point - center
        return ScenePoint(width / 2 + fromCenter.dot(right) * scale, height / 2 - fromCenter.dot(up) * scale)
    }

    companion object {
        fun fit(camera: SceneCamera, bounds: SceneBounds, width: Float, height: Float, padding: Float): SceneProjection {
            val yaw = camera.yaw.radians()
            val pitch = camera.pitch.radians()
            val east = cos(yaw)
            val north = sin(yaw)
            val right = SceneVector(-north, east, 0.0)
            val up = SceneVector(-east * sin(pitch), -north * sin(pitch), cos(pitch))
            val view = SceneVector(east * cos(pitch), north * cos(pitch), sin(pitch))
            val halfWidth = bounds.corners.maxOf { (it - bounds.center).dot(right) }
            val halfHeight = bounds.corners.maxOf { (it - bounds.center).dot(up) }
            val horizontal = (width - 2 * padding).coerceAtLeast(1f) / (2 * halfWidth.coerceAtLeast(0.005))
            val vertical = (height - 2 * padding).coerceAtLeast(1f) / (2 * halfHeight.coerceAtLeast(0.005))
            return SceneProjection(bounds.center, right, up, view, width, height, min(horizontal, vertical) * camera.zoom)
        }
    }
}

internal data class SceneFace(
    val vertices: List<Int>,
    val center: SceneVector,
    val normal: SceneVector,
    val color: Int,
    val material: String,
    val surfacePattern: SceneSurfacePattern? = null,
)

internal data class SceneMesh(val vertices: List<SceneVector>, val faces: List<SceneFace>, val bounds: SceneBounds) {
    fun visibleFaces(view: SceneVector): List<SceneFace> = faces
        .filter { it.normal.dot(view) > 0.00001 || it.material == ExtensionSceneMaterial.GLASS }
        .sortedBy { it.center.dot(view) }

    companion object {
        fun from(shapes: List<ExtensionSceneShape>): SceneMesh? {
            val vertices = mutableListOf<SceneVector>()
            val faces = mutableListOf<SceneFace>()
            for (shape in shapes.take(500)) {
                val solid = SceneSolid.from(shape) ?: continue
                solid.appendTo(vertices, faces)
            }
            return if (vertices.isEmpty()) null else SceneMesh(vertices, faces, SceneBounds.of(vertices))
        }
    }
}

private data class SceneSolid(
    val footprint: List<ScenePoint>,
    val bottom: Double,
    val top: Double,
    val color: Int,
    val material: String,
    val surfacePattern: SceneSurfacePattern,
) {
    fun appendTo(vertices: MutableList<SceneVector>, faces: MutableList<SceneFace>) {
        val first = vertices.size
        val count = footprint.size
        vertices += footprint.map { it.atHeight(bottom) }
        vertices += footprint.map { it.atHeight(top) }

        fun add(indices: List<Int>, normal: SceneVector, surfacePattern: SceneSurfacePattern? = null) {
            val points = indices.map(vertices::get)
            val center = SceneVector(points.map { it.x }.average(), points.map { it.y }.average(), points.map { it.z }.average())
            faces += SceneFace(indices, center, normal, color, material, surfacePattern)
        }
        // Triangulate simple (possibly concave) caps instead of emitting one perimeter n-gon: the renderer fills each
        // triangle exactly, so an L/U-shaped prism keeps its notch rather than letting the closing chord fill it (P2).
        val triangles = triangulateSimplePolygon(footprint)
        for ((a, b, c) in triangles) {
            add(listOf(first + c, first + b, first + a), SceneVector(0.0, 0.0, -1.0))
            add(
                listOf(first + count + a, first + count + b, first + count + c),
                SceneVector(0.0, 0.0, 1.0),
                surfacePattern,
            )
        }
        for (i in footprint.indices) {
            val j = (i + 1) % count
            val dx = footprint[j].x - footprint[i].x
            val dy = footprint[j].y - footprint[i].y
            val length = hypot(dx, dy)
            if (length <= 0.00001) continue
            add(listOf(first + i, first + j, first + j + count, first + i + count), SceneVector(dy / length, -dx / length, 0.0))
        }
    }

    companion object {
        fun from(shape: ExtensionSceneShape): SceneSolid? = when (shape) {
            is ExtensionSceneBox -> solid(
                box(shape),
                shape.bottom,
                shape.top,
                shape.color,
                shape.material,
                boxSurface(shape),
            )
            is ExtensionSceneCylinder -> solid(circle(shape), shape.bottom, shape.top, shape.color, shape.material)
            is ExtensionScenePrism -> solid(polygon(shape), shape.bottom, shape.top, shape.color, shape.material)
            is ExtensionSceneShape.Unknown -> null
        }

        private fun solid(
            points: List<ScenePoint>,
            bottom: Double,
            top: Double,
            color: String?,
            material: String?,
            surfacePattern: SceneSurfacePattern? = null,
        ): SceneSolid? {
            if (points.size < 3 || !bottom.isFinite() || !top.isFinite() || top - bottom < 0.004999) return null
            val resolved = material?.takeIf { it in ExtensionSceneMaterial.ALL } ?: ExtensionSceneMaterial.MATTE
            val defaultColor = when (resolved) {
                ExtensionSceneMaterial.GLASS -> 0xBED7DE
                ExtensionSceneMaterial.METAL -> 0xA7ABB0
                ExtensionSceneMaterial.SCREEN -> 0x30343B
                ExtensionSceneMaterial.TILE -> 0xD9D7CC
                ExtensionSceneMaterial.WOOD -> 0xB78F65
                else -> 0xD4CEC5
            }
            val rgb = color?.takeIf { it.length == 7 && it.startsWith("#") }?.substring(1)?.toIntOrNull(16) ?: defaultColor
            return SceneSolid(points, bottom, top, rgb, resolved, surfacePattern ?: SceneSurfacePattern.worldFootprint(points))
        }

        private fun box(shape: ExtensionSceneBox): List<ScenePoint> {
            val center = ScenePoint.from(shape.center) ?: return emptyList()
            val size = ScenePoint.from(shape.size) ?: return emptyList()
            if (size.x <= 0.0 || size.y <= 0.0) return emptyList()
            val x = size.x / 2
            val y = size.y / 2
            val corners = listOf(ScenePoint(-x, -y), ScenePoint(x, -y), ScenePoint(x, y), ScenePoint(-x, y))
            // A sub-millimetre/2 mm radius (e.g. 0.01 m on an 8.12 x 0.12 m wall) is visually square; rounding it
            // still replaces four corners with twenty near-coincident vertices and up to 18 side faces per box.
            val rawRadius = (shape.round?.takeIf(Double::isFinite) ?: 0.0).coerceIn(0.0, min(x, y))
            val radius = if (rawRadius <= NEGLIGIBLE_ROUND_METERS) 0.0 else rawRadius
            val outline = if (radius == 0.0) corners else corners.flatMapIndexed { i, corner ->
                val cx = if (corner.x < 0) corner.x + radius else corner.x - radius
                val cy = if (corner.y < 0) corner.y + radius else corner.y - radius
                (0..4).map { step ->
                    val angle = (180 + i * 90 + step * 22.5).radians()
                    ScenePoint(cx + radius * cos(angle), cy + radius * sin(angle))
                }
            }
            val yaw = (shape.yaw?.takeIf(Double::isFinite) ?: 0.0).radians()
            return outline.map { point ->
                ScenePoint(center.x + point.x * cos(yaw) - point.y * sin(yaw), center.y + point.x * sin(yaw) + point.y * cos(yaw))
            }
        }

        private fun boxSurface(shape: ExtensionSceneBox): SceneSurfacePattern? {
            val center = ScenePoint.from(shape.center) ?: return null
            val size = ScenePoint.from(shape.size) ?: return null
            if (size.x <= 0.0 || size.y <= 0.0) return null
            val yaw = shape.yaw?.takeIf(Double::isFinite) ?: 0.0
            return SceneSurfacePattern.box(center, size.x / 2, size.y / 2, yaw)
        }

        private fun circle(shape: ExtensionSceneCylinder): List<ScenePoint> {
            val center = ScenePoint.from(shape.center) ?: return emptyList()
            val radius = shape.radius.takeIf { it.isFinite() && it > 0 } ?: return emptyList()
            return List(24) { i ->
                val angle = i * PI / 12
                ScenePoint(center.x + radius * cos(angle), center.y + radius * sin(angle))
            }
        }

        private fun polygon(shape: ExtensionScenePrism): List<ScenePoint> {
            if (shape.points.size !in 3..64) return emptyList()
            val points = shape.points.map { ScenePoint.from(it) ?: return emptyList() }
            val signedArea = points.indices.sumOf { i ->
                val a = points[i]
                val b = points[(i + 1) % points.size]
                a.x * b.y - b.x * a.y
            }
            return if (signedArea < 0) points.asReversed() else points
        }
    }
}

private fun Double.radians() = this * PI / 180

/**
 * Ear-clipping triangulation of a simple polygon. The footprint is normalized counter-clockwise by [SceneSolid.from];
 * returns original-index triples in CCW order. Degenerate inputs fall back to a single fan so callers always get caps.
 */
internal fun triangulateSimplePolygon(points: List<ScenePoint>): List<Triple<Int, Int, Int>> {
    val n = points.size
    if (n < 3) return emptyList()
    if (n == 3) return listOf(Triple(0, 1, 2))
    val order = ArrayDeque<Int>().apply { addAll(points.indices) }
    val triangles = mutableListOf<Triple<Int, Int, Int>>()
    var guard = 0
    val maxSteps = n * n + 16
    while (order.size > 3 && guard++ < maxSteps) {
        var earIndex = -1
        val m = order.size
        for (k in 0 until m) {
            val prev = order[(k - 1 + m) % m]
            val cur = order[k]
            val next = order[(k + 1) % m]
            val a = points[prev]; val b = points[cur]; val c = points[next]
            if (cross(a, b, c) <= 1e-10) continue // reflex or degenerate vertex for a CCW polygon
            val containsOther = order.any { idx ->
                idx != prev && idx != cur && idx != next && pointInTriangle(points[idx], a, b, c)
            }
            if (!containsOther) { earIndex = k; break }
        }
        if (earIndex < 0) {
            // Defensive fan fallback for near-degenerate/self-touching outlines rather than dropping the cap.
            return (1 until n - 1).map { Triple(0, it, it + 1) }
        }
        val mNow = order.size
        val prev = order[(earIndex - 1 + mNow) % mNow]
        val cur = order[earIndex]
        val next = order[(earIndex + 1) % mNow]
        triangles += Triple(prev, cur, next)
        order.removeAt(earIndex)
    }
    if (order.size == 3) triangles += Triple(order[0], order[1], order[2])
    return triangles
}

private fun cross(a: ScenePoint, b: ScenePoint, c: ScenePoint): Double =
    (b.x - a.x) * (c.y - a.y) - (b.y - a.y) * (c.x - a.x)

private fun pointInTriangle(p: ScenePoint, a: ScenePoint, b: ScenePoint, c: ScenePoint): Boolean {
    val c1 = cross(a, b, p)
    val c2 = cross(b, c, p)
    val c3 = cross(c, a, p)
    val eps = 1e-9
    return c1 >= -eps && c2 >= -eps && c3 >= -eps
}
