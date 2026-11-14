#if DEBUG && (os(iOS) || os(visionOS))
import Network
import SwiftUI
import UIKit

/// A command channel into the running app for scripts (`scripts/app/personactl.py`, docs/operations/runtime-cli.md), DEBUG builds only.
///
/// Off unless launched with `-runtimeCLI <port>` (and `-runtimeCLIToken <token>`): it then listens on 127.0.0.1:<port>
/// (the simulator shares the Mac's loopback) for one JSON request per connection, a line
/// `{"token": …, "cmd": …, "args": {…}}`, and answers one line `{"ok": true, "ms": …, …}` or `{"ok": false, "error": …}`.
/// Commands run on the main actor against `AppModel.shared`; each area of commands lives in its own
/// `RuntimeCLI+<Area>.swift` and is listed in `commands`.
enum RuntimeCLI {
    typealias Args = [String: Any]
    typealias Reply = [String: Any]
    typealias Command = @MainActor (Args) async -> Reply

    nonisolated(unsafe) private static var listener: NWListener?
    nonisolated(unsafe) private static var token = ""

    /// Every command by name: these, and each area's (`RuntimeCLI+<Area>.swift`).
    static let commands: [String: Command] = coreCommands.merging(navigationCommands) { core, _ in core }
        .merging(elementCommands) { core, _ in core }
        .merging(actionCommands) { core, _ in core }
        .merging(motionCommands) { core, _ in core }
        .merging(imageViewerCommands) { core, _ in core }
        .merging(agentsPagingCommands) { core, _ in core }
        .merging(screenRoutesTable) { core, _ in core }

    /// The live screen's routes (#6398), on the phone only.
    private static var screenRoutesTable: [String: Command] {
        #if os(iOS) && canImport(PersonaScreenKit)
        screenRouteCommands
        #else
        [:]
        #endif
    }

    private static let coreCommands: [String: Command] = [
        "ping": { _ in ["pong": true, "pid": Int(ProcessInfo.processInfo.processIdentifier)] },
        "state": { _ in state() },
        "settle": { a in await settle(a) },
        "online": { a in await online(a) },
    ]

    static func startIfAsked() {
        let d = UserDefaults.standard
        restoreReduceMotion()
        if overHub { enableAccessibilityTree() } // the hub's calls read the tree too (RuntimeCLI+Hub.swift)
        let port = d.integer(forKey: "runtimeCLI")
        guard port > 0, port < 65536, listener == nil, let nwPort = NWEndpoint.Port(rawValue: UInt16(port)) else { return }
        token = d.string(forKey: "runtimeCLIToken") ?? ""
        enableAccessibilityTree()
        let params = NWParameters.tcp
        params.requiredLocalEndpoint = .hostPort(host: "127.0.0.1", port: nwPort)
        params.allowLocalEndpointReuse = true
        guard let l = try? NWListener(using: params) else { return }
        l.newConnectionHandler = { @Sendable conn in serve(conn) }
        l.start(queue: .global(qos: .userInitiated))
        listener = l
    }

    // MARK: Transport

    nonisolated private static func serve(_ conn: NWConnection) {
        conn.start(queue: .global(qos: .userInitiated))
        receive(conn, Data())
    }

    nonisolated private static func receive(_ conn: NWConnection, _ buffer: Data) {
        conn.receive(minimumIncompleteLength: 1, maximumLength: 1 << 20) { @Sendable data, _, done, error in
            var buf = buffer
            if let data { buf.append(data) }
            if let nl = buf.firstIndex(of: 0x0A) {
                let line = Data(buf[..<nl])
                Task { @MainActor in
                    var out = encode(await handle(line))
                    out.append(0x0A)
                    conn.send(content: out, completion: .contentProcessed { @Sendable _ in conn.cancel() })
                }
            } else if done || error != nil {
                conn.cancel()
            } else {
                receive(conn, buf)
            }
        }
    }

    private static func handle(_ line: Data) async -> Reply {
        guard let req = (try? JSONSerialization.jsonObject(with: line)) as? Args else { return fail("bad json") }
        guard (req["token"] as? String ?? "") == token else { return fail("bad token") }
        return await run(req["cmd"] as? String ?? "", req["args"] as? Args ?? [:])
    }

