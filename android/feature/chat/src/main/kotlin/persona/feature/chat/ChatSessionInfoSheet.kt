package persona.feature.chat

import androidx.compose.ui.platform.LocalResources
import android.content.res.Resources
import androidx.compose.foundation.layout.*
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import persona.core.contracts.DelegatedAnswer
import persona.core.contracts.Machine
import persona.core.contracts.Session
import persona.core.contracts.SessionInfo
import persona.core.contracts.parseTime
import persona.core.designsystem.*
import persona.core.domain.sort.isBotmux
import persona.core.designsystem.icons.PersonaIcons
import persona.core.designsystem.icons.PhIcons
import persona.core.domain.answer.Delegated
import persona.core.domain.lines.tokenCount
import persona.core.domain.sort.canSend
import persona.core.domain.sort.listedModes
import persona.core.domain.sort.displayTitle
import persona.core.domain.sort.displayTitleDetail
import persona.core.domain.sort.resumeCommand
import java.time.Instant
import java.time.ZoneId
import java.util.Locale
import kotlin.math.roundToLong
import kotlinx.coroutines.CancellationException

/**
 * A token count in the language of [resources], as the compaction divider counts them ([tokenCount]): 950, 48.2K and
 * 1.2M in English, 4.8 万 in Chinese, 4.8万 in Japanese.
 */
internal fun formatTokens(count: Long, resources: Resources): String = tokenCount(count).resolve(resources)

/**
 * One fact of "Session info"; a null value reads as —. [copy] names what tapping it copies. [note]: a line under it (why a
 * Codex is reached through its terminal); [usage]: the share of the model's window left, as a bar under it.
 */
internal data class SessionInfoRow(val label: String, val value: String?, val copy: String? = null, val onClick: (() -> Unit)? = null,
    val note: String? = null, val usage: ContextUsage? = null)
internal data class SessionInfoSection(val title: String, val rows: List<SessionInfoRow>)

/**
 * How much of the model's window the agent's latest reply used (iOS `ContextUsage`, the web's `lib/context-usage.ts`): the
 * node reads [tokens] and [window] off the transcript, [used] is the whole percent of it.
 */
internal data class ContextUsage(val used: Int, val tokens: Long, val window: Long) {
    val left: Int get() = 100 - used
    /** From [WARN_USED] percent used the bar and its words take the caution colour (the web's `CONTEXT_WARN_USED`). */
    val warns: Boolean get() = used >= WARN_USED
    /** What is left, 0 to 1, for the bar (like the web's header). */
    val fractionLeft: Float get() = left / 100f
    /** "172K / 200K", for TalkBack. */
    val detail: String get() = short(tokens) + " / " + short(window)

    companion object {
        const val WARN_USED = 80

        /** Null while either is unknown or zero: nothing is drawn then. */
        fun of(tokens: Long?, window: Long?): ContextUsage? {
            if (tokens == null || window == null || tokens <= 0 || window <= 0) return null
            return ContextUsage((tokens.toDouble() / window * 100).roundToLong().toInt().coerceIn(0, 100), tokens, window)
        }

        fun of(session: Session): ContextUsage? = of(session.contextTokens, session.contextWindow)

        /**
         * 172000 → "172K", 1000000 → "1M", 1260000 → "1.3M". A unit ends where its number would round to 1000 (999 600 is
         * "1M"), and the M figure is rounded to one decimal before asking whether it is whole (1 960 000 is "2M").
         */
        fun short(n: Long): String {
            if (n >= 999_500) {
                val m = (n / 100_000.0).roundToLong() / 10.0
                return (if (m == Math.rint(m)) m.toLong().toString() else String.format(Locale.ROOT, "%.1f", m)) + "M"
            }
            if (n >= 1000) return (n / 1000.0).roundToLong().toString() + "K"
            return n.toString()
        }
    }
}

/**
 * What an assistant conversation runs on, for How it runs (iOS `AssistantRuntime.status`): "Fell back to CLI: <why>" when the
 * node could not run the Agent SDK, "Agent SDK" while it does; null on the CLI as set (nothing to say).
 */
internal fun runtimeStatus(res: Resources, session: Session): String? {
    session.runtimeFallback?.trim()?.takeIf(String::isNotEmpty)?.let { return res.getString(R.string.chat_info_runtime_fallback, it) }
    return if (session.runtime == AGENT_SDK) "Agent SDK" else null
}

private const val AGENT_SDK = "agentsdk"

