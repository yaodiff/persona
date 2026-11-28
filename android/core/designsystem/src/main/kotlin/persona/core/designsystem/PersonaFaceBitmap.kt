package persona.core.designsystem

import android.graphics.Bitmap
import android.graphics.BlurMaskFilter
import android.graphics.Canvas
import android.graphics.LinearGradient
import android.graphics.Paint
import android.graphics.Path as AndroidPath
import android.graphics.RectF
import android.graphics.Shader
import android.util.LruCache
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.asAndroidPath
import androidx.compose.ui.graphics.toArgb
import androidx.core.graphics.createBitmap
import androidx.core.graphics.withSave
import kotlin.math.max

/**
 * Renders the built-in Mochi [PersonaFace] into a static [Bitmap] for system surfaces outside Compose
 * (such as Jetpack Glance home-screen widgets), matching [PersonaFace]'s exact geometry and expressions
 * across [PersonaState.Idle], [PersonaState.Thinking], [PersonaState.Working], [PersonaState.NeedsYou],
 * and [PersonaState.Offline].
 */
fun personaFaceBitmap(
    state: PersonaState,
    sizePx: Int,
    dark: Boolean,
    needsYouRing: Color,
    density: Float = 3f,
    compact: Boolean = false,
): Bitmap {
    val safeSize = sizePx.coerceAtLeast(1)
    val safeDensity = density.coerceAtLeast(1f)
    val key = FaceBitmapKey(state, safeSize, dark, needsYouRing.toArgb(), (safeDensity * 10f).toInt(), compact)
    faceBitmapCache.get(key)?.takeIf { !it.isRecycled }?.let { return it }
    val bitmap = renderPersonaFaceBitmap(state, safeSize, dark, needsYouRing, safeDensity, compact)
    val _ = faceBitmapCache.put(key, bitmap)
    return bitmap
}

/** Uncached renderer exposed for deterministic unit tests. */
fun renderPersonaFaceBitmap(
    state: PersonaState,
    sizePx: Int,
    dark: Boolean,
    needsYouRing: Color,
    density: Float = 3f,
    compact: Boolean = false,
): Bitmap {
    val side = sizePx.coerceAtLeast(1)
    val bitmap = createBitmap(side, side, Bitmap.Config.ARGB_8888)
    val canvas = Canvas(bitmap)
    val s = side.toFloat()
    val offline = state == PersonaState.Offline
    val ink = (if (offline) Mochi.sleepInk else Mochi.ink).toArgb()
    val darkOffline = dark && offline
    val bodyColors = when {
        darkOffline -> Mochi.sleepBodyDark
        offline -> Mochi.sleepBody
        else -> Mochi.bodyColors
    }
    val outline = (if (dark) Mochi.outlineDark else if (offline) Mochi.sleepInk else Mochi.ink).toArgb()
    val paint = Paint(Paint.ANTI_ALIAS_FLAG)

    // 1. Background disc gradient.
    val disc = if (dark) Mochi.discDark else Mochi.discLight
    paint.style = Paint.Style.FILL
    paint.alpha = 255
    paint.shader = LinearGradient(0f, 0f, 0f, s, disc[0].toArgb(), disc[1].toArgb(), Shader.TileMode.CLAMP)
    canvas.drawCircle(s / 2f, s / 2f, s / 2f, paint)
    paint.shader = null

    // 2. Clipped interior: working rim arc, soft body shadow, body fill/outline, cheek, gloss/lines, eyes.
    canvas.withSave {
        val circle = AndroidPath().apply { addOval(RectF(0f, 0f, s, s), AndroidPath.Direction.CW) }
        val _ = clipPath(circle)

        if (state == PersonaState.Working) {
            val w = max(RING_MIN_DP * density, s * RING_SHARE)
            paint.style = Paint.Style.STROKE
            paint.strokeWidth = w
            paint.strokeCap = Paint.Cap.ROUND
            paint.color = Mochi.cheek.toArgb()
            drawArc(RectF(w / 2f, w / 2f, s - w / 2f, s - w / 2f), 210f, 0.28f * 360f, false, paint)
        }

        val lw = max(STROKE_MIN_DP * density, s * Mochi.STROKE)

        // Soft shadow under the body.
        val shadowAlpha = when {
            darkOffline -> Mochi.SLEEP_SHADOW_DARK
            dark -> 0.45f
            else -> 0.14f
        }
        paint.style = Paint.Style.FILL
        paint.color = Tokens.System.Light.primary.copy(alpha = shadowAlpha).toArgb()
        paint.maskFilter = BlurMaskFilter(max(0.5f, s * 0.02f), BlurMaskFilter.Blur.NORMAL)
        val shadowCenter = Mochi.p(490f, 800f) * s
        drawOval(
            shadowCenter.x - 0.3f * s,
            shadowCenter.y - 0.022f * s,
            shadowCenter.x + 0.3f * s,
            shadowCenter.y + 0.022f * s,
            paint,
        )
        paint.maskFilter = null

        // Body fill and outline.
        val bodyPath = Mochi.body(s).asAndroidPath()
        paint.style = Paint.Style.FILL
        // Reset shadow alpha before binding the gradient shader; Canvas multiplies paint.alpha into shader output.
        paint.alpha = 255
        paint.shader = LinearGradient(
            0.4f * s, 0.3f * s, 0.5f * s, 0.82f * s,
            bodyColors[0].toArgb(), bodyColors[1].toArgb(), Shader.TileMode.CLAMP,
        )
        drawPath(bodyPath, paint)
        paint.shader = null

        paint.style = Paint.Style.STROKE
        paint.strokeWidth = lw
        paint.strokeCap = Paint.Cap.ROUND
        paint.strokeJoin = Paint.Join.ROUND
        paint.color = outline
        drawPath(bodyPath, paint)

        // Cheek.
        val featureScale = if (compact) Mochi.COMPACT_FEATURES else 1f
        val cheekCenter = Mochi.p(312f, 556f) * s
        val cr = Mochi.CHEEK_RADIUS * featureScale * s
        paint.style = Paint.Style.FILL
        paint.color = (if (offline) Mochi.sleepCheek else Mochi.cheek).toArgb()
        drawOval(cheekCenter.x - cr, cheekCenter.y - cr * 0.9f, cheekCenter.x + cr, cheekCenter.y + cr * 0.9f, paint)

        // Gloss and motion lines (omitted in compact mode; lines hidden while thinking/offline).
        if (!compact) {
            paint.style = Paint.Style.STROKE
            paint.strokeWidth = lw * 0.6f
            paint.strokeCap = Paint.Cap.ROUND
            paint.color = Tokens.System.Light.page.toArgb()
            drawPath(Mochi.gloss(s).asAndroidPath(), paint)

            val lit = when (state) {
                PersonaState.Idle, PersonaState.NeedsYou -> floatArrayOf(1f, 1f, 1f)
                PersonaState.Working -> floatArrayOf(1f, 0.35f, 0.35f)
                else -> FloatArray(0)
            }
            val motionBase = if (dark) Mochi.motionDark else Mochi.ink
            paint.strokeWidth = lw * 0.85f
            lit.forEachIndexed { i, alpha ->
                val (from, to) = Mochi.lines[i]
                paint.color = motionBase.copy(alpha = alpha).toArgb()
                drawLine(from.x * s, from.y * s, to.x * s, to.y * s, paint)
            }
        }

        // Eyes.
        val er = Mochi.EYE_RADIUS * featureScale * s
        if (offline) {
            paint.style = Paint.Style.STROKE
            paint.strokeWidth = lw * 0.8f
            paint.strokeCap = Paint.Cap.ROUND
            paint.color = ink
            for (eye in Mochi.eyes) {
                val c = eye * s
                val lid = AndroidPath().apply {
                    moveTo(c.x - er, c.y)
                    quadTo(c.x, c.y + er * 0.7f, c.x + er, c.y)
                }
                drawPath(lid, paint)
            }
        } else {
            val lookX = when (state) {
                PersonaState.Thinking -> s * 0.022f
                PersonaState.Working -> -s * 0.024f
                else -> 0f
            }
            val lookY = when (state) {
                PersonaState.Thinking -> -s * 0.03f
                PersonaState.Working -> s * 0.012f
                else -> 0f
            }
            paint.style = Paint.Style.FILL
            paint.color = ink
            for (eye in Mochi.eyes) {
                val c = eye * s
                val cx = c.x + lookX
                val cy = c.y + lookY
                drawOval(cx - er, cy - er, cx + er, cy + er, paint)
            }
        }
    }

    // 3. Outer state overlays (drawn unclipped like PersonaFace's ring and thought bubble).
    if (state == PersonaState.NeedsYou) {
        val w = max(RING_MIN_DP * density, s * RING_SHARE)
        paint.style = Paint.Style.STROKE
        paint.strokeWidth = w
        paint.color = needsYouRing.toArgb()
        canvas.drawCircle(s / 2f, s / 2f, (s - w) / 2f, paint)
    }

    if (state == PersonaState.Thinking) {
        drawThoughtBubble(canvas, s, paint)
    }

    return bitmap
}

