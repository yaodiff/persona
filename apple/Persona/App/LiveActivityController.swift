#if canImport(ActivityKit)
import ActivityKit
#endif
import Foundation
import OSLog
import Security
import SwiftUI
import UIKit

/// The app's half of the Fleet Live Activity (docs/apple/live-activity.md). The hub decides what it shows and pushes it while
/// the app is away; the app
///   - reports the push-to-start token (so the hub can open an activity when the app is not running) and each
///     activity's own push token (it rotates), and says when an activity ended;
///   - while open, applies the hub's `fleet` events itself (faster than the push, and the only way on the simulator):
///     starts the activity when there is something to show, updates it, ends it after a minute of nothing.
// visionOS has no Live Activities: Vision/App/VisionStandIns.swift keeps the same API (docs/apple/vision.md).
#if os(iOS)
@MainActor
final class LiveActivityController {
    static let shared = LiveActivityController()

    /// Settings › Notifications › Live Activities. Mirrored from the hub prefs so the app can decide without asking.
    static let enabledKey = "liveActivity.enabled"
    static let detailsKey = "liveActivity.lockDetails"
    static let islandKey = "liveActivity.island"
    var enabled: Bool { UserDefaults.standard.object(forKey: Self.enabledKey) as? Bool ?? true }
    var lockDetails: Bool { UserDefaults.standard.bool(forKey: Self.detailsKey) }
    var island: IslandPrefs {
        UserDefaults.standard.data(forKey: Self.islandKey).flatMap { try? JSONDecoder().decode(IslandPrefs.self, from: $0) } ?? IslandPrefs()
    }

    private var watching: Set<String> = []
    /// Activities `keepOne` is ending: their end runs on, and until then nothing updates them in place of the kept one.
    private var ending: Set<String> = []
    private var endTask: Task<Void, Never>?
    private var started = false

    private var activity: Activity<LegacyFleetActivityAttributes>? {
        Activity<LegacyFleetActivityAttributes>.activities.first { Self.running($0.activityState) && !ending.contains($0.id) }
    }

    /// Keep activities created by the interim canonical-identity builds alive too.
    private var canonicalActivity: Activity<PersonaActivityAttributes>? {
        Activity<PersonaActivityAttributes>.activities.first { Self.running($0.activityState) && !ending.contains($0.id) }
    }
    private var runningContent: ActivityContent<FleetState>? { canonicalActivity?.content ?? activity?.content }
    private var hasActivity: Bool { activity != nil || canonicalActivity != nil }

    private func updateRunning(_ content: ActivityContent<FleetState>) {
        if let activity { Self.update(activity, content) }
        if let canonicalActivity { Self.update(canonicalActivity, content) }
    }

    private func needsUpdate(_ state: FleetState) -> Bool {
        let legacy = activity.map { Self.worthUpdating($0.content.state, staleDate: $0.content.staleDate, to: state, now: .now) } ?? false
        let canonical = canonicalActivity.map { Self.worthUpdating($0.content.state, staleDate: $0.content.staleDate, to: state, now: .now) } ?? false
        return legacy || canonical
    }

    /// The device's settings changed (or were loaded from the hub): remember them, and apply at once — off ends the
    /// activity, the lock screen details and the island's sides change on the running one, on starts one from the hub's
    /// current fleet.
    func settings(_ prefs: NotifyPrefs) {
        let was = (enabled, lockDetails, island)
        UserDefaults.standard.set(prefs.liveActivity, forKey: Self.enabledKey)
        UserDefaults.standard.set(prefs.lockDetails, forKey: Self.detailsKey)
        UserDefaults.standard.set(try? JSONEncoder().encode(prefs.island), forKey: Self.islandKey)
        guard was != (prefs.liveActivity, prefs.lockDetails, prefs.island) else { return }
        if !prefs.liveActivity {
            endAll()
            return
        }
        if let content = runningContent {
            // At once from what is showing (another machine's load comes with the hub's fleet below).
            let state = content.state.tailored(island: prefs.island, details: prefs.lockDetails, look: AppModel.shared.fleetLook)
            updateRunning(ActivityContent(state: state, staleDate: content.staleDate, relevanceScore: Self.relevance(state)))
        }
        // Every group's fleet, merged (docs/protocol/multi-hub.md「Live Activities」).
        let model = AppModel.shared
        Task { await model.loadFleets() }
    }

