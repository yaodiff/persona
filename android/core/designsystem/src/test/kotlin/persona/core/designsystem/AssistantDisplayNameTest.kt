package persona.core.designsystem

import org.junit.Assert.assertEquals
import org.junit.Test
import persona.core.contracts.api.HubManager
import persona.core.contracts.api.ManagerRoster

/** #6401: every assistant surface reads one name — the current manager's with several assistants, the product title with one. */
class AssistantDisplayNameTest {
    private val fallback = "Persona"
    private val multi = ManagerRoster(
        listOf(
            HubManager("assistant", "团子", isDefault = true, builtin = true),
            HubManager("m-work", "工作"),
        ),
        "assistant",
    )

    @Test fun withSeveralAssistantsTheCurrentManagersNameIsTheOneSource() {
        assertEquals("团子", multi.assistantDisplayName("assistant", fallback))
        assertEquals("工作", multi.assistantDisplayName("m-work", fallback))
        // The shown id is missing from the roster: the product title stands in rather than a blank.
        assertEquals(fallback, multi.assistantDisplayName("m-gone", fallback))
    }

    @Test fun aSingleInstalledAssistantKeepsTheProductTitle() {
        val single = ManagerRoster(listOf(HubManager("assistant", "Personal assistant name", isDefault = true)), "assistant")
        assertEquals(fallback, single.assistantDisplayName("assistant", fallback))
        assertEquals(fallback, null.assistantDisplayName("assistant", fallback))
    }
}
