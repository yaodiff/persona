import ImageIO
import PersonaKit
import SwiftUI

/// A file at a version, as the node keeps it for comparing (`GET …/git/blob`, hub feature `git_blob`,
/// docs/protocol/protocol.md): a path in the node's cache to read with `/file/download`, or that the version has no such file.
nonisolated struct GitBlob: Decodable, Sendable {
    var path: String?
    var size: Int64?
    var missing: Bool?
}

extension APIClient {
    /// `rev`: HEAD, INDEX, a commit hash, or a hash and ^ (its parent); `path` from the repository's root.
    func gitBlob(machine: String, cwd: String, path: String, rev: String) async throws -> GitBlob {
        try await get("/api/v1/machines/\(machine)/git/blob", query: ["cwd": cwd, "path": path, "rev": rev])
    }
}

/// Which two versions of a changed file to compare (docs/apple/ios.md「Compare」): an unstaged change is the index against the
/// working tree, a staged one HEAD against the index, a commit's file its parent against the commit.
nonisolated struct CompareVersions: Hashable, Sendable {
    /// A revision for `git/blob`, or nil for the working tree's file.
    var old: String?
    var new: String?

    static func change(staged: Bool) -> CompareVersions { staged ? CompareVersions(old: "HEAD", new: "INDEX") : CompareVersions(old: "INDEX", new: nil) }
    static func commit(_ hash: String) -> CompareVersions { CompareVersions(old: hash + "^", new: hash) }
}

/// Fetches both versions of a file and compares them: pictures as they are, documents by their text.
@MainActor @Observable
final class CompareModel {
    enum Side { case old, new }

    struct Version {
        /// Where the bytes are on the machine (the node's cache, or the working tree's file).
        var path: String
        var data: Data
    }

    let machine: MachineRef
    let cwd: String
    let root: String
    let file: String
    let versions: CompareVersions
    let kind: DocumentText.Kind
    @ObservationIgnored private let api: APIClient?
    private(set) var old: Version?
    private(set) var new: Version?
    /// A version that has no such file: the file was added (no old) or deleted (no new).
    private(set) var oldMissing = false
    private(set) var newMissing = false
    private(set) var result: TextCompare.Result?
    private(set) var error: String?
    private(set) var loading = true

    init(machine: MachineRef, cwd: String, root: String, file: String, versions: CompareVersions, kind: DocumentText.Kind, api: APIClient?) {
        self.machine = machine
        self.cwd = cwd
        self.root = root
        self.file = file
        self.versions = versions
        self.kind = kind
        self.api = api
    }

    func load() async {
        loading = true
        error = nil
        defer { loading = false }
        guard let api else {
            error = String(localized: "Not connected to the hub [App Model 93d955]")
            return
        }
        do {
            async let a = fetch(versions.old, api: api)
            async let b = fetch(versions.new, api: api)
            let (o, n) = try await (a, b)
            old = o
            new = n
            oldMissing = o == nil
            newMissing = n == nil
            guard kind != .image else { return }
            let kind = kind
            result = try await Task.detached(priority: .userInitiated) {
                let ou = try o.map { try DocumentText.units($0.data, kind: kind) } ?? []
                let nu = try n.map { try DocumentText.units($0.data, kind: kind) } ?? []
                return TextCompare.compare(ou, nu)
            }.value
        } catch {
            // A timeout or no network is said too: left unsaid, the page was blank with both buttons grey (#4170).
            if !error.isCancellation { self.error = error.shown ?? error.localizedDescription }
        }
    }

    private func fetch(_ rev: String?, api: APIClient) async throws -> Version? {
        let path: String
        if let rev {
            let blob = try await api.gitBlob(machine: machine.id, cwd: cwd, path: file, rev: rev)
            guard blob.missing != true, let p = blob.path else { return nil }
            path = p
        } else {
            path = (root as NSString).appendingPathComponent(file)
        }
        do {
            return Version(path: path, data: try await api.fileBytes(machine: machine.id, path: path, cwd: cwd))
        } catch let e as APIError where e.status == 404 && rev == nil {
            return nil // deleted from the working tree
        }
    }
}

/// Two versions side by side (pictures) or as a text diff (PDF, Word, PowerPoint, Excel), with each version a tap away.
struct CompareView: View {
    let model: CompareModel
    @State private var open: FileRef?

