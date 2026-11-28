import Foundation
import PersonaWire

/// The mark before a row's second line (proposal 3.4): ● needs you, ◐ working, ✓ just finished, ◷ waiting; To do and the
/// rest have none.
nonisolated enum TodoMark: Equatable, Sendable { case needs, working, ready, waiting }

/// What a row draws, worked out in the list's model when the row's facts, view or the clock change, never in a view's
/// body (docs/apple/performance.md P4).
nonisolated struct TodoRowContent: Equatable, Sendable {
    var id: String
    var title: String
    /// A high-priority to-do: "!" before its title in the caution tone, as Reminders marks it; spoken "High priority".
    var highPriority = false
    /// The tick drawn ticked: the to-do is closed.
    var closed = false
    /// At the title's end, in words that say what they are: "Due Fri", or how long ago it came to where it is or closed
    /// ("16m ago"); none when line 2 says a time.
    var trailing: String?
    var overdue = false
    /// Finished work nobody has seen: the unread dot before `trailing`.
    var unseen = false
    var mark: TodoMark?
    /// Line 2 in words; two asks or more end it with how many ("· 2 asks").
    var said: TodoLineText.Said?
    /// How many asks hold it in Needs you.
    var asks = 0
    /// Line 3, in caption: what line 2 and the grouping do not say (its steps, the Inactive mark, with its project), or
    /// nil.
    var caption: String?
    var inactive = false
    /// Ticked a moment ago (`TodoOutbox`): struck through, the tick drawn ticked, until the hub has it closed.
    var struck = false
    /// Made on this device and not on the hub yet: line 2 says so, and the row neither opens nor has actions but Try
    /// Again and Remove when `unsure`.
    var saving = false
    /// A new to-do nobody knows the hub made: "Not sure it was saved", with Try Again and Remove.
    var unsure = false
    /// Line 2 is this device's own word on the owner's write ("Saving…", "Not on your hub yet"), in the tertiary color.
    var unsaved = false
    /// Persona or an Agent works on it: the tick asks first ("Mark it done and stop?").
    var working = false
    /// An Agent's question, plan or tool waits for the owner on it: the tick asks first too.
    var agentWaits = false
    /// In Someday: its swipe and menu take it out again.
    var someday = false
    /// Finished and seen, still open ("Finished Tue"): line 2 ends in Mark Done.
    var markDone = false
    /// Its answers at its foot, after line 3 (OK or Retry, and Options ⌄), and what this device did with its ask ("✓ You
    /// chose … · Undo" in line 2's place); none when its ask is answered only where it lives.
    var answers: TodoRowAnswers?
    /// The row in words, for VoiceOver: the title, the state its mark draws, its line (with how many asks), line 3, then
    /// its time said in full ("Due Saturday", "16 minutes ago").
    var spoken = ""
    /// What `spoken` says before and after its line, so a search's result found by its notes says the sentence it was
    /// found by in the line's place without the row's words worked out again (`spoken(_:)`).
    var spokenAround = TodoSpokenAround()
    /// The tick's value: Done or Not done (its label is the title).
    var tickValue = ""

    /// Line 2 of a search's result (`TodosListModel.Row.search`): the sentence of the notes it was found by, unless line 2
    /// says this device's own word on a write; nil for one found by its title, which keeps its own line.
    func found(by search: TodoSearchHit?) -> TodoFoundLine? {
        unsaved ? nil : search?.found
    }

    /// Whether line 2 ends in Mark Done: never on a search's result, which is found to be opened, its words half read
    /// beside it; its tick, the row's one deliberate control, stays.
    func offersMarkDone(_ search: TodoSearchHit?) -> Bool {
        markDone && search == nil
    }

    /// Its answers as the row draws them and VoiceOver offers them: none on a search's result, as no Mark Done
    /// (`offersMarkDone`), so no OK or Retry, no Options or its card and no Undo; they come back once the field is
    /// cleared. What this device did with the ask (`answers?.mark`) is not an answer but a fact: line 2 keeps saying it.
    func answers(_ search: TodoSearchHit?) -> TodoRowAnswers? {
        search == nil ? answers : nil
    }

    /// The row in words as it is drawn: `spoken`, the line or the answer given here kept on a result found by its
    /// title; one found by its notes says the sentence in the line's place: the title, the state its mark draws, the
    /// sentence, line 3, the time.
    func spoken(_ search: TodoSearchHit?) -> String {
        guard let found = found(by: search) else { return spoken }
        return [spokenAround.head, found.text, spokenAround.tail].filter { !$0.isEmpty }.joined(separator: spokenAround.separator)
    }
}

