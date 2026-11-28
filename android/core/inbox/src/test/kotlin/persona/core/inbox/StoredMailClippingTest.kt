package persona.core.inbox

import org.junit.Assert.assertEquals
import org.junit.Test

/** The hub's optional boolean becomes the closed tri-state once (#15995); absent is Unknown, never Whole. */
class StoredMailClippingTest {
    @Test fun hubBooleanMapsToTheClosedSet() {
        assertEquals(StoredMailClipping.Clipped, StoredMailClipping.fromHub(true))
        assertEquals(StoredMailClipping.Whole, StoredMailClipping.fromHub(false))
        assertEquals(StoredMailClipping.Unknown, StoredMailClipping.fromHub(null))
    }
}
