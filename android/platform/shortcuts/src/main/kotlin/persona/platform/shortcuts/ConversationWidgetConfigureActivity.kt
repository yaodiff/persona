package persona.platform.shortcuts

import android.appwidget.AppWidgetManager
import android.content.Context
import android.content.Intent
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.safeDrawingPadding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.lifecycle.lifecycleScope
import kotlinx.coroutines.launch
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.AssistantSnapshot
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.parseTime
import persona.core.designsystem.AppStyleStore
import persona.core.designsystem.JEmptyState
import persona.core.designsystem.JNotice
import persona.core.designsystem.JSectionHeader
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import java.time.Instant

/** Selectable target entry displayed in [ConversationWidgetConfigureActivity]. */
internal data class ConversationWidgetOption(
    val kind: ConversationWidgetKind,
    val sessionKey: SessionKey?,
    val title: String,
    val subtitle: String? = null,
    val selected: Boolean = false,
) {
    fun toBinding(pairing: String): ConversationWidgetBinding = when {
        kind == ConversationWidgetKind.Main || sessionKey == null -> ConversationWidgetBinding.main(pairing)
        else -> ConversationWidgetBinding(
            pairing = pairing,
            machineId = sessionKey.machineId,
            sessionId = sessionKey.sessionId,
            titleSnapshot = title,
            kind = kind,
        )
    }
}

/** Grouped conversation picker sections: Main chat (pinned) -> Standing directions -> Other conversations. */
internal data class ConversationWidgetPickerSections(
    val main: ConversationWidgetOption?,
    val leaders: List<ConversationWidgetOption>,
    val others: List<ConversationWidgetOption>,
) {
    val isEmpty: Boolean get() = main == null && leaders.isEmpty() && others.isEmpty()
}

/** Pure projection & search filter for [ConversationWidgetConfigureActivity]. */
internal object ConversationWidgetPickerModel {
    fun build(
        assistant: AssistantSnapshot,
        sessions: List<Session>,
        currentBinding: ConversationWidgetBinding?,
        mainChatTitle: String,
        mainChatSubtitle: String,
        directionFallbackTitle: String,
        query: String = "",
    ): ConversationWidgetPickerSections {
        val trimmedQuery = query.trim()
        val liveByKey = sessions.associateBy { it.key }
        val mainKey = assistant.mainSession
        val currentIsMain = currentBinding == null || currentBinding.isMain

        val mainOption = ConversationWidgetOption(
            kind = ConversationWidgetKind.Main,
            sessionKey = mainKey,
            title = mainChatTitle,
            subtitle = mainChatSubtitle,
            selected = currentIsMain,
        )

        val seenKeys = LinkedHashSet<SessionKey>()
        mainKey?.let { val _ = seenKeys.add(it) }

        val leaderOptions = mutableListOf<ConversationWidgetOption>()
        val treeLeaders = assistant.directionTree?.leaders.orEmpty()
        val leaderIdsFromTree = LinkedHashSet<String>()
        for (dir in treeLeaders) {
            val _ = leaderIdsFromTree.add(dir.id)
            val extTarget = dir.external?.target
            val matchedSession = extTarget?.let { liveByKey[it] }
                ?: assistant.conversations.firstOrNull { it.sessionId == dir.id }
                ?: sessions.firstOrNull { it.sessionId == dir.id }
            if (matchedSession?.archived == true) continue
            val key = extTarget ?: matchedSession?.key
                ?: mainKey?.let { SessionKey(it.machineId, dir.id) }
                ?: continue
            if (!seenKeys.add(key)) continue
            val title = dir.name.takeIf(String::isNotBlank)
                ?: assistant.directionNames[dir.id]?.takeIf(String::isNotBlank)
                ?: matchedSession?.displayTitle?.takeIf(String::isNotBlank)
                ?: directionFallbackTitle
            val subtitle = MainChatText.sanitize(matchedSession?.lastText, maxChars = SUBTITLE_MAX_CHARS)
                .takeIf(String::isNotBlank)
            leaderOptions += ConversationWidgetOption(
                kind = ConversationWidgetKind.Leader,
                sessionKey = key,
                title = title,
                subtitle = subtitle,
                selected = !currentIsMain && currentBinding?.sessionKey == key,
            )
        }
        for (leaderId in assistant.leaders) {
            if (leaderId in leaderIdsFromTree) continue
            val matchedSession = assistant.conversations.firstOrNull { it.sessionId == leaderId }
                ?: sessions.firstOrNull { it.sessionId == leaderId }
            if (matchedSession?.archived == true) continue
            val extTarget = assistant.externalDirection(leaderId)?.target
            val key = extTarget ?: matchedSession?.key
                ?: mainKey?.let { SessionKey(it.machineId, leaderId) }
                ?: continue
            if (!seenKeys.add(key)) continue
            val title = assistant.directionNames[leaderId]?.takeIf(String::isNotBlank)
                ?: matchedSession?.displayTitle?.takeIf(String::isNotBlank)
                ?: directionFallbackTitle
            val subtitle = MainChatText.sanitize(matchedSession?.lastText, maxChars = SUBTITLE_MAX_CHARS)
                .takeIf(String::isNotBlank)
            leaderOptions += ConversationWidgetOption(
                kind = ConversationWidgetKind.Leader,
                sessionKey = key,
                title = title,
                subtitle = subtitle,
                selected = !currentIsMain && currentBinding?.sessionKey == key,
            )
        }

        val otherCandidates = LinkedHashMap<SessionKey, Session>()
        for (conv in assistant.conversations) {
            if (conv.archived || conv.isSpace || conv.key in seenKeys) continue
            otherCandidates[conv.key] = liveByKey[conv.key] ?: conv
        }
        for (session in sessions) {
            if (session.archived || session.isSpace || session.key in seenKeys) continue
            if (session.sessionId.contains("@")) continue
            if (session.key !in otherCandidates) {
                otherCandidates[session.key] = session
            }
        }
        val otherOptions = otherCandidates.values
            .sortedWith(
                compareByDescending<Session> { it.assistant }
                    .thenByDescending { parseTime(it.updatedAt ?: it.activityAt ?: it.stateSince) ?: Instant.MIN }
                    .thenBy { it.displayTitle.lowercase() },
            )
            .map { session ->
                val kind = if (session.assistant) ConversationWidgetKind.Assistant else ConversationWidgetKind.Agent
                val title = session.displayTitle.takeIf(String::isNotBlank) ?: directionFallbackTitle
                val subtitle = MainChatText.sanitize(session.lastText, maxChars = SUBTITLE_MAX_CHARS)
                    .takeIf(String::isNotBlank)
                ConversationWidgetOption(
                    kind = kind,
                    sessionKey = session.key,
                    title = title,
                    subtitle = subtitle,
                    selected = !currentIsMain && currentBinding?.sessionKey == session.key,
                )
            }

        if (trimmedQuery.isEmpty()) {
            return ConversationWidgetPickerSections(
                main = mainOption,
                leaders = leaderOptions,
                others = otherOptions,
            )
        }
        return ConversationWidgetPickerSections(
            main = mainOption.takeIf { matches(it, trimmedQuery) },
            leaders = leaderOptions.filter { matches(it, trimmedQuery) },
            others = otherOptions.filter { matches(it, trimmedQuery) },
        )
    }

