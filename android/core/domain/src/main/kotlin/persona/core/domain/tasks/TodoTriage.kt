package persona.core.domain.tasks

import java.time.ZoneId
import persona.core.contracts.AgentState
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.Todo
import persona.core.contracts.TodoAsk
import persona.core.contracts.TodoPRStatus
import persona.core.contracts.generated.TodoWaitKind
import persona.core.contracts.generated.TodoWaitPRBlocker
import persona.core.contracts.parseTime
import persona.core.domain.lines.ApiErrorInfo

// The To-dos page's sections (#3156, #3206, docs/assistant/todo-status-sop.md): each to-do's place comes from real signals
// only — its agents' sessions (alive, running, asking), its PR / delivery state, the latest task_review verdict and when it
// last moved — never from what an agent says about itself. A port of the web's `web/src/lib/todo-triage.ts`; the rules are
// the SOP's table, in its order, and the first that holds wins. A change to the rules changes every client.

/**
 * Where a to-do shows, in the page's order (#5707), with cancelled separate from Done (#5732).
 */
enum class TriageSection { Waiting, Working, Pr, Other, JustDone, Shelved, Done, Cancelled;

    /** Starts folded: Shelved, Done and Cancelled; unfinished to-dos stay open. */
    val folded: Boolean get() = this == Shelved || this == Done || this == Cancelled
}

/** Which SOP rule placed a to-do; the section follows from it. */
enum class TriageRule { FailedStart, Cancelled, Superseded, AgentAsks, OwnerDecides, AgentRunning, Closed, Accepted, Deployed, FinishedUnseen, PrOpen, Seen, Stale, Rest }

/** A to-do's section, the rule that placed it, and the last moment anything of it moved (ms, 0 when nothing says). */
data class Triage(val section: TriageSection, val rule: TriageRule, val last: Long)

/** No activity for this long, nothing of it running: shelved. */
const val SHELVE_AFTER_MS: Long = 48 * 3_600_000L
const val JUST_DONE_MS: Long = 24 * 3_600_000L
private const val PR_STATUS_FRESH_MS: Long = 15 * 60_000L

private fun ms(at: String?): Long = parseTime(at)?.toEpochMilli() ?: 0L

/** The web's todo status (`web/src/lib/todos.ts` `todoStatus`), which the triage reads: an open ask does not make it Needs you. */
enum class TriageStatus { Failed, Done, NotStarted, NeedsYou, Interrupted, UsageLimit, Unread, Working, Waiting, FollowUp }

/**
 * The status the triage reads from the to-do and its current agents' sessions, as the web computes it.
 * A subscription usage limit ([Session.isUsageLimited]) is its own status (iOS `AgentsStatus.usageLimit`): it is not
 * [Session.interrupted], which stays the shared flag the capsule and the Agents list already use. [now] is epoch
 * millis, so a limit past its grace reads as interrupted on the next tick.
 */
fun Todo.triageStatus(lookup: (SessionKey) -> Session?, now: Long = System.currentTimeMillis()): TriageStatus {
    if (!startError.isNullOrEmpty()) return TriageStatus.Failed
    if (isDone) return TriageStatus.Done
    val active = currentAgents
    if (active.isEmpty()) return TriageStatus.NotStarted
    var interrupted = false
    var limit = false
    var unread = false
    var working = false
    var waiting = false
    for (a in active) {
        if (!a.startError.isNullOrEmpty()) continue // a failed start counts as stopped
        val s = lookup(a.key)
        val st = s?.agentState ?: AgentState.Offline
        if (st == AgentState.NeedsYou || s != null && (s.pendingInteractions > 0 || s.status == "blocked")) return TriageStatus.NeedsYou
        when {
            s?.isUsageLimited(now) == true -> limit = true
            s?.interrupted == true && (s.turnErrorKind == "unrecoverable" || s.turnErrorCode == "compaction_sealed" ||
                ApiErrorInfo.unrecoverable(s.turnError)) -> interrupted = true
            s?.backgroundWorking == true && st == AgentState.Working -> waiting = true
            st == AgentState.Working || s?.starting == true -> working = true
            active.size > 1 && s?.waitsWithoutBackground() == true -> waiting = true
            st != AgentState.NeedsYou && st != AgentState.Unread && s?.work?.phase == "blocked" && s?.work?.on in setOf("agent", "external", "transient") -> waiting = true
            s?.interrupted == true -> interrupted = true
            st == AgentState.Unread -> unread = true
        }
    }
    return when {
        interrupted && !working && !waiting && supervision?.state != "parked" -> TriageStatus.Interrupted
        limit -> TriageStatus.UsageLimit
        unread -> TriageStatus.Unread
        working -> TriageStatus.Working
        waiting -> TriageStatus.Waiting
        else -> TriageStatus.FollowUp
    }
}

