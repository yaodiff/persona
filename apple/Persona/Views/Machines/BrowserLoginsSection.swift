import SwiftUI
import UniformTypeIdentifiers

/// Browser Sign-ins on a Mac's page (#1304, docs/plans/plan-ego-browser.md「Importing cookies」; the web's BrowserLogins): the sites whose
/// sign-ins ego lite keeps (how many cookies, until when, never what), clearing some of them, and importing a cookie
/// file (cookies.txt, or an extension's JSON). The file is read here, its sites ticked by the user (none at first), and
/// only the ticked ones go to the hub, which passes them to the Mac and keeps nothing. The Mac must be online.
/// Three sites or fewer are listed here; more fold behind one summary row that opens them all on their own page
/// (`BrowserLoginsListView`, #5821, docs/design/DESIGN_SYSTEM.md「Long lists on a detail page」).
struct BrowserLoginsSection: View {
    @Environment(AppModel.self) private var model
    let ref: MachineRef
    var online: Bool
    /// The computer page's: it also opens the whole list (`MachineDetailView`, #6050).
    var logins: BrowserLoginsModel
    @Binding var listOpen: Bool
    @State private var picking = false
    @State private var file: PickedFile?
    /// The sites the confirmation asks about.
    @State private var clearing: [String]?

    init(ref: MachineRef, online: Bool, logins: BrowserLoginsModel, listOpen: Binding<Bool>) {
        self.ref = ref
        self.online = online
        self.logins = logins
        _listOpen = listOpen
    }

    struct PickedFile: Identifiable {
        var name: String
        var sites: [CookieFileSite]
        var bad: Int
        var ticked: Set<String> = []
        var id: String { name }
        var cookies: [BrowserCookie] { sites.filter { ticked.contains($0.site) }.flatMap(\.cookies) }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.m) {
            HStack(spacing: Theme.Space.s) {
                Text("Browser Sign-ins").font(Theme.Font.heading)
                if let sites = logins.sites { Text("\(sites.count)").font(Theme.Font.secondary.monospacedDigit()).foregroundStyle(.secondary) }
                Spacer(minLength: Theme.Space.s)
                Button { picking = true } label: {
                    Label("Import", systemImage: Theme.Icon.file).lineLimit(1)
                }
                .themedButton()
                .disabled(logins.busy || !online)
                .accessibilityIdentifier("browser-logins-import")
            }
            .padding(.horizontal, Theme.Space.xs)
            VStack(alignment: .leading, spacing: .zero) {
                content
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .card()
            Group {
                #if os(macOS)
                // read(_:) runs on this machine on every platform, so on the Mac the file is parsed locally, not on a phone.
                Text("Only sites and counts are listed, never cookie contents. Imported files are read on this machine, and only the sites you check go through the hub straight to the target machine. The hub keeps nothing, and the assistant can't see or import them.")
                #else
                Text("Only sites and counts are listed, never cookie contents. Imported files are read on your phone, and only the sites you check go through the hub straight to this Mac. The hub keeps nothing, and the assistant can't see or import them.")
                #endif
            }
            .font(Theme.Font.label).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            .padding(.horizontal, Theme.Space.xs)
        }
        .task(id: online) {
            await logins.appear(online: online)
            #if DEBUG
            // `-openCookieFile <path>`: as if that file were picked (UI tests and screenshots; the system's picker is
            // not theirs to drive). `-openBrowserLogins list|select|confirm|search`: straight into the whole list.
            if let path = UserDefaults.standard.string(forKey: "openCookieFile"), file == nil, logins.sites != nil { read(URL(fileURLWithPath: path)) }
            if let open = UserDefaults.standard.string(forKey: "openBrowserLogins"), open != "YES", logins.folded {
                // after the launch's own push to this page has settled: pushing during it left the stack showing its root
                try? await Task.sleep(for: .seconds(1.5))
                listOpen = true
            }
            #endif
        }
        .fileImporter(isPresented: $picking, allowedContentTypes: [.plainText, .json, .text, .data]) { result in
            if case .success(let url) = result { read(url) }
        }
        .sheet(item: $file) { _ in
            CookiePickSheet(file: $file, busy: logins.busy) {
                if let cookies = file?.cookies, await logins.importCookies(cookies) { file = nil } // the file's contents are gone once sent
            }
        }
        .browserLoginsClearDialog($clearing, busy: logins.busy) { sites in await logins.clear(sites) }
    }

