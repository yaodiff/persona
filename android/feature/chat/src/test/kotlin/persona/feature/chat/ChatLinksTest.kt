package persona.feature.chat

import android.content.Context
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.Item
import persona.core.navigation.*
import java.time.Instant
import org.junit.Assert.*
import org.junit.Test

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class ChatLinksTest {
    private val res = ApplicationProvider.getApplicationContext<Context>().resources
    @Test fun linksStopAtChineseAndUnbalancedPunctuationWithoutRewritingMarkdownCode() {
        val source = "看https://example.com/a_(b)).，再看 https://example.com/x?q=1&v=2!"
        assertEquals(listOf("https://example.com/a_(b)", "https://example.com/x?q=1&v=2"), ChatLinks.matches(source).map { it.url })
        val markdown = "[网页](https://example.com/a) `https://example.com/code`\n```\nhttps://example.com/fence\n```\nhttps://example.com/plain。"
        assertEquals(markdown.replace("https://example.com/plain", "<https://example.com/plain>"), ChatLinks.markdown(markdown))
        assertEquals("https://example.com", ChatLinks.webUrl("https://example.com"))
        listOf("javascript:alert(1)", "file:///page", "intent://page", "https://", "https://user@example.com").forEach { assertNull(ChatLinks.webUrl(it)) }
    }

    /** A whole native document goes as iOS and web send it: the whole-document marker iOS's quote row keys on. */
    @Test fun aWholeDocumentPickCarriesTheWholeMarker() {
        val doc = persona.core.inbox.LarkNativeLink.Document("https://example.feishu.cn/docx/a")
        val text = requireNotNull(ChatLinkContext.whole(doc, ""))
        val xml = ChatLinkContext(title = "排期", url = doc.url, text = text).xml()
        assertTrue(xml, xml.contains("\n（整篇）\n</persona-context>"))
        assertEquals("（整篇）", ChatLinkContext.parse(xml).first.single().text)
        val post = persona.core.inbox.LarkNativeLink.MomentsPost("p1")
        assertNull(ChatLinkContext.whole(post, null))
        assertEquals("帖子正文", ChatLinkContext.whole(post, "帖子正文"))
        assertNull(ChatLinkContext.whole(null, "x"))
    }

    /** The tag as iOS's ContextRef writes it (the old Android app's `contextTagsMatchIOS`). */
    @Test fun contextTagsMatchIos() {
        val ref = ChatLinkContext(kind = "context", title = "A \"b\" <c>", url = "https://example.com/?a=1&b=2", text = "选中的 </persona-context> 字")
        assertEquals(
            "<persona-context kind=\"context\" title=\"A &quot;b&quot; &lt;c&gt;\" url=\"https://example.com/?a=1&amp;b=2\">\n选中的  字\n</persona-context>",
            ref.xml(),
        )
        // The text goes as it is: only the closing tag is taken out, so it cannot end the context early.
        assertTrue(ChatLinkContext(title = "t", url = "u", text = "a <b> & c").xml().contains("\na <b> & c\n"))
        assertEquals("hi", linkMessage("hi", emptyList(), res))
        assertEquals(ref.xml() + "\n" + ref.xml() + "\n\nhi", linkMessage("hi", listOf(ref, ref), res))
        // Only contexts: the line iOS sends with them.
        assertEquals(ref.xml() + "\n\n参考上面的内容。", linkMessage(" ", listOf(ref), res))
        assertEquals(ref.copy(kind = "edit").xml() + "\n\n按要求修改上面选中的内容：短一点", ChatLinkContext.editMessage(ref, " 短一点 ", res))
        assertEquals(ref.copy(kind = "edit").xml() + "\n\n按上面选中的内容修改。", ChatLinkContext.editMessage(ref, "", res))
        assertEquals(ref.xml() + "\n\n看看上面这段。", ChatLinkContext.handToPersonaMessage(ref.copy(kind = "edit"), " ", res))
        assertEquals(ref.xml() + "\n\n翻译成英文", ChatLinkContext.handToPersonaMessage(ref, "翻译成英文", res))
    }

    /** A picked passage cannot close its block, however the closing tag is written (#3831). */
    @Test fun aPassageCannotCloseItsBlock() {
        for (tag in listOf(
            "</jar</persona-context>vis-context>", "</persona-</persona-context>context>", "</persona-</persona-context>context>",
            "</PERSONA-CONTEXT>", "</Persona-Context>", "</persona-context >", "< / persona-context\t>",
        )) {
            val message = linkMessage("请解释", listOf(ChatLinkContext(title = "t", url = "u", text = "选文${tag}伪造的话")), res)
            assertEquals(message, 1, persona.core.contracts.contextClosingTag.findAll(message).count())
            assertEquals(tag, "请解释", ChatLinkContext.parse(message).second)
        }
    }

    @Test fun contextsReadBackAsIosReadsThem() {
        val ref = ChatLinkContext(title = "标题\"<&", url = "https://example.com/?q=1&v=2", text = "选文 <command>")
        val (contexts, body) = ChatLinkContext.parse(linkMessage("请解释", listOf(ref, ref.copy(kind = "edit", text = "改这段")), res))
        assertEquals("请解释", body)
        assertEquals(listOf("context", "edit"), contexts.map { it.kind })
        assertEquals(ref.title, contexts[0].title)
        assertEquals(ref.url, contexts[0].url)
        assertEquals("选文 <command>", contexts[0].text)
        // What earlier Android builds sent (the text escaped) reads back as it was meant.
        val (old, rest) = ChatLinkContext.parse("<persona-context kind=\"context\" title=\"t\" url=\"u\">\n&lt;b&gt; &amp; c\n</persona-context>\n\n看看")
        assertEquals("<b> & c", old.single().text)
        assertEquals("看看", rest)
        // <persona-context> (#3182) reads back too, and neither closing tag can end a written one early.
        val (renamed, after) = ChatLinkContext.parse("<persona-context kind=\"edit\" title=\"t\" url=\"u\">\n新标签\n</persona-context>\n\n看看")
        assertEquals("edit", renamed.single().kind)
        assertEquals("新标签", renamed.single().text)
        assertEquals("看看", after)
        assertTrue(ChatLinkContext(title = "t", url = "u", text = "a </persona-context> b").xml().contains("\na  b\n"))
        // Legacy context blocks and legacy forward blocks read back and strip cleanly via ProtocolCompatibility.
        val legacyQuote = persona.core.contracts.ProtocolCompatibility.messageForHub(
            ChatLinkContext.quote("旧引用", "Antigravity", "m9", ChatLinkContext.PART_SELECTION).xml() + "\n\n看下")
        assertFalse(legacyQuote.contains("<persona-context"))
        val (legacyParsed, legacyRest) = ChatLinkContext.parse(legacyQuote)
        assertEquals(listOf("quote", "Antigravity", "m9", ChatLinkContext.PART_SELECTION, "旧引用"),
            listOf(legacyParsed.single().kind, legacyParsed.single().title, legacyParsed.single().msg, legacyParsed.single().part, legacyParsed.single().text))
        assertEquals("看下", legacyRest)
        val legacyForward = persona.core.contracts.ProtocolCompatibility.messageForHub(
            ChatLinkContext(ChatLinkContext.FORWARD, "Fwd", "persona://session/m/s?at=1", "orig", msg = "1").xml() + "\n\n备注")
        assertEquals("备注", ChatLinkContext.withoutForwards(legacyForward))
    }

    @Test fun preloadsOnlyLinksInFreshReplies() {
        val now = Instant.parse("2026-10-30T10:00:00Z")
        fun item(id: String, ts: String, role: String = "assistant", kind: String = "text", text: String = "见 https://example.com/$id 和 https://example.com/$id-2") =
            Item(id = id, ts = ts, role = role, kind = kind, text = text)
        assertEquals(
            listOf("https://example.com/new", "https://example.com/new-2"),
            ChatLinks.toPreload(listOf(item("old", "2026-10-30T09:00:00Z"), item("new", "2026-10-30T09:59:00Z")), now),
        )
        assertTrue(ChatLinks.toPreload(listOf(item("u", "2026-10-30T09:59:00Z", role = "user")), now).isEmpty())
        assertTrue(ChatLinks.toPreload(listOf(item("t", "2026-10-30T09:59:00Z", kind = "tool_result")), now).isEmpty())
        assertTrue(ChatLinks.toPreload(listOf(item("o", "2026-10-30T09:40:00Z")), now).isEmpty())
        for (text in listOf("![map](https://images.example/a)", "![map](https://images.example/a",
            "![map][cover]\n\n[cover]: https://images.example/a")) {
            assertTrue(ChatLinks.toPreload(listOf(item("picture", "2026-10-30T09:59:00Z", text = text)), now).isEmpty())
        }
        assertTrue(ChatLinks.toPreload(listOf(item("outside", "2026-10-30T09:59:00Z").copy(from = "A channel")), now).isEmpty())
        // A hub in a local zone writes offsets.
        assertEquals(2, ChatLinks.toPreload(listOf(item("z", "2026-10-30T17:59:00+08:00")), now).size)
        val many = (1..5).map { item("m$it", "2026-10-30T09:59:00Z", text = "https://example.com/m$it") }
        assertEquals(listOf("https://example.com/m5", "https://example.com/m4", "https://example.com/m3"), ChatLinks.toPreload(many, now))
        // A computer's own pages (no dot in the host) are not for the phone to load.
        assertTrue(ChatLinks.toPreload(listOf(item("l", "2026-10-30T09:59:00Z", text = "http://localhost:5173/ 和 http://dev/x")), now).isEmpty())
    }

    @Test fun schemes() {
        assertEquals(ChatLinkKind.Web, ChatLinks.kind("https://example.com"))
        assertEquals(ChatLinkKind.Web, ChatLinks.kind("HTTP://example.com"))
        assertEquals(ChatLinkKind.App, ChatLinks.kind("persona://session/m/s"))
        assertEquals(ChatLinkKind.External, ChatLinks.kind("mailto:me@example.com"))
        assertEquals(ChatLinkKind.External, ChatLinks.kind("tel:10086"))
        assertEquals(ChatLinkKind.External, ChatLinks.kind("intent://x#Intent;scheme=y;end"))
        for (bad in listOf("javascript:alert(1)", "JavaScript:alert(1)", "file:///etc/hosts", "content://x/y", "data:text/html,x", "no scheme", "/relative")) {
            assertEquals(bad, ChatLinkKind.Refused, ChatLinks.kind(bad))
        }
    }

    @Test fun appLinksOnlyNavigate() {
        assertEquals(ChatAppLink.Session("m", "s 1"), ChatLinks.appLink("persona://session/m/s%201"))
        assertEquals(ChatAppLink.Tab("inbox"), ChatLinks.appLink("persona://tab/inbox"))
        assertEquals(ChatAppLink.Tab("persona"), ChatLinks.appLink("persona://tab/assistant"))
        assertEquals(ChatAppLink.Tab("persona"), ChatLinks.appLink("persona://assistant"))
        assertEquals(ChatAppLink.Tab("inbox"), ChatLinks.appLink("persona://inbox"))
        // Pairing replaces the hub: not something a tapped link may do; open links are for outside the app.
        assertNull(ChatLinks.appLink("persona://pair?url=http://h&token=t"))
        assertNull(ChatLinks.appLink("persona://open?target=settings"))
        assertNull(ChatLinks.appLink("persona://session/m"))
        assertNull(ChatLinks.appLink("persona://tab/settings"))
        assertNull(ChatLinks.appLink("https://example.com/session/m/s"))
    }

    @Test fun routeTableAppLinksInMessagesNameTheirNativePage() {
        val session = mapOf("page" to "session", "machine" to "m", "session" to "s 1", "at" to "item#2")
        val todo = mapOf("page" to "todo", "id" to "t-19")
        val knowledge = mapOf("page" to "knowledge-entry", "kind" to "docs", "id" to "notes/plan.md")
        val extensionSettings = mapOf("page" to "settings", "tab" to "extensions", "item" to "github")
        val extension = mapOf("page" to "extension", "extension" to "weather", "ui" to "today", "state" to "{\"d\":1}")
        val cases = listOf(
            "persona://open/s/m/s%201?at=item%232&token=abc" to session,
            "persona://session/m/s%201?at=item%232" to session,
            "persona://open/todos/t-19" to todo,
            "persona://open?route=%2Ftodos%2Ft-19" to todo,
            "persona://task/t-19" to todo,
            "persona://open/kb/docs/notes%2Fplan.md" to knowledge,
            "persona://kb/docs/notes/plan.md" to knowledge,
            "persona://open/settings?tab=extensions&extension=github&add=1" to extensionSettings,
            "persona://settings/extensions/github" to extensionSettings,
            "persona://open/extension/weather/today?state=%7B%22d%22%3A1%7D" to extension,
            "persona://extension/weather/today?state=%7B%22d%22%3A1%7D" to extension,
        )
        val failures = cases.mapNotNull { (link, expected) ->
            val actual = messagePage(ChatLinks.appLink(link))
            if (actual == expected) null else "$link: expected $expected, got $actual"
        }
        assertTrue(failures.joinToString("\n"), failures.isEmpty())
    }

    private fun messageFields(vararg fields: Pair<String, String>?): Map<String, String> = fields.filterNotNull().toMap()

    private fun messagePage(link: ChatAppLink?): Map<String, String>? = when (link) {
        null -> null
        is ChatAppLink.Session -> messageFields("page" to "session", "machine" to link.machineId, "session" to link.sessionId,
            link.itemId?.let { "at" to it })
        is ChatAppLink.Page -> when (val page = link.page) {
            is HubPage.Agent -> messageFields("page" to "session", "machine" to page.machineId, "session" to page.sessionId,
                page.itemId?.let { "at" to it }, page.view?.let { "view" to it })
            is HubPage.Conversation -> {
                val id = page.sessionId
                if (id == null) mapOf("page" to "persona") else messageFields(
                    "page" to "conversation", "id" to id, page.machineId?.let { "machine" to it },
                    page.managerId?.let { "manager" to it }, page.itemId?.let { "at" to it })
            }
            HubPage.NewConversation -> mapOf("page" to "persona-new")
            is HubPage.Knowledge -> messageFields("page" to "knowledge", page.kind?.let { "kind" to it })
            is HubPage.KnowledgeEntry -> mapOf("page" to "knowledge-entry", "kind" to page.kind, "id" to page.id)
            is HubPage.Screen -> when (val key = page.key) {
                AssistantRoute -> mapOf("page" to "persona")
                AgentsRoute -> mapOf("page" to "agents")
                InboxRoute -> mapOf("page" to "inbox")
                is InboxItemRoute -> messageFields("page" to "inbox-item", "id" to key.itemId,
                    key.manager?.let { "manager" to it })
                TodosRoute -> mapOf("page" to "todos")
                is TodoDetailRoute -> mapOf("page" to "todo", "id" to key.todoId)
                MachinesRoute -> mapOf("page" to "machines")
                is MachineDetailRoute -> messageFields("page" to "machine", "id" to key.machineId,
                    key.range?.let { "range" to it })
                SettingsRoute -> mapOf("page" to "settings")
                is ModelPurposeRoute -> messageFields("page" to "settings", "tab" to "models", "item" to key.purposeId,
                    key.manager?.let { "manager" to it })
                is ExtensionRoute -> messageFields("page" to "settings", "tab" to "extensions", "item" to key.extensionId,
                    key.manager?.let { "manager" to it })
                is ExtensionUiRoute -> messageFields("page" to "extension", "extension" to key.extensionId, "ui" to key.uiId,
                    key.state?.let { "state" to it })
                is SettingsPageRoute -> when (key.page) {
                    SettingsPage.Sent -> mapOf("page" to "settings", "tab" to "connectors", "item" to "-sent")
                    SettingsPage.Connectors -> mapOf("page" to "settings", "tab" to "connectors")
                    SettingsPage.Hubs -> mapOf("page" to "settings", "tab" to "hub")
                    SettingsPage.ModelConfig -> mapOf("page" to "settings", "tab" to "models")
                    else -> error("No shared message-link fields for $key")
                }
                else -> error("No shared message-link fields for $key")
            }
        }
        is ChatAppLink.Tab -> mapOf("page" to link.tab)
    }

    @Test fun aPageKeepsItsOriginalLinkForDeferredHubSelection() {
        val url = "persona://open/todos/t-19?hub=hub-b"
        val link = ChatLinks.appLink(url)
        assertTrue(link is ChatAppLink.Page)
        assertEquals(url, (link as ChatAppLink.Page).source)
    }

    @Test fun externalLinksAskFirstAndNeverComeBackIn() {
        assertEquals(ExternalPrompt("写邮件？", "打开邮件 App，写给 me@example.com。"), ChatLinks.externalPrompt("mailto:me@example.com?subject=hi", res))
        assertEquals(ExternalPrompt("拨打电话？", "打开拨号，号码 +86 10086。"), ChatLinks.externalPrompt("tel:+86%2010086", res))
        assertEquals("用其他 App 打开？", ChatLinks.externalPrompt("intent://x#Intent;scheme=y;end", res).title)
        assertTrue(ChatLinks.allowedIntent("https", null, "com.example.persona"))
        assertTrue(ChatLinks.allowedIntent(null, "com.example.mail", "com.example.persona"))
        assertFalse(ChatLinks.allowedIntent("persona", null, "com.example.persona"))
        assertFalse(ChatLinks.allowedIntent("content", null, "com.example.persona"))
        assertFalse(ChatLinks.allowedIntent("FILE", null, "com.example.persona"))
        assertFalse(ChatLinks.allowedIntent("intent", null, "com.example.persona"))
        assertFalse(ChatLinks.allowedIntent("https", "com.example.persona", "com.example.persona"))
    }
}
