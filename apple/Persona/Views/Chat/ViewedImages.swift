import Foundation
import PersonaWire

/// An image the agent looked at in a turn (docs/protocol/protocol.md「Images viewed this turn」), for the strip before its answer:
/// one a tool's result carried, or a picture file Read opened whose result carried none (only `path`).
nonisolated struct ViewedImage: Identifiable, Hashable, Sendable {
    enum Source: String, Sendable { case read, screenshot, browser, mcp, tool }

    /// What it is told apart by (`ViewedImages.key`): one path once, one image once.
    var id: String
    /// Its bytes: inline, or lazy with a ref. Nil when only the file is known.
    var payload: ImagePayload?
    var path: String?
    var source: Source
    var tool: String
    /// The machine a screenshot's or a browser's call named (an id or a name).
    var machine: String?

    /// Width / height when known: the tile takes its shape before the bytes arrive.
    var aspect: CGFloat? { payload?.aspect }
    var name: String? { path.map { ($0 as NSString).lastPathComponent }.flatMap { $0.isEmpty ? nil : $0 } }
}

/// A turn's images from its work, by the same rules as the hub's `transcript.ViewedImages` (a fold carries what its
/// run looked at, `Fold.images`): in the order they came, each once. A plain function of the work, so `ChatLines`
/// builds the strip's line with the rest and `ViewedImagesTests` checks it.
nonisolated enum ViewedImages {
    /// The files whose Read is looking at a picture.
    static let pictureExtensions: Set<String> = ["png", "jpg", "jpeg", "gif", "webp", "heic", "heif", "bmp", "tif", "tiff"]

    static func of(_ work: [WorkItem]) -> [ViewedImage] {
        var out: [ViewedImage] = []
        var seen = Set<String>()
        func add(_ im: ViewedImage) {
            if seen.insert(im.id).inserted { out.append(im) }
        }
        for item in work {
            switch item {
            case .step(.call(let c)): images(of: c).forEach(add)
            case .step(.folded(let f)): (f.fold?.images ?? []).map(image).forEach(add)
            default: continue
            }
        }
        return out
    }

    /// What a call looked at: the images its result carried; a picture file Read opened, once it did, when none.
    static func images(of c: ToolCall) -> [ViewedImage] {
        let (source, path, machine) = said(c.name, c.input)
        if !c.images.isEmpty {
            // Read reads one picture: it is that file. Another tool's images are its own.
            let file = c.images.count == 1 ? path : nil
            return c.images.map { p in
                ViewedImage(id: key(path: file, payload: p), payload: p, path: file, source: source, tool: c.name, machine: machine)
            }
        }
        guard !c.isRunning, !c.isError, let path, isPicture(path) else { return [] }
        return [ViewedImage(id: key(path: path, payload: nil), payload: nil, path: path, source: source, tool: c.name, machine: machine)]
    }

    static func image(_ f: FoldImage) -> ViewedImage {
        var payload: ImagePayload?
        if let ref = f.ref {
            var p = ImagePayload(mime: f.mime ?? "image/png", data: "")
            p.lazy = true
            p.size = f.size
            p.width = f.width
            p.height = f.height
            p.ref = ref
            payload = p
        }
        let id = f.path.map { "p:" + $0 } ?? sizeKey(mime: f.mime, size: f.size, width: f.width, height: f.height) ?? "i:" + (f.itemID ?? f.toolUseID)
        return ViewedImage(id: id, payload: payload, path: f.path, source: ViewedImage.Source(rawValue: f.source) ?? .tool,
                           tool: f.tool, machine: f.machine)
    }

    /// Where a call's images came from, and the file or the machine it named. Only Read names a file.
    static func said(_ tool: String, _ input: JSONValue) -> (ViewedImage.Source, path: String?, machine: String?) {
        var base = tool, mcp = false
        if tool.hasPrefix("mcp__"), let r = tool.range(of: "__", options: .backwards), r.lowerBound > tool.index(tool.startIndex, offsetBy: 4) {
            base = String(tool[r.upperBound...])
            mcp = true
        }
        let machine = input["machine"]?.string.flatMap { $0.isEmpty ? nil : $0 }
        switch true {
        case base == "Read" && !mcp: return (.read, input["file_path"]?.string.flatMap { $0.isEmpty ? nil : $0 }, nil)
        case mcp && base.hasPrefix("computer_"): return (.screenshot, nil, machine)
        case mcp && base.hasPrefix("browser_"): return (.browser, nil, machine)
        case mcp: return (.mcp, nil, nil)
        default: return (.tool, nil, nil)
        }
    }

    static func isPicture(_ path: String) -> Bool { pictureExtensions.contains((path as NSString).pathExtension.lowercased()) }

    /// One file once; an image the same as one before (its bytes' size and shape: an unchanged screen looked at again)
    /// once; else the image itself.
    static func key(path: String?, payload: ImagePayload?) -> String {
        if let path { return "p:" + path }
        guard let p = payload else { return "" }
        let size = p.size ?? (p.data.isEmpty ? nil : p.data.count * 3 / 4)
        return sizeKey(mime: p.mime, size: size, width: p.width, height: p.height) ?? "d:" + p.identity
    }

    private static func sizeKey(mime: String?, size: Int?, width: Int?, height: Int?) -> String? {
        guard let size, let width, let height, size > 0, width > 0, height > 0 else { return nil }
        return "s:\(mime ?? ""):\(size):\(width)x\(height)"
    }
}
