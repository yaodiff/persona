package persona.feature.chat

import android.content.res.Resources
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateMapOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.runtime.staticCompositionLocalOf
import persona.core.contracts.AgentState
import persona.core.contracts.GroupKeys
import persona.core.contracts.Machine
import persona.core.contracts.ProtocolCompatibility
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.parseTime
import persona.core.contracts.api.AssistantSnapshot
import persona.core.designsystem.LocalizedFormats
import persona.core.domain.MessageContext
import persona.core.domain.sort.canSend
import java.net.URI
import java.net.URLDecoder
import java.time.Instant
import java.time.OffsetDateTime
import java.time.ZoneId
import java.time.format.DateTimeFormatter
import java.util.Locale
import kotlinx.coroutines.flow.StateFlow

// Forwarding a message (#3901, Android's side of #3807; iOS Forward.swift, web lib/forward*.ts; docs/protocol/protocol.md
// "Forwarded messages", shared cases in spec/fixtures/forward): long-press › Forward › a target, and that chat opens with the
// message over its composer as a card. What goes out is the user's own message there: a `<persona-context kind="forward">`
// block (the original and where it came from) before the note typed under it, sent with the chat's usual send. Nothing is
// written back to the original chat, and no hub endpoint is involved.

/** The forward block: what it carries and how it is written and read (iOS `ForwardMessage`). */
object ForwardMessage {
    const val KIND = ChatLinkContext.FORWARD
    /** The most of the original a forward carries; a longer one is cut with a line saying so. */
    const val LIMIT = 20_000
    const val MOST = 30
    private const val TOTAL = 60_000

    fun limit(count: Int): Int = minOf(LIMIT, TOTAL / count.coerceAtLeast(1))

    /** Whose message it was: the user's own, Persona's, an Agent's, or another source's (a relay, another session's Claude). */
    enum class Role(val wire: String) { User("user"), Persona("persona"), Agent("agent"), Other("other") }

    /** Where the forwarded message is and who wrote it, as the chat it was pressed in knows them. */
    data class Source(
        /** The chat as the app lists it (a linked group's machine ids carry its suffix, GroupKeys). */
        val key: SessionKey,
        val item: String,
        val role: Role,
        /** Who wrote it, by name ("Persona", the Agent's "Claude", a relay's source); unused for the user's own. */
        val author: String,
        /** The chat's title. */
        val chat: String,
        /** The Agent's project ("weather › tests"); null for Persona's chats. */
        val project: String? = null,
        /** The computer's name (not its id); null for Persona's chats. */
        val machine: String? = null,
        val time: Instant? = null,
    )

    /** The block for a message whose text, as the transcript has it, is [raw]. */
    fun ref(raw: String, source: Source, resources: Resources, zone: ZoneId = ZoneId.systemDefault(), limit: Int = LIMIT): ChatLinkContext {
        val extra = linkedMapOf(
            "role" to source.role.wire,
            // The user's own message is "Me" in the sender's language, as iOS and the web write it.
            "author" to if (source.role == Role.User) resources.getString(R.string.chat_forward_me) else source.author,
            "chat" to source.chat,
        )
        source.project?.takeIf { it.isNotEmpty() }?.let { extra["project"] = it }
        source.machine?.takeIf { it.isNotEmpty() }?.let { extra["machine"] = it }
        source.time?.let { extra["time"] = time(it, zone) }
        return ChatLinkContext(KIND, heading(extra, resources), url(source.key, source.item),
            body(raw, source.machine, source.role == Role.User, resources, limit), msg = source.item, extra = extra)
    }

    /** The block a message's text holds, read back (the new-agent form's card); null when it holds no forward. */
    fun read(text: String): ChatLinkContext? = ChatLinkContext.parse(text).first.firstOrNull { it.isForward }

    /** All forwarded messages in wire order. */
    fun readAll(text: String): List<ChatLinkContext> = ChatLinkContext.parse(text).first.filter(ChatLinkContext::isForward)