/// The words `TodoRowContent.spoken` says around its line: the title, High priority and the state before it; line 3 and
/// the time after it.
nonisolated struct TodoSpokenAround: Equatable, Sendable {
    var head = ""
    var tail = ""
    var separator = ""
}

/// What a To-dos row offers besides opening it (#10465): its swipe, its long press and VoiceOver's actions.
nonisolated enum TodoRowAction: String, Equatable, Sendable {
    case done, someday, outOfSomeday, undo, reopen, tryAgain, remove
    case today, date, project, copyLink, delete
}

/// Which of them a row offers, from its content, so the swipe, the menu and VoiceOver's actions agree.
nonisolated enum TodoRowActionList {
    /// The trailing swipe, the full swipe first: Done and Someday (Remove from Someday in Someday); Undo on a row ticked a
    /// moment ago; Reopen on a closed one; Try Again and Remove on a new one nobody knows the hub made; none while a new
    /// one is being saved.
    static func swipe(_ c: TodoRowContent) -> [TodoRowAction] {
        if c.unsure { return [.tryAgain, .remove] }
        if c.saving { return [] }
        if c.struck { return [.undo] }
        if c.closed { return [.reopen] }
        return [.done, c.someday ? .outOfSomeday : .someday]
    }

    /// The long press: Undo or Reopen first where the swipe has it, else Today, Date…, Project and Someday; then Copy
    /// Link and Delete….
    static func menu(_ c: TodoRowContent) -> [TodoRowAction] {
        if c.unsure { return [.tryAgain, .remove] }
        if c.saving { return [] }
        if c.struck { return [.undo, .copyLink, .delete] }
        if c.closed { return [.reopen, .copyLink, .delete] }
        return [.today, .date, .project, c.someday ? .outOfSomeday : .someday, .copyLink, .delete]
    }

    /// VoiceOver's and Switch Control's actions on the row: the swipe's, then Delete…; the menu's other items are its
    /// "Show menu" on the same element.
    static func spoken(_ c: TodoRowContent) -> [TodoRowAction] {
        let swipe = swipe(c)
        return swipe.isEmpty || c.saving ? swipe : swipe + [.delete]
    }
}

/// The owner's write on a row still on this device (`TodoOutbox`), as the row says it.
nonisolated struct TodoRowWrite: Equatable, Sendable {
    var write: TodoOutbox.Write
    /// It could not reach the hub yet, or nobody knows whether it did: it goes again when the hub next answers.
    var waits = false
    /// A new to-do nobody knows the hub made.
    var unsure = false
}

/// The order inside each section (⋯ › Sort By, kept on the device as `todos.v1.sort`): the hub's, by its rank, unless
/// the owner picks the most recently changed first (the old page's order), the earliest due first (the one way to order
/// Working and Waiting by date) or the high-priority ones first, which their rows mark. A sort never moves a section, and
/// Needs you and Can wait keep the hub's order, its deadlines first, and closed rows their newest first, under every
/// sort. A kept value this build does not offer reads as Suggested.
nonisolated enum TodosListSort: String, CaseIterable, Identifiable, Sendable {
    case suggested, activity, due, priority
    var id: String { rawValue }

    var title: String {
        switch self {
        case .suggested: String(localized: "Suggested [To-dos Sort]")
        case .activity: String(localized: "Recently Changed [To-dos Sort]")
        case .due: String(localized: "Due Date [To-dos Sort]")
        case .priority: String(localized: "Priority [To-dos Sort]")
        }
    }
}

