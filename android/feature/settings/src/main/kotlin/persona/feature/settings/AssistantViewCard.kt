package persona.feature.settings

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableStateMapOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextOverflow
import kotlinx.coroutines.launch
import persona.core.contracts.api.ExtensionViewItem
import persona.core.designsystem.GroupPosition
import persona.core.designsystem.JActionSheet
import persona.core.designsystem.JAction
import persona.core.designsystem.JDialog
import persona.core.designsystem.JGroupItem
import persona.core.designsystem.JIcon
import persona.core.designsystem.JTextButton
import persona.core.designsystem.LocalAppFeedback
import persona.core.designsystem.LocalAssistantCards
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import persona.core.designsystem.rowPadding
import persona.core.designsystem.icons.PhIcons
import persona.core.domain.views.AssistantAction
import persona.core.domain.views.AssistantCard
import persona.core.domain.views.AssistantCards
import persona.core.domain.views.AssistantFences
import persona.core.domain.views.AssistantOpen
import persona.core.domain.views.AssistantOpenTarget
import persona.core.domain.views.CardOutcome

// An assistant's card in its conversation (docs/assistant/assistant-views.md, #2022): the extensions' renderer with the
// assistant's actions — reply (once per card, with the version the user saw), open in the app, push / pop its pages —
// its answer from the hub's store, and its fallback whenever it cannot be drawn.

/** A ```persona-view block of a reply under its key: the card when it is a whole one, else its words. */
@Composable
internal fun AssistantBlock(key: String, raw: String, complete: Boolean, modifier: Modifier = Modifier) {
    val index = key.substringAfterLast('/').toIntOrNull() ?: 0
    val card = remember(raw, complete, index) { if (complete && index < AssistantFences.MostPerText) AssistantCards.parse(raw) else null }
    // A block from before cards had to carry a fallback keeps its old read-only drawing; a new one that fails is its words.
    val legacy = remember(raw, complete, card) {
        if (card != null || !complete || raw.contains("\"fallback\"")) null else PersonaViewBlocks.parse(PersonaViewBlocks.View, raw)
    }
    if (legacy != null) PersonaViewBlock(legacy, modifier)
    else if (card == null) AssistantFallback(remember(raw) { AssistantFences.fallback(raw) }, modifier)
    // A reply's card never changes: its version is 1. It answers only once the hub registered it from the transcript.
    else AssistantViewCard(key, 1L, card, live = false, modifier = modifier)
}

/**
 * A reply's card block in a picture (Share as a picture, #7261): a ```persona-view as [AssistantBlock] draws it, outside
 * any conversation so nothing on it answers, its replies drawn as they look; one with an HTML page, which only runs live,
 * is its fallback. A ```persona-card is drawn as always, or its placeholder: never its JSON.
 */
@Composable
internal fun AssistantPictureBlock(language: String, raw: String, complete: Boolean, index: Int, modifier: Modifier = Modifier) {
    if (language != AssistantFences.View) {
        val parsed = remember(language, raw, complete) { if (complete) PersonaViewBlocks.blocks.parse(language, raw) else null }
        if (parsed != null) PersonaViewBlocks.blocks.draw(parsed) else AssistantFallback(null, modifier)
    } else {
        val card = remember(raw, complete, index) { if (complete && index in 0 until AssistantFences.MostPerText) AssistantCards.parse(raw) else null }
        val legacy = remember(raw, complete, card) {
            if (card != null || !complete || raw.contains("\"fallback\"")) null else PersonaViewBlocks.parse(PersonaViewBlocks.View, raw)
        }
        when {
            legacy != null -> PersonaViewBlock(legacy, modifier)
            card == null -> AssistantFallback(remember(raw) { AssistantFences.fallback(raw) }, modifier)
            !card.drawsInPicture -> AssistantFallback(card.fallback, modifier)
            else -> CompositionLocalProvider(LocalAssistantCards provides null) {
                AssistantViewCard("picture/$index", 1L, card, live = false, modifier = modifier, picture = true)
            }
        }
    }
}

