package persona.feature.agents

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithContentDescription
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.text.TextLayoutResult
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.io.File
import java.time.Instant
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.ReviewCheck
import persona.core.contracts.Session
import persona.core.contracts.Supervision
import persona.core.contracts.TaskReview
import persona.core.contracts.Todo
import persona.core.contracts.TodoAgent
import persona.core.contracts.TodoPRStatus
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme

/** Open concerns stay visible in Working and Other open, a reported concern closes, and every one is labelled. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1400dp-xhdpi")
class TodoConcernShotsTest {
    @get:Rule val rule = createComposeRule()

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/concern-shots").also { it.mkdirs() }
    private val now = Instant.parse("2026-11-10T12:00:00Z")
    private fun ago(minutes: Long) = now.minusSeconds(minutes * 60).toString()

    private fun Todo.withKind(kind: String) = copy(kind = kind)

    private val sessions = listOf(
        Session("m", "run", state = "working", status = "working", updatedAt = ago(2)),
    )

    private fun todos(chinese: Boolean) = listOf(
        Todo(
            "working",
            title = if (chinese) "晨报为什么少了昨天" else "Morning summary",
            createdAt = ago(500), updatedAt = ago(3), origin = "conv",
            agents = listOf(TodoAgent("m", "run", linkedAt = ago(400))),
        ).withKind("concern"),
        Todo(
            "open",
            title = if (chinese) "假期安排怎么定" else "Holiday plan",
            createdAt = ago(60_000), updatedAt = ago(43_200), origin = "conv",
        ).withKind("concern"),
        Todo(
            "closed",
            title = if (chinese) "存储空间怎么办" else "Storage answer",
            createdAt = ago(500), updatedAt = ago(60), doneAt = ago(60), doneBy = "reported", doneReason = "Already answered",
        ).withKind("concern"),
        Todo(
            "ordinary",
            title = if (chinese) "整理本周笔记" else "Weekly notes",
            createdAt = ago(500), updatedAt = ago(5),
        ),
    )

    private fun has(text: String) = rule.onAllNodesWithText(text, useUnmergedTree = true).fetchSemanticsNodes().isNotEmpty()

    private fun textLayout(text: String): TextLayoutResult {
        val node = rule.onNodeWithText(text, useUnmergedTree = true).fetchSemanticsNode()
        val layouts = mutableListOf<TextLayoutResult>()
        val _ = node.config[SemanticsActions.GetTextLayoutResult].action!!(layouts)
        return layouts.single()
    }

    private fun shot(style: AppStyle, dark: Boolean, chinese: Boolean = false, large: Boolean = false) {
        val label = if (chinese) "关注点" else "Concern"
        val titles = if (chinese) listOf("晨报为什么少了昨天", "假期安排怎么定", "存储空间怎么办", "整理本周笔记")
        else listOf("Morning summary", "Holiday plan", "Storage answer", "Weekly notes")
        var renderedFontScale = 0f
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                val density = LocalDensity.current
                CompositionLocalProvider(LocalDensity provides Density(density.density, if (large) 2f else density.fontScale)) {
                    renderedFontScale = LocalDensity.current.fontScale
                    Box(Modifier.testTag("shot").width(400.dp).height(1400.dp).background(PersonaTheme.palette.background)) {
                        TodosScreen(todos(chinese), sessions, null, onBack = {}, onOpenTodo = {}, now = now)
                    }
                }
            }
        }
        rule.waitForIdle()
        val name = "todos-concerns-${style.name.lowercase()}-${if (dark) "dark" else "light"}-${if (chinese) "zh" else "en"}${if (large) "-large" else ""}.png"
        val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
        File(out(), name).outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }

        val tags = rule.onAllNodesWithContentDescription(label, useUnmergedTree = true).fetchSemanticsNodes()
        assertEquals("three concern rows, no label on the ordinary row", 3, tags.size)
        assertTrue("Working", has(if (chinese) "工作中" else "Working"))
        assertTrue("Other open", has(if (chinese) "其他未完成" else "Other open"))
        assertTrue("Just finished", has(if (chinese) "刚做完" else "Just finished"))
        assertTrue("the working concern keeps its state tag", has(if (chinese) "进行中" else "In progress"))
        val tracking = if (chinese) "跟进中" else "Tracking"
        val notStarted = if (chinese) "未开始" else "Not started"
        assertEquals("the open concern shows Tracking instead of Not started", 1,
            rule.onAllNodesWithText(tracking, useUnmergedTree = true).fetchSemanticsNodes().size)
        assertEquals("only the ordinary unstarted row shows Not started", 1,
            rule.onAllNodesWithText(notStarted, useUnmergedTree = true).fetchSemanticsNodes().size)
        titles.forEach { title -> assertTrue("missing title $title", has(title)) }
        val shotBounds = rule.onNodeWithTag("shot").fetchSemanticsNode().boundsInRoot
        assertTrue(tags.all { it.boundsInRoot.left >= shotBounds.left && it.boundsInRoot.right <= shotBounds.right })
        if (large) {
            assertEquals(2f, renderedFontScale, 0f)
            titles.take(3).forEach { title ->
                rule.onNodeWithText(title, useUnmergedTree = true).assertIsDisplayed()
                assertFalse("$title is clipped at 200% text", textLayout(title).hasVisualOverflow)
            }
        }
    }

    @Test fun glassLightEnglish() = shot(AppStyle.Glass, false)
    @Test fun glassDarkEnglish() = shot(AppStyle.Glass, true)
    @Test fun briefLightEnglish() = shot(AppStyle.Brief, false)
    @Test fun briefDarkEnglish() = shot(AppStyle.Brief, true)
    @Test fun boardLightEnglish() = shot(AppStyle.Board, false)
    @Test fun boardDarkEnglish() = shot(AppStyle.Board, true)

    @Config(qualifiers = "zh-w400dp-h1400dp-xhdpi")
    @Test fun glassLightChinese() = shot(AppStyle.Glass, false, chinese = true)
    @Config(qualifiers = "zh-w400dp-h1400dp-xhdpi")
    @Test fun glassDarkChinese() = shot(AppStyle.Glass, true, chinese = true)
    @Config(qualifiers = "zh-w400dp-h1400dp-xhdpi")
    @Test fun briefLightChinese() = shot(AppStyle.Brief, false, chinese = true)
    @Config(qualifiers = "zh-w400dp-h1400dp-xhdpi")
    @Test fun briefDarkChinese() = shot(AppStyle.Brief, true, chinese = true)
    @Config(qualifiers = "zh-w400dp-h1400dp-xhdpi")
    @Test fun boardLightChinese() = shot(AppStyle.Board, false, chinese = true)
    @Config(qualifiers = "zh-w400dp-h1400dp-xhdpi")
    @Test fun boardDarkChinese() = shot(AppStyle.Board, true, chinese = true)

    @Test fun workingConcernShowsBothStateAndConcernTags() {
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                Box(Modifier.testTag("shot").width(400.dp).height(1400.dp).background(PersonaTheme.palette.background)) {
                    TodosScreen(todos(false).take(1), sessions, null, onBack = {}, onOpenTodo = {}, now = now)
                }
            }
        }
        rule.onNodeWithText("Morning summary", useUnmergedTree = true).assertIsDisplayed()
        rule.onNodeWithText("In progress", useUnmergedTree = true).assertIsDisplayed()
        rule.onNodeWithContentDescription("Concern", useUnmergedTree = true).assertIsDisplayed()
        assertEquals("Concern is spoken once", 1,
            rule.onAllNodesWithContentDescription("Concern", useUnmergedTree = true).fetchSemanticsNodes().size)
    }

    @Test fun acceptedConcernAtRestShowsOpenStateAndConcernTags() {
        val criteria = (1..8).map { "c$it" }
        val accepted = Todo(
            "accepted-concern",
            title = "Review answer",
            createdAt = ago(500), updatedAt = ago(3), origin = "main",
            agents = listOf(TodoAgent("m", "idle", linkedAt = ago(400))),
            supervision = Supervision(
                supervisor = "main", doneWhen = criteria, state = "accepted", round = 1,
                reviews = listOf(TaskReview(at = ago(5), verdict = "accepted", checks = criteria.take(5).map { ReviewCheck(it, true) })),
            ),
        ).withKind("concern")
        val idle = Session("m", "idle", state = "idle", status = "idle", updatedAt = ago(2))
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                Box(Modifier.testTag("shot").width(400.dp).height(1400.dp).background(PersonaTheme.palette.background)) {
                    TodosScreen(listOf(accepted), listOf(idle), null, onBack = {}, onOpenTodo = {}, now = now)
                }
            }
        }
        rule.waitForIdle()
        val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
        File(out(), "todos-concern-accepted-at-rest-glass-light-en.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        rule.onNodeWithContentDescription("Concern", useUnmergedTree = true).assertIsDisplayed()
        assertFalse("an accepted open concern never shows Accepted", has("Accepted"))
        rule.onNodeWithText("Follow up", useUnmergedTree = true).assertIsDisplayed()
    }

    @Test fun closedConcernInExpandedRelatedRowIsLabelled() {
        // Two reported concerns keep one in the related row; an ordinary completed task is directly Done (#5732).
        val list = listOf(
            Todo("ordinary", title = "Morning work", createdAt = ago(500), updatedAt = ago(30), doneAt = ago(30), planId = "p1")
                .withKind("concern"),
            Todo("concern", title = "Morning answer", createdAt = ago(500), updatedAt = ago(60), doneAt = ago(60), doneBy = "reported", planId = "p1")
                .withKind("concern"),
        )
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                Box(Modifier.testTag("shot").width(400.dp).height(1400.dp).background(PersonaTheme.palette.background)) {
                    TodosScreen(list, emptyList(), null, onBack = {}, onOpenTodo = {}, now = now)
                }
            }
        }
        rule.waitForIdle()
        rule.onNodeWithText("1 related", useUnmergedTree = true).performClick()
        rule.waitForIdle()
        assertTrue(has("Morning answer"))
        assertEquals("both reported concerns are labelled", 2,
            rule.onAllNodesWithContentDescription("Concern", useUnmergedTree = true).fetchSemanticsNodes().size)
        val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
        File(out(), "todos-concerns-related-glass-light-en.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        val labelCounts = listOf("Just finished", "Delivered · awaiting review").associateWith {
            rule.onAllNodesWithText(it, useUnmergedTree = true).fetchSemanticsNodes().size
        }
        assertEquals("the section heading and the related line's section tag; labels=$labelCounts", 2,
            rule.onAllNodesWithText("Just finished", useUnmergedTree = true).fetchSemanticsNodes().size)
    }

    @Config(qualifiers = "ja-w400dp-h1400dp-xhdpi")
    @Test fun closedConcernJapaneseSectionAndRelatedRowStayJustFinished() {
        val list = listOf(
            Todo("ordinary", title = "Morning work", createdAt = ago(500), updatedAt = ago(30), doneAt = ago(30), planId = "p1")
                .withKind("concern"),
            Todo("concern", title = "Morning answer", createdAt = ago(500), updatedAt = ago(60), doneAt = ago(60), doneBy = "reported", planId = "p1")
                .withKind("concern"),
        )
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                Box(Modifier.testTag("shot").width(400.dp).height(1400.dp).background(PersonaTheme.palette.background)) {
                    TodosScreen(list, emptyList(), null, onBack = {}, onOpenTodo = {}, now = now)
                }
            }
        }
        rule.waitForIdle()
        rule.onNodeWithText("関連 1 件", useUnmergedTree = true).performClick()
        rule.waitForIdle()
        assertEquals("Japanese section heading and related line", 2,
            rule.onAllNodesWithText("完了したばかり", useUnmergedTree = true).fetchSemanticsNodes().size)
        assertFalse("a reported concern is not awaiting review", has("交付済み・確認待ち"))
    }

    @Config(fontScale = 2f)
    @Test fun glassLightEnglishLargeFont() = shot(AppStyle.Glass, false, large = true)

    @Test fun openPrTodosShowPrStatusInsteadOfNotStartedInListAndDetail() {
        val checked = ago(2)
        val mergePr = Todo(
            "pr-ready",
            title = "Ship parser fix",
            createdAt = ago(120), updatedAt = ago(5), origin = "conv",
            prUrl = "https://github.com/org/repo/pull/101",
            prStatus = TodoPRStatus(
                url = "https://github.com/org/repo/pull/101",
                state = "open", draft = false, review = "approved", checks = "success", conflicts = false, checkedAt = checked,
            ),
        )
        val conflictPr = Todo(
            "pr-conflict",
            title = "Rebase storage layer",
            createdAt = ago(110), updatedAt = ago(4), origin = "conv",
            prUrl = "https://github.com/org/repo/pull/102",
            prStatus = TodoPRStatus(
                url = "https://github.com/org/repo/pull/102",
                state = "open", draft = false, review = "changes_requested", checks = "failure", conflicts = true, checkedAt = checked,
            ),
        )
        val draftPr = Todo(
            "pr-draft",
            title = "WIP draft work",
            createdAt = ago(100), updatedAt = ago(3), origin = "conv",
            prUrl = "https://github.com/org/repo/pull/103",
            prStatus = TodoPRStatus(
                url = "https://github.com/org/repo/pull/103",
                state = "open", draft = true, review = "pending", checks = "none", conflicts = false, checkedAt = checked,
            ),
        )
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                Box(Modifier.width(400.dp).height(1400.dp).background(PersonaTheme.palette.background)) {
                    TodosScreen(listOf(mergePr, conflictPr, draftPr), emptyList(), null, onBack = {}, onOpenTodo = {}, now = now)
                }
            }
        }
        rule.waitForIdle()
        rule.onNodeWithText("Waiting to merge", useUnmergedTree = true).assertIsDisplayed()
        rule.onNodeWithText("Merge conflicts", useUnmergedTree = true).assertIsDisplayed()
        assertEquals("only the draft PR stays Not started", 1,
            rule.onAllNodesWithText("Not started", useUnmergedTree = true).fetchSemanticsNodes().size)
    }

    @Test fun detailScreenShowsTrackingForOpenConcernAndMergeStatusForOpenPr() {
        val checked = ago(2)
        val prTodo = Todo(
            "detail-pr",
            title = "PR detail item",
            createdAt = ago(60), updatedAt = ago(5), origin = "conv",
            prUrl = "https://github.com/org/repo/pull/205",
            prStatus = TodoPRStatus(
                url = "https://github.com/org/repo/pull/205",
                state = "open", draft = false, review = "required", checks = "success", conflicts = false, checkedAt = checked,
            ),
        )
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                Box(Modifier.width(400.dp).height(1000.dp).background(PersonaTheme.palette.background)) {
                    TodoDetailScreen(
                        todoId = prTodo.id,
                        todos = listOf(prTodo),
                        machines = emptyList(),
                        sessions = emptyList(),
                        tasks = null,
                        onBack = {},
                        onOpenSession = {},
                        onHandOff = {},
                        now = now,
                    )
                }
            }
        }
        rule.waitForIdle()
        rule.onNodeWithText("In review", useUnmergedTree = true).assertIsDisplayed()
        assertFalse("detail screen must not show Not started for open PR", has("Not started"))
    }
}
