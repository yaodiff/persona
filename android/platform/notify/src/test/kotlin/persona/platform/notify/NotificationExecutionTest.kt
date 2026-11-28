package persona.platform.notify

import persona.core.contracts.*
import persona.core.contracts.api.DeviceNotificationPreferences
import persona.core.contracts.api.InteractionCenter
import persona.core.contracts.api.InteractionGoneException
import persona.core.contracts.api.SessionsRepository
import persona.core.navigation.AppTab
import persona.core.navigation.NotificationRoute
import persona.core.navigation.TerminalRoute
import persona.core.designsystem.PendingStatus
import persona.core.domain.answer.RequestExpiry
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.async
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.runBlocking
import org.junit.Assert.*
import org.junit.Test
import java.time.Instant

class NotificationExecutionTest {
    private val now = Instant.parse("2030-01-01T00:00:00Z")
    private val request = Interaction("request", machineId = "machine", sessionId = "session", kind = "permission")

    @Test fun extensionAlertsRespectInputAndVisibilityWithoutRefreshingForSessionlessMessages() = runBlocking {
        val center = Center(request)
        var refreshes = 0
        val sessions = object : SessionsRepository {
            override val machines = MutableStateFlow<List<Machine>>(emptyList())
            override val sessions = MutableStateFlow<List<Session>>(emptyList())
            override suspend fun refresh() { refreshes++ }
        }
        var settings: persona.core.contracts.api.DeviceNotificationPreferences? =
            persona.core.contracts.api.DeviceNotificationPreferences(input = false)
        var visible = false
        val posted = mutableListOf<NotifyEvent>()
        val delivery = NotificationDelivery(center, sessions, { visible }, { _, _, _ -> fail("Not an interaction") },
            { posted += it }, {}, { settings })
        val alert = NotifyEvent("alert", "machine", "session", title = "提醒", body = "请查看任务")
        delivery.accept(alert)
        assertTrue(posted.isEmpty())
        assertEquals(0, refreshes)
        settings = persona.core.contracts.api.DeviceNotificationPreferences()
        visible = true
        delivery.accept(alert)
        assertTrue(posted.isEmpty())
        visible = false
        delivery.accept(alert)
        assertEquals(listOf(alert), posted)
        assertEquals(1, refreshes)
        // An unscoped reminder is a message, even while a conversation is visible.
        visible = true
        val sessionless = alert.copy(machineId = "", sessionId = "")
        delivery.accept(sessionless)
        assertEquals(listOf(alert, sessionless), posted)
        assertEquals(1, refreshes)
        settings = null
        delivery.accept(sessionless)
        assertEquals(2, posted.size)
        assertEquals(0, center.refreshes)
    }

