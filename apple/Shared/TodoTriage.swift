import Foundation

// The To-dos page's sections (#3206, docs/assistant/todo-status-sop.md): each to-do's place comes from real signals only —
// its agents' sessions (alive, running, asking), its PR / delivery state, the latest task_review verdict and when it last
// moved — never from what an agent says about itself. The rules are the SOP's decision table, in its order; the first that
// holds wins. Mirrors web/src/lib/todo-triage.ts (and Android's TodoTriage.kt): a change to the rules changes all three.

/// The cases are in the page's order (#5707): what the owner has to act on, the unfinished work, what just finished, then
/// the folded ones, Done last. A merged row is led by its member in the first of these.
nonisolated enum TodoSection: String, CaseIterable, Sendable {
    /// Something only the owner can move: an answer, an approval, a decision, a retry.
    case waiting
    /// An agent is running on it now.
    case working
    /// Its work stopped at a pull request that is still open and nothing runs.
    case pr
    /// Open, recently active, none of the above.
    case other
    /// Closed, accepted or deployed in the last 24 hours, or a turn nobody has seen / a review not done yet; not seen
    /// by the owner since.
    case justDone = "just_done"
    /// A task with no activity for 48 hours and nothing running.
    case shelved
    /// Closed, accepted or deployed more than 24 hours ago or seen since; a finished turn seen since it last moved.
    case done

    /// Shelved and Done start folded; the rest start open, unfinished to-dos included.
    var startsFolded: Bool { self == .shelved || self == .done }

    /// The section's header (the web's `todos.section_*`).
    var title: String {
        switch self {
        case .waiting: StateName.needsYou.text
        case .working: StateName.working.text
        case .justDone: String(localized: "Just Finished")
        case .pr: String(localized: "PR to Merge")
        case .other: String(localized: "Other Open")
        case .shelved: String(localized: "Shelved")
        case .done: String(localized: "Done [To-do Sections]")
        }
    }
}

/// Open counts, shelved folds (no activity for 48 h, nothing running), closed is finished (`TodoTriageLogic.bucket`).
nonisolated enum TodoBucket: String, Sendable {
    case open, shelved, closed
}

/// Which SOP rule placed a to-do; the section follows from it.
nonisolated enum TodoTriageRule: String, Sendable {
    case failedStart = "failed_start", superseded, agentAsks = "agent_asks", ownerDecides = "owner_decides", agentRunning = "agent_running"
    case closed, accepted, deployed, finishedUnseen = "finished_unseen", prOpen = "pr_open", seen, stale, rest
}

nonisolated struct TodoTriage: Equatable, Sendable {
    var section: TodoSection
    var rule: TodoTriageRule
    /// The last moment anything of it moved (distantPast when nothing says).
    var last: Date
}

/// One row of the list: related to-dos merged, led by the most urgent member.
nonisolated struct TodoTriageGroup: Identifiable, Sendable {
    /// The lead's id: stable while the lead stays.
    var key: String
    var section: TodoSection
    var lead: TodoItem
    /// Every member, the lead first, then by section and recency.
    var members: [TodoItem]
    /// Each member's triage, by id.
    var triage: [String: TodoTriage]
    var last: Date

    var id: String { key }
    /// The others under the lead ("N related").
    var related: [TodoItem] { Array(members.dropFirst()) }
}

nonisolated struct TodoTriageResult: Sendable {
    var sections: [TodoSection: [TodoTriageGroup]]
    /// To-dos (not rows) per section.
    var todoCounts: [TodoSection: Int]
    /// What the header calls open (`countsAsOpen`), shelved, and finished (closed, accepted or deployed).
    var open: Int
    var shelved: Int
    var finished: Int

    func groups(_ section: TodoSection) -> [TodoTriageGroup] { sections[section] ?? [] }
    var isEmpty: Bool { sections.values.allSatisfy(\.isEmpty) }
}