    @ViewBuilder private var content: some View {
        if !online {
            Text("This Mac must be online to view and import.").font(Theme.Font.secondary).foregroundStyle(.secondary)
        } else if let error = logins.error {
            VStack(alignment: .leading, spacing: Theme.Space.s) {
                Text(error).font(Theme.Font.secondary).foregroundStyle(Theme.danger).fixedSize(horizontal: false, vertical: true)
                Button("Try Again") { Task { await logins.load(online: online) } }
                    .lineLimit(1)
                    .themedButton()
                    .accessibilityIdentifier("browser-logins-retry")
            }
            .padding(.vertical, Theme.Space.s)
        } else if let sites = logins.sites {
            if sites.isEmpty {
                Text("No sites are signed in to ego lite yet. Import a cookie file exported from another computer or browser, and the assistant can open those sites as you.")
                    .font(Theme.Font.secondary).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            } else if logins.folded {
                summaryRow
            } else {
                ForEach(Array(sites.enumerated()), id: \.element.id) { i, s in
                    if i > 0 { Divider() }
                    row(s)
                }
            }
        } else {
            HStack(spacing: Theme.Space.s) {
                ProgressView().controlSize(.small)
                Text("Reading").font(Theme.Font.secondary).foregroundStyle(.secondary)
            }
        }
    }

    /// The folded list: how many sites, when the first still signed in expires, how many have expired; a tap opens them all.
    private var summaryRow: some View {
        Button { listOpen = true } label: {
            HStack(spacing: Theme.Space.m) {
                ThemedIcon(Theme.Icon.siteLogin).foregroundStyle(.secondary).scaledFrame(width: Theme.Size.icon)
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Text(String(localized: "\(logins.summary.count) sites")).font(Theme.Font.body).foregroundStyle(.primary).lineLimit(1)
                    if let detail = Self.summaryDetail(logins.summary) { detail.font(Theme.Font.label.monospacedDigit()).lineLimit(1) }
                }
                Spacer(minLength: Theme.Space.s)
                ThemedIcon(Theme.Icon.disclosure).font(Theme.Font.labelStrong).foregroundStyle(.tertiary)
            }
            .padding(.vertical, Theme.Space.s)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityIdentifier("browser-logins-summary")
    }

    /// "First expires 12/13/2026 · 2 expired" (the expired in the caution color); nil when no site has a date.
    static func summaryDetail(_ s: BrowserSiteList.Summary) -> Text? {
        let first = s.first.map { Text(String(localized: "First expires \($0.formatted(date: .numeric, time: .omitted))")).foregroundStyle(.secondary) }
        let expired = s.expired > 0 ? Text(String(localized: "\(s.expired) expired")).foregroundStyle(Theme.cautionText) : nil
        switch (first, expired) {
        case let (f?, e?): return f + Text(verbatim: " · ").foregroundStyle(.secondary) + e
        case let (f?, nil): return f
        case let (nil, e?): return e
        case (nil, nil): return nil
        }
    }

    private func row(_ s: BrowserCookieSite) -> some View {
        HStack(spacing: Theme.Space.m) {
            BrowserSiteRow(site: s, expired: logins.expired.contains(s.domain))
            Menu {
                Button(role: .destructive) { clearing = [s.domain] } label: { Label("Clear Sign-in…", systemImage: Theme.Icon.delete) }
            } label: {
                ThemedIcon(Theme.Icon.more).font(Theme.Font.body).foregroundStyle(.secondary).tapTargetFrame()
            }
            .disabled(logins.busy)
            .accessibilityLabel(String(localized: "More"))
            .accessibilityIdentifier("browser-logins-more")
        }
        .padding(.vertical, Theme.Space.xs)
        .contextMenu {
            Button(role: .destructive) { clearing = [s.domain] } label: { Label("Clear Sign-in…", systemImage: Theme.Icon.delete) }
        }
    }

    static func detail(count: Int, expires: Date?) -> String {
        if let expires {
            let date = expires.formatted(date: .numeric, time: .omitted)
            return String(localized: "\(count) cookies · expire by \(date)")
        }
        return String(localized: "\(count) cookies · end when the browser closes")
    }

    // MARK: Calls

    private func read(_ url: URL) {
        let scoped = url.startAccessingSecurityScopedResource()
        defer { if scoped { url.stopAccessingSecurityScopedResource() } }
        do {
            let text = try String(contentsOf: url, encoding: .utf8)
            let parsed = try CookieFile.parse(text)
            file = PickedFile(name: url.lastPathComponent, sites: CookieFile.sites(parsed.cookies), bad: parsed.bad)
        } catch {
            model.toast = Toast(error.localizedDescription, style: .error)
        }
    }
}

/// A site: its domain (with Expired when every cookie of it has), how many cookies and until when.
struct BrowserSiteRow: View, Equatable {
    var site: BrowserCookieSite
    var expired: Bool

    static func == (a: Self, b: Self) -> Bool { a.site == b.site && a.expired == b.expired }

