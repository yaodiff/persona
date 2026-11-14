package persona.feature.inbox

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.unit.dp
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.InboxEntry
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.domain.inbox.InboxItem
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator
import java.io.File

/**
 * The Inbox page at rest and with a row's swipe revealed, in each style, light and dark (#4210 T1): for the PR's
 * side-by-side pictures and for `scripts/app/android_shots_diff.py` (Brief and Board at rest must not change).
 *
 * Written against what `main` has too, so the same pictures draw its before: Done is revealed from the start on `main`
 * and from the end after this change, and the test drags the way the build it runs on opens Done (it tells the two apart
 * by whether the swipe has a tone). The PNGs go to `build/inbox-swipe-shots/`.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1000dp-xhdpi")
class InboxSwipeShotsTest {
    @get:Rule val rule = createComposeRule()

    /** Whether this build swipes as iOS does (Done from the end). `main` before the change swipes it from the start. */
    private val fromTheEnd = runCatching { Class.forName("persona.core.designsystem.SwipeTone") }.isSuccess

    private val records = listOf(
        InboxRecord(InboxItem("mail:a1:t:1", kind = "mail", title = "Q3 planning notes", sender = "Alex Example", preview = "Can you confirm the dates before Friday?",
            tier = "action", updatedAt = "2026-11-04T09:30:00Z", version = "v1"), source = "mail"),
        InboxRecord(InboxItem("mail:a1:t:2", kind = "mail", title = "Weekly report", sender = "Sam Example", preview = "This week's progress and next week's plan.",
            tier = "fyi", updatedAt = "2026-11-04T08:10:00Z", version = "v2"), source = "mail"),
        InboxRecord(InboxItem("mail:a1:t:3", kind = "mail", title = "Offsite photos", sender = "Kim Example", preview = "The photos from the offsite are up.",
            tier = "fyi", updatedAt = "2026-11-03T18:00:00Z", version = "v3", doneAt = "2026-11-04T07:00:00Z"), source = "mail"),
    )

    private class OneInbox(snapshot: InboxSnapshot) : InboxRepository {
        override val entries = MutableStateFlow(emptyList<InboxEntry>())
        override val snapshot = MutableStateFlow(snapshot)
        override val connectionState = flowOf(ConnectionState.Online)
        override val preferences = flowOf(InboxPreferences(expanded = listOf("action", "fyi", "done")))
        override suspend fun refresh() = Unit
        override fun conversation(feedId: String, startPosition: Long?) = flowOf(LarkConversation())
        override suspend fun loadOlder(feedId: String) = Unit
        override suspend fun refreshConversation(feedId: String) = Unit
        override suspend fun setDone(itemId: String, done: Boolean) = Unit
        override suspend fun readDraft(target: ReplyTarget): ReplyDraft? = null
        override suspend fun saveDraft(target: ReplyTarget, draft: ReplyDraft) = Unit
        override suspend fun reply(target: ReplyTarget, request: ReplyRequest) = Unit
        override suspend fun downloadFile(file: LarkPart.File): String = error("not used")
    }

    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }

    private fun out(): File = File("build/inbox-swipe-shots").also { it.mkdirs() }

    private fun save(name: String) {
        val bitmap = rule.onNodeWithTag("page").captureToImage().asAndroidBitmap()
        File(out(), "$name.png").outputStream().use { assertTrue(bitmap.compress(Bitmap.CompressFormat.PNG, 100, it)) }
    }

    /** The row's title, as the open row or the done row shows it. */
    private fun row(title: String) = rule.onNode(hasText(title) and hasClickAction())

    /** Drags [title]'s row the way Done opens on this build and holds it there (under the long-press time), then lets it go. */
    private fun reveal(title: String, name: String) {
        val sign = if (fromTheEnd) -1f else 1f
        row(title).performTouchInput {
            down(center)
            moveTo(center + Offset(sign * width * .08f, 0f), 50)
            moveTo(center + Offset(sign * width * .45f, 0f), 100)
        }
        rule.waitForIdle()
        save(name)
        row(title).performTouchInput { cancel() }
        rule.waitForIdle()
    }

    @Test fun everyStyleAtRestAndGlassWithTheSwipeRevealed() {
        val inbox = OneInbox(InboxSnapshot(records = records))
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                Box(Modifier.testTag("page").width(400.dp).height(1000.dp).background(PersonaTheme.palette.background)) {
                    InboxScreen(inbox, navigator)
                }
            }
        }
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            rule.waitForIdle()
            rule.mainClock.advanceTimeBy(1_000)
            rule.waitForIdle()
            val tag = "${s.name.lowercase()}-${if (d) "dark" else "light"}"
            save("rest-$tag")
            // Each style's own look for the revealed swipe: Glass fills it with the tint, Brief and Board keep their backing.
            reveal("Q3 planning notes", "done-$tag")
            reveal("Offsite photos", "back-$tag")
        }
    }
}
