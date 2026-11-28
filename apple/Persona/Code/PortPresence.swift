import Foundation
import SwiftUI

nonisolated struct PortProbe: Decodable, Sendable {
    var port: Int
    var listening: Bool
    var http: Bool?
    var title: String?
}

extension APIClient {
    /// Whether anything listens on a machine's port, and its page's title (nothing is opened).
    func probePort(machine: String, port: Int) async throws -> PortProbe {
        try await get("/api/v1/machines/\(machine)/ports/\(port)")
    }

    /// The user opens a port no agent is known to listen on: allowed from now on if something answers (404 if not).
    func openPort(machine: String, port: Int) async throws -> PortProbe {
        try await post("/api/v1/machines/\(machine)/ports/\(port)/open")
    }
}

/// What the app knows of the ports links point at (docs/operations/ports.md「Recognizing links in replies」): an agent's listed ports
/// (`Session.ports`) are live; a link to any other port is asked about (GET …/ports/{port}) as its badge appears and
/// remembered a little while, like `FilePresence`. A badge whose port is known gone is gray.
@MainActor @Observable
final class PortPresence {
    enum State: Equatable, Sendable {
        case live(title: String?)
        case gone
    }

    enum ReprobeOutcome: Equatable, Sendable {
        case live
        case gone
        case failed
    }

    private struct Key: Hashable {
        var machine: MachineRef
        var port: Int
    }

    private var known: [Key: (state: State, at: Date)] = [:]
    @ObservationIgnored private var asking: Set<Key> = []
    /// When asking about a port last failed (no hub, a timeout, an old hub without the endpoint): a badge scrolled
    /// back into view within `failureHold` waits for nothing instead of asking again.
    @ObservationIgnored private var failed: [Key: Date] = [:]
    /// How many times each group's socket connected: a question that fails after a reconnect it started before is
    /// not remembered (the hub is back).
    @ObservationIgnored private var connects: [String: Int] = [:]

    /// How long a failed question holds: long enough that scrolling a chat back and forth asks once, short enough
    /// that a hub back without a reconnect (a network that only hiccuped) is asked again within the minute.
    static let failureHold: TimeInterval = 20

    /// How long an explicit `.gone` re-probe overrides a stale `Session.ports` scan cache (node scans every 15 s).
    static let staleGoneOverride: TimeInterval = 20

    /// A port's state: listed by an agent on the machine (unless recently confirmed gone by a failed open), else what
    /// the hub said last; nil while unknown.
    func state(_ port: Int, machine: MachineRef, listed: [ListeningPort]) -> State? {
        let key = Key(machine: machine, port: port)
        if let k = known[key], k.state == .gone, Date.now.timeIntervalSince(k.at) < Self.staleGoneOverride {
            return .gone
        }
        if let p = listed.first(where: { $0.port == port }) { return .live(title: p.title) }
        return known[key]?.state
    }

    /// Whether opening `port` should call `POST …/ports/{port}/open` first: unlisted ports always do, and a listed
    /// port that recently failed opening is re-probed rather than trusting the 15 s scan cache.
    func shouldOpenPort(_ port: Int, machine: MachineRef, listed: [ListeningPort]) -> Bool {
        if !listed.contains(where: { $0.port == port }) { return true }
        let key = Key(machine: machine, port: port)
        return known[key]?.state == .gone
    }

    /// Re-probes a port via `POST …/ports/{port}/open` after an initial forwarded load failed (for example when
    /// `agentPorts` held a stale entry from the node's 15 s scan cache).
    func reprobe(port: Int, machine: MachineRef, api: APIClient?) async -> ReprobeOutcome {
        guard let api, !api.config.isDemo else { return .failed }
        do {
            let res = try await api.openPort(machine: machine.id, port: port)
            if res.listening {
                noteLive(port, machine: machine, title: res.title)
                return .live
            }
            noteGone(port, machine: machine)
            return .gone
        } catch let e as APIError where e.status == 404 {
            noteGone(port, machine: machine)
            return .gone
        } catch {
            return .failed
        }
    }

