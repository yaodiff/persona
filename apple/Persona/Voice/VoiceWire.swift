import Foundation
import PersonaWire

// The app's side of the voice extension's socket (docs/assistant/voice.md「Protocol changes」: protocol 2; protocol 1 is in
// docs/extensions/extensions.md「Voice」). Pure data and parsing: it builds for every platform the Persona folder is
// compiled for (iOS, Mac, visionOS) and its tests need neither audio nor a hub.
//
// Parsing follows the frame reader of PR #1931 (CarPlay voice conversations, by another contributor): an unknown frame
// comes back as `.other`, a missing or odd field falls back to its default, so a newer extension never breaks an older
// app. The names here are the phone's own; none of #1931's types are reused.

/// The conversation a call is bound to (ChatView's key) as `start.target` sends it.
nonisolated struct VoiceTarget: Equatable, Hashable, Sendable {
    var machineID: String
    var sessionID: String

    init(machineID: String, sessionID: String) {
        self.machineID = machineID
        self.sessionID = sessionID
    }

    init(_ key: SessionKey) {
        self.init(machineID: key.machineID, sessionID: key.sessionID)
    }
}

/// The bound conversation as `ready.target` describes it: fixed for the whole call.
nonisolated struct VoiceTargetInfo: Equatable, Sendable {
    var machineID: String
    var sessionID: String
    /// What the extension took it for (an agent, the main conversation…); the app only shows it.
    var kind: String?
    var title: String?
    /// 「Claude Code」, 「Codex」: who the call hands things to.
    var agentLabel: String?
    var machineName: String?

    func matches(_ key: SessionKey) -> Bool { machineID == key.machineID && sessionID == key.sessionID }
}

/// One line of words: `start.recent` (the conversation's last turns) and `start.history` (the call's captions so far).
nonisolated struct VoiceLine: Equatable, Sendable, Encodable {
    /// "user" or "assistant".
    var role: String
    var text: String

    static let user = "user"
    static let assistant = "assistant"
}