    /// Once per launch: watch tokens and activities the hub push-started.
    func start() {
        guard !started else { return }
        started = true
        Task {
            for await token in Activity<LegacyFleetActivityAttributes>.pushToStartTokenUpdates {
                report(.init(pushToStartToken: token.hex))
            }
        }
        Task {
            for await activity in Activity<LegacyFleetActivityAttributes>.activityUpdates { watch(activity) }
        }
        Task {
            for await activity in Activity<PersonaActivityAttributes>.activityUpdates { watch(activity) }
        }
        for activity in Activity<LegacyFleetActivityAttributes>.activities { watch(activity) }
        for activity in Activity<PersonaActivityAttributes>.activities { watch(activity) }
        keepOne()
    }

    /// The hub's current fleet (the `fleet` event, or GET /api/v1/fleet on becoming active).
    func apply(_ hubState: FleetState) {
        let state = hubState.tailored(island: island, details: lockDetails, look: AppModel.shared.fleetLook)
        guard enabled, ActivityAuthorizationInfo().areActivitiesEnabled else {
            endAll()
            return
        }
        keepOne()
        let step = Self.step(state, running: hasActivity, active: UIApplication.shared.applicationState == .active)
        if step == .endSoon {
            // The last request went (answered here or on a computer, or gone): off the activity now, with its buttons,
            // as the hub's push does while the app is away. The minute below still runs.
            if let content = runningContent, Self.clearsNow(content.state, to: state) {
                updateRunning(ActivityContent(state: state, staleDate: content.staleDate, relevanceScore: Self.relevance(state)))
            }
            // Agents flicker between turns: end only when nothing came back for a minute (the hub does the same).
            guard endTask == nil else { return }
            endTask = Task { [weak self] in
                try? await Task.sleep(for: .seconds(60))
                guard !Task.isCancelled else { return }
                self?.endAll()
                self?.endTask = nil
            }
            return
        }
        endTask?.cancel()
        endTask = nil
        let content = ActivityContent(state: state, staleDate: .now.addingTimeInterval(15 * 60), relevanceScore: Self.relevance(state))
        switch step {
        case .update:
            guard needsUpdate(state) else { return }
            updateRunning(content)
        case .start:
            request(content)
        case .endSoon, .nothing:
            break
        }
    }

    /// What `apply` does with a device's version of the fleet (`tailored`).
    nonisolated enum Step: Equatable, Sendable { case endSoon, update, start, nothing }

    /// `running`: an activity is on; `active`: the app is in front (only then can it start one). Nothing to show ends
    /// a running activity a minute later; the assistant answering on a side of the island is something to show and
    /// keeps it, though it starts none (#4628, as the hub, #4590). A new one starts in front for agents that count,
    /// not for finished ones alone.
    nonisolated static func step(_ state: FleetState, running: Bool, active: Bool) -> Step {
        if state.showsNothing { return running ? .endSoon : .nothing }
        if running { return .update }
        return active && state.phase != .none && state.phase != .done ? .start : .nothing
    }

    /// Whether an activity showing `shown` is cleared at once when the fleet leaves it nothing to show (`state`, an
    /// `.endSoon`): only when it still shows something needing the user. Anything else (the last agent done and seen,
    /// the assistant done) stays the minute until the end, as the hub pushes nothing for it either (liveactivity.go
    /// `flush`, #8636).
    nonisolated static func clearsNow(_ shown: FleetState, to state: FleetState) -> Bool {
        state.showsNothing && (shown.needsYou > 0 || shown.items.contains(where: \.needsYou))
    }

    /// Takes a settled request occurrence off the activity at once (the hub's update follows). A stale answer's 410
    /// must not remove a same-ID replacement that the activity already draws.
    func resolvedLocally(_ interactionID: String, occurrence: String? = nil) {
        guard let content = runningContent else { return }
        var state = content.state
        guard let i = state.items.firstIndex(where: {
            $0.interaction == interactionID && (occurrence == nil || $0.interactionCreatedAt == occurrence)
        }) else { return }
        state.items.remove(at: i)
        state.needsYou = max(0, state.needsYou - 1)
        if state.needsYou == 0 { state.phase = state.working > 0 ? .working : (state.unread > 0 ? .done : .none) }
        updateRunning(ActivityContent(state: state, staleDate: content.staleDate))
    }

