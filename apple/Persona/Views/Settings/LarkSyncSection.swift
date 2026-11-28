import PersonaKit
import SwiftUI
import WebKit

/// "Daily Sync to Your Knowledge Base" on the Lark Bot page (#15392; the web's LarkSyncSection.tsx): the bots that may
/// read as the owner with their accounts, each account's state, switch, Sync Now, Authorize Again and Remove, the sync
/// time, and adding an account in two steps (an app that may read as the owner, by scanning; then the owner allows it).
/// A hub without the sync (an older one, or one without the Lark module) sends no view, and nothing is drawn.
struct LarkSyncSections: View {
    var model: LarkSyncModel
    @State private var confirmRemove: LarkSyncView.Binding?
    @State private var confirmRemoveBot: LarkSyncView.Bot?

    var body: some View {
        Group {
            if let v = model.view {
                listSection(v)
                flowSection
            } else if let e = model.loadError {
                Section(String(localized: "Daily Sync to Your Knowledge Base")) {
                    LoadProblem(text: e, labeled: false)
                    Button { Task { await model.load() } } label: { Text("Try Again [Lark Sync]").lineLimit(1) }
                }
            }
        }
        // A sync in progress: look again every few seconds while the page is shown, until none runs.
        .task(id: model.running) {
            while model.running, !Task.isCancelled {
                try? await Task.sleep(for: .seconds(5))
                await model.load()
            }
        }
        .sheet(item: Binding(get: { model.browser }, set: { if $0 == nil { model.browser = nil } })) { b in
            LarkOAuthSheet(browser: b) { r in Task { await model.finishBrowser(r) } }
        }
        .accessibleConfirmationDialog(String(localized: "Remove this account?"), isPresented: Binding(get: { confirmRemove != nil }, set: { if !$0 { confirmRemove = nil } }),
                                      titleVisibility: .visible, hasCancel: true) {
            Button(role: .destructive) {
                if let b = confirmRemove { Task { await model.remove(b) } }
            } label: { Text("Remove [Lark Sync]").lineLimit(1) }
        }
        .accessibleConfirmationDialog(String(localized: "Remove this bot?"), isPresented: Binding(get: { confirmRemoveBot != nil }, set: { if !$0 { confirmRemoveBot = nil } }),
                                      titleVisibility: .visible, hasCancel: true) {
            Button(role: .destructive) {
                if let b = confirmRemoveBot { Task { await model.removeBot(b) } }
            } label: { Text("Remove Bot [Lark Sync]").lineLimit(1) }
        }
    }

    // MARK: The list

