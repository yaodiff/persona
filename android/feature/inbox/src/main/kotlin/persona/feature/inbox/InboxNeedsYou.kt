package persona.feature.inbox

import persona.core.domain.inbox.InboxListResult
import persona.core.domain.inbox.InboxRow
import persona.core.domain.inbox.InboxRowType

/**
 * Persona has this item in hand: the hub puts it In progress (`status`, inbox phase 1), or, from an older hub, the AI twin
 * is on it (queued or running). It is not the user's to answer: the inbox page lists it under In progress / "Persona is working on it",
 * so the Persona home's "Needs you now" leaves it out too (#4848).
 */
val InboxRecord.agentWorking: Boolean get() = item.isWorking || agent?.state == "running" || agent?.state == "queued"

/** How many rows of "Needs you" are the user's: the action group less the rows [working] says Persona is on. */
fun needsYouCount(list: InboxListResult, working: (InboxRow) -> Boolean): Int =
    list.groups.firstOrNull { it.id == "action" }?.rows?.count { !working(it) } ?: 0

/** [needsYouCount] for the hub's [records]: an item row is Persona's when its record says [agentWorking]. */
fun needsYouCount(list: InboxListResult, records: List<InboxRecord>): Int {
    val working = records.filter { it.agentWorking }.map { it.item.id }.toSet()
    return needsYouCount(list) { it.type == InboxRowType.Item && it.id in working }
}

/** Unread actionable items, one per item, using reconciled conversation reads before the hub item list catches up. */
fun inboxUnreadCount(records: List<InboxRecord>, conversations: InboxConversationList? = null): Int {
    val rows = buildMap {
        conversations?.rows.orEmpty().forEach { row ->
            row.records.forEach { record -> put(record.manager to record.item.id, row) }
        }
    }
    return records.count { record ->
        record.item.needsYou && !record.read && rows[record.manager to record.item.id]?.unread != false
    }
}