private fun drawThoughtBubble(canvas: Canvas, s: Float, paint: Paint) {
    val pillW = s * 0.34f
    val pillH = s * 0.16f
    val right = s * 0.98f
    val left = (right - pillW).coerceAtLeast(0f)
    val top = s * 0.06f
    val bottom = top + pillH
    val radius = pillH / 2f
    val rect = RectF(left, top, right, bottom)

    paint.style = Paint.Style.FILL
    paint.color = Tokens.System.Light.primary.copy(alpha = 0.16f).toArgb()
    paint.maskFilter = BlurMaskFilter(max(0.5f, s * 0.018f), BlurMaskFilter.Blur.NORMAL)
    canvas.drawRoundRect(RectF(left, top + s * 0.01f, right, bottom + s * 0.01f), radius, radius, paint)
    paint.maskFilter = null

    paint.color = Tokens.System.Light.page.toArgb()
    canvas.drawRoundRect(rect, radius, radius, paint)

    val dotRadius = s * 0.03f
    val cy = (top + bottom) / 2f
    val cx = (left + right) / 2f
    val step = dotRadius * 2.7f
    paint.color = Mochi.cheek.toArgb()
    canvas.drawCircle(cx - step, cy, dotRadius, paint)
    canvas.drawCircle(cx, cy, dotRadius, paint)
    canvas.drawCircle(cx + step, cy, dotRadius, paint)
}

private data class FaceBitmapKey(
    val state: PersonaState,
    val sizePx: Int,
    val dark: Boolean,
    val ringArgb: Int,
    val densityDeci: Int,
    val compact: Boolean,
)

private val faceBitmapCache = object : LruCache<FaceBitmapKey, Bitmap>(16) {}

private const val RING_SHARE = 0.07f
private const val RING_MIN_DP = 1.5f
private const val STROKE_MIN_DP = 1.3f
