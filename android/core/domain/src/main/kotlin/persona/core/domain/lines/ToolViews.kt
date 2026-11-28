package persona.core.domain.lines

import persona.core.contracts.Item
import persona.core.contracts.api.ExtensionView

private fun Item.views(): List<ExtensionView> = toolViews + fold?.toolViews.orEmpty().mapNotNull { it.view }

private fun WorkItem.views(): List<ExtensionView> = when (this) {
    is WorkItem.Tool -> call.use.item.views() + call.results.flatMap { it.item.views() }
    is WorkItem.Folded -> item.item.views()
    is WorkItem.Thought -> item.item.views()
    is WorkItem.Narration -> item.item.views()
}

/** A collapsed work group still shows the views its tools attached to the turn. */
fun ChatLine.toolViews(): List<ExtensionView> = when (this) {
    is ChatLine.Row -> row.item?.item?.views().orEmpty()
    is ChatLine.Work -> steps.flatMap { it.views() }
    is ChatLine.Step -> step.views()
    else -> emptyList()
}.distinct()