/** A live card of the conversation (a feed `view` item): the hub's latest version of it, redrawn in place. */
@Composable
internal fun AssistantLiveCard(id: String, modifier: Modifier = Modifier) {
    val host = LocalAssistantCards.current ?: return
    val all by host.store.state.collectAsState()
    val live = all[host.conversation]?.views?.get(id) ?: return
    val card = remember(live.id, live.version) { AssistantCards.parse(live.view) }
    if (card == null) AssistantFallback(remember(live.view) { AssistantFences.fallback(live.view.toString()) }, modifier)
    else AssistantViewCard(id, live.version, card, live = true, modifier = modifier)
}

/** A card's plain words, where it cannot be drawn (still coming in, broken, or one this app refuses). */
@Composable
internal fun AssistantFallback(text: String?, modifier: Modifier = Modifier) {
    JGroupItem(GroupPosition.Single, modifier) {
        Text(text ?: stringResource(R.string.settings_card_placeholder), Modifier.fillMaxWidth().padding(horizontal = rowPadding, vertical = Tokens.Space.m),
            style = PersonaTheme.type.body, color = PersonaTheme.palette.ink2)
    }
}

/** A confirm on its way: the action, what it sends, and the card's version the user is looking at. */
private data class Confirming(val item: ExtensionViewItem, val action: AssistantAction, val values: Map<String, String>, val version: Long)

/**
 * The card [key] at [version]. Its page stack and the fields typed in stay while a live card is replaced (a page or a
 * field that is gone goes with it); what the hub recorded is what shows once it is answered.
 */
