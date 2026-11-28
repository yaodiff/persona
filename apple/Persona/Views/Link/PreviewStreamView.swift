#if os(iOS)
import SwiftUI
import UIKit

/// Renders a computer's headless-browser preview stream (`PreviewStreamModel`) inside the iOS preview card:
/// draws decoded JPEG/PNG frames, maps taps and drags to page CSS coordinates (`mouse` / `wheel`), forwards text/keys
/// through a hidden `UITextField`, presents native SwiftUI sheets for `<select>` popups and native alerts for JS
/// dialogs, and displays clear design-system banners and empty states.
struct PreviewStreamView: View {
    var stream: PreviewStreamModel
    var machineName: String
    var showAutoFallbackNotice: Bool
    var onDismissFallbackNotice: () -> Void
    var onSwitchToDirect: () -> Void
    @Binding var keyboardActive: Bool
    @Environment(\.displayScale) private var displayScale
    @State private var containerSize: CGSize = .zero
    @State private var promptInput = ""
    @State private var dragging = false
    @State private var lastDragLocation: CGPoint = .zero

    private var displayedRect: CGRect {
        let content = stream.pageSize.width > 0 && stream.pageSize.height > 0 ? stream.pageSize : containerSize
        return PreviewStreamLogic.aspectFitRect(contentSize: content, in: containerSize)
    }

    private var activeAlertMessage: String? {
        guard let d = stream.activeDialog, d.kind == .alert else { return nil }
        return d.message
    }

    private var activeConfirmMessage: String? {
        guard let d = stream.activeDialog, d.kind == .confirm || d.kind == .beforeunload else { return nil }
        return d.kind == .beforeunload
            ? String(localized: "Leave this page? Changes you made may not be saved.")
            : d.message
    }

    private var activePromptMessage: String? {
        guard let d = stream.activeDialog, d.kind == .prompt else { return nil }
        return d.message
    }

    var body: some View {
        VStack(spacing: Theme.Space.flush) {
            if showAutoFallbackNotice {
                NoticeBanner(
                    text: String(localized: "Couldn't show the page directly, so it's shown through the computer."),
                    tone: .neutral,
                    symbol: Theme.Icon.screen,
                    dismiss: onDismissFallbackNotice
                )
                .padding(.horizontal, Theme.Space.m)
                .padding(.vertical, Theme.Space.s)
                .accessibilityIdentifier("preview-fallback-notice")
            }
            if let banner = stream.banner {
                streamBanner(banner)
                    .padding(.horizontal, Theme.Space.m)
                    .padding(.vertical, Theme.Space.xs)
            }
            canvasArea
        }
        .background(Theme.pageBackground)
        .onChange(of: stream.selectOpen?.id) { _, next in
            if next != nil { keyboardActive = false }
        }
        .onChange(of: stream.activeDialog?.id) { _, _ in
            if let d = stream.activeDialog {
                keyboardActive = false
                if d.kind == .prompt { promptInput = d.defaultText }
            }
        }
        .sheet(
            item: Binding(
                get: { stream.selectOpen },
                set: { if $0 == nil { stream.dismissSelect() } }
            )
        ) { state in
            PreviewSelectSheet(
                state: state,
                groups: stream.selectGroups,
                onChoose: { stream.chooseSelect(index: $0) },
                onCancel: { stream.dismissSelect() }
            )
        }
        .alert(
            String(localized: "Page Message"),
            isPresented: Binding(
                get: { activeAlertMessage != nil },
                set: { if !$0 { stream.answerDialog(accept: true) } }
            )
        ) {
            Button(String(localized: "OK"), role: .cancel) {
                stream.answerDialog(accept: true)
            }
        } message: {
            Text(activeAlertMessage ?? "")
        }
        .background {
            Color.clear
                .alert(
                    String(localized: "Confirm"),
                    isPresented: Binding(
                        get: { activeConfirmMessage != nil },
                        set: { if !$0 { stream.answerDialog(accept: false) } }
                    )
                ) {
                    Button(String(localized: "Cancel"), role: .cancel) {
                        stream.answerDialog(accept: false)
                    }
                    Button(String(localized: "OK")) {
                        stream.answerDialog(accept: true)
                    }
                } message: {
                    Text(activeConfirmMessage ?? "")
                }
                .background {
                    Color.clear
                        .alert(
                            String(localized: "Page Prompt"),
                            isPresented: Binding(
                                get: { activePromptMessage != nil },
                                set: { if !$0 { stream.answerDialog(accept: false) } }
                            )
                        ) {
                            TextField("", text: $promptInput)
                            Button(String(localized: "Cancel"), role: .cancel) {
                                stream.answerDialog(accept: false)
                            }
                            Button(String(localized: "OK")) {
                                let value = promptInput
                                stream.answerDialog(accept: true, text: value)
                            }
                        } message: {
                            Text(activePromptMessage ?? "")
                        }
                }
        }
    }

