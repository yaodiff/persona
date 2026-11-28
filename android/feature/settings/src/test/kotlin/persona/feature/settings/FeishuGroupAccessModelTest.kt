package persona.feature.settings

import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineStart
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import org.junit.Assert.*
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.api.FeishuGroupAccess
import persona.core.contracts.api.FeishuGroupAccessLimits
import persona.core.contracts.api.FeishuGroupAccessMode
import persona.core.contracts.api.FeishuGroupAccessPolicy
import persona.core.contracts.api.FeishuGroupAccessSettings
import persona.core.contracts.api.FeishuGroupChat
import persona.core.contracts.api.FeishuGroupRequest
import persona.core.contracts.api.FeishuGroupUser
import persona.core.contracts.api.FeishuGuestToolOption
import persona.core.hub.HubError
import persona.core.hub.HubFailure

private class FakeGroupAccess(var access: FeishuGroupAccess?) : FeishuGroupAccessSettings {
    val updates = mutableListOf<Pair<Long, FeishuGroupAccessPolicy>>()
    var refuse: Int? = null
    var read: (suspend () -> FeishuGroupAccess?)? = null
    override suspend fun read() = read?.invoke() ?: access
    override suspend fun update(revision: Long, policy: FeishuGroupAccessPolicy): FeishuGroupAccess {
        updates += revision to policy
        refuse?.let { throw HubFailure(HubError.Server, "refused", status = it) }
        val next = (access ?: FeishuGroupAccess()).copy(revision = revision + 1, paired = true, policy = policy)
        access = next
        return next
    }
}

private fun policy(
    mode: String = FeishuGroupAccessMode.OwnerOnly,
    users: List<FeishuGroupUser> = emptyList(),
    chats: List<FeishuGroupChat> = emptyList(),
    blockedUsers: List<FeishuGroupUser> = emptyList(),
    blockedChats: List<FeishuGroupChat> = emptyList(),
    tools: List<String> = emptyList(),
    hourly: Int = 20,
) = FeishuGroupAccessPolicy(mode, users, chats, blockedUsers, blockedChats, tools, hourly)

private fun access(policy: FeishuGroupAccessPolicy = policy(), revision: Long = 3, paired: Boolean = true) = FeishuGroupAccess(
    revision = revision, paired = paired, policy = policy,
    availableTools = listOf(FeishuGuestToolOption("wiki_search", "Wiki"), FeishuGuestToolOption("drive_list", "Drive")),
    recentRequests = listOf(FeishuGroupRequest("ou_2", "Teammate B", "oc_1", "Project", 1795000000)),
    limits = FeishuGroupAccessLimits(),
)