    @Test fun deadlineGuardsAnswerAndApprovalEvenWhenPersistenceCrossesTheDeadline() = runBlocking {
        val deadline = now.plusSeconds(1)
        val requests = listOf(
            request to "allow",
            request.copy(kind = "plan") to "approve_edits",
            request.copy(kind = "question", questions = listOf(Question("选择", options = listOf(QuestionOption("继续"))))) to "answer.0",
            request.copy(kind = "screen", dialog = ScreenDialog(options = listOf(DialogOption("1", "继续")))) to "option.1",
        )
        for ((ix, action) in requests) {
            for (expiresAt in listOf(null, deadline.toString(), now.toString(), now.minusSeconds(1).toString(), "invalid")) {
                val center = Center(ix.copy(expiresAt = expiresAt))
                val progress = mutableListOf<PendingStatus>()
                val executor = NotificationActionExecutor(center, { true }, { now })
                val expired = RequestExpiry.expired(expiresAt, now)
                assertEquals(if (expired) NotificationActionResult.Expired else NotificationActionResult.Submitted,
                    executor.execute(ix.id, action, null, { false }, onProgress = { progress += it }))
                assertEquals(if (expired) 0 else 1, center.writes)
                assertEquals(if (expired) listOf(PendingStatus.Verifying)
                    else listOf(PendingStatus.Verifying, PendingStatus.Sending), progress)
            }
            val center = Center(ix.copy(expiresAt = deadline.toString()))
            var time = now
            val executor = NotificationActionExecutor(center, { time = deadline; true }, { time })
            assertEquals(NotificationActionResult.Expired, executor.execute(ix.id, action, null, { false }))
            assertEquals(0, center.writes)

            val stale = Center(ix)
            val beforeRefresh = NotificationActionExecutor(stale, { error("Expired actions must not be claimed") }, { now })
            assertEquals(NotificationActionResult.Expired,
                beforeRefresh.execute(ix.id, action, null, { false }, expiresAt = now.toString()))
            assertEquals(0, stale.refreshes)
            assertEquals(0, stale.writes)
        }
        val link = terminalLink(TerminalRoute("machine & test", "session/分支"))
        val parameters = java.net.URI(link).rawQuery.split('&').associate {
            it.substringBefore('=') to java.net.URLDecoder.decode(it.substringAfter('='), "UTF-8")
        }
        assertEquals(mapOf("target" to "terminal", "machine" to "machine & test", "session" to "session/分支"), parameters)
    }

    @Test fun staleNotificationOccurrenceCannotAnswerItsReplacement() = runBlocking {
        val original = request.copy(createdAt = "2030-01-01T00:00:00.000000001Z")
        val replacement = original.copy(createdAt = "2030-01-01T00:00:00.000000002Z")
        val center = Center(original).apply { remote = replacement }
        val executor = NotificationActionExecutor(center, { error("A stale occurrence must not be claimed") }, { now })
        assertEquals(NotificationActionResult.Gone, executor.execute(original.id, "allow", null, { false },
            createdAt = original.createdAt))
        assertEquals(1, center.refreshes)
        assertEquals(0, center.writes)
    }

    @Test fun staleNotificationChoiceCannotSubmitAfterAnIrreversibleRefresh() = runBlocking {
        val card = request.copy(kind = "question", questions = listOf(
            Question("Continue?", options = listOf(QuestionOption("Continue"))),
        ))
        val center = Center(card).apply { remote = card.copy(reversible = false) }
        val executor = NotificationActionExecutor(center, { error("An irreversible action must not be claimed") }, { now })
        assertEquals(NotificationActionResult.Invalid, executor.execute(card.id, "answer.0", null, { false }))
        assertEquals(1, center.refreshes)
        assertEquals(0, center.writes)
    }

    @Test fun aStaleAllowCannotApproveASecretHandoffRevealedByRefresh() = runBlocking {
        val handoff = JsonCodec.decodeFromString<Interaction>(
            """{"id":"request","machine_id":"machine","session_id":"session","kind":"permission","tool_name":"secret_handoff","tool_input":{"variables":[{"name":"TOKEN"}]}}""")
        val center = Center(request).apply { remote = handoff }
        val executor = NotificationActionExecutor(center, { error("A hand-off must not be claimed") }, { now })
        assertEquals(NotificationActionResult.Invalid, executor.execute(request.id, "allow", null, { false }))
        assertEquals(1, center.refreshes)
        assertEquals(0, center.writes)
    }

    @Test fun staleNotificationChoiceCannotSubmitAfterScreenDialogBecomesMultiSelect() = runBlocking {
        val single = request.copy(
            kind = "screen",
            dialog = ScreenDialog(options = listOf(DialogOption("1", "Alpha"), DialogOption("2", "Beta")), multi = false),
        )
        val center = Center(single).apply { remote = single.copy(dialog = single.dialog?.copy(multi = true)) }
        val executor = NotificationActionExecutor(center, { error("A multi-select screen dialog must not be claimed") }, { now })
        assertEquals(NotificationActionResult.Invalid, executor.execute(single.id, "option.1", null, { false }))
        assertEquals(1, center.refreshes)
        assertEquals(0, center.writes)
    }

