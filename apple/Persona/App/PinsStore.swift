import SwiftUI
import PersonaWire

/// Pinned and muted spaces / agents. The hub keeps them; this keeps a copy in UserDefaults so the list orders right
/// before the hub answers, and changes them optimistically: a change the hub does not save goes back to what the hub
/// last accepted, with a message even when the request only timed out (#1866). Keys are "machine/space name" and
/// "machine/session id" (`AppModel.pinKey`). Split out of AppModel (ARCH-PLAN step 9); AppModel forwards to it. One per group of hubs
/// (docs/protocol/multi-hub.md): the default group's copy is where it always was ("dev.pins"), another group's has its id after.
@Observable final class PinsStore {
    struct Deps {
        var api: () -> APIClient?
        var report: (Error) -> Void
        var defaults: UserDefaults = .standard
        /// The group ("" = the default group).
        var suffix: String = ""
    }

    var pins: Pins { didSet { save(pins, Self.name("dev.pins", deps.suffix)) } }
    /// Muted: the hub sends no notifications about them and the list shows them quietly.
    var mutes: Pins { didSet { save(mutes, Self.name("dev.mutes", deps.suffix)) } }
    @ObservationIgnored private let deps: Deps
    @ObservationIgnored private var pinSaves: Saves
    @ObservationIgnored private var muteSaves: Saves

    init(deps: Deps) {
        self.deps = deps
        let pins = Self.load(Self.name("dev.pins", deps.suffix), deps.defaults)
        let mutes = Self.load(Self.name("dev.mutes", deps.suffix), deps.defaults)
        self.pins = pins
        self.mutes = mutes
        pinSaves = Saves(accepted: pins)
        muteSaves = Saves(accepted: mutes)
    }

    /// One list's saves to the hub (as the web's `save` in store.ts): every PUT sends the whole list, so the hub ends up
    /// with the newest one it accepted. When the newest change fails, the list goes back to that, but only once no other
    /// save is still on its way: a newer change is never undone by an older failure, and an older change that failed
    /// while a newer one got through is in the newer one.
    struct Saves {
        /// What the hub last accepted (or answered), as far as this device knows.
        var accepted: Pins
        /// The change `accepted` is from; `rev` when the list shows what the hub has.
        var acceptedRev = 0
        /// The newest change.
        var rev = 0
        /// Saves still waiting for the hub.
        var out = 0
        /// Why the newest failed save failed, for the message.
        var failure: Error?
    }

    /// A pin or mute the hub did not save. Always shown, unlike the timeout or offline error inside it: the list just
    /// changed back, and the user has to know why.
    struct NotSaved: LocalizedError {
        enum What { case pins, mutes }
        let what: What
        let cause: Error

        var errorDescription: String? {
            switch what {
            case .pins: String(localized: "Couldn't save the pin change, so it was undone.")
            case .mutes: String(localized: "Couldn't save the notification change, so it was undone.")
            }
        }
    }

    static func name(_ base: String, _ suffix: String) -> String { suffix.isEmpty ? base : base + "." + suffix }

    /// A group that is gone: its copies go too.
    static func forget(suffix: String, defaults: UserDefaults = .standard) {
        defaults.removeObject(forKey: name("dev.pins", suffix))
        defaults.removeObject(forKey: name("dev.mutes", suffix))
    }


    private static func load(_ key: String, _ defaults: UserDefaults) -> Pins {
        guard let data = defaults.data(forKey: key), let p = try? JSON.decoder.decode(Pins.self, from: data) else { return Pins() }
        return p
    }

    private func save(_ p: Pins, _ key: String) {
        if let data = try? JSON.encoder.encode(p) { deps.defaults.set(data, forKey: key) }
    }

    func isPinned(space: String) -> Bool { pins.spaces.contains(space) }
    func isPinned(session: String) -> Bool { pins.sessions.contains(session) }
    func isMuted(space: String) -> Bool { mutes.spaces.contains(space) }
    /// The agent itself, or its space unless the agent was turned back on there (the hub's rule).
    func isMuted(session: String, space: String?) -> Bool { mutes.mutes(session: session, space: space) }

