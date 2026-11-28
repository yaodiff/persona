package persona.android.assist

import android.content.Context
import android.graphics.Bitmap
import android.net.Uri
import androidx.core.content.FileProvider
import java.io.File
import java.io.FileOutputStream
import kotlin.math.roundToInt
import persona.feature.chat.ComposerAttachment

/**
 * Persists system-provided assist screenshots solely inside the app's private cache (`cacheDir/assist/`)
 * and serves them through the existing chat [FileProvider] (`<package>.chat.files`). Never writes to
 * MediaStore or public storage, and prunes expired or excess files automatically.
 */
internal object AssistScreenshotStore {
    const val DIR_NAME = "assist"
    const val MAX_SIDE_PX = 1600
    const val JPEG_QUALITY = 88
    const val DEFAULT_MAX_AGE_MILLIS = 24L * 60 * 60 * 1000
    const val DEFAULT_MAX_FILES = 16

    // Serializes saves and pruning so startup cleanup never deletes an in-flight `.tmp` screenshot on cold launch.
    private val storeLock = Any()

    fun directory(context: Context): File = File(context.cacheDir, DIR_NAME)

    /**
     * Converts a system-provided [bitmap] (including [Bitmap.Config.HARDWARE]) into an owned, downscaled
     * software [Bitmap] held strictly in memory without writing to disk. Returns null for null/recycled/empty bitmaps.
     */
    fun prepareInMemory(bitmap: Bitmap?, maxSidePx: Int = MAX_SIDE_PX): Bitmap? {
        if (bitmap == null || bitmap.isRecycled || bitmap.width <= 0 || bitmap.height <= 0) return null
        var software: Bitmap? = null
        return try {
            software = bitmap.copy(Bitmap.Config.ARGB_8888, false) ?: return null
            val scaled = scaleDownIfNeeded(software, maxSidePx)
            if (scaled !== software && !software.isRecycled) software.recycle()
            scaled
        } catch (_: Exception) {
            if (software != null && software !== bitmap && !software.isRecycled) software.recycle()
            null
        }
    }

    /** Deletes a single screenshot previously saved under `cacheDir/assist/`, verifying path containment. */
    fun deleteSaved(context: Context, attachment: ComposerAttachment?): Boolean {
        val name = attachment?.name?.takeIf { it.isNotBlank() } ?: return false
        return synchronized(storeLock) {
            val dir = directory(context)
            val target = File(dir, name)
            val safe = runCatching {
                target.canonicalPath.startsWith(dir.canonicalPath + File.separator)
            }.getOrDefault(false)
            safe && target.isFile && target.delete()
        }
    }

    fun pruneExpired(
        context: Context,
        nowMillis: Long = System.currentTimeMillis(),
        maxAgeMillis: Long = DEFAULT_MAX_AGE_MILLIS,
        maxFiles: Int = DEFAULT_MAX_FILES,
    ): Int = pruneExpired(directory(context), nowMillis, maxAgeMillis, maxFiles)

    /**
     * Deletes partial `.tmp` files, files older than [maxAgeMillis], and oldest files beyond [maxFiles].
     * Returns the number of deleted files.
     */
    fun pruneExpired(
        directory: File,
        nowMillis: Long = System.currentTimeMillis(),
        maxAgeMillis: Long = DEFAULT_MAX_AGE_MILLIS,
        maxFiles: Int = DEFAULT_MAX_FILES,
    ): Int = synchronized(storeLock) {
        pruneExpiredLocked(directory, nowMillis, maxAgeMillis, maxFiles)
    }