    @Test fun refreshExpiryLockAndDurableClaimGuardEveryResolve() = runBlocking {
        val center = Center(request)
        val claims = mutableMapOf<String, String>()
        fun executor() = NotificationActionExecutor(center, { id ->
            synchronized(claims) { if (id in claims) false else { claims[id] = "operation"; true } }
        }, { now })
        suspend fun execute(action: String = "allow", locked: Boolean = false, reply: String? = null) =
            executor().execute(request.id, action, reply, { locked })

        center.remote = null
        assertEquals(NotificationActionResult.Gone, execute())
        assertEquals(0, center.writes)
        for (deadline in listOf(now.toString(), now.minusSeconds(1).toString(), "invalid")) {
            center.remote = request.copy(expiresAt = deadline)
            assertEquals(NotificationActionResult.Expired, execute())
        }
        center.remote = request.copy(expiresAt = now.plusSeconds(1).toString())
        assertEquals(NotificationActionResult.Locked, execute(locked = true))
        assertEquals(NotificationActionResult.Invalid, execute(reply = "不要执行"))
        assertTrue(claims.isEmpty())

        val sent = CompletableDeferred<Unit>()
        val release = CompletableDeferred<Unit>()
        center.onResolve = { sent.complete(Unit); release.await() }
        val first = async { execute(action = "deny", locked = true, reply = "需要确认") }
        sent.await()
        assertEquals(NotificationActionResult.Duplicate, execute())
        release.complete(Unit)
        assertEquals(NotificationActionResult.Submitted, first.await())
        assertEquals("需要确认", center.last?.message)
        assertEquals(false, center.last?.allow)
        assertEquals(1, center.writes)
        // Recreating the executor must not reset the claim after an ambiguous response.
        claims.clear()
        center.onResolve = { throw java.io.IOException("Response lost") }
        try { execute(); fail("Expected ambiguous failure") } catch (_: java.io.IOException) { }
        assertEquals(NotificationActionResult.Duplicate, execute())
        assertEquals(2, center.writes)
        claims.clear()
        center.onResolve = { throw InteractionGoneException() }
        assertEquals(NotificationActionResult.Gone, execute())
        assertEquals(3, center.writes)
    }

    @Test fun aSecondTapOnARequestThisPhoneAnsweredSaysNothingRatherThanHandledOnTheComputer() = runBlocking {
        // #2483: the first tap's answer took the request off the hub before its notification went away, and a second tap
        // finds it gone. Claimed here, it was this phone's own answer: no notice, no second resolve. Not claimed here, the
        // computer answered it, as before.
        for ((claimedHere, expected) in listOf(
            true to NotificationActionResult.AnsweredHere,
            false to NotificationActionResult.Gone,
        )) {
            val center = Center(request)
            center.remote = null
            val executor = NotificationActionExecutor(center, { fail("A request that is gone is never claimed"); false }, { now },
                claimed = { it == request.id && claimedHere })
            val result = executor.execute(request.id, "allow", null, { false })
            assertEquals("claimed here: $claimedHere", expected, result)
            assertEquals(0, center.writes)
            if (claimedHere) assertNull(actionResultCopy(result))
            else assertEquals(R.string.notify_result_gone_title to R.string.notify_result_gone_detail, actionResultCopy(result))
        }
        // The whole sequence with one journal: the first tap answers, the second finds the request gone.
        val center = Center(request)
        val claims = mutableSetOf<String>()
        fun executor() = NotificationActionExecutor(center, { synchronized(claims) { claims.add(it) } }, { now },
            claimed = { synchronized(claims) { it in claims } })
        assertEquals(NotificationActionResult.Submitted, executor().execute(request.id, "allow", null, { false }))
        center.remote = null
        assertEquals(NotificationActionResult.AnsweredHere, executor().execute(request.id, "allow", null, { false }))
        assertEquals(1, center.writes)
    }

