package persona.feature.files

import persona.core.contracts.JsonCodec
import persona.core.contracts.api.*
import persona.core.designsystem.Status
import org.junit.Assert.*
import org.junit.Test
import org.junit.runner.RunWith
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.robolectric.RuntimeEnvironment
import org.robolectric.annotation.Config
import java.time.Instant

/** The node's answers as they come (shapes from a real node; times with the machine's offset), read by the Git pages. */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class GitModelsTest {
    private val res get() = RuntimeEnvironment.getApplication().resources

    @Test fun decodesStatus() {
        val status = JsonCodec.decodeFromString<SessionGitStatus>(
            """{"is_repo":true,"root":"/r","branch":"main","upstream":"origin/main","ahead":2,"behind":0,"stashes":1,
               "head":{"hash":"abc123","short":"abc","subject":"x","author":"a","date":"2026-06-01T10:00:00+08:00","refs":"HEAD -> main, origin/main, tag: v1"},
               "files":[{"path":"a/b.kt","index":"M","worktree":"M","added":3,"deleted":1},
                        {"path":"n.txt","index":"?","worktree":"?"},
                        {"path":"c.go","orig_path":"old.go","index":"R","worktree":"."},
                        {"path":"m.go","index":"U","worktree":"U"}],
               "commits":[{"hash":"abc123","short":"abc","subject":"x","author":"a","date":"0001-01-01T00:00:00Z"}],"extra":1}""",
        )
        assertTrue(status.isRepo)
        assertEquals(listOf("main", "origin/main", "tag: v1"), status.head!!.refNames)
        assertEquals(Instant.parse("2026-06-01T02:00:00Z"), fileTime(status.head!!.date))
        assertNull(fileTime(status.commits[0].date)) // Go's zero time
        assertNull(fileDate(res, status.commits[0].date))
        val (partly, untracked, renamed, conflict) = status.files
        assertEquals("M", partly.letter)
        assertTrue(partly.staged && partly.unstaged)
        assertEquals("部分已暂存", res.getString(partly.stage))
        assertEquals(Status.Unread, partly.tone)
        assertEquals("b.kt", partly.name)
        assertEquals("a", partly.folder)
        assertEquals("U", untracked.letter)
        assertFalse(untracked.staged)
        assertEquals("未跟踪", res.getString(untracked.stage))
        assertEquals(Status.Unknown, untracked.tone)
        assertEquals("R", renamed.letter)
        assertTrue(renamed.staged && !renamed.unstaged)
        assertEquals("old.go", renamed.originalPath)
        assertEquals("", renamed.folder)
        assertEquals("已暂存", res.getString(renamed.stage))
        assertEquals("!", conflict.letter)
        assertEquals("有冲突", res.getString(conflict.stage))
        assertEquals(Status.NeedsYou, conflict.tone)
        assertEquals("↑2  贮藏 1", gitSync(res, status))
    }

    @Test fun nodeAnswersDecode() {
        // As a node answers for a throwaway repository: the machine's offset on every time, nanoseconds on files.
        val status = JsonCodec.decodeFromString<SessionGitStatus>(
            """{"is_repo":true,"root":"/tmp/t","branch":"master","head":{"hash":"321e4a2395fe01a647752b26d29581827f01a1bf","short":"321e4a2","subject":"init","author":"t","date":"2026-10-31T13:58:40+08:00","refs":"HEAD -> master"},"ahead":0,"behind":0,"files":[{"path":"src/a.txt","index":".","worktree":"M","added":2,"deleted":1},{"path":"n.txt","index":"?","worktree":"?","added":0,"deleted":0}],"stashes":0,"commits":[]}""",
        )
        assertEquals(Instant.parse("2026-10-31T05:58:40Z"), fileTime(status.head?.date))
        assertEquals(listOf("master"), status.head?.refNames)
        assertEquals(listOf("M", "U"), status.files.map { it.letter })
        assertEquals("未暂存", res.getString(status.files[0].stage))
        assertNull(gitSync(res, status)) // no upstream, no stash: nothing to say
        val listing = JsonCodec.decodeFromString<SessionDirectory>(
            """{"entries":[{"name":"src","dir":true,"size":4096,"mtime":"2026-10-31T13:58:40.205893083+08:00"}],"path":"/tmp/t","truncated":false}""",
        )
        assertEquals(Instant.parse("2026-10-31T05:58:40.205893083Z"), fileTime(listing.entries.single().mtime))
        val synced = status.copy(upstream = "origin/master")
        assertEquals("已同步", gitSync(res, synced))
        assertEquals("↑1  ↓3", gitSync(res, synced.copy(ahead = 1, behind = 3)))
        // a deleted upstream (#1884) is not "up to date"
        val gone = JsonCodec.decodeFromString<SessionGitStatus>(
            """{"is_repo":true,"branch":"feature","upstream":"origin/feature","gone":true,"ahead":0,"behind":0,"files":[],"stashes":0,"commits":[]}""",
        )
        assertEquals("远端已删除", gitSync(res, gone))
        assertFalse(status.gone)
    }

    @Test fun refNamesDropABareHead() {
        assertEquals(emptyList<String>(), SessionGitCommit("h", refs = "HEAD").refNames)
        assertEquals(emptyList<String>(), SessionGitCommit("h").refNames)
        assertEquals(listOf("feat/x", "upstream/feat/x"), SessionGitCommit("h", refs = "HEAD -> feat/x, upstream/feat/x").refNames)
    }

    @Test fun theUpstreamsRemoteTellsLocalBranchesFromRemoteOnes() {
        assertTrue(isLocalBranch("main"))
        assertTrue(isLocalBranch("feat/x"))
        assertFalse(isLocalBranch("origin/main"))
        assertFalse(isLocalBranch("tag: v1"))
        assertEquals("upstream", remoteOf("upstream/feat/x"))
        assertEquals("origin", remoteOf(null))
        assertTrue(isLocalBranch("origin/x", remote = "upstream"))
        assertFalse(isLocalBranch("upstream/feat/x", remote = remoteOf("upstream/feat/x")))
        assertEquals("abc1234", SessionGitCommit("abc1234def", short = "").shortHash)
    }

    @Test fun splitsAShowPerFileKeepingSpaces() {
        val show = "diff --git a/a.txt b/a.txt\n@@ -1 +1 @@\n-x\n+y\ndiff --git a/my dir/b c.txt b/my dir/b c.txt\n@@ -0,0 +1 @@\n+z"
        val parts = GitDiffs.split(show)
        assertEquals(setOf("a.txt", "my dir/b c.txt"), parts.keys)
        assertTrue(parts.getValue("my dir/b c.txt").endsWith("+z"))
        assertEquals(listOf(FileLineKind.Info, FileLineKind.Delete, FileLineKind.Add),
            fileLines(parts.getValue("a.txt"), diff = true).map { it.kind })
        val last = GitDiffs.part(show, SessionGitFile("my dir/b c.txt"))
        assertTrue(last.last)
        assertTrue(last.diff!!.endsWith("+z"))
        assertFalse(GitDiffs.part(show, SessionGitFile("a.txt")).last)
        // A cut diff (the node stops at 512 KB) may not reach a file at all.
        assertNull(GitDiffs.part(show, SessionGitFile("later.txt")).diff)
    }

    @Test fun aFilesDiffShowsItsHunksNotItsHeader() {
        val diff = "diff --git a/q.sql b/q.sql\nindex 1..2 100644\n--- a/q.sql\n+++ b/q.sql\n@@ -10,3 +10,3 @@ select\n keep\n--- old comment\n+-- new comment\n\\ No newline at end of file"
        val lines = fileLines(diff, diff = true)
        assertEquals(listOf(FileLineKind.Info, FileLineKind.Context, FileLineKind.Delete, FileLineKind.Add, FileLineKind.Info), lines.map { it.kind })
        // Inside a hunk "--- x" is a removed "-- x", not a header.
        assertEquals(FileTextLine("-- old comment", old = 11, kind = FileLineKind.Delete), lines[2])
        assertEquals(FileTextLine("-- new comment", new = 11, kind = FileLineKind.Add), lines[3])
        val created = fileLines("diff --git a/n.txt b/n.txt\nnew file mode 100644\n--- /dev/null\n+++ b/n.txt\n@@ -0,0 +1 @@\n+hi", diff = true)
        assertEquals(listOf("new file mode 100644", "@@ -0,0 +1 @@", "hi"), created.map { it.text })
    }

    @Test fun aRenamedFileIsFoundUnderEitherName() {
        val show = "diff --git a/old.go b/old.go\ndeleted file mode 100644\n--- a/old.go\n+++ /dev/null\n@@ -1 +0,0 @@\n-x"
        assertNotNull(GitDiffs.part(show, SessionGitFile("c.go", originalPath = "old.go", index = "R")).diff)
    }

    @Test fun splitsQuotedNonAsciiAndEscapedGitDiffPaths() {
        // Git C-quotes non-ASCII paths by default (core.quotePath=true) and always quotes paths with tabs, quotes, or backslashes.
        val chinese = "文档/设计.md"
        val escaped = "a\t\"b\"\\c.txt"
        val emojiTab = "🚀\tnotes.md"
        val show = listOf(
            """diff --git "a/\346\226\207\346\241\243/\350\256\276\350\256\241.md" "b/\346\226\207\346\241\243/\350\256\276\350\256\241.md"""",
            "@@ -1 +1 @@",
            "-old",
            "+new",
            """diff --git "a/a\t\"b\"\\c.txt" "b/a\t\"b\"\\c.txt"""",
            "@@ -0,0 +1 @@",
            "+escaped",
            """diff --git "a/🚀\tnotes.md" "b/🚀\tnotes.md"""",
            "@@ -0,0 +1 @@",
            "+emoji",
            """diff --git a/plain.txt "b/\346\226\260.txt"""",
            "similarity index 100%",
            "rename from plain.txt",
            """rename to "\346\226\260.txt"""",
            """diff --git "a/\346\227\247.txt" b/ascii.txt""",
            "similarity index 100%",
            """rename from "\346\227\247.txt"""",
            "rename to ascii.txt",
        ).joinToString("\n")
        val parts = GitDiffs.split(show)
        assertEquals(setOf(chinese, escaped, emojiTab, "新.txt", "ascii.txt"), parts.keys)
        assertTrue(GitDiffs.part(show, SessionGitFile(chinese)).diff!!.endsWith("+new"))
        assertTrue(GitDiffs.part(show, SessionGitFile(escaped)).diff!!.endsWith("+escaped"))
        assertTrue(GitDiffs.part(show, SessionGitFile(emojiTab)).diff!!.endsWith("+emoji"))
        assertNotNull(GitDiffs.part(show, SessionGitFile("新.txt", originalPath = "plain.txt", index = "R")).diff)
        assertNotNull(GitDiffs.part(show, SessionGitFile("ascii.txt", originalPath = "旧.txt", index = "R")).diff)
    }

    @Test fun splitsPathsContainingSpaceBSlash() {
        val samePath = "dir b/sub b/note.txt"
        val renamedNew = "dir b/new b/file.txt"
        val show = listOf(
            "diff --git a/$samePath b/$samePath",
            "@@ -1 +1 @@",
            "-a",
            "+b",
            "diff --git a/old b/x.txt b/$renamedNew",
            "similarity index 80%",
            "rename from old b/x.txt",
            "rename to $renamedNew",
            "--- a/old b/x.txt",
            "+++ b/$renamedNew",
            "@@ -1 +1 @@",
            "-1",
            "+2",
        ).joinToString("\n")
        val parts = GitDiffs.split(show)
        assertEquals(setOf(samePath, renamedNew), parts.keys)
        assertTrue(GitDiffs.part(show, SessionGitFile(samePath)).diff!!.endsWith("+b"))
        assertTrue(GitDiffs.part(show, SessionGitFile(renamedNew, originalPath = "old b/x.txt", index = "R")).diff!!.endsWith("+2"))
    }

    @Test fun splitsDiffEdgeCasesMixedQuotesHunkBodyCopyLiteralBPrefixCrlfAndMalformed() {
        // Mixed-quote headers WITHOUT rename to (exercises parseHeaderNewPath mixed-quote branches directly),
        // 4-byte UTF-8 octal (\360\237\230\200 -> 😀), copy to, literal b/foo.txt, rename to inside hunk body,
        // commit preamble, deletion, binary diff, CRLF headers, and malformed/unterminated quote safety.
        val show = listOf(
            "commit abc1234",
            "Author: Dev <dev@example.com>",
            "",
            "    commit message preamble",
            "",
            """diff --git a/plain.txt "b/\346\226\260-no-rename.txt"""",
            "@@ -1 +1 @@",
            "-old",
            "+new-mixed-1",
            """diff --git "a/\346\227\247-no-rename.txt" b/ascii-no-rename.txt""",
            "@@ -1 +1 @@",
            "-old",
            "+new-mixed-2",
            """diff --git "a/\360\237\230\200.txt" "b/\360\237\230\200.txt"""",
            "@@ -0,0 +1 @@",
            "+emoji-octal",
            "diff --git a/real.txt b/real.txt",
            "--- a/real.txt",
            "+++ b/real.txt",
            "@@ -1,2 +1,2 @@",
            "rename to fake-in-hunk.txt",
            "+real-line",
            "diff --git a/src.txt b/dir b/copied.txt",
            "similarity index 100%",
            "copy from src.txt",
            "copy to dir b/copied.txt",
            "diff --git a/b/foo.txt b/b/foo.txt",
            "@@ -1 +1 @@",
            "-1",
            "+literal-b",
            "diff --git a/old.txt b/b/bar.txt",
            "similarity index 100%",
            "rename from old.txt",
            "rename to b/bar.txt",
            "diff --git a/gone.txt b/gone.txt",
            "deleted file mode 100644",
            "--- a/gone.txt",
            "+++ /dev/null",
            "@@ -1 +0,0 @@",
            "-bye",
            "diff --git a/img.png b/img.png",
            "Binary files a/img.png and b/img.png differ",
            """diff --git "a/unterminated.txt b/broken.txt""",
            "@@ -1 +1 @@",
            "+ignored-malformed",
        ).joinToString("\n")
        val parts = GitDiffs.split(show)
        assertEquals(
            setOf(
                "新-no-rename.txt",
                "ascii-no-rename.txt",
                "😀.txt",
                "real.txt",
                "dir b/copied.txt",
                "b/foo.txt",
                "b/bar.txt",
                "gone.txt",
                "img.png",
            ),
            parts.keys,
        )
        assertFalse("fake-in-hunk.txt" in parts)
        assertTrue(parts.getValue("新-no-rename.txt").endsWith("+new-mixed-1"))
        assertTrue(parts.getValue("ascii-no-rename.txt").endsWith("+new-mixed-2"))
        assertTrue(parts.getValue("😀.txt").endsWith("+emoji-octal"))
        assertTrue(parts.getValue("real.txt").endsWith("+real-line"))
        assertTrue(parts.getValue("b/foo.txt").endsWith("+literal-b"))
        assertTrue(parts.getValue("gone.txt").endsWith("-bye"))
        assertTrue(parts.getValue("img.png").contains("Binary files"))

        // CRLF headers with quoted rename and copy paths.
        val crlf = listOf(
            """diff --git "a/\346\226\207.txt" "b/\346\226\207.txt"""",
            "@@ -1 +1 @@",
            "+crlf",
            "diff --git a/x.txt b/y.txt",
            "rename from x.txt",
            """rename to "\346\226\260.txt"""",
            "@@ -1 +1 @@",
            "+renamed",
        ).joinToString("\r\n")
        val crlfParts = GitDiffs.split(crlf)
        assertEquals(setOf("文.txt", "新.txt"), crlfParts.keys)
    }

    @Test fun totalsAddUpTheChanges() {
        assertEquals(5 to 1, GitDiffs.totals(listOf(SessionGitFile("a", added = 3, deleted = 1), SessionGitFile("b", added = 2))))
    }

    @Test fun readmeHtmlBecomesMarkdown() {
        val md = "<p align=\"center\"><img src=\"logo.png\" alt=\"Logo\"></p>\n<a href=\"https://example.com\">Site</a>\n```\n<keep>\n```\ntext"
        assertEquals("![Logo](logo.png)\n[Site](https://example.com)\n```\n<keep>\n```\ntext", FileMarkdownText.simplifyHtml(md))
        assertEquals("plain", FileMarkdownText.simplifyHtml("plain"))
        assertEquals("a b", FileMarkdownText.simplifyHtml("<p>a<br/>b</p>"))
        assertEquals("Tom & Jerry", FileMarkdownText.simplifyHtml("<b>Tom &amp; Jerry</b>"))
        // A line that is only a dropped tag disappears instead of leaving an empty paragraph mark.
        assertEquals("before\nafter", FileMarkdownText.simplifyHtml("before\n<div>\nafter"))
        // CommonMark 4-backtick outer fence containing a 3-backtick HTML example must NOT close prematurely.
        val nestedFence = "````markdown\n```html\n<div>keep</div>\n```\n````\n<p>after</p>"
        assertEquals("````markdown\n```html\n<div>keep</div>\n```\n````\nafter", FileMarkdownText.simplifyHtml(nestedFence))
        // CommonMark 0-3 space indentation opens/closes fences; 4 spaces or leading tab does NOT; CRLF fences work.
        assertEquals("   ```\n<keep>\n   ```\nafter", FileMarkdownText.simplifyHtml("   ```\n<keep>\n   ```\n<p>after</p>"))
        assertEquals("    ```\nplain\n    ```", FileMarkdownText.simplifyHtml("    ```\n<p>plain</p>\n    ```"))
        assertEquals("\t```\nplain\n\t```", FileMarkdownText.simplifyHtml("\t```\n<p>plain</p>\n\t```"))
        assertEquals("```\r\n<keep>\r\n```\r\nafter", FileMarkdownText.simplifyHtml("```\r\n<keep>\r\n```\r\n<p>after</p>"))
        // Apostrophes inside double-quoted AND unquoted attributes, `>` inside quoted attrs, entities, and HTML5 whitespace end tags work.
        assertEquals("![Don't panic](a.png)", FileMarkdownText.simplifyHtml("<img src=\"a.png\" alt=\"Don't panic\">"))
        assertEquals("![don't](a.png)", FileMarkdownText.simplifyHtml("<img alt=don't src=\"a.png\">"))
        assertEquals("[Go](https://a)", FileMarkdownText.simplifyHtml("<a title=\"x > y\" href=\"https://a\">Go</a>"))
        assertEquals("Go", FileMarkdownText.simplifyHtml("<span title=\"x > y\">Go</span>"))
        assertEquals("[Go](https://example.com)", FileMarkdownText.simplifyHtml("<a href=\"https://example.com\">Go</a \t>"))
        assertEquals("keep &lt; & ' ' &#0; &#xD800;", FileMarkdownText.simplifyHtml("<a-tag>keep &amp;lt; &AMP; &#39; &#x27; &#0; &#xD800;</a-tag>"))
        // Repeated unclosed HTML tags run in linear time without regex backtracking.
        val unclosedAnchors = "<a href=\"x\">".repeat(20_000) + "ok"
        assertEquals("ok", FileMarkdownText.simplifyHtml(unclosedAnchors))
        val unclosedLt = "<".repeat(30_000) + "tail"
        assertEquals(unclosedLt, FileMarkdownText.simplifyHtml(unclosedLt))
    }

    @Test fun bareLinksEndAtTheFirstNonAsciiCharacter() {
        assertEquals("详见 <https://example.com/a>。后面", FileMarkdownText.links("详见 https://example.com/a。后面"))
        assertEquals("(see <https://example.com/x_(y)>).", FileMarkdownText.links("(see https://example.com/x_(y))."))
        // Links already written as Markdown, autolinks, nested badges, escapes, balanced parens, and multi-backtick code are left alone.
        assertEquals("[a](https://example.com) <https://example.com> `https://example.com`",
            FileMarkdownText.links("[a](https://example.com) <https://example.com> `https://example.com`"))
        assertEquals("``https://example.com``", FileMarkdownText.links("``https://example.com``"))
        assertEquals(
            "[![badge](https://img.example/a.svg)](https://example.com)",
            FileMarkdownText.links("[![badge](https://img.example/a.svg)](https://example.com)"),
        )
        assertEquals(
            """[a\]b](https://example.com/a\)b) [c](https://example.com/(b)c)""",
            FileMarkdownText.links("""[a\]b](https://example.com/a\)b) [c](https://example.com/(b)c)"""),
        )
        assertEquals("[api]: <https://example.com/api>", FileMarkdownText.links("[api]: https://example.com/api"))
        // Bare URL directly before OR immediately after a protected span is handled without swallowing or skipping.
        assertEquals("<https://a.example/>`code`<https://b.example>", FileMarkdownText.links("https://a.example/`code`https://b.example"))
        assertEquals("[a](https://a.example)<https://b.example>", FileMarkdownText.links("[a](https://a.example)https://b.example"))
        // Mid-line ``` inside a fenced code block does not close it; unclosed fence protects through EOF.
        val midLineFence = "```\nmid ``` line\nhttps://inside.example\n```\nhttps://outside.example"
        assertEquals("```\nmid ``` line\nhttps://inside.example\n```\n<https://outside.example>", FileMarkdownText.links(midLineFence))
        val unclosedMultilineFence = "```\nhttps://inside.example"
        assertEquals(unclosedMultilineFence, FileMarkdownText.links(unclosedMultilineFence))
        // Unclosed `[` or `<https://` must not swallow bare URLs on following lines or paragraphs.
        val unclosedBracketNextLine = "[unclosed\nhttps://example.com/mid\n[ok](https://b.example)"
        assertEquals("[unclosed\n<https://example.com/mid>\n[ok](https://b.example)", FileMarkdownText.links(unclosedBracketNextLine))
        val unclosedBracketBlankLine = "[unclosed\n\nhttps://example.com/p2\nfoo](https://b.example)"
        assertEquals("[unclosed\n\n<https://example.com/p2>\nfoo](<https://b.example>)", FileMarkdownText.links(unclosedBracketBlankLine))
        val unclosedAutolinkNextLine = "<https://unclosed.example\nhttps://example.com/next\nfoo>"
        assertEquals("<<https://unclosed.example>\n<https://example.com/next>\nfoo>", FileMarkdownText.links(unclosedAutolinkNextLine))
        // Adversarial inputs stay strictly O(N) without DoS or OOM.
        val unclosedLinks = "[a](".repeat(20_000) + " https://example.com/end"
        assertTrue(FileMarkdownText.links(unclosedLinks).endsWith(" <https://example.com/end>"))
        val repeatedOpens = "[".repeat(30_000) + "]( https://example.com/o"
        assertTrue(FileMarkdownText.links(repeatedOpens).endsWith(" <https://example.com/o>"))
        val unclosedFences = "``` ".repeat(15_000) + "https://example.com/f"
        assertTrue(FileMarkdownText.links(unclosedFences).endsWith("<https://example.com/f>"))
        val hugeBacktickFence = "`".repeat(50_000) + "a".repeat(50_000) + " https://example.com"
        assertEquals(hugeBacktickFence, FileMarkdownText.links(hugeBacktickFence))
        val hugeInlineBackticks = "x " + "`".repeat(50_000) + "a".repeat(50_000) + " https://example.com"
        assertTrue(FileMarkdownText.links(hugeInlineBackticks).endsWith(" <https://example.com>"))
        val descendingInlineBackticks = "x " + (400 downTo 3).joinToString(" ") { "`".repeat(it) } + " https://example.com"
        assertTrue(FileMarkdownText.links(descendingInlineBackticks).endsWith(" <https://example.com>"))
        val descendingTildes = (400 downTo 3).joinToString("\n") { "~".repeat(it) } + "\nhttps://inside.example"
        assertEquals(descendingTildes, FileMarkdownText.links(descendingTildes))
        val manyTrailingParens = "https://example.com/" + ")".repeat(30_000)
        assertEquals("<https://example.com/>" + ")".repeat(30_000), FileMarkdownText.links(manyTrailingParens))
        val repeatedProtectedLinks = "[a](https://a.example)".repeat(30_000)
        assertEquals(repeatedProtectedLinks, FileMarkdownText.links(repeatedProtectedLinks))
        val repeatedSchemeLt = "https://<".repeat(50_000)
        assertEquals(repeatedSchemeLt, FileMarkdownText.links(repeatedSchemeLt))
        val repeatedBareAndCode = "https://a.example`x`".repeat(30_000)
        assertEquals("<https://a.example>`x`".repeat(30_000), FileMarkdownText.links(repeatedBareAndCode))
        val manySpans = "`x` ".repeat(3_000) + "https://example.com/1 ".repeat(1_000)
        assertTrue(FileMarkdownText.links(manySpans).contains("<https://example.com/1>"))
    }

    @Test fun linksAndImagesInAMarkdownFileNameFilesNextToIt() {
        val base = "/Users/me/code/weather"
        assertEquals("/Users/me/code/weather/docs/icon.png", FileMarkdownText.target("docs/icon.png", base))
        assertEquals("/Users/me/code/weather/docs/guide.md", FileMarkdownText.target("./docs/guide.md#install", base))
        assertEquals("/Users/me/code/LICENSE", FileMarkdownText.target("../LICENSE", base))
        assertEquals("/Users/me/code/weather/my file.md", FileMarkdownText.target("my%20file.md", base))
        assertEquals("/etc/hosts", FileMarkdownText.target("/etc/hosts", base))
        assertEquals("~/notes.txt", FileMarkdownText.target("~/notes.txt", base))
        assertNull(FileMarkdownText.target("https://example.com/a.png", base))
        assertNull(FileMarkdownText.target("mailto:me@example.com", base))
        assertNull(FileMarkdownText.target("#usage", base))
        assertNull(FileMarkdownText.target("//cdn.example.com/x.png", base))
    }
}