    private func listSection(_ v: LarkSyncView) -> some View {
        Section {
            ForEach(v.orderedBots) { bot in
                botRow(v, bot)
                ForEach(v.bindings(of: bot.botID)) { b in bindingRow(v, b) }
            }
            Picker(selection: Binding(get: { v.scheduleHour }, set: { h in Task { await model.setSchedule(h) } })) {
                ForEach(0..<24, id: \.self) { Text(LarkSyncText.hour($0)).tag($0) }
            } label: {
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Text("Sync Time [Lark Sync]")
                    Text("When the daily sync starts, in the hub's time.").font(Theme.Font.caption).foregroundStyle(.secondary)
                }
            }
            .disabled(model.busy == "schedule")
            if model.flow == nil {
                Button { model.flow = .choose } label: {
                    Label("Add Account or Bot", systemImage: Theme.Icon.add).lineLimit(1)
                }
                .accessibilityIdentifier("lark-sync-add")
            }
            if let e = model.error, !e.isEmpty, model.flow == nil {
                LoadProblem(text: e, labeled: false)
            }
        } header: {
            Text("Daily Sync to Your Knowledge Base")
        } footer: {
            Text("Every day around \(LarkSyncText.hour(v.scheduleHour)), Persona reads what changed in the chats, documents and meeting minutes these accounts can see, and adds it to your knowledge base. Nothing is posted or changed in Lark.")
        }
    }

    private func botRow(_ v: LarkSyncView, _ bot: LarkSyncView.Bot) -> some View {
        let ready = bot.hasSecret != false
        let busy = model.busy == "bot:\(bot.botID)"
        return VStack(alignment: .leading, spacing: Theme.Space.s) {
            HStack(spacing: Theme.Space.m) {
                IconTile(bot.primary == true ? Theme.Icon.chatBot : Theme.Icon.link, tint: Theme.accent)
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Text(LarkSyncText.botTitle(bot)).font(Theme.Font.body).lineLimit(1)
                    Text(LarkSyncText.botHint(bot, accounts: v.bindings(of: bot.botID).count))
                        .font(Theme.Font.caption).foregroundStyle(.secondary)
                }
                Spacer(minLength: Theme.Space.s)
                if busy { ProgressView() }
            }
            if let a = LarkSyncText.approval(bot) { Chip(text: a.text, look: look(a.tone)) }
            HStack(spacing: Theme.Space.s) {
                if ready {
                    Button { Task { await model.startAuthorize(bot: bot.botID, tier: bot.tier ?? LarkSyncView.tierDocs) } } label: {
                        Label("Connect Account", systemImage: Theme.Icon.add).lineLimit(1).minimumScaleFactor(0.8)
                    }
                    .themedButton()
                    .disabled(model.flow != nil)
                } else if bot.primary != true, bot.registering == true {
                    Button { Task { await model.resume(bot) } } label: {
                        Label("Continue Setup", systemImage: Theme.Icon.scanCode).lineLimit(1).minimumScaleFactor(0.8)
                    }
                    .themedButton()
                    .disabled(busy)
                }
                if bot.primary != true {
                    Button(role: .destructive) { confirmRemoveBot = bot } label: {
                        Label("Remove Bot [Lark Sync]", systemImage: Theme.Icon.delete).labelStyle(.iconOnly)
                    }
                    .themedButton()
                    .accessibilityLabel(String(localized: "Remove \(LarkSyncText.botTitle(bot))"))
                    .disabled(busy)
                }
            }
            .buttonStyle(.borderless)
        }
        .padding(.vertical, Theme.Space.xxs)
        .accessibilityIdentifier("lark-sync-bot-\(bot.botID)")
    }

    private func bindingRow(_ v: LarkSyncView, _ b: LarkSyncView.Binding) -> some View {
        let name = v.title(b)
        let state = LarkSyncText.health(b)
        let soon = LarkSyncText.reauthSoon(b)
        let authorize = LarkSyncText.needsAuthorize(b)
        let busy = model.busy == b.key
        return VStack(alignment: .leading, spacing: Theme.Space.s) {
            HStack(alignment: .top, spacing: Theme.Space.m) {
                VStack(alignment: .leading, spacing: Theme.Space.xs) {
                    Text(name).font(Theme.Font.body).lineLimit(1)
                    Chip(text: state.text, look: look(state.tone))
                    TimelineView(.periodic(from: .now, by: 30)) { ctx in
                        Text(LarkSyncText.statusLine(b, now: ctx.date)).font(Theme.Font.caption).foregroundStyle(.secondary)
                    }
                }
                Spacer(minLength: Theme.Space.s)
                Toggle(String(localized: "Sync \(name) every day"), isOn: Binding(get: { b.enabled }, set: { on in Task { await model.setEnabled(b, on) } }))
                    .labelsHidden()
                    .disabled(busy || (authorize && b.health != "scope_missing"))
            }
            if let e = b.error, !e.isEmpty, b.running != true {
                Text(e).font(Theme.Font.caption).foregroundStyle(Theme.cautionText)
            }
            if let soon {
                Text("Access ends in \(soon) days. Authorize again to keep syncing.").font(Theme.Font.caption).foregroundStyle(Theme.cautionText)
            }
            HStack(spacing: Theme.Space.s) {
                if authorize || soon != nil {
                    Button { Task { await model.startAuthorize(bot: b.botID, tier: b.tier ?? LarkSyncView.tierDocs, again: true) } } label: {
                        Label("Authorize Again", systemImage: Theme.Icon.credentials).lineLimit(1).minimumScaleFactor(0.8)
                    }
                    .themedButton(prominent: authorize)
                    .disabled(model.flow != nil)
                }
                if !authorize {
                    Button { Task { await model.run(b) } } label: {
                        Group {
                            if busy { ProgressView().controlSize(.small) } else {
                                Label(b.running == true ? String(localized: "Syncing…") : String(localized: "Sync Now"), systemImage: Theme.Icon.retry)
                                    .lineLimit(1).minimumScaleFactor(0.8)
                            }
                        }
                    }
                    .themedButton()
                    .disabled(busy || !b.enabled || b.running == true)
                }
                Button(role: .destructive) { confirmRemove = b } label: { Text("Remove [Lark Sync]").lineLimit(1) }
                    .themedButton()
                    .disabled(busy)
            }
            .buttonStyle(.borderless)
        }
        .padding(.vertical, Theme.Space.xxs)
        .accessibilityIdentifier("lark-sync-binding-\(b.key)")
    }

    private func look(_ t: LarkSyncText.Tone) -> Chip.Look {
        switch t {
        case .positive: .review(.positive)
        case .caution: .review(.caution)
        case .danger: .review(.danger)
        case .accent: .tint(Theme.accent)
        case .neutral: .neutral
        }
    }

    // MARK: Adding and authorizing

    @ViewBuilder private var flowSection: some View {
        switch model.flow {
        case .choose: LarkSyncChooseSection(model: model)
        case let .register(_, _, prompt, scopes):
            Section {
                LarkSyncPromptCard(prompt: prompt, onCancel: { model.cancelFlow() }, onRestart: { Task { await model.restartRegistration() } })
            } header: {
                Text(scopes ? String(localized: "Step 1 of 2: Give the Chat Bot Permission") : String(localized: "Step 1 of 2: Create the App"))
            } footer: {
                Text("Scan with the Lark app and confirm. This page moves on by itself.")
            }
            .task(id: prompt.error == nil ? ExtensionSignIn.pollKey(prompt) : nil) {
                guard prompt.error == nil, ExtensionSignIn.polls(prompt) else { return }
                while !Task.isCancelled {
                    try? await Task.sleep(for: .seconds(max(2, prompt.interval ?? 3)))
                    if Task.isCancelled { return }
                    if await model.pollRegister() { return }
                }
            }
        case let .authorize(_, _, again, answer, prompt, error):
            Section {
                authorizeBody(answer: answer, prompt: prompt, error: error)
            } header: {
                Text(again ? String(localized: "Authorize Again [Lark Sync title]") : String(localized: "Step 2 of 2: Allow Reading as You"))
            }
            .task(id: answer?.mode == "device" && prompt?.error == nil ? prompt.flatMap(ExtensionSignIn.pollKey) : nil) {
                guard answer?.mode == "device", let prompt, prompt.error == nil, ExtensionSignIn.polls(prompt) else { return }
                while !Task.isCancelled {
                    try? await Task.sleep(for: .seconds(max(2, prompt.interval ?? 3)))
                    if Task.isCancelled { return }
                    if await model.pollDevice() { return }
                }
            }
        case nil: EmptyView()
        }
    }

    @ViewBuilder private func authorizeBody(answer: LarkSyncAuthorize?, prompt: ConnectorPrompt?, error: String?) -> some View {
        if let error {
            Text(error).font(Theme.Font.label).foregroundStyle(Theme.danger).textSelection(.enabled)
            actionButtons(primary: (String(localized: "Try Again [Lark Sync]"), { Task { await model.prepareAuthorize() } }))
        } else if let answer {
            switch answer.mode {
            case "device":
                Text("Scan with the Lark app, or open the link and enter the code, then allow Persona to read as you.")
                    .font(Theme.Font.secondary).foregroundStyle(.secondary)
                if let prompt {
                    LarkSyncPromptCard(prompt: prompt, onCancel: { model.cancelFlow() }, onRestart: { Task { await model.prepareAuthorize() } })
                }
            case "manual":
                Text("One more thing first: in the app's settings in the Lark developer console, add this address under Security settings › Redirect URLs. Then continue.")
                    .font(Theme.Font.secondary).foregroundStyle(.secondary)
                let uri = answer.redirectURI ?? model.redirectURI ?? ""
                HStack {
                    Text(uri).font(Theme.Font.code).textSelection(.enabled).lineLimit(2).accessibilityIdentifier("lark-sync-redirect")
                    Spacer(minLength: Theme.Space.s)
                    Button { UIPasteboard.general.string = uri; Haptics.success() } label: {
                        Label("Copy Address", systemImage: Theme.Icon.copy).labelStyle(.iconOnly)
                    }
                    .buttonStyle(.borderless)
                }
                if let s = answer.consoleURL, let url = URL(string: s) {
                    Button { UIApplication.shared.open(url) } label: {
                        Label("Open App Settings", systemImage: Theme.Icon.openExternal).lineLimit(1)
                    }
                }
                actionButtons(primary: (String(localized: "I've Added It, Continue"), { model.openBrowser() }))
            default:
                Text("Lark opens and asks whether Persona may read as you. Afterwards you come back here.")
                    .font(Theme.Font.secondary).foregroundStyle(.secondary)
                actionButtons(primary: (String(localized: "Continue in Lark"), { model.openBrowser() }))
            }
        } else {
            HStack(spacing: Theme.Space.s) {
                ProgressView().controlSize(.small)
                Text("Getting ready…").font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(1)
            }
        }
    }

    private func actionButtons(primary: (String, () -> Void)) -> some View {
        HStack(spacing: Theme.Space.s) {
            Button(action: primary.1) {
                Group {
                    if model.busy == "authorize" { ProgressView().controlSize(.small) } else { Text(primary.0).lineLimit(1).minimumScaleFactor(0.8) }
                }
                .formActionWidth()
            }
            .themedButton(prominent: true)
            .disabled(model.busy == "authorize")
            Button { model.cancelFlow() } label: { Text("Cancel").lineLimit(1).formActionWidth() }
                .themedButton()
        }
        .buttonStyle(.borderless)
        .padding(.vertical, Theme.Space.xs)
    }
}

