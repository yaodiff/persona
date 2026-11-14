import PhotosUI
import SwiftUI

/// The Files tab: a folder of the computer as a list you walk through, with git change marks; tap a file to read it.
/// It starts at the session's folder (the machine page: the home) and goes anywhere (#5827): the path bar names every
/// folder from the home or `/`, with Back, Forward, Up and the home; Find looks for names under the folder shown; Info
/// tells what an item is and, for a symlink, where it leads. Files go into the folder shown from this device (Upload:
/// the Files app or the photo library, or dropped on it on an iPad) and out of it (Save to Files); a long press renames,
/// moves or moves to the trash (Undo in the toast), and Select does it for several. Folders outside the home are
/// read-only. docs/protocol/protocol.md "Changing files" and "File details and links".
struct FilesPane: View {
    @Environment(AppModel.self) private var model
    let machineID: String
    /// The group of hubs the machine is in (docs/protocol/multi-hub.md).
    var group: String = ""
    let cwd: String
    var git: GitStatus?
    var key: SessionKey? = nil
    /// Reads the folder's Git status again (its marks come from the chat's): live refresh when it changes (#3089).
    var reloadGit: (() async -> Void)? = nil
    /// A file to show in its folder, highlighted (the preview's Show in Files Page from the chat); set back to nil once shown.
    var reveal: Binding<String?> = .constant(nil)
    @State private var nav: FilesNavigation?
    @State private var listing: FileListing?
    @State private var listed = "" // the path `listing` belongs to (as asked; the listing's own path is the shown one)
    /// Folders listed lately, shown at once on Back / Forward while they are read again.
    @State private var recent: [String: FileListing] = [:]
    @State private var error: String?
    @State private var file: FileRef?
    @State private var info: InfoTarget?
    /// The item whose permissions are open (#5831).
    @State private var permissions: InfoTarget?
    /// A terminal opened in a folder (#5832), shown over the page.
    @State private var terminal: OpenedTerminal?

    private struct OpenedTerminal: Identifiable {
        let key: SessionKey
        var id: String { key.sessionID }
    }
    /// The item to mark after opening a folder (a found file, Show in Files Page, a link's real location).
    @State private var highlight: String?
    @AppStorage("files.showHidden") private var showHidden = false
    @State private var transfers: FolderTransfers
    @State private var pickingFiles = false
    @State private var pickingPhotos = false
    @State private var photoPicks: [PhotosPickerItem] = []
    @State private var edits = FolderEdits()
    @State private var selecting = false
    @State private var selection: Set<String> = [] // names in the folder shown
    @State private var renaming: FileEntry?
    @State private var newName = ""
    @State private var trashing: [String] = [] // names waiting for the confirmation
    @State private var moving: [String] = [] // names the folder picker is open for
    @State private var copyingPick = false // the picker copies them (Copy to…, #5830)
    @State private var dropping = false
    @State private var query = ""
    @FocusState private var findFocused: Bool
    @State private var finder = FilesFinder()

    init(machineID: String, group: String = "", cwd: String, git: GitStatus? = nil, key: SessionKey? = nil,
         reloadGit: (() async -> Void)? = nil, reveal: Binding<String?> = .constant(nil), transfers: FolderTransfers? = nil) {
        self.machineID = machineID
        self.group = group
        self.cwd = cwd
        self.git = git
        self.key = key
        self.reloadGit = reloadGit
        self.reveal = reveal
        let transfers = transfers ?? FolderTransfers()
        transfers.session = key
        _transfers = State(initialValue: transfers)
    }

    /// New Folder / New Text File waiting for a name (#5828).
    @State private var naming: NewItem?
    @State private var newItemName = ""

    private enum NewItem: Identifiable {
        case folder, text
        var id: Self { self }
    }

    private struct InfoTarget: Identifiable {
        var path: String
        var entry: FileEntry?
        var id: String { path }
    }

    /// The folder asked for.
    private var path: String { nav?.current ?? cwd }
    /// The folder as the computer named it (absolute) once listed, else as asked.
    private var shown: String { listed == path ? listing?.path ?? path : path }
    private var machine: MachineRef { MachineRef(group: group, id: machineID) }
    private var home: String? { model.machine(machine)?.home }
    private var canEdit: Bool { FolderEdits.available(model, group: group) }
    /// The hub can make folders, write text files, zip (hello `file_mkdir`, `file_write`, `file_zip`; an unknown list
    /// offers them and lets the answer tell).
    /// Permissions can be changed (#5831): not on a Windows computer, where the hub has `file_chmod` and the folder takes changes.
    private var canChangeMode: Bool {
        writable && hubHas("file_chmod") && model.machine(machine)?.os?.lowercased() != "windows"
    }
    private func hubHas(_ feature: String) -> Bool {
        let f = model.features(group)
        return f.isEmpty || f.contains(feature)
    }
    private var writable: Bool { canEdit && FilesNavigation.writable(shown, home: home) }
    /// The session's folder (marked in the path bar); the machine page's home is no session's.
    private var sessionFolder: String? { cwd.hasPrefix("/") ? (cwd as NSString).standardizingPath : nil }
    private func full(_ name: String) -> String { (shown as NSString).appendingPathComponent(name) }