    /// Asks about ports not listed and not known (or known over a minute ago, half that when gone), skipping those
    /// whose question failed within `failureHold`.
    func ask(_ ports: [Int], machine: MachineRef, listed: [ListeningPort], api: APIClient?) {
        guard let api, !api.config.isDemo else { return }
        for port in ports where !listed.contains(where: { $0.port == port }) {
            let key = Key(machine: machine, port: port)
            if let k = known[key], Date.now.timeIntervalSince(k.at) < (k.state == .gone ? 30 : 60) { continue }
            if let at = failed[key], Date.now.timeIntervalSince(at) < Self.failureHold { continue }
            guard !asking.contains(key) else { continue }
            asking.insert(key)
            let connect = connects[machine.group, default: 0]
            Task { [weak self] in
                let res = try? await api.probePort(machine: machine.id, port: port)
                guard let self else { return }
                self.asking.remove(key)
                if let res {
                    self.known[key] = (res.listening ? .live(title: res.title) : .gone, .now)
                    self.failed[key] = nil
                } else if self.connects[machine.group, default: 0] == connect {
                    self.failed[key] = .now
                }
            }
        }
    }

    func noteGone(_ port: Int, machine: MachineRef) {
        let key = Key(machine: machine, port: port)
        known[key] = (.gone, .now)
        failed[key] = nil
    }

    func noteLive(_ port: Int, machine: MachineRef, title: String?) {
        let key = Key(machine: machine, port: port)
        known[key] = (.live(title: title), .now)
        failed[key] = nil
    }

    /// A group's socket connected (again): the hub is back, so ports whose question failed are asked again as their
    /// badges appear.
    func reconnected(group: String) {
        connects[group, default: 0] += 1
        failed = failed.filter { $0.key.machine.group != group }
    }
}

/// Opening a port link: the hub is told (a port no agent listed), the phone listens, and the page's address there.
extension AppModel {
    /// The phone's address for the link, or why it cannot be opened (nothing listens there: the badge turns gray).
    func forwardedURL(_ link: PortLink, machine: MachineRef) async throws -> URL {
        guard let api = api(machine.group) else { throw APIError(status: 0, message: String(localized: "Not connected to a hub [Port Presence c03cfc]")) }
        if api.config.isDemo { throw APIError(status: 403, message: String(localized: "Ports can't be opened in demo mode")) }
        let listed = agentPorts(on: machine)
        if portPresence.shouldOpenPort(link.port, machine: machine, listed: listed) {
            do {
                let res = try await api.openPort(machine: machine.id, port: link.port)
                portPresence.noteLive(link.port, machine: machine, title: res.title)
            } catch let e as APIError where e.status == 404 {
                portPresence.noteGone(link.port, machine: machine)
                throw APIError(status: 404, message: String(localized: "Nothing is listening on port :\(String(link.port)) anymore"))
            }
        }
        // Asked again for each connection through the forward: the hub the group's socket is on now, its route and token.
        return try await PortForwarder.shared.url(for: link, machine: machine) { [weak self] in self?.api(machine.group)?.config }
    }

    /// Re-probes a forwarded port through `openPort` once after an initial load fails, refreshing presence and error state.
    func reprobeForwardedPort(_ key: PortForwarder.Key) async -> PortPresence.ReprobeOutcome {
        let machine = MachineRef(group: key.group, id: key.machine)
        let outcome = await portPresence.reprobe(port: key.port, machine: machine, api: api(key.group))
        switch outcome {
        case .live:
            PortForwarder.shared.forward(key)?.clearError()
        case .gone:
            PortForwarder.shared.recordError(.portGone(key.port), for: key)
        case .failed:
            break
        }
        return outcome
    }

    /// The ports the agents on a machine listen on, one per port.
    func agentPorts(on machine: MachineRef) -> [ListeningPort] {
        var out: [ListeningPort] = []
        for s in sessionsByMachine[machine] ?? [] {
            for p in s.ports ?? [] where !out.contains(where: { $0.port == p.port }) { out.append(p) }
        }
        return out
    }

    /// The names a machine goes by in links (its host name): `namedMachine`, so the chips in a chat are not drawn again by
    /// each heartbeat.
    func hostnames(of machine: MachineRef) -> [String] {
        [namedMachine(machine)?.hostname].compactMap { $0 }
    }
}
