import Foundation
import Observation

/// Where the terminal tab's screen comes from (docs/integrations/terminal.md): the hub's live stream of the pane — herdr's frames
/// applied to a grid of the pane's size, continued from the last frame after a reconnect — or, with a hub, node or
/// herdr that cannot stream, the colored screen read once a second. Pages of scrollback load above it either way.
///
/// Driving it (`wantsControl`, the default): the phone takes the terminal at its own grid, the programs in it lay
/// themselves out for the phone, and keys go straight in. Scrolling up shows what the terminal printed since: herdr
/// keeps no scrollback for Claude Code at that size (it clears its own when the size changes, as in any terminal), so
/// the rows that scroll off the top are kept here. What was there before at the computer's width is not carried over.
@MainActor @Observable
final class TerminalFeed {
    enum Mode { case stream, poll }

    let key: SessionKey
    private(set) var mode = Mode.poll
    /// Scrollback pages above the screen, oldest first.
    private(set) var older: [TermRow] = []
    private(set) var screen: [TermRow] = []
    private(set) var cols = 0
    private(set) var cursor: (row: Int, col: Int)?
    private(set) var hasOlder = true
    private(set) var atLimit = false
    private(set) var loading = false
    private(set) var loaded = false
    private(set) var offline = false
    /// Reading history: the screen stays as it was until Jump to Latest.
    private(set) var paused = false
    /// A failed page load (shown briefly).
    var error: String?
    /// Driving the terminal at the phone's size: asked for (starting), granted (active), another program holds it and
    /// the user is to say whether to take it (busy), or not (declined: watching, `controlNote` says why).
    enum Control: Equatable { case off, starting, active, busy, declined }
    private(set) var control = Control.off
    private(set) var controlNote: String?
    /// This view wants to drive the terminal (Settings › Terminal Size).
    var wantsControl = false
    /// The grid that fits the view at its font size: the size the phone asks for.
    private(set) var phoneGrid: TerminalGridSize?
    /// While driving: the rows that scrolled off the top of the screen, oldest first.
    private(set) var scrollback: [TermRow] = []
    var drives: Bool { control == .active }
    /// The link's health for the status bar: when the screen was last known current, why not now, the next try.
    private(set) var lastOK: Date?
    private(set) var failure: String?
    private(set) var nextTry = Date.distantPast
    private var failures = 0
    /// When the subscription still waiting for its answer went out: its first frame, and when it takes the terminal over,
    /// what the hub says of that (nil: answered, or nothing asked). Past `answerLimit` the feed gives up on it (#6327).
    private(set) var waitingSince: Date?

    /// The time (unit tests move it on rather than wait out a limit).
    @ObservationIgnored var now: () -> Date = { Date() }

    @ObservationIgnored private let grid = TerminalGrid(cols: 1, rows: 1)
    @ObservationIgnored private var epoch: String?
    @ObservationIgnored private var seq = 0
    @ObservationIgnored private var subscribed = false
    /// Stream unavailable for this session (old node / herdr): stay on polling.
    @ObservationIgnored private var unsupported = false
    @ObservationIgnored private var takeover = false
    /// The screen the last subscription builds on is there: its first frame came, or it continues the frames the feed has.
    @ObservationIgnored private var framed = false
    /// The computer was offline at the last tick: once it is back, subscribe again at once.
    @ObservationIgnored private var computerWasOffline = false

    static let page = 200
    static let scrollbackLimit = 3000
    static let linkDown = String(localized: "Can't reach the hub")
    /// How long a subscription may go unanswered: the takeover went with a node connection that dropped (a deploy
    /// restarts the node), or the computer doesn't answer. Then the terminal says why and tries again, never spins on.
    static let answerLimit: TimeInterval = 10

    init(key: SessionKey) { self.key = key }

    /// History, then the screen down to its last row with text or the cursor (a shell after `clear` has its prompt
    /// at the top and forty empty rows under it: following the end would show only those).
    var content: TerminalContent {
        var end = screen.count
        while end > 0, end - 1 > (cursor?.row ?? -1), screen[end - 1].allSatisfy(\.isBlank) { end -= 1 }
        var c = TerminalContent(rows: older + scrollback + Array(screen[..<end]), cols: cols)
        if let cursor { c.cursor = (older.count + scrollback.count + cursor.row, cursor.col) }
        c.screenStart = older.count + scrollback.count
        return c
    }

