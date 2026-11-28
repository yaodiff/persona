package persona.android

import androidx.compose.ui.res.stringResource
import android.annotation.SuppressLint
import android.content.ComponentName
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Bundle
import android.view.KeyEvent
import android.view.MotionEvent
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.foundation.layout.*
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.SideEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import androidx.compose.runtime.mutableStateOf
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import androidx.core.view.WindowCompat
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.lifecycle.lifecycleScope
import dagger.hilt.android.AndroidEntryPoint
import persona.android.navigation.LaunchAdapter
import persona.android.navigation.LocalDeveloperTools
import persona.android.navigation.notificationRoute
import persona.android.navigation.open
import persona.android.navigation.terminalLink
import persona.android.platform.LockScreenTests
import persona.android.platform.PlatformPairing
import persona.android.platform.PlatformRoot
import persona.android.ui.AppServices
import persona.android.ui.AppearanceOverride
import persona.feature.assistant.conversationOpeningSource
import persona.core.contracts.LarkFeatures
import persona.core.contracts.api.HubConnection
import persona.core.designsystem.GlassThrottle
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.LocalAppStyleStore
import persona.core.designsystem.LocalGlassLevel
import persona.core.designsystem.LocalPersonaDesign
import persona.core.designsystem.dense
import persona.core.designsystem.keepAmbientLightMoving
import persona.core.navigation.*
import persona.feature.settings.ProviderImportRequests
import persona.feature.settings.SettingsServices
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.flow.flatMapLatest
import kotlinx.coroutines.flow.flowOf
import kotlinx.coroutines.launch
import javax.inject.Inject

@AndroidEntryPoint
class MainActivity : ComponentActivity() {
    /** Text in the language of Settings › Language (docs/protocol/i18n.md). */
    override fun attachBaseContext(base: android.content.Context) = super.attachBaseContext(persona.core.designsystem.AppLanguages.wrap(base))

    @Inject lateinit var settingsServices: SettingsServices
    /** The hubs' channels, for the "From WeChat" marks on messages and conversations (docs/extensions/channels.md "Source markers"). */
    @Inject lateinit var channelDirectory: persona.core.contracts.api.ChannelDirectory
    /** The installed extensions' icons, for the marks of conversations an extension opened (#10358). */
    @Inject lateinit var openerIcons: persona.feature.assistant.OpenerIcons
    @Inject lateinit var connection: HubConnection
    @Inject lateinit var shell: AppServices
    @Inject lateinit var platformPairing: PlatformPairing
    @Inject lateinit var translator: persona.feature.inbox.Translator
    @Inject lateinit var capsules: persona.android.capsule.CapsuleRuntime
    @Inject lateinit var managerRoster: persona.android.di.ManagerRosterSource

    /** A destination from a notification, deep link or share; resolved once session metadata allows. */
    private val openNavigation = mutableStateOf<((Router) -> ResolvedRoute?)?>(null)
    /** Only a debug launch adapter overrides the system appearance. */
    private val darkTheme = mutableStateOf<Boolean?>(null)

    /** Installed only by the debug manifest; release builds have no launch adapter or developer pages. */
    private val launchAdapter: LaunchAdapter? by lazy {
        // A legacy launcher alias does not inherit target metadata; always query the canonical Activity.
        packageManager.getActivityInfo(ComponentName(this, MainActivity::class.java), PackageManager.GET_META_DATA).metaData
            ?.getString("persona.launchAdapter")
            ?.let { Class.forName(it).getDeclaredConstructor().newInstance() as LaunchAdapter }
    }