    /// The persona and its color changed, or the app started or came back (what failed is tried again). It goes three
    /// ways (docs/apple/live-activity.md「Avatar」):
    ///   - into the running activity's content now, which also makes the system draw it again;
    ///   - to the hub, which puts it in this device's pushes (`look` in the device's prefs);
    ///   - into the App Group container shared with the widget extension, with a package's pictures (`FleetLook.save`).
    /// `force` (Dynamic Island Diagnostics): written anew everywhere and redrawn even when nothing changed.
    func look(_ look: FleetLook, force: Bool = false, faces: () -> FleetFaces?) {
        _ = Self.forgetKeychainLook
        if force {
            _ = FleetLookStore.shared.write(nil, FleetLookStore.lookFile)
            _ = FleetLookStore.shared.save(nil)
            toldHub = nil
        }
        let saved = FleetLook.save(look, faces: faces)
        tellHub(look)
        guard let content = runningContent else { return }
        guard content.state.look != look || saved || force else { return }
        var state = content.state
        state.look = look
        // A new stale date too: an update identical to what is showing may draw nothing.
        let stale = (content.staleDate ?? .now.addingTimeInterval(15 * 60)).addingTimeInterval(1)
        updateRunning(ActivityContent(state: state, staleDate: stale, relevanceScore: content.relevanceScore))
    }

    /// The look in the running activity's content: nil without an activity (Dynamic Island Diagnostics).
    var runningLook: FleetLook?? { runningContent.map { $0.state.look } }

    /// The look the hub has for this device (nil: not told since launch).
    private var toldHub: FleetLook?
    private var telling: Task<Void, Never>?

    private func tellHub(_ look: FleetLook) {
        guard toldHub != look, let api = AppModel.shared.api, !api.config.isDemo else { return }
        telling?.cancel()
        telling = Task { [weak self] in
            do {
                try await api.setFleetLook(device: PushRegistrar.deviceID, look)
                guard !Task.isCancelled else { return }
                self?.toldHub = look
            } catch {
                FleetLookStore.log.error("telling the hub the look: \(error.localizedDescription, privacy: .public)")
            }
        }
    }

    /// Builds 92–103 left the look and the package's pictures in a keychain group the extension could not read on a
    /// phone; keychain items outlive the app, so they go once a launch (the group stays in the app's entitlements
    /// for this).
    private static let forgetKeychainLook: Void = {
        let status = SecItemDelete([kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: "persona.look"] as CFDictionary)
        if status == errSecSuccess { FleetLookStore.log.notice("deleted the keychain look of an older build") }
    }()

    /// One Fleet card on the Lock Screen (#12982): of every activity this app has, of either attribute type, the running
    /// one updated last stays; every other ends at once, ended ones too — iOS ends an activity after about eight hours
    /// but leaves it on the Lock Screen, stale, for hours, where `activity` no longer sees it and a new one would start
    /// beside it.
    func keepOne() {
        let legacy = Activity<LegacyFleetActivityAttributes>.activities.map {
            Self.Candidate(id: $0.id, running: Self.running($0.activityState), staleDate: $0.content.staleDate)
        }
        let canonical = Activity<PersonaActivityAttributes>.activities.map {
            Self.Candidate(id: $0.id, running: Self.running($0.activityState), staleDate: $0.content.staleDate)
        }
        let keep = Self.kept(legacy + canonical)
        for activity in Activity<LegacyFleetActivityAttributes>.activities where activity.id != keep && ending.insert(activity.id).inserted {
            Self.end(activity)
        }
        for activity in Activity<PersonaActivityAttributes>.activities where activity.id != keep && ending.insert(activity.id).inserted {
            Self.end(activity)
        }
    }

    /// An activity as `kept` weighs it.
    nonisolated struct Candidate: Equatable, Sendable {
        var id: String
        var running: Bool
        var staleDate: Date?
    }

    nonisolated static func running(_ state: ActivityState) -> Bool { state == .active || state == .stale }

