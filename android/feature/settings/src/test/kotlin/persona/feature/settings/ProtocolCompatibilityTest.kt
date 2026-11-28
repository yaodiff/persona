package persona.feature.settings

import org.junit.Assert.*
import org.junit.Test

class ProtocolCompatibilityTest {
    @Test fun baselineExtensionSourceLinksUseTheSameInAppTargetAsCurrentLinks() {
        for (scheme in listOf("jarvis", "persona")) {
            assertEquals("test:item", ExtensionViewText.inboxSource("$scheme://inbox/test%3Aitem/source"))
            assertNull(ExtensionViewText.inboxSource("$scheme://user@example.com/inbox/test/source"))
            assertNull(ExtensionViewText.inboxSource("$scheme://inbox/test/source?other=1"))
        }
        assertNull(ExtensionViewText.inboxSource("other://inbox/test/source"))
    }

    @Test fun spaceConversationLinkStaysWithinItsExtension() {
        val link = "persona://tracks/computer-example/reading/session-example"
        assertEquals("computer-example" to "session-example", ExtensionViewText.spaceConversation(link))
        assertTrue(ExtensionViewText.opens("space-conversations", link))
        assertFalse(ExtensionViewText.opens("other", link))
        assertNull(ExtensionViewText.spaceConversation("persona://tracks/computer-example/health/session-example"))
        assertNull(ExtensionViewText.spaceConversation("persona://settings/extensions/space-conversations"))
        assertNull(ExtensionViewText.spaceConversation("https://example.com/tracks/computer-example/reading/session-example"))
    }

    @Test fun settingsLinkOpensOnlyThePagesOwnExtension() {
        for (scheme in listOf("jarvis", "persona")) {
            assertEquals("example-notes", ExtensionViewText.settings("example-notes", "$scheme://settings/extensions/example-notes"))
            assertEquals("a/b c", ExtensionViewText.settings("a/b c", "$scheme://settings/extensions/a%2Fb%20c"))
            assertTrue(ExtensionViewText.opens("example-notes", "$scheme://settings/extensions/example-notes"))
        }
        assertNull(ExtensionViewText.settings("example-notes", "persona://settings/extensions/another"))
        assertNull(ExtensionViewText.settings("example-notes", "persona://settings/extensions/example-notes/extra"))
        assertNull(ExtensionViewText.settings("example-notes", "persona://settings/extensions/example-notes?x=1"))
        assertNull(ExtensionViewText.settings("example-notes", "https://settings/extensions/example-notes"))
        assertFalse(ExtensionViewText.opens("", "persona://settings/extensions/example-notes"))
    }

    @Test fun composeLinkCarriesOnlyOneOwnQuickPromptText() {
        val link = "persona://compose?prompt=today&text=What%20happened%3F"
        assertEquals("What happened?", ExtensionViewText.composeText(link))
        assertEquals("What happened?", ExtensionViewText.composeFrom("example-notes", link))
        assertTrue(ExtensionViewText.opens("example-notes", link))
        assertTrue(ExtensionViewText.opens("another-extension", link))
        assertFalse(ExtensionViewText.opens("", link))
        for (scheme in listOf("JARVIS", "jar" + "vis")) {
            assertEquals("What happened?", ExtensionViewText.composeText("$scheme://COMPOSE/?prompt=today&text=What+happened%3F"))
            assertEquals("a+b c", ExtensionViewText.composeText("$scheme://compose?prompt=today&text=a%2Bb+c"))
        }

        for (bad in listOf(
            "persona://compose?prompt=today&text=hello&other=1",
            "persona://compose?prompt=today&prompt=again&text=hello",
            "persona://compose?prompt=today&text=hello&text=again",
            "persona://compose?prompt=today",
            "persona://compose?text=hello",
            "persona://compose/extra?prompt=today&text=hello",
            "persona://compose?prompt=today&text=hello#",
            "persona://compose?prompt=today&text=hello#fragment",
            "persona://user@compose?prompt=today&text=hello",
            "persona://compose:443?prompt=today&text=hello",
            "persona://compose?prompt=&text=hello",
            "persona://compose?prompt=two%20words&text=hello",
            "persona://compose?prompt=today&text=",
            "persona://compose?prompt=today&text=%20%20",
            "persona://ask?text=hello",
            "https://compose?prompt=today&text=hello",
        )) assertNull(bad, ExtensionViewText.composeText(bad))

        assertEquals("x", ExtensionViewText.composeText("persona://compose?prompt=${"p".repeat(64)}&text=x"))
        assertNull(ExtensionViewText.composeText("persona://compose?prompt=${"p".repeat(65)}&text=x"))
        assertEquals("😀".repeat(8_000), ExtensionViewText.composeText("persona://compose?prompt=today&text=" + "😀".repeat(8_000)))
        assertNull(ExtensionViewText.composeText("persona://compose?prompt=today&text=" + "😀".repeat(8_001)))
    }
}