    func togglePin(space: String) { updatePins { Self.toggle(&$0.spaces, space) } }
    func togglePin(session: String) { updatePins { Self.toggle(&$0.sessions, session) } }
    func toggleMute(space: String) { updateMutes { Self.toggle(&$0.spaces, space) } }
    /// One agent's own switch, on top of its space's: muting adds it to the muted agents; turning it on inside a
    /// muted space adds it to the allowed ones.
    func toggleMute(session: String, space: String?) { updateMutes { $0 = $0.togglingMute(session: session, space: space) } }

    /// A space was renamed: its pin and mute follow the new name.
    func renameSpace(_ old: String, to new: String) {
        pins.spaces = pins.spaces.map { $0 == old ? new : $0 }
        mutes.spaces = mutes.spaces.map { $0 == old ? new : $0 }
        pinSaves.accepted.spaces = pinSaves.accepted.spaces.map { $0 == old ? new : $0 }
        muteSaves.accepted.spaces = muteSaves.accepted.spaces.map { $0 == old ? new : $0 }
    }

    /// The hub's lists. One that was changed here while it was being read, or still has a save on its way, keeps what
    /// it shows: the answer may be from before that change.
    func load() async {
        let pinsAt = pinSaves.out == 0 ? pinSaves.rev : -1
        if let p = try? await deps.api()?.pins(), pinsAt == pinSaves.rev, pinSaves.out == 0 {
            pins = p
            pinSaves.accepted = p
            pinSaves.acceptedRev = pinSaves.rev
        }
        let mutesAt = muteSaves.out == 0 ? muteSaves.rev : -1
        if let m = try? await deps.api()?.mutes(), mutesAt == muteSaves.rev, muteSaves.out == 0 {
            mutes = m
            muteSaves.accepted = m
            muteSaves.acceptedRev = muteSaves.rev
        }
    }

    private static func toggle(_ list: inout [String], _ key: String) {
        if let i = list.firstIndex(of: key) { list.remove(at: i) } else { list.insert(key, at: 0) }
    }

    private func updatePins(_ change: (inout Pins) -> Void) {
        Haptics.tap()
        var p = pins
        change(&p)
        withAnimation(Theme.Motion.standard) { pins = p } // optimistic; the hub keeps the truth
        save(p, \.pins, \.pinSaves, .pins) { try await $0.setPins($1) }
    }

    private func updateMutes(_ change: (inout Pins) -> Void) {
        Haptics.tap()
        var p = mutes
        change(&p)
        withAnimation(Theme.Motion.standard) { mutes = p }
        save(p, \.mutes, \.muteSaves, .mutes) { try await $0.setMutes($1) }
    }

    /// Sends the list as it now is. Once no save of it is on its way and the newest change was not taken, the list goes
    /// back to what the hub has, and the user is told.
    private func save(_ p: Pins, _ list: ReferenceWritableKeyPath<PinsStore, Pins>,
                      _ saves: ReferenceWritableKeyPath<PinsStore, Saves>, _ what: NotSaved.What,
                      _ send: @escaping (APIClient, Pins) async throws -> Void) {
        self[keyPath: saves].rev += 1
        self[keyPath: saves].out += 1
        let rev = self[keyPath: saves].rev, api = deps.api
        Task {
            var error: Error?
            do {
                if let api = api() { try await send(api, p) }
            } catch let e { error = e }
            var s = self[keyPath: saves]
            s.out -= 1
            if let error { s.failure = error } else if rev > s.acceptedRev { s.accepted = p; s.acceptedRev = rev }
            let failed = s.out == 0 && s.acceptedRev != s.rev ? s.failure : nil
            if failed != nil { s.acceptedRev = s.rev; s.failure = nil }
            self[keyPath: saves] = s
            guard let failed else { return }
            withAnimation(Theme.Motion.standard) { self[keyPath: list] = s.accepted }
            deps.report(NotSaved(what: what, cause: failed))
        }
    }
}
