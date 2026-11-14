package persona.feature.chat

import androidx.compose.material3.Text
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithText
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.designsystem.LocalViewBlocks
import persona.core.designsystem.ViewBlocks

/**
 * Share's picture of a reply with cards (#7261): each card block goes to the app's picture drawing (`ViewBlocks.picture`,
 * nothing on it answers, never its JSON), not to the chat's live card; the words around it stay words.
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class MessagePictureCardsTest {
    @get:Rule val rule = createComposeRule()

    private val blocks = ViewBlocks(parse = { _, _ -> null }, draw = {},
        block = { key, _, _ -> Text("live $key") },
        picture = { language, _, complete, index -> Text("picture $language $index $complete") })

    @Test fun aCardInThePictureIsDrawnForThePicture() {
        val reply = "Where the release stands.\n\n```persona-view\n{\"fallback\":\"Build done.\",\"sections\":[{\"items\":" +
            "[{\"type\":\"progress\",\"label\":\"Build\",\"ratio\":1}]}]}\n```\n\n```persona-card\n{\"type\":\"stat\",\"value\":3}\n```\n\nMore soon."
        rule.setContent {
            CompositionLocalProvider(LocalViewBlocks provides blocks) {
                MessageShareCard(MessagePicture(reply, user = false, agent = "Persona"), dark = false)
            }
        }
        rule.onNodeWithText("picture persona-view 0 true").assertExists()
        rule.onNodeWithText("picture persona-card -1 true").assertExists()
        rule.onNodeWithText("Where the release stands.", substring = true).assertExists()
        assertEquals(0, rule.onAllNodesWithText("live", substring = true).fetchSemanticsNodes().size)
        assertEquals(0, rule.onAllNodesWithText("\"sections\"", substring = true).fetchSemanticsNodes().size)
    }
}