    fun split(text: String): Pair<ChatLinkContext?, String> {
        val (all, note) = splitAll(text)
        return all.firstOrNull() to note
    }

    /** Restore every forwarded block plus the original note and any other contexts on a pending start. */
    fun splitAll(text: String): Pair<List<ChatLinkContext>, String> {
        val forwards = readAll(text)
        return forwards to if (forwards.isEmpty()) text else ChatLinkContext.withoutForwards(text)
    }

    private val quoteKinds = setOf(ChatLinkContext.QUOTE, "context", "edit", KIND)

    /**
     * What of [raw] a forward carries: the words as the chat shows them, without the hub's metadata, photo notes and file
     * tags; the quotes and forwards it opened with as `> ` lines led by their heading; files sent with it by name; cut at
     * [LIMIT] characters.
     */
    fun body(raw: String, machine: String?, user: Boolean, resources: Resources, limit: Int = LIMIT): String {
        val model = messageModel(raw, user = user)
        val parts = mutableListOf<String>()
        for (c in model.contexts) {
            if (c.kind !in quoteKinds) continue
            parts += (listOf(quoteHeading(c, resources)) + c.body.split('\n')).joinToString("\n") { "> $it" }
        }
        model.plain.takeIf { it.isNotBlank() }?.let(parts::add)
        for (f in model.attached) parts += machine?.takeIf { it.isNotEmpty() }
            ?.let { resources.getString(R.string.chat_forward_file_on, it, f.name) } ?: resources.getString(R.string.chat_forward_file, f.name)
        val text = parts.joinToString("\n\n")
        if (text.codePointCount(0, text.length) <= limit) return text
        return text.substring(0, text.offsetByCodePoints(0, limit)) + "\n\n" + resources.getString(R.string.chat_forward_cut)
    }

    /** What a quote or forward block a message opened with says about itself, as its `> ` lines are led by. */
    private fun quoteHeading(c: MessageContext, resources: Resources): String = when {
        c.kind == KIND -> heading(c.attributes, resources)
        c.kind == ChatLinkContext.QUOTE && c.title == ChatLinkContext.QUOTE_ME -> resources.getString(R.string.chat_quote_yours)
        c.url?.let(::isSessionContextUrl) == true -> resources.getString(R.string.chat_quote_session, c.title)
        else -> resources.getString(R.string.chat_quote_from,
            c.title.ifBlank { c.url ?: resources.getString(R.string.chat_forward_messages) })
    }

    // What URLComponents leaves as it is in a path segment and in a query value (iOS writes the link; the web the same).
    private const val PATH_SAFE = "!$&'()*+,;=:@"
    private const val QUERY_SAFE = "!$&'()*+,;=:@/?"

    private fun escape(value: String, safe: String): String = buildString {
        for (byte in value.toByteArray(Charsets.UTF_8)) {
            val code = byte.toInt() and 0xff
            val char = code.toChar()
            if (code < 0x80 && (char in 'a'..'z' || char in 'A'..'Z' || char in '0'..'9' || char in "-._~" || char in safe)) append(char)
            else append('%').append("%02X".format(Locale.ROOT, code))
        }
    }

    /** The original message's address: `persona://session/<hub's machine id>/<session id>?at=<item id>`, `#` as `%23`. */
    fun url(key: SessionKey, item: String): String =
        "persona://session/${escape(GroupKeys.raw(key.machineId), PATH_SAFE)}/${escape(key.sessionId, PATH_SAFE)}" +
            if (item.isEmpty()) "" else "?at=${escape(item, QUERY_SAFE)}"

    /** The chat and message a forward's link names, as its hub calls them ([msg] when the link has no item). */
    data class Origin(val machine: String, val session: String, val item: String?) {
        fun sameChat(other: Origin?): Boolean = machine == other?.machine && session == other.session
        /** The chat as this app lists it, for a forward read in a chat of the group of [near]. */
        fun key(near: SessionKey): SessionKey = SessionKey(GroupKeys.machine(machine, GroupKeys.namespaceOf(near.machineId)), session)
    }