/// Step 1's choice: what the app may read, its name and service, then a new app by scanning or the chat bot's app.
private struct LarkSyncChooseSection: View {
    var model: LarkSyncModel
    @State private var tier = LarkSyncView.tierDocs
    @State private var label = ""
    @State private var region = "feishu"

    var body: some View {
        Section {
            Text("Persona reads Lark through an app you allow to read as you. Create a new one by scanning, or give the chat bot this permission.")
                .font(Theme.Font.secondary).foregroundStyle(.secondary)
            Picker(selection: $tier) {
                ForEach(LarkSyncView.tiers, id: \.self) { Text(LarkSyncText.tier($0)).tag($0) }
            } label: {
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Text("What Persona May Read")
                    Text(LarkSyncText.tierHint(tier)).font(Theme.Font.caption).foregroundStyle(.secondary)
                }
            }
            TextField(String(localized: "Name (optional)"), text: $label, prompt: Text("For example, Work"))
                .textInputAutocapitalization(.never).autocorrectionDisabled()
            Picker("Service [Lark Sync]", selection: $region) {
                Text("Lark [Inbox Event Views 7e4419]").tag("feishu")
                Text("Lark").tag("lark")
            }
            if let e = model.error, !e.isEmpty {
                LoadProblem(text: e, labeled: false)
            }
            VStack(spacing: Theme.Space.s) {
                Button { Task { await model.startAdd(new: true, label: label, tier: tier, region: region) } } label: {
                    Group {
                        if model.busy == "add" { ProgressView().controlSize(.small) } else {
                            Label("Create by Scanning", systemImage: Theme.Icon.scanCode).lineLimit(1).minimumScaleFactor(0.8)
                        }
                    }
                    .formActionWidth()
                }
                .themedButton(prominent: true)
                if let p = model.primary, p.hasSecret != false {
                    Button { Task { await model.startAdd(new: false, label: label, tier: tier, region: region) } } label: {
                        Text("Use the Chat Bot").lineLimit(1).minimumScaleFactor(0.8).formActionWidth()
                    }
                    .themedButton()
                }
                Button { model.cancelFlow() } label: { Text("Cancel").lineLimit(1).formActionWidth() }
                    .themedButton()
            }
            .buttonStyle(.borderless)
            .disabled(model.busy == "add")
            .padding(.vertical, Theme.Space.xs)
        } header: {
            Text("Add an Account")
        } footer: {
            Text("Use a new bot for an account in another company or another Lark account.")
        }
        .onAppear {
            if model.primary?.domain?.contains("larksuite") == true { region = "lark" }
        }
    }
}

