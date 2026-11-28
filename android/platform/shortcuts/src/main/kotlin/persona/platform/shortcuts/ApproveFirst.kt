package persona.platform.shortcuts

import android.app.KeyguardManager
import android.content.res.Resources
import android.os.Bundle
import android.widget.Toast
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.stringResource
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.lifecycle.lifecycleScope
import persona.core.contracts.Interaction
import persona.core.contracts.Machine
import persona.core.contracts.api.InteractionCenter
import persona.core.designsystem.AppStyle
import persona.core.designsystem.AppStyleStore
import persona.core.designsystem.JDialog
import persona.core.designsystem.JSpinner
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import persona.core.hub.HubError
import persona.core.hub.HubFailure
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.launch
import kotlinx.coroutines.withTimeoutOrNull

/** What the "Approve first pending" dialog shows. */
sealed interface ApproveState {
    data object Loading : ApproveState
    data object NotPaired : ApproveState
    data object Nothing : ApproveState
    /** A question or a terminal dialog: it needs the conversation. */
    data class NeedsApp(val request: Interaction, val where: String?) : ApproveState
    data class Confirm(val request: Interaction, val where: String?, val sending: Boolean = false) : ApproveState
    data class Failed(val message: String) : ApproveState
}

/**
 * "Approve first pending" (iOS ApproveFirstIntent): reads the pending requests fresh from the hub, shows the oldest and settles
 * it after one tap. Nothing is sent twice: the button is off while a send runs, and a send whose result is unknown is
 * not retried (the user checks it in the app).
 */
class ApproveFirstModel(
    private val res: Resources,
    private val paired: () -> Boolean,
    private val center: InteractionCenter,
    private val machines: () -> List<Machine>,
    private val restore: suspend () -> Unit = {},
) {
    private val mutable = MutableStateFlow<ApproveState>(ApproveState.Loading)
    val state: StateFlow<ApproveState> = mutable.asStateFlow()

    suspend fun load() {
        restore()
        if (!paired()) { mutable.value = ApproveState.NotPaired; return }
        mutable.value = ApproveState.Loading
        mutable.value = try {
            // The socket may have been asleep: ask the hub, as the iOS intent does.
            val before = center.pending.value
            withTimeoutOrNull(LOAD_TIMEOUT_MILLIS) { center.refresh(); true } ?: throw HubFailure(HubError.Timeout)
            // The app's InteractionCenter republishes the refreshed list from another coroutine: give it a moment
            // (an unchanged list is not published again, so this only waits when nothing changed).
            val requests = withTimeoutOrNull(SETTLE_MILLIS) { center.pending.first { it !== before } } ?: center.pending.value
            val request = ShortcutText.pending(requests)
            val where = request?.let { ShortcutText.where(it, machines()) }
            when {
                request == null -> ApproveState.Nothing
                ShortcutText.resolution(request) == null -> ApproveState.NeedsApp(request, where)
                else -> ApproveState.Confirm(request, where)
            }
        } catch (cancelled: CancellationException) { throw cancelled }
        catch (failure: Exception) { ApproveState.Failed(ShortcutText.loadFailure(res, failure)) }
    }

    /** Sends the answer once; the toast to show when it went through, null otherwise (the state says why). */
    suspend fun approve(locked: Boolean): String? {
        val current = mutable.value as? ApproveState.Confirm ?: return null
        if (current.sending) return null
        // Saying yes needs an unlocked phone (the shared policy's launcher entry).
        val resolution = ShortcutText.resolution(current.request, locked)
            ?: run { mutable.value = ApproveState.Failed(res.getString(R.string.shortcuts_locked)); return null }
        mutable.value = current.copy(sending = true)
        return try {
            center.resolveOccurrence(current.request, resolution)
            ShortcutText.done(res, current.request)
        } catch (cancelled: CancellationException) { throw cancelled }
        catch (failure: Exception) {
            mutable.value = ApproveState.Failed(ShortcutText.sendFailure(res, failure))
            null
        }
    }

    private companion object {
        const val LOAD_TIMEOUT_MILLIS = 15_000L
        const val SETTLE_MILLIS = 300L
    }
}

/**
 * The launcher's "Approve pending". Not exported: only the shortcut the app published, started on its behalf, reaches it, so a
 * link from another app can never approve anything. Only its dialog shows, over whatever was on screen.
 */