@Composable
internal fun AssistantViewCard(key: String, version: Long, card: AssistantCard, live: Boolean, modifier: Modifier = Modifier,
    picture: Boolean = false) {
    val host = LocalAssistantCards.current
    val feedback = LocalAppFeedback.current
    val uri = LocalUriHandler.current
    val scope = rememberCoroutineScope()
    val changedText = stringResource(R.string.settings_card_changed)
    val answeredText = stringResource(R.string.settings_card_answered)
    val cannotAnswerText = stringResource(R.string.settings_card_cannot_answer)
    val badLinkText = stringResource(R.string.settings_card_bad_link)
    val unsupportedText = stringResource(R.string.settings_card_target_unsupported)
    val busyText = stringResource(R.string.settings_card_busy)
    val notSentText = stringResource(R.string.settings_card_not_sent)
    val words = AssistantCardWords(stringResource(R.string.settings_card_chosen), stringResource(R.string.settings_card_on),
        stringResource(R.string.settings_card_off), stringResource(R.string.settings_ext_load_more))
    fun say(outcome: CardOutcome) {
        when (outcome) {
            CardOutcome.AlreadyAnswered -> feedback?.notice(answeredText)
            CardOutcome.Changed -> feedback?.notice(changedText)
            CardOutcome.Busy -> feedback?.notice(busyText)
            CardOutcome.Unsupported -> feedback?.notice(cannotAnswerText)
            is CardOutcome.Failed -> feedback?.notice(outcome.message ?: notSentText)
            CardOutcome.Recorded, is CardOutcome.Open -> Unit
        }
    }
    val all = host?.store?.state?.collectAsState()?.value
    val cards = all?.get(host.conversation)
    val answer = cards?.answers?.get(key)
    val busyKeys = host?.actions?.busy?.collectAsState()?.value.orEmpty()
    val busy = host != null && host.actions.busyKey(host.conversation, key) in busyKeys
    // A reply's block takes answers only once the hub registered it from a trusted transcript; a live card is the hub's.
    val registered = live || cards?.blocks?.contains(key) == true
    // Only a hub that says it takes cards' answers (its hello's assistant_views) is sent one: on an older hub, or with
    // cards switched off, the card draws and nothing answers.
    val features = host?.actions?.features?.collectAsState()?.value.orEmpty()
    val hubAnswers = host?.actions?.answers(features) == true
    val locked = host == null || !host.interactive || !hubAnswers || !registered || answer != null
    var pushed by rememberSaveable(key) { mutableStateOf(listOf<String>()) }
    // A page a newer version took away goes, and the pages above it.
    val stack = pushed.takeWhile { it in card.pages }
    val pageName = stack.lastOrNull()
    val page = card.page(pageName) ?: card.root
    val edits = remember(key) { mutableStateMapOf<String, String>() }
    var confirming by remember(key) { mutableStateOf<Confirming?>(null) }
    var choosing by remember(key) { mutableStateOf<ExtensionViewItem?>(null) }
    var sending by remember(key) { mutableStateOf<String?>(null) }
    val currentVersion by rememberUpdatedState(version)
    // The card changed under an open confirm: what the user was about to confirm is not what is there now.
    LaunchedEffect(version) {
        if (confirming != null && confirming?.version != version) {
            confirming = null
            feedback?.notice(changedText)
        }
    }
    // In a picture nothing answers, yet its replies and fields are drawn as the chat draws a card that can still be answered.
    val shownLocked = locked && !picture
    val presented = remember(page, card, answer, shownLocked, words) { AssistantCardPresentation.page(page, card, answer, shownLocked, words) }
    val initial = remember(page) { ExtensionViewText.fields(page) + card.searchValues(page) }
    val values = initial + edits.filterKeys { it in initial }
    val state = ExtensionViewState(view = presented, values = values, acting = sending.takeIf { busy })

    fun open(url: String) {
        when (val target = AssistantOpen.target(url)) {
            is AssistantOpenTarget.Web -> runCatching { uri.openUri(target.url) }
            is AssistantOpenTarget.Session -> runCatching {
                uri.openUri("persona://session/${segment(target.machine)}/${segment(target.session)}")
            }
            null -> feedback?.notice(badLinkText)
            else -> feedback?.notice(unsupportedText)
        }
    }
    fun send(action: AssistantAction, sent: Map<String, String>, confirmed: Boolean, seen: Long) {
        val h = host ?: return
        if (seen != currentVersion) { feedback?.notice(changedText); return }
        sending = action.id
        scope.launch {
            say(h.actions.submit(h.conversation, key, action, sent, confirmed, seen))
            sending = null
        }
    }
    fun reply(item: ExtensionViewItem, action: AssistantAction, sent: Map<String, String>) {
        if (locked || busy) {
            feedback?.notice(if (answer != null) answeredText else cannotAnswerText)
            return
        }
        if (action.confirm != null) confirming = Confirming(item, action, sent, version)
        else send(action, sent, confirmed = false, seen = version)
    }
    fun press(item: ExtensionViewItem, id: String) {
        val action = card.actions[id] ?: return
        when {
            action.push != null -> if (stack.size < AssistantCards.MostDepth) pushed = stack + action.push!!
            action.pop -> pushed = stack.dropLast(1)
            action.open != null -> if (action.confirm != null) confirming = Confirming(item, action, emptyMap(), version) else open(action.open!!)
            action.reply != null -> reply(item, action, card.fields(action).associateWith { values[it].orEmpty() })
        }
    }
    // An HTML page's two calls, after the user confirmed them: a reply is the card's one answer (as a declared one is),
    // a link opens only while the card is still the version it was asked from.
    val bridge = host?.takeIf { it.actions.bridges(features) }?.let { h ->
        HtmlBridgeHost(
            reply = if (locked || busy) null else { componentKey, text ->
                say(h.actions.html(h.conversation, key, componentKey, pageName, version, text = text))
            },
            open = { componentKey, url ->
                val seen = version
                when (val outcome = h.actions.html(h.conversation, key, componentKey, pageName, seen, url = url)) {
                    is CardOutcome.Open -> if (seen == currentVersion) open(outcome.url) else feedback?.notice(changedText)
                    else -> say(outcome)
                }
            },
        )
    }
    val handle = ExtensionViewHandle(
        press = { item, a -> press(item, a.id) },
        edit = { name, value -> if (!locked) edits[name] = value },
        choose = { if (!locked) choosing = it },
        open = { url -> if (ExtensionViewText.isHttps(url)) runCatching { uri.openUri(url) } },
        segment = { item, value ->
            val action = item.action?.id?.let { card.actions[it] }
            if (action?.reply != null && !locked) {
                edits[item.name.orEmpty()] = value
                reply(item, action, card.fields(action).associateWith { name -> if (name == item.name) value else values[name].orEmpty() })
            }
        },
        // Typing in the search box only keeps the words: they go with the page's submit or filter, never on a pause
        // (a card is answered once).
        search = { query -> if (!locked) edits[AssistantCard.SearchField] = query },
        filter = { value ->
            val f = page.filter
            val action = f?.action?.id?.let { card.actions[it] }
            if (f != null && action?.reply != null && !locked) {
                edits[f.name] = value
                reply(ExtensionViewItem("button", title = f.title), action,
                    card.fields(action).associateWith { name -> if (name == f.name) value else values[name].orEmpty() })
            }
        },
    )
    Column(modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
        if (pageName != null) Row(verticalAlignment = Alignment.CenterVertically) {
            JTextButton(stringResource(R.string.settings_card_back), { pushed = stack.dropLast(1) }, icon = PhIcons.CaretLeft)
        }
        val searchAction = page.search?.action?.id?.let { card.actions[it] }?.takeIf { it.reply != null }
        val searchLabel = stringResource(R.string.settings_card_search)
        if (AssistantCardPresentation.searchable(presented)) key(pageName) {
            // The search is the card's answer like any reply: sent on one tap of "Search" with what the box holds, never
            // on a pause in typing.
            ExtensionSearchBar(presented, state, handle, submit = searchAction?.let { action ->
                SearchSubmit(searchLabel, sending = busy && sending == action.id) { query ->
                    edits[AssistantCard.SearchField] = query
                    reply(ExtensionViewItem("button", title = searchLabel), action,
                        card.fields(action).associateWith { name -> if (name == AssistantCard.SearchField) query else values[name].orEmpty() })
                }
            })
        }
        CompositionLocalProvider(LocalViewHtml provides { item, m -> AssistantHtmlItem(item, card, m, bridge) }) {
            ExtensionViewColumn(state, handle)
        }
        AssistantCardPresentation.status(answer)?.let { status ->
            val text = when (status) {
                AssistantCardStatus.Sending -> stringResource(R.string.settings_card_sending)
                AssistantCardStatus.Sent -> stringResource(R.string.settings_card_sent)
                AssistantCardStatus.Failed -> answer?.error?.takeIf { it.isNotBlank() }?.let { stringResource(R.string.settings_card_failed_reason, it) }
                    ?: stringResource(R.string.settings_card_failed)
            }
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                JIcon(if (status == AssistantCardStatus.Failed) PhIcons.WarningCircle else PhIcons.CheckCircle, null,
                    size = Tokens.Space.l, tint = PersonaTheme.palette.ink3)
                Text(text, style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2, maxLines = 2, overflow = TextOverflow.Ellipsis)
            }
        }
    }
    confirming?.let { c ->
        val res = androidx.compose.ui.platform.LocalResources.current
        val words = listOfNotNull(c.action.confirm, c.action.reply?.let { stringResource(R.string.settings_card_will_send, it) },
            c.action.open?.let { stringResource(R.string.settings_card_will_open, it) }).joinToString("\n\n")
        JDialog({ confirming = null }, title = ExtensionViewText.confirmTitle(res, c.item), text = words,
            confirm = ExtensionViewText.confirmLabel(res, c.item) to {
                confirming = null
                if (c.action.open != null) open(c.action.open!!) else send(c.action, c.values, confirmed = true, seen = c.version)
            }, destructive = c.item.role == "destructive")
    }
    choosing?.let { item ->
        val name = item.name.orEmpty()
        JActionSheet(item.choices.orEmpty().map { choice ->
            JAction(choice.label, checked = values[name] == choice.value, onClick = { edits[name] = choice.value })
        }, { choosing = null }, title = item.label ?: name)
    }
}

/** One path segment of an app link, escaped once. */
private fun segment(value: String): String = java.net.URLEncoder.encode(value, "UTF-8").replace("+", "%20")