    private fun decode(value: String): String = URLDecoder.decode(value.replace("+", "%2B"), "UTF-8")

    fun origin(url: String?, msg: String? = null): Origin? {
        val uri = runCatching { URI(url.orEmpty().trim()) }.getOrNull() ?: return null
        if (uri.scheme?.lowercase() !in ProtocolCompatibility.appSchemes || uri.host?.lowercase() != "session") return null
        val parts = runCatching { uri.rawPath.orEmpty().split('/').filter { it.isNotEmpty() }.map(::decode) }.getOrNull() ?: return null
        if (parts.size != 2) return null
        val at = uri.rawQuery.orEmpty().split('&').firstOrNull { it.startsWith("at=") }?.removePrefix("at=")
            ?.let { runCatching { decode(it) }.getOrNull() }?.takeIf { it.isNotEmpty() }
        return Origin(parts[0], parts[1], at ?: msg?.takeIf { it.isNotEmpty() })
    }

    fun origin(ref: ChatLinkContext): Origin? = origin(ref.url, ref.msg)

    /** `time` as written: ISO 8601 to the second with the sender's offset ("2026-11-11T10:24:00+08:00", "Z" in UTC). */
    fun time(at: Instant, zone: ZoneId = ZoneId.systemDefault()): String =
        DateTimeFormatter.ofPattern("yyyy-MM-dd'T'HH:mm:ssXXX", Locale.ROOT).withZone(zone).format(at)

    /** The card's first line: "Forwarded from Claude · Network tests". */
    fun heading(attributes: Map<String, String>, resources: Resources): String {
        val who = shownAuthor(attributes, resources)
        val chat = attributes["chat"].orEmpty()
        return if (chat.isEmpty()) resources.getString(R.string.chat_forward_heading, who)
            else resources.getString(R.string.chat_forward_heading_chat, who, chat)
    }

    /** The author as this phone names it: the user's own message is "Me" in this phone's language. */
    fun shownAuthor(attributes: Map<String, String>, resources: Resources): String =
        if (attributes["role"] == Role.User.wire) resources.getString(R.string.chat_forward_me)
        else attributes["author"]?.takeIf { it.isNotEmpty() } ?: resources.getString(R.string.chat_forward_messages)

    /** The card's second line: the computer and when ("MacBook Pro · 10:24"); null when it knows neither. */
    fun meta(attributes: Map<String, String>, resources: Resources, locale: Locale, now: Instant = Instant.now(),
        zone: ZoneId = ZoneId.systemDefault()): String? {
        val machine = attributes["machine"]?.takeIf { it.isNotEmpty() }
        val time = attributes["time"]?.let { runCatching { OffsetDateTime.parse(it).toInstant() }.getOrNull() }
            ?.let { shortTime(it, locale, now, zone) }
        return when {
            machine != null && time != null -> resources.getString(R.string.chat_forward_meta, machine, time)
            else -> machine ?: time
        }
    }

    /** A time of today as the time alone, another day's with its date. */
    fun shortTime(at: Instant, locale: Locale, now: Instant = Instant.now(), zone: ZoneId = ZoneId.systemDefault()): String =
        if (at.atZone(zone).toLocalDate() == now.atZone(zone).toLocalDate()) LocalizedFormats.format(at, LocalizedFormats.TIME, locale, zone)
        else LocalizedFormats.format(at, LocalizedFormats.MONTH_DAY_TIME, locale, zone)

    /** A forward's words in its card: its own lines, blank ones dropped. */
    fun summary(text: String, resources: Resources): String {
        val lines = text.lines().map { it.trim() }.filter { it.isNotEmpty() }
        return if (lines.isEmpty()) resources.getString(R.string.chat_forward_no_content) else lines.joinToString("\n").take(1200)
    }

