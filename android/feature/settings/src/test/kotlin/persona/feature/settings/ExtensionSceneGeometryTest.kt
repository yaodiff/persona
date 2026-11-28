package persona.feature.settings

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.generated.ExtensionSceneBox
import persona.core.contracts.generated.ExtensionSceneCamera
import persona.core.contracts.generated.ExtensionSceneCylinder
import persona.core.contracts.generated.ExtensionSceneMaterial
import persona.core.contracts.generated.ExtensionScenePrism
import persona.core.contracts.generated.ExtensionSceneShape
import kotlin.math.abs

class ExtensionSceneGeometryTest {
    private val box = ExtensionSceneBox(
        center = listOf(0.0, 0.0), size = listOf(4.0, 2.0),
        bottom = 0.0, top = 1.0, color = "#658197",
    )

    @Test fun cameraUsesWireDefaultsAndLimitsOrbitAndZoom() {
        assertEquals(SceneCamera(225.0, 35.0, 1.0), SceneCamera.from(null))
        val camera = SceneCamera.from(ExtensionSceneCamera(yaw = -90.0, pitch = 25.0, zoom = 1.5))
        assertEquals(SceneCamera(270.0, 25.0, 1.5), camera)
        assertEquals(SceneCamera(10.0, 10.0, 4.0), camera.orbit(100.0, -45.0).zoomBy(5.0))
        assertEquals(0.5, camera.zoomBy(0.1).zoom, 1e-8)
    }

    @Test fun knownShapesUseMetresAndBoxYawTurnsCounterClockwise() {
        val mesh = requireNotNull(SceneMesh.from(listOf(box.copy(yaw = 90.0))))
        assertEquals(1.0, mesh.vertices.maxOf { it.x }, 1e-8)
        assertEquals(2.0, mesh.vertices.maxOf { it.y }, 1e-8)
        assertEquals(1.0, mesh.vertices.maxOf { it.z }, 1e-8)
        assertTrue(mesh.faces.all { it.color == 0x658197 })
        val rounded = requireNotNull(SceneMesh.from(listOf(box.copy(round = 0.3))))
        assertTrue(rounded.faces.size > mesh.faces.size)
        val cylinder = ExtensionSceneCylinder(center = listOf(0.0, 0.0), radius = 0.3, bottom = 0.0, top = 0.4)
        assertEquals(0.3, requireNotNull(SceneMesh.from(listOf(cylinder))).vertices.maxOf { it.x }, 1e-8)
    }

    @Test fun concavePrismKeepsItsPerimeterAndOutwardNormalsInEitherOrder() {
        val outline = listOf(listOf(0.0, 0.0), listOf(2.0, 0.0), listOf(2.0, 1.0),
            listOf(1.0, 1.0), listOf(1.0, 2.0), listOf(0.0, 2.0))
        val prism = ExtensionScenePrism(points = outline, bottom = 0.0, top = 0.08)
        val forward = requireNotNull(SceneMesh.from(listOf(prism)))
        val reversed = requireNotNull(SceneMesh.from(listOf(prism.copy(points = outline.reversed()))))
        assertEquals(forward.vertices, reversed.vertices)
        // The concave cap is ear-clipped into (n-2)=4 upward triangles rather than one 6-vertex n-gon.
        val topCaps = forward.faces.filter { it.normal.z == 1.0 }
        assertEquals(4, topCaps.size)
        assertTrue(topCaps.all { it.vertices.size == 3 })
        assertEquals(SceneVector(0.0, -1.0, 0.0), forward.faces.first { it.normal.y == -1.0 }.normal)
    }

    @Test fun earClipperTriangulatesConcaveAndConvexPolygons() {
        val lShape = listOf(
            ScenePoint(0.0, 0.0), ScenePoint(2.0, 0.0), ScenePoint(2.0, 1.0),
            ScenePoint(1.0, 1.0), ScenePoint(1.0, 2.0), ScenePoint(0.0, 2.0),
        )
        val triangles = triangulateSimplePolygon(lShape)
        assertEquals(4, triangles.size)
        // Every original perimeter edge must be represented by at least one triangle edge.
        val edges = triangles.flatMap { (a, b, c) -> listOf(setOf(a, b), setOf(b, c), setOf(c, a)) }.toSet()
        for (i in lShape.indices) assertTrue(edges.contains(setOf(i, (i + 1) % lShape.size)))
        // A convex quad yields exactly two triangles.
        assertEquals(2, triangulateSimplePolygon(listOf(
            ScenePoint(-1.0, -1.0), ScenePoint(1.0, -1.0), ScenePoint(1.0, 1.0), ScenePoint(-1.0, 1.0))).size)
    }