    /// Which activity stays: a running one, the one whose content was updated last (each update moves its stale date
    /// on); nil when none runs, so every one ends.
    nonisolated static func kept(_ all: [Candidate]) -> String? {
        all.filter(\.running).max { ($0.staleDate ?? .distantPast) < ($1.staleDate ?? .distantPast) }?.id
    }

    /// The app went to the background. With no push route to this phone at `group`'s hub (its `none`, or no route at
    /// all for this credential, #16756), nothing can update or end the activity from now on: it would go stale and keep
    /// showing old counts and an old request for hours. It ends now instead, and the app starts it again when it is back
    /// in front (#12982).
    func left(health: PushHealth, group: String?) {
        guard Self.endsOnLeaving(health, group: group) else { return }
        endAll()
    }

    /// Only a confirmed outage ends the activity. An unknown route (an older hub that reports none, or none read yet)
    /// keeps it: such a hub may well push, and ending a working activity at every switch away would cost more than an
    /// activity that might go stale.
    nonisolated static func endsOnLeaving(_ health: PushHealth, group: String?) -> Bool {
        group.flatMap { health.pushReaches(group: $0) } == false
    }

    func endAll() {
        endTask?.cancel()
        endTask = nil
        for activity in Activity<LegacyFleetActivityAttributes>.activities { Self.end(activity) }
        for activity in Activity<PersonaActivityAttributes>.activities { Self.end(activity) }
    }

    private static func end<Attributes: ActivityAttributes>(_ activity: Activity<Attributes>) where Attributes.ContentState == FleetState {
        nonisolated(unsafe) let value = activity
        Task { await value.end(nil, dismissalPolicy: .immediate) }
    }

    // Activity is not Sendable, but its methods are safe from any thread (ActivityKit does its own locking).
    /// Whether `state` needs an update over what the activity shows: each one redraws the island and the lock screen,
    /// and most fleet events change something this phone does not show (another island slot, the numbers of another
    /// machine). The same content still goes when its stale date is near, which each update moves 15 minutes on.
    nonisolated static func worthUpdating(_ shown: FleetState, staleDate: Date?, to state: FleetState, now: Date) -> Bool {
        shown != state || (staleDate ?? .distantPast) < now.addingTimeInterval(5 * 60)
    }

    private static func update<Attributes: ActivityAttributes>(_ activity: Activity<Attributes>, _ content: ActivityContent<FleetState>)
    where Attributes.ContentState == FleetState {
        nonisolated(unsafe) let a = activity
        Task { await a.update(content) }
    }

    static func relevance(_ s: FleetState) -> Double {
        switch s.phase {
        case .needsYou: 1
        case .working: 0.5
        default: 0.2
        }
    }

    private func request(_ content: ActivityContent<FleetState>) {
        do {
            watch(try Activity.request(attributes: LegacyFleetActivityAttributes(), content: content, pushType: .token))
        } catch {
            // The simulator without APNs, or the user turned Live Activities off for Persona.
            if let local = try? Activity.request(attributes: LegacyFleetActivityAttributes(), content: content, pushType: nil) {
                watch(local)
            }
        }
    }

    private func watch<Attributes: ActivityAttributes>(_ activity: Activity<Attributes>) where Attributes.ContentState == FleetState {
        guard watching.insert(activity.id).inserted else { return }
        let id = activity.id
        Task {
            for await token in activity.pushTokenUpdates {
                #if DEBUG
                // For pushing to the simulator by hand (docs/operations/testing.md, TestManualLiveActivityPush).
                Logger(subsystem: "persona", category: "live-activity").notice("activity push token \(token.hex, privacy: .public)")
                #endif
                report(.init(activityID: id, pushToken: token.hex))
            }
        }
        Task {
            for await state in activity.activityStateUpdates where state == .ended || state == .dismissed {
                report(.init(activityID: id, ended: true))
                watching.remove(id)
            }
        }
    }

