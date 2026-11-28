package persona.platform.notify

import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.Interaction
import persona.core.contracts.Machine
import persona.core.contracts.Resolution
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.DeviceNotificationPreferences
import persona.core.contracts.api.InteractionCenter
import persona.core.contracts.api.QuietHours
import persona.core.contracts.api.SessionsRepository
import java.io.IOException
import java.time.Instant
import java.time.ZoneId

class PendingRequestCatchUpTest {
    private val now = Instant.parse("2030-01-01T23:30:00Z")
    private val otherNamespace = "g1234abcd"
    private val request = Interaction("request", machineId = "machine", sessionId = "session", kind = "permission")

    private fun otherRequest(id: String = "request") = request.copy(
        id = "$id~$otherNamespace",
        machineId = "machine~$otherNamespace",
    )

    private class Center(var requests: List<Interaction>) : InteractionCenter {
        override val pending = MutableStateFlow(requests)
        var refreshes = 0
        var failure: Exception? = null
        override suspend fun refresh() {
            refreshes++
            failure?.let { throw it }
            pending.value = requests
        }
        override suspend fun resolve(id: String, resolution: Resolution) = Unit
    }

    private class Sessions(var current: List<Session>) : SessionsRepository {
        override val machines = MutableStateFlow<List<Machine>>(emptyList())
        override val sessions = MutableStateFlow(current)
        var refreshes = 0
        override suspend fun refresh() { refreshes++; sessions.value = current }
    }

    private inner class Fixture(
        requests: List<Interaction>,
        sessions: List<Session>,
        var settings: Map<String?, DeviceNotificationPreferences> = mapOf(null to DeviceNotificationPreferences()),
        notified: MutableSet<String> = mutableSetOf(),
        occurrences: Map<String, String?> = emptyMap(),
        visible: SessionKey? = null,
        desktops: Map<String, Boolean> = emptyMap(),
    ) {
        val center = Center(requests)
        val sessions = Sessions(sessions)
        val posted = mutableListOf<Pair<Interaction, Boolean>>()
        val postedPreferences = mutableListOf<DeviceNotificationPreferences?>()
        val retracted = mutableListOf<String>()
        val delivery = NotificationDelivery(center, this.sessions, { it == visible },
            postInteraction = { ix, hushed, groupSettings ->
                posted += ix to hushed
                postedPreferences += groupSettings
                notified += ix.id
            },
            postNotify = { error("Catch-up posts requests only") },
            retract = { retracted += it },
            preferences = { settings[null] },
            notified = { notified },
            notifiedOccurrences = { occurrences },
            desktopOnline = { desktops[it] },
            now = { this@PendingRequestCatchUpTest.now },
            catchUpPreferences = { settings },
        )
    }

    @Test fun disabledOrUnavailablePreferencesNeverTurnIntoAnEmptySuccessfulFetch() = runBlocking {
        val disabled = Fixture(listOf(request), emptyList(),
            settings = mapOf(null to DeviceNotificationPreferences(input = false)))
        disabled.delivery.catchUp()
        assertEquals(0, disabled.center.refreshes)
        assertEquals(0, disabled.sessions.refreshes)
        assertTrue(disabled.posted.isEmpty())

        val unavailable = Fixture(listOf(request), emptyList(), settings = emptyMap())
        val failure = runCatching { unavailable.delivery.catchUp() }.exceptionOrNull()
        assertTrue(failure is IllegalStateException)
        assertEquals(0, unavailable.center.refreshes)
    }

    @Test fun refreshFailurePropagatesForWorkManagerRetry() = runBlocking {
        val fixture = Fixture(listOf(request), emptyList())
        fixture.center.failure = IOException("offline")
        assertTrue(runCatching { fixture.delivery.catchUp() }.exceptionOrNull() is IOException)
        assertEquals(1, fixture.sessions.refreshes)
        assertTrue(fixture.posted.isEmpty())
    }

    @Test fun onlyNewAllowedPendingCardsArePosted() = runBlocking {
        val visible = SessionKey("machine", "visible")
        val expired = request.copy(id = "expired", expiresAt = "2029-01-01T00:00:00Z")
        val expiredAlready = request.copy(id = "expired-already", expiresAt = "2029-01-01T00:00:00Z")
        val muted = request.copy(id = "muted", manager = "manager")
        val quietTask = request.copy(id = "quiet-task", sessionId = "quiet")
        val already = request.copy(id = "already")
        val fixture = Fixture(
            listOf(request, expired, expiredAlready, muted, quietTask, already,
                request.copy(id = "visible", sessionId = "visible"),
                request.copy(id = "display", sessionId = "main", mirrorOf = request.id)),
            listOf(Session(machineId = "machine", sessionId = "session"),
                Session(machineId = "machine", sessionId = "quiet", quiet = true),
                Session(machineId = "machine", sessionId = "visible")),
            settings = mapOf(null to DeviceNotificationPreferences(mutedManagers = listOf("manager"))),
            notified = mutableSetOf("already", "expired-already"),
            visible = visible,
        )
        fixture.delivery.catchUp()
        assertEquals(listOf("request"), fixture.posted.map { it.first.id })
        assertEquals(listOf("expired", "expired-already"), fixture.retracted)
        fixture.delivery.catchUp()
        assertEquals(listOf("request"), fixture.posted.map { it.first.id })
        assertEquals(2, fixture.center.refreshes)
    }

    private fun prepared(id: String, expiresAt: String) = persona.core.contracts.JsonCodec.decodeFromString<Interaction>(
        """{"id":"$id","machine_id":"machine","session_id":"session","kind":"permission",""" +
            """"expires_at":"$expiresAt","can_clarify":true}""",
    )

