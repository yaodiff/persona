import Foundation

// Phone Screen (#9314, docs/extensions/computer-use.md "Phone Screen"): the live screen's third mode. The Mac's Persona
// Input helper adds a portrait virtual display shaped like the phone's picture and puts one window on it at a time.
// This file is the part both ends can test without a Mac: the display's sizes, where it goes, the messages between the
// node and the helper, and the records that put windows back.
//
// The sizes and the 540 × 800 point threshold follow Snug's measurements (https://github.com/zephyr4123/snug, MIT,
// Copyright (c) 2026 AoyamaShou, Copyright (c) 2026 zephyr4123; THIRD_PARTY_NOTICES.md).

/// The virtual display's shape and the widths it offers. The aspect is the phone's picture (height over width); every
/// width shares it, so moving to a wider one only scales the picture.
public struct PhoneScreenFit: Equatable, Sendable {
    /// macOS offers a 2× (HiDPI) desktop mode only from 540 × 800 points (Snug, measured; not a pixel limit).
    public static let minWidth = 540
    public static let minHeight = 800
    /// The widest step: text is half the base size, no smaller than the Mac's own screen shown whole on a phone.
    public static let maxWidth = 1080
    /// Steps 10 points apart: a window that does not fit loses at most 2% more than it must.
    public static let step = 10
    /// Squarest to longest: a folding phone's inner screen to a tall phone in portrait with no bars.
    public static let ratioRange: ClosedRange<Double> = 1...2.2
    /// The display's pixel limit, the widest step at the longest aspect, at 2×: one identity for every shape.
    public static let maxPixels = (width: maxWidth * 2, height: Int((Double(maxWidth) * ratioRange.upperBound).rounded(.up)) * 2)

    /// Height over width.
    public let ratio: Double
    /// The width a window that fits is shown at (the narrowest with a 2× mode).
    public let baseWidth: Int
    /// Every width offered, base first.
    public let widths: [Int]

    /// The shape of a phone's picture area, in points.
    public init(viewWidth: Double, viewHeight: Double) {
        let ratio = viewWidth.isFinite && viewHeight.isFinite && viewWidth > 0 && viewHeight > 0 ? viewHeight / viewWidth : 2
        self.init(ratio: ratio)
    }

    public init(ratio: Double) {
        let r = min(max(ratio.isFinite ? ratio : 2, Self.ratioRange.lowerBound), Self.ratioRange.upperBound)
        self.ratio = r
        let all = Array(stride(from: Self.minWidth, through: Self.maxWidth, by: Self.step))
        // A short picture is under 800 points tall at 540 wide and gets no 2× mode: start at the first width tall enough.
        let base = all.first { (Double($0) * r).rounded() >= Double(Self.minHeight) } ?? Self.maxWidth
        baseWidth = base
        widths = all.filter { $0 >= base }
    }

    /// The display's size in points at `width`.
    public func size(width: Int) -> PhoneScreenSize {
        PhoneScreenSize(width: width, height: Self.clampedInt((Double(width) * ratio).rounded()))
    }

    /// The width to show a window at, given the size it really took when fitted to `area` at width `current` (an app
    /// with a minimum size stays larger): `current` when it fits (2 points of slack), else the narrowest width whose
    /// area holds it, at most the widest. Multiply before dividing: 540 × (580 / 540) is 580.0000000000001.
    public func width(toFit window: PhoneScreenSize, in area: PhoneScreenSize, at current: Int) -> Int {
        let slack = 2.0
        guard area.width > 0, area.height > 0,
              Double(window.width) > Double(area.width) + slack || Double(window.height) > Double(area.height) + slack
        else { return current }
        guard current > 0 else { return baseWidth }
        let needed = max(Double(current) * Double(window.width) / Double(area.width),
                         Double(current) * Double(window.height) / Double(area.height))
        guard needed.isFinite else { return Self.maxWidth }
        return widths.first { Double($0) >= needed.rounded(.up) } ?? Self.maxWidth
    }

    static func clampedInt(_ value: Double) -> Int {
        guard !value.isNaN else { return 0 }
        if value >= Double(Int.max) { return Int.max }
        if value <= Double(Int.min) { return Int.min }
        return Int(value)
    }
}