    /** Where a forward goes, for the line under its card: the target's Agent ("Codex") and computer; null is Persona. */
    data class RouteTarget(val agent: String, val machine: String?)

    /** The composer's line under the card: which computer it comes from and where it goes (iOS `ForwardMessage.route`). */
    fun route(from: String?, to: RouteTarget?, resources: Resources): String {
        val source = from?.takeIf { it.isNotEmpty() }
        if (to == null) return source?.let { resources.getString(R.string.chat_forward_route_from_to_persona, it) }
            ?: resources.getString(R.string.chat_forward_route_to_persona)
        val machine = to.machine?.takeIf { it.isNotEmpty() } ?: return resources.getString(R.string.chat_forward_route_to_agent, to.agent)
        if (source != null && source != machine) return resources.getString(R.string.chat_forward_route_from_to_agent_on, source, to.agent, machine)
        return resources.getString(R.string.chat_forward_route_to_agent_on, to.agent, machine)
    }
}

/** A tab's label as iOS `slotLabel` gives it: its name, or "#n" when that is only the host's number. */
internal val Session.tabLabel: String? get() {
    val name = slotName?.takeIf { it.isNotEmpty() } ?: return slotIndex?.let { "#$it" }
    return if (slotIndex != null && name == slotIndex.toString()) "#$slotIndex" else name
}

/** The Agent's project as a forward names it: "space › tab". */
internal val Session.forwardProject: String? get() =
    listOfNotNull(spaceName?.takeIf { it.isNotBlank() }, tabLabel).joinToString(" › ").ifEmpty { null }

/** Where a forward can go. */
sealed interface ForwardTarget {
    /** Persona's main conversation (the new conversation's composer when there is none yet). */
    data object Persona : ForwardTarget
    /** A new Agent: the new-agent form with the forward over its first message. */
    data object NewAgent : ForwardTarget
    /** An Agent's chat or another Persona conversation. */
    data class Chat(val key: SessionKey) : ForwardTarget

    /** How Recent remembers it; a new Agent is not a place to go back to. */
    val stored: String? get() = when (this) {
        Persona -> PERSONA
        NewAgent -> null
        is Chat -> SESSION + key.path
    }

    companion object {
        const val PERSONA = "persona"
        const val SESSION = "session:"

        fun parse(stored: String): ForwardTarget? = when {
            stored == PERSONA -> Persona
            stored.startsWith(SESSION) -> stored.removePrefix(SESSION).split('/', limit = 2).takeIf { it.size == 2 && it.all(String::isNotEmpty) }
                ?.let { Chat(SessionKey(it[0], it[1])) }
            else -> null
        }
    }
}

/**
 * The targets forwards went to lately, latest first (Forward to's Recent; iOS `ForwardMemory`): kept on the phone for one
 * hub group, ids only, never synced, gone with its pairing.
 */
interface ForwardMemory {
    suspend fun targets(): List<String>
    suspend fun remember(target: String)

    companion object {
        /** Targets kept; Recent shows [ForwardTargets.RECENT_LIMIT] of them. */
        const val LIMIT = 8
        /** Its key in the group's preferences. */
        const val KEY = "forward.recentTargets"

        /** [list] with [target] first, once, at most [LIMIT]. */
        fun next(list: List<String>, target: String): List<String> = (listOf(target) + list.filter { it != target }).take(LIMIT)

        /** The remembered chats, latest first. */
        fun sessions(list: List<String>): List<SessionKey> = list.mapNotNull { (ForwardTarget.parse(it) as? ForwardTarget.Chat)?.key }
    }
}

/**
 * Forward to's lists (iOS `ForwardTargets`): which chats a forward may go to, the recent ones first, the rest by computer,
 * and what a search finds. Built from the sessions the app already holds; nothing is fetched.
 */