/**
 * "Session info" (iOS SessionInfoSheet), from GET …/info: what it runs (How it runs while an assistant conversation is on the
 * Agent SDK or fell back from it, and how Persona reaches a Codex), how far it has got (the share of the model's window
 * left from the session list), where its files are, and what to copy. The resume command only for a session in a terminal
 * (an agent runtime's is picked up in its own UI); a Codex thread resumes by its own thread id.
 */
internal fun sessionInfoSections(
    res: Resources,
    session: Session,
    info: SessionInfo,
    zone: ZoneId = ZoneId.systemDefault(),
    onOpenTodo: ((String) -> Unit)? = null,
    features: Set<String> = emptySet(),
): List<SessionInfoSection> {
    fun String?.orSession(fallback: String?) = this?.takeIf(String::isNotBlank) ?: fallback?.takeIf(String::isNotBlank)
    val cwd = info.cwd.orSession(session.cwd)
    val id = info.sessionId.ifBlank { session.sessionId }
    val locale = res.configuration.locales[0]
    val directory = res.getString(R.string.chat_info_directory)
    val branch = res.getString(R.string.chat_info_branch)
    val resume = res.getString(R.string.chat_info_resume_command)
    val mode = info.permissionMode.orSession(session.permissionMode)
    return buildList {
        session.todo?.let { todo ->
            add(SessionInfoSection(res.getString(R.string.chat_info_todo_section), listOf(
                SessionInfoRow(res.getString(R.string.chat_info_todo), todo.title, copy = todo.id, onClick = onOpenTodo?.let { { it(todo.id) } })
            )))
        }
        add(SessionInfoSection(res.getString(R.string.chat_info_run_section), listOfNotNull(
            SessionInfoRow(res.getString(R.string.chat_info_model), info.model.orSession(session.model)),
            SessionInfoRow(res.getString(R.string.chat_info_launcher), info.launcher.orSession(session.launcher)),
            runtimeStatus(res, session)?.let { SessionInfoRow(res.getString(R.string.chat_info_runtime), it) },
            SessionInfoRow(if (session.isClaude) "Claude Code" else session.agentLabel, info.version.orSession(null)),
            // How Persona reaches this Codex (docs/integrations/codex.md, "Sessions on the daemon"): its app-server, or the terminal and why.
            session.codexRoute?.takeIf(String::isNotEmpty)?.let { route ->
                val appServer = route == "app_server"
                SessionInfoRow(res.getString(R.string.chat_info_route),
                    res.getString(if (appServer) R.string.chat_info_route_app_server else R.string.chat_info_route_terminal),
                    note = session.codexRouteNote?.takeIf { !appServer && it.isNotBlank() })
            },
            // A session that lists its own modes (#8261) runs in one of them: no mode reported is its own approval.
            SessionInfoRow(res.getString(R.string.chat_info_permission), session.listedModes?.let { listed ->
                SessionModes.listedOption(mode?.takeIf { it in listed } ?: "default").title(res)
            } ?: SessionModes.modeLabel(mode, res)),
            SessionInfoRow(res.getString(R.string.chat_info_effort), SessionModes.effortLabel(info.effort.orSession(session.effort), res)),
            // #582: on / off / unknown, as the node last saw it
            SessionInfoRow(Ultracode.NAME, Ultracode.infoWord(session, res)).takeIf { Ultracode.infoShown(session, features) },
        )))
        // Counts nobody gives (#8261, `usage_unknown`: a runtime's session with no transcript here) are left out, not zeros.
        val counted = !info.usageUnknown
        add(SessionInfoSection(res.getString(R.string.chat_info_usage_section), buildList {
            if (counted) add(SessionInfoRow(res.getString(R.string.chat_info_context), res.getString(R.string.chat_info_tokens, formatTokens(info.contextTokens, res))))
            // The share of the window, live from the session list; none while the window is unknown.
            ContextUsage.of(session)?.let { usage ->
                add(SessionInfoRow(res.getString(R.string.chat_info_context_left), res.getString(R.string.chat_info_context_left_value, usage.left),
                    usage = usage))
            }
            if (counted && info.compactions > 0) add(SessionInfoRow(res.getString(R.string.chat_info_compactions),
                res.getQuantityString(R.plurals.chat_info_compaction_count, info.compactions, info.compactions)))
            if (counted) {
                add(SessionInfoRow(res.getString(R.string.chat_info_turns), res.getQuantityString(R.plurals.chat_info_turn_count, info.turns, info.turns)))
                add(SessionInfoRow(res.getString(R.string.chat_info_output), res.getString(R.string.chat_info_tokens, formatTokens(info.outputTokens, res))))
            }
            parseTime(info.startedAt)?.let {
                add(SessionInfoRow(res.getString(R.string.chat_info_started),
                    LocalizedFormats.format(it, LocalizedFormats.MONTH_DAY_TIME, locale, zone)))
            }
        }))
        add(SessionInfoSection(res.getString(R.string.chat_info_place_section), listOfNotNull(
            SessionInfoRow(directory, cwd, copy = cwd?.let { directory }),
            info.gitBranch.orSession(null)?.let { SessionInfoRow(branch, it, copy = branch) },
        )))
        add(SessionInfoSection(res.getString(R.string.chat_info_copy_section), listOfNotNull(
            SessionInfoRow("session id", id, copy = "session id"),
            session.resumeCommand(cwd = cwd.orEmpty())?.let { SessionInfoRow(resume, it, copy = resume) },
        )))
    }
}