/** The ask that is open now: one with a verdict and the moment it was asked (web `openAsk`). */
val Todo.openAsk: TodoAsk? get() = ask?.takeIf { it.verdict.isNotEmpty() && it.askedAt.isNotEmpty() }

/**
 * The last moment the to-do or any of its agents moved: its own times, its supervision's (reviews, reports), its
 * delivery and ask, and each linked agent's session. A restore from Shelved counts as a move ([restoredAt]).
 */
fun Todo.lastActivity(lookup: (SessionKey) -> Session?, restoredAt: Long = 0): Long {
    val sup = supervision
    var last = maxOf(
        maxOf(ms(updatedAt), ms(createdAt), ms(reopenedAt), ms(doneAt)),
        maxOf(ms(sup?.updatedAt), ms(sup?.progress?.at), ms(sup?.latestReview?.at)),
        maxOf(ms(delivery?.at), ms(ask?.askedAt?.takeIf(String::isNotEmpty)), restoredAt),
    )
    for (a in agents) {
        if (a.releasedAt != null) continue
        val s = lookup(a.key) ?: continue
        last = maxOf(last, ms(s.updatedAt), ms(s.stateSince))
    }
    return last
}

private fun normalizedPR(url: String?): String =
    url.orEmpty().trim().replace(Regex("[#?].*$"), "").trimEnd('/').lowercase()

/** Its PRs with fresh facts saying open and non-draft: the current one first, then earlier ones (#7935). */
fun Todo.openMergePRs(now: Long): List<TodoPRStatus> {
    fun fresh(facts: TodoPRStatus): Boolean {
        val checked = ms(facts.checkedAt)
        return checked > 0 && now - checked >= 0 && now - checked <= PR_STATUS_FRESH_MS && facts.state == "open" && !facts.draft
    }
    val out = mutableListOf<TodoPRStatus>()
    prStatus?.let { current ->
        if (normalizedPR(prUrl) == normalizedPR(current.url) && normalizedPR(prUrl).isNotEmpty() && fresh(current)) out += current
    }
    for (facts in otherPrs.orEmpty()) {
        if (normalizedPR(facts.url) != normalizedPR(prUrl) && fresh(facts)) out += facts
    }
    return out
}

/** Fresh facts are required: stale, absent, mismatched, draft, merged or closed facts do not imply waiting for merge. */
fun Todo.hasOpenPR(now: Long): Boolean = openMergePRs(now).isNotEmpty()

/** Accurate display status when an open to-do has no active agent handling it: open non-draft PR state, parked hold,
 *  or an open main-chat concern ("Tracking") instead of a misleading "Not started". */
enum class TodoRestStatus {
    PrConflicts,
    PrChangesRequested,
    PrChecksFailed,
    PrInReview,
    PrChecksPending,
    PrWaitingMerge,
    ParkedMergeReview,
    Parked,
    ConcernTracking,
}

private fun openPRFactsStatus(prs: List<TodoPRStatus>): TodoRestStatus? {
    if (prs.isEmpty()) return null
    return when {
        prs.any { it.conflicts } -> TodoRestStatus.PrConflicts
        prs.any { it.review == "changes_requested" } -> TodoRestStatus.PrChangesRequested
        prs.any { it.checks == "failure" || it.checks == "failing" } -> TodoRestStatus.PrChecksFailed
        prs.any { it.review == "required" || it.review == "review_required" } -> TodoRestStatus.PrInReview
        prs.any { it.checks == "pending" } -> TodoRestStatus.PrChecksPending
        else -> TodoRestStatus.PrWaitingMerge
    }
}

