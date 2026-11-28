import BeautifulUI
import SwiftUI

/// Settings › Templates (#17129): the shared template link or JSON, a native preview, Apply and Default. Nothing is
/// applied on import or on opening a link; the owner confirms the changes first.
struct NativeTemplatesView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @State private var store = NativeTemplateStore.shared
    @State private var input = ""
    @State private var candidate: PersonaTemplate?
    @State private var plan: [NativeTemplateItem] = []
    @State private var error: String?
    @State private var applied = false

    init(initialInput: String = "") {
        _input = State(initialValue: initialInput)
    }

    var body: some View {
        SettingsForm {
            Section {
                if let candidate {
                    preview(candidate)
                } else {
                    importRows
                }
            } header: {
                Text(candidate == nil ? "Templates" : candidate!.title)
            } footer: {
                if let error { Text(error).foregroundStyle(Theme.danger) }
                else if candidate == nil { Text("A template changes this iPhone only after you preview and apply it. External conversations are included in Agents, not a separate tab.") }
            }

            if candidate == nil {
                Section("Built in") {
                    Button {
                        candidate = .default
                        plan = store.plan(.default)
                        error = nil
                    } label: {
                        VStack(alignment: .leading, spacing: Theme.Space.xs) {
                            Text("Default")
                            Text(PersonaTemplate.default.detail).font(Theme.Font.secondary).foregroundStyle(.secondary)
                        }
                    }
                    .accessibilityIdentifier("template-default")
                    if store.enabled {
                        LabeledContent("In use") { Text(store.templateName).foregroundStyle(.secondary) }
                    }
                }
            }
        }
        .themedGroupedPage()
        .detailTitle(String(localized: "Templates"))
        .navigationBarTitleDisplayMode(.inline)
        .accessibilityIdentifier("native-templates")
        .onAppear {
            if input.isEmpty, let value = model.templateLink { input = value }
            if !input.isEmpty { read() }
        }
        .onChange(of: model.pendingNativeTemplate) { _, value in
            guard let value else { return }
            model.pendingNativeTemplate = nil
            input = value
            read()
        }
    }

    @ViewBuilder private var importRows: some View {
        TextField("Link or JSON", text: $input, axis: .vertical)
            .lineLimit(3...8)
            .textInputAutocapitalization(.never)
            .autocorrectionDisabled()
            .accessibilityIdentifier("template-input")
        Button("Preview") { read() }
            .disabled(input.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
            .accessibilityIdentifier("template-preview")
    }

    @ViewBuilder private func preview(_ template: PersonaTemplate) -> some View {
        if !template.detail.isEmpty {
            Text(template.detail).font(Theme.Font.secondary).foregroundStyle(.secondary)
        }
        if !template.isDefault {
            Label("This template came from an import. Review every change before applying it.", systemImage: Theme.Icon.info)
                .font(Theme.Font.secondary).foregroundStyle(.secondary)
        }
        let visible = plan.filter { $0.status != .unchanged }
        let already = plan.count - visible.count
        ForEach(visible) { item in
            VStack(alignment: .leading, spacing: Theme.Space.xs) {
                HStack(alignment: .firstTextBaseline) {
                    Text(item.title)
                    Spacer()
                    Text(item.status == .unsupported ? String(localized: "Not applied") : String(localized: "Will change"))
                        .font(Theme.Font.secondary).foregroundStyle(item.status == .unsupported ? .secondary : Theme.accent)
                }
                if item.status != .unsupported {
                    Text("\(item.before) → \(item.after)").font(Theme.Font.secondary).foregroundStyle(.secondary)
                }
                if let note = item.note {
                    Text(note).font(Theme.Font.secondary).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                }
            }
            .padding(.vertical, Theme.Space.xxs)
        }
        if already > 0 {
            Text(String(format: String(localized: "%d item is already set."), already)).font(Theme.Font.secondary).foregroundStyle(.secondary)
        }
        if visible.isEmpty && already > 0 {
            Text("This template changes nothing here.").foregroundStyle(.secondary)
        }
        FormAction {
            HStack {
                Button("Cancel") {
                    candidate = nil
                    plan = []
                    error = nil
                }
                .buttonStyle(.bordered)
                Spacer()
                Button(applied ? "Done" : "Apply") {
                    if applied {
                        candidate = nil
                        plan = []
                        applied = false
                    } else {
                        guard store.apply(template) else {
                            error = String(localized: "This iPhone can apply the Agent console template with plain main chat, active Agents and short Settings. Use Default to restore Persona.")
                            return
                        }
                        error = nil
                        applied = true
                        model.templateLink = nil
                        model.pendingNativeTemplate = nil
                        model.settingsPath = NavigationPath()
                        model.selectedTab = .assistant
                        model.toast = Toast(String(localized: "Template applied"), style: .success)
                    }
                }
                .buttonStyle(.borderedProminent)
                .disabled(!applied && !plan.contains(where: { $0.status == .change }))
                .accessibilityIdentifier("template-apply")
            }
            .formActionWidth()
        }
    }

    private func read() {
        do {
            let template = try PersonaTemplate.parse(input)
            candidate = template
            plan = store.plan(template)
            error = nil
            applied = false
        } catch {
            candidate = nil
            plan = []
            self.error = error.localizedDescription
        }
    }
}