/// A step's prompt from the hub: its words, then the QR code (with the link that opens Lark on this phone) or a code to
/// enter on a page; a failure (expired, declined) says so, with Start Over.
struct LarkSyncPromptCard: View {
    var prompt: ConnectorPrompt
    var onCancel: () -> Void
    var onRestart: () -> Void

    var body: some View {
        if let e = prompt.error, !e.isEmpty {
            Text(e).font(Theme.Font.label).foregroundStyle(Theme.danger)
            buttons(primary: (String(localized: "Start Over"), onRestart))
        } else {
            if let hint = prompt.hint, !hint.isEmpty {
                Text(hint).font(Theme.Font.secondary).foregroundStyle(.secondary)
            }
            if let code = prompt.userCode, !code.isEmpty {
                HStack {
                    Text(code).font(Theme.Font.display).monospaced().textSelection(.enabled).lineLimit(1).minimumScaleFactor(0.6)
                    Spacer(minLength: Theme.Space.s)
                    Button { UIPasteboard.general.string = code; Haptics.success() } label: {
                        Label("Copy Code [Connector Sign In Views 85a0b9]", systemImage: Theme.Icon.copy).labelStyle(.iconOnly)
                    }
                    .buttonStyle(.borderless)
                }
            }
            if ExtensionSignIn.isQR(prompt) {
                QRCodeView(rows: prompt.qr?.rows ?? [])
                    .scaledFrame(width: Theme.Size.box200, height: Theme.Size.box200)
                    .frame(maxWidth: .infinity)
                    .accessibilityIdentifier("lark-sync-qr")
            }
            if ExtensionSignIn.polls(prompt) || prompt.isPairCode {
                HStack(spacing: Theme.Space.s) {
                    ProgressView().controlSize(.small)
                    Text("Waiting for you to confirm in Lark…").font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(1)
                }
            } else {
                Text("This version of the app can't show this step. Update the app, then tap Start Over.")
                    .font(Theme.Font.secondary).foregroundStyle(Theme.cautionText)
            }
            buttons(primary: Self.link(prompt).map { url in
                (prompt.linkText?.isEmpty == false ? prompt.linkText! : String(localized: "Open on This Device"), { UIApplication.shared.open(url) })
            })
        }
    }