private fun Todo.openPRRestStatus(now: Long): TodoRestStatus? {
    if (wait?.kind == TodoWaitKind.PrMerge) {
        val blockers = wait?.prBlockers.orEmpty()
        return when {
            TodoWaitPRBlocker.Conflicts in blockers -> TodoRestStatus.PrConflicts
            TodoWaitPRBlocker.ChangesRequested in blockers -> TodoRestStatus.PrChangesRequested
            TodoWaitPRBlocker.ChecksFailing in blockers -> TodoRestStatus.PrChecksFailed
            TodoWaitPRBlocker.ReviewRequired in blockers -> TodoRestStatus.PrInReview
            TodoWaitPRBlocker.ChecksPending in blockers -> TodoRestStatus.PrChecksPending
            else -> openPRFactsStatus(openMergePRs(now)) ?: TodoRestStatus.PrWaitingMerge
        }
    }
    return openPRFactsStatus(openMergePRs(now))
}

/** Resolves a non-misleading display status when an open to-do is at rest without active agent work; null keeps the standard [TodoState]. */
fun Todo.restStatus(lookup: (SessionKey) -> Session?, now: Long = System.currentTimeMillis()): TodoRestStatus? {
    if (isDone || isCancelled || !startError.isNullOrEmpty()) return null
    if (supervision?.state == "user_check" || acceptedAtRest(lookup, now) || waitsForOwner() || deliveredAwaitingReview(lookup, now)) return null
    val st = triageStatus(lookup, now)
    if (st != TriageStatus.NotStarted && st != TriageStatus.FollowUp) return null
    openPRRestStatus(now)?.let { return it }
    if (st == TriageStatus.NotStarted) {
        return when {
            wait?.kind == TodoWaitKind.MergeReview -> TodoRestStatus.ParkedMergeReview
            wait != null || supervision?.state == "parked" -> TodoRestStatus.Parked
            kind == "concern" -> TodoRestStatus.ConcernTracking
            else -> null
        }
    }
    return null
}

/** A task (an agent was linked, or a conversation handed it out) with no due date ahead: the user's own reminders never shelve. */
private fun Todo.shelvable(now: Long, zone: ZoneId): Boolean {
    if (kind == "concern") return false
    val due = dueBy(zone)?.toEpochMilli()
    if (due != null && due >= now) return false
    return agents.isNotEmpty() || !origin.isNullOrEmpty()
}

/** Got It files a finished turn only when no acceptance is still required. An open concern goes to Other open rather
 *  than Done after Seen and bypasses only the ordinary live-delivery restriction, never review or supervision (#5732, #3582). */
val Todo.seenCanFile: Boolean
    get() = (kind == "concern" || delivery?.state != "live") && reviewState != "pending_review" && supervision?.supervisor.isNullOrEmpty()

/**
 * A to-do's section by the SOP's decision table, first match wins:
 *
 *  1. FailedStart    the task's agent never started                     → Waiting (retry or remove), Done after 48 h;
 *     Superseded     … and a related to-do (its row) really finished    → Done immediately (#3713)
 *  2. AgentAsks      an agent asks (question, approval) or was cut off  → Waiting
 *  3. OwnerDecides   review says needs_user, or a decision / "cannot" ask → Waiting
 *  4. AgentRunning   an agent is working or starting                    → Working
 *  5. Closed         done_at set                                        → Done immediately
 *  6. Accepted       the latest review accepted it (even reopened)      → Done immediately
 *  7. FinishedUnseen its delivery is live, not accepted                 → Delivered · awaiting review, no timeout
 *  8. FinishedUnseen an agent finished unseen, awaits review, or a done ask → Delivered · awaiting review
 *  9. PrOpen         its PR is open, nothing running                    → PR to merge (however old)
 *  9a. Seen          rule 8 without supervision, seen since it last moved, no PR open → Done
 * 10. Stale          a task with no activity for 48 h                   → Shelved
 * 11. Rest           anything else                                      → Other open
 *
 * An open concern keeps rules 1–4, 8 and 9, never uses 6, 7, 9a or 10, and an old failed start is Rest.
 * A closed concern follows rule 5 like any closed to-do (#3582).
 */
