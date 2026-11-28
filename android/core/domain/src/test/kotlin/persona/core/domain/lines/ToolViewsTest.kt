package persona.core.domain.lines

import org.junit.Assert.assertEquals
import org.junit.Test
import persona.core.contracts.Item
import persona.core.contracts.JsonCodec
import persona.core.contracts.api.ExtensionView

class ToolViewsTest {
    @Test fun readsItemAndFoldViewsAndKeepsThemInAClosedWorkGroup() {
        val first = JsonCodec.decodeFromString(Item.serializer(), """
            {"id":"result","seq":2,"kind":"tool_result","tool_views":[{"title":"Weekday route"}]}
        """.trimIndent())
        val folded = JsonCodec.decodeFromString(Item.serializer(), """
            {"id":"fold","seq":3,"kind":"fold","fold":{"tools":1,"tool_views":[{
                "item_id":"weekend-result","tool_use_id":"weekend-tool","tool":"route_preview",
                "view":{"title":"Weekend route","sections":[{"items":[{
                    "type":"map","markers":[{"id":"park","lat":37.7,"lon":-122.4,"label":"Park"}]
                }]}]}
            }]}}
        """.trimIndent())
        val tool = WorkItem.Tool(ToolCall(LineItem(Item("use", kind = "tool_use")), LineItem(first), emptyList()))
        val fold = WorkItem.Folded(LineItem(folded))
        val work = ChatLine.Work("work", listOf(tool, fold), live = false, start = null, end = null, open = false)
        val views = work.toolViews()
        assertEquals(listOf("Weekday route", "Weekend route"), views.map { it.title })
        val map = views[1].sections.single().items.single()
        assertEquals("map", map.type)
        assertEquals("park", map.markers?.single()?.id)
        assertEquals(listOf(views[1]), ChatLine.Step(work.id, fold, false).toolViews())
        val reply = ChatLine.Row(ChatRow("reply", LineItem(Item("reply", kind = "text"))))
        assertEquals(emptyList<ExtensionView>(), reply.toolViews())
    }
}