    /// Where the step is done on this phone: the hub's link (the registration page opens Lark), else the step's own page.
    static func link(_ p: ConnectorPrompt) -> URL? {
        if let s = p.link, let url = URL(string: s), ["https", "http"].contains(url.scheme?.lowercased() ?? "") { return url }
        return ExtensionSignIn.link(p)
    }

    private func buttons(primary: (String, () -> Void)?) -> some View {
        HStack(spacing: Theme.Space.s) {
            if let primary {
                Button(action: primary.1) { Text(primary.0).lineLimit(1).minimumScaleFactor(0.8).formActionWidth() }
                    .themedButton(prominent: true)
            }
            Button(action: onCancel) { Text("Cancel").lineLimit(1).formActionWidth() }
                .themedButton()
        }
        .buttonStyle(.borderless)
        .padding(.vertical, Theme.Space.xs)
    }
}

/// The platform's authorization page in the app's own web view. It stops at the return address (the hub's
/// `/lark-oauth`) and hands what the platform sent there to the page: the hub's web page never loads.
private struct LarkOAuthSheet: View {
    let browser: LarkOAuthBrowser
    let onReturn: (LarkOAuthReturn) -> Void
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            LarkOAuthWebView(url: browser.url, redirect: browser.redirect, onReturn: onReturn)
                .ignoresSafeArea(edges: .bottom)
                .navigationTitle(String(localized: "Lark"))
                .navigationBarTitleDisplayMode(.inline)
                .toolbar {
                    // The sheet's way out, ✕ at the top leading corner (#9653, #16676).
                    SheetClose { dismiss() }
                }
        }
    }
}

private struct LarkOAuthWebView: UIViewRepresentable {
    let url: URL
    let redirect: String
    let onReturn: (LarkOAuthReturn) -> Void

    func makeCoordinator() -> Coordinator { Coordinator(redirect: redirect, onReturn: onReturn) }

    func makeUIView(context: Context) -> WKWebView {
        let web = WKWebView(frame: .zero, configuration: WKWebViewConfiguration())
        web.navigationDelegate = context.coordinator
        web.load(URLRequest(url: url))
        return web
    }

    func updateUIView(_ web: WKWebView, context: Context) {}

    final class Coordinator: NSObject, WKNavigationDelegate {
        let redirect: String
        let onReturn: (LarkOAuthReturn) -> Void
        private var returned = false

        init(redirect: String, onReturn: @escaping (LarkOAuthReturn) -> Void) {
            self.redirect = redirect
            self.onReturn = onReturn
        }

        func webView(_ webView: WKWebView, decidePolicyFor navigationAction: WKNavigationAction) async -> WKNavigationActionPolicy {
            guard let url = navigationAction.request.url, let r = LarkOAuthReturn.parse(url, redirect: redirect) else { return .allow }
            if !returned {
                returned = true
                onReturn(r)
            }
            return .cancel
        }
    }
}