class ApproveFirstActivity : ComponentActivity() {
    /** Text in the language of Settings › Language (docs/protocol/i18n.md). */
    override fun attachBaseContext(base: android.content.Context) = super.attachBaseContext(persona.core.designsystem.AppLanguages.wrap(base))

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        val dependencies = SurfacesDependencies.of(this)
        val model = ApproveFirstModel(resources, { dependencies.pairing().isPaired.value }, dependencies.interactions(),
            { dependencies.sessions().machines.value }, restore = { dependencies.pairing().restore() })
        val keyguard = getSystemService(KeyguardManager::class.java)
        val style = AppStyleStore(this).style.value
        setContent {
            ShortcutTheme(style) {
                val state by model.state.collectAsStateWithLifecycle()
                LaunchedEffect(model) { model.load() }
                ApproveFirstDialog(state, onDismiss = ::finish,
                    onOpen = { request ->
                        startActivity(request?.let {
                            SurfaceLinks.request(this, it) { key -> dependencies.sessions().assistant(key) }
                        } ?: SurfaceLinks.app(this))
                        finish()
                    },
                    onApprove = {
                        fun send() = lifecycleScope.launch {
                            model.approve(keyguard.isDeviceLocked)?.let { done ->
                                Toast.makeText(applicationContext, done, Toast.LENGTH_SHORT).show()
                                finish()
                            }
                        }
                        if (!keyguard.isDeviceLocked) send()
                        else keyguard.requestDismissKeyguard(this, object : KeyguardManager.KeyguardDismissCallback() {
                            override fun onDismissSucceeded() { send() }
                        })
                    })
            }
        }
    }

    companion object {
        const val ACTION_APPROVE = "persona.platform.shortcuts.APPROVE"
    }
}

/** The app's theme for a window of its own: the user's style, light or dark with the system. */
@Composable
internal fun ShortcutTheme(style: AppStyle, dark: Boolean? = null, content: @Composable () -> Unit) =
    PersonaTheme(darkTheme = dark ?: persona.core.designsystem.appDarkTheme(), style = style, content = content)

/** The dialog for each state; the debug preview draws the same one. */
@Composable
fun ApproveFirstDialog(
    state: ApproveState,
    onApprove: () -> Unit,
    onOpen: (Interaction?) -> Unit,
    onDismiss: () -> Unit,
) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    val res = LocalResources.current
    val title = stringResource(R.string.shortcuts_approve_title)
    val openPersona = stringResource(R.string.shortcuts_open_persona)
    val ok = stringResource(R.string.shortcuts_ok)
    val cancel = stringResource(R.string.shortcuts_cancel) to onDismiss
    // The window shows nothing but the dialog, over the launcher: the design system's scrim sets it apart from the home
    // screen and gives the status bar's icons (light in dark mode) a background they can be read on, whatever the
    // wallpaper. Without it, a light wallpaper left them white on white.
    Box(Modifier.fillMaxSize().background(palette.scrim))
    when (state) {
        ApproveState.Loading -> JDialog(onDismiss, title, dismiss = cancel, content = {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
                JSpinner()
                Text(stringResource(R.string.shortcuts_loading), style = type.callout, color = palette.ink2)
            }
        })
        ApproveState.NotPaired -> JDialog(onDismiss, title, text = stringResource(R.string.shortcuts_not_paired),
            confirm = openPersona to { onOpen(null) }, dismiss = cancel)
        ApproveState.Nothing -> JDialog(onDismiss, title, text = stringResource(R.string.shortcuts_no_pending),
            confirm = ok to onDismiss, dismiss = null)
        is ApproveState.NeedsApp -> JDialog(onDismiss, title, content = {
            Request(ShortcutText.needsApp(res, state.request.kind), null, state.where)
        }, confirm = openPersona to { onOpen(state.request) }, dismiss = cancel)
        is ApproveState.Confirm -> JDialog(onDismiss = { if (!state.sending) onDismiss() }, title = title,
            content = {
                Request(ShortcutText.ask(res, state.request), stringResource(R.string.shortcuts_plan_note).takeIf { state.request.kind == "plan" }, state.where)
            },
            confirm = ShortcutText.confirmLabel(res, state.request) to onApprove, confirmEnabled = !state.sending,
            dismiss = cancel.first to { if (!state.sending) onDismiss() })
        is ApproveState.Failed -> JDialog(onDismiss, title, text = state.message, confirm = ok to onDismiss, dismiss = null)
    }
}

@Composable
private fun Request(question: String, note: String?, where: String?) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
        Text(question, style = type.body, color = palette.ink)
        note?.let { Text(it, style = type.caption, color = palette.ink2) }
        where?.let { Text(it, style = type.caption, color = palette.ink3, maxLines = 1) }
    }
}
