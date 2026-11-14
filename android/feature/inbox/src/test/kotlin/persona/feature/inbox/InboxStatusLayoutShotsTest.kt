package persona.feature.inbox

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Rect
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.text.TextLayoutResult
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.io.File
import java.time.Instant
import java.time.ZoneOffset
import kotlin.math.ceil
import kotlin.math.floor
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.contentOrNull
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.InboxSuggestion
import persona.core.contracts.inboxEventFields
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.domain.inbox.InboxItem
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator

/** Focused native captures of the shared sc-1 status sentence and its actions at default and large text sizes. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1800dp-xhdpi")
class InboxStatusLayoutShotsTest {
    @get:Rule val rule = createComposeRule()

    private val clock = Instant.parse("2026-01-01T12:00:00Z")
    private val status = "Waiting for 张三确认新报价 · look again tomorrow 09:00: 问更正后的报价行不行"
    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }

    private fun JsonObject.text(key: String) = (this[key] as? JsonPrimitive)?.contentOrNull

    private fun showcase(): InboxRecord {
        val root = System.getProperty("persona.repo.root")?.let(::File) ?: generateSequence(File(".").canonicalFile) { it.parentFile }
            .first { File(it, ".git").exists() && File(it, "android").isDirectory }
        val fixture = Json.parseToJsonElement(File(root, "spec/fixtures/inbox/showcase.json").readText()).jsonObject
        val item = fixture.getValue("items").jsonArray.first {
            it.jsonObject.getValue("id").jsonPrimitive.content == "sc-1"
        }.jsonObject
        val event = inboxEventFields(item)
        return InboxRecord(
            InboxItem(
                id = "sc-1", account = item.text("account").orEmpty(), kind = item.text("kind").orEmpty(),
                title = item.text("title").orEmpty(), sender = item.text("sender"), preview = item.text("preview").orEmpty(),
                summary = item.text("summary"), tier = item.text("tier"), updatedAt = item.text("updated_at"),
                doneAt = item.text("done_at"), status = event.status, urgent = event.urgent, where = event.where,
            ),
            sourceLabel = item.text("account_name").orEmpty(), count = item.text("count")?.toIntOrNull() ?: 0,
            source = item.text("source").orEmpty(), event = event,
        )
    }

    private fun bounds(text: String): Rect =
        rule.onNodeWithText(text, useUnmergedTree = true).fetchSemanticsNode().boundsInRoot

    private fun assertComplete(text: String, context: String, checkVisualOverflow: Boolean = true) {
        val node = rule.onNodeWithText(text, useUnmergedTree = true).fetchSemanticsNode()
        val layouts = mutableListOf<TextLayoutResult>()
        node.config[SemanticsActions.GetTextLayoutResult].action!!(layouts)
        val layout = layouts.single()
        val lastGlyph = layout.getBoundingBox(text.lastIndex)
        if (checkVisualOverflow) {
            assertFalse("$context: $text must not be ellipsized or clipped: ${layout.size}", layout.hasVisualOverflow)
        }
        assertTrue("$context: the last glyph of $text must be inside its layout: ${layout.size}",
            lastGlyph.width > 0f && lastGlyph.left >= 0f && lastGlyph.right <= layout.size.width)
    }

    private fun overlaps(a: Rect, b: Rect): Boolean =
        a.left < b.right && b.left < a.right && a.top < b.bottom && b.top < a.bottom

    private fun saveStatusCrop(name: String, actions: List<Rect>) {
        val bitmap = rule.onNodeWithTag("status-layout-shot").captureToImage().asAndroidBitmap()
        val state = bounds(status)
        val top = floor(state.top - 16f).toInt().coerceIn(0, bitmap.height - 1)
        val bottom = ceil((actions + state).maxOf { it.bottom } + 16f).toInt().coerceIn(top + 1, bitmap.height)
        val crop = Bitmap.createBitmap(bitmap, 0, top, bitmap.width, bottom - top)
        assertTrue("$name drew nothing", crop.width > 0 && crop.height > 0)
        val out = File(System.getProperty("persona.shots") ?: "build/inbox-status-layout-shots").apply { mkdirs() }
        File(out, "$name.png").outputStream().use { crop.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }

    private fun drawMatrix(record: InboxRecord, fontScale: Float, expanded: Boolean, hasSay: Boolean, prefix: String) {
        val repository = FakeInboxRepository()
        repository.snapshot.value = InboxSnapshot(records = listOf(record))
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var run by mutableIntStateOf(0)
        rule.setContent {
            CompositionLocalProvider(
                LocalDensity provides Density(LocalDensity.current.density, fontScale),
                LocalInboxClock provides { clock },
                LocalInboxZone provides ZoneOffset.UTC,
            ) {
                PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                    key(run) {
                        Box(Modifier.testTag("status-layout-shot").width(400.dp).height(1800.dp)
                            .background(PersonaTheme.palette.background)) {
                            InboxEventScreen(repository, InboxEventRoute(record.item.id), navigator)
                        }
                    }
                }
            }
        }
        val failures = mutableListOf<String>()
        for (appearance in AppStyle.entries) for (night in listOf(false, true)) {
            rule.runOnIdle { style = appearance; dark = night; run++ }
            rule.waitForIdle()
            if (expanded) {
                rule.onNodeWithText("Timeline").performClick()
                rule.waitForIdle()
            }
            val context = "$appearance dark=$night fontScale=$fontScale suggestion=${record.event.suggestion != null}"
            val state = bounds(status)
            val conversation = bounds("Conversation ›")
            val actions = mutableListOf(conversation)
            if (hasSay) actions += bounds("Say something")
            try {
                assertComplete(status, context)
                assertComplete("Conversation ›", context, checkVisualOverflow = false)
                if (hasSay) {
                    assertComplete("Say something", context, checkVisualOverflow = false)
                } else {
                    rule.onNodeWithText("Say something", useUnmergedTree = true).assertDoesNotExist()
                }
                for (action in actions) {
                    assertFalse("$context: the sentence and its actions must not overlap: $state / $action", overlaps(state, action))
                    assertTrue("$context: actions belong below the complete sentence: $state / $action",
                        state.bottom <= action.top + 1f)
                }
                if (actions.size == 2) {
                    assertFalse("$context: the actions must not overlap: ${actions[0]} / ${actions[1]}",
                        overlaps(actions[0], actions[1]))
                }
            } catch (failure: AssertionError) {
                failures += failure.message ?: context
            }
            val suffix = "${appearance.name.lowercase()}-${if (night) "dark" else "light"}"
            saveStatusCrop("$prefix-$suffix", actions)
        }
        assertTrue("Status layout failures:\n${failures.joinToString("\n")}", failures.isEmpty())
    }

    @Test fun englishExpandedDefaultFontInEveryStyleAndAppearance() {
        drawMatrix(showcase(), fontScale = 1f, expanded = true, hasSay = true, prefix = "status-sc-1-expanded-en-1x")
    }

    @Test fun englishLargeTextInEveryStyleAndAppearance() {
        drawMatrix(showcase(), fontScale = 2f, expanded = false, hasSay = true, prefix = "status-sc-1-en-2x")
    }

    @Test fun pendingSuggestionLargeTextInEveryStyleAndAppearance() {
        val suggested = showcase().let {
            it.copy(event = it.event.copy(suggestion = InboxSuggestion(version = 1, kind = "reply", text = "Friday works")))
        }
        drawMatrix(suggested, fontScale = 2f, expanded = false, hasSay = false, prefix = "status-sc-1-suggestion-en-2x")
    }
}
