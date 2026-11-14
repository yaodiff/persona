import CoreGraphics
import Foundation

/// One of the Mac's displays on its Screen Sharing picture (#80): an id that stays the same across connections, and
/// where the display is on the framebuffer, in pixels.
nonisolated struct VNCDisplay: Equatable, Sendable, Identifiable {
    var id: String
    var frame: CGRect
}

/// The displays the VNC page offers (docs/extensions/computer-use.md "Displays"), most trusted source first:
/// 1. the server's own screens (ExtendedDesktopSize), already in framebuffer pixels;
/// 2. else the node's arrangement (points, global display space), scaled onto the framebuffer;
/// 3. else none. Fewer than two displays, or an arrangement whose shape is not the framebuffer's, offers none either.
nonisolated enum VNCDisplays {
    /// How far the arrangement's aspect ratio may be from the framebuffer's (a fraction) before it is not trusted: a
    /// display changed since the node last looked, or the server draws the displays at different scales.
    static let aspectTolerance: CGFloat = 0.02

    static func layout(server: [RFBClient.Screen], machine: [MachineDisplay], framebuffer: CGSize) -> [VNCDisplay] {
        let own = fromServer(server, framebuffer: framebuffer)
        return own.isEmpty ? fromMachine(machine, framebuffer: framebuffer) : own
    }

    /// The server's screens that lie on the framebuffer; none unless at least two do.
    static func fromServer(_ screens: [RFBClient.Screen], framebuffer: CGSize) -> [VNCDisplay] {
        let whole = CGRect(origin: .zero, size: framebuffer)
        let on = screens.filter { !$0.frame.isEmpty && whole.contains($0.frame) }
        guard on.count > 1 else { return [] }
        return ordered(on.map { VNCDisplay(id: "screen-\($0.id)", frame: $0.frame) })
    }

    /// The node's displays, their bounding box scaled onto the framebuffer; none unless at least two and the box has the
    /// framebuffer's shape.
    static func fromMachine(_ displays: [MachineDisplay], framebuffer: CGSize) -> [VNCDisplay] {
        let sized = displays.filter { !$0.frame.isEmpty }
        guard sized.count > 1, framebuffer.width > 0, framebuffer.height > 0 else { return [] }
        let box = sized.dropFirst().reduce(sized[0].frame) { $0.union($1.frame) }
        let sx = framebuffer.width / box.width, sy = framebuffer.height / box.height
        guard abs(sx / sy - 1) <= aspectTolerance else { return [] }
        let whole = CGRect(origin: .zero, size: framebuffer)
        return ordered(sized.map { d in
            let f = d.frame
            let r = CGRect(x: ((f.minX - box.minX) * sx).rounded(), y: ((f.minY - box.minY) * sy).rounded(),
                           width: (f.width * sx).rounded(), height: (f.height * sy).rounded())
            return VNCDisplay(id: "display-\(d.id)", frame: r.intersection(whole))
        })
    }

    /// Left to right, then top to bottom: the order the menu numbers them in.
    static func ordered(_ displays: [VNCDisplay]) -> [VNCDisplay] {
        displays.sorted { ($0.frame.minX, $0.frame.minY) < ($1.frame.minX, $1.frame.minY) }
    }

    /// Framebuffer pixels a point of the Mac's screen (#7775: a wheel notch scrolls a point): the framebuffer's width
    /// over the node's arrangement's (2 where Screen Sharing sends a Retina display at its full size); 1 without an
    /// arrangement, or one whose shape is not the framebuffer's.
    static func pixelsPerMacPoint(_ displays: [MachineDisplay], framebuffer: CGSize) -> CGFloat {
        let sized = displays.filter { !$0.frame.isEmpty }
        guard let first = sized.first, framebuffer.width > 0, framebuffer.height > 0 else { return 1 }
        let box = sized.dropFirst().reduce(first.frame) { $0.union($1.frame) }
        let sx = framebuffer.width / box.width, sy = framebuffer.height / box.height
        guard abs(sx / sy - 1) <= aspectTolerance else { return 1 }
        return min(max(sx, 1), 4)
    }
}

/// The display the VNC page shows for a Mac, kept per Mac on this device as `VNCMode` is; nil: all of them. A chosen
/// display that is not there now (unplugged) shows all of them and stays chosen for when it is back.
enum VNCDisplayChoice {
    private static func key(_ ref: MachineRef) -> String { "vnc.display." + ScreenCredentials.key(ref) }
    static func of(_ ref: MachineRef) -> String? { UserDefaults.standard.string(forKey: key(ref)) }
    static func set(_ id: String?, for ref: MachineRef) {
        if let id { UserDefaults.standard.set(id, forKey: key(ref)) } else { UserDefaults.standard.removeObject(forKey: key(ref)) }
    }
}

/// The part of the framebuffer the page shows (`visible`, in pixels: one display, or all of it) and where it is drawn in
/// the view (`rect`, in points, zoomed or fitted). Touches become framebuffer pixels, as RFB's pointer events carry
/// them, kept on the visible part.
nonisolated struct VNCViewport: Equatable {
    var visible: CGRect
    var rect: CGRect

    /// Framebuffer pixels per view point (the same both ways: the picture keeps its aspect).
    var pixelsPerPoint: CGFloat { visible.width / max(rect.width, 1) }

    /// A point of the view on the framebuffer.
    func toScreen(_ p: CGPoint) -> CGPoint {
        CGPoint(x: visible.minX + (p.x - rect.minX) / rect.width * visible.width,
                y: visible.minY + (p.y - rect.minY) / rect.height * visible.height)
    }

    /// A framebuffer pixel in the view.
    func toView(_ q: CGPoint) -> CGPoint {
        CGPoint(x: rect.minX + (q.x - visible.minX) / visible.width * rect.width,
                y: rect.minY + (q.y - visible.minY) / visible.height * rect.height)
    }

    /// q kept on the visible part (its last pixel at most).
    func clamped(_ q: CGPoint) -> CGPoint {
        CGPoint(x: min(max(visible.minX, q.x), max(visible.minX, visible.maxX - 1)),
                y: min(max(visible.minY, q.y), max(visible.minY, visible.maxY - 1)))
    }

    /// Where the whole framebuffer (of this size) is drawn, relative to `rect`'s origin, so that `visible` fills `rect`.
    func framebufferFrame(_ size: CGSize) -> CGRect {
        let k = rect.width / visible.width
        return CGRect(x: -visible.minX * k, y: -visible.minY * k, width: size.width * k, height: size.height * k)
    }

    /// Where a part of the framebuffer (pixels) is drawn, in the same space as `framebufferFrame` (the clip's).
    func frame(of part: CGRect) -> CGRect {
        let k = rect.width / visible.width
        return CGRect(x: (part.minX - visible.minX) * k, y: (part.minY - visible.minY) * k,
                      width: part.width * k, height: part.height * k)
    }
}