/// One block of the list, one card: a section with its header (or, in the calm list, none), its rows and the folds at
/// its foot ("Can wait · 1"); or a tail group (Someday, Done today) whose header is its fold, its rows drawn only while
/// it is open.
nonisolated struct TodosListGroup: Identifiable, Equatable, Sendable {
    nonisolated struct Fold: Identifiable, Equatable, Sendable {
        /// The section it folds: `TodoListSection.canWait`, `someday` or `done`.
        var id: String
        var title: String
        var keys: [String]
        var open: Bool
    }

    var id: String
    /// nil: no header.
    var title: String?
    var count = 0
    /// The count in the needs-you tone (Needs you's N); the others neutral.
    var needs = false
    var keys: [String]
    var folds: [Fold] = []
    /// A tail group's fold: its header, and its rows while open.
    var tail: Fold?

    /// The keys it draws: its rows, then those of its open folds.
    var drawnKeys: [String] {
        keys + folds.filter(\.open).flatMap(\.keys) + (tail.map { $0.open ? $0.keys : [] } ?? [])
    }
}

/// The list as drawn: its groups for the Filter's choice, the search, the sort and ⋯'s Group by Project, worked out once
/// per change of the filing (`TodoCountsSource.filed`) or of a choice, in the model (P4). Pure.
nonisolated enum TodosListLayout {
    struct Input {
        /// The current assistant's rows as filed, in the list's order.
        var rows: [TodoFiled]
        var items: [String: TodoItem]
        var choice: TodosChoice = .all
        /// ⋯'s Group by Project: below Needs you, a group per project in place of the other sections (never in the calm
        /// list, nor under Needs You or Done, which draw one section or the days).
        var byProject = false
        var sort: TodosListSort = .suggested
        /// The folds the owner opened.
        var unfolded: Set<String> = []
        /// The search field's words: while there are any, the list is All's rows, open and closed, that `hits` holds,
        /// every fold open, and the choice waits until the field is cleared.
        var query = ""
        /// What the search found for `query`, by to-do id: the model's look, off the main actor (`TodoSearch.find`), so
        /// the layout never looks through the to-dos itself (P4).
        var hits: [String: TodoSearchHit] = [:]
        var now = Date()
        var calendar = Calendar.current
        var projectName: (String) -> String = { $0 }
    }

    struct Output: Equatable {
        var groups: [TodosListGroup] = []
        /// The calm rule holds: no Filter, no headers.
        var calm = false
        /// The ones closed today (the Filter's Done says how many).
        var doneToday = 0
        /// The search field has words: the rows are what they match.
        var searching = false
        /// Every row's section, by key: what the hold compares to tell a new need apart.
        var sections: [String: TodoListSection] = [:]
        static let empty = Output()
    }

    /// A row's identity in the list: its manager and its id, unique in a merged list (and the list's tie-breakers after
    /// the rank, `TodoFiling.ordered`).
    static func key(_ row: TodoFiled) -> String { (row.manager ?? "") + "/" + row.id }

    /// The sections below Needs you, in the list's order: the ones Group by Project files under their projects'
    /// headings. Needs you heads the list and Someday and Done today fold after the last card, so no project heading
    /// names their rows' projects.
    static let underProjects: [TodoListSection] = [.working, .waiting, .todo, .justFinished]

    static func layout(_ input: Input) -> Output {
        let carried = TodoCountsFallback.carried(input.rows)
        var rows = input.rows.filter { !carried.contains($0.id) }
        var out = Output()
        for row in rows { out.sections[key(row)] = row.section }
        let closedToday = { (row: TodoFiled) in
            input.items[row.id]?.doneAt.map { input.calendar.isDate($0, inSameDayAs: input.now) } ?? false
        }
        out.doneToday = rows.count(where: { $0.section == TodoListSection.done && closedToday($0) })
        let present = Set(rows.map(\.section))
        out.calm = TodosFilter.calm(Array(present))
        // A search looks through every row, open and closed, whatever the choice, its folds open. The device holds every
        // to-do (`GET /api/v1/todos` returns them all), so it is the whole answer: there is nothing for the hub to add.
        let words = TodoSearch.words(input.query)
        out.searching = !words.isEmpty
        if out.searching { rows = rows.filter { input.hits[$0.id] != nil } }
        let choice: TodosChoice = out.calm || out.searching ? .all : input.choice
        let opened = { (section: TodoListSection) in out.searching || input.unfolded.contains(section.rawValue) }
        let shown = Set(choice.sections)
        var bySection: [TodoListSection: [TodoFiled]] = [:]
        for row in rows where shown.contains(row.section) { bySection[row.section, default: []].append(row) }
        // What needs the owner keeps the hub's order (its deadlines first) under every sort, and what closed its newest
        // first, since a closed row shows neither a due day nor its priority; the sort orders the rest.
        let hubsOrder: Set<TodoListSection> = [.needsYou, .canWait]
        func sorted(_ list: [TodoFiled]) -> [String] {
            if list.allSatisfy({ hubsOrder.contains($0.section) }) { return list.map(key) }
            if list.allSatisfy({ $0.section == TodoListSection.done }) { return newestFirst(list, input).map(key) }
            return order(list, input).map(key)
        }
        func fold(_ section: TodoListSection, title: String, _ list: [TodoFiled]) -> TodosListGroup.Fold? {
            list.isEmpty ? nil : .init(id: section.rawValue, title: title, keys: sorted(list), open: opened(section))
        }
        func tail(_ fold: TodosListGroup.Fold?) -> TodosListGroup? {
            fold.map { TodosListGroup(id: "tail:" + $0.id, keys: [], tail: $0) }
        }
        let canWait = fold(TodoListSection.canWait, title: String(localized: "Can wait [To-dos List]"),
                           bySection[TodoListSection.canWait] ?? [])
        let someday = fold(TodoListSection.someday, title: StateName.todoSomeday, bySection[TodoListSection.someday] ?? [])
        let doneToday = fold(TodoListSection.done, title: String(localized: "Done today [To-dos List]"),
                             (bySection[TodoListSection.done] ?? []).filter(closedToday))
        // What closed: Done today folded under All; while searching, every closed match last, by the day it closed as
        // the Filter's Done lists them, its headings saying Done.
        let done = out.searching ? byDay(bySection[TodoListSection.done] ?? [], input, titles: searchedDays)
            : [tail(doneToday)].compactMap { $0 }

        if out.calm {
            // One headerless card; Someday and Done today (a search's closed matches) close the list as their own groups.
            out.groups = [TodosListGroup(id: "plain", keys: sorted(bySection[TodoListSection.todo] ?? []))]
                + [tail(someday)].compactMap { $0 } + done
            return out
        }
        if choice == .done {
            out.groups = byDay(bySection[TodoListSection.done] ?? [], input)
            return out
        }

        var groups: [TodosListGroup] = []
        // Needs you heads the list in either grouping, Can wait folded at its foot, inside its card.
        let needs = bySection[TodoListSection.needsYou] ?? []
        if !needs.isEmpty || canWait != nil {
            // Its header says N, as the drawer does (the page reads it from the store, never held); while searching, what
            // matches.
            groups.append(TodosListGroup(id: TodoListSection.needsYou.rawValue, title: StateName.todoNeedsYou, count: needs.count,
                                         needs: true, keys: sorted(needs), folds: [canWait].compactMap { $0 }))
        }
        let rest = underProjects.filter(shown.contains)
        if input.byProject {
            groups += byProject(rest.flatMap { bySection[$0] ?? [] }, input)
        } else {
            for section in rest {
                let list = bySection[section] ?? []
                if !list.isEmpty {
                    groups.append(TodosListGroup(id: section.rawValue, title: title(section), count: list.count, keys: sorted(list)))
                }
            }
        }
        // Someday, then (under All) Done today: folds of their own after the last card; while searching, the closed
        // matches by day.
        groups += [tail(someday)].compactMap { $0 } + (choice == .all ? done : [])
        out.groups = groups
        return out
    }

    static func title(_ section: TodoListSection) -> String {
        switch section {
        case .needsYou: StateName.todoNeedsYou
        case .canWait: String(localized: "Can wait [To-dos List]")
        case .working: StateName.taskWorking
        case .waiting: StateName.taskWaiting.text
        case .todo: StateName.todoToDo
        case .justFinished: StateName.justFinished.text
        case .someday: StateName.todoSomeday
        case .done: StateName.done.text
        // Never drawn: a row whose view names a section this build does not know is filed by the app's own rules
        // (`TodoFiling.knows`), so no group has one. It reads as the open set's catch-all did.
        case .unknown: StateName.done.text
        }
    }

    /// `list` in the sort's order; the hub's (as filed) for Suggested and for every tie.
    static func order(_ list: [TodoFiled], _ input: Input) -> [TodoFiled] {
        let item = { (row: TodoFiled) in input.items[row.id] }
        switch input.sort {
        case .suggested:
            return list
        case .activity:
            return stable(list) { a, b in
                let x = item(a).flatMap { $0.updatedAt ?? $0.createdAt } ?? .distantPast
                let y = item(b).flatMap { $0.updatedAt ?? $0.createdAt } ?? .distantPast
                return x == y ? nil : x > y
            }
        case .due:
            return stable(list) { a, b in
                let x = item(a).flatMap { TodosLogic.dueBy($0, calendar: input.calendar) } ?? .distantFuture
                let y = item(b).flatMap { TodosLogic.dueBy($0, calendar: input.calendar) } ?? .distantFuture
                return x == y ? nil : x < y
            }
        case .priority:
            // High, then normal (none said), then low; the hub's order inside each.
            let weight = { (row: TodoFiled) in ["high": 0, "low": 2][item(row)?.priority ?? ""] ?? 1 }
            return stable(list) { a, b in weight(a) == weight(b) ? nil : weight(a) < weight(b) }
        }
    }

    /// Sorted by `before` (nil: a tie), ties kept in the order given.
    private static func stable(_ list: [TodoFiled], _ before: (TodoFiled, TodoFiled) -> Bool?) -> [TodoFiled] {
        list.enumerated().sorted { a, b in before(a.element, b.element) ?? (a.offset < b.offset) }.map(\.element)
    }

    /// A group per project, where its first row comes in the list, then the rows of none.
    private static func byProject(_ list: [TodoFiled], _ input: Input) -> [TodosListGroup] {
        var slugs: [String] = []
        var rows: [String: [TodoFiled]] = [:]
        for row in list {
            let slug = row.project ?? ""
            if rows[slug] == nil { slugs.append(slug) }
            rows[slug, default: []].append(row)
        }
        slugs = slugs.filter { !$0.isEmpty } + (rows[""] == nil ? [] : [""])
        return slugs.map { slug in
            let members = order(rows[slug] ?? [], input).map(key)
            return TodosListGroup(id: "project:" + slug,
                                  title: slug.isEmpty ? String(localized: "No project [To-dos List]") : input.projectName(slug),
                                  count: members.count, keys: members)
        }
    }

    /// Closed rows by when they closed, the newest first; ties in the order given.
    private static func newestFirst(_ list: [TodoFiled], _ input: Input) -> [TodoFiled] {
        stable(list) { a, b in
            let x = input.items[a.id]?.doneAt ?? .distantPast, y = input.items[b.id]?.doneAt ?? .distantPast
            return x == y ? nil : x > y
        }
    }

    /// The Filter's Done's headings: the Filter names Done.
    private static var filterDays: [(id: String, title: String)] {
        [("today", String(localized: "Today [To-dos Done]")), ("yesterday", String(localized: "Yesterday [To-dos Done]")),
         ("week", String(localized: "This week [To-dos Done]")), ("earlier", String(localized: "Earlier [To-dos Done]"))]
    }

    /// The same days' headings in a search's results, which name Done themselves: "Today" alone there reads as the
    /// Today place.
    private static var searchedDays: [(id: String, title: String)] {
        [("today", String(localized: "Done today [To-dos List]")), ("yesterday", String(localized: "Done yesterday [To-dos List]")),
         ("week", String(localized: "Done this week [To-dos List]")), ("earlier", String(localized: "Done earlier [To-dos List]"))]
    }

    /// Done by when each closed: today, yesterday, this week, earlier; in each, the newest first under every sort.
    private static func byDay(_ list: [TodoFiled], _ input: Input, titles days: [(id: String, title: String)] = filterDays)
        -> [TodosListGroup] {
        let cal = input.calendar
        let today = cal.startOfDay(for: input.now)
        var rows: [String: [TodoFiled]] = [:]
        for row in list {
            let at = input.items[row.id]?.doneAt ?? .distantPast
            let ago = cal.dateComponents([.day], from: cal.startOfDay(for: at), to: today).day ?? 99
            let id = ago <= 0 ? "today" : ago == 1 ? "yesterday" : ago <= 6 ? "week" : "earlier"
            rows[id, default: []].append(row)
        }
        return days.compactMap { d in
            guard let members = rows[d.id], !members.isEmpty else { return nil }
            let ordered = newestFirst(members, input)
            return TodosListGroup(id: "done:" + d.id, title: d.title, count: ordered.count, keys: ordered.map(key))
        }
    }
}

