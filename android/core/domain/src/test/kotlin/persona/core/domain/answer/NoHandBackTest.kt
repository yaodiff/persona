package persona.core.domain.answer

import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.Interaction
import persona.core.contracts.Resolution

class NoHandBackTest {
    private val permission = Interaction("p", machineId = "m", sessionId = "s", kind = "permission", toolName = "Bash")

    @Test fun noHandBackBlocksDismissWhileKeepingAllowAndDeny() {
        val headless = permission.copy(noHandBack = true)
        assertFalse(AnswerBuilder.build(headless, AnswerAction.Dismiss).allowed)
        assertEquals(Resolution(allow = true), AnswerBuilder.build(headless, AnswerAction.Allow).resolution)
        assertEquals(Resolution(allow = false), AnswerBuilder.build(headless, AnswerAction.Deny).resolution)
    }

    @Test fun interactivePermissionCardKeepsDismiss() {
        assertEquals(Resolution(dismiss = true), AnswerBuilder.build(permission, AnswerAction.Dismiss).resolution)
    }
}