    @Test fun aHubThatCannotBeAskedMeansNothingWasSent() = runBlocking {
        // #5008: the check before the claim failed, so the tap can be repeated; only a lost reply after it is ambiguous.
        val center = Center(request)
        val claims = mutableSetOf<String>()
        val executor = NotificationActionExecutor(center, { synchronized(claims) { claims.add(it) } }, { now })
        val progress = mutableListOf<PendingStatus>()
        center.refreshFailure = java.io.IOException("Unreachable")
        assertEquals(NotificationActionResult.NotSent, executor.execute(request.id, "allow", null, { false }, onProgress = { progress += it }))
        assertEquals(listOf(PendingStatus.Verifying), progress)
        assertTrue(claims.isEmpty())
        assertEquals(0, center.writes)
        center.refreshFailure = null
        assertEquals(NotificationActionResult.Submitted, executor.execute(request.id, "allow", null, { false }))
        assertEquals(1, center.writes)
    }

    @Test fun theRequestsOwnGroupThatCannotBeAskedMeansNothingWasSentEvenWhenAnotherGroupAnswers() = runBlocking {
        // #5697: with two hub groups, the merged refresh succeeds when any group does; only the request's own group counts.
        val center = object : InteractionCenter {
            override val pending = MutableStateFlow(listOf(request))
            var writes = 0
            override suspend fun refresh() = Unit
            override suspend fun refresh(id: String) { throw java.io.IOException("Unreachable") }
            override suspend fun resolve(id: String, resolution: Resolution) { writes++ }
        }
        val claims = mutableSetOf<String>()
        val executor = NotificationActionExecutor(center, { synchronized(claims) { claims.add(it) } }, { now })
        assertEquals(NotificationActionResult.NotSent, executor.execute(request.id, "allow", null, { false }))
        assertTrue(claims.isEmpty())
        assertEquals(0, center.writes)
    }

    @Test fun aSessionThatCannotApproveIsAnsweredOnTheComputer() = runBlocking {
        val center = Center(request)
        val history = Session(machineId = "machine", sessionId = "session", sourceKind = "codex_app_server",
            capabilities = SessionCapabilities(read = true, send = true, attach = true))
        val notJoined = NotificationActionExecutor(center, { true }, { now }, session = { history })
        assertEquals(NotificationActionResult.ReadOnly, notJoined.execute(request.id, "allow", null, { false }))
        assertEquals(0, center.writes)
        assertNotNull(actionResultCopy(NotificationActionResult.ReadOnly))
        // Joined live, the thread's approvals can be answered; an unknown session is left to the hub.
        val joined = history.copy(attached = true, capabilities = history.capabilities!!.copy(approve = true))
        assertEquals(NotificationActionResult.Submitted,
            NotificationActionExecutor(center, { true }, { now }, session = { joined }).execute(request.id, "allow", null, { false }))
        assertEquals(NotificationActionResult.Submitted,
            NotificationActionExecutor(center, { true }, { now }).execute(request.id, "allow", null, { false }))
        assertEquals(2, center.writes)
    }

