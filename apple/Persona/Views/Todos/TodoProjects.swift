import SwiftUI

// The knowledge-base projects a to-do can belong to (#2581, docs/assistant/todos.md "Project"), as the web's
// `lib/todo-projects.ts` and `groupTodosByProject`: the list read from the current assistant's knowledge base when a page
// that uses it opens, the detail's choices, and the to-dos list grouped by project.

/// A project as to-dos use it: its slug (what a to-do keeps), its name, and whether it is long-term (an area of work).
nonisolated struct TodoProject: Hashable, Identifiable, Sendable {
    var slug: String
    var name: String
    var long: Bool
    var id: String { slug }
}

/// Open to-dos of one project, or of none (`slug` "").
nonisolated struct TodoProjectSection: Identifiable, Sendable {
    var slug: String
    var title: String
    var todos: [TodoItem]
    var id: String { slug }
}

nonisolated enum TodoProjectLogic {
    /// The knowledge base's projects as to-dos use them: the hub lists no merged ones; "not a project" ones are left out.
    static func projects(_ items: [KBEntry]) -> [TodoProject] {
        items.filter { !$0.projectIgnored }.map {
            TodoProject(slug: $0.id, name: $0.name.isEmpty ? $0.id : $0.name, long: ($0.extra ?? "").hasPrefix("long"))
        }
    }

    /// What the picker offers: long-term projects first, then the rest, each by name; the to-do's own project stays
    /// even when the list does not have it (not read yet, or no knowledge base).
    static func choices(_ list: [TodoProject], current: String?) -> [TodoProject] {
        var out = list.sorted { a, b in
            a.long != b.long ? a.long : a.name.localizedStandardCompare(b.name) == .orderedAscending
        }
        if let current, !current.isEmpty, !out.contains(where: { $0.slug == current }) {
            out.insert(TodoProject(slug: current, name: current, long: false), at: 0)
        }
        return out
    }

    /// A project's name by its slug; the slug itself for one not (or not yet) in the list.
    static func name(_ slug: String, in list: [TodoProject]) -> String {
        list.first { $0.slug == slug }?.name ?? slug
    }

    /// Open to-dos by project, in the order given (the list's, most pressing first): a project's section comes where its
    /// first to-do does; those in none go last under Unsorted.
    static func sections(_ todos: [TodoItem], projects: [TodoProject]) -> [TodoProjectSection] {
        var bySlug: [String: [TodoItem]] = [:]
        var order: [String] = []
        for t in todos {
            let slug = t.project ?? ""
            if bySlug[slug] == nil, !slug.isEmpty { order.append(slug) }
            bySlug[slug, default: []].append(t)
        }
        var out = order.map { TodoProjectSection(slug: $0, title: name($0, in: projects), todos: bySlug[$0] ?? []) }
        if let none = bySlug[""] {
            out.append(TodoProjectSection(slug: "", title: String(localized: "Unsorted [Todo Project]"), todos: none))
        }
        return out
    }
}

/// The projects, read again whenever a page that uses them opens (a project made meanwhile shows up). The current
/// assistant's: another one made current reads its own, and until then the list is empty rather than the last one's.
@Observable final class TodoProjects {
    static let shared = TodoProjects()
    private(set) var list: [TodoProject] = []
    @ObservationIgnored private var scope: APIClient?

    func load(_ api: APIClient?) async {
        guard let api else { return }
        if !api.sameSettingsScope(as: scope) { list = [] }
        scope = api
        // No knowledge base (off, not open yet): no choices beyond the to-do's own; groups then show the slug.
        guard let items = try? await api.kbList(.projects).items, api.sameSettingsScope(as: scope) else { return }
        list = TodoProjectLogic.projects(items)
    }
}