/** Settings › Lark bot › Group access (web `feishu-group-access.test.ts`): the helpers and the read-modify-write. */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class FeishuGroupAccessModelTest {
    @Test fun helpers() {
        val user = FeishuGroupUser("ou_1", "A")
        assertEquals(listOf(user), withGroupMember(emptyList(), user, FeishuGroupUser::openId))
        // An entry already there is replaced, so a label can be corrected.
        assertEquals("Ada", withGroupMember(listOf(user), user.copy(name = "Ada"), FeishuGroupUser::openId).single().name)
        assertTrue(withoutGroupMember(listOf(user), "ou_1", FeishuGroupUser::openId).isEmpty())
        val base = policy(tools = listOf("wiki_search"))
        assertEquals(listOf("wiki_search", "drive_list"), withGuestTool(base, "drive_list", true).guestTools)
        assertTrue(withGuestTool(base, "wiki_search", false).guestTools.isEmpty())
        // A tool the hub no longer offers is dropped, so the next save is not refused for it.
        val stale = base.copy(guestTools = listOf("wiki_search", "gone"))
        assertEquals(listOf("wiki_search"), usableGuestTools(stale, listOf(FeishuGuestToolOption("wiki_search", "Wiki"))).guestTools)
        val request = FeishuGroupRequest("ou_2", "B", "oc_1", "Project", 1)
        val allowed = allowGroupRequest(base, request)
        assertEquals("ou_2", allowed.allowedUsers.single().openId)
        assertEquals("oc_1", allowed.allowedChats.single().chatId)
        // Allowing again keeps the label already there.
        val kept = allowGroupRequest(allowed.copy(allowedUsers = listOf(FeishuGroupUser("ou_2", "Bea"))), request)
        assertEquals("Bea", kept.allowedUsers.single().name)
        assertEquals("ou_2", blockGroupRequest(base, request).blockedUsers.single().openId)
        assertEquals(listOf(5, 10, 15, 20, 40, 60, 120), feishuGroupHourlyChoices(15))
        assertEquals(FeishuGroupHourlyChoices, feishuGroupHourlyChoices(20))
        assertNull(feishuGroupIdError("", taken = false, full = false))
        assertNull(feishuGroupIdError("oc_1", taken = false, full = false))
        assertNotNull(feishuGroupIdError("oc 1", taken = false, full = false))
        assertNotNull(feishuGroupIdError("x".repeat(101), taken = false, full = false))
        assertEquals(R.string.settings_feishu_group_id_taken, feishuGroupIdError("oc_1", taken = true, full = false))
        assertEquals(R.string.settings_feishu_group_list_full, feishuGroupIdError("oc_1", taken = false, full = true))
    }

    @Test fun aMissingRouteHidesTheSection() = runBlocking {
        val model = FeishuGroupAccessModel(FakeGroupAccess(null))
        model.load()
        assertTrue(model.state.value.loaded)
        assertFalse(model.state.value.shown)
        assertNull(model.state.value.loadError)
    }

    @Test fun anUnpairedBotHidesTheSection() = runBlocking {
        val model = FeishuGroupAccessModel(FakeGroupAccess(access(paired = false)))
        model.load()
        assertFalse(model.state.value.shown)
        assertFalse(model.save { it.copy(mode = FeishuGroupAccessMode.Allowlist) })
    }

    @Test fun eachEditSendsTheWholePolicyAndTheRevision() = runBlocking {
        val api = FakeGroupAccess(access())
        val model = FeishuGroupAccessModel(api)
        model.load()
        assertTrue(model.setMode(FeishuGroupAccessMode.Allowlist))
        assertTrue(model.addUser(GroupList.Allowed, " ou_1 ", "Ada"))
        assertTrue(model.addChat(GroupList.Allowed, "oc_1", ""))
        assertTrue(model.allow(FeishuGroupRequest("ou_2", "B", "oc_2", null, 1)))
        assertTrue(model.block(FeishuGroupRequest("ou_3", null, "oc_3", null, 1)))
        assertTrue(model.setTool("wiki_search", true))
        assertTrue(model.setTool("gone", true)) // dropped: the hub no longer offers it
        assertTrue(model.setPerChatHourly(40))
        assertTrue(model.removeUser(GroupList.Allowed, "ou_1"))
        val sent = api.updates
        assertEquals((3L..11L).toList(), sent.map { it.first })
        assertTrue(sent.all { it.second.allowedChats.isNotEmpty() || it == sent[0] || it == sent[1] })
        val last = model.state.value.access!!
        assertEquals(12L, last.revision)
        assertEquals(FeishuGroupAccessMode.Allowlist, last.policy.mode)
        assertEquals(listOf("ou_2"), last.policy.allowedUsers.map { it.openId })
        assertEquals(listOf("oc_1", "oc_2"), last.policy.allowedChats.map { it.chatId })
        assertNull(last.policy.allowedChats.first().name) // an empty name is not sent
        assertEquals(listOf("ou_3"), last.policy.blockedUsers.map { it.openId })
        assertEquals(listOf("wiki_search"), last.policy.guestTools)
        assertEquals(40, last.policy.perChatHourly)
    }

    @Test fun aConflictReloadsAndAFailedSaveLeavesThePage() = runBlocking {
        val api = FakeGroupAccess(access())
        val model = FeishuGroupAccessModel(api)
        model.load()
        api.refuse = 500
        assertFalse(model.setMode(FeishuGroupAccessMode.Allowlist))
        assertEquals(FeishuGroupAccessMode.OwnerOnly, model.state.value.access!!.policy.mode)
        assertEquals(3L, model.state.value.access!!.revision)
        assertEquals(R.string.settings_feishu_group_save_failed, model.state.value.error!!.id)
        assertFalse(model.state.value.saving)

        api.refuse = 409
        api.access = access(policy(FeishuGroupAccessMode.EveryoneInAllowedChats), revision = 9)
        assertFalse(model.setPerChatHourly(5))
        assertEquals(R.string.settings_feishu_group_conflict, model.state.value.error!!.id)
        assertEquals(9L, model.state.value.access!!.revision) // reloaded once
        assertEquals(FeishuGroupAccessMode.EveryoneInAllowedChats, model.state.value.access!!.policy.mode)
        assertFalse(model.state.value.saving)
    }

    @Test(timeout = 10_000) fun anOlderReadCannotUndoASavedChange() = runBlocking {
        val original = access()
        val api = FakeGroupAccess(original)
        val model = FeishuGroupAccessModel(api)
        model.load()
        val read = CompletableDeferred<FeishuGroupAccess?>()
        api.read = { read.await() }
        val refresh = launch(start = CoroutineStart.UNDISPATCHED) { model.load() }
        assertTrue(model.setMode(FeishuGroupAccessMode.Allowlist))
        val saved = model.state.value.access
        read.complete(original)
        refresh.join()
        assertEquals(saved, model.state.value.access)
        assertNull(model.state.value.error)
    }

    @Test fun aFailedReadShowsTheError() = runBlocking {
        val api = FakeGroupAccess(null)
        api.read = { throw HubFailure(HubError.Server, "down", status = 503) }
        val model = FeishuGroupAccessModel(api)
        model.load()
        assertEquals(R.string.settings_feishu_group_load_failed, model.state.value.loadError!!.id)
        assertFalse(model.state.value.shown)
        api.read = null
        api.access = access()
        model.load()
        assertTrue(model.state.value.shown)
        assertNull(model.state.value.loadError)
    }
}