    @Test fun rotatedTileCapsShareOneYawedSurfacePattern() {
        val mesh = requireNotNull(SceneMesh.from(listOf(box.copy(
            size = listOf(4.0, 2.0),
            yaw = 90.0,
            material = ExtensionSceneMaterial.TILE,
        ))))
        val caps = mesh.faces.filter { it.normal.z > 0.5 }
        assertEquals(2, caps.size)
        val pattern = requireNotNull(caps.first().surfacePattern)
        assertTrue(caps.all { it.surfacePattern == pattern })
        assertEquals(-2.0, pattern.minX, 1e-8)
        assertEquals(2.0, pattern.maxX, 1e-8)
        assertEquals(-1.0, pattern.minY, 1e-8)
        assertEquals(1.0, pattern.maxY, 1e-8)
        // A 90° box turns the pattern's local x axis onto world +y and local y onto world -x.
        assertEquals(0.0, pattern.xAxis.x, 1e-8)
        assertEquals(1.0, pattern.xAxis.y, 1e-8)
        assertEquals(-1.0, pattern.yAxis.x, 1e-8)
        assertEquals(0.0, pattern.yAxis.y, 1e-8)
        val seamX = repeatedSeams(pattern.minX, pattern.maxX, 0.42).first()
        val start = pattern.at(seamX, pattern.minY, 1.0)
        val end = pattern.at(seamX, pattern.maxY, 1.0)
        assertTrue(abs(end.x - start.x) > 1.0)
        assertEquals(0.0, end.y - start.y, 1e-8)
    }

    @Test fun earClippedTileCapsShareTheFullFootprintSurfacePattern() {
        val outline = listOf(listOf(0.0, 0.0), listOf(2.0, 0.0), listOf(2.0, 1.0),
            listOf(1.0, 1.0), listOf(1.0, 2.0), listOf(0.0, 2.0))
        val mesh = requireNotNull(SceneMesh.from(listOf(ExtensionScenePrism(
            points = outline,
            bottom = 0.0,
            top = 0.08,
            material = ExtensionSceneMaterial.TILE,
        ))))
        val caps = mesh.faces.filter { it.normal.z > 0.5 }
        assertEquals(4, caps.size)
        val pattern = requireNotNull(caps.first().surfacePattern)
        assertTrue(caps.all { it.surfacePattern == pattern })
        assertEquals(ScenePoint(0.0, 0.0), pattern.origin)
        assertEquals(0.0, pattern.minX, 1e-8)
        assertEquals(2.0, pattern.maxX, 1e-8)
        assertEquals(0.0, pattern.minY, 1e-8)
        assertEquals(2.0, pattern.maxY, 1e-8)
        // Every cap triangle clips the same full-footprint seam: it is not restarted at that triangle's own bbox.
        val seamX = repeatedSeams(pattern.minX, pattern.maxX, 0.42).first()
        assertEquals(SceneVector(seamX, 0.0, 0.08), pattern.at(seamX, pattern.minY, 0.08))
        assertEquals(SceneVector(seamX, 2.0, 0.08), pattern.at(seamX, pattern.maxY, 0.08))
    }

    @Test fun negligibleRoundsStaySquareButVisibleRoundsAddVertices() {
        val square = requireNotNull(SceneMesh.from(listOf(box)))
        val tinyRound = requireNotNull(SceneMesh.from(listOf(box.copy(round = 0.001))))
        // At or below the 0.002 m cutoff the footprint stays four corners (8 top+bottom vertices).
        assertEquals(8, square.vertices.size)
        assertEquals(square.vertices.size, tinyRound.vertices.size)
        val visibleRound = requireNotNull(SceneMesh.from(listOf(box.copy(round = 0.3))))
        assertTrue(visibleRound.vertices.size > square.vertices.size)
    }

    @Test fun seamsAnchorToTheFaceBoundsInsteadOfWorldZero() {
        assertEquals(listOf(0.5, 1.0), repeatedSeams(0.0, 1.0, 0.5).toList())
        // Translating the face far from the origin preserves the same local pattern.
        assertEquals(listOf(100.5, 101.0), repeatedSeams(100.0, 101.0, 0.5).toList())
        // A narrow face with a fine spacing keeps every seam inside its own bounds.
        val narrow = repeatedSeams(4.0, 4.2, 0.05).toList()
        assertTrue(narrow.isNotEmpty() && narrow.all { it > 4.0 && it <= 4.2 + 1e-9 })
        // A spacing larger than the span yields no seam deterministically rather than snapping to world zero.
        assertTrue(repeatedSeams(4.0, 4.2, 8.0).none { it <= 4.0 || it > 4.2 + 1e-9 })
        assertEquals(emptyList<Double>(), repeatedSeams(2.0, 2.0, 0.5).toList())
    }

    @Test fun topDownFitUsesJustShapeBoundsAndZoomKeepsTheCenter() {
        val mesh = requireNotNull(SceneMesh.from(listOf(box)))
        val camera = SceneCamera(0.0, 90.0, 1.0)
        val fit = SceneProjection.fit(camera, mesh.bounds, 300f, 300f, 20f)
        assertEquals(ScenePoint(150.0, 150.0), fit.position(mesh.bounds.center))
        assertTrue(mesh.vertices.all {
            val point = fit.position(it)
            point.x in 19.99..280.01 && point.y in 19.99..280.01
        })
        val zoomed = SceneProjection.fit(camera.zoomBy(2.0), mesh.bounds, 300f, 300f, 20f)
        assertEquals(fit.scale * 2.0, zoomed.scale, 1e-8)
        assertEquals(fit.position(mesh.bounds.center), zoomed.position(mesh.bounds.center))
    }

    @Test fun malformedOrFutureShapesDoNotBreakTheRestOfTheScene() {
        val unknown = ExtensionSceneShape.Unknown("future")
        assertNull(SceneMesh.from(listOf(unknown, box.copy(top = 0.0))))
        assertNotNull(SceneMesh.from(listOf(unknown, box)))
    }
}