public struct PhoneScreenSize: Codable, Equatable, Sendable {
    public var width: Int
    public var height: Int
    public init(width: Int, height: Int) { self.width = width; self.height = height }
}

/// A rectangle in macOS's global display space: points, origin at the main display's top-left corner, y down.
public struct PhoneScreenRect: Codable, Equatable, Sendable {
    public var x: Double
    public var y: Double
    public var width: Double
    public var height: Double

    public init(x: Double, y: Double, width: Double, height: Double) {
        self.x = x; self.y = y; self.width = width; self.height = height
    }

    public var maxX: Double { x + width }
    public var maxY: Double { y + height }

    var isFinite: Bool {
        x.isFinite && y.isFinite && width.isFinite && height.isFinite && maxX.isFinite && maxY.isFinite
    }

    /// Overlap with an area, not just a shared edge.
    public func overlaps(_ o: PhoneScreenRect) -> Bool {
        guard isFinite, o.isFinite, width > 0, height > 0, o.width > 0, o.height > 0,
              x < maxX, y < maxY, o.x < o.maxX, o.y < o.maxY else { return false }
        return x < o.maxX && o.x < maxX && y < o.maxY && o.y < maxY
    }

    /// Within `tolerance` points of `o` on every edge.
    public func matches(_ o: PhoneScreenRect, tolerance: Double = 2) -> Bool {
        guard isFinite, o.isFinite, tolerance.isFinite, tolerance >= 0 else { return false }
        return abs(x - o.x) <= tolerance && abs(y - o.y) <= tolerance
            && abs(width - o.width) <= tolerance && abs(height - o.height) <= tolerance
    }
}

/// Where the virtual display goes: against the main display, on its first side with room (right, left, below, above),
/// aligned to main's top or left edge. macOS Screen Sharing draws each display at its own pixel size and keeps the
/// edge that touches the main display at main's pixel coordinate (measured on macOS 26.6), so a display against the
/// main one is where the phone can find it on the framebuffer.
public enum PhoneScreenPlacement {
    public enum Side: String, CaseIterable, Sendable { case right, left, below, above }

    /// The origin for a display of `size` points next to `main`, clear of `others`; nil when every side is taken.
    public static func origin(for size: PhoneScreenSize, main: PhoneScreenRect, others: [PhoneScreenRect]) -> (x: Double, y: Double)? {
        guard size.width > 0, size.height > 0, main.isFinite, main.width > 0, main.height > 0 else { return nil }
        let w = Double(size.width), h = Double(size.height)
        for side in Side.allCases {
            let r: PhoneScreenRect
            switch side {
            case .right: r = PhoneScreenRect(x: main.maxX, y: main.y, width: w, height: h)
            case .left: r = PhoneScreenRect(x: main.x - w, y: main.y, width: w, height: h)
            case .below: r = PhoneScreenRect(x: main.x, y: main.maxY, width: w, height: h)
            case .above: r = PhoneScreenRect(x: main.x, y: main.y - h, width: w, height: h)
            }
            guard r.isFinite else { continue }
            if !others.contains(where: { $0.overlaps(r) }) { return (r.x, r.y) }
        }
        return nil
    }
}

// MARK: - Node ⇄ helper messages

/// One request from the node, a JSON line. `op`: start (`width`, `height`: the phone's picture in points), windows,
/// place (`pid`, `window`), stop. The helper answers each with one `PhoneScreenReply` line, in order.
public struct PhoneScreenRequest: Codable, Equatable, Sendable {
    public var op: String
    public var width: Double?
    public var height: Double?
    public var pid: Int32?
    public var window: UInt32?

    public init(op: String, width: Double? = nil, height: Double? = nil, pid: Int32? = nil, window: UInt32? = nil) {
        self.op = op; self.width = width; self.height = height; self.pid = pid; self.window = window
    }
}