    @Test fun catchUpUsesTheCurrentRetainedDeadlineWithoutASecondPush() = runBlocking {
        val retained = prepared("retained", now.plusSeconds(3_600).toString())
        val alreadyDelivered = prepared("already", now.plusSeconds(7_200).toString())
        val genuinelyExpired = prepared("expired", now.toString())
        val fixture = Fixture(listOf(retained, alreadyDelivered, genuinelyExpired), emptyList(),
            notified = mutableSetOf(alreadyDelivered.id))
        fixture.delivery.catchUp()
        assertEquals(listOf(retained.id), fixture.posted.map { it.first.id })
        assertEquals(listOf(genuinelyExpired.id), fixture.retracted)
        fixture.delivery.catchUp()
        assertEquals(listOf(retained.id), fixture.posted.map { it.first.id })
    }

    @Test fun catchUpDoesNotLetThePreviousOccurrenceSuppressItsReplacement() = runBlocking {
        val original = request.copy(createdAt = "2030-01-01T00:00:00.000000001Z")
        val replacement = request.copy(createdAt = "2030-01-01T00:00:00.000000002Z")
        val fixture = Fixture(listOf(replacement), emptyList(), notified = mutableSetOf(original.id),
            occurrences = mapOf(original.id to original.createdAt))
        fixture.delivery.catchUp()
        assertEquals(listOf(replacement.id), fixture.posted.map { it.first.id })
    }

    @Test fun desktopMuteUsesTheRequestsOwnHubGroup() = runBlocking {
        val settings = DeviceNotificationPreferences(muteWhileDesktop = true)
        val fixture = Fixture(listOf(request, otherRequest()), emptyList(),
            settings = mapOf(null to settings, otherNamespace to settings),
            desktops = mapOf("machine" to true, "machine~$otherNamespace" to false))
        fixture.delivery.catchUp()
        assertEquals(listOf(otherRequest().id), fixture.posted.map { it.first.id })

        // HTTP catch-up can finish before the desktops event: only a desktop known to be online mutes.
        val unknown = Fixture(listOf(request), emptyList(), settings = mapOf(null to settings))
        unknown.delivery.catchUp()
        assertEquals(listOf(request.id), unknown.posted.map { it.first.id })
    }

    @Test fun inputAndMutedManagersAreScopedToTheirOwnHubGroup() = runBlocking {
        val primary = request.copy(manager = "shared-manager")
        val other = otherRequest().copy(manager = "shared-manager")
        val muted = Fixture(listOf(primary, other), emptyList(), settings = mapOf(
            null to DeviceNotificationPreferences(mutedManagers = listOf("shared-manager")),
            otherNamespace to DeviceNotificationPreferences(),
        ))
        muted.delivery.catchUp()
        assertEquals(listOf(other.id), muted.posted.map { it.first.id })

        val disabled = Fixture(listOf(request, otherRequest()), emptyList(), settings = mapOf(
            null to DeviceNotificationPreferences(),
            otherNamespace to DeviceNotificationPreferences(input = false),
        ))
        disabled.delivery.catchUp()
        assertEquals(listOf(request.id), disabled.posted.map { it.first.id })

        val defaultManager = Fixture(listOf(request), emptyList(), settings = mapOf(
            null to DeviceNotificationPreferences(mutedManagers = listOf("assistant")),
        ))
        defaultManager.delivery.catchUp()
        assertTrue(defaultManager.posted.isEmpty())
    }

    @Test fun requestsResolvedWhileNoServiceWasAliveAreRetracted() = runBlocking {
        val fixture = Fixture(emptyList(), emptyList(), notified = mutableSetOf("resolved"))
        fixture.delivery.catchUp()
        assertEquals(listOf("resolved"), fixture.retracted)
        assertTrue(fixture.posted.isEmpty())
    }

    @Test fun missingPreferencesForARequestsGroupRetryBeforeAnythingIsPosted() = runBlocking {
        val fixture = Fixture(listOf(request, otherRequest()), emptyList())
        assertTrue(runCatching { fixture.delivery.catchUp() }.exceptionOrNull() is IllegalStateException)
        assertTrue(fixture.posted.isEmpty())
        assertTrue(fixture.retracted.isEmpty())
    }

    @Test fun quietHoursAreScopedToTheirOwnHubGroup() = runBlocking {
        val quiet = QuietHours(enabled = true, start = "23:00", end = "07:00", tz = "UTC")
        val fixture = Fixture(listOf(request, otherRequest()), emptyList(), settings = mapOf(
            null to DeviceNotificationPreferences(quiet = quiet),
            otherNamespace to DeviceNotificationPreferences(quiet = quiet.copy(start = "08:00", end = "09:00")),
        ))
        fixture.delivery.catchUp()
        assertEquals(mapOf(request.id to true, otherRequest().id to false),
            fixture.posted.associate { it.first.id to it.second })
        assertEquals(listOf(quiet, quiet.copy(start = "08:00", end = "09:00")),
            fixture.postedPreferences.map { it?.quiet })
    }

    @Test fun quietHoursWithoutAZoneUseThePhonesReportedZone() {
        val quiet = QuietHours(enabled = true, start = "23:00", end = "07:00")
        assertTrue(NotificationPolicy.hushForCatchUp(quiet, now, ZoneId.of("UTC")))
        assertFalse(NotificationPolicy.hushForCatchUp(quiet, now, ZoneId.of("Asia/Tokyo")))
        assertFalse(NotificationPolicy.hushForCatchUp(quiet.copy(tz = "not-a-zone"), now, ZoneId.of("Asia/Tokyo")))
    }
}