    var body: some View {
        VStack(spacing: 0) {
            chrome.accessibilityElement(children: .contain).accessibilityIdentifier("files.pane")
            ScrollViewReader { scroller in
                List {
                    if !query.trimmingCharacters(in: .whitespaces).isEmpty {
                        findResults
                    } else if let listing, listed == path || recent[path] != nil {
                        let entries = listing.entries.filter { showHidden || !$0.name.hasPrefix(".") }
                        if entries.isEmpty {
                            Text(listing.entries.isEmpty ? String(localized: "Empty folder") : String(localized: "Only hidden files")).foregroundStyle(.secondary)
                        }
                        ForEach(entries) { e in entryRow(e).id(e.name) }
                        if listing.truncated {
                            Text("Too many files; showing the first 2,000").font(Theme.Font.label).foregroundStyle(.secondary)
                        }
                    } else if let error {
                        ContentUnavailableView("Can't Open This Folder", systemImage: Theme.Icon.error, description: Text(error))
                            .listRowBackground(Color.clear)
                    } else {
                        HStack { Spacer(); ProgressView(); Spacer() }.listRowBackground(Color.clear)
                    }
                }
                .listStyle(.plain)
                .themedPage()
                .scrollDismissesKeyboard(.immediately)
                .refreshable { await load() }
                .onChange(of: highlight) { _, name in
                    guard let name else { return }
                    withAnimation(Theme.Motion.standard) { scroller.scrollTo(name, anchor: .center) }
                }
            }
            // Files dragged in from another app (an iPad beside Files or Photos) upload into the folder shown.
            .dropDestination(for: PickedLibraryFile.self) { files, _ in
                guard writable else { return false }
                upload(files.map(\.url), owned: true)
                return !files.isEmpty
            } isTargeted: { dropping = $0 && writable }
            .overlay {
                if dropping {
                    RoundedRectangle(cornerRadius: Theme.Radius.card).strokeBorder(Theme.accent, lineWidth: 2)
                        .padding(Theme.Space.s)
                        .allowsHitTesting(false)
                        .accessibilityIdentifier("files.drop")
                }
            }
            .safeAreaInset(edge: .bottom) {
                VStack(spacing: Theme.Space.s) {
                    if !transfers.visibleItems.isEmpty {
                        FolderTransfersBar(transfers: transfers)
                    }
                    if selecting { selectionBar }
                }
                .padding(.horizontal, Theme.Space.l)
                .padding(.bottom, Theme.Space.s)
            }
        }
        .task(id: path) { await load() }
        .onChange(of: path) {
            selection = []
            selecting = false
            query = ""
        }
        .onChange(of: query) { _, q in finder.find(q, in: shown, machine: machine, model: model) }
        .onChange(of: reveal.wrappedValue, initial: true) { _, target in
            guard let target else { return }
            reveal.wrappedValue = nil
            show(target, dir: false)
        }
        // Live refresh (#3089): the folder shown, and the Git marks when the chat can read them again.
        .fsLive(group: group, machineID: machineID, cwd: cwd, paths: [path], git: reloadGit != nil) { change in
            if change.dirs.contains(path) || change.dirs.contains(shown) { Task { await load() } }
            if change.git, let reloadGit { Task { await reloadGit() } }
        }
        .sheet(item: $file) { ref in
            FileViewer(ref: ref, key: key)
                .environment(\.showInFiles) { target in
                    file = nil
                    show(target, dir: false)
                }
        }
        #if os(iOS)
        .fullScreenCover(item: $terminal) { t in TerminalCover(key: t.key).presentationBackground(.clear) }
        #else
        .sheet(item: $terminal) { t in TerminalCover(key: t.key) }
        #endif
        .sheet(item: $permissions) { t in
            FilePermissionsSheet(machine: machine, path: t.path, cwd: cwd) { _ in Task { await load() } }
                .presentationDetents([.medium, .large])
        }
        .sheet(item: $info) { t in
            FileInfoSheet(machine: machine, path: t.path, cwd: cwd, entry: t.entry) { real, dir in show(real, dir: dir) }
                .presentationDetents([.large]) // all of it at once: the permissions under the times (the approved mockup)
        }
        .fileUploadPicker(isPresented: $pickingFiles) { upload($0) }
        .photosPicker(isPresented: $pickingPhotos, selection: $photoPicks, maxSelectionCount: 20, matching: .any(of: [.images, .videos]))
        .onChange(of: photoPicks) { _, picks in
            guard !picks.isEmpty else { return }
            photoPicks = []
            Task {
                let got = await PickedLibraryFile.load(picks)
                if got.failed > 0 { model.toast = Toast(String(localized: "\(got.failed) items could not be read"), style: .error) }
                upload(got.urls, owned: true)
            }
        }
        // The transfers' "already exists" questions, one after another in the order asked (#6113).
        .background { FolderTransferQuestion(transfers: transfers) }
        .fileMover(isPresented: Binding(get: { transfers.saving != nil }, set: { if !$0 { transfers.saved() } }),
                   file: transfers.saving) { _ in transfers.saved() }
        .alert("Rename [Chats View 0d0cba]", isPresented: Binding(get: { renaming != nil }, set: { if !$0 { renaming = nil } })) {
            TextField("Name [Personal Agent Prefs d44e9b]", text: $newName)
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
                .accessibilityIdentifier("files.rename.field")
            Button("Rename [Chats View 0d0cba]") {
                if let e = renaming { edits.rename(full(e.name), to: newName, cwd: cwd, machine: machine, model: model) { Task { await load() } } }
                renaming = nil
            }
            Button("Cancel", role: .cancel) { renaming = nil }
        }
        .alert(naming == .text ? String(localized: "New Text File") : String(localized: "New Folder"),
               isPresented: Binding(get: { naming != nil }, set: { if !$0 { naming = nil } })) {
            TextField("Name [Personal Agent Prefs d44e9b]", text: $newItemName)
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
                .accessibilityIdentifier("files.new.field")
            Button("Create") {
                if let kind = naming { create(kind, name: newItemName) }
                naming = nil
            }
            Button("Cancel", role: .cancel) { naming = nil }
        } message: {
            Text(naming == .text ? String(localized: "In “\(FolderEdits.folderName(FilesNavigation.tilde(shown, home: home)))”. It opens once it’s made.")
                                 : String(localized: "In “\(FolderEdits.folderName(FilesNavigation.tilde(shown, home: home)))”."))
        }
        .alert(trashTitle, isPresented: Binding(get: { !trashing.isEmpty }, set: { if !$0 { trashing = [] } })) {
            Button("Move to Trash", role: .destructive) {
                let names = trashing
                trashing = []
                selecting = false
                selection = []
                edits.trash(names.map(full), cwd: cwd, machine: machine, model: model) { Task { await load() } }
            }
            Button("Cancel", role: .cancel) { trashing = [] }
        } message: {
            Text("It goes to the computer’s Trash. You can undo right after, or put it back from the Trash there.")
        }
        .alert(String(localized: "“\(edits.conflictName)” already exists"),
               isPresented: Binding(get: { edits.conflict != nil }, set: { if !$0 { edits.answer(nil) } })) {
            Button("Replace") { edits.answer("replace") }
            Button("Keep Both") { edits.answer("keep") }
            Button("Cancel", role: .cancel) { edits.answer(nil) }
        } message: {
            Text("The folder already has an item with that name. Replace moves the one there to the Trash; Keep Both names this one “name 2”.")
        }
        .sheet(isPresented: Binding(get: { !moving.isEmpty }, set: { if !$0 { moving = [] } })) {
            FolderPicker(machineID: machineID, group: group, cwd: cwd, root: pickerRoot, at: shown, moving: moving.map(full), copying: copyingPick) { dir in
                let names = moving
                moving = []
                selecting = false
                selection = []
                if copyingPick {
                    transfers.copy(names.map(full), into: dir, cwd: cwd, machine: machine, model: model) { Task { await load() } }
                } else {
                    edits.move(names.map(full), into: dir, cwd: cwd, machine: machine, model: model) { Task { await load() } }
                }
            }
        }
        // Control Center, a call or the app switcher change the scene, not the page: they only pause progress polling.
        // Leaving the page is what answers questions, stops downloads and drops a file waiting for Save to Files (#6113).
        .onTabVisibility(sceneAware: true, appear: { transfers.resumePolling() }, disappear: { transfers.pausePolling() })
        .onTabVisibility(appear: {}, disappear: { transfers.stop() })
        #if DEBUG
        .task { // `-showFilesDrop YES`: the folder looks as it does with files dragged over it (iPad), for screenshots
            if UserDefaults.standard.bool(forKey: "showFilesDrop") { dropping = true }
        }
        .task { // `-showFolderUpload <name>`: a made-up 24 MB file of that name uploading here (an existing name asks first), for screenshots
            if let name = UserDefaults.standard.string(forKey: "showFolderUpload") {
                try? await Task.sleep(for: .seconds(2))
                let dir = FileManager.default.temporaryDirectory.appending(path: "uploads/" + UUID().uuidString)
                try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
                let url = dir.appending(path: name)
                try? Data(count: 24 << 20).write(to: url)
                upload([url], owned: true)
            }
        }
        .task { // `-showFilesPath <path>`, `-showFilesFind <words>`, `-showFilesInfo <name>`: the page there, for screenshots
            let d = UserDefaults.standard
            if let p = d.string(forKey: "showFilesPath") { go(p) }
            try? await Task.sleep(for: .seconds(2))
            if let q = d.string(forKey: "showFilesFind") { query = q }
            if let n = d.string(forKey: "showFilesInfo") { info = InfoTarget(path: full(n), entry: listing?.entries.first { $0.name == n }) }
        }
        #endif
        #if os(macOS)
        .pageToolbar { MacRefreshItem(id: [machineID, cwd]) { await load() } } // no pull to refresh on the Mac
        #endif
    }