/// The helper's answer. `error` is a code the node passes on: `permission` (no Accessibility), `display` (macOS would
/// not add or show the display), `no_display` (place before start), `window_gone`, `not_responding` (the app's
/// accessibility did not answer), `bad_request`.
public struct PhoneScreenReply: Codable, Equatable, Sendable {
    public var ok: Bool
    public var error: String?
    public var message: String?
    /// The virtual display (start, place: its width may have moved to fit the window).
    public var display: PhoneScreenDisplay?
    /// Every display as arranged now, the virtual one included (start).
    public var displays: [PhoneScreenDisplay]?
    public var windows: [PhoneScreenWindow]?
    /// The window placed and the frame it took (place).
    public var placed: PhoneScreenWindow?

    public init(ok: Bool, error: String? = nil, message: String? = nil, display: PhoneScreenDisplay? = nil,
                displays: [PhoneScreenDisplay]? = nil, windows: [PhoneScreenWindow]? = nil, placed: PhoneScreenWindow? = nil) {
        self.ok = ok; self.error = error; self.message = message; self.display = display
        self.displays = displays; self.windows = windows; self.placed = placed
    }

    public static func failure(_ code: String, _ message: String) -> PhoneScreenReply {
        PhoneScreenReply(ok: false, error: code, message: message)
    }
}

/// A display in the global display space, as the node's `displays` carry them, plus its pixels per point.
public struct PhoneScreenDisplay: Codable, Equatable, Sendable {
    public var id: Int
    public var x: Int
    public var y: Int
    public var width: Int
    public var height: Int
    public var scale: Double
    public var main: Bool?

    public init(id: Int, x: Int, y: Int, width: Int, height: Int, scale: Double, main: Bool? = nil) {
        self.id = id; self.x = x; self.y = y; self.width = width; self.height = height; self.scale = scale; self.main = main
    }
}

/// A window the phone can put on the display. `placed`: it is on the virtual display now.
public struct PhoneScreenWindow: Codable, Equatable, Sendable {
    public var pid: Int32
    public var window: UInt32
    public var app: String
    public var bundle: String?
    public var title: String
    public var minimized: Bool?
    public var placed: Bool?
    public var frame: PhoneScreenRect?

    public init(pid: Int32, window: UInt32, app: String, bundle: String? = nil, title: String, minimized: Bool? = nil,
                placed: Bool? = nil, frame: PhoneScreenRect? = nil) {
        self.pid = pid; self.window = window; self.app = app; self.bundle = bundle; self.title = title
        self.minimized = minimized; self.placed = placed; self.frame = frame
    }
}

// MARK: - Putting windows back

/// Where a window was before it went on the virtual display. Written before the window moves (no record, no move) and
/// removed once it is back, so a helper that died leaves what the next session needs to put it back.
public struct PhoneScreenRestore: Codable, Equatable, Sendable {
    public struct Record: Codable, Equatable, Sendable {
        public var pid: Int32
        public var window: UInt32
        public var app: String
        public var frame: PhoneScreenRect
        public var minimized: Bool
        public var fullScreen: Bool
        /// Steps of ⌘+ the helper gave the window's app (PhoneScreenZoom), undone with as many ⌘− when it goes back;
        /// nil: none. Absent from a record a helper before it wrote.
        public var zoom: Int?

        public init(pid: Int32, window: UInt32, app: String, frame: PhoneScreenRect, minimized: Bool, fullScreen: Bool,
                    zoom: Int? = nil) {
            self.pid = pid; self.window = window; self.app = app; self.frame = frame
            self.minimized = minimized; self.fullScreen = fullScreen; self.zoom = zoom
        }
    }

    public var records: [Record]

    public init(records: [Record] = []) { self.records = records }

    /// Keeps the first record of a window: placed again, it still goes back to where it was before the session.
    public mutating func add(_ r: Record) {
        if !records.contains(where: { $0.pid == r.pid && $0.window == r.window }) { records.append(r) }
    }

    /// Notes the zoom given to a window that has a record.
    public mutating func setZoom(_ steps: Int?, pid: Int32, window: UInt32) {
        guard let i = records.firstIndex(where: { $0.pid == pid && $0.window == window }) else { return }
        records[i].zoom = steps
    }

    public mutating func remove(pid: Int32, window: UInt32) {
        records.removeAll { $0.pid == pid && $0.window == window }
    }