nonisolated extension VoiceLine {
    /// How many of the conversation's turns `recent` carries, and how long each line may be (docs/assistant/voice.md 3.2).
    static let recentTurns = 3
    static let recentLength = 300
    /// At most this many lines of `recent`: an agent's turn can be a long run of short texts between tool calls.
    static let recentLines = 12
    /// How many of the call's captions a reconnect hands back (`history`).
    static let historyLines = 40

    private static let contextBlock = try! NSRegularExpression(
        pattern: "<" + HubNaming.contextPattern + #"\b[^>]*>[\s\S]*?</"# + HubNaming.contextPattern + ">")

    /// The conversation's last three turns as `start.recent`: what the user and the agent wrote, nothing else (tool calls,
    /// thinking and notices stay out; the extension says 「用了 Bash」 itself when it reads them), context blocks taken out,
    /// each line cut to 300 characters. The extension fetches them itself when this is empty.
    static func recent(_ items: [Item]) -> [VoiceLine] {
        var lines: [VoiceLine] = []
        for item in items where item.kind == .text && (item.role == user || item.role == assistant) {
            let text = clean(item.text ?? "")
            guard !text.isEmpty else { continue }
            lines.append(VoiceLine(role: item.role, text: cut(text, recentLength)))
        }
        let starts = lines.indices.filter { lines[$0].role == user }
        if starts.count > recentTurns { lines.removeFirst(starts[starts.count - recentTurns]) }
        return Array(lines.suffix(recentLines))
    }

    /// Text without the app's context blocks, its runs of blank space made one.
    static func clean(_ raw: String) -> String {
        let ns = raw as NSString
        let bare = contextBlock.stringByReplacingMatches(in: raw, range: NSRange(location: 0, length: ns.length), withTemplate: " ")
        return bare.split(whereSeparator: \.isWhitespace).joined(separator: " ")
    }

    static func cut(_ text: String, _ limit: Int) -> String {
        text.count <= limit ? text : String(text.prefix(limit - 1)) + "…"
    }
}

/// What a `run` frame says about one instruction the call handed to the conversation (docs/assistant/voice.md 3.3).
nonisolated enum VoiceRunState: Equatable, Hashable, Sendable {
    case sending, sent, working, needsYou, blocked, held, done, failed, refused, unknown, folded
    case other(String)

    init(wire: String) {
        switch wire {
        case "sending": self = .sending
        case "sent": self = .sent
        case "working": self = .working
        case "needs_you": self = .needsYou
        case "blocked": self = .blocked
        case "held": self = .held
        case "done": self = .done
        case "failed": self = .failed
        case "refused": self = .refused
        case "unknown": self = .unknown
        case "folded": self = .folded
        default: self = .other(wire)
        }
    }

    var wire: String {
        switch self {
        case .sending: "sending"
        case .sent: "sent"
        case .working: "working"
        case .needsYou: "needs_you"
        case .blocked: "blocked"
        case .held: "held"
        case .done: "done"
        case .failed: "failed"
        case .refused: "refused"
        case .unknown: "unknown"
        case .folded: "folded"
        case .other(let raw): raw
        }
    }

    /// Not over yet: a reconnect hands it back (`resume`). `held` answered once and still works in the background; a
    /// state this app does not know is kept too, so a result that comes later is still read out.
    var isOpen: Bool {
        switch self {
        case .done, .failed, .refused, .unknown, .folded: false
        case .sending, .sent, .working, .needsYou, .blocked, .held, .other: true
        }
    }
}

/// `started_at` as the extension wrote it (an RFC 3339 string or a number), echoed back unchanged in `resume`. The
/// number is kept as it came, never converted: a fraction stays, and no value can trap.
nonisolated enum VoiceStamp: Equatable, Hashable, Sendable {
    case text(String)
    case number(Double)

    /// When it started: a number is Unix seconds, or milliseconds when it is that large.
    var date: Date? {
        switch self {
        case .text(let s): JSON.parseDate(s)
        case .number(let n): Date(timeIntervalSince1970: n > 100_000_000_000 ? n / 1000 : n)
        }
    }

    init?(_ value: JSONValue?) {
        switch value {
        case .string(let s)? where !s.isEmpty: self = .text(s)
        case .number(let n)? where n.isFinite: self = .number(n)
        default: return nil
        }
    }
}

/// One instruction handed to the bound conversation, as the latest `run` frame for its id says.
nonisolated struct VoiceRun: Equatable, Identifiable, Sendable {
    var id: String
    var task: String
    var state: VoiceRunState
    var msgIDs: [String]
    /// The transcript's last seq before it was sent; moves on after a held answer (`Hold`).
    var baseSeq: Int
    var startedAt: VoiceStamp?
    /// Why it was refused or failed: dialog_open, input_draft, unprompted…
    var code: String?
    var message: String?
    /// The words left unsent in the agent's input box on the computer (409 input_draft): the whole line, shown to the user.
    var draft: String?
    /// The hub's 409 `clearable` text: present only when the node can clear that line, and not always the same as `draft`
    /// (an IDE selection marker stays out of it). Clear and Send sends it back as `clear_draft` exactly (docs/assistant/voice.md 3.2).
    var clearable: String?
    /// The extension keeps it for a resend (#5904): what the user-speech gate held back offers Send Anyway only then
    /// (one longer than its quoted task, or from before it, is not kept).
    var resendable = false
}

nonisolated enum VoiceAskState: Equatable, Sendable {
    case asking, answered, released
    case other(String)

    init(wire: String) {
        switch wire {
        case "asking": self = .asking
        case "answered": self = .answered
        case "released": self = .released
        default: self = .other(wire)
        }
    }
}

/// Which card the call is asking about by voice (`ask` frame).
nonisolated struct VoiceAsk: Equatable, Sendable {
    var interactionID: String
    var state: VoiceAskState
    /// "voice" or "elsewhere" once it is answered or let go.
    var by: String?
}

/// The upstream model's connection as the extension reports it (`link` frame).
nonisolated enum VoiceLink: Equatable, Sendable {
    case live
    /// Closed while the agent works and nobody speaks; it wakes up by itself.
    case paused
    case waking
    case reconnecting
    case other(String)

    init(wire: String) {
        switch wire {
        case "live": self = .live
        case "paused": self = .paused
        case "waking": self = .waking
        case "reconnecting": self = .reconnecting
        default: self = .other(wire)
        }
    }
}

/// The `code` of an `error` frame (docs/assistant/voice.md 3.3).
nonisolated enum VoiceErrorCode: Equatable, Sendable {
    case noUpstream, upstreamForbidden, upstreamUnavailable, upstreamFailed
    case targetGone, targetReadOnly, targetForbidden
    case machineOffline, modelUnavailable
    /// A meeting call whose target is not that recording's room, or a room that isn't `ready` (#3862 phase 4).
    case notMeetingRoom
    /// An ordinary call bound to a room: a room takes only meeting calls (#3862 phase 4).
    case meetingRoom
    case other(String)

    init(wire: String) {
        switch wire {
        case "no_upstream": self = .noUpstream
        case "upstream_forbidden": self = .upstreamForbidden
        case "upstream_unavailable": self = .upstreamUnavailable
        case "upstream_failed": self = .upstreamFailed
        case "target_gone": self = .targetGone
        case "target_read_only": self = .targetReadOnly
        case "target_forbidden": self = .targetForbidden
        case "machine_offline": self = .machineOffline
        case "model_unavailable": self = .modelUnavailable
        case "not_meeting_room": self = .notMeetingRoom
        case "meeting_room": self = .meetingRoom
        default: self = .other(wire)
        }
    }
}

/// An `error` frame. The message is the extension's (or the upstream's) own words: for Details, not the line on screen.
nonisolated struct VoiceFailure: Equatable, Sendable {
    var code: VoiceErrorCode?
    var message: String
    /// The extension closes the socket after a fatal one: the call is over, nothing reconnects.
    var fatal: Bool
}

/// The `ready` frame: the extension is connected upstream and the rates are known.
nonisolated struct VoiceReady: Equatable, Sendable {
    /// 1 when the frame does not say (an extension from before protocol 2).
    var protocolVersion: Int
    var features: Set<String>
    var inputRate: Int
    var outputRate: Int
    var model: String?
    var voice: String?
    var target: VoiceTargetInfo?
}

nonisolated struct VoiceTranscript: Equatable, Sendable {
    var id: String
    /// "user" or "assistant".
    var role: String
    var text: String
    var final: Bool
    /// The line this one goes before, when it is new (protocol 2, #5673): the user's caption came after the voice began
    /// to answer it. nil: at the end.
    var before: String? = nil
}

/// One text frame from the extension.
nonisolated enum VoiceFrame: Equatable, Sendable {
    case ready(VoiceReady)
    case clear
    case transcript(VoiceTranscript)
    /// A function tool at work (protocol 2 uses it for those only; reconnects come as `link`).
    case tool(id: String, label: String, done: Bool)
    case run(VoiceRun)
    case ask(VoiceAsk)
    case link(VoiceLink)
    case error(VoiceFailure)
    case other(type: String)

    static let defaultInputRate = 16000
    static let defaultOutputRate = 24000

    /// nil when the text is not a JSON object with a string "type".
    static func parse(_ text: String) -> VoiceFrame? {
        guard case .object(let o)? = try? JSON.decoder.decode(JSONValue.self, from: Data(text.utf8)),
              case .string(let type)? = o["type"] else { return nil }
        switch type {
        case "ready":
            return .ready(VoiceReady(protocolVersion: whole(o["protocol"]).flatMap { $0 > 0 ? $0 : nil } ?? 1,
                                     features: Set((o["features"]?.array ?? []).compactMap { str($0) }),
                                     inputRate: rate(o["input_rate"]) ?? defaultInputRate,
                                     outputRate: rate(o["output_rate"]) ?? defaultOutputRate,
                                     model: str(o["model"]), voice: str(o["voice"]),
                                     target: target(o["target"])))
        case "clear":
            return .clear
        case "transcript":
            return .transcript(VoiceTranscript(id: str(o["id"]) ?? "", role: str(o["role"]) ?? "", text: str(o["text"]) ?? "",
                                               final: o["final"]?.bool ?? false,
                                               before: str(o["before"]).flatMap { $0.isEmpty ? nil : $0 }))
        case "tool":
            return .tool(id: str(o["id"]) ?? "", label: str(o["label"]) ?? "", done: o["done"]?.bool ?? false)
        case "run":
            guard let id = str(o["id"]), !id.isEmpty else { return .other(type: type) }
            return .run(VoiceRun(id: id, task: str(o["task"]) ?? "", state: VoiceRunState(wire: str(o["state"]) ?? ""),
                                 msgIDs: (o["msg_ids"]?.array ?? []).compactMap { str($0) },
                                 baseSeq: whole(o["base_seq"]) ?? 0, startedAt: VoiceStamp(o["started_at"]),
                                 code: str(o["code"]), message: str(o["message"]), draft: str(o["draft"]),
                                 clearable: str(o["clearable"]).flatMap { $0.isEmpty ? nil : $0 },
                                 resendable: o["resendable"]?.bool ?? false))
        case "ask":
            guard let id = str(o["interaction_id"]), !id.isEmpty else { return .other(type: type) }
            return .ask(VoiceAsk(interactionID: id, state: VoiceAskState(wire: str(o["state"]) ?? ""), by: str(o["by"])))
        case "link":
            return .link(VoiceLink(wire: str(o["state"]) ?? ""))
        case "error":
            return .error(VoiceFailure(code: str(o["code"]).map(VoiceErrorCode.init(wire:)), message: str(o["message"]) ?? "",
                                       fatal: o["fatal"]?.bool ?? false))
        default:
            return .other(type: type)
        }
    }

    private static func str(_ value: JSONValue?) -> String? {
        if case .string(let s)? = value { return s }
        return nil
    }

    /// A whole number that fits an Int; anything else (a fraction, 1e19, a string) takes the default. Not
    /// `JSONValue.int`, which traps on a number too large for an Int: a frame must never crash the app.
    private static func whole(_ value: JSONValue?) -> Int? {
        guard case .number(let n)? = value else { return nil }
        return Int(exactly: n)
    }

    /// A rate must be a positive number below a million; anything else takes the default.
    private static func rate(_ value: JSONValue?) -> Int? {
        guard case .number(let n)? = value, n.isFinite, n > 0, n < 1_000_000 else { return nil }
        return Int(n)
    }

    private static func target(_ value: JSONValue?) -> VoiceTargetInfo? {
        guard case .object(let t)? = value, let machine = str(t["machine_id"]), let session = str(t["session_id"]) else { return nil }
        return VoiceTargetInfo(machineID: machine, sessionID: session, kind: str(t["kind"]), title: str(t["title"]),
                               agentLabel: str(t["agent_label"]), machineName: str(t["machine_name"]))
    }
}

/// The `start` frame: the call's first, and again after every reconnect.
nonisolated struct VoiceStart: Equatable, Sendable {
    var model: String?
    var voice: String?
    var protocolVersion = 2
    var target: VoiceTarget?
    var recent: [VoiceLine] = []
    /// The call's captions so far: only when it picks up again.
    var history: [VoiceLine] = []
    /// The runs not over yet, as last reported: only when it picks up again.
    var resume: [VoiceRun] = []
    /// False: the microphone records without echo cancellation, so a caption of the user's may be the voice's own.
    var echoCancellation: Bool?
    /// False: the user turned the user-speech gate off on this device (Only Send What I Just Said, #5904); true: on.
    /// An ordinary call always says it (`VoiceConversation.startFrame`): it also tells the extension this app has Send
    /// Anyway, which an app from before it does not (nil, left out). An extension that does not know it ignores it. A
    /// meeting call leaves it out: the meeting page has no Send Anyway, and the extension keeps the gate on for it.
    var speechGate: Bool?
    /// A meeting call (#3862 phase 4, docs/assistant/meeting-minutes.md「Phase 4: realtime voice in the meeting」):
    /// `"meeting"`, with the recording whose room `target` is. nil for every other call.
    var mode: String? = nil
    var recordingID: String? = nil

    /// The mode of a meeting call: bound to the recording's room, hearing only Talk spans.
    static let meetingMode = "meeting"
}

/// Text frames the app sends.
nonisolated enum VoiceMessage {
    static func start(_ s: VoiceStart) -> String {
        encode(StartFrame(model: nonEmpty(s.model), voice: nonEmpty(s.voice), protocolVersion: s.protocolVersion,
                          mode: nonEmpty(s.mode), recordingID: nonEmpty(s.recordingID),
                          target: s.target.map { TargetFrame(machineID: $0.machineID, sessionID: $0.sessionID) },
                          recent: s.recent.isEmpty ? nil : s.recent, history: s.history.isEmpty ? nil : s.history,
                          resume: s.resume.isEmpty ? nil : ResumeFrame(runs: s.resume.map(ResumeRun.init)),
                          echoCancellation: s.echoCancellation, speechGate: s.speechGate))
    }

    /// A meeting call's Talk span opens (`true`) or closes (#3862 phase 4): the app sends the microphone's audio only
    /// between the two, and the extension feeds the upstream silence outside them whatever it receives.
    static func talk(_ on: Bool) -> String {
        encode(TalkFrame(on: on))
    }

    /// Stops what the voice is saying (not the agent).
    static let interrupt = #"{"type":"interrupt"}"#

    /// Says a sentence by typing it: the same as speaking it.
    static func text(_ text: String) -> String {
        encode(TextFrame(text: text))
    }

    /// Sends again a run a 409 refused, or one the user-speech gate held back (Send Anyway), under the same id.
    /// `clearDraft`: the line in the agent's input box the node is to clear first (`run.clearable`, checked against
    /// what is there).
    static func resend(id: String, clearDraft: String?) -> String {
        encode(ResendFrame(id: id, clearDraft: nonEmpty(clearDraft)))
    }

    private static func nonEmpty(_ s: String?) -> String? {
        guard let s, !s.isEmpty else { return nil }
        return s
    }

    private static let encoder: JSONEncoder = {
        let e = JSONEncoder()
        e.outputFormatting = [.sortedKeys, .withoutEscapingSlashes]
        return e
    }()

    private static func encode(_ frame: some Encodable) -> String {
        guard let data = try? encoder.encode(frame) else { return "{}" }
        return String(decoding: data, as: UTF8.self)
    }

    private struct StartFrame: Encodable {
        let type = "start"
        var model: String?
        var voice: String?
        var protocolVersion: Int
        var mode: String?
        var recordingID: String?
        var target: TargetFrame?
        var recent: [VoiceLine]?
        var history: [VoiceLine]?
        var resume: ResumeFrame?
        var echoCancellation: Bool?
        var speechGate: Bool?

        enum CodingKeys: String, CodingKey {
            case type, model, voice, mode, target, recent, history, resume
            case protocolVersion = "protocol"
            case recordingID = "recording_id"
            case echoCancellation = "echo_cancellation"
            case speechGate = "speech_gate"
        }
    }

    private struct TalkFrame: Encodable {
        let type = "talk"
        var on: Bool
    }

    private struct TargetFrame: Encodable {
        var machineID: String
        var sessionID: String
        enum CodingKeys: String, CodingKey { case machineID = "machine_id", sessionID = "session_id" }
    }

    private struct ResumeFrame: Encodable {
        var runs: [ResumeRun]
    }

    private struct ResumeRun: Encodable {
        var id: String
        var msgIDs: [String]
        var task: String
        var baseSeq: Int
        var startedAt: VoiceStamp?
        var state: String

        init(_ run: VoiceRun) {
            id = run.id
            msgIDs = run.msgIDs
            task = run.task
            baseSeq = run.baseSeq
            startedAt = run.startedAt
            state = run.state.wire
        }

        enum CodingKeys: String, CodingKey {
            case id, task, state
            case msgIDs = "msg_ids", baseSeq = "base_seq", startedAt = "started_at"
        }

        func encode(to encoder: any Encoder) throws {
            var c = encoder.container(keyedBy: CodingKeys.self)
            try c.encode(id, forKey: .id)
            try c.encode(msgIDs, forKey: .msgIDs)
            try c.encode(task, forKey: .task)
            try c.encode(baseSeq, forKey: .baseSeq)
            switch startedAt {
            case .text(let s)?: try c.encode(s, forKey: .startedAt)
            // Whole numbers go back without a fraction (1793872800123, not 1793872800123.0).
            case .number(let n)?:
                if let whole = Int64(exactly: n) { try c.encode(whole, forKey: .startedAt) } else { try c.encode(n, forKey: .startedAt) }
            case nil: break
            }
            try c.encode(state, forKey: .state)
        }
    }

    private struct TextFrame: Encodable {
        let type = "text"
        var text: String
    }

    private struct ResendFrame: Encodable {
        let type = "resend"
        var id: String
        var clearDraft: String?
        enum CodingKeys: String, CodingKey { case type, id, clearDraft = "clear_draft" }
    }
}

/// Where the call's socket goes: the extension's `/ws` under `/api/v1/extensions` (`/api/v1/plugins` on a hub from before
/// the rename), through the app's current way to the group's hub (`APIClient.url`: its SSH tunnel when it uses one), with
/// the device's token. As `RFBWebSocket` opens the Screen Sharing relay.
nonisolated enum VoiceEndpoint {
    static func url(_ api: APIClient, extensionID: String) -> URL {
        let http = api.url(api.extensionPath("/\(extensionID)/ws"))
        guard var comps = URLComponents(url: http, resolvingAgainstBaseURL: false) else { return http }
        comps.scheme = comps.scheme?.lowercased() == "https" ? "wss" : "ws"
        return comps.url ?? http
    }

    static func request(_ api: APIClient, extensionID: String) -> URLRequest {
        var req = URLRequest(url: url(api, extensionID: extensionID), timeoutInterval: 15)
        api.config.authorize(&req)
        return req
    }
}
