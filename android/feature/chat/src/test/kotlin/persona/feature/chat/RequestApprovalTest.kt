package persona.feature.chat

import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.*

class RequestApprovalTest {
    private val request = Interaction("q", machineId = "m", sessionId = "child", kind = "question", foldedUnder = "parent")
    private val child = Session("m", "child", sourceKind = "runtime", status = "working", state = "working",
        capabilities = SessionCapabilities(approve = true))

    @Test fun unknownChildrenKeepThePreviousPolicyWhileTheirMetadataLoadsOrFails() {
        assertNull(requestApprovalBlockedReason(request, null, null) { "Child blocked" })
        assertEquals("Parent blocked", requestApprovalBlockedReason(request, null, "Parent blocked") { "Child blocked" })
    }

    @Test fun onlyAKnownChildsCapabilityOverridesTheParentPolicy() {
        assertNull(requestApprovalBlockedReason(request, child, "Parent blocked") { "Child blocked" })
        assertEquals("Child blocked", requestApprovalBlockedReason(request, child.copy(capabilities = SessionCapabilities()), null) { "Child blocked" })
        assertEquals("Parent blocked", requestApprovalBlockedReason(request.copy(foldedUnder = null), child, "Parent blocked") { "Child blocked" })
        assertEquals("Parent blocked", requestApprovalBlockedReason(request, child.copy(machineId = "other"), "Parent blocked") { "Child blocked" })
    }
}