fun Todo.triage(
    lookup: (SessionKey) -> Session?,
    now: Long,
    restoredAt: Long = 0,
    zone: ZoneId = ZoneId.systemDefault(),
    superseded: Boolean = false,
): Triage {
    val last = lastActivity(lookup, restoredAt)
    val status = triageStatus(lookup, now)
    // Seen files an unsupervised finished turn (#5707), never a task that still needs acceptance (#5732).
    // Explicit closure and acceptance are already Done; Seen does not move them through awaiting review.
    val seenMs = ms(this.seenAt)
    fun seen(at: Long) = seenMs > 0 && seenMs >= at
    fun fresh(at: Long) = now - at < JUST_DONE_MS && !seen(at)
    val ask = openAsk
    fun at(section: TriageSection, rule: TriageRule) = Triage(section, rule, last)
    val review = reviewState
    // Only its report closes a concern: no other signal files an open one with finished work or shelves it.
    val concern = kind == "concern" && !isDone

    if (isCancelled) return at(TriageSection.Cancelled, TriageRule.Cancelled)
    if (status == TriageStatus.Failed) {
        // the same piece of work finished in a related to-do ([supersededFailedStarts]): history, nothing to retry
        if (superseded) return at(TriageSection.Done, TriageRule.Superseded)
        if (now - last < SHELVE_AFTER_MS) return at(TriageSection.Waiting, TriageRule.FailedStart)
        if (concern) return at(TriageSection.Other, TriageRule.Rest)
        return at(TriageSection.Done, TriageRule.FailedStart)
    }
    if (status == TriageStatus.NeedsYou || status == TriageStatus.Interrupted) return at(TriageSection.Waiting, TriageRule.AgentAsks)
    val ownerDelivery = hold?.unblock == HoldUnblock.User
    val ownerReview = review == "user_check" ||
        !decisionDismissed() && (review == "needs_user" || ask?.outcome == "decide" || ask?.outcome == "cannot")
    if (!isDone && (ownerDelivery || ownerReview)) {
        return at(TriageSection.Waiting, TriageRule.OwnerDecides)
    }
    if (status == TriageStatus.Working || status == TriageStatus.Waiting) return at(TriageSection.Working, TriageRule.AgentRunning)
    if (isDone) return at(if (kind == "concern" && fresh(ms(doneAt))) TriageSection.JustDone else TriageSection.Done, TriageRule.Closed)
    if (!concern && acceptanceStands) return at(TriageSection.Done, TriageRule.Accepted)
    // Live is delivery, not acceptance: an unreviewed live task stays open and awaiting review, however old (#5732).
    if (!concern && delivery?.state == "live") return at(TriageSection.JustDone, TriageRule.FinishedUnseen)
    val finished = status == TriageStatus.Unread || review == "pending_review" || (ask != null && ask.outcome.isNullOrEmpty()) || ask?.outcome == "done"
    val acceptanceRequired = !seenCanFile
    // Parked open-PR work is not awaiting acceptance; keep it in PR to Merge, matching Apple and the web (#5732).
    if (finished && (!seen(last) || acceptanceRequired) && supervision?.state != "parked") return at(TriageSection.JustDone, TriageRule.FinishedUnseen)
    if (hasOpenPR(now)) return at(TriageSection.Pr, TriageRule.PrOpen)
    // seen since it last moved, and no PR waits on the owner; an open concern is never Done
    if (finished && !concern) return at(TriageSection.Done, TriageRule.Seen)
    if (shelvable(now, zone) && now - last >= SHELVE_AFTER_MS) return at(TriageSection.Shelved, TriageRule.Stale)
    return at(TriageSection.Other, TriageRule.Rest)
}