    var body: some View {
        HStack(spacing: Theme.Space.m) {
            ThemedIcon(Theme.Icon.siteLogin).foregroundStyle(.secondary).scaledFrame(width: Theme.Size.icon)
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                HStack(spacing: Theme.Space.s) {
                    Text(site.domain).font(Theme.Font.body).lineLimit(1).accessibilityIdentifier("browser-logins-site")
                    if expired { Chip(text: String(localized: "Expired"), look: .review(.caution)) }
                }
                Text(BrowserLoginsSection.detail(count: site.count, expires: site.expires))
                    .font(Theme.Font.label.monospacedDigit()).foregroundStyle(.secondary).lineLimit(1)
            }
            Spacer(minLength: Theme.Space.s)
        }
        .accessibilityElement(children: .combine)
    }
}

extension View {
    /// Asks before clearing these sites' sign-ins: the one site by name, or how many (#5821).
    func browserLoginsClearDialog(_ clearing: Binding<[String]?>, busy: Bool, run: @escaping ([String]) async -> Void) -> some View {
        let sites = clearing.wrappedValue ?? []
        let title = sites.count == 1 ? String(localized: "Clear Sign-in for \(sites[0])?") : String(localized: "Clear Sign-ins for \(sites.count) Sites?")
        return confirmationDialog(title, isPresented: Binding(get: { clearing.wrappedValue != nil }, set: { if !$0 { clearing.wrappedValue = nil } }),
                                  titleVisibility: .visible) {
            Button("Clear [Image Editor bce237]", role: .destructive) {
                Task {
                    await run(sites)
                    clearing.wrappedValue = nil
                }
            }
            .disabled(busy)
            .accessibilityIdentifier("browser-logins-clear-confirm")
        } message: {
            Text(sites.count == 1 ? String(localized: "This site will be signed out in ego lite on this Mac. This includes your own sign-in in ego.")
                                  : String(localized: "These sites will be signed out in ego lite on this Mac. This includes your own sign-ins in ego."))
        }
    }
}

/// The sites of a picked cookie file to tick (none at first), and Import N.
private struct CookiePickSheet: View {
    @Binding var file: BrowserLoginsSection.PickedFile?
    var busy: Bool
    var run: () async -> Void

    var body: some View {
        NavigationStack {
            List {
                if let f = file {
                    Section {
                        ForEach(f.sites) { s in
                            let none = s.cookies.isEmpty
                            Button {
                                guard !none else { return }
                                if file?.ticked.contains(s.site) == true { file?.ticked.remove(s.site) } else { file?.ticked.insert(s.site) }
                            } label: {
                                HStack(spacing: Theme.Space.m) {
                                    ThemedIcon(f.ticked.contains(s.site) ? Theme.Icon.successFilled : Theme.Icon.unchecked)
                                        .foregroundStyle(f.ticked.contains(s.site) ? Theme.accent : Color.secondary)
                                    VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                                        Text(s.site).font(Theme.Font.body).foregroundStyle(.primary).lineLimit(1)
                                        Text(Self.detail(s)).font(Theme.Font.label.monospacedDigit()).foregroundStyle(.secondary).lineLimit(1)
                                    }
                                }
                            }
                            .buttonStyle(.plain) // the label in its own colors, not the tint; the whole row takes the tap
                            .contentShape(.rect)
                            .disabled(none)
                            .opacity(none ? 0.5 : 1)
                            .accessibilityIdentifier("browser-logins-pick")
                        }
                    } header: {
                        Text(f.bad > 0 ? String(localized: "\(f.name) · \(f.sites.count) sites · skipped \(f.bad) unreadable lines") : String(localized: "\(f.name) · \(f.sites.count) sites"))
                    } footer: {
                        Text("Nothing is checked at first; only what you check is imported. They go into ego lite's default profile on this Mac, so your own browsing in ego uses these sign-ins too.")
                    }
                }
            }
            .navigationTitle("Choose Sites to Import")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { file = nil }.lineLimit(1).disabled(busy)
                }
                ToolbarItem(placement: .confirmationAction) {
                    let n = file?.cookies.count ?? 0
                    Button(n > 0 ? String(localized: "Import \(n)") : String(localized: "Import")) { Task { await run() } }
                        .lineLimit(1)
                        .disabled(busy || n == 0)
                        .accessibilityIdentifier("browser-logins-confirm")
                }
            }
        }
        .presentationDetents([.medium, .large])
    }

    static func detail(_ s: CookieFileSite) -> String {
        let count = s.cookies.count
        let date = s.expires.map { Date(timeIntervalSince1970: $0).formatted(date: .numeric, time: .omitted) }
        switch (date, s.expired > 0) {
        case let (date?, true): return String(localized: "\(count) cookies · expire by \(date) · \(s.expired) expired, not imported")
        case let (date?, false): return String(localized: "\(count) cookies · expire by \(date)")
        case (nil, true): return String(localized: "\(count) cookies · \(s.expired) expired, not imported")
        case (nil, false): return String(localized: "cookies.count", defaultValue: "\(count) cookies")
        }
    }
}
