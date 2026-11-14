package persona.feature.inbox

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.Density
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.InboxChainEntry
import persona.core.contracts.InboxEventFields
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.InboxEntry
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.domain.inbox.InboxItem
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator
import java.io.File
import java.io.IOException

/** Loading, failure and genuinely missing are distinct when an event is not in the live list. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1000dp-xhdpi")
class InboxEventLookupTest {
    @get:Rule val rule = createComposeRule()
    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }

    private fun record() = InboxRecord(
        InboxItem("history", kind = "group", title = "Historical event", preview = "Old preview",
            summary = "Historical summary", status = "action", doneAt = "2026-11-01T00:00:00Z", version = "v1"),
        event = InboxEventFields(status = "action", recap = "The launch slipped a day",
            chain = listOf(InboxChainEntry("c1", kind = "received", text = "Received"))),
    )

    private class LookupInbox(var record: InboxRecord?, var failure: IOException? = null) : InboxRepository {
        override val entries = MutableStateFlow(emptyList<InboxEntry>())
        override val snapshot = MutableStateFlow(InboxSnapshot())
        override val connectionState = flowOf(ConnectionState.Online)
        val calls = mutableListOf<String>()
        var gate: CompletableDeferred<Unit>? = null
        override suspend fun item(itemId: String): InboxRecord? {
            calls += itemId
            gate?.await()
            failure?.let { throw it }
            return record
        }
        override suspend fun refresh() = Unit
        override suspend fun refreshNow() = Unit
        override fun conversation(feedId: String, startPosition: Long?) = flowOf(LarkConversation())
        override suspend fun loadOlder(feedId: String) = Unit
        override suspend fun refreshConversation(feedId: String) = Unit
        override suspend fun setDone(itemId: String, done: Boolean) = Unit
        override suspend fun readDraft(target: ReplyTarget): ReplyDraft? = null
        override suspend fun saveDraft(target: ReplyTarget, draft: ReplyDraft) = Unit
        override suspend fun reply(target: ReplyTarget, request: ReplyRequest) = Unit
        override suspend fun downloadFile(file: LarkPart.File): String = error("not used")
    }

    private fun show(inbox: LookupInbox) {
        rule.setContent { PersonaTheme(reduceMotion = true) { InboxEventScreen(inbox, InboxEventRoute("history"), navigator) } }
    }

    @Test fun loadingNeverFlashesMissingAndTheWholeEventArrives() {
        val inbox = LookupInbox(record()).apply { gate = CompletableDeferred() }
        show(inbox)
        rule.onNodeWithText("Loading the inbox").assertExists()
        rule.onNodeWithText("Can't find this item. It may have been handled or removed.").assertDoesNotExist()

        inbox.gate?.complete(Unit)
        rule.waitForIdle()
        rule.onNodeWithText("Historical event").assertExists()
        rule.onNodeWithText("Historical summary").assertExists()
        rule.onNodeWithText("The launch slipped a day").assertExists()
        assertEquals(listOf("history"), inbox.calls)
    }

    @Test fun aReadFailureRetriesWithoutBecomingMissing() {
        val inbox = LookupInbox(record(), IOException("offline"))
        show(inbox)
        rule.onNodeWithText("Couldn't refresh the inbox. Try again.").assertExists()
        rule.onNodeWithText("Can't find this item. It may have been handled or removed.").assertDoesNotExist()

        inbox.failure = null
        rule.onNodeWithText("Retry").performClick()
        rule.waitForIdle()
        rule.onNodeWithText("Historical event").assertExists()
        assertEquals(listOf("history", "history"), inbox.calls)
    }

    @Test fun onlyA404ShowsTheItemAsMissing() {
        show(LookupInbox(null))
        rule.onNodeWithText("Can't find this item. It may have been handled or removed.").assertExists()
        rule.onNodeWithText("Couldn't refresh the inbox. Try again.").assertDoesNotExist()
    }

    private enum class Mode { Loading, Failed, Missing }

    @Test fun lookupStatesInEveryAppearance() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var fontScale by mutableStateOf(1f)
        var mode by mutableStateOf(Mode.Loading)
        rule.setContent {
            CompositionLocalProvider(LocalDensity provides Density(LocalDensity.current.density, fontScale)) {
                key(style, dark, fontScale, mode) {
                    val inbox = when (mode) {
                        Mode.Loading -> LookupInbox(record()).apply { gate = CompletableDeferred() }
                        Mode.Failed -> LookupInbox(record(), IOException("offline"))
                        Mode.Missing -> LookupInbox(null)
                    }
                    PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                        Box(Modifier.fillMaxSize().background(PersonaTheme.palette.background).testTag("shot")) {
                            InboxEventScreen(inbox, InboxEventRoute("history"), navigator)
                        }
                    }
                }
            }
        }
        for (appearance in AppStyle.entries) for (night in listOf(false, true)) for (shown in Mode.entries) {
            rule.runOnIdle { style = appearance; dark = night; fontScale = 1f; mode = shown }
            rule.waitForIdle()
            capture("${shown.name.lowercase()}-${appearance.name.lowercase()}-${if (night) "dark" else "light"}-1x")
        }
        for (appearance in AppStyle.entries) for (night in listOf(false, true)) {
            rule.runOnIdle { style = appearance; dark = night; fontScale = 2f; mode = Mode.Failed }
            rule.waitForIdle()
            rule.onNodeWithText("Retry").assertExists()
            capture("failed-${appearance.name.lowercase()}-${if (night) "dark" else "light"}-2x")
        }
    }

    private fun capture(name: String) {
        val dir = File("build/inbox-history-shots").apply { mkdirs() }
        val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
        File(dir, "$name.png").outputStream().use { assertTrue(bitmap.compress(android.graphics.Bitmap.CompressFormat.PNG, 100, it)) }
    }
}
