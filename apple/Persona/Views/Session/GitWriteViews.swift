import SwiftUI

/// Commit what is staged (#7013): the message, and the files going in. Opened from the Git tab's commit bar; the
/// message is the tab's, so it stays when the sheet is closed or the commit is refused, and goes once one is made.
/// While the commit runs (hooks can take minutes) the sheet can't be swiped away.
struct GitNewCommitSheet: View {
    @Environment(\.dismiss) private var dismiss
    let status: GitStatus
    @Binding var message: String
    @Bindable var writer: GitWriteModel
    /// Commits; the tab closes the sheet when it went through.
    let commit: () async -> Void
    @FocusState private var focused: Bool

    private var staged: [GitFile] { status.files.filter { GitStageMark($0) != .none } }
    private var committing: Bool { writer.busy == .commit }
    private var canCommit: Bool {
        !message.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty && !staged.isEmpty && status.branch != nil && writer.busy == nil
    }

    var body: some View {
        NavigationStack {
            List {
                Section {
                    TextField("Commit message", text: $message, axis: .vertical)
                        .lineLimit(3...10)
                        .focused($focused)
                        .disabled(committing)
                        .accessibilityIdentifier("git.commit.message")
                }
                Section {
                    ForEach(staged) { GitFileRow(file: $0) }
                } header: {
                    Text("\(staged.count) files going in")
                } footer: {
                    Text("The commit stays on this computer until you push.")
                }
            }
            .listStyle(.insetGrouped)
            .themedPage()
            .navigationTitle(Text("Commit"))
            .navigationSubtitle(status.branch ?? "")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button { dismiss() } label: { Text("Cancel").lineLimit(1) }
                        .disabled(committing)
                }
                ToolbarItem(placement: .confirmationAction) {
                    if committing {
                        ProgressView().accessibilityLabel(Text("Committing"))
                    } else {
                        Button { Task { await commit() } } label: { Text("Commit").lineLimit(1) }
                            .disabled(!canCommit)
                            .accessibilityIdentifier("git.commit.go")
                    }
                }
            }
            .gitRefusalAlert($writer.refusal)
        }
        .interactiveDismissDisabled(committing)
        .onAppear { focused = true }
    }
}

extension View {
    /// A refused Git write (#7013): its plain title, then the files it is about and git's own words (`shownDetail`), and OK. With
    /// stashes, a switch refused for the uncommitted files in its way also offers Stashes, to put them away (#7015):
    /// it only opens the page.
    func gitRefusalAlert(_ refusal: Binding<GitRefusal?>, stashes: (() -> Void)? = nil) -> some View {
        alert(refusal.wrappedValue?.title ?? "",
              isPresented: Binding(get: { refusal.wrappedValue != nil }, set: { if !$0 { refusal.wrappedValue = nil } }),
              presenting: refusal.wrappedValue) { r in
            if r.code == "git_conflict", let stashes {
                Button {
                    refusal.wrappedValue = nil
                    stashes()
                } label: { Text("Stashes").lineLimit(1) }
            }
            Button { refusal.wrappedValue = nil } label: { Text("OK [Recordings Section de32e2]").lineLimit(1) }
        } message: { r in
            Text(([r.files.joined(separator: "\n"), r.shownDetail ?? ""].filter { !$0.isEmpty }).joined(separator: "\n\n"))
        }
    }
}