    @Test fun onlyHubApprovedAlertsAreDeliveredAndRoutesKeepTheirTab() = runBlocking {
        val center = Center(request)
        val sessions = object : SessionsRepository {
            override val machines = MutableStateFlow<List<Machine>>(emptyList())
            override val sessions = MutableStateFlow(listOf(Session(machineId = "machine", sessionId = "session", assistant = true)))
            override suspend fun refresh() = Unit
        }
        var visible = false
        val posted = mutableListOf<Interaction>()
        val notifications = mutableListOf<NotifyEvent>()
        val retracted = mutableListOf<String>()
        val delivery = NotificationDelivery(center, sessions, { visible },
            { ix, _, _ -> posted += ix }, { notifications += it }, { retracted += it })
        val notice = NotifyEvent("interaction", "machine", "session", interactionId = request.id)
        delivery.accept(InteractionEvent(request))
        assertTrue(posted.isEmpty())
        assertEquals(0, center.refreshes)
        delivery.accept(notice)
        assertEquals(listOf(request), posted)
        visible = true
        delivery.accept(notice)
        assertEquals(1, posted.size)
        visible = false
        center.remote = request.copy(expiresAt = "2000-01-01T00:00:00Z")
        delivery.accept(notice)
        center.remote = null
        delivery.accept(notice)
        assertEquals(1, posted.size)
        delivery.accept(InteractionResolvedEvent(request.id))
        assertEquals(listOf(request.id, request.id), retracted)
        delivery.accept(notice.copy(kind = "done"))
        delivery.accept(notice.copy(kind = "assistant"))
        assertEquals(listOf("done", "assistant"), notifications.map { it.kind })
        assertEquals(AppTab.Persona, NotificationRoute.conversation("machine", "session", true).tab)
        assertEquals(AppTab.Agents, NotificationRoute.conversation("machine", "session", false).tab)
        assertNull(NotificationRoute.conversation("machine", "session", null).tab)
    }

    // `personad ask` in a plain terminal (#4515): the card has no session. It is still told, and no conversation being
    // looked at holds it back, since it has none.
    @Test fun aRequestOutsideAnySessionIsNotified() = runBlocking {
        val script = Interaction("script", machineId = "machine", kind = "permission", summary = "Deploy?")
        val center = Center(script)
        val sessions = object : SessionsRepository {
            override val machines = MutableStateFlow<List<Machine>>(emptyList())
            override val sessions = MutableStateFlow(listOf(Session(machineId = "machine", sessionId = "session")))
            override suspend fun refresh() = Unit
        }
        val posted = mutableListOf<Interaction>()
        val delivery = NotificationDelivery(center, sessions, { true }, { ix, _, _ -> posted += ix }, { fail("Not an alert") }, {})
        delivery.accept(NotifyEvent("interaction", "machine", "", interactionId = script.id))
        assertEquals(listOf(script), posted)
        // The same id under another machine is not this card.
        delivery.accept(NotifyEvent("interaction", "other", "", interactionId = script.id))
        assertEquals(1, posted.size)
    }

    @Test fun devicePreferencesGateRawInteractionsAndCompletionWithoutChangingHubLongPolicy() = runBlocking {
        val center = Center(request)
        val sessions = object : SessionsRepository {
            override val machines = MutableStateFlow<List<Machine>>(emptyList())
            override val sessions = MutableStateFlow(listOf(Session(machineId = "machine", sessionId = "session")))
            override suspend fun refresh() = Unit
        }
        var settings: persona.core.contracts.api.DeviceNotificationPreferences? =
            persona.core.contracts.api.DeviceNotificationPreferences(input = false, done = persona.core.contracts.api.DoneNotification.Off)
        var posted = 0
        val delivery = NotificationDelivery(center, sessions, { false }, { _, _, _ -> posted++ }, { posted++ }, {}, { settings })
        val notice = NotifyEvent("interaction", "machine", "session", interactionId = request.id)
        delivery.accept(InteractionEvent(request))
        delivery.accept(notice)
        delivery.accept(notice.copy(kind = "done"))
        assertEquals(0, posted)
        assertEquals(0, center.refreshes)
        settings = persona.core.contracts.api.DeviceNotificationPreferences(done = persona.core.contracts.api.DoneNotification.Long)
        delivery.accept(notice)
        delivery.accept(notice.copy(kind = "done"))
        assertEquals(2, posted)
        settings = null
        delivery.accept(notice)
        assertEquals(2, posted)
    }