    // MARK: Path bar and tools

    private var chrome: some View {
        VStack(spacing: Theme.Space.s) {
            HStack(spacing: Theme.Space.s) {
                circle(Theme.Icon.back, String(localized: "Back [Task Sheets 572cf4]"), id: "files.back", enabled: nav?.canGoBack == true) { goBack() }
                circle(Theme.Icon.forward, String(localized: "Forward"), id: "files.forward", enabled: nav?.canGoForward == true) { goForward() }
                circle(Theme.Icon.parentFolder, String(localized: "Parent Folder"), id: "files.up", enabled: FilesNavigation.parent(of: shown) != nil) {
                    if let up = FilesNavigation.parent(of: shown) { go(up) }
                }
                crumbs
            }
            HStack(spacing: Theme.Space.s) {
                if selecting {
                    Text(selection.isEmpty ? String(localized: "Select Items") : String(localized: "\(selection.count) Selected"))
                        .font(Theme.Font.secondaryMedium)
                        .lineLimit(1)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .accessibilityIdentifier("files.select.count")
                } else {
                    findField
                }
                if selecting {
                    Button {
                        let names = (listing?.entries ?? []).filter { showHidden || !$0.name.hasPrefix(".") }.map(\.name)
                        selection = selection.count == names.count ? [] : Set(names)
                    } label: {
                        Text("Select All").font(Theme.Font.captionStrong).lineLimit(1)
                            .padding(.horizontal, Theme.Space.m)
                            .scaledFrame(height: Theme.Size.wellS)
                    }
                    .themedButton()
                    .clipShape(.capsule)
                    .accessibilityIdentifier("files.select.all")
                    Button {
                        selecting = false
                        selection = []
                    } label: {
                        Text("Done [Extension System Presentation c0b3fb]").font(Theme.Font.captionStrong).lineLimit(1)
                            .padding(.horizontal, Theme.Space.m)
                            .scaledFrame(height: Theme.Size.wellS)
                    }
                    .themedButton()
                    .clipShape(.capsule)
                    .accessibilityIdentifier("files.select.done")
                } else {
                    if writable {
                        // ＋ (#5828): new folder and text file, then the uploads
                        Menu {
                            Section {
                                if hubHas("file_mkdir") {
                                    Button { startNew(.folder) } label: { Label("New Folder", systemImage: Theme.Icon.newFolder) }
                                }
                                if hubHas("file_write") {
                                    Button { startNew(.text) } label: { Label("New Text File", systemImage: Theme.Icon.newFile) }
                                }
                            }
                            Section {
                                Button { pickingPhotos = true } label: { Label("Upload from Photos", systemImage: Theme.Icon.photos) }
                                Button { pickingFiles = true } label: { Label("Upload from Files", systemImage: Theme.Icon.upload) }
                            }
                        } label: { tool(Theme.Icon.add) }
                        #if os(macOS)
                        .menuStyle(.button)
                        .menuIndicator(.hidden)
                        #endif
                        .themedButton()
                        .clipShape(.circle)
                        .accessibilityLabel("New or Upload")
                        .accessibilityIdentifier("files.upload")
                    }
                    if canEdit, listing?.entries.isEmpty == false {
                        Button {
                            selection = []
                            selecting = true
                        } label: { tool(Theme.Icon.success) }
                        .themedButton()
                        .clipShape(.circle)
                        .accessibilityLabel("Select [Translation c11330]")
                        .accessibilityIdentifier("files.select")
                    }
                    moreMenu
                }
            }
        }
        .padding(.horizontal, Theme.Space.l)
        .padding(.vertical, Theme.Space.sm)
    }