    private func report(_ body: APIClient.LiveActivityReport) {
        #if DEBUG
        if UserDefaults.standard.string(forKey: "pairURL") != nil { return } // simulator automation is not this phone
        #endif
        // To the first group's hub only (docs/protocol/multi-hub.md「Live Activities」): two hubs pushing the same activity would each
        // overwrite it with their own agents. In the app the activity shows every group (`AppModel.mergedFleet`); in
        // the background it follows that hub's pushes, and its unqualified ids are that group's (FleetActions).
        guard let api = AppModel.shared.links.first?.api else { return }
        Task { try? await api.reportLiveActivity(device: PushRegistrar.deviceID, body) }
    }
}

#if DEBUG
extension LiveActivityController {
    /// `-showFleetSample needsYou|working|background|done|thinking|assistant|offline|cleared` (`-fleetDetails YES` for names on the lock screen,
    /// `-fleetIsland b`, `c` or `custom:assistant,load` for the island's sides, `-showFleetCount 12` for that many in
    /// each kind there is; `-openPersona nwverse` for the persona): starts the activity with made-up agents, no
    /// hub needed, for screenshots. `thinking` is only the assistant answering (beside one finished agent);
    /// `assistant` is the assistant alone, nothing to count (the minimal island then shows the persona); `background`
    /// leads with two agents whose workflows run on in the background (#3437: their lines, bars and workflows' clocks);
    /// `offline` is the working sample made stale once it shows; `cleared` is a lone request answered elsewhere two
    /// seconds after it shows (the fleet then empty, the minute before the end, #8636).
    func showSample(_ phase: String, details: Bool, island spec: String?, count: Int = 0) {
        UserDefaults.standard.set(details, forKey: Self.detailsKey)
        var island = IslandPrefs()
        if let spec {
            let parts = spec.split(separator: ":", maxSplits: 1)
            island.preset = IslandPrefs.Preset(rawValue: String(parts[0])) ?? .a
            if parts.count > 1 {
                let sides = parts[1].split(separator: ",").map { IslandSlot(rawValue: String($0)) }
                island.left = sides.first ?? nil
                island.right = sides.count > 1 ? sides[1] : nil
            }
        }
        UserDefaults.standard.set(try? JSONEncoder().encode(island), forKey: Self.islandKey)
        let now = Date.now.timeIntervalSince1970
        let ask = FleetState.Item(id: "macbook/s1", title: "补齐网络层的单元测试", place: "MacBook · weather", kind: .permission, // i18n-ignore
                                  detail: "Bash: swift test --parallel", interaction: "ix_sample", since: now - 90)
        let plan = FleetState.Item(id: "studio/s2", title: "照片同步支持 HEIC", place: "Studio · photo-sync", kind: .plan, // i18n-ignore
                                   detail: "支持 HEIC", interaction: "ix_plan", since: now - 300) // i18n-ignore
        let work = FleetState.Item(id: "studio/s3", title: "重构设置页", place: "Studio · notes", kind: .working, since: now - 754) // i18n-ignore
        let done = FleetState.Item(id: "macbook/s4", title: "升级依赖", place: "MacBook · api", kind: .unread, detail: "全部测试通过，已提交", since: now - 60) // i18n-ignore
        let audit = FleetState.Item(id: "studio/s5", title: "各端功能对齐审计", place: "Studio · persona", kind: .working, // i18n-ignore
                                    detail: "parity-audit · Verify · 169/185", since: now - 14 * 60, background: true,
                                    progress: .init(done: 169, started: 185))
        let sweep = FleetState.Item(id: "macbook/s6", title: "清理过期文档", place: "MacBook · docs", kind: .working, // i18n-ignore
                                    detail: "docs-sweep · Fix · 3/8", since: now - 13 * 60, background: true, progress: .init(done: 3, started: 8))
        var state: FleetState = switch phase {
        case "working": FleetState(phase: .working, working: 3, unread: 1, items: [work, done], workingSince: now - 754)
        // Two background rows first, the most the island and the lock screen draw: both their tallest.
        case "background": FleetState(phase: .working, working: 3, unread: 1, items: [audit, sweep, work], workingSince: now - 14 * 60)
        case "done", "thinking": FleetState(phase: .done, unread: 1, items: [done])
        case "assistant": FleetState(phase: .none)
        default: FleetState(phase: .needsYou, needsYou: 2, working: 3, unread: 1, items: [ask, plan, work], workingSince: now - 754)
        }
        if count > 0 {
            state.needsYou = state.needsYou > 0 ? count : 0
            state.working = state.working > 0 ? count : 0
            state.unread = state.unread > 0 ? count : 0
        }
        state.assistant = phase != "done"
        if phase == "cleared" { state = FleetState(phase: .needsYou, needsYou: 1, items: [ask]) }
        if phase == "offline" { state = FleetState(phase: .working, working: 3, unread: 1, items: [work, done], workingSince: now - 754) }
        // The Studio's threads wait for a core (1.4 per core, past 100: #12519), so the busiest shows how that reads.
        state.stats = .init(tpm: 12000, tokens: 42_000_000, requests: 1200,
                            loads: [.init(id: "studio", name: "Studio", percent: 140), .init(id: "macbook", name: "MacBook", percent: 70)])
        Task {
            try? await Task.sleep(for: .seconds(1)) // the scene must be active to start one
            // `apply` starts no activity for finished agents alone (the hub does not either) and keeps it fresh: these
            // start one directly, and `offline` is then marked stale.
            if ["done", "thinking", "assistant", "offline"].contains(phase) {
                endAll()
                try? await Task.sleep(for: .seconds(1))
                let content = ActivityContent(state: state.tailored(island: island, details: details, look: AppModel.shared.fleetLook),
                                              staleDate: .now.addingTimeInterval(15 * 60))
                request(content)
                if phase == "offline", let activity {
                    // A request already stale is refused, and the simulator does not redraw when a stale date passes:
                    // an update dated in the past draws it stale at once.
                    try? await Task.sleep(for: .seconds(1))
                    Self.update(activity, ActivityContent(state: content.state, staleDate: .now.addingTimeInterval(-1)))
                }
                return
            }
            apply(state)
            if phase == "cleared" {
                try? await Task.sleep(for: .seconds(2))
                var empty = FleetState()
                empty.stats = state.stats
                apply(empty)
            }
        }
    }
}
#endif
#endif // os(iOS)