    var body: some View {
        Group {
            if let error = model.error {
                EmptyStateView(symbol: Theme.Icon.error, title: String(localized: "Can't Compare These Versions"), message: error,
                               action: (title: String(localized: "Retry [Components b8784c]"), run: { Task { await model.load() } }))
            } else if model.loading {
                ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity)
            } else if model.kind == .image {
                ImageCompareView(model: model)
                    .id(ObjectIdentifier(model))
            } else if let result = model.result {
                TextCompareView(result: result, added: model.oldMissing, deleted: model.newMissing)
            }
        }
        .safeAreaInset(edge: .bottom) { if !model.loading, model.error == nil { openBar } }
        .task(id: ObjectIdentifier(model)) { await model.load() }
        .onHubReconnect(model.machine.group) { if model.error != nil { Task { await model.load() } } }
        .sheet(item: $open) { ref in FileViewer(ref: ref) } // no identifier here: it would hide compare-text / compare-image
    }

    private var openBar: some View {
        HStack(spacing: Theme.Space.m) {
            Button("Open Old") { model.old.map { open = ref($0) } }
                .disabled(model.old == nil)
            Button("Open New") { model.new.map { open = ref($0) } }
                .disabled(model.new == nil)
        }
        .lineLimit(1)
        .themedButton()
        .padding(Theme.Space.s)
        .frame(maxWidth: .infinity)
    }

    private func ref(_ v: CompareModel.Version) -> FileRef {
        FileRef(group: model.machine.group, machineID: model.machine.id, path: v.path, cwd: model.cwd)
    }
}

/// A document's text diff: each change under its page / slide / sheet, the lines around it, the rest folded.
private struct TextCompareView: View {
    let result: TextCompare.Result
    let added: Bool
    let deleted: Bool

    var body: some View {
        if result.rows.isEmpty {
            EmptyStateView(symbol: Theme.Icon.document, title: String(localized: "Text Unchanged"),
                           message: String(localized: "Both versions have the same text; the change may be in formatting, images, or layout. Open each one with the buttons below."))
        } else {
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 0) {
                    Text(summary).font(Theme.Font.label).foregroundStyle(.secondary)
                        .padding(.horizontal, Theme.Space.page).padding(.vertical, Theme.Space.s)
                    ForEach(Array(result.rows.enumerated()), id: \.offset) { _, row in line(row) }
                }
            }
            .accessibilityIdentifier("compare-text")
        }
    }

    private var summary: String {
        var parts = [added ? String(localized: "New file") : deleted ? String(localized: "Deleted") : String(localized: "+\(result.added) −\(result.removed) lines")]
        if result.capped { parts.append(String(localized: "Too long; compared only the first \(TextCompare.maxLines) lines")) }
        return parts.joined(separator: " · ")
    }

    @ViewBuilder private func line(_ row: TextCompare.Row) -> some View {
        switch row {
        case .unit(let label):
            Text(label).font(Theme.Font.captionStrong).foregroundStyle(.secondary)
                .padding(.horizontal, Theme.Space.page).padding(.top, Theme.Space.m).padding(.bottom, Theme.Space.xs)
        case .folded(let n):
            Text("… \(n) unchanged lines").font(Theme.Font.label).foregroundStyle(.tertiary)
                .padding(.horizontal, Theme.Space.page).padding(.vertical, Theme.Space.xs)
        case .same(let t): text(" ", t, color: .primary, fill: .clear)
        case .added(let t): text("+", t, color: Theme.Diff.add, fill: Theme.Diff.addFill)
        case .removed(let t): text("−", t, color: Theme.Diff.del, fill: Theme.Diff.delFill)
        }
    }

    private func text(_ sign: String, _ t: String, color: Color, fill: Color) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) {
            Text(sign).font(Theme.Font.code).foregroundStyle(color)
            // A table row's cells (tab-separated in the text) shown apart: tabs did not line up between rows.
            Text(t.isEmpty ? " " : t.replacingOccurrences(of: "\t", with: "  ·  ")).font(Theme.Font.secondary).foregroundStyle(color == .primary ? Color.primary : color)
                .textSelection(.enabled)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
        .padding(.horizontal, Theme.Space.page)
        .padding(.vertical, Theme.Space.xxs)
        .background(fill)
    }
}

/// The two previews belong to this comparison; both layouts draw the same prepared pictures.
nonisolated struct ComparePictures: Sendable {
    let maxPixel: Int
    let old: UIImage?
    let new: UIImage?

    @concurrent static func prepare(old: Data?, new: Data?, maxPixel: Int) async -> Self {
        func preview(_ data: Data?) -> UIImage? {
            guard !Task.isCancelled, let data, let source = CGImageSourceCreateWithData(data as CFData, nil) else { return nil }
            let options: [CFString: Any] = [kCGImageSourceCreateThumbnailFromImageAlways: true,
                                             kCGImageSourceCreateThumbnailWithTransform: true,
                                             kCGImageSourceShouldCacheImmediately: true,
                                             kCGImageSourceThumbnailMaxPixelSize: maxPixel]
            // Like UIImage(data:), compare the first frame, including in multi-image HEIC files.
            return CGImageSourceCreateThumbnailAtIndex(source, 0, options as CFDictionary).map { UIImage(cgImage: $0) }
        }
        return Self(maxPixel: maxPixel, old: preview(old), new: preview(new))
    }
}