    private fun pruneExpiredLocked(
        directory: File,
        nowMillis: Long,
        maxAgeMillis: Long,
        maxFiles: Int,
    ): Int {
        if (!directory.isDirectory) return 0
        val files = directory.listFiles()?.filter { it.isFile }.orEmpty()
        if (files.isEmpty()) return 0
        var deleted = 0
        val kept = mutableListOf<File>()
        for (file in files) {
            val age = nowMillis - file.lastModified()
            if (file.name.endsWith(".tmp") || age > maxAgeMillis) {
                if (file.delete()) deleted++
            } else {
                kept += file
            }
        }
        val limit = maxFiles.coerceAtLeast(0)
        if (kept.size > limit) {
            kept.sortWith(compareBy<File> { it.lastModified() }.thenBy { it.name })
            val excess = kept.size - limit
            for (i in 0 until excess) {
                if (kept[i].delete()) deleted++
            }
        }
        return deleted
    }

    /**
     * Compresses [bitmap] (converting hardware bitmaps and downscaling large screens) into `cacheDir/assist/`
     * and returns a [ComposerAttachment] ready for the chat composer draft, or null when [bitmap] is null/invalid.
     */
    fun save(
        context: Context,
        bitmap: Bitmap?,
        id: String,
        nowMillis: Long = System.currentTimeMillis(),
        maxAgeMillis: Long = DEFAULT_MAX_AGE_MILLIS,
        maxFiles: Int = DEFAULT_MAX_FILES,
        uriForFile: (File) -> Uri = { file -> FileProvider.getUriForFile(context, "${context.packageName}.chat.files", file) },
    ): ComposerAttachment? {
        if (bitmap == null || bitmap.isRecycled || bitmap.width <= 0 || bitmap.height <= 0) return null
        return synchronized(storeLock) {
            val dir = directory(context)
            if (!dir.isDirectory && !dir.mkdirs()) return null
            pruneExpiredLocked(dir, nowMillis, maxAgeMillis, (maxFiles - 1).coerceAtLeast(0))
            val safeId = sanitizeId(id)
            val tmpFile = File(dir, "assist-$safeId.tmp")
            val targetFile = File(dir, "assist-$nowMillis-$safeId.jpg")
            var software: Bitmap? = null
            var scaled: Bitmap? = null
            try {
                software = if (bitmap.config == Bitmap.Config.HARDWARE) {
                    bitmap.copy(Bitmap.Config.ARGB_8888, false) ?: return null
                } else {
                    bitmap
                }
                scaled = scaleDownIfNeeded(software, MAX_SIDE_PX)
                FileOutputStream(tmpFile).use { stream ->
                    check(scaled.compress(Bitmap.CompressFormat.JPEG, JPEG_QUALITY, stream)) { "JPEG compression failed" }
                    stream.fd.sync()
                }
                if (targetFile.exists()) targetFile.delete()
                check(tmpFile.renameTo(targetFile)) { "Unable to finalize assist screenshot" }
                targetFile.setLastModified(nowMillis)
                val uri = uriForFile(targetFile)
                ComposerAttachment(
                    id = "assist-$safeId",
                    name = targetFile.name,
                    uri = uri.toString(),
                    image = true,
                    size = targetFile.length(),
                )
            } catch (_: Exception) {
                tmpFile.delete()
                targetFile.delete()
                null
            } finally {
                if (scaled != null && scaled !== software && !scaled.isRecycled) scaled.recycle()
                if (software != null && software !== bitmap && !software.isRecycled) software.recycle()
            }
        }
    }

    internal fun scaleDownIfNeeded(source: Bitmap, maxSidePx: Int): Bitmap {
        val longest = maxOf(source.width, source.height)
        if (longest <= maxSidePx || maxSidePx <= 0) return source
        val scale = maxSidePx.toDouble() / longest.toDouble()
        val width = maxOf(1, (source.width * scale).roundToInt())
        val height = maxOf(1, (source.height * scale).roundToInt())
        return Bitmap.createScaledBitmap(source, width, height, true)
    }

    private fun sanitizeId(raw: String): String =
        raw.filter { it.isLetterOrDigit() || it == '-' || it == '_' }.take(40).ifEmpty { "shot" }
}
