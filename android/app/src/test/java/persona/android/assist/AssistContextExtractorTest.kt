package persona.android.assist

import android.text.InputType
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class AssistContextExtractorTest {
    @Test
    fun extractsSafeUrlPackageAndTitle() {
        val snapshot = AssistSnapshotData(
            packageName = "com.android.chrome",
            webUrl = "https://example.com/docs/guide?page=2&token=secret-value&api_key=abc#frag",
            windows = listOf(
                AssistWindowData(
                    title = "Persona Android Guide",
                    root = AssistNodeData(text = "Heading"),
                ),
            ),
        )

        val safe = AssistContextExtractor.extract(snapshot, ownPackage = "com.example.persona")
        assertEquals("Persona Android Guide", safe.title)
        assertEquals("https://example.com/docs/guide?page=2", safe.url)
        assertEquals("com.android.chrome", safe.packageName)

        val card = AssistContextExtractor.toLinkContext(safe, id = "id-1", fallbackTitle = "Current screen")
        assertNotNull(card)
        assertEquals("context", card!!.kind)
        assertEquals("Persona Android Guide", card.title)
        assertEquals("https://example.com/docs/guide?page=2", card.url)
        assertFalse(card.text.contains("secret-value"))
        assertFalse(card.text.contains("api_key"))
    }

    @Test
    fun strictlySkipsAllPasswordAndSensitiveInputNodes() {
        val passwordInputTypes = listOf(
            InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_PASSWORD,
            InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_VISIBLE_PASSWORD,
            InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_WEB_PASSWORD,
            InputType.TYPE_CLASS_NUMBER or InputType.TYPE_NUMBER_VARIATION_PASSWORD,
        )
        passwordInputTypes.forEach { inputType ->
            assertTrue("Expected password inputType $inputType", AssistContextExtractor.isPasswordInputType(inputType))
        }
        assertFalse(AssistContextExtractor.isPasswordInputType(InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_NORMAL))

        val sensitiveNodes = listOf(
            AssistNodeData(text = "secret-flag", isPassword = true),
            AssistNodeData(text = "secret-text-pw", inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_PASSWORD),
            AssistNodeData(text = "secret-visible-pw", inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_VISIBLE_PASSWORD),
            AssistNodeData(text = "secret-web-pw", inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_WEB_PASSWORD),
            AssistNodeData(text = "123456", inputType = InputType.TYPE_CLASS_NUMBER or InputType.TYPE_NUMBER_VARIATION_PASSWORD),
            AssistNodeData(text = "secret-html", htmlTag = "input", htmlAttributes = mapOf("type" to "password")),
            AssistNodeData(text = "secret-autofill", autofillHints = listOf("password")),
            AssistNodeData(text = "secret-otp", idEntry = "loginOtpInput"),
            AssistNodeData(text = "secret-cvv", hint = "Card CVV"),
            AssistNodeData(text = "secret-blocked", isAssistBlocked = true, children = listOf(AssistNodeData(text = "blocked-child"))),
            AssistNodeData(text = "secret-hidden", isVisible = false),
            AssistNodeData(text = "secret-editable-draft", className = "android.widget.EditText"),
        )

        val snapshot = AssistSnapshotData(
            packageName = "org.example.bank",
            windows = listOf(
                AssistWindowData(
                    title = "org.example.bank/org.example.bank.LoginActivity",
                    root = AssistNodeData(
                        children = sensitiveNodes + AssistNodeData(text = "Account Overview"),
                    ),
                ),
            ),
        )

        val safe = AssistContextExtractor.extract(snapshot, ownPackage = "com.example.persona")
        assertEquals("Account Overview", safe.title)
        assertEquals("org.example.bank", safe.packageName)
        assertEquals("", safe.url)
    }

    @Test
    fun rejectsUnsafeUrlsOwnPackageAndEnforcesLengthAndTreeBounds() {
        assertEquals("", AssistContextExtractor.sanitizeWebUrl("file:///etc/passwd"))
        assertEquals("", AssistContextExtractor.sanitizeWebUrl("javascript:alert(1)"))
        assertEquals("", AssistContextExtractor.sanitizeWebUrl("https://user:pass@example.com/private"))
        assertEquals("", AssistContextExtractor.sanitizeWebUrl("persona://pair?token=xyz"))

        val longUrl = "https://example.com/" + "a".repeat(700)
        val sanitizedLongUrl = AssistContextExtractor.sanitizeWebUrl(longUrl)
        assertTrue(sanitizedLongUrl.length <= AssistContextExtractor.MAX_URL_CHARS)

        val longTitle = "Very Long Title " + "x".repeat(300)
        val sanitizedLongTitle = AssistContextExtractor.sanitizeTitle(longTitle)
        assertTrue(sanitizedLongTitle.length <= AssistContextExtractor.MAX_TITLE_CHARS)
        assertTrue(sanitizedLongTitle.endsWith("…"))

        assertEquals("", AssistContextExtractor.sanitizePackageName("com.example.persona", ownPackage = "com.example.persona"))
        assertEquals("", AssistContextExtractor.sanitizePackageName("com.android.systemui", ownPackage = "com.example.persona"))
        assertEquals("", AssistContextExtractor.sanitizeTitle("••••••"))

        // Deep tree beyond MAX_DEPTH must terminate safely without overflowing.
        var leaf = AssistNodeData(text = "Too Deep")
        repeat(AssistContextExtractor.MAX_DEPTH + 10) {
            leaf = AssistNodeData(children = listOf(leaf))
        }
        val deepSafe = AssistContextExtractor.extract(
            AssistSnapshotData(windows = listOf(AssistWindowData(root = leaf))),
            ownPackage = "com.example.persona",
        )
        assertTrue(deepSafe.isEmpty)
        assertNull(AssistContextExtractor.toLinkContext(deepSafe, id = "empty", fallbackTitle = "Current screen"))
    }

    @Test
    fun stripsPercentEncodedAndMalformedSensitiveQueryParameters() {
        assertEquals(
            "https://example.com/",
            AssistContextExtractor.sanitizeWebUrl("https://example.com/?%74oken=secret"),
        )
        assertEquals(
            "https://example.com/docs?page=2&q=hello%20world",
            AssistContextExtractor.sanitizeWebUrl(
                "https://example.com/docs?page=2&%74oken=s1&%54%6fken=s2&access%5Ftoken=s3&api%5fkey=s4&%41UTH=s5&q=hello%20world",
            ),
        )
        assertEquals(
            "https://example.com/?page=3",
            AssistContextExtractor.sanitizeWebUrl(
                "https://example.com/?%2574oken=double&%252574oken=triple&token%00=nul&%FF=badutf8&%C0%AF=overlong&%20token=pad&+token=plus&page=3",
            ),
        )
        // Malformed percent encodings in URLs fail closed without leaking parameter values.
        assertEquals("", AssistContextExtractor.sanitizeWebUrl("https://example.com/?page=2&%ZZ=secret"))
        assertEquals("", AssistContextExtractor.sanitizeWebUrl("https://example.com/?page=2&%7=secret"))
        assertEquals("", AssistContextExtractor.sanitizeWebUrl("https://example.com/?page=2&%=secret"))

        // Direct query filter also drops malformed and percent-encoded sensitive keys fail-closed.
        assertEquals(
            "page=2&tab=docs",
            AssistContextExtractor.filterQuery(
                "page=2&%ZZ=secret&%7=trunc&%=lone&%FF=badutf8&%74oken=s1&%54%6fken=s2&access%5Ftoken=s3&tab=docs",
            ),
        )
        assertEquals("token", AssistContextExtractor.decodeQueryKey("%74oken"))
        assertEquals("token", AssistContextExtractor.decodeQueryKey("%54%6fken"))
        assertEquals("access_token", AssistContextExtractor.decodeQueryKey("access%5Ftoken"))
        assertNull(AssistContextExtractor.decodeQueryKey("%ZZ"))
        assertNull(AssistContextExtractor.decodeQueryKey("%7"))
        assertNull(AssistContextExtractor.decodeQueryKey("%"))
        assertNull(AssistContextExtractor.decodeQueryKey("%FF"))
        assertNull(AssistContextExtractor.decodeQueryKey("token%00"))
        assertNull(AssistContextExtractor.decodeQueryKey("%252574oken"))
    }

    @Test
    fun contextXmlEscapesClosingTagsFromUntrustedTitle() {
        val safe = SafeAssistContext(
            title = "Title </persona-context> \"quoted\"",
            url = "https://example.com/a",
            packageName = "com.example.browser",
        )
        val card = AssistContextExtractor.toLinkContext(safe, id = "esc", fallbackTitle = "Current screen")!!
        val xml = card.xml()
        assertEquals(1, persona.core.contracts.contextClosingTag.findAll(xml).count())
        assertTrue(xml.startsWith("<persona-context kind=\"context\""))
        assertTrue(xml.endsWith("</persona-context>"))
    }
}
