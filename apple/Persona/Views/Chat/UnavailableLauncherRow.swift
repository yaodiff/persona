import SwiftUI

extension UnavailableLauncher {
    /// What the pickers show, as `Launcher.title`: its own name, else its startup name (the web's `providerTitle`).
    var title: String {
        let trimmed = displayName?.trimmingCharacters(in: .whitespacesAndNewlines)
        let label = trimmed?.isEmpty == false ? trimmed : nil
        if kind != "api" {
            return Launcher.traeCLITitle(name: name, source: nil, agent: agent, baseURL: nil, displayName: label)
                ?? Launcher.builtinTitle(name: name, displayName: label)
        }
        return label ?? name
    }

    /// What New Agent's launcher list shows off after the launchers (#8109): the hub's `unavailable` for the computer
    /// (the Persona-managed providers their own-service rule leaves out there), in its order, less a name the list has
    /// as a launcher already or a second time, and of those only the ones a search matches by name or display name.
    static func shown(_ unavailable: [UnavailableLauncher], after launchers: [Launcher], query: String) -> [UnavailableLauncher] {
        var seen = Set(launchers.map(\.name))
        let q = query.trimmingCharacters(in: .whitespaces)
        return unavailable.filter { u in
            guard seen.insert(u.name).inserted else { return false }
            return q.isEmpty || u.name.localizedCaseInsensitiveContains(q) || u.title.localizedCaseInsensitiveContains(q)
        }
    }
}

/// A launcher or provider that can't run on this computer, with the hub's reason (`UnavailableLauncher`): the main
/// chat's model sheet (#5033) and New Agent's launcher list (#8109) draw it alike, gray under a lock. `fixable` adds
/// How to Fix under the reason, for a row that opens the provider's page.
struct UnavailableLauncherRow: View {
    var item: UnavailableLauncher
    var fixable = false

    var body: some View {
        HStack(alignment: .top, spacing: Theme.Space.m) {
            ThemedIcon(Theme.Icon.locked).foregroundStyle(.secondary)
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                Text(item.title).font(Theme.Font.heading).foregroundStyle(.secondary).lineLimit(1)
                Text(item.reason).font(Theme.Font.label).foregroundStyle(.tertiary)
                if fixable {
                    Text("How to Fix").font(Theme.Font.label).foregroundStyle(Theme.accent)
                }
            }
        }
    }
}