nonisolated enum TodoTriageLogic {
    /// No activity for this long, nothing of it running: shelved.
    static let shelveAfter: TimeInterval = 48 * 3600
    /// Finished this recently, and not seen since: Just finished; earlier, Done. Seeing it (Got It, or opening it,
    /// #5707) files it at once; the time is the fallback.
    static let justDone: TimeInterval = 24 * 3600

    /// How a to-do's linked agents stand, the way the web's `todoStatus` reads them: an open ask does not count here
    /// (rules 3 and 8 read it), unlike `TaskState(todo:agents:)`.
    enum AgentsStatus: Equatable, Sendable {
        case failed, done, notStarted, needsYou, interrupted, usageLimit, unread, working, followUp
    }

    static func agentsStatus(_ todo: TodoItem, lookup: TodosLogic.SessionLookup?) -> AgentsStatus {
        if todo.failedToStart { return .failed }
        if todo.doneAt != nil { return .done }
        let active = todo.activeAgents
        if active.isEmpty { return .notStarted }
        var interrupted = false, limit = false, unread = false, working = false
        for a in active {
            if let e = a.startError, !e.isEmpty { continue } // a failed start counts as stopped
            let s = lookup?(a.machineID, a.sessionID)
            let st = s?.state ?? .offline
            if st == .needsYou { return .needsYou }
            let stopped = st == .unread || st == .idle
            let error = !(s?.turnError ?? "").isEmpty
            if error, s?.turnErrorKind == "usage_limit", stopped { limit = true }
            else if error, stopped { interrupted = true }
            else if st == .unread { unread = true }
            else if st == .working || s?.starting == true { working = true }
        }
        if interrupted { return .interrupted }
        if limit { return .usageLimit }
        if unread { return .unread }
        if working { return .working }
        return .followUp
    }

    /// The last moment the to-do or any of its agents moved: its own times, its supervision's (reviews, reports), its
    /// delivery and ask, and each linked agent's session. A restore from Shelved counts as a move.
    static func lastActivity(_ todo: TodoItem, lookup: TodosLogic.SessionLookup?, restoredAt: Date? = nil) -> Date {
        let sup = todo.supervision
        var times: [Date?] = [todo.updatedAt, todo.createdAt, todo.reopenedAt, todo.doneAt, sup?.updatedAt, sup?.progress?.at,
                              sup?.latestReview?.at, todo.delivery?.at, todo.ask.flatMap { JSON.parseDate($0.askedAt) }, restoredAt]
        for a in todo.agents where a.releasedAt == nil {
            if let s = lookup?(a.machineID, a.sessionID) { times += [s.updatedAt, s.stateSince] }
        }
        return times.compactMap { $0 }.max() ?? .distantPast
    }

    /// The PR its work stopped at, still to merge: the hub says the delivery stopped at a PR, or it knows a PR and no
    /// delivery state past it.
    static func openPR(_ todo: TodoItem) -> Bool {
        let d = todo.delivery?.state ?? ""
        if d == "pr" { return true }
        return !(todo.prURL ?? "").isEmpty && (d.isEmpty || d == "none")
    }

    /// A task (an agent was linked, or a conversation handed it out) with no due date ahead. The user's own to-dos
    /// without agents — reminders, errands — never shelve themselves.
    static func shelvable(_ todo: TodoItem, now: Date, calendar: Calendar = .current) -> Bool {
        if let due = TodosLogic.dueBy(todo, calendar: calendar), due >= now { return false }
        return !todo.agents.isEmpty || !(todo.origin ?? "").isEmpty
    }

    /// The open ask, as the web reads it (`openAsk`): one with a verdict and a time.
    private static func openAsk(_ todo: TodoItem) -> TodoAsk? {
        guard let a = todo.ask, !(a.verdict ?? "").isEmpty, !a.askedAt.isEmpty else { return nil }
        return a
    }

    /// A to-do's section by the SOP's decision table, first match wins:
    ///
    ///  1. failedStart    the task's agent never started                    → Waiting on you, Done after 48 h;
    ///     superseded     … and a related to-do (its row) really finished   → Just finished (24 h), then Done (#3713)
    ///  2. agentAsks      an agent asks (question, approval) or was cut off → Waiting on you
    ///  3. ownerDecides   review says needs_user, or a decision / "cannot" ask → Waiting on you
    ///  4. agentRunning   an agent is working or starting                   → Working
    ///  5. closed         done_at set                                       → Just finished (24 h), then Done
    ///  6. accepted       the latest review accepted it (even reopened)     → Just finished (24 h), then Done
    ///  7. deployed       its delivery is live                              → Just finished (24 h), then Done
    ///  8. finishedUnseen an agent finished unseen, awaits review, or a done ask → Just finished
    ///  9. prOpen         its PR is open, nothing running                   → PR to merge (however old)
    ///  9a. seen          rule 8, seen since it last moved, no PR open      → Done
    /// 10. stale          a task with no activity for 48 h                  → Shelved
    /// 11. rest           anything else                                     → Other open
    ///
    /// Seen (#5707): the owner looked at it (`seenAt`: Got It, or opening it) at or after the moment that put it in Just
    /// finished — its close, its accepting review, its deploy, its last move for rule 8 — so it goes to Done at once.
    static func triage(_ todo: TodoItem, lookup: TodosLogic.SessionLookup?, now: Date, restoredAt: Date? = nil,
                       superseded: Bool = false, calendar: Calendar = .current) -> TodoTriage {
        let last = lastActivity(todo, lookup: lookup, restoredAt: restoredAt)
        let status = agentsStatus(todo, lookup: lookup)
        // seen: the owner looked at it at or after `at`; fresh: it got to Just finished at `at`, under a day ago, unseen since
        let seen = { (at: Date) in todo.seenAt.map { $0 >= at } ?? false }
        let fresh = { (at: Date) in now.timeIntervalSince(at) < justDone && !seen(at) }
        let ask = openAsk(todo)
        let review = todo.reviewState
        func at(_ s: TodoSection, _ r: TodoTriageRule) -> TodoTriage { TodoTriage(section: s, rule: r, last: last) }

        if status == .failed {
            // the same piece of work finished in a related to-do (`supersededFailedStarts`): history, nothing to retry
            if superseded { return at(fresh(todo.doneAt ?? last) ? .justDone : .done, .superseded) }
            return at(now.timeIntervalSince(last) < shelveAfter ? .waiting : .done, .failedStart)
        }
        if status == .needsYou || status == .interrupted { return at(.waiting, .agentAsks) }
        if todo.doneAt == nil, review == .needsUser || ask?.outcome == "decide" || ask?.outcome == "cannot" {
            return at(.waiting, .ownerDecides)
        }
        if status == .working { return at(.working, .agentRunning) }
        if let done = todo.doneAt { return at(fresh(done) ? .justDone : .done, .closed) }
        if todo.acceptanceStands {
            return at(fresh(todo.supervision?.latestReview?.at ?? .distantPast) ? .justDone : .done, .accepted)
        }
        if todo.delivery?.state == "live" { return at(fresh(todo.delivery?.at ?? last) ? .justDone : .done, .deployed) }
        let finished = status == .unread || review == .pendingReview || (ask != nil && (ask?.outcome ?? "").isEmpty)
            || ask?.outcome == "done"
        if finished, !seen(last) { return at(.justDone, .finishedUnseen) }
        if openPR(todo) { return at(.pr, .prOpen) }
        // seen since it last moved, and no PR waits on the owner
        if finished { return at(.done, .seen) }
        if shelvable(todo, now: now, calendar: calendar), now.timeIntervalSince(last) >= shelveAfter { return at(.shelved, .stale) }
        return at(.other, .rest)
    }

    /// Whether a to-do counts toward the header's open count: not closed, not accepted or deployed, not shelved. A
    /// finished turn nobody looked at yet (Just finished, still open) counts: only a review or the user closes a to-do.
    /// One the owner has seen since (rule 9a, in Done) does not.
    static func countsAsOpen(_ todo: TodoItem, _ t: TodoTriage) -> Bool {
        // a failed start has done_at too (the hub ends it), yet while it waits on the owner it is theirs to retry: open
        if t.rule == .failedStart { return t.section == .waiting }
        if todo.doneAt != nil || t.section == .shelved || t.section == .done { return false }
        return t.rule != .accepted && t.rule != .deployed
    }

    /// Whether a to-do's checkbox shows it open (the web's `checksAsOpen`): not closed, or a failed start still waiting
    /// on the owner (rule 1), which the hub closed itself and the page counts as open (#3713). Ticking an open one closes
    /// it (done: for a failed start the hub clears its start error, so it moves to Just finished); unticking a closed
    /// one reopens it.
    static func checksAsOpen(_ todo: TodoItem, _ t: TodoTriage?) -> Bool {
        if todo.doneAt == nil { return true }
        return todo.failedToStart && t?.rule == .failedStart && t?.section == .waiting
    }

    /// `checksAsOpen` for a checkbox drawn outside the To-dos list (a meeting's action item, #6563), with the triage the
    /// list gives the to-do. Only a closed failed start needs it, so only then are `all` (whose finished work may have
    /// superseded it) and `lookup` read.
    static func checksAsOpen(_ todo: TodoItem, among all: () -> [TodoItem], lookup: () -> TodosLogic.SessionLookup?,
                             now: Date, calendar: Calendar = .current) -> Bool {
        guard todo.doneAt != nil, todo.failedToStart else { return todo.doneAt == nil }
        let t = triage(todo, lookup: lookup(), now: now, superseded: supersededFailedStarts(all()).contains(todo.id),
                       calendar: calendar)
        return checksAsOpen(todo, t)
    }

    /// Where a to-do stands for every count of open work (#3331): the To-dos page's "Not done" and the assistant's
    /// Progress entry read this one rule, so the two numbers agree (the web's `todoBucket`).
    static func bucket(_ todo: TodoItem, lookup: TodosLogic.SessionLookup?, now: Date, restoredAt: Date? = nil,
                       superseded: Bool = false) -> TodoBucket {
        let t = triage(todo, lookup: lookup, now: now, restoredAt: restoredAt, superseded: superseded)
        if countsAsOpen(todo, t) { return .open }
        return t.section == .shelved ? .shelved : .closed
    }

    // MARK: Related to-dos

    /// The first issue number in a title, as the web's `/#(\d{2,7})\b/` finds it: "#" then 2–7 ASCII digits not followed
    /// by another word character.
    static func issueNumber(_ title: String) -> String? {
        let c = Array(title.unicodeScalars)
        let isDigit = { (u: Unicode.Scalar) in u >= "0" && u <= "9" }
        let isWord = { (u: Unicode.Scalar) in isDigit(u) || (u >= "a" && u <= "z") || (u >= "A" && u <= "Z") || u == "_" }
        var i = 0
        while i < c.count {
            if c[i] == "#" {
                var j = i + 1
                while j < c.count, isDigit(c[j]) { j += 1 }
                let n = j - i - 1
                if n >= 2, n <= 7, j == c.count || !isWord(c[j]) {
                    var out = String.UnicodeScalarView()
                    out.append(contentsOf: c[(i + 1)..<j])
                    return String(out)
                }
            }
            i += 1
        }
        return nil
    }

    /// The keys that make two to-dos the same piece of work: the same PR, the same issue number in the title, the same
    /// title once case, spaces and punctuation are dropped (six characters or more), or the same plan.
    static func relatedKeys(_ todo: TodoItem) -> [String] {
        var keys: [String] = []
        var pr = (todo.prURL ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
        if let cut = pr.firstIndex(where: { $0 == "#" || $0 == "?" }) { pr = String(pr[..<cut]) }
        while pr.hasSuffix("/") { pr.removeLast() }
        if !pr.isEmpty { keys.append("pr:" + pr.lowercased()) }
        if let issue = issueNumber(todo.title) { keys.append("issue:" + issue) }
        let drop = CharacterSet.whitespacesAndNewlines.union(.punctuationCharacters).union(.symbols)
        var scalars = String.UnicodeScalarView()
        scalars.append(contentsOf: todo.title.lowercased().unicodeScalars.filter { !drop.contains($0) })
        // long enough that two short generic titles ("test it") do not merge
        if scalars.count >= 6 { keys.append("title:" + String(scalars)) }
        if let plan = todo.planID, !plan.isEmpty { keys.append("plan:" + plan) }
        return keys
    }

    /// The row each to-do is in: the root of its related to-dos (union of shared keys), open and closed kept apart.
    static func relatedRoots(_ list: [TodoItem]) -> [String: String] {
        var parent: [String: String] = Dictionary(list.map { ($0.id, $0.id) }, uniquingKeysWith: { a, _ in a })
        func find(_ id: String) -> String {
            var r = id
            while let p = parent[r], p != r { r = p }
            parent[id] = r
            return r
        }
        var owner: [String: String] = [:]
        for td in list {
            let side = td.doneAt != nil ? "c" : "o"
            for k in relatedKeys(td) {
                let key = side + "|" + k
                if let other = owner[key] {
                    let a = find(td.id), b = find(other)
                    parent[a] = b
                } else {
                    owner[key] = td.id
                }
            }
        }
        var roots: [String: String] = [:]
        for td in list { roots[td.id] = find(td.id) }
        return roots
    }

    /// Failed starts (rule 1) whose row has a to-do that really finished: closed without a start error (#3713). The
    /// work got done another way; the failed start goes to Just finished / Done and counts as closed, on the page, the
    /// badge and the assistant's entries alike (the web's `supersededFailedStarts`).
    static func supersededFailedStarts(_ todos: [TodoItem]) -> Set<String> {
        let root = relatedRoots(todos)
        let finished = Set(todos.filter { $0.doneAt != nil && !$0.failedToStart }.compactMap { root[$0.id] })
        return Set(todos.filter { $0.failedToStart && root[$0.id].map(finished.contains) == true }.map(\.id))
    }

    /// Every to-do placed in its section, related ones merged into one row (union of shared keys). Closed to-dos are
    /// not merged into open ones' rows. A row takes its most urgent member's section (a superseded failed start never
    /// leads); rows in a section go most recently active first. `quiet: false` leaves quiet tasks (#1731) out, as
    /// badges do; the page lists them. A caller that narrows the list (search, a filter) passes `superseded` decided on
    /// every to-do, so a failed start whose finished work is left out does not wait again.
    static func triageTodos(_ todos: [TodoItem], lookup: TodosLogic.SessionLookup?, now: Date,
                            restored: [String: Date] = [:], quiet: Bool = true, superseded known: Set<String>? = nil,
                            calendar: Calendar = .current) -> TodoTriageResult {
        let list = quiet ? todos : todos.filter { !$0.quiet }
        let superseded = known ?? supersededFailedStarts(list)
        var placed: [String: TodoTriage] = [:]
        for td in list {
            placed[td.id] = triage(td, lookup: lookup, now: now, restoredAt: restored[td.id], superseded: superseded.contains(td.id),
                                   calendar: calendar)
        }
        let roots = relatedRoots(list)
        var byRoot: [String: [TodoItem]] = [:]
        var rootOrder: [String] = []
        for td in list {
            let r = roots[td.id] ?? td.id
            if byRoot[r] == nil { rootOrder.append(r) }
            byRoot[r, default: []].append(td)
        }

        var counts = Dictionary(uniqueKeysWithValues: TodoSection.allCases.map { ($0, 0) })
        var open = 0
        for td in list {
            let t = placed[td.id]!
            counts[t.section, default: 0] += 1
            if countsAsOpen(td, t) { open += 1 }
        }
        let shelved = counts[.shelved] ?? 0
        let order = Dictionary(uniqueKeysWithValues: TodoSection.allCases.enumerated().map { ($0.element, $0.offset) })
        var sections = Dictionary(uniqueKeysWithValues: TodoSection.allCases.map { ($0, [TodoTriageGroup]()) })
        for r in rootOrder {
            let members = byRoot[r]!.sorted { a, b in
                let ta = placed[a.id]!, tb = placed[b.id]!
                if ta.section != tb.section { return order[ta.section]! < order[tb.section]! }
                if (ta.rule == .superseded) != (tb.rule == .superseded) { return tb.rule == .superseded }
                if ta.last != tb.last { return ta.last > tb.last }
                return a.id < b.id
            }
            let lead = members[0]
            let lt = placed[lead.id]!
            var triageOf: [String: TodoTriage] = [:]
            for m in members { triageOf[m.id] = placed[m.id]! }
            let last = members.map { placed[$0.id]!.last }.max() ?? lt.last
            sections[lt.section]!.append(TodoTriageGroup(key: lead.id, section: lt.section, lead: lead, members: members,
                                                         triage: triageOf, last: last))
        }
        for s in TodoSection.allCases {
            sections[s]!.sort { a, b in a.last != b.last ? a.last > b.last : a.key < b.key }
        }
        return TodoTriageResult(sections: sections, todoCounts: counts, open: open, shelved: shelved,
                                finished: list.count - open - shelved)
    }
}

/// Seen marks (#5707, docs/assistant/todo-status-sop.md "Seen"): the hub keeps when the owner last looked at a Just
/// finished to-do (`TodoItem.seenAt`); the pages offer Got It, and count opening one as seeing it, only when it lists this.
nonisolated enum TodoSeen {
    static let feature = "todo_seen"
}

/// Restores from Shelved (#3206): a restore counts as activity now, kept on this device only (as the web keeps it per
/// browser): another device still shows the to-do shelved until it moves. Entries older than the shelving window are
/// dropped, they no longer change anything.
nonisolated enum TodoRestores {
    static let key = "todos.restored"

    static func load(_ defaults: UserDefaults = .standard, now: Date = .now) -> [String: Date] {
        guard let raw = defaults.dictionary(forKey: key) as? [String: Double] else { return [:] }
        var out: [String: Date] = [:]
        for (id, t) in raw where now.timeIntervalSince1970 - t < TodoTriageLogic.shelveAfter { out[id] = Date(timeIntervalSince1970: t) }
        return out
    }

    @discardableResult
    static func restore(_ ids: [String], _ defaults: UserDefaults = .standard, now: Date = .now) -> [String: Date] {
        var all = load(defaults, now: now)
        for id in ids { all[id] = now }
        defaults.set(all.mapValues(\.timeIntervalSince1970), forKey: key)
        return all
    }
}