    // MARK: Canvas & gestures

    private var canvasArea: some View {
        ZStack {
            Theme.pageBackground

            // Hidden input bridging iOS keyboard, IME composition, Return, and Backspace to key/text messages.
            StreamKeyboardInput(
                isActive: $keyboardActive,
                onText: { stream.sendText($0) },
                onKey: { key, code, keyCode, text in
                    stream.sendKey(key: key, code: code, keyCode: keyCode, text: text)
                }
            )
            .frame(width: Theme.Size.line, height: Theme.Size.line)
            .opacity(0)
            .allowsHitTesting(false)
            .accessibilityHidden(true)

            if let image = stream.frameImage {
                let rect = displayedRect
                Image(uiImage: image)
                    .resizable()
                    .interpolation(.high)
                    .frame(width: max(Theme.Size.line, rect.width), height: max(Theme.Size.line, rect.height))
                    .opacity(isDimmed ? Theme.Fade.unavailable : 1)
                    .accessibilityLabel(stream.pageTitle.isEmpty ? String(localized: "Web Pages") : stream.pageTitle)
                    .accessibilityIdentifier("preview-stream-frame")
            }

            if stream.frameImage == nil && !isStopped {
                VStack(spacing: Theme.Space.m) {
                    ProgressView()
                    Text("Opening the page on \(machineName)…")
                        .font(Theme.Font.secondary)
                        .foregroundStyle(.secondary)
                        .multilineTextAlignment(.center)
                }
                .padding(Theme.Space.xl)
                .accessibilityIdentifier("preview-stream-loading")
            }

            if case .reconnecting = stream.connection {
                VStack {
                    HStack(spacing: Theme.Space.s) {
                        ProgressView()
                        Text("Reconnecting…")
                            .font(Theme.Font.captionMedium)
                            .foregroundStyle(.secondary)
                    }
                    .padding(.horizontal, Theme.Space.m)
                    .padding(.vertical, Theme.Space.s)
                    .surface(in: .capsule)
                    .padding(.top, Theme.Space.s)
                    Spacer(minLength: Theme.Space.flush)
                }
                .accessibilityIdentifier("preview-stream-reconnecting")
            }

            if isStopped {
                stoppedOverlay
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .contentShape(.rect)
        .gesture(pointerGesture)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("preview-stream-canvas")
        .onGeometryChange(for: CGSize.self) { proxy in
            CGSize(width: proxy.size.width.rounded(), height: proxy.size.height.rounded())
        } action: { newSize in
            guard newSize.width > 0, newSize.height > 0, newSize != containerSize else { return }
            containerSize = newSize
            stream.start(viewportSize: newSize, displayScale: displayScale)
        }
        .onChange(of: displayScale) { _, scale in
            guard containerSize.width > 0, containerSize.height > 0 else { return }
            stream.updateViewport(size: containerSize, displayScale: scale)
        }
        .onDisappear {
            keyboardActive = false
            stream.stop()
        }
    }

    private var pointerGesture: some Gesture {
        DragGesture(minimumDistance: 0, coordinateSpace: .local)
            .onChanged { value in
                guard !isStopped else { return }
                let dist = hypot(value.translation.width, value.translation.height)
                if !dragging && dist > Theme.Space.sm {
                    dragging = true
                    lastDragLocation = value.startLocation
                }
                if dragging {
                    let delta = CGSize(
                        width: value.location.x - lastDragLocation.x,
                        height: value.location.y - lastDragLocation.y
                    )
                    lastDragLocation = value.location
                    stream.scroll(delta: delta, at: value.location, in: displayedRect)
                }
            }
            .onEnded { value in
                let wasDragging = dragging
                dragging = false
                guard !isStopped, !wasDragging else { return }
                stream.tap(at: value.location, in: displayedRect)
            }
    }

    private var isStopped: Bool {
        switch stream.connection {
        case .error, .closed: true
        default: false
        }
    }

    private var isDimmed: Bool {
        switch stream.connection {
        case .reconnecting, .error, .closed: true
        default: false
        }
    }

    // MARK: Banners & stopped states

    @ViewBuilder
    private func streamBanner(_ banner: PreviewStreamBanner) -> some View {
        switch banner {
        case .failed(_, let error):
            NoticeBanner(
                text: String(localized: "Couldn't load the page: \(error)"),
                tone: .caution,
                dismiss: { stream.dismissBanner() }
            ) {
                Button { stream.reload() } label: { Text("Refresh").lineLimit(1) }
            }
        case .crashed:
            NoticeBanner(
                text: String(localized: "The page stopped unexpectedly."),
                tone: .danger,
                dismiss: { stream.dismissBanner() }
            ) {
                Button { stream.reload() } label: { Text("Refresh").lineLimit(1) }
            }
        case .notice(let code):
            NoticeBanner(
                text: noticeText(for: code),
                tone: .neutral,
                dismiss: { stream.dismissBanner() }
            )
        }
    }

    private func noticeText(for code: String) -> String {
        switch code {
        case "file_chooser":
            String(localized: "This page asked to choose a file, which isn't supported when viewing through the computer.")
        default:
            code
        }
    }

    @ViewBuilder
    private var stoppedOverlay: some View {
        ScrollView {
            stoppedEmptyState
                .containerRelativeFrame(.vertical)
        }
        .background(Theme.pageBackground)
        .accessibilityIdentifier("preview-stream-error")
    }

    @ViewBuilder
    private var stoppedEmptyState: some View {
        switch stream.connection {
        case .closed:
            EmptyStateView(
                symbol: Theme.Icon.screen,
                title: String(localized: "Preview Closed"),
                message: String(localized: "The browser tab on your computer was closed."),
                action: (title: String(localized: "Reopen"), run: { stream.reconnect() }),
                secondaryAction: (title: String(localized: "View directly"), run: onSwitchToDirect)
            )
        case .error(let code, let message):
            switch code {
            case "no_browser":
                EmptyStateView(
                    symbol: Theme.Icon.screen,
                    title: String(localized: "No Browser on This Computer"),
                    message: String(localized: "Install Chrome, Chromium, Edge, or Brave on \(machineName) to view pages through the computer."),
                    action: (title: String(localized: "View directly"), run: onSwitchToDirect),
                    tint: Theme.caution
                )
            case "node_outdated":
                EmptyStateView(
                    symbol: Theme.Icon.info,
                    title: String(localized: "Update This Computer to Preview"),
                    message: String(localized: "Persona on \(machineName) is too old to view pages through the computer. Update it and try again."),
                    action: (title: String(localized: "View directly"), run: onSwitchToDirect),
                    tint: Theme.caution
                )
            case "port_gone":
                EmptyStateView(
                    symbol: Theme.Icon.error,
                    title: String(localized: "Server Stopped"),
                    message: String(localized: "The service on port :\(String(stream.port)) has stopped."),
                    action: (title: String(localized: "Retry"), run: { stream.reconnect() }),
                    tint: Theme.caution
                )
            case "offline":
                EmptyStateView(
                    symbol: Theme.Icon.offline,
                    title: String(localized: "Computer Offline"),
                    message: String(localized: "This computer is offline right now."),
                    action: (title: String(localized: "Retry"), run: { stream.reconnect() }),
                    tint: Theme.caution
                )
            default:
                EmptyStateView(
                    symbol: Theme.Icon.error,
                    title: String(localized: "Couldn't Open Preview"),
                    message: message.isEmpty ? String(localized: "Couldn't reach the server on your computer. Make sure it's still running and try again.") : message,
                    action: (title: String(localized: "Retry"), run: { stream.reconnect() }),
                    tint: Theme.caution,
                    secondaryAction: (title: String(localized: "View directly"), run: onSwitchToDirect)
                )
            }
        default:
            EmptyView()
        }
    }
}

// MARK: - Native <select> picker sheet

/// Native SwiftUI picker sheet rendered when the remote headless browser sends `select_open`.
struct PreviewSelectSheet: View {
    var state: PreviewSelectState
    var groups: [PreviewSelectGroup]
    var onChoose: (Int) -> Void
    var onCancel: () -> Void

    var body: some View {
        NavigationStack {
            Group {
                if state.options.isEmpty {
                    Text("No options available.")
                        .font(Theme.Font.secondary)
                        .foregroundStyle(.secondary)
                        .frame(maxWidth: .infinity, maxHeight: .infinity)
                        .background(Theme.pageBackground)
                } else {
                    List {
                        ForEach(groups) { group in
                            Section {
                                ForEach(group.options) { opt in
                                    optionRow(opt)
                                }
                            } header: {
                                if let title = group.title {
                                    Text(title)
                                }
                            }
                        }
                    }
                    .listStyle(.insetGrouped)
                }
            }
            .navigationTitle(String(localized: "Choose an Option"))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                SheetClose(id: "preview-select-close") { onCancel() }
            }
            .accessibilityIdentifier("preview-select-sheet")
        }
        .presentationDetents([.medium, .large])
        .presentationDragIndicator(.visible)
    }

    private func optionRow(_ opt: PreviewSelectOption) -> some View {
        let isSelected = opt.index == state.selected
        return Button {
            Haptics.tap()
            onChoose(opt.index)
        } label: {
            HStack(spacing: Theme.Space.m) {
                Text(opt.label)
                    .font(Theme.Font.body)
                    .foregroundStyle(opt.disabled ? AnyShapeStyle(.secondary) : AnyShapeStyle(Color.primary))
                    .frame(maxWidth: .infinity, alignment: .leading)
                if isSelected {
                    ThemedIcon(Theme.Icon.done)
                        .font(Theme.Font.bodyStrong)
                        .foregroundStyle(opt.disabled ? AnyShapeStyle(.secondary) : AnyShapeStyle(Theme.accent))
                        .accessibilityHidden(true)
                }
            }
            .contentShape(.rect)
        }
        .disabled(opt.disabled)
        .accessibilityIdentifier("preview-select-option-\(opt.index)")
        .accessibilityAddTraits(isSelected ? .isSelected : [])
    }
}

// MARK: - Hidden keyboard input bridge

/// Hidden `UITextField` forwarding software/IME text, paste, Backspace, and Return into the remote browser tab.
private struct StreamKeyboardInput: UIViewRepresentable {
    @Binding var isActive: Bool
    var onText: (String) -> Void
    var onKey: (String, String, Int, String?) -> Void