/** Whether a Finished row is new. Seen clears that marker without filing a delivery that still awaits review (#5732). */
fun Todo.showsJustFinished(t: Triage, now: Long): Boolean {
    val completedAt = when (t.rule) {
        TriageRule.Closed -> ms(doneAt)
        TriageRule.Accepted -> ms(supervision?.latestReview?.at)
        TriageRule.FinishedUnseen -> finishedAt(t)
        else -> 0
    }
    val seenAt = ms(this.seenAt)
    return completedAt > 0 && now - completedAt < JUST_DONE_MS && (seenAt == 0L || seenAt < completedAt)
}

/**
 * Whether a to-do counts toward the header's open count: not closed, accepted or shelved. An unaccepted delivery
 * still counts after being seen; only an unsupervised finished turn can be filed by Seen (#5707, #5732).
 */
fun Todo.countsAsOpen(t: Triage): Boolean {
    // a failed start has done_at too (the hub ends it), yet while it waits on the owner it is theirs to retry: open
    if (t.rule == TriageRule.FailedStart) return t.section == TriageSection.Waiting
    if (isDone || t.section == TriageSection.Cancelled || t.section == TriageSection.Shelved || t.section == TriageSection.Done) return false
    return t.rule != TriageRule.Accepted && t.rule != TriageRule.Deployed
}

/**
 * When a to-do the triage filed with the finished ones (Just finished or Done) finished, epoch ms: the moment its rule
 * reads in [triage] — its close, its accepting review, its deploy — else its last move (a finished turn the owner has
 * seen since). The Assistant's Finished entry lists what finished less than [JUST_DONE_MS] ago, seen or not (#10922; web
 * `finishedAt`). 0 when nothing says.
 */
fun Todo.finishedAt(t: Triage): Long = when (t.rule) {
    TriageRule.Closed -> ms(doneAt)
    TriageRule.Accepted -> ms(supervision?.latestReview?.at)
    TriageRule.Deployed -> ms(delivery?.at).takeIf { it > 0 } ?: t.last
    TriageRule.FinishedUnseen -> if (delivery?.state == "live") ms(delivery?.at).takeIf { it > 0 } ?: t.last else t.last
    TriageRule.Superseded -> ms(doneAt).takeIf { it > 0 } ?: t.last
    else -> t.last
}

/** Where a to-do stands for every count of open work (#3331): open counts, shelved folds, closed is finished. */
enum class TodoBucket { Open, Shelved, Closed }

/**
 * The one rule the To-dos page's "Not done" and the assistant's Progress entry both count by (#3331), so the two numbers
 * agree: [countsAsOpen] on [triage]; a to-do not open is shelved when its section is Shelved, else closed.
 */
fun Todo.bucket(lookup: (SessionKey) -> Session?, now: Long, restoredAt: Long = 0, superseded: Boolean = false): TodoBucket {
    val t = triage(lookup, now, restoredAt, superseded = superseded)
    return when {
        countsAsOpen(t) -> TodoBucket.Open
        t.section == TriageSection.Shelved -> TodoBucket.Shelved
        else -> TodoBucket.Closed
    }
}

/**
 * Whether a to-do's checkbox shows it open (the web's `checksAsOpen`): not closed, or a failed start still waiting on the
 * owner (rule 1), which the hub closed itself and the page counts as open (#3713). Ticking an open one closes it (done:
 * for a failed start the hub clears its start error, so it moves to Just finished); unticking a closed one reopens it.
 */
fun Todo.checksAsOpen(t: Triage?): Boolean =
    !isDone || !startError.isNullOrEmpty() && t?.rule == TriageRule.FailedStart && t.section == TriageSection.Waiting

/** An accepted task its hub reopened with more work, with nothing of it running or asking: it reads Accepted, not Follow up.
 * An open concern still waits for its report, so neither Accepted nor its done styling applies (#3582). */
fun Todo.acceptedAtRest(lookup: (SessionKey) -> Session?, now: Long = System.currentTimeMillis()): Boolean =
    kind != "concern" && !isDone && (!waitsForOwner() || decisionDismissed()) && acceptanceStands && triageStatus(lookup, now).let {
        it == TriageStatus.FollowUp || it == TriageStatus.Unread || it == TriageStatus.NotStarted || it == TriageStatus.UsageLimit
    }