    public func record(pid: Int32, window: UInt32) -> Record? {
        records.first { $0.pid == pid && $0.window == window }
    }
}

/// Bigger text for the apps that are read at phone size most (#9314's follow-up): the display can't be narrower than 540
/// points at 2× (macOS marks narrower 2× modes not usable for the desktop, measured), so its text is about three
/// quarters of the Mac's on a phone. Terminals and editors get ⌘+ `steps` times while their window is on the display
/// and as many ⌘− when it goes back, as Snug does. Browsers are left out: their zoom is per site, so it would follow the
/// user's other tabs of that site, and a page at 540 points is already laid out for a narrow screen.
public enum PhoneScreenZoom {
    public static let steps = 2

    /// Bundle ids whose ⌘+ / ⌘− zoom the text of the front window (or the whole app) and nothing else.
    public static let apps: Set<String> = [
        "com.mitchellh.ghostty", "com.apple.Terminal", "com.googlecode.iterm2",
        "com.microsoft.VSCode", "com.microsoft.VSCodeInsiders", "com.todesktop.230313mzl4w4u92", // Cursor
        "dev.zed.Zed", "dev.zed.Zed-Preview",
    ]

    /// Apps whose zoom is one for the whole app, not the window's: when the window went away while zoomed, the app
    /// still gets its ⌘− (Zed keeps the size for all its windows).
    public static let appWide: Set<String> = ["dev.zed.Zed", "dev.zed.Zed-Preview"]

    /// How many steps of ⌘+ a window of this app gets: `steps` for the apps above, else 0.
    public static func steps(for bundle: String?) -> Int {
        bundle.map { apps.contains($0) } == true ? steps : 0
    }
}

extension PhoneScreenZoom {
    /// Raises a placed window's text (#9540): `steps` go into its record, the records are saved, and only once that save
    /// succeeded are the keys sent (`send`), so a crash after them always leaves the ⌘− to undo them on disk. A failed
    /// save sends nothing and takes the steps back out of the record, so the records in memory are what the disk holds.
    /// True when the keys went.
    @discardableResult
    public static func raise(_ restore: inout PhoneScreenRestore, pid: Int32, window: UInt32, steps: Int,
                             save: (PhoneScreenRestore) -> Bool, send: (Int) -> Void) -> Bool {
        guard steps > 0, let r = restore.record(pid: pid, window: window), r.zoom == nil else { return false }
        restore.setZoom(steps, pid: pid, window: window)
        guard save(restore) else {
            restore.setZoom(nil, pid: pid, window: window)
            return false
        }
        send(steps)
        return true
    }

    /// The keys that undo a record's zoom: as many ⌘− (negative) as it got ⌘+, 0 for none.
    public static func undo(_ r: PhoneScreenRestore.Record) -> Int {
        guard let z = r.zoom, z > 0 else { return 0 }
        return -z
    }
}

/// The restore records on disk (`~/.persona/phone-screen.json` for the helper): JSON, written atomically, gone while
/// there is nothing to put back. `save` says whether the disk now holds exactly `restore`.
public struct PhoneScreenRestoreStore: Sendable {
    public let url: URL

    public init(url: URL) { self.url = url }

    public func readRecords() -> PhoneScreenRestore {
        guard let data = try? Data(contentsOf: url) else { return PhoneScreenRestore() }
        return (try? JSONDecoder().decode(PhoneScreenRestore.self, from: data)) ?? PhoneScreenRestore()
    }

    public func writeRecords(_ restore: PhoneScreenRestore) -> Bool {
        let files = FileManager.default
        if restore.records.isEmpty {
            try? files.removeItem(at: url)
            return !files.fileExists(atPath: url.path)
        }
        do {
            try files.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
            try JSONEncoder().encode(restore).write(to: url, options: .atomic)
            return true
        } catch {
            return false
        }
    }
}

/// The helper's `--preflight` line for this mode, `phone_screen: <version> granted|missing` (Accessibility), which the
/// node reads to offer it. A helper without the mode prints none.
public enum PhoneScreenPreflight {
    public static let version = 1

    public static func line(trusted: Bool) -> String {
        "phone_screen: \(version) \(trusted ? "granted" : "missing")"
    }
}
