package persona.feature.chat

import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.test.runTest

class ChildSessionHomeTest {
    @Test fun onlyAConfirmedParentChangesThePageKey() {
        val key = SessionKey("m~g12345678", "child")
        val row = Session(key.machineId, key.sessionId, foldedUnder = "parent")
        val parent = Session(key.machineId, "parent")
        assertEquals(ChildSessionHome(parent.key, "child"), childSessionHome(key, row, parent))
        assertEquals(ChildSessionHome(key), childSessionHome(key, row))
        assertEquals(ChildSessionHome(key), childSessionHome(key, row, parent.copy(machineId = "another")))
        assertEquals(ChildSessionHome(key), childSessionHome(key, null))
        assertEquals(ChildSessionHome(key), childSessionHome(key, row.copy(machineId = "another")))
        assertEquals(ChildSessionHome(key), childSessionHome(key, row.copy(foldedUnder = key.sessionId)))
        assertEquals(ChildSessionHome(key), childSessionHome(key, row.copy(foldedUnder = null)))
    }

    @Test fun coldLinksKeepBothExactRowsWithoutChangingTheRootWindow() = runTest {
        val child = Session("m", "child", foldedUnder = "parent")
        val parent = Session("m", "parent", title = "Lead")
        val window = emptyList<Session>()
        val reads = mutableListOf<SessionKey>()
        val home = resolveChildSessionHome(child.key, window) { key ->
            reads += key
            listOf(child, parent).firstOrNull { it.key == key }
        }
        assertEquals(parent.key, home.key)
        assertEquals("child", home.childSessionId)
        assertEquals(listOf(child, parent), home.rows)
        assertEquals(listOf(child.key, parent.key), reads)
        assertTrue(window.isEmpty())
        assertTrue(resolveChildSessionHome(child.key, listOf(child, parent)) {
            fail("Listed metadata needs no lookup"); null
        }.rows.isEmpty())
    }

    @Test fun offlinePagesUseTheirRequestedKeyWithoutStartingAnyLookup() = runTest {
        val key = SessionKey("m", "ordinary")
        assertEquals(ChildSessionHome(key), resolveChildSessionHome(key, emptyList(), connected = false) {
            fail("An offline page must open from cache without a route/read timeout"); null
        })
    }

    @Test fun aMissingOrDeletedParentKeepsTheChildsOwnPage() = runTest {
        val child = Session("m", "child", foldedUnder = "deleted")
        val home = resolveChildSessionHome(child.key, listOf(child)) { null }
        assertEquals(ChildSessionHome(child.key), home)
    }

    @Test fun wrongMachineLookupCannotRedirectAndClosingCancelsTheLookup() = runTest {
        val key = SessionKey("m", "child")
        assertEquals(ChildSessionHome(key), resolveChildSessionHome(key, emptyList()) {
            Session("other", "child", foldedUnder = "parent")
        })
        try {
            resolveChildSessionHome(key, emptyList()) { throw CancellationException("Closed") }
            fail("A departed page must cancel the lookup")
        } catch (_: CancellationException) { }
    }
}