    // MARK: Driving

    /// Called four times a second while the tab is on screen: keeps the stream subscribed, polls when it cannot
    /// stream, retries after failures and keeps the health current.
    func tick(_ model: AppModel) async {
        if mode == .poll, !unsupported, model.canStreamTerminal(key.group), model.isLive(key.group) {
            if wantsControl, model.canControlTerminal(key.group), control == .off {
                guard phoneGrid != nil else { return } // the view's first layout tells the size
            }
            mode = .stream // the hub's hello came after the first reads: what they concluded about history is moot
            hasOlder = !willDrive(model)
            atLimit = false
            subscribe(model, continuing: false)
            return
        }
        switch mode {
        case .stream:
            guard model.isLive(key.group) else {
                subscribed = false // the connection's hello subscribes again
                if failure == nil { failure = Self.linkDown }
                // The hub no longer knows who drives it: taking it over again, no keys until it says so (as the web).
                if control == .active { control = .starting }
                return
            }
            if failure == Self.linkDown { failure = nil } // back: an idle pane sends nothing, that is not a failure
            let offline = model.machine(key.machineRef)?.online == false
            if offline != computerWasOffline {
                computerWasOffline = offline
                if !offline { // its node is back: subscribe (and take the terminal over) again now, not at the backoff's turn
                    failures = 0
                    nextTry = .distantPast
                }
            }
            if let waitingSince, now().timeIntervalSince(waitingSince) > Self.answerLimit {
                unanswered(model)
            } else if !subscribed, now() >= nextTry {
                subscribe(model, continuing: true)
            } else if subscribed, loaded, failure == nil {
                lastOK = now()
            }
        case .poll:
            if !paused, now() >= nextTry { await refresh(model) }
        }
    }

    func stop(_ model: AppModel) {
        if mode == .stream { model.terminalUnsubscribe(key) }
        subscribed = false
        waitingSince = nil // back on screen it asks afresh, with a wait of its own
    }

    /// Retry: subscribe again now. One still unanswered goes first, at the hub too, so this one has it ask the node
    /// again (the same subscription again would only be told what it has).
    func retryNow(_ model: AppModel) {
        nextTry = .distantPast
        guard mode == .stream, model.isLive(key.group) else { return }
        if waitingSince != nil, subscribed { drop(model) }
        subscribe(model, continuing: true)
    }

    /// Nothing answered the subscription within `answerLimit`: it goes, at the hub too, and the terminal says why; the
    /// next one goes at the backoff's turn, or at once when the computer comes back.
    private func unanswered(_ model: AppModel) {
        drop(model) // still taking it over (the size chip says so): the next try asks again
        loaded = true // the reason, not a spinner
        failed(Self.notAnswering(model.machine(key.machineRef)))
    }

    /// Unsubscribes at the hub and starts the next subscription from a whole frame.
    private func drop(_ model: AppModel) {
        model.terminalUnsubscribe(key)
        subscribed = false
        waitingSince = nil
        epoch = nil
    }

    /// Why the terminal hasn't come: its computer is offline, or isn't answering.
    static func notAnswering(_ machine: Machine?) -> String {
        guard let machine else { return String(localized: "This computer hasn't answered yet.") }
        return machine.online ? String(localized: "\(machine.name) isn't answering") : String(localized: "\(machine.name) is offline")
    }

    private func willDrive(_ model: AppModel) -> Bool {
        wantsControl && model.canControlTerminal(key.group) && phoneGrid != nil && control != .declined && control != .busy
    }

    private func subscribe(_ model: AppModel, continuing: Bool) {
        let drive = willDrive(model)
        if drive, control == .off { control = .starting }
        // Answered for sure: a stream from its whole frame, and taking it over (terminal_control). One continuing from
        // the frame it has may hear nothing of an idle pane, which is no failure.
        let fresh = !continuing || epoch == nil
        if waitingSince == nil, fresh || control == .starting { waitingSince = now() }
        framed = !fresh
        let sent = model.terminalSubscribe(key, epoch: continuing ? epoch : nil, after: seq, grid: drive ? phoneGrid : nil, takeover: takeover) {
            [weak self, weak model] event in
            guard let self, let model else { return }
            self.receive(event, model)
        }
        subscribed = sent
        if !sent { // nothing went out (no socket yet, the session's row not in yet): again in a moment, still bounded
            if waitingSince == nil { waitingSince = now() }
            nextTry = now().addingTimeInterval(1)
            return
        }
        takeover = false
    }

