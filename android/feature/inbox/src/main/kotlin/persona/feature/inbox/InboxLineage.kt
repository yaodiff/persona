package persona.feature.inbox

import persona.core.contracts.api.InboxParentRef

/** The line of one inbox item: its ancestors (nearest first) and its direct children. */
data class InboxLineage(
    val node: InboxParentRef,
    val item: InboxRecord? = null,
    val ancestors: List<InboxLineAncestor> = emptyList(),
    val children: List<InboxRecord> = emptyList(),
)

data class InboxLineAncestor(val parent: InboxParentRef, val item: InboxRecord? = null)

/** One displayed row. [unlink] is the lower item of this row's pair, never the ancestor shown by an up row. */
internal data class InboxLineRow(
    val key: String,
    val parent: InboxParentRef,
    val item: InboxRecord?,
    val up: Boolean,
    val unlink: InboxRecord?,
)

// The hub caps a line at eight parent levels, plus the terminal to-do or session reference it may return.
private const val MAX_LINE_ANCESTORS = 9

/** Ancestors farthest first, then children in the hub's order (oldest first), matching iOS and the web. */
internal fun inboxLineRows(tree: InboxLineage): List<InboxLineRow> {
    val ancestors = tree.ancestors.take(MAX_LINE_ANCESTORS)
    val ups = ancestors.mapIndexed { index, ancestor ->
        val lower = if (index == 0) tree.item else ancestors[index - 1].item
        InboxLineRow("up:${ancestor.parent.kind}:${ancestor.parent.id}", ancestor.parent, ancestor.item, up = true, unlink = lower)
    }.asReversed()
    val downs = tree.children.map {
        InboxLineRow("down:${it.hubItemId}", InboxParentRef("inbox", it.hubItemId), it, up = false, unlink = it)
    }
    return ups + downs
}
