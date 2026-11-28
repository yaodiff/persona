package persona.feature.chat

import android.content.res.Configuration
import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.getBoundsInRoot
import androidx.compose.ui.test.getUnclippedBoundsInRoot
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.DpRect
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.height
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Interaction
import persona.core.contracts.Machine
import persona.core.contracts.Resolution
import persona.core.contracts.Session
import persona.core.contracts.SessionCapabilities
import persona.core.contracts.SessionKey
import persona.core.contracts.api.AgentLauncher
import persona.core.contracts.api.AgentModel
import persona.core.contracts.api.AssistantConfig
import persona.core.contracts.api.AssistantManagement
import persona.core.contracts.api.AssistantNotes
import persona.core.contracts.api.ChatRepository
import persona.core.contracts.api.ChatSnapshot
import persona.core.contracts.api.InteractionCenter
import persona.core.contracts.api.LauncherModels
import persona.core.contracts.api.MemoryFile
import persona.core.contracts.api.MessageSender
import persona.core.contracts.api.OutboxMessage
import persona.core.contracts.api.Run
import persona.core.contracts.api.ScheduledTask
import persona.core.contracts.api.SessionsRepository
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import java.io.File
import kotlin.math.abs

/**
 * Verifies that Brief and Board styles place the launcher chip and permission mode hint chip in one wrapping FlowRow
 * above the composer while keeping suggestions/context cards below and Glass unchanged.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class ComposerHintChipsRowTest {
    @get:Rule val rule = createComposeRule()

    private val management = object : AssistantManagement {
        override suspend fun config() = AssistantConfig(machineId = "box", launcher = "agy", main = "main")
        override suspend fun assistantLaunchers(machineId: String) = listOf(AgentLauncher("agy"))
        override suspend fun launcherModels(machineId: String, launcher: String) =
            LauncherModels(agent = "agy", models = listOf(AgentModel("default", name = "Default", isDefault = true)))
        override suspend fun warm() = Unit
        override suspend fun endConversation(session: SessionKey) = Unit
        override suspend fun tasks() = emptyList<ScheduledTask>()
        override suspend fun saveTask(task: ScheduledTask) = task
        override suspend fun deleteTask(id: String) = Unit
        override suspend fun runTask(id: String) = SessionKey("box", "main")
        override suspend fun freshTask(id: String) = ScheduledTask(id)
        override suspend fun runs() = emptyList<Run>()
        override suspend fun notes() = AssistantNotes()
        override suspend fun setNotes(text: String) = Unit
        override suspend fun memory() = emptyList<MemoryFile>()
        override suspend fun saveMemory(name: String, content: String) = Unit
        override suspend fun deleteMemory(name: String) = Unit
    }

    private fun mainSession(suggestion: String? = null) = Session(
        machineId = "box",
        sessionId = "main",
        assistant = true,
        status = "idle",
        state = "idle",
        launcher = "agy",
        permissionMode = "bypassPermissions",
        suggestion = suggestion,
        capabilities = SessionCapabilities(read = true, send = true, modes = true),
    )

    private fun DpRect.centerY(): Float = (top.value + bottom.value) / 2f

    private fun maybeSaveShot(tag: String, fileName: String) {
        val dir = File(System.getProperty("persona.shots") ?: "build/composer-hint-chips-shots").apply { val _ = mkdirs() }
        val bitmap = rule.onNodeWithTag(tag).captureToImage().asAndroidBitmap()
        File(dir, fileName).outputStream().use { stream ->
            assertTrue("Failed to encode $fileName", bitmap.compress(Bitmap.CompressFormat.PNG, 100, stream))
        }
    }

    private fun assertSameHorizontalRow(first: DpRect, second: DpRect, context: String) {
        assertTrue(
            "$context: chips must not overlap horizontally ($first vs $second)",
            first.right.value + Tokens.Space.s.value <= second.left.value + 0.5f,
        )
        assertTrue(
            "$context: chip centerY must align ($first vs $second)",
            abs(first.centerY() - second.centerY()) <= 1.5f,
        )
        assertTrue(
            "$context: second chip must vertically sit within the same row band ($first vs $second)",
            second.top >= first.top && second.bottom <= first.bottom,
        )
    }

    private fun assertUnclippedInside(bounds: DpRect, unclipped: DpRect, container: DpRect, label: String) {
        assertEquals("$label must not be clipped", unclipped, bounds)
        assertTrue("$label left out of bounds: $bounds in $container", bounds.left >= container.left - 0.5.dp)
        assertTrue("$label right out of bounds: $bounds in $container", bounds.right <= container.right + 0.5.dp)
    }

    @Test fun briefAndBoardPlaceLauncherAndModeHintOnOneRowAtPhoneWidth() {
        var style by mutableStateOf(AppStyle.Brief)
        var dark by mutableStateOf(false)
        val session = mainSession()
        rule.setContent {
            val configuration = Configuration(LocalConfiguration.current).apply {
                uiMode = (uiMode and Configuration.UI_MODE_NIGHT_MASK.inv()) or
                    if (dark) Configuration.UI_MODE_NIGHT_YES else Configuration.UI_MODE_NIGHT_NO
            }
            CompositionLocalProvider(LocalConfiguration provides configuration) {
                PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                    Box(Modifier.width(400.dp).background(PersonaTheme.palette.background).testTag("composer-container")) {
                        LargeWindowComposer(
                            session = session.key,
                            state = rememberChatComposerState(),
                            working = false,
                            sending = false,
                            canClear = false,
                            service = null,
                            onSend = {},
                            onCommand = {},
                            onNotice = {},
                            assistantChat = true,
                            hintChips = {
                                ConversationLauncherChip(management, "box", "main", session.launcher, null, null) { _, _ -> }
                            },
                            modeHint = chatModeHint(session, includeModes = true),
                        )
                    }
                }
            }
        }
        for (targetStyle in listOf(AppStyle.Brief, AppStyle.Board)) for (night in listOf(false, true)) {
            rule.runOnIdle { style = targetStyle; dark = night }
            rule.waitUntil(5_000) {
                rule.onAllNodes(hasText("Default: agy")).fetchSemanticsNodes().isNotEmpty()
            }
            rule.waitForIdle()
            val container = rule.onNodeWithTag("composer-container").getBoundsInRoot()
            val launcherNode = rule.onNodeWithText("Default: agy").assertIsDisplayed()
            val modeNode = rule.onNodeWithText("Full access").assertIsDisplayed()
            val modePillNode = rule.onNodeWithTag("chat-composer-mode-hint").assertIsDisplayed()
            val launcherBounds = launcherNode.getBoundsInRoot()
            val modeBounds = modeNode.getBoundsInRoot()
            val modePillBounds = modePillNode.getBoundsInRoot()
            assertUnclippedInside(launcherBounds, launcherNode.getUnclippedBoundsInRoot(), container, "$targetStyle launcher")
            assertUnclippedInside(modeBounds, modeNode.getUnclippedBoundsInRoot(), container, "$targetStyle modeHint")
            assertUnclippedInside(modePillBounds, modePillNode.getUnclippedBoundsInRoot(), container, "$targetStyle modeHint pill")
            assertSameHorizontalRow(launcherBounds, modeBounds, "$targetStyle/${if (night) "dark" else "light"}")
            assertEquals("$targetStyle: side-by-side mode chip matches launcher height", launcherBounds.height.value, modePillBounds.height.value, 0.5f)
            assertEquals("$targetStyle: side-by-side mode chip top aligns with launcher", launcherBounds.top.value, modePillBounds.top.value, 0.5f)
            assertEquals("$targetStyle: side-by-side mode chip bottom aligns with launcher", launcherBounds.bottom.value, modePillBounds.bottom.value, 0.5f)
            maybeSaveShot("composer-container", "composer-chips-${targetStyle.name.lowercase()}-${if (night) "dark" else "light"}.png")
        }
    }

    @Test fun briefAndBoardWrapWithoutClippingOnVeryNarrowWidth() {
        var style by mutableStateOf(AppStyle.Brief)
        val session = mainSession()
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = style) {
                Box(Modifier.width(180.dp).background(PersonaTheme.palette.background).testTag("composer-container")) {
                    LargeWindowComposer(
                        session = session.key,
                        state = rememberChatComposerState(),
                        working = false,
                        sending = false,
                        canClear = false,
                        service = null,
                        onSend = {},
                        onCommand = {},
                        onNotice = {},
                        assistantChat = true,
                        hintChips = {
                            ConversationLauncherChip(management, "box", "main", session.launcher, null, null) { _, _ -> }
                        },
                        modeHint = chatModeHint(session, includeModes = true),
                    )
                }
            }
        }
        for (targetStyle in listOf(AppStyle.Brief, AppStyle.Board)) {
            rule.runOnIdle { style = targetStyle }
            rule.waitUntil(5_000) {
                rule.onAllNodes(hasText("Default: agy")).fetchSemanticsNodes().isNotEmpty()
            }
            rule.waitForIdle()
            val container = rule.onNodeWithTag("composer-container").getBoundsInRoot()
            val launcherNode = rule.onNodeWithText("Default: agy").assertIsDisplayed()
            val modeNode = rule.onNodeWithText("Full access").assertIsDisplayed()
            val launcherBounds = launcherNode.getBoundsInRoot()
            val modeBounds = modeNode.getBoundsInRoot()
            assertUnclippedInside(launcherBounds, launcherNode.getUnclippedBoundsInRoot(), container, "$targetStyle narrow launcher")
            assertUnclippedInside(modeBounds, modeNode.getUnclippedBoundsInRoot(), container, "$targetStyle narrow modeHint")
            assertTrue(
                "$targetStyle narrow: modeHint should wrap below launcher without overlap ($launcherBounds vs $modeBounds)",
                modeBounds.top >= launcherBounds.bottom,
            )
            maybeSaveShot("composer-container", "composer-chips-narrow-${targetStyle.name.lowercase()}.png")
        }
    }

    @Test
    @Config(fontScale = 2f)
    fun briefAndBoardWrapWithoutClippingAtLargeFontScale() {
        var style by mutableStateOf(AppStyle.Brief)
        var dark by mutableStateOf(false)
        val session = mainSession()
        rule.setContent {
            val configuration = Configuration(LocalConfiguration.current).apply {
                uiMode = (uiMode and Configuration.UI_MODE_NIGHT_MASK.inv()) or
                    if (dark) Configuration.UI_MODE_NIGHT_YES else Configuration.UI_MODE_NIGHT_NO
            }
            CompositionLocalProvider(LocalConfiguration provides configuration) {
                PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                    Box(Modifier.width(280.dp).background(PersonaTheme.palette.background).testTag("composer-container")) {
                        LargeWindowComposer(
                            session = session.key,
                            state = rememberChatComposerState(),
                            working = false,
                            sending = false,
                            canClear = false,
                            service = null,
                            onSend = {},
                            onCommand = {},
                            onNotice = {},
                            assistantChat = true,
                            hintChips = {
                                ConversationLauncherChip(management, "box", "main", session.launcher, null, null) { _, _ -> }
                            },
                            modeHint = chatModeHint(session, includeModes = true),
                        )
                    }
                }
            }
        }
        for (targetStyle in listOf(AppStyle.Brief, AppStyle.Board)) for (night in listOf(false, true)) {
            rule.runOnIdle { style = targetStyle; dark = night }
            rule.waitUntil(5_000) {
                rule.onAllNodes(hasText("Default: agy")).fetchSemanticsNodes().isNotEmpty()
            }
            rule.waitForIdle()
            val container = rule.onNodeWithTag("composer-container").getBoundsInRoot()
            val launcherNode = rule.onNodeWithText("Default: agy").assertIsDisplayed()
            val modeNode = rule.onNodeWithText("Full access").assertIsDisplayed()
            val launcherBounds = launcherNode.getBoundsInRoot()
            val modeBounds = modeNode.getBoundsInRoot()
            assertUnclippedInside(launcherBounds, launcherNode.getUnclippedBoundsInRoot(), container, "$targetStyle 2x launcher")
            assertUnclippedInside(modeBounds, modeNode.getUnclippedBoundsInRoot(), container, "$targetStyle 2x modeHint")
            assertTrue(
                "$targetStyle 2x font: modeHint should wrap below launcher without overlap ($launcherBounds vs $modeBounds)",
                modeBounds.top >= launcherBounds.bottom,
            )
            maybeSaveShot("composer-container", "composer-chips-large-${targetStyle.name.lowercase()}-${if (night) "dark" else "light"}.png")
        }
    }

    @Test fun singleChipAndEmptyChipRowRenderWithoutExtraSpace() {
        var showLauncher by mutableStateOf(true)
        var showModeHint by mutableStateOf(false)
        var sessionId by mutableStateOf("main")
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Brief) {
                Box(Modifier.width(400.dp).testTag("composer-container")) {
                    LargeWindowComposer(
                        session = SessionKey("box", sessionId),
                        state = rememberChatComposerState(),
                        working = false,
                        sending = false,
                        canClear = false,
                        service = null,
                        onSend = {},
                        onCommand = {},
                        onNotice = {},
                        assistantChat = true,
                        hintChips = if (showLauncher) ({
                            ConversationLauncherChip(management, "box", sessionId, "agy", null, null) { _, _ -> }
                        }) else null,
                        modeHint = if (showModeHint) "Full access" else null,
                    )
                }
            }
        }

        // Only launcher chip present.
        rule.waitUntil(5_000) { rule.onAllNodes(hasText("Default: agy")).fetchSemanticsNodes().isNotEmpty() }
        rule.onNodeWithText("Default: agy").assertIsDisplayed()
        rule.onNodeWithText("Full access").assertDoesNotExist()

        // Only modeHint chip present.
        rule.runOnIdle { showLauncher = false; showModeHint = true }
        rule.waitForIdle()
        rule.onNodeWithText("Default: agy").assertDoesNotExist()
        val soloModeBounds = rule.onNodeWithText("Full access").assertIsDisplayed().getBoundsInRoot()

        // Non-main conversation keeps ConversationLauncherChip composed (returning early) alongside modeHint: no leading gap.
        rule.runOnIdle { sessionId = "side"; showLauncher = true; showModeHint = true }
        rule.waitForIdle()
        rule.onNodeWithText("Default: agy").assertDoesNotExist()
        val sideModeBounds = rule.onNodeWithText("Full access").assertIsDisplayed().getBoundsInRoot()
        assertEquals("Empty launcher slot must not shift modeHint horizontally", soloModeBounds.left, sideModeBounds.left)

        // Neither chip rendered when both slots are absent vs non-main launcher slot returning early: identical height.
        rule.runOnIdle { showLauncher = false; showModeHint = false }
        rule.waitForIdle()
        val noChipHeight: Dp = rule.onNodeWithTag("composer-container").getBoundsInRoot().height
        rule.runOnIdle { sessionId = "side"; showLauncher = true; showModeHint = false }
        rule.waitForIdle()
        val emptySlotHeight: Dp = rule.onNodeWithTag("composer-container").getBoundsInRoot().height
        assertEquals("Empty hintChips slot must add no vertical space", noChipHeight, emptySlotHeight)
    }

    @Test fun suggestionAndContextCardsStayBelowTheHintChipRow() {
        var showContext by mutableStateOf(false)
        val session = mainSession(suggestion = "Summarize today's notes")
        val context = ChatLinkContext(url = "https://example.com/spec", title = "Design spec", text = "Quoted excerpt")
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Board) {
                Box(Modifier.width(400.dp)) {
                    val state = rememberChatComposerState()
                    LargeWindowComposer(
                        session = session.key,
                        state = state,
                        working = false,
                        sending = false,
                        canClear = false,
                        service = null,
                        onSend = {},
                        onCommand = {},
                        onNotice = {},
                        assistantChat = true,
                        hintChips = {
                            ConversationLauncherChip(management, "box", "main", session.launcher, null, null) { _, _ -> }
                        },
                        modeHint = chatModeHint(session, includeModes = true),
                        accessory = {
                            if (!showContext) ComposerSuggestion(session, state, onSend = {})
                            else ComposerContextCards(listOf(context)) {}
                        },
                    )
                }
            }
        }
        rule.waitUntil(5_000) { rule.onAllNodes(hasText("Default: agy")).fetchSemanticsNodes().isNotEmpty() }
        val launcherBounds = rule.onNodeWithText("Default: agy").assertIsDisplayed().getBoundsInRoot()
        val modeBounds = rule.onNodeWithText("Full access").assertIsDisplayed().getBoundsInRoot()
        assertSameHorizontalRow(launcherBounds, modeBounds, "Board with suggestion")

        val suggestionBounds = rule.onNodeWithText("Summarize today's notes").assertIsDisplayed().getBoundsInRoot()
        val chipRowBottom = maxOf(launcherBounds.bottom, modeBounds.bottom)
        assertTrue("Suggestion must sit below the hint chip row ($chipRowBottom vs $suggestionBounds)", suggestionBounds.top >= chipRowBottom)

        rule.runOnIdle { showContext = true }
        rule.waitForIdle()
        val contextBounds = rule.onNodeWithText("Quoted excerpt").assertIsDisplayed().getBoundsInRoot()
        assertTrue("Context card must sit below the hint chip row ($chipRowBottom vs $contextBounds)", contextBounds.top >= chipRowBottom)
    }

    @Test fun chatScreenWiresBriefAndBoardToSingleRowAndKeepsGlassUnchanged() {
        val row = mainSession()
        val chats = object : ChatRepository {
            override fun observe(session: SessionKey) = MutableStateFlow(ChatSnapshot(loading = false))
            override suspend fun loadOlder(session: SessionKey) = Unit
            override suspend fun expandFold(session: SessionKey, fromSeq: Int, toSeq: Int) = emptyList<persona.core.contracts.Item>()
        }
        val sessions = object : SessionsRepository {
            override val machines = MutableStateFlow(listOf(Machine("box", "workstation", online = true)))
            override val sessions = MutableStateFlow(listOf(row))
            override suspend fun refresh() = Unit
        }
        val sender = object : MessageSender {
            override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
            override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) = Unit
        }
        val interactions = object : InteractionCenter {
            override val pending = MutableStateFlow(emptyList<Interaction>())
            override suspend fun refresh() = Unit
            override suspend fun resolve(id: String, resolution: Resolution) = Unit
        }
        var style by mutableStateOf(AppStyle.Brief)
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = style) {
                ChatScreen(
                    sessionKey = row.key,
                    repository = chats,
                    sender = sender,
                    interactions = interactions,
                    sessions = sessions,
                    onBack = {},
                    renderStyle = ChatRenderStyle.Assistant,
                    chrome = ChatChromeBindings(assistantManagement = management),
                )
            }
        }
        for (targetStyle in listOf(AppStyle.Brief, AppStyle.Board)) {
            rule.runOnIdle { style = targetStyle }
            rule.waitUntil(10_000) {
                rule.onAllNodes(hasText("Default: agy")).fetchSemanticsNodes().isNotEmpty() &&
                    rule.onAllNodes(hasText("Full access")).fetchSemanticsNodes().isNotEmpty()
            }
            val launcherBounds = rule.onNodeWithText("Default: agy").assertIsDisplayed().getBoundsInRoot()
            val modeBounds = rule.onNodeWithText("Full access").assertIsDisplayed().getBoundsInRoot()
            val modePillBounds = rule.onNodeWithTag("chat-composer-mode-hint").assertIsDisplayed().getBoundsInRoot()
            assertSameHorizontalRow(launcherBounds, modeBounds, "ChatScreen $targetStyle")
            assertEquals("ChatScreen $targetStyle: mode chip matches launcher height", launcherBounds.height.value, modePillBounds.height.value, 0.5f)
            assertEquals("ChatScreen $targetStyle: mode chip top aligns with launcher", launcherBounds.top.value, modePillBounds.top.value, 0.5f)
            assertEquals("ChatScreen $targetStyle: mode chip bottom aligns with launcher", launcherBounds.bottom.value, modePillBounds.bottom.value, 0.5f)
        }

        // Glass renders neither the Brief/Board launcher chip nor the modeHint chip.
        rule.runOnIdle { style = AppStyle.Glass }
        rule.waitForIdle()
        rule.onNodeWithText("Default: agy").assertDoesNotExist()
        rule.onNodeWithText("Full access").assertDoesNotExist()
    }

    @Test fun modeHintOpensPermissionsWhenEnabledAndOverriddenLauncherStaysAligned() {
        var style by mutableStateOf(AppStyle.Board)
        var enabled by mutableStateOf(true)
        var permissionTaps = 0
        val session = mainSession()
        rule.setContent {
            PersonaTheme(darkTheme = true, reduceMotion = true, style = style) {
                Box(Modifier.width(400.dp).background(PersonaTheme.palette.background).testTag("composer-container")) {
                    LargeWindowComposer(
                        session = session.key,
                        state = rememberChatComposerState(),
                        working = false,
                        sending = false,
                        canClear = false,
                        service = null,
                        onSend = {},
                        onCommand = {},
                        onNotice = {},
                        assistantChat = true,
                        enabled = enabled,
                        hintChips = {
                            ConversationLauncherChip(management, "box", "main", session.launcher, "codex", null) { _, _ -> }
                        },
                        modeHint = chatModeHint(session, includeModes = true),
                        onPermissions = { permissionTaps++ },
                    )
                }
            }
        }
        for (targetStyle in listOf(AppStyle.Board, AppStyle.Brief)) {
            rule.runOnIdle { style = targetStyle; enabled = true }
            rule.waitUntil(5_000) { rule.onAllNodes(hasText("codex")).fetchSemanticsNodes().isNotEmpty() }
            val container = rule.onNodeWithTag("composer-container").getBoundsInRoot()
            val launcherNode = rule.onNodeWithText("codex").assertIsDisplayed()
            val modeNode = rule.onNodeWithText("Full access").assertIsDisplayed()
            assertUnclippedInside(launcherNode.getBoundsInRoot(), launcherNode.getUnclippedBoundsInRoot(), container, "$targetStyle override launcher")
            assertUnclippedInside(modeNode.getBoundsInRoot(), modeNode.getUnclippedBoundsInRoot(), container, "$targetStyle override mode")
            assertSameHorizontalRow(launcherNode.getBoundsInRoot(), modeNode.getBoundsInRoot(), "$targetStyle override")
            maybeSaveShot("composer-container", "composer-chips-overridden-${targetStyle.name.lowercase()}-dark.png")

            val before = permissionTaps
            modeNode.performClick()
            assertEquals("$targetStyle tapping modeHint invokes onPermissions", before + 1, permissionTaps)

            rule.runOnIdle { enabled = false }
            rule.waitForIdle()
            modeNode.performClick()
            assertEquals("$targetStyle disabled composer keeps modeHint non-interactive", before + 1, permissionTaps)
        }
    }
}