    private fun matches(option: ConversationWidgetOption, query: String): Boolean =
        option.title.contains(query, ignoreCase = true) ||
            (option.subtitle?.contains(query, ignoreCase = true) == true)

    private const val SUBTITLE_MAX_CHARS = 72
}

/**
 * Configuration & reconfiguration Activity for [MainChatWidget] and [AskPersonaWidget].
 * Lets the user bind each `appWidgetId` to Main Chat, a standing direction (`leader`), or any other
 * conversation, with live search and graceful offline fallback.
 */
class ConversationWidgetConfigureActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        setResult(RESULT_CANCELED)

        val appWidgetId = intent?.getIntExtra(
            AppWidgetManager.EXTRA_APPWIDGET_ID,
            AppWidgetManager.INVALID_APPWIDGET_ID,
        ) ?: AppWidgetManager.INVALID_APPWIDGET_ID
        if (appWidgetId == AppWidgetManager.INVALID_APPWIDGET_ID) {
            finish()
            return
        }

        val dependencies = SurfacesDependencies.of(this)
        val feed = dependencies.fleet()
        val assistant = dependencies.assistant()
        val sessions = dependencies.sessions()
        val appearance = AppStyleStore(this)

        lifecycleScope.launch { feed.settled() }

        setContent {
            val style by appearance.style.collectAsStateWithLifecycle()
            val color by appearance.color.collectAsStateWithLifecycle()
            val scheme by appearance.scheme.collectAsStateWithLifecycle()
            val paired by feed.paired.collectAsStateWithLifecycle()
            val connectionState by feed.connectionState.collectAsStateWithLifecycle()
            val assistantSnapshot by assistant.assistant.collectAsStateWithLifecycle()
            val sessionList by sessions.sessions.collectAsStateWithLifecycle()
            val activePairing = if (paired) feed.activePairing()?.takeIf(String::isNotBlank) else null
            val currentBinding = remember(activePairing, appWidgetId) {
                ConversationWidgetPrefs.read(this, appWidgetId, activePairing)
            }
            var query by remember { mutableStateOf("") }

            PersonaTheme(darkTheme = scheme.dark ?: persona.core.designsystem.appDarkTheme(), style = style) {
                ConversationWidgetConfigureScreen(
                    paired = paired,
                    offline = paired && connectionState != ConnectionState.Online,
                    assistant = assistantSnapshot,
                    sessions = sessionList,
                    currentBinding = currentBinding,
                    query = query,
                    onQueryChange = { query = it },
                    onCancel = {
                        setResult(RESULT_CANCELED)
                        finish()
                    },
                    onSelect = { option ->
                        val binding = option.toBinding(activePairing.orEmpty())
                        ConversationWidgetPrefs.save(this@ConversationWidgetConfigureActivity, appWidgetId, binding)
                        lifecycleScope.launch {
                            MainChatWidget.refresh(this@ConversationWidgetConfigureActivity)
                            AskPersonaWidget.refresh(this@ConversationWidgetConfigureActivity)
                        }
                        setResult(
                            RESULT_OK,
                            Intent().putExtra(AppWidgetManager.EXTRA_APPWIDGET_ID, appWidgetId),
                        )
                        finish()
                    },
                )
            }
        }
    }

    companion object {
        /** Direct reconfiguration intent used when tapping a widget whose bound conversation was archived/deleted. */
        fun reconfigureIntent(context: Context, appWidgetId: Int): Intent =
            Intent(context, ConversationWidgetConfigureActivity::class.java).apply {
                action = AppWidgetManager.ACTION_APPWIDGET_CONFIGURE
                putExtra(AppWidgetManager.EXTRA_APPWIDGET_ID, appWidgetId)
                addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TOP)
            }
    }
}

