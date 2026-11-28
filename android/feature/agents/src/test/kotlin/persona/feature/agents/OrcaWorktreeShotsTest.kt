package persona.feature.agents

import android.graphics.Bitmap
import androidx.activity.ComponentActivity
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.api.*
import persona.core.designsystem.*
import persona.feature.agents.FakeWorktrees.Companion.machine
import persona.feature.agents.FakeWorktrees.Companion.main
import persona.feature.agents.FakeWorktrees.Companion.rows
import persona.feature.agents.FakeWorktrees.Companion.target
import java.io.File

/**
 * Orca's worktree actions on Android (#17054), in every style, light and dark: a worktree's menu (a linked worktree with
 * everything on; the main checkout with Delete off and why), New worktree with Orca's composer (its Advanced choices open)
 * and the basic branch-and-path form, the deletion preview with changed files and a branch, and Rename. PNGs in
 * `build/orca-worktree-shots/` (or `-Dpersona.shots=<dir>`).
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1200dp-xhdpi")
class OrcaWorktreeShotsTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate)

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/orca-worktree-shots").also { it.mkdirs() }
    private fun save(name: String) {
        val bitmap = rule.onNode(isDialog()).captureToImage().asAndroidBitmap()
        File(out(), "$name.png").outputStream().use { assertTrue(bitmap.compress(Bitmap.CompressFormat.PNG, 100, it)) }
    }
    private fun exists(text: String) =
        assertTrue(text, rule.onAllNodesWithText(text, substring = true, useUnmergedTree = true).fetchSemanticsNodes().isNotEmpty())

    private fun service(scopeName: String, api: FakeWorktrees) = object : AgentCreationService {
        override val scope = scopeName
        override suspend fun launchers(machineId: String) = emptyList<AgentLauncher>()
        override suspend fun directories(machineId: String, path: String) = AgentDirectories("/code")
        override suspend fun start(request: AgentStartRequest, operationId: String) = AgentStartResult.Unknown
        override suspend fun operation(operationId: String) = AgentStartResult.Unknown
        override val worktrees: WorktreeManagement = api
    }

    private fun eachLook(name: String, check: () -> Unit, set: (AppStyle, Boolean) -> Unit) {
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            rule.runOnIdle { set(s, d) }
            rule.waitForIdle()
            check()
            save("$name-${s.name.lowercase()}-${if (d) "dark" else "light"}")
        }
    }

    @Test fun theWorktreeMenuSaysWhatItCanDo() {
        val api = FakeWorktrees()
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var shown by mutableStateOf(target)
        rule.setContent {
            PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                val actions = rememberOrcaWorktreeActions(service("test/menu", api), listOf(machine), rows) {}
                if (actions != null) JActionSheet(actions.menuActions(shown), {}, title = shown.label, note = actions.note(shown))
            }
        }
        try {
            rule.waitUntil(5_000) { api.calls.contains("access:mac") }
            eachLook("worktree-menu", { exists("Delete worktree"); exists("Rename worktree") }) { s, d -> style = s; dark = d; shown = target }
            eachLook("worktree-menu-main", { exists("Remove child worktrees") }) { s, d -> style = s; dark = d; shown = WorktreeTarget.of(main) }
        } finally { scope.cancel() }
    }

    @Test fun newWorktreeWithOrcasComposer() {
        val api = FakeWorktrees()
        val creation = WorktreeCreation(target, api, scope)
        val gate = WorktreeGates.resolve(target, rows, listOf(machine), WorktreeAccess.Full)
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) { NewWorktreeSheet(creation, gate, rows, {}) }
        }
        try {
            rule.waitUntil(5_000) { creation.state.value.info != null }
            eachLook("new-worktree-composer", { exists("Base branch"); exists("origin/main") }) { s, d -> style = s; dark = d }
            rule.onNodeWithText("Advanced").performClick()
            eachLook("new-worktree-advanced", { exists("Parent worktree"); exists("Sparse checkout") }) { s, d -> style = s; dark = d }
        } finally { scope.cancel() }
    }

    @Test fun newWorktreeOnAComputerWithoutTheComposer() {
        val api = FakeWorktrees().apply { prepare = { FakeWorktrees.basicPrepare } }
        val creation = WorktreeCreation(target, api, scope)
        creation.update { WorktreeForms.typeBranch(it, FakeWorktrees.basicPrepare.path, "feat/sign-in") }
        val gate = WorktreeGates.resolve(target, rows, listOf(machine), WorktreeAccess.Full)
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) { NewWorktreeSheet(creation, gate, rows, {}) }
        }
        try {
            rule.waitUntil(5_000) { creation.state.value.info != null }
            eachLook("new-worktree-basic", { exists("/code/.orca/feat-sign-in"); exists("Advanced options need an updated Orca") }) { s, d ->
                style = s; dark = d
            }
        } finally { scope.cancel() }
    }

    // 320dp wide: on this JVM a JDialog holding a JTextField never goes idle at 360dp and wider (FileEditsShotsTest).
    @Config(sdk = [35], qualifiers = "en-w320dp-h900dp-xhdpi")
    @Test fun deletingShowsWhatGoesAndAsksForTheChangedFiles() {
        val api = FakeWorktrees()
        val mutation = WorktreeMutation(target, api, scope, { WorktreeGates.resolve(target, rows, listOf(machine), WorktreeAccess.Full) })
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) { DeleteWorktreeDialog(mutation, "login-form", "MacBook Pro", {}) }
        }
        try {
            rule.waitUntil(5_000) { mutation.state.value.phase == WorktreePhase.Ready }
            eachLook("worktree-delete", { exists("2 files have uncommitted changes"); exists("Discard these uncommitted changes") }) { s, d ->
                style = s; dark = d
            }
        } finally { scope.cancel() }
    }

    @Config(sdk = [35], qualifiers = "en-w320dp-h800dp-xhdpi")
    @Test fun renamingAWorktree() {
        val mutation = WorktreeMutation(target, FakeWorktrees(), scope, { WorktreeGates.resolve(target, rows, listOf(machine), WorktreeAccess.Full) })
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) { RenameWorktreeDialog(mutation, "login-form", {}) }
        }
        try {
            eachLook("worktree-rename", { exists("Worktree name"); exists("names stay the same") }) { s, d -> style = s; dark = d }
        } finally { scope.cancel() }
    }
}