// ── Related to-dos ────────────────────────────────────────────

private val ISSUE_REF = Regex("#(\\d{2,7})\\b")
private val TITLE_NOISE = Regex("[\\s\\p{P}\\p{S}]+")

/**
 * The keys that make two to-dos the same piece of work: the same PR (case and a trailing slash ignored), the same issue
 * number in the title, the same title once case, spaces and punctuation are dropped (six characters or more), or the
 * same plan (its subtasks under one request).
 */
fun Todo.relatedKeys(): List<String> {
    val keys = mutableListOf<String>()
    val rawPr = prUrl.orEmpty().trim()
    val cut = rawPr.indexOfFirst { it == '#' || it == '?' }
    val pr = (if (cut >= 0) rawPr.substring(0, cut) else rawPr).trimEnd('/').lowercase()
    if (pr.isNotEmpty()) keys += "pr:$pr"
    ISSUE_REF.find(title)?.let { keys += "issue:" + it.groupValues[1] }
    val t = title.lowercase().replace(TITLE_NOISE, "")
    // long enough that two short generic titles ("test it") do not merge
    if (t.codePointCount(0, t.length) >= 6) keys += "title:$t"
    if (!planId.isNullOrEmpty()) keys += "plan:$planId"
    return keys
}

/** One row: related to-dos merged, led by the most urgent member (by section, then most recent). */
data class TriageGroup(
    val key: String,
    val section: TriageSection,
    val lead: Todo,
    /** Every member, the lead first, then by section and recency. */
    val members: List<Todo>,
    val triage: Map<String, Triage>,
    val last: Long,
)

data class TriageResult(
    val sections: Map<TriageSection, List<TriageGroup>>,
    /** To-dos (not rows) per section. */
    val todoCounts: Map<TriageSection, Int>,
    /** The header's "N open · N shelved · N done". */
    val open: Int,
    val shelved: Int,
    val finished: Int,
    /** Cancelled separately from completed (#5732). */
    val cancelled: Int,
) {
    fun rows(section: TriageSection): List<TriageGroup> = sections[section].orEmpty()

    /**
     * This without the to-dos [ids] (another grouping shows them already, #4352): a row keeps its other members, led by
     * the first one left and in that one's section; a row left with none goes. The header totals stay as they are.
     */
    fun without(ids: Set<String>): TriageResult {
        if (ids.isEmpty()) return this
        val groups = sections.values.flatten().mapNotNull { group ->
            val members = group.members.filterNot { it.id in ids }
            when {
                members.isEmpty() -> null
                members.size == group.members.size -> group
                else -> group.copy(section = group.triage.getValue(members.first().id).section, lead = members.first(),
                    members = members, triage = members.associate { it.id to group.triage.getValue(it.id) },
                    last = members.maxOf { group.triage.getValue(it.id).last })
            }
        }
        val kept = groups.flatMap { group -> group.members.map { group.triage.getValue(it.id).section } }
        return copy(
            sections = TriageSection.entries.associateWith { section ->
                groups.filter { it.section == section }.sortedWith(compareByDescending<TriageGroup> { it.last }.thenBy { it.key })
            },
            todoCounts = TriageSection.entries.associateWith { section -> kept.count { it == section } },
        )
    }
}

/** The row each to-do is in: the root of its related to-dos (union of shared keys), open and closed kept apart.
 * An open concern always has its own row: it neither joins a related row nor lets another to-do join through its keys. */
fun relatedRoots(list: List<Todo>): Map<String, String> {
    val parent = HashMap<String, String>().apply { list.forEach { put(it.id, it.id) } }
    fun find(id: String): String {
        var r = id
        while (parent[r] != r) r = parent.getValue(r)
        parent[id] = r
        return r
    }
    val owner = HashMap<String, String>()
    for (td in list) {
        if (td.kind == "concern" && !td.isDone) continue
        val side = if (td.isCancelled) "x" else if (td.isDone) "c" else "o"
        for (k in td.relatedKeys()) {
            val key = "$side|$k"
            val other = owner[key]
            if (other == null) owner[key] = td.id else parent[find(td.id)] = find(other)
        }
    }
    return list.associate { it.id to find(it.id) }
}

