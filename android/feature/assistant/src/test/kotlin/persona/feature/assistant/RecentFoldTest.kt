package persona.feature.assistant

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.ui.Modifier
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.dp
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.After
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.designsystem.LocalHomeDrawer
import persona.core.designsystem.PersonaTheme
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import java.time.Instant

/**
 * #6228: the "Recent" fold survives a cold start (the web keeps it in localStorage, iOS in @StoredValue), and the
 * conversation currently open stays visible while folded (iOS `holdsCurrent`, the web's `holds`).
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h1200dp-xhdpi")
class RecentFoldTest {
    @get:Rule val rule = createComposeRule()

    private val context get() = ApplicationProvider.getApplicationContext<android.content.Context>()
    private val now get() = Instant.now().toString()
    private val main = Session(machineId = "hub", sessionId = "main", title = "主对话", status = "idle", updatedAt = now)
    private val recent = listOf(
        Session(machineId = "hub", sessionId = "r1", title = "整理收件箱", status = "working", updatedAt = now),
        Session(machineId = "hub", sessionId = "r2", title = "周末去哪儿", status = "idle", updatedAt = now),
    )
    private val groups = ConversationGroups(main, recent = recent)

    @Before fun clearFolds() {
        context.getSharedPreferences(SidebarFoldStore.PREFS_NAME, android.content.Context.MODE_PRIVATE)
            .edit().clear().commit()
    }

    @After fun dropFolds() = clearFolds()

    private fun drawer(active: SessionKey? = null) {
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true) {
                CompositionLocalProvider(LocalHomeDrawer provides true) {
                    Box(Modifier.width(380.dp).height(1100.dp).background(PersonaTheme.palette.background)) {
                        AssistantDrawer("", {}, groups, active = active, hasMain = true,
                            onEnd = null, onMain = {}, onOpen = {}, onInfo = {}, onNew = {}, onSettings = {},
                            onInbox = {}, inboxCount = 0, showTodos = false)
                    }
                }
            }
        }
    }

    @Test fun theChoiceIsKeptAcrossANewStoreLikeAColdStart() {
        SidebarFoldStore(context).setRecentFolded(true)
        assertTrue(SidebarFoldStore(context).recentFolded)
        SidebarFoldStore(context).setRecentFolded(false)
        assertFalse(SidebarFoldStore(context).recentFolded)
    }

    @Test fun foldedHidesTheRowsUntilTheHeaderReopensThemAndTheChoicePersists() {
        SidebarFoldStore(context).setRecentFolded(true)
        drawer()
        rule.onNodeWithText("最近").assertExists()
        rule.onNodeWithText("整理收件箱").assertDoesNotExist()
        rule.onNodeWithText("周末去哪儿").assertDoesNotExist()
        rule.onNodeWithText("最近").performClick()
        rule.onNodeWithText("整理收件箱").assertExists()
        rule.onNodeWithText("周末去哪儿").assertExists()
        assertFalse("The header writes the new choice for the next cold start", SidebarFoldStore(context).recentFolded)
    }

    @Test fun theOpenConversationInRecentStaysVisibleWhileFolded() {
        SidebarFoldStore(context).setRecentFolded(true)
        var active by androidx.compose.runtime.mutableStateOf<SessionKey?>(SessionKey("hub", "r1"))
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true) {
                CompositionLocalProvider(LocalHomeDrawer provides true) {
                    Box(Modifier.width(380.dp).height(1100.dp).background(PersonaTheme.palette.background)) {
                        AssistantDrawer("", {}, groups, active = active, hasMain = true,
                            onEnd = null, onMain = {}, onOpen = {}, onInfo = {}, onNew = {}, onSettings = {},
                            onInbox = {}, inboxCount = 0, showTodos = false)
                    }
                }
            }
        }
        rule.onNodeWithText("整理收件箱").assertExists()
        rule.onNodeWithText("周末去哪儿").assertExists()
        // Another conversation selected: the stored fold takes effect again.
        active = SessionKey("hub", "main")
        rule.waitForIdle()
        rule.onNodeWithText("整理收件箱").assertDoesNotExist()
    }

    @Test fun openByDefaultOnFirstRun() {
        drawer()
        rule.onNodeWithText("整理收件箱").assertExists()
        assertFalse(SidebarFoldStore(context).recentFolded)
    }
}