    @OptIn(ExperimentalCoroutinesApi::class)
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        // A recreated activity already restored its back stacks; replaying the launch intent would navigate again.
        if (savedInstanceState == null) handle(intent)
        else launchAdapter?.read(intent)?.let { input ->
            darkTheme.value = input.darkTheme
            shell.automation = input.automation
            shell.posture.value = input.posture
            input.style?.let { (application as PersonaApp).appearance.setStyle(it) }
        }
        val app = application as PersonaApp
        lifecycleScope.launch { shell.restoreDemo() }
        persona.android.platform.GlassFrameMonitor.attach(
            this,
            deviceLevel = { app.appearance.deviceLevel(app.appearance.glass.value) },
            enabled = { !shell.automation },
        )
        managerRoster.start()
        setContent {
            val scheme by app.appearance.scheme.collectAsStateWithLifecycle()
            // A debug capture's pin, else Settings › Appearance › Light/Dark (null: the system's).
            val forced = darkTheme.value ?: scheme.dark
            val style by app.appearance.style.collectAsStateWithLifecycle()
            val themeColor by app.appearance.color.collectAsStateWithLifecycle()
            val glassPreference by app.appearance.glass.collectAsStateWithLifecycle()
            val face by app.appearance.face.collectAsStateWithLifecycle()
            val pack by app.personas.current.collectAsStateWithLifecycle()
            // Levels are ordered best first, so the lower of the two choices is the later one.
            val glassLevel = maxOf(app.appearance.deviceLevel(glassPreference), GlassThrottle.ceiling.value)
            val channels by channelDirectory.channels.collectAsStateWithLifecycle()
            // What the selected hub offers, from its hello: Feishu through the Lark extension (#1530), the main
            // conversation's actions (#1727).
            val features by remember(app) {
                app.hub.flatMapLatest { it?.connection?.serverFeatures ?: flowOf(emptySet()) }.distinctUntilChanged()
            }.collectAsStateWithLifecycle(emptySet())
            val imageLimits by remember(app) {
                app.hub.flatMapLatest { it?.connection?.imageLimits ?: flowOf(persona.core.contracts.ImageLimitsTable.Standard) }.distinctUntilChanged()
            }.collectAsStateWithLifecycle(persona.core.contracts.ImageLimitsTable.Standard)
            val lark = remember(features) { LarkFeatures.of(features) }
            val mail = remember(features) { persona.android.ui.mailAccessOf(features) }
            // The selected hub's managers: pages group by them when there are several (docs/assistant/managers.md step 4).
            val managers by managerRoster.roster.collectAsStateWithLifecycle()
            val managerGroup by managerRoster.group.collectAsStateWithLifecycle()
            AppearanceOverride(forced) { CompositionLocalProvider(
                LocalDeveloperTools provides launchAdapter?.tools,
                persona.core.designsystem.LocalLanguageHost provides this@MainActivity,
                persona.core.designsystem.LocalChannels provides channels,
                persona.feature.assistant.LocalOpenerIcons provides openerIcons,
                persona.feature.chat.LocalOpeningMessageSource provides ::conversationOpeningSource,
                persona.core.designsystem.LocalLarkFeatures provides lark,
                persona.core.designsystem.LocalMailAccess provides mail,
                persona.core.designsystem.LocalHubFeatures provides features,
                persona.core.designsystem.LocalHubImageLimits provides imageLimits,
                persona.core.designsystem.LocalManagerRoster provides managers,
                persona.core.designsystem.LocalManagerGroup provides managerGroup,
                persona.feature.inbox.LocalTranslator provides translator,
                LocalAppStyleStore provides app.appearance,
                persona.core.designsystem.LocalStarPrompt provides app.starPrompt,
                LocalGlassLevel provides glassLevel,
                LocalPersonaDesign provides face,
                persona.core.designsystem.LocalPersonaStore provides app.personas,
                persona.core.designsystem.LocalPersonaPack provides pack,
                persona.core.designsystem.LocalThemeAccent provides persona.core.designsystem.ThemeColors.accent(themeColor, pack?.manifest?.colors),
                persona.core.designsystem.LocalPersonaName provides (pack?.manifest?.displayName(persona.core.designsystem.AppLanguages.shown(this@MainActivity).tag ?: "en") ?: "Persona"),
            ) {
                val dark = isSystemInDarkTheme()
                SideEffect {
                    WindowCompat.getInsetsController(window, window.decorView).apply {
                        isAppearanceLightStatusBars = !dark
                        isAppearanceLightNavigationBars = !dark
                    }
                }
                PersonaTheme(darkTheme = dark, style = style) {
                    val hub by app.hub.collectAsStateWithLifecycle()
                    val ready by app.pairingReady.collectAsStateWithLifecycle()
                    val failed by app.pairingRestoreError.collectAsStateWithLifecycle()
                    val capsuleFullScreen by capsules.fullScreen.collectAsStateWithLifecycle()
                    val capsulePresentation by capsules.presentation.collectAsStateWithLifecycle()
                    if (ready && capsuleFullScreen && capsulePresentation != null) persona.android.capsule.CapsuleFullScreen(capsules) {
                        capsules.returnToCapsule()
                        moveTaskToBack(true)
                    }
                    else if (ready) PlatformRoot(hub = hub, openNavigation = openNavigation, services = settingsServices, shell = shell)
                    else Surface(Modifier.fillMaxSize()) {
                        Column(Modifier.fillMaxSize().safeDrawingPadding().padding(24.dp.dense)) {
                            if (failed) {
                                Text(stringResource(R.string.app_pairing_unreadable))
                                TextButton(onClick = app::restorePairing) { Text(stringResource(R.string.app_retry), maxLines = 1) }
                            } else CircularProgressIndicator()
                        }
                    }
                }
            } }
        }
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        keepAmbientLightMoving()
        setIntent(intent)
        handle(intent)
    }

    override fun onStart() {
        super.onStart()
        val app = application as PersonaApp
        lifecycleScope.launch {
            app.pairingReady.first { it }
            app.hub.value?.kick()
        }
    }

    // Input that reaches this window keeps Glass's ambient light moving; five seconds after the last it holds still
    // (AmbientClock). Dialogs and bottom sheets are windows of their own: input there does not arrive here, so the
    // light behind their scrim stops five seconds after the input that opened them, on purpose.
    override fun onResume() {
        super.onResume()
        keepAmbientLightMoving()
    }

    override fun dispatchTouchEvent(ev: MotionEvent): Boolean {
        keepAmbientLightMoving()
        return super.dispatchTouchEvent(ev)
    }

    // Overridden only to note when the user last pressed a key; it calls super as before, so behaviour is unchanged.
    // Lint's RestrictedApi check is about calling ComponentActivity.dispatchKeyEvent from outside its library.
    @SuppressLint("RestrictedApi")
    override fun dispatchKeyEvent(event: KeyEvent): Boolean {
        keepAmbientLightMoving()
        return super.dispatchKeyEvent(event)
    }

    /** Mouse wheels, touchpads and hovering pointers on tablets and foldables. */
    override fun dispatchGenericMotionEvent(ev: MotionEvent): Boolean {
        keepAmbientLightMoving()
        return super.dispatchGenericMotionEvent(ev)
    }

    private fun handle(intent: Intent?) {
        if (intent == null) return
        val launchInput = launchAdapter?.read(intent)
        if (leavesCapsuleFullScreen(intent, launchInput != null)) capsules.leaveFullScreen()
        intent.getStringExtra(EXTRA_CAPSULE_URL)?.let { url ->
            val target = android.net.Uri.parse(url)
            if (target.scheme == "https" && !target.host.isNullOrBlank()) pendingCapsuleUrl.value = url
            intent.removeExtra(EXTRA_CAPSULE_URL)
        }
        if (intent.getBooleanExtra(LockScreenTests.EXTRA_LOCK_TEST, false)) {
            pendingLockTest.value = true
            intent.removeExtra(LockScreenTests.EXTRA_LOCK_TEST)
        }
        launchInput?.let { input ->
            darkTheme.value = input.darkTheme
            shell.automation = input.automation
            shell.posture.value = input.posture
            input.style?.let { (application as PersonaApp).appearance.setStyle(it) }
            openNavigation.value = focusing(input.resolve)
            input.prepare?.let { prepare -> lifecycleScope.launch {
                try { prepare(shell) }
                catch (cancelled: CancellationException) { throw cancelled }
                catch (error: Exception) { shell.feedback.failure(error) }
            } }
            input.pairing?.let { request ->
                lifecycleScope.launch {
                    try {
                        platformPairing.pair(request)
                        // Automated launches skip the first-run checklist so pages can be captured directly.
                        if (input.automation) platformPairing.completeReadiness()
                    } catch (cancelled: CancellationException) { throw cancelled }
                    catch (error: Exception) { shell.feedback.failure(error) }
                }
            }
            return
        }
        intent.getStringExtra(EXTRA_SESSION)?.let { session ->
            openNavigation.value = { it.parse(RouteInput.Notification(session)) }
        }
        val uri = intent.data ?: return
        if (uri.host == "password-login") {
            persona.android.platform.PasswordBrowserLogin.complete(uri)
            return
        }
        if (uri.host == "pair") {
            pendingPairLink.value = uri.toString()
            return
        }
        if (uri.scheme == "persona" && uri.host == "provider" && uri.path == "/import") {
            // A gateway's or a shared provider import link (#5428): the providers page reads it through the hub.
            ProviderImportRequests.pending.value = uri.toString()
            openNavigation.value = focusing { router -> router.parse(RouteInput.Internal(SettingsPageRoute(SettingsPage.Providers))) }
            return
        }
        val link = uri.toString()
        val notification = notificationRoute(uri)
        val terminal = terminalLink(link)
        openNavigation.value = focusing { router ->
            terminal ?: notification?.let { router.open(it) } ?: router.parse(RouteInput.DeepLink(link))
        }
    }

    /**
     * "Ask Persona" (`focus=composer`): the Persona page focuses its composer once the link is applied; a notification's
     * card outside any session opens on Agents.
     */
    private fun focusing(resolve: (Router) -> ResolvedRoute?): (Router) -> ResolvedRoute? = { router ->
        resolve(router)?.also {
            if (it.focusComposer) {
                val chat = it.key as? ChatRoute
                if (chat != null) persona.feature.chat.ChatComposerFocus.request(persona.core.contracts.SessionKey(chat.machineId, chat.sessionId))
                else persona.feature.assistant.AssistantComposerFocus.request()
            }
            // A card outside any session (#4808): Agents opens its sheet once the card is listed.
            it.card?.let(persona.feature.agents.AgentsCardOpen::request)
        }
    }

    companion object {
        val pendingLockTest = mutableStateOf(false)
        const val EXTRA_SESSION = "session"
        const val EXTRA_CAPSULE_URL = "persona.capsule.url"
        val pendingCapsuleUrl = mutableStateOf<String?>(null)
        /** A message link waiting for its hub group and replacement session; survives the shell's identity change. */
        val pendingHubLink = mutableStateOf<String?>(null)
        /** A pairing link the activity was opened with, for the pairing screen to confirm. */
        val pendingPairLink = mutableStateOf<String?>(null)
    }
}

/** Explicit notification, deep-link, capsule URL, lock-test and debug intents exit any lingering full-screen capsule mode. */
internal fun leavesCapsuleFullScreen(intent: Intent?, hasLaunchInput: Boolean = false): Boolean =
    intent != null && (
        hasLaunchInput ||
            intent.data != null ||
            intent.hasExtra(MainActivity.EXTRA_SESSION) ||
            intent.hasExtra(MainActivity.EXTRA_CAPSULE_URL) ||
            intent.getBooleanExtra(LockScreenTests.EXTRA_LOCK_TEST, false) ||
            intent.hasExtra("debug.openTab")
        )