    /// One command by name, with its `ok` and `ms`: what the local port and the hub (RuntimeCLI+Hub.swift) both run.
    static func run(_ name: String, _ args: Args) async -> Reply {
        guard let command = commands[name] else { return fail("unknown command \(name): \(commands.keys.sorted())") }
        let started = Date.now
        var r = await command(args)
        if r["ok"] == nil { r["ok"] = true }
        r["ms"] = Int(Date.now.timeIntervalSince(started) * 1000)
        return r
    }

    /// JSON, or an error reply when a value cannot be written (a NaN frame would otherwise throw an exception).
    static func encode(_ r: Reply) -> Data {
        if JSONSerialization.isValidJSONObject(r), let d = try? JSONSerialization.data(withJSONObject: r, options: [.sortedKeys]) {
            return d
        }
        return Data(#"{"ok":false,"error":"reply is not JSON"}"#.utf8)
    }

    static func fail(_ why: String) -> Reply { ["ok": false, "error": why] }

    // MARK: State

    static func name(_ k: SessionKey?) -> Any { k.map { "\($0.machineID)/\($0.sessionID)" } ?? NSNull() }

    static func state() -> Reply {
        let m = AppModel.shared
        return [
            "tab": m.selectedTab.rawValue, "openChat": name(m.openChat), "assistant": name(m.assistantCurrent),
            "visibleSession": name(m.visibleSession), "settingsOpen": m.shellSettings, "settingsDepth": m.settingsPath.count,
            "style": m.style.rawValue, "color": m.themeColor.rawValue, "paired": m.isPaired,
            "connection": "\(m.connection.shown)",
            "appearance": window?.traitCollection.userInterfaceStyle == .dark ? "dark" : "light",
            "appState": ["active", "inactive", "background"][min(2, UIApplication.shared.applicationState.rawValue)],
            "screen": [Double(window?.bounds.width ?? 0), Double(window?.bounds.height ?? 0)],
            "vncPiP": Array(VNCPiPLog.lines.suffix(20)), // the small window's steps (#1839)
        ]
    }

    /// Waits for the hub's live connection; after 2 s asks it to connect, what coming back to the foreground does. A
    /// long scripted run outlives sockets that a relaunch per scene never kept.
    private static func online(_ a: Args) async -> Reply {
        let m = AppModel.shared
        let started = Date.now, deadline = started.addingTimeInterval(a["timeout"] as? Double ?? 15)
        var nudged = false
        while m.connection.shown != .online {
            if Date.now > deadline { return fail("still \(m.connection.shown)") }
            if !nudged, Date.now.timeIntervalSince(started) > 2 { m.live.ensureConnected(); nudged = true }
            try? await Task.sleep(for: .milliseconds(100))
        }
        return ["nudged": nudged]
    }

    // MARK: Pixels

    static var windows: [UIWindow] {
        UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.flatMap(\.windows)
    }

    static var window: UIWindow? { windows.first(where: \.isKeyWindow) ?? windows.first }
    /// What is on screen, in window coordinates: the screen's bounds; visionOS has no screen, so the window's own.
    static var screenBounds: CGRect {
        #if os(visionOS)
        window?.bounds ?? .zero
        #else
        window?.screen.bounds ?? .zero
        #endif
    }

    static func render(scale: CGFloat) -> UIImage? {
        guard let w = window else { return nil }
        let f = UIGraphicsImageRendererFormat()
        f.scale = scale
        return UIGraphicsImageRenderer(bounds: w.bounds, format: f).image { _ in
            for win in windows where !win.isHidden { win.drawHierarchy(in: win.frame, afterScreenUpdates: false) }
        }
    }

    /// The windows at 1× as RGBA bytes.
    private static func pixels() -> [UInt8]? {
        guard let cg = render(scale: 1)?.cgImage else { return nil }
        let w = cg.width, h = cg.height
        var buf = [UInt8](repeating: 0, count: w * h * 4)
        let ok = buf.withUnsafeMutableBytes { raw -> Bool in
            guard let ctx = CGContext(data: raw.baseAddress, width: w, height: h, bitsPerComponent: 8, bytesPerRow: w * 4,
                                      space: CGColorSpaceCreateDeviceRGB(),
                                      bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue) else { return false }
            ctx.draw(cg, in: CGRect(x: 0, y: 0, width: w, height: h))
            return true
        }
        return ok ? buf : nil
    }

    /// Waits until the screen stops changing: the windows drawn every `every` ms (at 1×) differ in at most `pixels`
    /// pixels (a working agent's dot keeps ~300 changing) `stable` times in a row, or `timeout` seconds pass. What a
    /// fixed `sleep` after a launch stood for. Left out of the comparison: the frames of the elements `ignore` finds
    /// (one set of element filters or a list of them, found again every frame; `ignorePad` points around each,
    /// default 6: a breathing dot's ring is drawn past its frame) and `ignoreRects` ([x, y, w, h] in points), for
    /// what never stops on its own (machines' live curves). `box`: where the last frames still differed.
    private static func settle(_ a: Args) async -> Reply {
        let deadline = Date.now.addingTimeInterval(a["timeout"] as? Double ?? 8)
        let every = a["every"] as? Int ?? 150, stable = a["stable"] as? Int ?? 3, allowed = a["pixels"] as? Int ?? 0
        let ignore = Ignore(a)
        var last: [UInt8]?
        var lastMask: [CGRect] = []
        var same = 0, frames = 0, changed = 0
        var box = CGRect.null
        while Date.now < deadline {
            try? await Task.sleep(for: .milliseconds(every))
            let mask = ignore.rects()
            let now = pixels()
            frames += 1
            if let now, let last, now.count == last.count, let w = window.map({ Int($0.bounds.width) }), w > 0 {
                (changed, box) = differ(now, last, width: w, skip: mask + lastMask)
                same = changed <= allowed ? same + 1 : 0
            } else {
                same = 0
            }
            last = now
            lastMask = mask
            if same >= stable { return ["settled": true, "frames": frames, "changed": changed, "box": rect(box)] }
        }
        return ["settled": false, "frames": frames, "changed": changed, "box": rect(box)]
    }

    static func rect(_ r: CGRect) -> Any {
        r.isNull ? NSNull() : [r.minX, r.minY, r.width, r.height].map { Double($0) }
    }

    /// What `settle` leaves out: element filters (`ignore`) and rects (`ignoreRects`), in points (= pixels at 1×).
    struct Ignore {
        var filters: [Args] = []
        var fixed: [CGRect] = []
        var pad: CGFloat = 6

        init(_ a: Args) {
            if let one = a["ignore"] as? Args { filters = [one] } else { filters = a["ignore"] as? [Args] ?? [] }
            fixed = (a["ignoreRects"] as? [[Double]] ?? []).compactMap { r in
                r.count == 4 ? CGRect(x: r[0], y: r[1], width: r[2], height: r[3]) : nil
            }
            pad = CGFloat(a["ignorePad"] as? Double ?? 6)
        }

        func rects() -> [CGRect] {
            guard !filters.isEmpty else { return fixed }
            return fixed + filters.flatMap { f in
                RuntimeCLI.elements(matching: f).map(\.frame)
                    .filter { $0.minX.isFinite && $0.minY.isFinite && !$0.isEmpty }
                    .map { $0.insetBy(dx: -pad, dy: -pad) }
            }
        }
    }

    /// Pixels (RGB) that differ between two frames of `width` columns, outside `skip`, and the box around them.
    private static func differ(_ a: [UInt8], _ b: [UInt8], width: Int, skip: [CGRect]) -> (Int, CGRect) {
        if a == b { return (0, .null) }
        let height = a.count / 4 / width
        var masked = [Bool](repeating: false, count: skip.isEmpty ? 0 : width * height)
        for r in skip {
            let x0 = max(0, Int(r.minX.rounded(.down))), x1 = min(width, Int(r.maxX.rounded(.up)))
            let y0 = max(0, Int(r.minY.rounded(.down))), y1 = min(height, Int(r.maxY.rounded(.up)))
            guard x0 < x1, y0 < y1 else { continue }
            for y in y0..<y1 { for x in x0..<x1 { masked[y * width + x] = true } }
        }
        var n = 0, minX = width, minY = height, maxX = -1, maxY = -1
        var i = 0, p = 0
        while i < a.count {
            if a[i] != b[i] || a[i + 1] != b[i + 1] || a[i + 2] != b[i + 2], masked.isEmpty || !masked[p] {
                n += 1
                let x = p % width, y = p / width
                minX = min(minX, x); maxX = max(maxX, x); minY = min(minY, y); maxY = max(maxY, y)
            }
            i += 4
            p += 1
        }
        return (n, n == 0 ? .null : CGRect(x: minX, y: minY, width: maxX - minX + 1, height: maxY - minY + 1))
    }
}
#endif