    private func tool(_ symbol: String) -> some View {
        ThemedIcon(symbol).font(Theme.Font.captionStrong).scaledFrame(width: Theme.Size.wellS, height: Theme.Size.wellS)
    }

    private func circle(_ symbol: String, _ label: String, id: String, enabled: Bool, action: @escaping () -> Void) -> some View {
        Button {
            Haptics.tap()
            action()
        } label: { tool(symbol) }
        .themedButton()
        .clipShape(.circle)
        .disabled(!enabled)
        .accessibilityLabel(label)
        .accessibilityIdentifier(id)
    }

    private var crumbs: some View {
        let segments = FilesNavigation.segments(shown, home: home)
        return ScrollViewReader { proxy in
            ScrollView(.horizontal, showsIndicators: false) {
                HStack(spacing: Theme.Space.xxs) {
                    ForEach(Array(segments.enumerated()), id: \.element.path) { i, s in
                        if i > 0 { ThemedIcon(Theme.Icon.pathSeparator).font(Theme.Font.label).foregroundStyle(.tertiary) }
                        crumb(s, current: i == segments.count - 1).id(i)
                    }
                    if !FilesNavigation.writable(shown, home: home), listing != nil {
                        Chip(text: String(localized: "Read Only"), symbol: Theme.Icon.locked, compact: true)
                            .padding(.leading, Theme.Space.xs)
                            .accessibilityIdentifier("files.readonly")
                    }
                }
                .padding(.vertical, Theme.Space.xxs)
            }
            .onChange(of: segments.count, initial: true) { _, n in proxy.scrollTo(n - 1, anchor: .trailing) }
            .onChange(of: shown) { proxy.scrollTo(segments.count - 1, anchor: .trailing) }
        }
        .contextMenu {
            Button { copy(shown) } label: { Label("Copy Path", systemImage: Theme.Icon.copy) }
        }
    }

    private func crumb(_ s: FilesNavigation.Segment, current: Bool) -> some View {
        let isSession = sessionFolder == s.path
        return Button {
            guard !current else { return }
            Haptics.tap()
            go(s.path)
        } label: {
            HStack(spacing: Theme.Space.xs) {
                if s.home {
                    ThemedIcon(Theme.Icon.home).font(Theme.Font.captionStrong)
                } else {
                    Text(s.title).lineLimit(1)
                }
                if isSession { Chip(text: String(localized: "Session"), look: .tint(Theme.accent), compact: true) }
            }
            .font(current ? Theme.Font.captionStrong : Theme.Font.caption)
            .foregroundStyle(current ? Color.primary : Color.secondary)
            // only the current folder sits on a chip: the others keep a hair of room, so the path from the home fits
            // beside the buttons on a phone
            .padding(.horizontal, current ? Theme.Space.s : Theme.Space.xxs)
            .padding(.vertical, Theme.Space.xs)
            .background(current ? Theme.Fill.chip : .clear, in: .capsule)
        }
        .buttonStyle(.plain)
        .accessibilityLabel(s.home ? String(localized: "Home Folder") : s.title)
        .accessibilityIdentifier(s.home ? "files.home" : "files.crumb." + s.title)
    }

    private var findField: some View {
        HStack(spacing: Theme.Space.xs) {
            ThemedIcon(Theme.Icon.search).font(Theme.Font.caption).foregroundStyle(.secondary)
            TextField(String(localized: "Find in “\(FolderEdits.folderName(FilesNavigation.tilde(shown, home: home)))”"), text: $query)
                .font(Theme.Font.secondary)
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
                .submitLabel(.search)
                .focused($findFocused)
                .accessibilityIdentifier("files.find")
            if !query.isEmpty {
                Button {
                    query = ""
                    findFocused = false
                } label: { ThemedIcon(Theme.Icon.clear).font(Theme.Font.caption).foregroundStyle(.tertiary) }
                .buttonStyle(.plain)
                .accessibilityLabel("Clear [Files Find]")
            }
        }
        .padding(.horizontal, Theme.Space.m)
        .scaledFrame(height: Theme.Size.wellS)
        .background(Theme.Fill.chip, in: .capsule)
        .frame(maxWidth: .infinity)
    }