object ForwardTargets {
    /** Recent targets shown at most. */
    const val RECENT_LIMIT = 5

    enum class Kind { Recent, Machine, Results }

    data class Section(val id: String, val kind: Kind, val title: String, val sessions: List<Session>)

    /**
     * Whether [s] can take a forward from the chat [from]: another chat of the same hub group that takes messages; an
     * Agent on an online computer, live or ([ended]) one the send resumes; never an archived chat. Persona's conversations
     * come from its own list, which holds no Health chats.
     */
    fun takes(s: Session, from: SessionKey, ended: Boolean, online: (String) -> Boolean): Boolean {
        if (s.key == from || GroupKeys.namespaceOf(s.machineId) != GroupKeys.namespaceOf(from.machineId)) return false
        if (!s.canSend || s.archived) return false
        if (s.assistant) return true
        return online(s.machineId) && (ended || s.agentState != AgentState.Offline)
    }

    /** What a search looks through for [s]: its title, who answers there, its project and its computer. */
    private fun haystack(s: Session, title: String, machine: String?): String =
        (if (s.assistant) listOf(title, "Persona") else listOfNotNull(title, s.agentLabel, s.spaceName?.takeIf { it.isNotBlank() } ?: s.folderName, machine))
            .joinToString(" ").lowercase()

    private val latest = compareByDescending<Session> { it.updated ?: Instant.EPOCH }

    /**
     * The sections for [query]: with none, the recent targets, then each computer's live Agents (in [machines]' order,
     * latest first); with one, every match, ended Agents and Persona's other conversations included. [main] has its own row.
     * [agents] are the Agents list's sessions, [conversations] Persona's.
     */
    fun sections(agents: List<Session>, conversations: List<Session>, from: SessionKey, recents: List<SessionKey>, main: SessionKey?,
        machines: List<Machine>, query: String, online: (String) -> Boolean, title: (Session) -> String): List<Section> {
        val name: (String) -> String = { id -> machines.firstOrNull { it.id == id }?.displayName ?: id }
        val seen = HashSet<SessionKey>()
        val all = (agents.filterNot { it.assistant } + conversations.map { it.copy(assistant = true) }).filter { seen.add(it.key) }
        val terms = query.lowercase().split(Regex("\\s+")).filter { it.isNotEmpty() }
        if (terms.isNotEmpty()) {
            val found = all.filter { s ->
                if (s.key == main || !takes(s, from, ended = true, online)) return@filter false
                val hay = haystack(s, title(s), if (s.assistant) null else name(s.machineId))
                terms.all { hay.contains(it) }
            }
            return if (found.isEmpty()) emptyList() else listOf(Section("results", Kind.Results, "", found.sortedWith(latest)))
        }
        val out = mutableListOf<Section>()
        val byKey = all.associateBy { it.key }
        val recent = recents.mapNotNull { byKey[it] }.filter { it.key != main && takes(it, from, ended = false, online) }.take(RECENT_LIMIT)
        if (recent.isNotEmpty()) out += Section("recent", Kind.Recent, "", recent)
        val shown = recent.map { it.key }.toSet()
        val rest = all.filter { !it.assistant && it.key !in shown && takes(it, from, ended = false, online) }
        for (m in machines) {
            val rows = rest.filter { it.machineId == m.id }.sortedWith(latest)
            if (rows.isNotEmpty()) out += Section("machine:${m.id}", Kind.Machine, m.displayName, rows)
        }
        return out
    }
}

/**
 * What the app shell gives chats for Forward (#3901): the sessions the app already holds, Persona's conversations, each
 * group's Recent, and how a target opens. Null where messages are not forwarded (previews, tests without it).
 */