@Composable
internal fun ChatSessionInfoSheet(
    session: Session,
    machine: Machine?,
    state: ChatChromeState,
    onOpenTodo: ((String) -> Unit)? = null,
    preferHerdrTabName: Boolean = false,
    onDismiss: () -> Unit,
) {
    val copyText = rememberCopy()
    val res = LocalResources.current
    val uriHandler = LocalUriHandler.current
    var info by remember(session.key) { mutableStateOf<SessionInfo?>(null) }
    var error by remember(session.key) { mutableStateOf<String?>(null) }
    var attempt by remember(session.key) { mutableIntStateOf(0) }
    LaunchedEffect(session.key, attempt) {
        error = null
        try { info = state.info(session) }
        catch (e: CancellationException) { throw e }
        catch (e: Exception) {
            error = res.getString(R.string.chat_info_unreadable, (e as? UnsupportedOperationException)?.message ?: operationFeedback(e).text.resolve(res))
        }
    }
    JSheet(onDismiss = onDismiss, title = stringResource(R.string.chat_info_title), scrollable = true) {
        Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
            Text(session.displayTitle(preferHerdrTabName), style = PersonaTheme.type.headline, color = PersonaTheme.palette.ink, maxLines = 2,
                overflow = TextOverflow.Ellipsis)
            session.displayTitleDetail(preferHerdrTabName)?.let { cliTitle ->
                Text(cliTitle, style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2, maxLines = 1,
                    overflow = TextOverflow.Ellipsis)
            }
            val status = Status.entries.firstOrNull { it.name == session.agentState.name }
            Text(listOfNotNull(status?.localizedLabel, machine?.displayName ?: session.machineId, session.spaceName?.takeIf(String::isNotBlank))
                .joinToString(" / "), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2, maxLines = 1,
                overflow = TextOverflow.Ellipsis)
        }
        // Open in Lark (iOS `FeishuLink`): handed to the system, which gives Feishu its own links.
        session.externalLink?.let { url ->
            JGroupItem(GroupPosition.of(0, 1), Modifier.padding(top = Tokens.Space.m)) {
                JRow(stringResource(R.string.chat_host_open_feishu), leadingIcon = PersonaIcons.openExternal, chevron = false,
                    onClick = { val _ = runCatching { uriHandler.openUri(url) } })
            }
        }
        // A BotMux session reads as in Lark, or as its raw transcript (BotmuxMessages.kt).
        if (session.isBotmux) BotmuxModeRow(Modifier.padding(top = Tokens.Space.m))
        // The questions the assistant answered for this agent; its own conversations have none.
        if (!session.assistant) DelegatedHistorySection(session, state)
        val loaded = info
        when {
            loaded != null -> sessionInfoSections(res, session, loaded, onOpenTodo = onOpenTodo,
                features = persona.core.designsystem.LocalHubFeatures.current).filter { it.rows.isNotEmpty() }.forEach { section ->
                JSectionHeader(section.title)
                section.rows.forEachIndexed { index, row ->
                    JGroupItem(GroupPosition.of(index, section.rows.size)) {
                        val value = row.value ?: "—"
                        val copy = row.copy?.takeIf { row.value != null }
                        if (row.usage != null) ContextUsageRow(row.label, value, row.usage)
                        else if (row.onClick != null) JRow(row.label, subtitle = value, subtitleLines = 4,
                            chevron = true, onClick = { onDismiss(); row.onClick.invoke() })
                        else if (copy != null) JRow(row.label, subtitle = value, subtitleLines = 4,
                            trailing = { JIcon(PhIcons.Copy, stringResource(R.string.chat_info_copy_item, copy), tint = PersonaTheme.palette.ink2) }, onClick = {
                                copyText(value, res.getString(R.string.chat_info_copied_item, copy))
                            })
                        else JRow(row.label, value = value)
                        row.note?.let {
                            Text(it, Modifier.padding(start = rowPadding, end = rowPadding, bottom = Tokens.Space.m),
                                style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
                        }
                    }
                }
            }
            error != null -> JNotice(error.orEmpty(), Modifier.padding(top = Tokens.Space.m), status = Status.NeedsYou,
                icon = PhIcons.WarningCircle, actionLabel = stringResource(R.string.chat_info_retry), onAction = { attempt++ })
            else -> JLoading()
        }
    }
}