/**
 * Failed starts (rule 1) whose row has a to-do that really finished: closed without a start error (#3713). The work got
 * done another way; the failed start goes to Just finished / Done and counts as closed, on the page and in the
 * assistant's entries alike (the web's `supersededFailedStarts`).
 */
fun supersededFailedStarts(todos: List<Todo>): Set<String> {
    if (todos.none { !it.startError.isNullOrEmpty() }) return emptySet()
    if (todos.none { it.isDone && it.startError.isNullOrEmpty() }) return emptySet()
    val roots = relatedRoots(todos)
    val finished = todos.filter { it.isDone && it.startError.isNullOrEmpty() && !it.isCancelled }.mapTo(HashSet()) { roots.getValue(it.id) }
    return todos.filter { !it.startError.isNullOrEmpty() && roots.getValue(it.id) in finished }.mapTo(HashSet()) { it.id }
}

/**
 * Every to-do placed in its section, related ones merged into one row (union of shared keys). Closed to-dos are not merged
 * into open ones' rows. A group takes its most urgent member's section (a superseded failed start never leads); rows in
 * a section go most recently active first.
 * [quiet] false leaves quiet tasks (#1731) out, as a badge does; the page lists them.
 */
fun triageTodos(
    todos: List<Todo>,
    lookup: (SessionKey) -> Session?,
    now: Long,
    restored: Map<String, Long> = emptyMap(),
    quiet: Boolean = true,
    zone: ZoneId = ZoneId.systemDefault(),
    // a caller that narrows the list (search, a filter) passes the set decided on every to-do (#3713)
    known: Set<String>? = null,
): TriageResult {
    val list = if (quiet) todos else todos.filter { !it.quiet }
    val superseded = known ?: supersededFailedStarts(list)
    val placed = list.associate { it.id to it.triage(lookup, now, restored[it.id] ?: 0, zone, superseded = it.id in superseded) }

    val roots = relatedRoots(list)
    val byRoot = LinkedHashMap<String, MutableList<Todo>>()
    for (td in list) byRoot.getOrPut(roots.getValue(td.id)) { mutableListOf() }.add(td)

    val counts = TriageSection.entries.associateWith { 0 }.toMutableMap()
    var open = 0
    for (td in list) {
        val tr = placed.getValue(td.id)
        counts[tr.section] = counts.getValue(tr.section) + 1
        if (td.countsAsOpen(tr)) open++
    }
    val shelved = counts.getValue(TriageSection.Shelved)
    val cancelled = counts.getValue(TriageSection.Cancelled)
    val finished = list.size - open - shelved - cancelled

    val sections = TriageSection.entries.associateWith { mutableListOf<TriageGroup>() }
    for (group in byRoot.values) {
        val members = group.sortedWith(
            compareBy<Todo> { placed.getValue(it.id).section.ordinal }
                // a superseded failed start never leads the row of the work that finished
                .thenBy { placed.getValue(it.id).rule == TriageRule.Superseded }
                .thenByDescending { placed.getValue(it.id).last }
                .thenBy { it.id }
        )
        val lead = members.first()
        val lt = placed.getValue(lead.id)
        sections.getValue(lt.section).add(TriageGroup(
            key = lead.id,
            section = lt.section,
            lead = lead,
            members = members,
            triage = members.associate { it.id to placed.getValue(it.id) },
            last = members.maxOf { placed.getValue(it.id).last },
        ))
    }
    val sorted = sections.mapValues { (_, rows) -> rows.sortedWith(compareByDescending<TriageGroup> { it.last }.thenBy { it.key }) }
    return TriageResult(sorted, counts, open, shelved, finished, cancelled)
}

/**
 * Restores kept on this device (to-do id → when, ms), dropping ones that would have shelved again anyway, with [id]
 * restored at [now]. The hub keeps nothing about it (docs/assistant/todo-status-sop.md, "Shelving").
 */
fun withRestore(restored: Map<String, Long>, id: String, now: Long): Map<String, Long> =
    restored.filter { (_, at) -> now - at < SHELVE_AFTER_MS } + (id to now)