class ChatForwarding(
    /** The Agents list's sessions, every group's (machine ids with their suffix). */
    val agents: StateFlow<List<Session>>,
    val machines: StateFlow<List<Machine>>,
    /** Persona's conversations and its main one, of the group Persona's page shows. */
    val assistant: StateFlow<AssistantSnapshot>,
    /** Whether [assistant] is the hub group of [chat] (a forward stays on its hub). */
    val personaGroupOf: (SessionKey) -> Boolean,
    /** A Persona conversation's name as Persona's history shows it. */
    val conversationTitle: (Session) -> String,
    /** Recent of the chat's hub group; null when it has none (demo mode). */
    val memory: (SessionKey) -> ForwardMemory?,
    /** Opens a chat, a Persona conversation ([assistant]) in Persona's page. */
    val openChat: (key: SessionKey, assistant: Boolean) -> Unit,
    /** Persona's new conversation, its composer focused ([ForwardPlacements.home] holds the forward). */
    val openPersonaHome: () -> Unit,
    /** The new-agent form with [context] (a forward block) over its first message. */
    val openNewAgent: (context: String) -> Unit,
)

/** Supplied by the app shell; null: messages offer no Forward. */
val LocalChatForwarding = staticCompositionLocalOf<ChatForwarding?> { null }

/** A forward and the photos read from its source message, waiting for their target's composer. */
data class ForwardPlacement(val ref: ChatLinkContext, val photos: RestoredPhotos = RestoredPhotos())

/**
 * Forwards waiting for their target's composer (iOS `composerContexts`): put here as the target opens, taken by that chat's
 * page once it is on screen (already composed or not), and by Persona's new conversation ([home]). In memory only.
 */
object ForwardPlacements {
    private val pending = mutableStateMapOf<SessionKey, List<ForwardPlacement>>()

    /** The forwards over Persona's new-conversation composer, removed there by ✕ and once sent. */
    private val homeState = mutableStateOf<List<ChatLinkContext>>(emptyList())
    var home: List<ChatLinkContext>
        get() = homeState.value
        set(value) {
            homeState.value.filter { old -> value.none { it.id == old.id } }.forEach { homePhotos.remove(it.id) }
            homeState.value = value
        }
    private val homePhotos = mutableStateMapOf<String, RestoredPhotos>()
    private val newAgentPhotos = mutableStateMapOf<String, List<ForwardPlacement>>()

    /** False when this message already waits for the target; its new photo handoff belongs to the caller to discard. */
    fun place(key: SessionKey, ref: ChatLinkContext, photos: RestoredPhotos = RestoredPhotos()): Boolean {
        val current = pending[key].orEmpty()
        if (withQuote(current.map(ForwardPlacement::ref), ref).size == current.size) return false
        pending[key] = current + ForwardPlacement(ref, photos)
        return true
    }

    fun placeHome(ref: ChatLinkContext, photos: RestoredPhotos = RestoredPhotos()): Boolean {
        if (withQuote(home, ref).size == home.size) return false
        home = home + ref
        homePhotos[ref.id] = photos
        return true
    }

    /** What waits for [key] (read in composition, so the page sees a new one at once). */
    fun waiting(key: SessionKey): List<ForwardPlacement>? = pending[key]

    fun take(key: SessionKey): List<ForwardPlacement> = pending.remove(key).orEmpty()

    /** Photo handoffs for the home composer, retained while their forward is over it. */
    fun homePhotoHandoffs(): Map<String, RestoredPhotos> = homePhotos.toMap()

    /** Records source loss and target-cap rejection once; accepted photos stay available if the composer is replaced. */
    fun finishHomePhotoHandoffs(rejected: Set<String>) {
        homePhotos.replaceAll { _, handoff ->
            handoff.copy(attachments = handoff.attachments.filterNot { it.id in rejected }, lost = 0)
        }
    }

    /** The route carries text; keep each staged photo with the source message that owns it. */
    fun placeNewAgent(context: String, messages: List<ForwardPlacement>): List<ForwardPlacement> =
        newAgentPhotos.put(context, messages.filter { it.photos.attachments.isNotEmpty() || it.photos.lost > 0 }).orEmpty()