/**
 * Session info's context row (iOS `ContextUsageRow`), under the tokens: the share left in words and a thin bar, both in the
 * caution colour from [ContextUsage.WARN_USED] percent used; read out as one line with the share used and the window.
 */
@Composable
internal fun ContextUsageRow(label: String, value: String, usage: ContextUsage) {
    val palette = PersonaTheme.palette
    val spoken = stringResource(R.string.chat_info_context_spoken, value, usage.used, usage.detail)
    Column(Modifier.clearAndSetSemantics { contentDescription = spoken }) {
        JRow(label, trailing = {
            Text(value, style = PersonaTheme.type.callout.copy(fontWeight = FontWeight.Medium),
                color = if (usage.warns) palette.cautionText else palette.ink2, maxLines = 1)
        })
        JLinearProgress(usage.fractionLeft, Modifier.padding(start = rowPadding, end = rowPadding, bottom = Tokens.Space.m),
            color = if (usage.warns) palette.caution else null)
    }
}

/**
 * Session info's questions the assistant answered (iOS `DelegatedHistorySection`, GET /api/v1/interactions/history): each
 * with when, every question and its answer as it is now, why, and Change answer while the session takes messages (the
 * new answer reaches the Agent as a message). Nothing while it loads or when there are none; a failure says so with Retry.
 */
@Composable
internal fun DelegatedHistorySection(session: Session, state: ChatChromeState) {
    val res = LocalResources.current
    var answers by remember(session.key) { mutableStateOf<List<DelegatedAnswer>?>(null) }
    var failure by remember(session.key) { mutableStateOf<String?>(null) }
    var attempt by remember(session.key) { mutableIntStateOf(0) }
    LaunchedEffect(session.key, attempt) {
        failure = null
        try { answers = state.delegatedAnswers(session) }
        catch (e: CancellationException) { throw e }
        catch (e: Exception) { failure = operationFeedback(e).text.resolve(res) }
    }
    val shown = answers.orEmpty()
    val title = stringResource(R.string.chat_info_delegated_section)
    failure?.let { text ->
        JSectionHeader(title)
        JNotice(text, status = Status.NeedsYou, icon = PersonaIcons.error,
            actionLabel = stringResource(R.string.chat_info_retry), onAction = { attempt++ })
        return
    }
    if (shown.isEmpty()) return
    val center = state.interactions
    val changes = center?.reanswered?.collectAsStateWithLifecycle()?.value.orEmpty()
    var changing by remember(session.key) { mutableStateOf<DelegatedAnswer?>(null) }
    JSectionHeader(title)
    val now = Instant.now()
    shown.forEachIndexed { index, answer ->
        JGroupItem(GroupPosition.of(index, shown.size)) {
            Column(Modifier.fillMaxWidth().padding(horizontal = rowPadding, vertical = Tokens.Space.m),
                verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                answer.at?.let { Text(relativeTime(it, now, res), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2) }
                DelegatedBody(answer, changes[answer.id], onChange = if (center != null && session.canSend) ({ changing = answer }) else null)
            }
        }
    }
    changing?.let { answer ->
        if (center != null) ReanswerSheet(answer, Delegated.latest(answer, changes[answer.id]), onDismiss = { changing = null }) { answers ->
            // A history row may not name its manager: the session's then, as iOS's history fills it in.
            center.reanswer(answer.id, answers, answer.interaction.manager ?: session.manager)
        }
    }
}