    func makeUIView(context: Context) -> SentinelTextField {
        let field = SentinelTextField()
        field.autocorrectionType = .no
        field.autocapitalizationType = .none
        field.spellCheckingType = .no
        field.smartQuotesType = .no
        field.smartDashesType = .no
        field.returnKeyType = .default
        field.delegate = context.coordinator
        field.onDeleteBackward = { [weak coord = context.coordinator] in
            coord?.onKey("Backspace", "Backspace", 8, nil)
        }
        field.addTarget(context.coordinator, action: #selector(Coordinator.textDidChange(_:)), for: .editingChanged)
        return field
    }

    func updateUIView(_ field: SentinelTextField, context: Context) {
        context.coordinator.onText = onText
        context.coordinator.onKey = onKey
        context.coordinator.onActiveChange = { active in
            if isActive != active {
                Task { @MainActor in isActive = active }
            }
        }
        if isActive && !field.isFirstResponder {
            field.becomeFirstResponder()
        } else if !isActive && field.isFirstResponder {
            field.resignFirstResponder()
        }
    }

    func makeCoordinator() -> Coordinator {
        Coordinator(onText: onText, onKey: onKey)
    }

    final class Coordinator: NSObject, UITextFieldDelegate {
        var onText: (String) -> Void
        var onKey: (String, String, Int, String?) -> Void
        var onActiveChange: ((Bool) -> Void)?

        init(onText: @escaping (String) -> Void, onKey: @escaping (String, String, Int, String?) -> Void) {
            self.onText = onText
            self.onKey = onKey
        }

        @objc func textDidChange(_ field: UITextField) {
            // Wait for IME composition to finish before forwarding committed text.
            guard field.markedTextRange == nil else { return }
            let raw = field.text ?? ""
            guard !raw.isEmpty else { return }
            field.text = ""
            onText(raw)
        }

        func textFieldShouldReturn(_ textField: UITextField) -> Bool {
            onKey("Enter", "Enter", 13, "\r")
            return false
        }

        func textFieldDidEndEditing(_ textField: UITextField) {
            onActiveChange?(false)
        }
    }

    final class SentinelTextField: UITextField {
        var onDeleteBackward: (() -> Void)?

        override func deleteBackward() {
            let empty = (text ?? "").isEmpty && markedTextRange == nil
            super.deleteBackward()
            if empty { onDeleteBackward?() }
        }
    }
}
#endif