    private var moreMenu: some View {
        Menu {
            Section {
                if hubHas("pane_cwd") {
                    Button { openTerminal(in: shown) } label: { Label("Open Terminal Here", systemImage: Theme.Icon.terminal) }
                        .accessibilityIdentifier("files.terminal")
                }
                Button { info = InfoTarget(path: shown, entry: nil) } label: { Label("Info [File Info]", systemImage: Theme.Icon.info) }
                Button { copy(shown) } label: { Label("Copy Path", systemImage: Theme.Icon.copy) }
            }
            Section {
                Toggle(isOn: $showHidden) { Label("Show Hidden Files", systemImage: Theme.Icon.view) }
                if let sessionFolder, shown != sessionFolder {
                    Button { go(sessionFolder) } label: { Label("Back to Session Folder", systemImage: Theme.Icon.folder) }
                }
            }
        } label: { tool(Theme.Icon.more) }
        #if os(macOS)
        // A Mac menu draws as a pop-up button with its own arrow: the ⋯ was squeezed to two dots inside the circle.
        .menuStyle(.button)
        .menuIndicator(.hidden)
        #endif
        .themedButton()
        .clipShape(.circle)
        .accessibilityLabel("More")
        .accessibilityIdentifier("files.more")
    }

    private var trashTitle: String {
        trashing.count == 1 ? String(localized: "Move “\(trashing[0])” to the Trash?") : String(localized: "Move \(trashing.count) items to the Trash?")
    }