    /// The subscription's answer is in: a frame, and the hub's word on taking the terminal over when it asked to.
    private func answeredIfSo() {
        if framed, control != .starting { waitingSince = nil }
    }

    // MARK: Driving the terminal

    /// The view's grid changed (laid out, the keyboard, the font): a terminal the phone drives takes the new size.
    func setGrid(_ g: TerminalGridSize, _ model: AppModel?) {
        guard g != phoneGrid, g.cols >= 10, g.rows >= 4 else { return }
        phoneGrid = g
        if subscribed, control == .active || control == .starting { model?.terminalInput(key, "", grid: g) }
    }

    /// What the hub said about driving it (terminal_control).
    func controlChanged(_ state: String, why: String?) {
        defer { answeredIfSo() }
        switch state {
        case "active":
            control = .active
            controlNote = nil
        case "busy":
            control = .busy
        case "unsupported":
            control = .declined
            controlNote = why ?? String(localized: "This computer is view-only and can't be taken over")
            leaveControl()
        default: // taken, lost: watching from now on
            control = .declined
            controlNote = why ?? String(localized: "The terminal is back to the computer's size")
            leaveControl()
        }
    }

    /// Bytes typed on the phone, straight to the terminal it drives.
    func type(_ text: String, _ model: AppModel) {
        guard drives, !text.isEmpty else { return }
        model.terminalInput(key, text)
    }

    /// The user said take it from the program that holds it (or take it back).
    func takeOver(_ model: AppModel) {
        takeover = true
        control = .starting
        controlNote = nil
        guard model.isLive else { return }
        subscribe(model, continuing: true)
    }

    /// The user said only watch: the pane stays as the computer has it (and its history pages as usual).
    func watchOnly(_ why: String) {
        control = .declined
        controlNote = why
        leaveControl()
    }

    /// Driving it or not (Settings › Terminal Size changed): subscribe again the other way.
    func setWantsControl(_ on: Bool, _ model: AppModel) {
        guard on != wantsControl else { return }
        wantsControl = on
        if !on {
            control = .off
            controlNote = nil
            leaveControl()
        } else if control == .declined {
            control = .off
        }
        if subscribed, model.isLive { subscribe(model, continuing: false) }
    }

    /// Back to watching the pane at its size: what was kept at the phone's goes.
    private func leaveControl() {
        scrollback = []
        older = []
        hasOlder = true
        atLimit = false
    }

    private func receive(_ event: TerminalEvent, _ model: AppModel) {
        switch event {
        case .frame(let epoch, let seq, let full, let cols, let rows, let data):
            switch apply(epoch: epoch, seq: seq, full: full, cols: cols, rows: rows, data: data) {
            case .applied:
                healthy()
                offline = false
                loaded = true
                framed = true
                answeredIfSo()
            case .duplicate:
                break
            case .gap:
                subscribe(model, continuing: false)
            }
        case .ended(let why, let unsupported):
            subscribed = false
            epoch = nil
            waitingSince = nil
            if unsupported {
                self.unsupported = true
                mode = .poll
                nextTry = .distantPast
                if control == .active || control == .starting { control = .off } // a screen read can't be driven: it only watches
                return
            }
            offline = why.contains("not running")
            let machine = model.machine(key.machineRef)
            // An offline computer in the app's words, whatever the hub's error says (it may name the machine by id).
            failed(offline ? String(localized: "The session isn't running in herdr") : machine?.online == false ? Self.notAnswering(machine) : why)
            loaded = true
            // Nothing runs it: nothing to take over. Otherwise it still takes the terminal over, and the size chip says so
            // until the next try is answered (with "Wrap" between tries, it read as a mirror that had given up, #6327).
            if offline, control == .active || control == .starting { control = .off } else if control == .active { control = .starting }
        case .reconnected:
            if mode == .stream { subscribe(model, continuing: true) }
        case .control(let state, _, _, let why):
            controlChanged(state, why: why)
        }
    }

