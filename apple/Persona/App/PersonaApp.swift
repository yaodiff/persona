import SwiftUI
import UIKit
import UserNotifications
import PersonaWire

@main
struct PersonaApp: App {
    @UIApplicationDelegateAdaptor(AppDelegate.self) private var delegate
    @State private var model = AppModel.shared
    @Environment(\.scenePhase) private var phase

    var body: some Scene {
        WindowGroup {
            RootView()
                #if os(iOS)
                .personaCallCover() // the call to Persona's page and the bar back to it (#9290, CallScreen.swift)
                .urgentCallCover() // an answered urgent call's page, over everything (UrgentCallView.swift)
                .overlay { MeetingFloatingWindow() } // a meeting recording with its room put away (#8731 V5)
                .locationAlwaysOffer() // once, after a location read failed in the background (#13200)
                #endif
                #if DEBUG && os(iOS)
                .task { ProductionContinuityShadow.shared.install() } // Lazy: no core is created until a real input arrives.
                #endif
                #if DEBUG && os(iOS)
                .modifier(TreeSwapFlip()) // `-swapTreeAway YES` (#2836)
                .overlay { HomeGalleryOverlay() } // `-showHomeWidgets YES`: every widget, every size (docs/apple/home-widgets.md)
                .overlay { ButtonContrastOverlay() } // `-showButtonContrast YES`: secondary buttons on every surface (#6157)
                #endif
                .environment(model)
                // A link to one of Persona's pages opens in the app on every screen that has no link handling of its own
                // (#9926, docs/protocol/deep-links.md); any other link opens as it did.
                .environment(\.openURL, OpenURLAction { url in
                    guard let opened = model.openOwnLink(url) else { return .systemAction }
                    if case .sheet(let page) = opened { model.linkSheet = page }
                    return .handled
                })
                .rootHubLinkSheet($model.linkSheet)
                .sheet(isPresented: Binding(get: { model.templateLink != nil }, set: { if !$0 { model.templateLink = nil } })) {
                    NavigationStack { NativeTemplatesView() }
                        .environment(model) // the sheet has its own environment; NativeTemplatesView reads AppModel
                }
                // A pairing link from outside the app asks before it pairs (#9929).
                .pairConfirmation($model.pairToConfirm) { model.pairFromLink($0.config) }
                #if os(iOS)
                // A redial from the Phone app's Recents (#9290), where SwiftUI hands it over rather than the scene's delegate;
                // the two are one tap (`PersonaCalls.continueActivity` takes it once).
                .onContinueUserActivity(PersonaCalls.activityTypes[0]) { PersonaCalls.shared.continueActivity($0) }
                .onContinueUserActivity(PersonaCalls.activityTypes[1]) { PersonaCalls.shared.continueActivity($0) }
                #endif
                #if os(iOS)
                .onChange(of: model.hiddenNavigationPages) {
                    QuickAction.publish()
                    if case .todo? = model.linkSheet { model.returnToStartIfHidden(.todos) }
                }
                #endif
                .task(id: model.fleetLook) { LiveActivityController.shared.look(model.fleetLook) { model.fleetFaces } }
                .onOpenURL { url in
                    if let template = NativeTemplateLink.parameter(url) {
                        model.openNativeTemplate(template)
                        return
                    }
                    #if os(iOS)
                    if ShortcutCallbacks.shared.handle(url) { return } // a shortcut Persona ran came back (#2283)
                    #endif
                    if Modules.registry.handle(url) { return } // a module's own URL, as the Meta AI app's callback (docs/apple/ios.md, "Modules")
                    #if os(iOS)
                    if let link = FleetLink.session(url) { // tapped the Live Activity
                        // "group|machine" for another group's row (FleetMerge); a row the hub pushed names no group.
                        model.openExternal(path: link.machine + "/" + link.session)
                        return
                    }
                    if RecordingLink.matches(url) { MeetingRecorder.openRecordings(askingEnd: RecordingLink.endID(url)); return } // the recording's activity
                    if let kind = HomeWidgetLink.kind(url) { model.openWidget(kind); return } // a Home Screen widget
                    #endif
                    if let link = ExtensionSystemLink(url: url) { model.openExtension(link); return }
                    if SentSettingsLink.matches(url) { model.openSentRecords(); return }
                    if let link = ProviderImportLink.text(url) { model.openProviderImport(link); return } // a gateway's or a shared provider import link (#5428)
                    if IPhoneSetupLink.matches(url) { model.openIPhoneSetup(); return }
                    // The web sign-in page's code, read by the Camera app: only the scanner in Settings › Devices
                    // approves a browser (#10293), so a link says where to scan it and does nothing else.
                    if BrowserLoginLink.matches(url) {
                        model.toast = Toast(String(localized: "To sign in a browser, scan its code in Settings › Devices › Scan Browser Sign-in Code."))
                        return
                    }
                    #if os(iOS)
                    if let (group, page) = model.hubPage(url), page.navigationPage != nil,
                       model.openNavigationLink(page, group: group) != nil { return }
                    #endif
                    // A page of Persona's (persona://open<path>, an older persona:// form, #9926): only opens it.
                    if let opened = model.openOwnLink(url) {
                        if case .sheet(let page) = opened { model.linkSheet = page }
                        return
                    }
                    // A computer's pairing link (「添加电脑」, Add computer) is for Persona Link on that computer, not for this device (#4198).
                    if ServerConfig.isComputerLink(url) {
                        model.toast = Toast(String(localized: "Open this link on the computer you're adding"))
                        return
                    }
                    guard let cfg = ServerConfig(pairURL: url.absoluteString) else {
                        // A pairing link without its address or token did nothing at all: tapped again and again (#4273).
                        if ServerConfig.isPairLink(url.absoluteString) {
                            model.toast = Toast(String(localized: "The pairing link is incomplete. Copy it again."), style: .error)
                            Haptics.error()
                        }
                        return
                    }
                    // A link only opens pages: pairing waits for the user's Connect (#9929).
                    model.askToPair(cfg)
                }
        }
        #if os(visionOS)
        .defaultSize(width: Theme.Size.visionMainWindowWidth, height: Theme.Size.visionMainWindowHeight)
        #endif
        // `initial`: a launch into the foreground is already active here, and `start()` may have found the app in the
        // background (scene apps are, during didFinishLaunching): the socket opens now.
        .onChange(of: phase, initial: true) { old, new in
            if new == .active {
                model.retryRegistry() // launched in the background before the first unlock: the pairing reads now
                PushRegistrar.shared.foreground() // notifications allowed in Settings since a denial: register now (#1861)
                model.resumeLinks()
                PortForwarder.shared.ensureReadyAll()
                model.refreshExtensionSurfaces()
                model.textLookup.foreground() // the lookup extension turned on or off elsewhere (#10977), at most once a minute
                #if os(iOS)
                model.refreshPageVisibility()
                HomeWidgetWriter.shared.tellHub() // whether the hub should wake it for its widgets (#5721)
                #endif
                // Rolling recordings expire even when no segment arrives and the recording page stays closed.
                RecordingRollingKeeper.shared.schedule()
                // All-day recording: what waited while the app was away (a model can download now, a 403 pause ends), and
                // the audio past its retention.
                RecordingDayTranscriber.shared.foreground()
                RecordingDaySync.shared.foreground()
                RecordingDayKeeper.shared.schedule()
                LiveActivityController.shared.look(model.fleetLook) { model.fleetFaces }
                // The home screen icon in step with the Avatar and Light & Dark (a scheme from a launch argument, a switch the system
                // cut short, the old App Icon setting); nothing happens when it already is, so no alert.
                model.followAppIcon()
                Task {
                    if old != new { await model.refresh() }
                    #if os(iOS)
                    await WorkoutReports.shared.refresh(model: model)
                    #endif
                }
                #if os(iOS)
                ClipboardOfferWatcher.shared.returned(model) // copied something elsewhere: offer to send it to a computer
                RemindersImporter.shared.activate(model) // Reminders imported as to-dos, when that is on (#4009)
                // Refresh the health summary snapshot while unlocked, so a locked-phone phone_health_summary
                // call later can serve it instead of HealthKit's inaccessible-database error.
                Task { await HealthTool.refreshSnapshot() }
                // And the watched health types' sample snapshots (phone_health_watch, #13740); throttled.
                Task { await HealthSampleWatcher.shared.foreground() }
                Task { await HealthIntakeSync.shared.refresh(model: model) }
                SiriCalls.askOnce(paired: model.hub != nil) // a build with app.siri_calls only (#9290)
                BackgroundLocation.shared.cameToFront() // offers Always once after a read refused in the background
                #endif
            } else if new == .background {
                #if os(iOS)
                ClipboardOfferWatcher.shared.left()
                // The Live Activity follows the first group's hub (LiveActivityController.report): without a push route
                // there it would only go stale (#12982).
                LiveActivityController.shared.left(health: PushHealthStore.shared.health, group: model.links.first?.api.group)
                #endif
                // A suspended app's socket looks alive to the hub until it times out, and phone_* calls sent to it hang.
                // Closed, the hub knows the phone is away and wakes it with a silent push instead; and nothing opens it
                // again until the app is back (the network returning, a refresh ending: LiveConnection.suspended).
                model.suspendLinks()
                // A refresh still running from the last return to the app stops before its next write to the App Group:
                // iOS ends an app that is suspended holding that store's lock (0xdead10cc, #11360).
                ExtensionSurfaceForegroundRefresh.shared.cancel()
                // All-day recording: a run or request in progress finishes only inside a grant of background time, and a
                // model download a run started in front stops.
                RecordingDayTranscriber.shared.background()
                RecordingDaySync.shared.background()
                #if os(iOS)
                QuickAction.publish() // the home screen menu with the conversations' titles as they are now (#2191)
                RecordingDayCatchUp.scheduleIfNeeded() // transcribe and upload the day's backlog while charging
                #endif
                CacheBudget.runOnce() // chat pictures, Feishu media and WebKit's cache back under their budgets (#8341)
            }
        }
        #if os(visionOS)
        // A conversation opened in a window of its own (VisionScenes).
        VisionChatScene(model: model)
        #endif
    }
}

