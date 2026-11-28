package persona.core.domain.tasks

import persona.core.contracts.Supervision
import persona.core.contracts.TaskReview
import persona.core.contracts.Todo
import persona.core.contracts.TodoState
import persona.core.contracts.SessionKey
import persona.core.contracts.Session
import persona.core.contracts.state

// A task the main conversation supervises (#3124, docs/assistant/unified-entry.md "Supervision"): where its review stands and how each
// criterion was found, for the task card, the todo page and the conversation's review lines.

/** Where a supervised task's review stands. */
enum class ReviewStage { Ongoing, Pending, UserCheck, Rework, NeedsUser, Accepted, Cancelled }

/**
 * Whether its supervisor's acceptance still stands: the latest review accepted it and no review came since. That holds
 * even when the hub reopened it because its agent was sent more work (#3156): the task is accepted until a new review
 * says otherwise (docs/assistant/todo-status-sop.md, "Accepted tasks"; web `acceptanceStands`).
 */
fun Supervision.acceptanceStands(): Boolean = state == "accepted" && latestReview?.verdict == "accepted"

/** [Supervision.acceptanceStands] for a to-do; false when it is not supervised. */
val Todo.acceptanceStands: Boolean get() = supervision?.acceptanceStands() == true

/**
 * The review state that holds now (web `reviewState`): a cancelled task reopened goes on again (""); an accepted one
 * stays accepted while its acceptance stands. "" when not supervised.
 */
val Todo.reviewState: String
    get() {
        val st = supervision?.state.orEmpty()
        if (isDone && st == "user_check") return "accepted"
        if (!isDone && st == "accepted" && !acceptanceStands) return ""
        if (!isDone && st == "cancelled") return ""
        return st
    }

/**
 * The stage that holds now (a cancelled task the user reopened goes on again; an accepted one reopened stays accepted
 * while its acceptance stands, #3156); null when not supervised.
 */
val Todo.reviewStage: ReviewStage?
    get() {
        val sup = supervision ?: return null
        return when (sup.state) {
            "pending_review" -> ReviewStage.Pending
            "user_check" -> if (isDone) ReviewStage.Accepted else ReviewStage.UserCheck
            "rework" -> ReviewStage.Rework
            "needs_user" -> ReviewStage.NeedsUser
            "accepted" -> if (isDone || sup.acceptanceStands()) ReviewStage.Accepted else ReviewStage.Ongoing
            "cancelled" -> if (isDone) ReviewStage.Cancelled else ReviewStage.Ongoing
            else -> ReviewStage.Ongoing
        }
    }

/** An undismissed decision or personal verification only the owner can move, with the same label on the row and detail as in triage (#5732). */
fun Todo.waitsForOwner(): Boolean =
    !isDone && (reviewState == "user_check" || !decisionDismissed() && (reviewState == "needs_user" || openAsk?.outcome == "decide" || openAsk?.outcome == "cannot"))

/** Delivered work at rest still awaits acceptance; `live` alone never means done (#5732). */
fun Todo.deliveredAwaitingReview(lookup: (SessionKey) -> Session?, now: Long = System.currentTimeMillis()): Boolean {
    if (isDone || waitsForOwner()) return false
    val atRest = triageStatus(lookup, now) in setOf(TriageStatus.FollowUp, TriageStatus.Unread, TriageStatus.NotStarted)
    val reviewRequired = reviewState == "pending_review" || openAsk != null || (kind != "concern" && delivery?.state == "live")
    return atRest && reviewRequired
}

/** The agent-state mark for the same effective delivery state ([state] for ordinary work). */
fun Todo.shownState(lookup: (SessionKey) -> Session?, now: Long = System.currentTimeMillis()): TodoState = when {
    acceptedAtRest(lookup, now) -> TodoState.Done
    waitsForOwner() -> TodoState.NeedsYou
    deliveredAwaitingReview(lookup, now) -> TodoState.Unread
    else -> state(lookup)
}

/** A review's verdict as a stage (a review line in the conversation); null for one it does not know. */
fun reviewVerdictStage(verdict: String): ReviewStage? = when (verdict) {
    "accepted" -> ReviewStage.Accepted
    "rework" -> ReviewStage.Rework
    "needs_user" -> ReviewStage.NeedsUser
    "cancelled" -> ReviewStage.Cancelled
    else -> null
}

/** [Unchecked]: no review judged it after a standing acceptance (a criterion added with more work, #3156): nothing waits on it. */
enum class CheckMark { Met, Missing, Open, Unchecked }

data class CheckLine(val criterion: String, val mark: CheckMark, val evidence: String?)

/**
 * Each criterion as the latest review that judged it found it: met, missing (with its evidence or gap), or open (not
 * reviewed yet); criteria a review judged that are not in done_when (a task without any) follow. Once the acceptance
 * stands, a criterion no review judged is [CheckMark.Unchecked], not open.
 */
fun Supervision.checklist(): List<CheckLine> {
    val settled = acceptanceStands()
    val latest = LinkedHashMap<String, persona.core.contracts.ReviewCheck>()
    for (r in reviews) for (c in r.checks) {
        latest.remove(c.criterion)
        latest[c.criterion] = c
    }
    val out = doneWhen.map { criterion ->
        val c = latest[criterion]
        CheckLine(criterion, when { c == null -> if (settled) CheckMark.Unchecked else CheckMark.Open; c.met -> CheckMark.Met; else -> CheckMark.Missing }, c?.evidence?.takeIf { it.isNotBlank() })
    }
    val more = latest.values.filter { it.criterion !in doneWhen }
        .map { CheckLine(it.criterion, if (it.met) CheckMark.Met else CheckMark.Missing, it.evidence?.takeIf { e -> e.isNotBlank() }) }
    return out + more
}

/** Criteria met and all of them ("2/3" on a card); null without criteria. Criteria left unchecked by a standing
 *  acceptance are not counted (an accepted task reads "5/5", not "5/8"). */
fun Supervision.checkCount(): Pair<Int, Int>? {
    val list = checklist().filter { it.mark != CheckMark.Unchecked }
    if (list.isEmpty()) return null
    return list.count { it.mark == CheckMark.Met } to list.size
}

val Supervision.latestReview: TaskReview? get() = reviews.lastOrNull()