/// A row's content from its facts and its view (#10465): the trailing time, the mark, the line in words, line 3 and the
/// row in words. Pure.
nonisolated enum TodoRowContentLogic {
    /// `pending`: the owner's write on it still on the device (`TodoOutbox`): a tick is drawn at once, struck through; a
    /// new to-do says it is being saved; one that could not reach the hub says so.
    /// `inProject`: the place shown is the row's project, whose title names it, so line 3 leaves it out.
    /// `underItsDay`: the row is under a heading that names its due day (Upcoming), so it says only its time, if any;
    /// an overdue one, under Overdue, keeps its day. VoiceOver still says the day, the row being heard on its own.
    /// `answered`: what this device did with its ask (`AskCardModel.mark`).
    static func content(_ filed: TodoFiled, item: TodoItem, view: TodoSummary?, byProject: Bool = false, inProject: Bool = false,
                        underItsDay: Bool = false, now: Date, words: TodoWords = TodoWords(), calendar: Calendar = .current,
                        projectName: (String) -> String = { $0 }, pending: TodoRowWrite? = nil,
                        answered: TodoAnswerMark? = nil) -> TodoRowContent {
        var c = TodoRowContent(id: filed.id, title: item.title, closed: filed.closed, inactive: filed.inactive)
        c.highPriority = item.priority == "high"
        let since = view?.since.flatMap(JSON.parseDate)
        // The time as drawn at the title's end, and as VoiceOver says it; `aged`: it says how long ago, which a line that
        // says a time ("since Tue") already does.
        var heard: [String] = []
        var aged = false
        if let due = TodosLogic.dueBy(item, calendar: calendar), !filed.closed {
            let day = item.dueAt ?? TodosLogic.dueDay(item, calendar: calendar) ?? due
            c.overdue = due < now
            if underItsDay && !c.overdue {
                c.trailing = item.dueAt.map { TodoTimeWords.dueTime($0, words: words, calendar: calendar) }
            } else {
                c.trailing = TodoTimeWords.due(day, now: now, words: words, calendar: calendar)
            }
            if c.overdue { heard.append(words("Overdue [To-dos Spoken]")) }
            heard.append(TodoTimeWords.spokenDue(day, now: now, words: words, calendar: calendar))
        } else if filed.closed, let done = item.doneAt {
            c.trailing = TodoTimeWords.age(done, now: now, words: words, calendar: calendar)
            heard.append(now.timeIntervalSince(done) < 60 ? words("Done just now [To-dos Spoken]")
                : words("Done \(TodoTimeWords.spokenAge(done, now: now, words: words, calendar: calendar)) [To-dos Spoken]"))
            aged = true
        } else if ![TodoListSection.todo, TodoListSection.someday].contains(filed.section),
                  let at = since ?? item.updatedAt ?? item.createdAt {
            c.trailing = TodoTimeWords.age(at, now: now, words: words, calendar: calendar)
            heard.append(TodoTimeWords.spokenAge(at, now: now, words: words, calendar: calendar))
            aged = true
        }
        c.unseen = filed.section == TodoListSection.justFinished && view?.seen != true
        c.mark = mark(filed, view: view)
        if let wait = item.wait, filed.section == TodoListSection.waiting {
            let summary = TodoWaitText.rowSummary(wait, now: now, words: TodoWaitWords(locale: words.locale), calendar: calendar)
            c.said = TodoLineText.Said(lead: summary.lead, rest: summary.timing, timed: summary.timed)
            c.asks = view?.asks?.count ?? 0
        } else if let view {
            c.said = TodoLineText.said(view.line, since: since, closedReason: item.doneReason.flatMap(firstLine), now: now,
                                       words: words, calendar: calendar)
            c.asks = view.asks?.count ?? 0
            // A run's plan or tool approval is named by the request's title (DESIGN_SYSTEM 5.6), not by what it is about:
            // the row has no door, and its tap opens the to-do, where the request is.
            if view.line.map({ TodoLineKey(rawValue: $0.key) }) == .agentAsks, let ask = view.asks?.first, let title = requestTitle(ask, words: words) {
                let who = ask.run?.title.flatMap { $0.isEmpty ? nil : $0 } ?? view.line?.args?["run"] ?? words("An Agent [To-dos Line]")
                c.said = TodoLineText.Said(lead: words("\(who): \(title) [To-dos Line]"), tone: .needs)
            }
        }
        // A row filed by the app's own rules (an older manager's), and one whose line this build cannot say (a newer
        // manager's key), say their section's word: the mark is drawn on line 2, and Working must not lose its ◐.
        if c.said == nil { c.said = legacy(filed) }
        // The row is its own door to its asks (no Review button): two or more say how many at line 2's end.
        if c.asks > 1, [TodoListSection.needsYou, TodoListSection.canWait].contains(filed.section), var said = c.said {
            said.rest = [said.rest, words("\(c.asks) asks [To-dos List]")].compactMap { $0 }.joined(separator: TodoTimeWords.separator)
            c.said = said
        }
        if aged, c.said?.timed == true {
            c.trailing = nil
            heard.removeAll()
        }
        // A plain To do says the first line of its notes, which every client has (the hub sends no line for it).
        if c.said == nil, [TodoListSection.todo, TodoListSection.someday].contains(filed.section),
           let notes = item.notes.flatMap(firstLine) {
            c.said = TodoLineText.Said(rest: notes)
        }
        var facts: [String] = []
        if filed.inactive { facts.append(StateName.todoInactive) }
        if let steps = view?.steps, steps.total > 0 { facts.append(words("Steps \(steps.done)/\(steps.total) [To-dos List]")) }
        // The project only beside another fact: alone it is not worth a line, and Group by Project's heading says it for
        // the rows filed under it, never Needs you's at the top nor those of the Someday and Done today folds; a
        // project's place says it in its title for every row.
        let underItsProject = inProject || byProject && TodosListLayout.underProjects.contains(filed.section)
        if !facts.isEmpty, !underItsProject, let project = item.project, !project.isEmpty {
            facts.insert(projectName(project), at: 0)
        }
        c.caption = facts.isEmpty ? nil : facts.joined(separator: TodoTimeWords.separator)
        c.working = filed.section == TodoListSection.working && !filed.closed
        c.agentWaits = !filed.closed && (view?.asks ?? []).contains { $0.run != nil }
        c.someday = filed.section == TodoListSection.someday
        c.markDone = view?.line.map({ TodoLineKey(rawValue: $0.key) }) == .finishedSeen && !filed.closed
        c.answers = TodoRowAnswersLogic.answers(view, section: filed.section, closed: filed.closed, mark: answered)
        switch pending?.write {
        case .done(true)?:
            (c.closed, c.struck, c.markDone, c.mark) = (true, true, false, nil)
        case .done(false)?:
            c.closed = false
        case .create?:
            let said = pending?.unsure == true ? words("Not sure it was saved [To-dos List]")
                : pending?.waits == true ? words("Not on your hub yet [To-dos List]") : words("Saving… [To-dos List]")
            (c.saving, c.unsure, c.said, c.mark, c.caption) = (true, pending?.unsure == true, TodoLineText.Said(rest: said), nil, nil)
            c.unsaved = true
        default:
            break
        }
        // A change that could not reach the hub yet says so where the hub's line was, until it does.
        if let pending, pending.waits, pending.write.edits {
            (c.said, c.mark, c.markDone, c.unsaved) = (TodoLineText.Said(rest: words("Not on your hub yet [To-dos List]")), nil, false, true)
        }
        if c.closed { c.highPriority = false } // closed, or ticked a moment ago: nothing to put first
        if c.saving || c.struck { c.answers = nil } // nothing to answer on a row being saved or ticked a moment ago
        (c.spoken, c.spokenAround) = spoken(c, heard: heard, words: words)
        c.tickValue = c.closed ? StateName.done.text : words("Not done [To-dos Actions]")
        return c
    }

    /// The row in words: the title; High priority, where it is; the state its mark draws (its line may already say it);
    /// the line, with how many asks, or the answer given here in its place; line 3; the time. And what it says around the
    /// line, the state always (a search's sentence does not say it).
    private static func spoken(_ c: TodoRowContent, heard: [String], words: TodoWords) -> (String, TodoSpokenAround) {
        let separator = words.listSeparator
        var head = [c.title]
        if c.highPriority { head.append(words("High priority [To-dos Spoken]")) }
        let state: String? = switch c.mark {
        case .needs: StateName.todoNeedsYou
        case .working: StateName.taskWorking
        case .ready: StateName.justFinished.text
        case .waiting: StateName.taskWaiting.text
        case nil: c.struck || (c.closed && heard.isEmpty) ? StateName.done.text : nil
        }
        let tail = [c.caption].compactMap { $0 } + heard
        var parts = head
        if let state, c.said?.lead != state { parts.append(state) }
        switch c.answers?.mark {
        case .chosen(let label, _)?: parts.append(label)
        case .elsewhere?: parts.append(words("Answered elsewhere [To-dos Answers]"))
        case .unsure?: parts.append(words("Not sure your answer went through [To-dos Answers]"))
        default: if let said = c.said { parts.append(said.spoken(separator: separator)) }
        }
        return ((parts + tail).joined(separator: separator),
                TodoSpokenAround(head: (head + [state].compactMap { $0 }).joined(separator: separator),
                                 tail: tail.joined(separator: separator), separator: separator))
    }

    /// The request's title for a run's plan or tool approval (DESIGN_SYSTEM 5.6); nil for any other ask.
    private static func requestTitle(_ ask: OwnerAsk, words: TodoWords) -> String? {
        switch ask.kind {
        case OwnerAskKind.plan: words("Plan awaiting approval [To-dos Line]")
        case OwnerAskKind.tool: words("Waiting for your approval [To-dos Line]")
        default: nil
        }
    }

    /// Where it is, when no line can be said (no view, or a key this build does not know).
    private static func legacy(_ filed: TodoFiled) -> TodoLineText.Said? {
        switch filed.section {
        case .needsYou: TodoLineText.Said(lead: StateName.todoNeedsYou, tone: .needs)
        case .working: TodoLineText.Said(lead: StateName.taskWorking)
        case .waiting: TodoLineText.Said(lead: StateName.taskWaiting.text)
        case .justFinished: TodoLineText.Said(lead: StateName.justFinished.text, tone: .ready)
        case .canWait, .todo, .someday, .done, .unknown: nil
        }
    }

    static func mark(_ filed: TodoFiled, view: TodoSummary?) -> TodoMark? {
        switch filed.section {
        case .needsYou: view?.reason == .failedStart ? nil : .needs
        case .working: .working
        case .waiting: .waiting
        case .justFinished: filed.closed ? nil : .ready
        case .canWait, .todo, .someday, .done, .unknown: nil
        }
    }

    private static func firstLine(_ text: String) -> String? {
        let line = text.split(whereSeparator: \.isNewline).first.map { $0.trimmingCharacters(in: .whitespaces) } ?? ""
        return line.isEmpty ? nil : line
    }
}
