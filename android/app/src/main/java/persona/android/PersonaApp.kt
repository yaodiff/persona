package persona.android

import persona.core.designsystem.resolve
import android.app.Application
import dagger.hilt.android.HiltAndroidApp
import android.os.Build
import persona.android.data.Hub
import persona.android.data.PairingStore
import persona.android.di.ConnectionRuntime
import persona.android.di.WiredPairingService
import persona.core.contracts.api.HubConnection
import persona.platform.notify.Notify
import javax.inject.Inject
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.launch
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.flow.drop
import kotlinx.coroutines.flow.map
import kotlinx.coroutines.flow.buffer
import kotlinx.coroutines.flow.filter

/** Holds the one hub connection for the process: the activity and the foreground service share it. */
@HiltAndroidApp
class PersonaApp : Application() {
    val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)
    lateinit var pairings: PairingStore
        private set
    @Inject lateinit var runtime: ConnectionRuntime
    @Inject lateinit var connection: HubConnection
    @Inject lateinit var pairingService: WiredPairingService
    @Inject lateinit var platform: persona.android.platform.PlatformRuntime
    @Inject lateinit var capsule: persona.android.capsule.CapsuleRuntime
    /** The widget, the Quick Settings tile and the launcher shortcuts (platform/shortcuts). */
    @Inject lateinit var surfaces: persona.platform.shortcuts.SurfacesRuntime
    val pairingReady = MutableStateFlow(false)
    val pairingRestoreError = MutableStateFlow(false)

    /** Captures debug-only selection before the graph starts; this never creates a core. */
    val continuityFactory: (String, (persona.core.data.ContinuityAdapter.Applied) -> Unit) -> persona.core.data.ContinuityAdapter by lazy {
        if (BuildConfig.DEBUG) ContinuitySelection.factory(this)
        else { chat, _ -> persona.core.data.KotlinContinuityAdapter(chat) }
    }

    /** Kotlin by default; the optional DEBUG observer is chosen before the graph starts, never on the UI hot path. */
    val agentListAdapter: persona.core.data.AgentListAdapter by lazy {
        AgentSortSelection.wrap(this, persona.core.data.AgentListAdapters.create())
    }

    /** Settings › Interface style; device-wide, kept across pairings. */
    val appearance: persona.core.designsystem.AppStyleStore by lazy { persona.core.designsystem.AppStyleStore(this) }

    /** The GitHub star prompts (#5761); device-wide like the appearance, kept across pairings. */
    val starPrompt: persona.core.designsystem.StarPromptStore by lazy { persona.core.designsystem.StarPromptStore(this) }

    /** Settings › Appearance › Persona look's hub packages; device-wide, listed again whenever the hub comes online. */
    val personas: persona.core.designsystem.PersonaStore by lazy {
        persona.core.designsystem.PersonaStore(this, appearance) {
            runtime.scopedData.value?.let { persona.core.hub.HttpPersonaPacks(runtime.connectionFor(it)) }
        }
    }

    private val _hub = MutableStateFlow<Hub?>(null)
    val hub: StateFlow<Hub?> = _hub.asStateFlow()

    /** Notifications, the widget and the tile are built from this context: their text follows Settings › Language too. */
    override fun attachBaseContext(base: android.content.Context) {
        persona.core.designsystem.AppLanguages.init(base)
        super.attachBaseContext(persona.core.designsystem.AppLanguages.wrap(base))
        AppText.install(resources)
    }

    /** Device-local diagnostics, independent of the selected hub. */
    internal val developingPersona by lazy { persona.android.dev.DevelopingPersona(this, scope) }

    /** Crashes and ANRs for the hub's `error_reports` (docs/android/android.md "Error reports"). */
    private val errorReporting by lazy {
        persona.android.errors.ErrorReporting(this, developingPersona::recordError, developingPersona::recordExit)
    }

    override fun onCreate() {
        // Before Hilt builds the graph, so a crash there is written too.
        errorReporting.installCrashHandler()
        continuityFactory // Capture before Hilt, and before a debug Activity can save the next selection.
        agentListAdapter
        persona.core.domain.answer.AnswerAdapters.install(AnswerSelection.choose(this))
        super.onCreate()
        developingPersona.start()
        // The core modules' words (errors, the data repositories' state) in the shown language (docs/protocol/i18n.md).
        persona.core.contracts.CoreText.words = { it.resolve(this) }
        pairings = PairingStore(this)
        Notify.install(this, connection.events)
        runtime.start()
        platform.start()
        surfaces.start()
        capsule.start()
        restorePairing()
        errorReporting.start(scope, hub)
        scope.launch(Dispatchers.IO) {
            val _ = runCatching { persona.android.assist.AssistScreenshotStore.pruneExpired(this@PersonaApp) }
        }
        scope.launch {
            connection.state.map { it == persona.core.contracts.api.ConnectionState.Online }.distinctUntilChanged()
                .collect { online -> if (online) { val _ = runCatching { personas.refresh() } } }
        }
        // Theme color for the app's other windows (share, the capsule, dialogs): the main one provides the same accent itself.
        scope.launch {
            kotlinx.coroutines.flow.combine(appearance.color, personas.current) { color, pack ->
                persona.core.designsystem.ThemeColors.accent(color, pack?.manifest?.colors)
            }.collect { persona.core.designsystem.AppAppearance.accent = it }
        }
        // Light/dark for what Compose does not draw (WebView pages, system dialogs) too: the app's own night mode.
        scope.launch { appearance.scheme.collect { applyNightMode(it) } }
        // Placed widgets follow AppStyleStore and the active persona package; redraw them when style, theme color, or
        // the package's colours change.
        scope.launch {
            persona.platform.shortcuts.PersonaWidget
                .appearanceChanges(appearance.style, appearance.color, personas.current)
                .collect {
                    persona.platform.shortcuts.PersonaWidget.refresh(this@PersonaApp)
                    persona.platform.shortcuts.AskPersonaWidget.refresh(this@PersonaApp)
                    persona.platform.shortcuts.MainChatWidget.refresh(this@PersonaApp)
                }
        }
        // MainChatWidget also honors the app's explicit light/dark override.
        scope.launch {
            appearance.scheme.drop(1).collect {
                persona.platform.shortcuts.MainChatWidget.refresh(this@PersonaApp)
            }
        }
        // A tiered look moved to another tier: the new tier is downloaded whole before the face changes. The two events
        // are taken off the event flow first, so the download holds up no other live event (#4038).
        scope.launch {
            connection.events.filter { it is persona.core.contracts.PersonasEvent || it is persona.core.contracts.LookEvent }
                .buffer(kotlinx.coroutines.channels.Channel.UNLIMITED).collect { event ->
                    if (event is persona.core.contracts.PersonasEvent) { val _ = runCatching { personas.refresh() } }
                    // Another device chose a look (#3200): this phone shows it too.
                    if (event is persona.core.contracts.LookEvent) { val _ = runCatching { personas.follow(event.id) } }
                }
        }
    }

    /** The desktop apps (the Mac app) connected to the selected hub now, from its `desktops` event. */
    val desktops: StateFlow<List<persona.core.contracts.HubDesktop>> get() = runtime.desktops

    /** Android 12+ keeps a night mode per app; on 11 the windows still follow the setting through [PersonaTheme]. */
    private fun applyNightMode(choice: persona.core.designsystem.ColorSchemeChoice) {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.S) return
        val mode = when (choice.dark) {
            null -> android.app.UiModeManager.MODE_NIGHT_AUTO
            true -> android.app.UiModeManager.MODE_NIGHT_YES
            false -> android.app.UiModeManager.MODE_NIGHT_NO
        }
        val _ = runCatching { getSystemService(android.app.UiModeManager::class.java)?.setApplicationNightMode(mode) }
    }

    val deviceName: String get() = "${Build.MANUFACTURER} ${Build.MODEL}".trim()

    /** Picks up what the runtime restored or migrated, for first-run routing and the settings screens. */
    fun restorePairing() {
        scope.launch {
            pairingRestoreError.value = false
            try {
                pairingService.restore()
                pairingReady.value = true
            } catch (error: kotlinx.coroutines.CancellationException) { throw error }
            catch (_: Exception) { pairingRestoreError.value = true }
        }
    }

    internal fun showHub(value: Hub?) { _hub.value = value }

}