    @Test fun quietTaskAgentsGetNoAlerts() = runBlocking {
        val center = Center(request)
        val sessions = object : SessionsRepository {
            override val machines = MutableStateFlow<List<Machine>>(emptyList())
            override val sessions = MutableStateFlow(listOf(Session(machineId = "machine", sessionId = "session", quiet = true)))
            override suspend fun refresh() = Unit
        }
        var posted = 0
        val delivery = NotificationDelivery(center, sessions, { false }, { _, _, _ -> posted++ }, { posted++ }, {})
        val notice = NotifyEvent("interaction", "machine", "session", interactionId = request.id)
        delivery.accept(notice)
        delivery.accept(notice.copy(kind = "done"))
        assertEquals(0, posted)
        sessions.sessions.value = listOf(Session(machineId = "machine", sessionId = "session"))
        delivery.accept(notice.copy(kind = "done"))
        assertEquals(1, posted)
    }

    // Quiet hours (#1863): a card the hub marks `quiet` (inside this device's window) is still shown, without a sound.
    @Test fun aCardInsideQuietHoursIsPostedHushed() = runBlocking {
        val center = Center(request)
        val sessions = object : SessionsRepository {
            override val machines = MutableStateFlow<List<Machine>>(emptyList())
            override val sessions = MutableStateFlow(listOf(Session(machineId = "machine", sessionId = "session")))
            override suspend fun refresh() = Unit
        }
        val hushed = mutableListOf<Boolean>()
        val deliveryPreferences = mutableListOf<DeviceNotificationPreferences?>()
        val delivery = NotificationDelivery(center, sessions, { false }, { _, quiet, prefs ->
            hushed += quiet
            deliveryPreferences += prefs
        }, { fail("Not a card") }, {})
        val notice = NotifyEvent("interaction", "machine", "session", interactionId = request.id)
        delivery.accept(notice)
        delivery.accept(notice.copy(quiet = true))
        assertEquals(listOf(false, true), hushed)
        assertEquals(listOf(null, null), deliveryPreferences)
    }

    private fun prepared(expiresAt: String, id: String = "request") = JsonCodec.decodeFromString<Interaction>(
        """{"id":"$id","machine_id":"machine","session_id":"session","kind":"permission",""" +
            """"expires_at":"$expiresAt","can_clarify":true}""",
    )

    @Test fun aPreparedNotificationActionUsesTheHubsExtendedDeadline() = runBlocking {
        val current = prepared(now.plusSeconds(3_600).toString())
        val center = Center(current)
        val executor = NotificationActionExecutor(center, { true }, { now })
        assertEquals(NotificationActionResult.Submitted, executor.execute(current.id, "allow", null, { false },
            expiresAt = now.minusSeconds(1).toString()))
        assertEquals(1, center.refreshes)
        assertEquals(1, center.writes)
        assertEquals(true, center.last?.allow)
    }

    @Test fun aPreparedActionStillChecksActualExpiryAndWhetherTheCardIsGone() = runBlocking {
        val delivered = prepared(now.plusSeconds(60).toString())
        val expiredCenter = Center(delivered).apply { remote = prepared(now.toString()) }
        val goneCenter = Center(delivered).apply { remote = null }
        var claims = 0
        fun executor(center: Center) = NotificationActionExecutor(center, { claims++; true }, { now })
        val results = listOf(expiredCenter, goneCenter).map { center ->
            executor(center).execute(delivered.id, "allow", null, { false },
                expiresAt = now.minusSeconds(1).toString())
        }
        assertEquals(listOf(NotificationActionResult.Expired, NotificationActionResult.Gone), results)
        assertEquals(listOf(1, 1), listOf(expiredCenter.refreshes, goneCenter.refreshes))
        assertEquals(0, claims)
        assertEquals(0, expiredCenter.writes + goneCenter.writes)
    }