    fun takeNewAgent(context: String?): List<ForwardPlacement> = context?.let(newAgentPhotos::remove).orEmpty()
}

/** A pressed message on its way to Forward to: where it is and who wrote it, its text and photos, and its cut item. */
internal data class ForwardDraft(
    val source: ForwardMessage.Source,
    val raw: String,
    /** The item the hub cut for the page (`full_size`): its whole text is read when it goes. */
    val cut: String? = null,
    val photos: List<String> = emptyList(),
)

/**
 * Puts a forward where [target] takes it and opens that chat (iOS `AppModel.forward`): over its composer with the line saying
 * where it comes from and goes; Persona with no main conversation yet, the new conversation's composer; a new Agent, the
 * new-agent form. [from] is the chat it was pressed in.
 */
internal fun deliverForward(target: ForwardTarget, messages: List<ForwardPlacement>, from: SessionKey, forwarding: ChatForwarding,
    resources: Resources, onDuplicate: (RestoredPhotos) -> Unit = {}) {
    if (messages.isEmpty()) return
    if (target == ForwardTarget.NewAgent) {
        val context = messages.joinToString("\n") { it.ref.xml() }
        ForwardPlacements.placeNewAgent(context, messages).forEach { onDuplicate(it.photos) }
        forwarding.openNewAgent(context)
        return
    }
    val key = when (target) {
        is ForwardTarget.Chat -> target.key
        else -> forwarding.assistant.value.mainSession?.takeIf { forwarding.personaGroupOf(from) }
    }
    val session = forwarding.agents.value.firstOrNull { it.key == key }
    val persona = target == ForwardTarget.Persona || session?.assistant == true ||
        forwarding.assistant.value.conversations.any { it.key == key }
    val to = if (persona) null else ForwardMessage.RouteTarget(session?.agentLabel ?: "Claude",
        forwarding.machines.value.firstOrNull { it.id == key?.machineId }?.displayName)
    for ((ref, photos) in messages) {
        val routed = ref.copy(route = ForwardMessage.route(ref.extra["machine"], to, resources))
        val added = if (key == null) ForwardPlacements.placeHome(routed, photos) else ForwardPlacements.place(key, routed, photos)
        if (!added) onDuplicate(photos)
    }
    if (key == null) forwarding.openPersonaHome() else forwarding.openChat(key, persona)
}

/**
 * The pressed message as a forward (iOS `ChatView.forwardDraft`): who wrote it and where, its text as this chat holds it
 * (the whole of it read when it goes, if cut). [session] and [machine] are the chat's, as its page has them.
 */
internal fun forwardDraftOf(target: MessageTarget, key: SessionKey, session: Session?, machine: Machine?, style: ChatRenderStyle,
    forwarding: ChatForwarding, maxPhotos: Int = persona.core.contracts.ImageLimits.DEFAULT_PER_MESSAGE): ForwardDraft {
    val persona = style == ChatRenderStyle.Assistant || session?.assistant == true
    val role = when {
        target.from != null -> ForwardMessage.Role.Other
        target.user -> ForwardMessage.Role.User
        persona -> ForwardMessage.Role.Persona
        else -> ForwardMessage.Role.Agent
    }
    // On the wire Persona's messages are by "Persona", as iOS and the web write them.
    val author = target.from ?: if (persona) "Persona" else session?.agentLabel ?: "Claude"
    val chat = session?.let { if (persona) forwarding.conversationTitle(it) else it.displayTitle }.orEmpty()
    val source = ForwardMessage.Source(key, target.id, role, author, chat,
        project = if (persona) null else session?.forwardProject, machine = if (persona) null else machine?.displayName,
        time = parseTime(target.time))
    return ForwardDraft(source, target.raw, target.cut,
        photos = if (role == ForwardMessage.Role.User) messageModel(target.raw, user = true).images.take(maxPhotos) else emptyList())
}