    enum FrameResult { case applied, duplicate, gap }

    /// Applies a frame in order: a whole one always, a diff only right after the frame before it. A diff that does
    /// not follow (one went missing, or a new stream started without its whole frame) is a gap: start again from a
    /// whole frame.
    func apply(epoch: String, seq: Int, full: Bool, cols: Int, rows: Int, data: Data) -> FrameResult {
        // Driving it, the screen is the phone's: rows that scroll off the top go to the scrollback.
        let before = (control == .active || control == .starting) && cols == phoneGrid?.cols ? grid.lines : nil
        let beforeCols = grid.cols
        if full {
            grid.reset(cols: cols, rows: rows)
        } else if epoch == self.epoch, seq <= self.seq {
            return .duplicate // a replay overlapping what arrived live
        } else if epoch != self.epoch || seq != self.seq + 1 {
            self.epoch = nil
            return .gap
        }
        grid.feed(data)
        if let before, beforeCols == grid.cols {
            let k = TerminalGrid.scrolled(from: before, to: grid.lines)
            if k > 0 {
                scrollback += before.prefix(k)
                if scrollback.count > Self.scrollbackLimit { scrollback.removeFirst(scrollback.count - Self.scrollbackLimit) }
            }
        }
        self.epoch = epoch
        self.seq = seq
        if !paused { publish() }
        return .applied
    }

    private func publish() {
        screen = grid.lines
        cols = grid.cols
        cursor = grid.cursorVisible ? (grid.cursorRow, grid.cursorCol) : nil
    }

    private func healthy() {
        lastOK = now()
        failure = nil
        failures = 0
    }

    private func failed(_ why: String) {
        failure = why
        failures += 1
        nextTry = now().addingTimeInterval(Self.backoff(failures))
    }

    /// 1, 2, 4, 8 and then every 15 seconds.
    static func backoff(_ failures: Int) -> TimeInterval { min(15, pow(2, Double(max(0, failures - 1)))) }

    // MARK: Polling (no stream)

    private func refresh(_ model: AppModel) async {
        guard let api = model.api(key.group) else { return }
        do {
            let r = try await api.screen(key, lines: Self.page, recent: true, ansi: true)
            guard mode == .poll else { return }
            offline = r.status == .offline
            screen = TerminalGrid.rows(fromANSI: r.text)
            cols = 0
            cursor = nil
            if screen.count < Self.page - 1 { hasOlder = false }
            healthy()
            nextTry = now().addingTimeInterval(1)
        } catch {
            guard !error.isCancellation else { return }
            failed(error.shown ?? String(localized: "Couldn't read the terminal"))
        }
        loaded = true
    }

    /// Ask for the screen again right away (after sending keys, so their echo shows).
    func nudge() {
        if mode == .poll { nextTry = .distantPast }
    }

    // MARK: History

    /// The page of scrollback before the first rows on screen; the screen stops following the pane meanwhile.
    func loadOlder(_ model: AppModel) async {
        guard !loading, hasOlder, loaded, control == .off || control == .declined, let api = model.api(key.group) else { return }
        loading = true
        error = nil
        let wasPaused = paused
        paused = true // the screen must stay put while a page is placed above it
        defer {
            loading = false
            if older.isEmpty, !wasPaused { resume() } // nothing above (a full-screen TUI): keep following
        }
        var all = (older + screen).map(TerminalGrid.plain)
        while all.last?.isEmpty == true { all.removeLast() } // herdr's reads end at the last row with text
        let anchor = Array(all.prefix(20))
        do {
            let r = try await api.screen(key, before: anchor, loaded: all.count, lines: Self.page, ansi: true)
            if !r.text.isEmpty { older = TerminalGrid.rows(fromANSI: r.text) + older }
            hasOlder = r.hasMore
            atLimit = r.atLimit
        } catch {
            if !error.isCancellation { self.error = error.shown }
        }
    }

    /// Back to the live screen: history goes, the screen follows the pane again.
    func resume() {
        older = []
        hasOlder = true
        atLimit = false
        paused = false
        if mode == .stream { publish() } else { nextTry = .distantPast }
    }
}