    @Test fun theServerStillRejectsAStalePreparedExecution() = runBlocking {
        val current = prepared(now.plusSeconds(3_600).toString())
        val center = Center(current).apply { onResolve = { throw InteractionGoneException("stale") } }
        val executor = NotificationActionExecutor(center, { true }, { now })
        assertEquals(NotificationActionResult.Gone, executor.execute(current.id, "allow", null, { false },
            expiresAt = now.minusSeconds(1).toString()))
        assertEquals(1, center.refreshes)
        assertEquals(1, center.writes)
    }

    @Test fun anOldHubNotificationDeadlineStillFailsClosedBeforeARefresh() = runBlocking {
        val center = Center(request.copy(expiresAt = now.plusSeconds(3_600).toString()))
        val executor = NotificationActionExecutor(center, { error("An old expired action must not be claimed") }, { now })
        assertEquals(NotificationActionResult.Expired, executor.execute(request.id, "allow", null, { false },
            expiresAt = now.minusSeconds(1).toString()))
        assertEquals(0, center.refreshes)
        assertEquals(0, center.writes)
    }

    @Test fun anUpdatedPreparedCardRedrawsOnlyItsDeliveredNotification() = runBlocking {
        val original = prepared(now.plusSeconds(60).toString())
        val updated = prepared(now.plusSeconds(3_600).toString())
        val center = Center(original)
        val sessions = object : SessionsRepository {
            override val machines = MutableStateFlow<List<Machine>>(emptyList())
            override val sessions = MutableStateFlow(listOf(Session(machineId = "machine", sessionId = "session")))
            override suspend fun refresh() = Unit
        }
        val notified = mutableSetOf<String>()
        val posted = mutableListOf<Interaction>()
        val retracted = mutableListOf<String>()
        val delivery = NotificationDelivery(center, sessions, { false },
            { ix, _, _ -> posted += ix; notified += ix.id }, { fail("Not an alert") }, { retracted += it },
            notified = { notified })
        val notice = NotifyEvent("interaction", "machine", "session", interactionId = original.id)
        delivery.accept(notice)
        center.remote = updated
        delivery.accept(InteractionEvent(updated))
        // An interaction update is not a new push for a card this device never delivered.
        center.remote = prepared(now.plusSeconds(7_200).toString(), id = "unseen")
        delivery.accept(InteractionEvent(center.remote!!))
        delivery.accept(InteractionResolvedEvent(original.id))
        assertEquals(listOf(original, updated), posted)
        assertEquals(listOf(original.id), retracted)
    }

    @Test fun interactionSnapshotsNeverRepostDismissedOrOrdinaryCards() = runBlocking {
        val delivered = prepared(now.plusSeconds(3_600).toString())
        for (current in listOf(request, delivered)) {
            val center = Center(current)
            val sessions = object : SessionsRepository {
                override val machines = MutableStateFlow<List<Machine>>(emptyList())
                override val sessions = MutableStateFlow(listOf(Session(machineId = "machine", sessionId = "session")))
                override suspend fun refresh() = Unit
            }
            val posted = mutableListOf<Interaction>()
            val delivery = NotificationDelivery(center, sessions, { false },
                { ix, _, _ -> posted += ix }, { fail("Not an alert") }, {},
                notified = { setOf(current.id) }, showing = { emptySet() })
            delivery.accept(InteractionEvent(current))
            assertEquals(emptyList<Interaction>(), posted)
        }
    }

    private class Center(request: Interaction) : InteractionCenter {
        override val pending = MutableStateFlow(listOf(request))
        var remote: Interaction? = request
        var refreshes = 0
        var writes = 0
        var last: Resolution? = null
        var onResolve: suspend () -> Unit = {}
        var refreshFailure: Exception? = null
        override suspend fun refresh() { refreshes++; refreshFailure?.let { throw it }; pending.value = listOfNotNull(remote) }
        override suspend fun resolve(id: String, resolution: Resolution) {
            writes++
            last = resolution
            onResolve()
        }
    }
}