extension AppModel {
    /// What the Live Activity draws for the persona on screen (docs/apple/live-activity.md「Avatar」).
    var fleetLook: FleetLook { FleetLook(persona: persona, builtinAccent: builtinAccent) }
    var fleetFaces: FleetFaces? { FleetFaces(persona: persona, personas: personas) }
}

nonisolated extension FleetLook {
    /// A built-in in the accent it has in the app; a package as its pictures (`FleetFaces`), and while they cannot be
    /// read Mochi, which keeps the app icon's colors. From the persona itself, not `Theme`: the spec is recomposed after
    /// the persona changes.
    init(persona p: Persona, builtinAccent: Color) {
        guard !p.isBuiltin else {
            self.init(design: p.design.flatMap(PersonaFace.Design.init(rawValue:)) ?? .mochi, tint: builtinAccent)
            return
        }
        let color = p.palette?.primary
        self.init(design: PersonaFace.Design.mochi.rawValue, light: color?.light ?? Self.standard.light,
                  dark: color?.dark ?? Self.standard.dark, package: "\(p.id)/\(p.rev)")
    }
}

extension FleetFaces {
    /// A package's faces for the activity: one picture a state, its first frame (what the top bar's dot holds still),
    /// and the avatar only for states without one, each made at every face size from the package's own pixels. Nil for
    /// a built-in, or a package not on the phone.
    @MainActor init?(persona p: Persona, personas: PersonaStore) {
        guard let m = p.manifest, personas.isOnPhone(p) else { return nil }
        var states: [String: Picture] = [:]
        for s in PersonaState.allCases where m.dot.states[s.rawValue] != nil {
            if let first = personas.frames(p, s)?.images.first, let picture = Self.picture(first) { states[s.rawValue] = picture }
        }
        // The avatar file at least as large as the largest face, scaled down from it.
        let avatar = states.count == PersonaState.allCases.count ? nil
            : personas.avatar(p, points: CGFloat(Self.sizes[Self.sizes.count - 1]), scale: Self.scale).flatMap(Self.picture)
        guard !states.isEmpty || avatar != nil else { return nil }
        self.init(package: "\(p.id)/\(p.rev)", states: states, avatar: avatar)
    }
}

extension Data {
    nonisolated var hex: String { map { String(format: "%02x", $0) }.joined() }
}