final class AppDelegate: NSObject, UIApplicationDelegate, UNUserNotificationCenterDelegate {
    #if os(iOS)
    /// Upright on an iPhone, but for the live screen's page (`AppOrientation`, #3075).
    func application(_ application: UIApplication, supportedInterfaceOrientationsFor window: UIWindow?) -> UIInterfaceOrientationMask {
        AppOrientation.mask
    }
    #endif

    func application(_ application: UIApplication, didFinishLaunchingWithOptions launchOptions: [UIApplication.LaunchOptionsKey: Any]? = nil) -> Bool {
        ErrorReports.installExceptionHandler(queue: .shared) // first: a crash while launching is reported too (#5621)
        ExtensionSurfaceAppLease.install() // background time around the App Group store's lock, before anything writes it (#11360)
        LogWaterRefresh.writeSnapshot = { group in
            AppModel.shared.handle(.healthIntake, group: group)
            #if os(iOS)
            await HomeWidgetWriter.shared.flushAndWait()
            #endif
        }
        // Developing Persona carries on recording when it was left on (#9460); reads two defaults otherwise.
        DevMode.shared.launch(tab: UserDefaults.standard.string(forKey: "app.tab").flatMap(AppTab.init(rawValue:)) ?? .assistant)
        _ = KeyboardState.shared // watches the keyboard from the start: a card may come while it is already up
        UNUserNotificationCenter.current().delegate = self
        Task { await ExtensionNotificationActions.preserveAndRegister { NotificationActions.categories } }
        ModuleState.shared.launch() // the active modules' launch hooks, as the Meta glasses' set-up (docs/apple/ios.md, "Modules")
        #if os(iOS)
        QuickAction.publish()
        // Here, for HealthKit to wake the app after a workout (#2526); only a build with background delivery reads the
        // reports' small state file this early.
        if WorkoutReports.deliversInBackground { WorkoutReports.shared.observe() }
        // The observers of the types phone_health_watch keeps current (#13740), for the same reason; a small defaults list.
        HealthSampleWatcher.shared.start()
        // Here too, for a launch iOS made for a significant location change: the Always session and the service are kept
        // only a few seconds after a relaunch (#13200). Asks nothing; one manager whose level arrives in a callback.
        BackgroundLocation.shared.launch()
        // Urgent calls' PushKit registry, when they are on: a VoIP push that launches the app reaches only one made now.
        UrgentCallPush.shared.start()
        #endif
        #if DEBUG && os(iOS)
        TerminationActivities.install() // a test's Live Activities end with the app, not at the next run's install (#10109)
        #endif
        #if DEBUG
        // Simulator automation: `simctl launch … -pairURL persona://pair?...`; `-pairURL2`, `-pairURL3` pair more hubs
        // (several groups, docs/protocol/multi-hub.md「Tests」).
        if let s = UserDefaults.standard.string(forKey: "pairURL"), let cfg = ServerConfig(pairURL: s) {
            let more = ["pairURL2", "pairURL3"].compactMap { UserDefaults.standard.string(forKey: $0).flatMap(ServerConfig.init(pairURL:)) }
            AppModel.shared.adopt([cfg] + more)
        }
        let d = UserDefaults.standard
        #if os(iOS)
        // `-showUrgentCallSample YES|held`: the urgent call page, from the mock hub (UrgentCallCenter.showSample); `held`
        // on hold with Resume. `-showUrgentCallSampleTitle <text>`: its title instead of the mock's (a long one).
        let urgentSampleHeld = d.string(forKey: "showUrgentCallSample") == "held"
        if d.bool(forKey: "showUrgentCallSample") || urgentSampleHeld {
            let title = d.string(forKey: "showUrgentCallSampleTitle")
            Task { @MainActor in
                try? await Task.sleep(for: .seconds(2))
                UrgentCallCenter.shared.showSample(held: urgentSampleHeld, title: title)
            }
        }
        // `-openOwnerCall YES`: calls Persona (#9290) once the hub is there, as the main conversation's Call does (on the
        // Simulator through `SimulatedCallSystem`); `-openOwnerCallSeconds <n>` hangs up n seconds after it connected.
        if d.bool(forKey: "openOwnerCall") {
            let seconds = d.integer(forKey: "openOwnerCallSeconds")
            Task { @MainActor in
                try? await Task.sleep(for: .seconds(3))
                await PersonaCalls.shared.callPersona(origin: .recents)
                guard seconds > 0 else { return }
                for _ in 0..<60 where PersonaCalls.shared.ownerCall?.connectedSince == nil && PersonaCalls.shared.ownerInProgress {
                    try? await Task.sleep(for: .milliseconds(500))
                }
                try? await Task.sleep(for: .seconds(seconds))
                PersonaCalls.shared.hangUp()
            }
        }
        // `-showCallSample dialing|connecting|connected|muted|held|interrupted|resume|speaker|ended|speaking|hearing|
        // thinking|working|asking|mutedSpeaking|paused|…`: the call page's state, with no system and no audio
        // (PersonaCalls.showSample; a speaking or hearing sample has a made-up voice level). `-showCallSampleName <text>`,
        // `-showCallSampleCaption <text>`, `-showCallSampleReason <text>`: the name (the page's and the bar's), the latest
        // words and why it ended, instead of the sample's own (long texts for the screenshots). `-showCallSampleLook
        // package|uploaded|<id>`: that look's avatar (waits up to 30 s for it to be on the phone; the mock's
        // MOCK_PERSONAS=builtin MOCK_LOOK=nwverse has a package). `-showCallSampleAgent YES`: a call to an Agent session.
        if let sample = d.string(forKey: "showCallSample").flatMap(PersonaCalls.Sample.init(rawValue:)) {
            let name = d.string(forKey: "showCallSampleName"), caption = d.string(forKey: "showCallSampleCaption")
            let reason = d.string(forKey: "showCallSampleReason"), wanted = d.string(forKey: "showCallSampleLook")
            let agent = d.bool(forKey: "showCallSampleAgent")
            Task { @MainActor in
                try? await Task.sleep(for: .seconds(2))
                var look: Persona?
                if let wanted {
                    for _ in 0..<60 {
                        look = PersonaCalls.sampleLook(wanted, model: AppModel.shared)
                        if look != nil { break }
                        try? await Task.sleep(for: .milliseconds(500))
                    }
                }
                PersonaCalls.shared.showSample(sample, name: name, caption: caption, reason: reason, look: look, agent: agent)
            }
        }
        #endif
        // `-openPairLink <persona://pair?…>`: pairs as a scanned link does (`pair`, then the device token trade), unlike
        // `-pairURL`; `-openPairLinkTimes 2` handles the same link again a few seconds later (#2766).
        if let s = d.string(forKey: "openPairLink"), let cfg = ServerConfig(pairURL: s) {
            let times = max(1, d.integer(forKey: "openPairLinkTimes"))
            Task { @MainActor in
                for i in 0..<times {
                    if i > 0 { try? await Task.sleep(for: .seconds(3)) }
                    do { try await AppModel.shared.pair(cfg) } catch { AppModel.shared.showPairing(error) }
                }
            }
        }
        PopFrames.startIfAsked()
        // `-openChatsFresh YES`: no chat kept on disk from an earlier run (LiveUpdateTests: churned steps piled up), every
        // report folded again (ReportFoldUITests), and nowhere a chat was left (`chat.read.<session>`: Persona's
        // conversations reopen there too since #1666).
        if d.bool(forKey: "openChatsFresh") {
            ChatCache.removeAll()
            OpenReports.shared.closeAll()
            let kept = d.persistentDomain(forName: Bundle.main.bundleIdentifier ?? "") ?? [:]
            for k in kept.keys where k.hasPrefix("chat.read.") { d.removeObject(forKey: k) }
        }
        // `-openImagesFresh YES`: no chat picture kept on disk from an earlier run, so a hub that fails it fails it (#182).
        if d.bool(forKey: "openImagesFresh") { try? FileManager.default.removeItem(at: URL.cachesDirectory.appending(path: "chat-images")) }
        // `-openAgentsFresh YES`: the Agents list's saved look forgotten (AgentsListTests).
        if d.bool(forKey: "openAgentsFresh") {
            for k in ["dev.machineFilter", HubFilter.key, "dev.grouping", "dev.sort", "dev.compact", "dev.folded", AgentRowStyle.key,
                      "agents.list.width", "agents.list.closed", SeenRows.key, SeenRows.noticedKey] { d.removeObject(forKey: k) }
        }
        // `-openSplitFresh YES`: the lists beside the pages shown (SplitListTests, #2624, #4206).
        if d.bool(forKey: "openSplitFresh") {
            for list in ["agents.list", "assistant.list", "sidebar.list"] { d.removeObject(forKey: list + ".closed") }
        }
        // `-openRecapsFresh YES`: no away recap closed yet, so the newest shows open again (RecapInFlowTests).
        if d.bool(forKey: "openRecapsFresh") { d.removeObject(forKey: "closedRecaps") }
        // `-openDraftsFresh YES`: nothing left in any chat's box from an earlier run, typed or over it (SelectTextTests
        // read a copy by pasting it there; a long one left behind makes the box tall and moves the chat, #603; a forward
        // a failed run left over a box takes the place of the same message forwarded again, #3838).
        if d.bool(forKey: "openDraftsFresh") {
            let model = AppModel.shared
            model.drafts = [:]
            model.composerContexts = [:]
            model.composerComments = [:]
        }
        // `-openScreenFresh` and `-openScreenDemo` belong to the screen-share module (its launch hook).
        // `-showLarkSendFresh YES`: no Feishu draft and no outbox (unsent, failed messages) from an earlier run:
        // LarkSendTests found earlier runs' failed messages and drafts in the chat (#1627).
        if d.bool(forKey: "showLarkSendFresh") {
            StateCache.remove(LarkDrafts.stateName)
            for dir in ["lark-outbox-v2", "lark-outbox-v1"] {
                try? FileManager.default.removeItem(at: URL.applicationSupportDirectory.appending(path: dir))
            }
        }
        PagePerf.runIfAsked()
        PagePerf.offsetIfAsked()
        FrameProbe.listenIfAsked()
        // Every tab a UI test can ask for. One it asks for and this does not know leaves the tab the last run saved
        // (`app.tab`): after a run with `-tab settings`, the shell opened on the Settings sheet and the
        // Feishu tests that asked for `-tab feishu` never saw the Messages page (#1664).
        switch d.string(forKey: "tab") {
        case "dev": AppModel.shared.selectedTab = .chats
        case "mail": AppModel.shared.selectedTab = .mail
        case "settings": AppModel.shared.selectedTab = .settings
        case "assistant": AppModel.shared.selectedTab = .assistant
        case "feishu": AppModel.shared.selectedTab = LarkHomePreference.shared.shows ? .feishu : .assistant
        default: break
        }
        // "machine/session", or "group|machine/session" for another group.
        if let open = d.string(forKey: "openSession") {
            // `-openFromDoneNotice YES`: as a tap on its "Done" notification (lands on the latest answer).
            AppModel.shared.openExternal(path: open, latestAnswer: d.bool(forKey: "openFromDoneNotice"))
        }
        if d.string(forKey: "showLark") != nil { AppModel.shared.selectedTab = .mail }
        if let open = d.string(forKey: "openAssistant"), let key = AppModel.shared.resolve(path: open) {
            AppModel.shared.selectedTab = .assistant
            AppModel.shared.assistantCurrent = key
        }
        // `-openHub <group id or 1-based index>`: the hub-level pages show that group (several groups).
        if let g = d.string(forKey: "openHub") {
            let ids = AppModel.shared.groupIDs
            AppModel.shared.selectHub(Int(g).flatMap { ids.indices.contains($0 - 1) ? ids[$0 - 1] : nil } ?? g)
        }
        if d.bool(forKey: "newAssistant") {
            AppModel.shared.selectedTab = .assistant
            AppModel.shared.assistantCurrent = nil
        }
        #if os(iOS)
        // `-openDictationControl YES`: the Dictate to Persona control's intent, including its cold-start request.
        if d.bool(forKey: "openDictationControl") { OpenPersonaDictationIntent.open(AppModel.shared) }
        #endif
        if let id = d.string(forKey: "openAssistantManager") { AppModel.shared.openAssistantManagerWhenListed(id) }
        AppModel.shared.debugShowNewChat = d.bool(forKey: "showNewChat")
        // `-simulateTap <machine>/<session>`: what tapping a push does, 4 s after launch (on another screen by then).
        if let tap = d.string(forKey: "simulateTap") {
            DispatchQueue.main.asyncAfter(deadline: .now() + 4) { AppModel.shared.openExternal(path: tap) }
        }
        // `-showTestNotification permission|plan|elicitation`: posts what an interaction push looks like (category, ids),
        // 3 s after the app goes to the background, so the UI test can exercise the notification without APNs.
        // `-showTestNotificationIn <machine>/<session>` names the session it is about (default: a made-up one).
        if let kind = d.string(forKey: "showTestNotification").flatMap(InteractionKind.init(rawValue:)) {
            let place = d.string(forKey: "showTestNotificationIn")
            Task { await AppDelegate.postTestNotification(kind, in: place) }
        } else if d.string(forKey: "showTestNotification") == "decide" {
            Task { await AppDelegate.postDecideTestNotification() }
        } else if let sample = d.string(forKey: "showTestNotification"),
                  ["extension-background", "extension-foreground", "extension-tap"].contains(sample) {
            Task { await AppDelegate.postExtensionTestNotification(sample) }
        }
        // `-showPushKey YES`: asks for notifications and writes this phone's push public key to Caches/push-key.txt, so
        // scripts/test/relay_sim_e2e.sh can seal a push to it and send it through a local relay (docs/operations/push-relay.md).
        if d.bool(forKey: "showPushKey") {
            Task {
                _ = try? await UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound])
                guard let key = PushCrypto.ensureKey(),
                      let dir = FileManager.default.urls(for: .cachesDirectory, in: .userDomainMask).first else { return }
                try? Data(PushCrypto.publicKey(key).utf8).write(to: dir.appending(path: "push-key.txt"))
            }
        }
        #if os(iOS)
        // `-showHomeWidgetSample YES` (or `ticked`): the widgets draw the gallery's made-up snapshot, written at once
        // (Home Screen screenshots without a hub, docs/apple/home-widgets.md「Screenshots」).
        if let sample = d.string(forKey: "showHomeWidgetSample"), sample != "NO" { HomeWidgetWriter.shared.showSample(sample) }
        #endif
        if let phase = d.string(forKey: "showFleetSample") {
            LiveActivityController.shared.showSample(phase, details: d.bool(forKey: "fleetDetails"), island: d.string(forKey: "fleetIsland"),
                                                     count: d.integer(forKey: "showFleetCount"))
        }
        #if os(iOS) && canImport(AlarmKit)
        // `-showAlarmSample timer|paused`: a 25-minute "Focus" timer set as phone_timer_start sets one (paused: then paused),
        // so its Live Activity can be photographed (docs/apple/live-activity.md "Alarms and timers (AlarmKit)").
        if #available(iOS 26.0, *), let sample = d.string(forKey: "showAlarmSample") {
            AlarmsTool.showSample(paused: sample == "paused")
        }
        #endif
        RuntimeCLI.startIfAsked() // `-runtimeCLI <port>`: scripts/app/personactl.py drives the running app
        let automation = d.string(forKey: "pairURL") != nil
        #else
        let automation = false
        #endif
        PhoneRecordingReceiver.shared.start()
        // All-day recording (docs/assistant/day-recap.md): its lanes follow the day store before WatchConnectivity can
        // deliver a chunk, and the catch-up task is registered before launching finishes, as the system requires.
        RecordingDayTranscriber.shared.start()
        RecordingDaySync.shared.start()
        #if os(iOS)
        RecordingDayCatchUp.register()
        #endif
        #if os(iOS)
        MeetingReplay.shared.clearEnded() // a replayed meeting tape (#10589) left from the last run leaves the recordings
        #endif
        #if DEBUG
        RecordingSamples.showMeetingIfAsked() // `-showMeetingSample recording|paused|interrupted|minutes|pending|overview`
        #if os(iOS)
        MeetingReminders.showIfAsked() // `-showMeetingReminder start|open`, after the made-up meetings are cleared
        #endif
        #if os(iOS)
        Task { await RecordingSamples.installDaySampleIfAsked() } // `-showDaySample today|paused|recap|pending|failed`
        #endif
        #if os(iOS)
        // `-openDayCatchUp YES`: runs the all-day catch-up now (the simulator never runs background tasks).
        if UserDefaults.standard.bool(forKey: "openDayCatchUp") { Task { _ = await RecordingDayCatchUp.catchUp() } }
        MeetingReplay.startIfAsked() // `-showMeetingReplay <tape>|sample` (#9417)
        MeetingTapeLibrary.installSampleIfAsked() // `-openMeetingTapes sample|empty` (#10589)
        MeetingTapeRecorder.startIfAsked() // `-openMeetingAudio <file>`: the meeting it records writes a tape
        #endif
        #endif
        WatchSync.shared.activate()
        AppModel.shared.start()
        AppIconBadgeWatcher.shared.start(AppModel.shared)
        LiveActivityController.shared.start()
        // A hub that retired this device's push registration answers its writes 409 (#9612): register again, then retry.
        APIClient.onPushRegistrationRequired = { group in await PushRegistrar.shared.registrationRequired(group: group).value }
        if !automation { PushRegistrar.shared.registerIfPossible() }
        return true
    }

    #if os(iOS)
    /// The home screen menu's taps reach the scene's delegate (`QuickActionSceneDelegate`).
    func application(_ application: UIApplication, configurationForConnecting session: UISceneSession,
                     options: UIScene.ConnectionOptions) -> UISceneConfiguration {
        let config = UISceneConfiguration(name: nil, sessionRole: session.role)
        if session.role == .windowApplication { config.delegateClass = QuickActionSceneDelegate.self }
        return config
    }
    #endif

    func application(_ application: UIApplication, didRegisterForRemoteNotificationsWithDeviceToken deviceToken: Data) {
        PushRegistrar.shared.didRegister(token: deviceToken)
    }

    func application(_ application: UIApplication, didFailToRegisterForRemoteNotificationsWithError error: Error) {
        PushRegistrar.shared.didFail(error)
    }

    /// Silent push from the hub (`persona_wake`): the assistant needs a phone_* tool while the app is in the background.
    /// iOS gives ~30 s; connect, let the hub deliver the call over the socket, then let go. A withdrawal push
    /// (`resolved_interaction_ids`) removes the notifications of cards that are over.
    func application(_ application: UIApplication, didReceiveRemoteNotification userInfo: [AnyHashable: Any],
                     fetchCompletionHandler completionHandler: @escaping (UIBackgroundFetchResult) -> Void) {
        // Through the relay the reason is sealed (docs/operations/push-relay.md): open it here, in the app's own process, with
        // each group's key (the one that opens it says whose hub needs the phone).
        guard let info = PushCrypto.notificationInfo(userInfo) else {
            completionHandler(.noData)
            return
        }
        if WaterReminderActions.reminderID(info) != nil {
            guard let group = WaterReminderOrigin(info).group(AppModel.shared) else {
                completionHandler(.noData)
                return
            }
            nonisolated(unsafe) let done = completionHandler
            Task {
                await LogWaterRefresh.run(group: group)
                done(.newData)
            }
            return
        }
        if let widget = info["extension_widget"] as? [String: Any],
           let owner = widget["extension"] as? String, let id = widget["id"] as? String,
           !owner.isEmpty, !id.isEmpty, !AppModel.shared.isDemo {
            let group = AppModel.shared.pushGroup(info)
            ExtensionWidgetReconciliation.invalidate(group: group, owner: owner, contribution: id)
            nonisolated(unsafe) let done = completionHandler
            Task {
                // Reconcile removals too: a stale subscription's wake can clear its last deleted widget.
                await ExtensionWidgetReconciliation.reportConfigurations(group: group)
                done(.newData)
            }
            return
        }
        if let entries = info["resolved_interactions"] as? [[String: Any]] {
            // Occurrence-bound entries protect a same-ID replacement whose alert arrived during the gathering delay.
            let cards = entries.compactMap { entry -> NotificationWithdrawal.Resolved? in
                guard let id = entry["id"] as? String, !id.isEmpty else { return nil }
                return NotificationWithdrawal.Resolved(interactionID: id, interactionCreatedAt: entry["created_at"] as? String)
            }
            nonisolated(unsafe) let done = completionHandler
            AppModel.shared.withdrawResolvedOccurrences(cards, group: AppModel.shared.pushGroup(info)) { done(.newData) }
            return
        }
        if let ids = info["resolved_interaction_ids"] as? [String] {
            // Cards answered elsewhere while suspended (#4349): their notifications leave the lock screen.
            nonisolated(unsafe) let done = completionHandler
            AppModel.shared.withdrawResolved(ids, group: AppModel.shared.pushGroup(info)) { done(.newData) }
            return
        }
        let wake = info["persona_wake"] ?? info[LegacyNames.wakeKey]
        guard wake != nil, AppModel.shared.isPaired, !AppModel.shared.isDemo else {
            completionHandler(.noData)
            return
        }
        // That group's hub (`hub_id`, the key); unknown: every group.
        let group = AppModel.shared.pushGroup(info)
        AppModel.shared.wakeLinks(group)
        #if os(iOS)
        Task {
            await HomeWidgetData.shared.restore()
            HomeWidgetWriter.shared.tellHub(forceRefresh: true) // a cold wake obtains the installed widget kinds
        }
        HomeWidgetData.shared.refresh() // the calendar, Health and the usage the widgets show, when due
        let intakeSync = Task { await HealthIntakeSync.shared.refresh(model: .shared) }
        #endif
        nonisolated(unsafe) let done = completionHandler
        DispatchQueue.main.asyncAfter(deadline: .now() + 25) {
            #if os(iOS)
            intakeSync.cancel()
            HomeWidgetWriter.shared.flush() // the hub woke it for the widgets (#5721), or anything they show changed
            #endif
            if UIApplication.shared.applicationState == .background { AppModel.shared.suspendLinks() }
            done(.newData)
        }
    }

    // Completion-handler versions on purpose: the async variants of these delegate methods return on a background
    // executor, so UIKit gets its completion off the main thread and crashes when a tap opens the app. Hop to the main
    // queue, do the work there and call back from there.
    nonisolated func userNotificationCenter(_ center: UNUserNotificationCenter, willPresent notification: UNNotification,
                                            withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void) {
        let info = PushCrypto.notificationInfo(notification.request.content.userInfo)
        let ref = info.flatMap(Self.sessionRef)
        let waterOrigin = info.flatMap { WaterReminderActions.reminderID($0) == nil ? nil : WaterReminderOrigin($0) }
        nonisolated(unsafe) let content = notification.request.content.copy() as! UNNotificationContent
        nonisolated(unsafe) let done = completionHandler
        Task { @Sendable in
            await ExtensionNotificationActions.register(content: content)
            DispatchQueue.main.async {
                let (key, visible, quiet) = MainActor.assumeIsolated {
                    if let waterOrigin, let group = waterOrigin.group(AppModel.shared) {
                        Task { await LogWaterRefresh.run(group: group) }
                    }
                    let key = ref.flatMap(Self.sessionKey)
                    // A quiet task's agent (#1731) is the main conversation's to tell; a hub that knows sends no push for it.
                    return (key, AppModel.shared.visibleSession, key.map(AppModel.shared.isQuiet) ?? false)
                }
                done(key != nil && (key == visible || quiet) ? [] : [.banner, .sound, .list])
            }
        }
    }

    nonisolated func userNotificationCenter(_ center: UNUserNotificationCenter, didReceive response: UNNotificationResponse,
                                            withCompletionHandler completionHandler: @escaping () -> Void) {
        #if os(iOS)
        // An urgent call's companion notification (UrgentCallCenter): answers the call while it rings, else opens its
        // conversation.
        let callInfo = response.notification.request.content.userInfo
        if let call = callInfo[UrgentCallCenter.callKey] as? String {
            let group = callInfo[PushCrypto.groupKey] as? String ?? ""
            nonisolated(unsafe) let done = completionHandler
            DispatchQueue.main.async {
                MainActor.assumeIsolated {
                    UrgentCallCenter.shared.opened(callID: call, group: group)
                    done()
                }
            }
            return
        }
        // "Recording paused" (MeetingRecorder.notifyPaused): open Recordings; coming to the front resumes it.
        if response.notification.request.content.userInfo[RecordingLink.notificationKey] != nil {
            nonisolated(unsafe) let done = completionHandler
            let opened = response.actionIdentifier != UNNotificationDismissActionIdentifier
            DispatchQueue.main.async {
                MainActor.assumeIsolated {
                    if opened { MeetingRecorder.openRecordings() }
                    done()
                }
            }
            return
        }
        // A calendar meeting's reminder (MeetingReminders, #8731 G2): Start starts the meeting, a tap opens Meetings.
        if let meeting = MeetingReminders.meeting(response.notification.request.content.userInfo) {
            nonisolated(unsafe) let done = completionHandler
            let action = response.actionIdentifier
            DispatchQueue.main.async {
                MainActor.assumeIsolated {
                    MeetingReminders.respond(action: action, meeting: meeting)
                    done()
                }
            }
            return
        }
        #endif
        guard let info = PushCrypto.notificationInfo(response.notification.request.content.userInfo) else {
            completionHandler()
            return
        }
        let ref = Self.sessionRef(info)
        let action = response.actionIdentifier
        let text = (response as? UNTextInputNotificationResponse)?.userText
        let ixID = info["interaction_id"] as? String
        let ixCreatedAt = info["interaction_created_at"] as? String
        let extensionNotice = info["extension_notification"] != nil
        let pair = PairPush(info) // a pairing receipt (#4199); nil from an older hub: a plain notification
        let decision = NotificationActions.decision(action: action, info: info) // Take Recommendation on a decision's push
        #if os(iOS)
        let pushedRecording = MeetingTalk.recordingID(info) // a meeting's minutes (#3862 phase 6)
        #endif
        let noteID = response.notification.request.identifier
        let event = ExtensionNotificationEvent(id: response.notification.request.identifier, info: info,
                                              category: response.notification.request.content.categoryIdentifier,
                                              actionID: action, text: text)
        let water = WaterReminderActions.request(category: response.notification.request.content.categoryIdentifier,
                                                 action: action, info: info, text: text)
        // Reuse the extension event's strict origin routing, even though this is a built-in notification.
        let waterOrigin = WaterReminderOrigin(info)
        nonisolated(unsafe) let done = completionHandler
        DispatchQueue.main.async {
            MainActor.assumeIsolated {
                if let water {
                    let model = AppModel.shared
                    if let group = waterOrigin.group(model) {
                        AppDelegate.inBackground("water-reminder", done: done) { await WaterReminderActions.run(water, group: group) }
                    } else {
                        AppDelegate.inBackground("water-reminder", done: done) {
                            await WaterReminderActions.show(String(localized: "Can't find the hub this notification came from. Reconnect, then do it in the app."), id: water.reminder)
                        }
                    }
                    return
                }
                if extensionNotice {
                    if let event { ExtensionNotificationDelivery.run(event, done: done) }
                    else { done() }
                    return
                }
                if let pair {
                    if let request = PairReceiptActions.request(action: action, pair) {
                        // Undo / It's Mine / Not Mine: done in the background, the result posted as a local notification.
                        AppDelegate.inBackground("pair-receipt", done: done) { await PairReceiptActions.run(request, pair, id: noteID) }
                        return
                    }
                    if action == UNNotificationDefaultActionIdentifier {
                        let model = AppModel.shared
                        model.openPair(PairReceiptActions.destination(pair, group: model.pushGroup(pair.userInfo)))
                    }
                    done()
                    return
                }
                // The group the push came from (docs/protocol/multi-hub.md「Push and notifications」): its hub settles the button, its session opens.
                let key = ref.flatMap(Self.sessionKey)
                if let key, let reply = NotificationActions.replyText(action: action, text: text) {
                    // Reply on a finished turn: sent in the background, like a button (#3449).
                    AppDelegate.inBackground("reply-from-notification", done: done) { await NotificationActions.sendReply(key, text: reply) }
                    return
                }
                if let decision {
                    // Take Recommendation on a decision's push (#2056): answered in the background, the app stays where it is.
                    AppDelegate.inBackground("take-recommendation", done: done) { await NotificationActions.take(decision, group: key?.group ?? ref?.group) }
                    return
                }
                if let ixID, let resolution = NotificationActions.resolution(action: action, text: text),
                   NotificationActions.answerable(ixID, occurrence: ixCreatedAt, among: AppModel.shared.pendingInteractions) {
                    // A button on the notification: settle it in the background, the app stays where it is. A card its
                    // task's supervisor holds is not settled from here: the tap opens its session below, as a plain tap does.
                    AppDelegate.resolveFromNotification(ixID, group: key?.group ?? ref?.group, resolution, occurrence: ixCreatedAt, action: action, done: done)
                    return
                }
                #if os(iOS)
                // A meeting's minutes (#3862 phase 6): the meeting's thread when its recording is on this phone; else, and
                // from a hub that doesn't name it, the session the push names, as before.
                if let recording = MeetingTalk.threadToOpen(pushedRecording, interaction: ixID, action: action,
                                                            onPhone: { RecordingLibrary.shared.entry($0) != nil }) {
                    MeetingRecorder.openMeeting(recording)
                    done()
                    return
                }
                #endif
                // No interaction: a turn finished ("Done"), opened at its answer like from the list.
                if action != UNNotificationDismissActionIdentifier, let ref {
                    let model = AppModel.shared
                    model.openExternal(machineID: ref.machine, sessionID: ref.session, group: ref.sourceGroup(model), latestAnswer: ixID == nil)
                }
                done()
            }
        }
    }

    /// Runs a notification button against the hub. iOS may have launched the app in the background just for this, so
    /// hold a background task until the request is answered; a failure comes back as a local notification, because
    /// the user is not looking at the app.
    static func resolveFromNotification(_ id: String, group: String?, _ resolution: Resolution, occurrence: String? = nil, action: String, done: @escaping () -> Void) {
        inBackground("resolve-interaction", done: done) {
            do {
                try await AppModel.shared.outside(HubStore.client(group: group)) { try await $0.resolve(id, resolution, occurrence: occurrence) }
            } catch {
                await NotificationActions.reportFailure(id: id, title: NotificationActions.failureTitle(action: action), error: error)
            }
        }
    }

    /// `work` under a background task: iOS may have launched the app in the background just for a notification's
    /// button, and suspends it the moment `done` is called.
    static func inBackground(_ name: String, done: @escaping () -> Void, _ work: @escaping () async -> Void) {
        let app = UIApplication.shared
        var task = UIBackgroundTaskIdentifier.invalid
        task = app.beginBackgroundTask(withName: name) {
            app.endBackgroundTask(task)
            task = .invalid
        }
        Task {
            await work()
            done()
            if task != .invalid { app.endBackgroundTask(task) }
        }
    }

    #if DEBUG
    static func postTestNotification(_ kind: InteractionKind, in place: String? = nil) async {
        let center = UNUserNotificationCenter.current()
        _ = try? await center.requestAuthorization(options: [.alert, .sound])
        // The answer can come back "not granted" while the alert is still being tapped: wait for the setting itself.
        for _ in 0..<60 where await center.notificationSettings().authorizationStatus != .authorized {
            try? await Task.sleep(for: .milliseconds(500))
        }
        // Delivered while the app is in the background, like a real push.
        for await _ in NotificationCenter.default.notifications(named: UIApplication.didEnterBackgroundNotification) { break }
        let content = UNMutableNotificationContent()
        switch kind { // the hub's words (push.json), as it writes them in Chinese
        case .plan: (content.title, content.body) = ("计划等你审批", "支持 HEIC") // i18n-ignore
        case .elicitation: (content.title, content.body) = ("MCP 服务 deploy-mcp 等你处理", "确认这次部署的参数（打开 Persona 填写）") // i18n-ignore
        default: (content.title, content.body) = ("需要你授权", "Bash: swift test") // i18n-ignore
        }
        content.subtitle = "测试会话 · 测试机器" // i18n-ignore
        content.categoryIdentifier = NotificationActions.category(kind)
        let (machine, session) = place.flatMap { p in p.firstIndex(of: "/").map { (String(p[..<$0]), String(p[p.index(after: $0)...])) } } ?? ("test", "test")
        content.userInfo = ["interaction_id": "ix_test_\(kind.rawValue)", "machine_id": machine, "session_id": session]
        try? await center.add(UNNotificationRequest(identifier: "test-\(kind.rawValue)", content: content,
                                                     trigger: UNTimeIntervalNotificationTrigger(timeInterval: 3, repeats: false)))
    }
    /// `-showTestNotification decide` (#2056): what a decision's push looks like (`decidePayloadIn` in
    /// server/internal/hub/push_payload.go), for the first to-do the paired hub lists with a recommendation, its round as
    /// the hub sent it; delivered 3 s after the app goes to the background.
    static func postDecideTestNotification() async {
        let center = UNUserNotificationCenter.current()
        _ = try? await center.requestAuthorization(options: [.alert, .sound])
        for _ in 0..<60 where await center.notificationSettings().authorizationStatus != .authorized {
            try? await Task.sleep(for: .milliseconds(500))
        }
        TasksStore.shared.reloadTodos()
        var found: (TodoItem, TodoAsk)?
        for _ in 0..<40 {
            found = TasksStore.shared.todos.values.sorted { $0.id < $1.id }
                .compactMap { t in t.openAsk.flatMap { $0.recommend?.isEmpty == false ? (t, $0) : nil } }.first
            if found != nil { break }
            try? await Task.sleep(for: .milliseconds(500))
        }
        guard let (todo, ask) = found else { return }
        for await _ in NotificationCenter.default.notifications(named: UIApplication.didEnterBackgroundNotification) { break }
        let content = UNMutableNotificationContent()
        content.title = "等你决定" // i18n-ignore: the hub's push.json decide_title
        content.body = ask.verdict ?? todo.title
        content.subtitle = todo.title
        content.categoryIdentifier = NotificationActions.decideCategory
        content.userInfo = ["machine_id": "test", "session_id": "test", "todo_id": todo.id,
                            "todo": ["id": todo.id, "asked_at": ask.askedAt, "recommend": ask.recommend ?? ""]]
        try? await center.add(UNNotificationRequest(identifier: "test-decide", content: content,
                                                     trigger: UNTimeIntervalNotificationTrigger(timeInterval: 3, repeats: false)))
    }
    static func postExtensionTestNotification(_ sample: String) async {
        let center = UNUserNotificationCenter.current()
        _ = try? await center.requestAuthorization(options: [.alert, .sound])
        for _ in 0..<60 where await center.notificationSettings().authorizationStatus != .authorized {
            try? await Task.sleep(for: .milliseconds(500))
        }
        let content = UNMutableNotificationContent()
        content.title = "扩展通知测试"
        content.body = "示例扩展请求处理一条通知"
        content.categoryIdentifier = "ext.notice-demo." + UUID().uuidString
        content.userInfo = ["extension_notification": ["extension": "notice-demo", "ui": "home", "state": "notice-state",
            "actions": [["id": "reply", "title": "回复", "input": ["placeholder": "输入回复"]],
                        ["id": "open", "title": "打开处理", "foreground": true],
                        ["id": "stop", "title": "停用示例", "role": "destructive"]]]]
        // simctl push does not run the NSE. This only reuses its pre-delivery registration; it does not prove the first real-device APNs push.
        nonisolated(unsafe) let ready = content.copy() as! UNNotificationContent
        await ExtensionNotificationActions.register(content: ready)
        var background = NotificationCenter.default.notifications(named: UIApplication.didEnterBackgroundNotification).makeAsyncIterator()
        if let api = AppModel.shared.api, ["127.0.0.1", "localhost"].contains(api.config.url.host() ?? "") {
            _ = try? await api.request("POST", "/mock/extension-notifications/ready")
        }
        _ = await background.next()
        try? await center.add(UNNotificationRequest(identifier: "extension-test-" + sample, content: content,
                                                    trigger: UNTimeIntervalNotificationTrigger(timeInterval: 3, repeats: false)))
    }
    #endif

    nonisolated static func sessionRef(_ info: [AnyHashable: Any]) -> PushRef? { PushRef(info) }
    static func sessionKey(_ ref: PushRef) -> SessionKey { ref.key(AppModel.shared) }
}
