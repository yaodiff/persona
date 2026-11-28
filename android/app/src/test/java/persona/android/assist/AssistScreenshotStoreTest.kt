package persona.android.assist

import android.content.Context
import android.graphics.Bitmap
import android.graphics.Color
import android.net.Uri
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.io.File
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class AssistScreenshotStoreTest {
    private val context: Context get() = ApplicationProvider.getApplicationContext()

    @After
    fun cleanUp() {
        AssistScreenshotStore.directory(context).deleteRecursively()
    }

    @Test
    fun saveWritesPrivateCacheJpegAndReturnsImageAttachment() {
        val bitmap = Bitmap.createBitmap(120, 80, Bitmap.Config.ARGB_8888).apply { eraseColor(Color.BLUE) }
        val now = System.currentTimeMillis()
        val attachment = AssistScreenshotStore.save(
            context = context,
            bitmap = bitmap,
            id = "session-1",
            nowMillis = now,
            uriForFile = { file -> Uri.parse("content://${context.packageName}.chat.files/assist/${file.name}") },
        )

        assertNotNull(attachment)
        assertTrue(attachment!!.image)
        assertEquals("assist-session-1", attachment.id)
        assertTrue(attachment.uri.startsWith("content://${context.packageName}.chat.files/assist/assist-"))
        assertTrue((attachment.size ?: 0L) > 0L)

        val dir = AssistScreenshotStore.directory(context)
        assertTrue(dir.canonicalPath.startsWith(context.cacheDir.canonicalPath))
        val saved = File(dir, attachment.name)
        assertTrue(saved.isFile)
        assertEquals(saved.length(), attachment.size)
        bitmap.recycle()
    }

    @Test
    fun nullOrRecycledBitmapDegradesGracefullyWithoutWritingFiles() {
        val dir = AssistScreenshotStore.directory(context)
        assertNull(AssistScreenshotStore.save(context, null, "null-shot"))
        val recycled = Bitmap.createBitmap(10, 10, Bitmap.Config.ARGB_8888)
        recycled.recycle()
        assertNull(AssistScreenshotStore.save(context, recycled, "recycled-shot"))
        assertTrue(dir.listFiles().orEmpty().isEmpty())
    }

    @Test
    fun scaleDownRespectsMaxSideAndPreservesAspectRatio() {
        val large = Bitmap.createBitmap(2400, 3200, Bitmap.Config.ARGB_8888)
        val scaled = AssistScreenshotStore.scaleDownIfNeeded(large, AssistScreenshotStore.MAX_SIDE_PX)
        assertEquals(1200, scaled.width)
        assertEquals(1600, scaled.height)
        scaled.recycle()
        large.recycle()
    }

    @Test
    fun pruneExpiredDeletesStaleTempAndExcessFilesKeepingNewest() {
        // Use an isolated directory so PersonaApp's startup background prune cannot race with synthetic timestamps.
        val dir = File(context.cacheDir, "assist-prune-test-${System.nanoTime()}").apply { mkdirs() }
        try {
            val now = 10_000_000_000L
            val maxAge = 10_000L

            val expired = File(dir, "assist-expired.jpg").apply { writeBytes(byteArrayOf(1)); setLastModified(now - 20_000L) }
            val temp = File(dir, "assist-partial.tmp").apply { writeBytes(byteArrayOf(2)); setLastModified(now - 1_000L) }
            val oldKeep = File(dir, "assist-old.jpg").apply { writeBytes(byteArrayOf(3)); setLastModified(now - 6_000L) }
            val midKeep = File(dir, "assist-mid.jpg").apply { writeBytes(byteArrayOf(4)); setLastModified(now - 4_000L) }
            val newest = File(dir, "assist-new.jpg").apply { writeBytes(byteArrayOf(5)); setLastModified(now - 2_000L) }

            val deleted = AssistScreenshotStore.pruneExpired(dir, nowMillis = now, maxAgeMillis = maxAge, maxFiles = 2)

            assertEquals(3, deleted)
            assertFalse(expired.exists())
            assertFalse(temp.exists())
            assertFalse(oldKeep.exists())
            assertTrue(midKeep.exists())
            assertTrue(newest.exists())
        } finally {
            dir.deleteRecursively()
        }
    }

    @Test
    fun prepareInMemoryCreatesIndependentScaledCopyWithoutWritingFiles() {
        val dir = AssistScreenshotStore.directory(context)
        val source = Bitmap.createBitmap(2400, 1200, Bitmap.Config.ARGB_8888).apply { eraseColor(Color.GREEN) }
        val staged = AssistScreenshotStore.prepareInMemory(source)
        assertNotNull(staged)
        assertEquals(1600, staged!!.width)
        assertEquals(800, staged.height)
        assertTrue(dir.listFiles().orEmpty().isEmpty())

        // Recycling the staged copy must not affect the source bitmap.
        staged.recycle()
        assertFalse(source.isRecycled)
        source.recycle()
    }

    @Test
    fun deleteSavedDeletesOnlyFilesContainedInAssistCacheDir() {
        val bitmap = Bitmap.createBitmap(64, 64, Bitmap.Config.ARGB_8888).apply { eraseColor(Color.RED) }
        val saved = AssistScreenshotStore.save(
            context = context,
            bitmap = bitmap,
            id = "delete-me",
            nowMillis = System.currentTimeMillis(),
            uriForFile = { file -> Uri.parse("content://${context.packageName}.chat.files/assist/${file.name}") },
        )!!
        val savedFile = File(AssistScreenshotStore.directory(context), saved.name)
        assertTrue(savedFile.isFile)

        // Path traversal attempt outside cacheDir/assist must be rejected.
        val outside = File(context.cacheDir, "outside-keep.txt").apply { writeText("keep") }
        try {
            val traversal = saved.copy(name = "../outside-keep.txt")
            assertFalse(AssistScreenshotStore.deleteSaved(context, traversal))
            assertTrue(outside.isFile)

            assertTrue(AssistScreenshotStore.deleteSaved(context, saved))
            assertFalse(savedFile.exists())
        } finally {
            outside.delete()
            bitmap.recycle()
        }
    }
}