@Composable
internal fun ConversationWidgetConfigureScreen(
    paired: Boolean,
    offline: Boolean,
    assistant: AssistantSnapshot,
    sessions: List<Session>,
    currentBinding: ConversationWidgetBinding?,
    query: String,
    onQueryChange: (String) -> Unit,
    onCancel: () -> Unit,
    onSelect: (ConversationWidgetOption) -> Unit,
) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    val mainChatTitle = stringResource(R.string.shortcuts_widget_configure_main_chat)
    val mainChatSubtitle = stringResource(R.string.shortcuts_widget_configure_main_chat_subtitle)
    val directionFallback = stringResource(R.string.shortcuts_widget_configure_untitled)
    val sections = remember(assistant, sessions, currentBinding, query, mainChatTitle, mainChatSubtitle, directionFallback) {
        ConversationWidgetPickerModel.build(
            assistant = assistant,
            sessions = sessions,
            currentBinding = currentBinding,
            mainChatTitle = mainChatTitle,
            mainChatSubtitle = mainChatSubtitle,
            directionFallbackTitle = directionFallback,
            query = query,
        )
    }

    Column(
        modifier = Modifier
            .fillMaxSize()
            .background(palette.surfaceOpaque)
            .safeDrawingPadding()
            .padding(horizontal = Tokens.Space.Standard.l, vertical = Tokens.Space.Standard.m),
    ) {
        Row(
            modifier = Modifier.fillMaxWidth(),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.SpaceBetween,
        ) {
            Text(
                text = stringResource(R.string.shortcuts_widget_configure_title),
                style = type.headline.copy(fontWeight = FontWeight.SemiBold),
                color = palette.ink,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis,
                modifier = Modifier.weight(1f),
            )
            Spacer(Modifier.width(Tokens.Space.Standard.s))
            Box(
                modifier = Modifier
                    .clip(RoundedCornerShape(Tokens.Radius.Standard.control))
                    .clickable(onClick = onCancel)
                    .padding(horizontal = Tokens.Space.Standard.s, vertical = Tokens.Space.Standard.xs),
            ) {
                Text(
                    text = stringResource(R.string.shortcuts_cancel),
                    style = type.callout,
                    color = palette.accent,
                )
            }
        }

        Spacer(Modifier.height(Tokens.Space.Standard.m))

        SearchInputBox(
            query = query,
            onQueryChange = onQueryChange,
            placeholder = stringResource(R.string.shortcuts_widget_configure_search_hint),
        )

        if (!paired) {
            Spacer(Modifier.height(Tokens.Space.Standard.s))
            JNotice(text = stringResource(R.string.shortcuts_widget_configure_unpaired_notice))
        } else if (offline) {
            Spacer(Modifier.height(Tokens.Space.Standard.s))
            JNotice(text = stringResource(R.string.shortcuts_widget_configure_offline_notice))
        }

        Spacer(Modifier.height(Tokens.Space.Standard.s))

        if (sections.isEmpty) {
            Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
                JEmptyState(
                    title = stringResource(R.string.shortcuts_widget_configure_no_matches),
                    description = stringResource(R.string.shortcuts_widget_configure_no_matches_hint),
                )
            }
        } else {
            LazyColumn(
                modifier = Modifier.fillMaxSize(),
                contentPadding = PaddingValues(bottom = Tokens.Space.Standard.xl),
                verticalArrangement = Arrangement.spacedBy(Tokens.Space.Standard.xs),
            ) {
                sections.main?.let { mainOption ->
                    item(key = "section_main") {
                        JSectionHeader(title = stringResource(R.string.shortcuts_widget_configure_section_main))
                    }
                    item(key = "opt_main") {
                        PickerOptionRow(option = mainOption, onClick = { onSelect(mainOption) })
                    }
                }
                if (sections.leaders.isNotEmpty()) {
                    item(key = "section_leaders") {
                        Spacer(Modifier.height(Tokens.Space.Standard.xs))
                        JSectionHeader(title = stringResource(R.string.shortcuts_widget_configure_section_directions))
                    }
                    items(
                        items = sections.leaders,
                        key = { "leader_${it.sessionKey?.machineId}_${it.sessionKey?.sessionId}" },
                    ) { option ->
                        PickerOptionRow(option = option, onClick = { onSelect(option) })
                    }
                }
                if (sections.others.isNotEmpty()) {
                    item(key = "section_others") {
                        Spacer(Modifier.height(Tokens.Space.Standard.xs))
                        JSectionHeader(title = stringResource(R.string.shortcuts_widget_configure_section_others))
                    }
                    items(
                        items = sections.others,
                        key = { "other_${it.sessionKey?.machineId}_${it.sessionKey?.sessionId}" },
                    ) { option ->
                        PickerOptionRow(option = option, onClick = { onSelect(option) })
                    }
                }
            }
        }
    }
}