/// Two pictures: side by side, or the new over the old with a slider between them.
private struct ImageCompareView: View {
    let model: CompareModel
    @Environment(\.displayScale) private var displayScale
    @State private var pictures: ComparePictures?
    @State private var slider = false
    @State private var split = 0.5

    var body: some View {
        GeometryReader { area in
            let pixels = Int((max(area.size.width, area.size.height) * displayScale).rounded(.up))
            Group {
                if let pictures { comparison(pictures) } else { ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity) }
            }
            .task(id: pixels) {
                guard pixels > (pictures?.maxPixel ?? 0) else { return }
                let prepared = await ComparePictures.prepare(old: model.old?.data, new: model.new?.data, maxPixel: pixels)
                if !Task.isCancelled { pictures = prepared }
            }
        }
    }

    private func comparison(_ pictures: ComparePictures) -> some View {
        VStack(spacing: Theme.Space.m) {
            SegmentedChoice("", [(false, String(localized: "Side by Side")), (true, String(localized: "Slider"))], selection: $slider)
                .padding(.horizontal, Theme.Space.page)
            if slider, let o = pictures.old, let n = pictures.new {
                GeometryReader { g in
                    ZStack(alignment: .leading) {
                        Image(uiImage: n).resizable().scaledToFit().frame(width: g.size.width, height: g.size.height)
                        Image(uiImage: o).resizable().scaledToFit().frame(width: g.size.width, height: g.size.height)
                            .mask(alignment: .leading) { Rectangle().frame(width: g.size.width * split) }
                        Rectangle().fill(Theme.accent).frame(width: Theme.Size.rule).offset(x: g.size.width * split)
                    }
                }
                .padding(.horizontal, Theme.Space.page)
                Slider(value: $split, in: 0...1).padding(.horizontal, Theme.Space.page)
                    .accessibilityLabel("Divider between old and new")
                HStack { Text("Old"); Spacer(); Text("New") }.font(Theme.Font.label).foregroundStyle(.secondary)
                    .padding(.horizontal, Theme.Space.page)
            } else {
                HStack(alignment: .top, spacing: Theme.Space.m) {
                    side(String(localized: "Old"), model.old, picture: pictures.old, missing: model.oldMissing ? String(localized: "Not in this version yet") : nil)
                    side(String(localized: "New"), model.new, picture: pictures.new, missing: model.newMissing ? String(localized: "Deleted in this version") : nil)
                }
                .padding(.horizontal, Theme.Space.page)
            }
            Spacer(minLength: 0)
        }
        .padding(.top, Theme.Space.m)
        .accessibilityIdentifier("compare-image")
    }

    private func side(_ title: String, _ v: CompareModel.Version?, picture: UIImage?, missing: String?) -> some View {
        VStack(spacing: Theme.Space.s) {
            Text(title).font(Theme.Font.captionStrong).foregroundStyle(.secondary)
            if let picture {
                Image(uiImage: picture).resizable().scaledToFit()
                    .clipShape(.rect(cornerRadius: Theme.Radius.control, style: .continuous))
                if let v { Text(Int64(v.data.count).storageText).font(Theme.Font.label).foregroundStyle(.tertiary) }
            } else {
                Text(missing ?? String(localized: "Can't read this image")).font(Theme.Font.caption).foregroundStyle(.secondary)
                    .frame(maxWidth: .infinity, minHeight: Theme.Size.box120)
                    .background(Theme.Fill.well, in: .rect(cornerRadius: Theme.Radius.control, style: .continuous))
            }
        }
        .frame(maxWidth: .infinity)
    }
}

/// `CompareView` for one file's two versions, its model made once per file and pair of versions.
struct CompareFileView: View {
    let machine: MachineRef
    let cwd: String
    let root: String
    let file: String
    let versions: CompareVersions
    let kind: DocumentText.Kind
    @Environment(AppModel.self) private var app
    @State private var model: CompareModel?

    var body: some View {
        Group {
            if let model { CompareView(model: model) } else { ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity) }
        }
        .task(id: versions) {
            model = CompareModel(machine: machine, cwd: cwd, root: root, file: file, versions: versions, kind: kind, api: app.api(machine.group))
        }
    }
}