    /// Under the list while selecting (#5830): Download, Copy to, Move to, Compress and Move to Trash for all of it.
    private var selectionBar: some View {
        let none = selection.isEmpty || edits.busy
        return HStack(spacing: Theme.Space.xs) {
            barButton(Theme.Icon.save, String(localized: "Download"), id: "files.select.download", disabled: selection.isEmpty || !canDownloadSelection) {
                downloadSelection()
            }
            if hubHas("file_copy") {
                barButton(Theme.Icon.copy, String(localized: "Copy To [Files]"), id: "files.select.copy", disabled: none) {
                    copyingPick = true
                    moving = Array(selection).sorted()
                }
            }
            barButton(Theme.Icon.folder, String(localized: "Move To [Files]"), id: "files.select.move", disabled: none || !writable) {
                copyingPick = false
                moving = Array(selection).sorted()
            }
            if hubHas("file_zip") {
                barButton(Theme.Icon.fileArchive, String(localized: "Compress"), id: "files.select.compress", disabled: none || !writable) {
                    compress(Array(selection).sorted())
                }
            }
            barButton(Theme.Icon.delete, String(localized: "Trash [Files]"), id: "files.select.trash", disabled: none || !writable, role: .destructive) {
                trashing = Array(selection).sorted()
            }
        }
        .padding(.horizontal, Theme.Space.s)
        .padding(.vertical, Theme.Space.s)
        .surface(in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
    }

    /// One action of the selection bar: its mark over its word, the whole cell a target.
    private func barButton(_ symbol: String, _ title: String, id: String, disabled: Bool, role: ButtonRole? = nil,
                           action: @escaping () -> Void) -> some View {
        Button(role: role, action: action) {
            VStack(spacing: Theme.Space.xxs) {
                ThemedIcon(symbol).font(Theme.Font.body)
                Text(title).font(Theme.Font.label).lineLimit(1).minimumScaleFactor(0.8)
            }
            .foregroundStyle(role == .destructive ? Theme.danger : Theme.accent)
            .frame(maxWidth: .infinity)
            .tapTargetHeight()
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .disabled(disabled)
        .opacity(disabled ? 0.4 : 1)
        .accessibilityIdentifier(id)
    }

    /// Zips names in the folder shown, the zip marked once it is there.
    private func compress(_ names: [String]) {
        selecting = false
        selection = []
        transfers.compress(names.map(full), cwd: cwd, machine: machine, model: model) { made in
            Task {
                await load()
                show(full(made), dir: false)
            }
        }
    }

    // MARK: Rows

    private func entryRow(_ e: FileEntry) -> some View {
        let full = full(e.name)
        let mark = gitMark(full, dir: e.dir)
        let marked = highlight == e.name
        return Button {
            if selecting {
                Haptics.tap()
                if selection.contains(e.name) { selection.remove(e.name) } else { selection.insert(e.name) }
            } else {
                open(e, at: full)
            }
        } label: {
            HStack(spacing: Theme.Space.m) {
                if selecting {
                    ThemedIcon(selection.contains(e.name) ? Theme.Icon.successFilled : Theme.Icon.unchecked)
                        .font(Theme.Font.body)
                        .foregroundStyle(selection.contains(e.name) ? Theme.accent : Color.secondary)
                        .scaledFrame(width: Theme.Size.icon)
                }
                Group {
                    if e.dir {
                        ThemedIcon(Theme.Icon.folderFilled)
                            .font(Theme.Font.body)
                            .foregroundStyle(Theme.accent)
                            .scaledFrame(width: Theme.Size.icon)
                    } else {
                        FileThumbnailIcon(ref: FileRef(group: group, machineID: machineID, path: full, cwd: cwd), size: e.size, modified: e.mtime,
                                          symbol: FileViewer.symbol(for: e.name))
                    }
                }
                .overlay(alignment: .bottomLeading) { if e.symlink == true { linkBadge(broken: e.linkBroken == true) } }
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Text(e.name).font(Theme.Font.secondary).foregroundStyle(e.name.hasPrefix(".") ? Color.secondary : Color.primary).lineLimit(1)
                    HStack(spacing: Theme.Space.sm) {
                        if e.linkBroken == true {
                            Text("Link broken · \(e.linkTarget ?? "") isn’t there").foregroundStyle(Theme.danger)
                        } else if e.symlink == true, let target = e.linkTarget {
                            Text("Link → \(target)")
                        } else {
                            if !e.dir { Text(e.size.storageText) }
                            if let m = e.mtime { Text(Fmt.relative(m)) }
                        }
                    }
                    .font(Theme.Font.label)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                    .truncationMode(.middle)
                }
                Spacer(minLength: Theme.Space.s)
                if let mark {
                    if e.dir {
                        Circle().fill(GitBadge(letter: mark).color).scaledFrame(width: Theme.Size.dotS, height: Theme.Size.dotS)
                    } else {
                        GitBadge(letter: mark)
                    }
                }
                if e.dir && !selecting { ThemedIcon(Theme.Icon.disclosure).font(Theme.Font.labelStrong).foregroundStyle(.tertiary) }
            }
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .listRowBackground(marked ? Theme.accent.opacity(0.14) : nil)
        .accessibilityAddTraits(selecting && selection.contains(e.name) ? .isSelected : [])
        .accessibilityIdentifier("files.row." + e.name)
        .contextMenu {
            if !e.dir && e.linkBroken != true {
                Button { save(e, full) } label: { Label("Save to Files", systemImage: Theme.Icon.save) }
            } else if e.dir, hubHas("file_zip") {
                Button { transfers.downloadZip([full], name: e.name + ".zip", cwd: cwd, machine: machine, model: model) } label: {
                    Label("Download as Zip", systemImage: Theme.Icon.save)
                }
            }
            if e.dir, e.linkBroken != true, hubHas("pane_cwd") {
                Button { openTerminal(in: full) } label: { Label("Open Terminal Here", systemImage: Theme.Icon.terminal) }
            }
            Button { copy(full) } label: { Label("Copy Path", systemImage: Theme.Icon.copy) }
            Button { info = InfoTarget(path: full, entry: e) } label: { Label("Info [File Info]", systemImage: Theme.Icon.info) }
            if canChangeMode, e.symlink != true {
                Button { permissions = InfoTarget(path: full, entry: e) } label: { Label("Permissions…", systemImage: Theme.Icon.permissions) }
            }
            if writable || hubHas("file_copy") {
                Divider()
                if writable { Button { startRename(e) } label: { Label("Rename [Chats View 0d0cba]", systemImage: Theme.Icon.edit) } }
                if hubHas("file_copy") {
                    Button { copyingPick = true; moving = [e.name] } label: { Label("Copy To…", systemImage: Theme.Icon.copy) }
                }
                if writable {
                    Button { copyingPick = false; moving = [e.name] } label: { Label("Move", systemImage: Theme.Icon.folder) }
                    if hubHas("file_copy") {
                        Button { duplicate([e.name]) } label: { Label("Duplicate", systemImage: Theme.Icon.duplicate) }
                    }
                    if hubHas("file_zip") {
                        Button { compress([e.name]) } label: { Label("Compress", systemImage: Theme.Icon.fileArchive) }
                    }
                }
            }
            if writable {
                Divider()
                Button(role: .destructive) { trashing = [e.name] } label: { Label("Move to Trash", systemImage: Theme.Icon.delete) }
            }
        }
        .accessibilityActions {
            if !e.dir { Button("Save to Files") { save(e, full) } }
            Button("Info [File Info]") { info = InfoTarget(path: full, entry: e) }
            if writable {
                Button("Rename [Chats View 0d0cba]") { startRename(e) }
                Button("Move") { copyingPick = false; moving = [e.name] }
                if hubHas("file_copy") { Button("Duplicate") { duplicate([e.name]) } }
                if hubHas("file_zip") { Button("Compress") { compress([e.name]) } }
                Button("Move to Trash") { trashing = [e.name] }
            }
        }
    }

    /// The arrow on a symlink's icon; a broken link's is the danger mark.
    private func linkBadge(broken: Bool) -> some View {
        ThemedIcon(broken ? Theme.Icon.error : Theme.Icon.symlink)
            .font(Theme.Font.labelHeavy)
            .foregroundStyle(broken ? Theme.danger : Theme.accent)
            .padding(Theme.Space.xxs)
            .background(Circle().fill(.background))
            .offset(x: -Theme.Space.xs, y: Theme.Space.xs)
            .accessibilityLabel(broken ? String(localized: "Broken link") : String(localized: "Link"))
    }

    @ViewBuilder private var findResults: some View {
        switch finder.state {
        case .idle, .loading:
            HStack { Spacer(); ProgressView(); Spacer() }.listRowBackground(Color.clear)
        case .failed(let message):
            ContentUnavailableView("Can't Search This Folder", systemImage: Theme.Icon.error, description: Text(message))
                .listRowBackground(Color.clear)
        case .ready(let hits, let truncated):
            Text(String(localized: "In “\(FolderEdits.folderName(FilesNavigation.tilde(finder.root, home: home)))” and its folders · \(hits.count) found"))
                .font(Theme.Font.label).foregroundStyle(.secondary)
                .accessibilityIdentifier("files.find.count")
            ForEach(hits, id: \.path) { hit in
                Button { openFound(hit) } label: {
                    HStack(spacing: Theme.Space.m) {
                        ThemedIcon(hit.dir ? Theme.Icon.folderFilled : FileViewer.symbol(for: hit.name))
                            .font(Theme.Font.body)
                            .foregroundStyle(hit.dir ? Theme.accent : Color.secondary)
                            .scaledFrame(width: Theme.Size.icon)
                        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                            Text(hit.name).font(Theme.Font.secondary).lineLimit(1)
                            Text(hit.folder.isEmpty ? "./" : hit.folder + "/").font(Theme.Font.code).foregroundStyle(.secondary)
                                .lineLimit(1).truncationMode(.head)
                        }
                        Spacer(minLength: Theme.Space.s)
                        if hit.dir { ThemedIcon(Theme.Icon.disclosure).font(Theme.Font.labelStrong).foregroundStyle(.tertiary) }
                    }
                    .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .accessibilityIdentifier("files.found." + hit.path)
            }
            if truncated {
                Text("This folder has too many files to search them all").font(Theme.Font.label).foregroundStyle(.secondary)
            }
        }
    }

    // MARK: Actions

    private func go(_ p: String) {
        var n = nav ?? FilesNavigation(cwd)
        n.go(p)
        nav = n
    }

    private func goBack() {
        var n = nav ?? FilesNavigation(cwd)
        n.goBack()
        nav = n
    }

    private func goForward() {
        var n = nav ?? FilesNavigation(cwd)
        n.goForward()
        nav = n
    }

    /// A folder opens; a file opens in the preview; a broken link says what it says (Info).
    private func open(_ e: FileEntry, at full: String) {
        if e.linkBroken == true {
            info = InfoTarget(path: full, entry: e)
        } else if e.dir {
            Haptics.tap()
            go(full)
        } else {
            file = FileRef(group: group, machineID: machineID, path: full, cwd: cwd)
        }
    }

    private func openFound(_ hit: FileFind.Hit) {
        let target = (finder.root as NSString).appendingPathComponent(hit.path)
        findFocused = false
        query = ""
        if hit.dir {
            go(target)
        } else {
            file = FileRef(group: group, machineID: machineID, path: target, cwd: cwd)
        }
    }

    /// Opens target's folder with target marked (dir: target is a folder to open instead).
    private func show(_ target: String, dir: Bool) {
        if dir {
            go(target)
            return
        }
        let folder = (target as NSString).deletingLastPathComponent
        highlight = nil
        go(folder.isEmpty ? "/" : folder)
        let name = (target as NSString).lastPathComponent
        Task {
            // Once the folder is listed (a moment for one in memory).
            for _ in 0..<40 where listing?.entries.contains(where: { $0.name == name }) != true || listed != path {
                try? await Task.sleep(for: .milliseconds(100))
            }
            highlight = name
            try? await Task.sleep(for: .seconds(2.5))
            if highlight == name { withAnimation(Theme.Motion.standard) { highlight = nil } }
        }
    }

    private func startNew(_ kind: NewItem) {
        newItemName = kind == .folder ? String(localized: "untitled folder") : String(localized: "Untitled.txt")
        naming = kind
    }

    /// Makes the folder or the empty text file in the folder shown, lists it marked; a text file opens.
    private func create(_ kind: NewItem, name: String) {
        let name = name.trimmingCharacters(in: .whitespaces)
        if let problem = FolderEdits.nameProblem(name) {
            model.toast = Toast(problem, style: .error)
            return
        }
        guard let api = model.api(group) else { return }
        let dir = shown
        Task {
            do {
                switch kind {
                case .folder: _ = try await api.makeDir(machine: machineID, dir: dir, cwd: cwd, name: name)
                case .text: _ = try await api.writeFile(machine: machineID, path: (dir as NSString).appendingPathComponent(name), cwd: cwd, text: "", create: true)
                }
                Haptics.success()
                await load()
                show((dir as NSString).appendingPathComponent(name), dir: false)
                if kind == .text { file = FileRef(group: group, machineID: machineID, path: (dir as NSString).appendingPathComponent(name), cwd: cwd) }
            } catch {
                if !error.isCancellation { model.toast = Toast(FolderEdits.message(error), style: .error) }
            }
        }
    }

    /// One file as it is; several, or a folder, zipped on the computer first (#5828).
    private var canDownloadSelection: Bool {
        let picked = (listing?.entries ?? []).filter { selection.contains($0.name) }
        return picked.count == 1 && !picked[0].dir ? picked[0].linkBroken != true : hubHas("file_zip")
    }

    private func downloadSelection() {
        let picked = (listing?.entries ?? []).filter { selection.contains($0.name) }
        guard !picked.isEmpty else { return }
        if picked.count == 1, !picked[0].dir {
            save(picked[0], full(picked[0].name))
        } else {
            let name = picked.count == 1 ? picked[0].name + ".zip" : String(localized: "Archive.zip")
            transfers.downloadZip(picked.map { full($0.name) }, name: name, cwd: cwd, machine: machine, model: model)
        }
        selecting = false
        selection = []
    }

    /// Copies names beside themselves ("name 2.ext"), the first copy marked.
    private func duplicate(_ names: [String]) {
        transfers.duplicate(names.map(full), cwd: cwd, machine: machine, model: model) { made in
            Task {
                await load()
                if let first = made.first { show(full(first), dir: false) }
            }
        }
    }

    /// A new terminal tab on the computer, in folder (#5832): in the session's workspace when there is one, else the
    /// computer's first; shown over the page once it is there.
    private func openTerminal(in folder: String) {
        guard let api = model.api(group) else { return }
        var body = ["cwd": folder]
        if let key, let ws = model.session(key)?.workspaceID, !ws.isEmpty { body["workspace_id"] = ws }
        Haptics.tap()
        Task {
            do {
                let data = try await api.nodeAction(machine: machineID, "panes", body)
                if let made = try? JSONDecoder().decode(NewShell.self, from: data), !made.terminal.isEmpty {
                    terminal = OpenedTerminal(key: SessionKey(group: group, machineID: machineID, sessionID: made.terminal))
                }
            } catch {
                NodeActions.shared.fail(String(localized: "Couldn't open a terminal"), error)
            }
        }
    }

    private func copy(_ p: String) {
        UIPasteboard.general.string = p
        Haptics.tap()
    }

    private func startRename(_ e: FileEntry) {
        newName = e.name
        renaming = e
    }

    private func save(_ e: FileEntry, _ full: String) {
        transfers.download(FileRef(group: group, machineID: machineID, path: full, cwd: cwd), name: e.name, size: e.size, model: model)
    }

    /// Files from this device into the folder shown; the list reloads after each (the live refresh may lag).
    private func upload(_ urls: [URL], owned: Bool = false) {
        guard !urls.isEmpty else { return }
        transfers.upload(urls, into: shown, cwd: cwd, machine: machine, model: model, owned: owned) { _ in
            Task { await load() }
        }
    }

    /// Where Move's folder picker starts: the session's folder when the folder shown is in it, else the home, else `/`.
    private var pickerRoot: String {
        if let s = sessionFolder, shown == s || shown.hasPrefix(s + "/") { return s }
        if let home, shown == home || shown.hasPrefix(home + "/") { return home }
        return "/"
    }

    /// The git letter of a file, or for a folder the letter of anything changed inside it.
    private func gitMark(_ full: String, dir: Bool) -> String? {
        guard let git, let root = git.root, !git.files.isEmpty, full.hasPrefix(root + "/") else { return nil }
        let rel = String(full.dropFirst(root.count + 1))
        if dir {
            return git.files.first { $0.path.hasPrefix(rel + "/") }?.letter
        }
        return git.files.first { $0.path == rel }?.letter
    }

    private func load() async {
        guard let api = model.api(group), !cwd.isEmpty else { return }
        let target = path
        if listed != target, let cached = recent[target] { // Back / Forward: the folder as it was, at once
            listing = cached
            listed = target
            error = nil
        }
        do {
            let l = try await api.listDir(machine: machineID, path: target, cwd: cwd)
            guard target == path else { return }
            listing = l
            listed = target
            error = nil
            recent[target] = l
            if recent.count > 24, let drop = recent.keys.first(where: { $0 != target }) { recent[drop] = nil }
        } catch {
            guard target == path, !error.isCancellation else { return }
            if listed != target { listing = nil }
            self.error = error.shown
        }
    }
}

/// The "already exists" alert for the transfers' first question, one question at a time. Its buttons answer the question
/// it shows, never the next one in line. After the first, each opening waits `Theme.Motion.alertHandoff` (UIKit presents
/// one at a time), and a question whose alert UIKit dropped (another alert, sheet or picker was up) is asked again.
private struct FolderTransferQuestion: View {
    let transfers: FolderTransfers
    /// The question the alert is open (or opening) for.
    @State private var shown: FolderTransfers.Conflict.ID?
    /// Its name, kept after it closes so the title does not go blank on the way out (lessons/ios.md, #1830).
    @State private var name = ""
    /// This view has opened the alert before: the next opening waits for that one to have closed.
    @State private var opened = false
    /// Bumped when the alert closed with its question still waiting, to ask again.
    @State private var retry = 0

    private struct Opening: Hashable {
        var question: FolderTransfers.Conflict.ID?
        var retry: Int
    }

    var body: some View {
        let question = transfers.conflict.flatMap { $0.id == shown ? $0 : nil }
        // An alert, not a confirmation dialog: from a list that one is a popover with no Cancel (lessons/ios.md, #1830).
        // Closing it never answers: SwiftUI also writes false when UIKit drops a presentation, and that must not turn
        // into Cancel. Every answer is a button, the row's Cancel, or leaving the page.
        Color.clear
            .allowsHitTesting(false)
            .task(id: Opening(question: transfers.conflict?.id, retry: retry)) {
                guard let next = transfers.conflict else { return }
                if opened {
                    try? await Task.sleep(for: Theme.Motion.alertHandoff)
                    guard !Task.isCancelled, transfers.conflict?.id == next.id else { return }
                }
                opened = true
                name = next.name
                shown = next.id
            }
            .alert(String(localized: "“\(name)” already exists"),
                   isPresented: Binding(get: { question != nil }, set: { presented in
                       guard !presented, let id = shown, transfers.conflict?.id == id else { return }
                       shown = nil
                       retry += 1
                   })) {
                if let question {
                    Button("Replace") { transfers.answer(.replace, to: question.id) }
                    Button("Keep Both") { transfers.answer(.keep, to: question.id) }
                    Button("Cancel", role: .cancel) { transfers.answer(.cancel, to: question.id) }
                }
            } message: {
                Text("This folder already has a file with that name. Replace overwrites it; Keep Both saves the new file as “name 2”.")
            }
    }
}

/// Finding by name under the folder shown (FileFind): the folder's files from the node once (`fs/find`, kept for a
/// minute), matched as the user types.
@MainActor @Observable
final class FilesFinder {
    enum State: Equatable {
        case idle, loading
        case ready([FileFind.Hit], truncated: Bool)
        case failed(String)
    }

    private(set) var state: State = .idle
    /// The folder searched (absolute), what the hits are relative to.
    private(set) var root = ""
    @ObservationIgnored private var lists: [String: (entries: [FileFind.Hit], truncated: Bool, at: Date)] = [:]
    @ObservationIgnored private var task: Task<Void, Never>?

    func find(_ query: String, in dir: String, machine: MachineRef, model: AppModel) {
        task?.cancel()
        let q = query.trimmingCharacters(in: .whitespaces)
        guard !q.isEmpty else {
            state = .idle
            return
        }
        root = dir
        task = Task { [weak self] in
            guard let self else { return }
            let key = machine.path + "\u{0}" + dir
            var list = self.lists[key]
            if list == nil || Date.now.timeIntervalSince(list!.at) > 60 {
                if case .ready = self.state {} else { self.state = .loading }
                do {
                    guard let api = model.api(machine.group) else { return }
                    let r = try await api.findFiles(machine: machine.id, dir: dir)
                    let entries = await Task.detached(priority: .userInitiated) { FileFind.entries(r.files ?? []) }.value
                    list = (entries, r.truncated ?? false, .now)
                    self.lists[key] = list
                } catch {
                    if !error.isCancellation { self.state = .failed(error.localizedDescription) }
                    return
                }
            }
            try? await Task.sleep(for: .milliseconds(120)) // typing on
            guard !Task.isCancelled, let list else { return }
            let hits = await Task.detached(priority: .userInitiated) { FileFind.search(list.entries, query: q) }.value
            guard !Task.isCancelled else { return }
            self.state = .ready(hits, truncated: list.truncated)
        }
    }
}

extension EnvironmentValues {
    /// The preview's Show in Files Page (#5827): set where the viewer was opened from a file page or a chat, which shows the
    /// file in its folder. nil: the item is not offered.
    @Entry var showInFiles: ((String) -> Void)? = nil
}