@Composable
private fun SearchInputBox(
    query: String,
    onQueryChange: (String) -> Unit,
    placeholder: String,
) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    Box(
        modifier = Modifier
            .fillMaxWidth()
            .heightIn(min = Tokens.MinTouch.android)
            .clip(RoundedCornerShape(Tokens.Radius.Standard.control))
            .background(palette.sunken)
            .padding(horizontal = Tokens.Space.Standard.m, vertical = Tokens.Space.Standard.s),
        contentAlignment = Alignment.CenterStart,
    ) {
        if (query.isEmpty()) {
            Text(
                text = placeholder,
                style = type.callout,
                color = palette.ink2,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis,
            )
        }
        BasicTextField(
            value = query,
            onValueChange = onQueryChange,
            singleLine = true,
            textStyle = type.callout.copy(color = palette.ink),
            cursorBrush = SolidColor(palette.accent),
            modifier = Modifier.fillMaxWidth(),
        )
    }
}

@Composable
private fun PickerOptionRow(
    option: ConversationWidgetOption,
    onClick: () -> Unit,
) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    val shape = RoundedCornerShape(Tokens.Radius.Standard.control)
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .heightIn(min = Tokens.MinTouch.android)
            .clip(shape)
            .background(if (option.selected) palette.accentSoft else palette.sunken)
            .then(
                if (option.selected) Modifier.border(Tokens.Size.hairline, palette.accent, shape)
                else Modifier,
            )
            .clickable(onClick = onClick)
            .padding(horizontal = Tokens.Space.Standard.m, vertical = Tokens.Space.Standard.sm),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Box(
            modifier = Modifier
                .size(Tokens.Size.dotS)
                .clip(CircleShape)
                .background(
                    when (option.kind) {
                        ConversationWidgetKind.Main -> palette.accent
                        ConversationWidgetKind.Leader -> palette.working.dot
                        ConversationWidgetKind.Assistant -> palette.done.dot
                        ConversationWidgetKind.Agent -> palette.idle.dot
                    },
                ),
        )
        Spacer(Modifier.width(Tokens.Space.Standard.s))
        Column(modifier = Modifier.weight(1f)) {
            Text(
                text = option.title,
                style = type.callout.copy(fontWeight = if (option.selected) FontWeight.SemiBold else FontWeight.Medium),
                color = palette.ink,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis,
            )
            if (!option.subtitle.isNullOrBlank()) {
                Spacer(Modifier.height(Tokens.Space.Standard.xxs))
                Text(
                    text = option.subtitle,
                    style = type.caption,
                    color = palette.ink2,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                )
            }
        }
        if (option.selected) {
            Spacer(Modifier.width(Tokens.Space.Standard.s))
            Text(
                text = stringResource(R.string.shortcuts_widget_configure_selected),
                style = type.micro.copy(fontWeight = FontWeight.SemiBold),
                color = palette.accent,
                maxLines = 1,
            )
        }
    }
}
