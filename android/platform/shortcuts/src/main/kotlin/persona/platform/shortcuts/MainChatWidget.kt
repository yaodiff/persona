package persona.platform.shortcuts

import android.appwidget.AppWidgetManager
import android.content.Context
import androidx.glance.appwidget.GlanceAppWidgetManager
import android.content.res.Configuration
import android.os.Build
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight as ComposeWeight
import androidx.compose.ui.unit.Dp
import androidx.glance.ColorFilter
import androidx.glance.GlanceId
import androidx.glance.GlanceModifier
import androidx.glance.Image
import androidx.glance.ImageProvider
import androidx.glance.LocalSize
import androidx.glance.action.clickable
import androidx.glance.appwidget.GlanceAppWidget
import androidx.glance.appwidget.GlanceAppWidgetReceiver
import androidx.glance.appwidget.SizeMode
import androidx.glance.appwidget.action.actionStartActivity
import androidx.glance.appwidget.appWidgetBackground
import androidx.glance.appwidget.cornerRadius
import androidx.glance.appwidget.provideContent
import androidx.glance.appwidget.updateAll
import androidx.glance.background
import androidx.glance.color.ColorProvider
import androidx.glance.layout.Alignment
import androidx.glance.layout.Box
import androidx.glance.layout.Column
import androidx.glance.layout.Row
import androidx.glance.layout.Spacer
import androidx.glance.layout.fillMaxSize
import androidx.glance.layout.fillMaxWidth
import androidx.glance.layout.height
import androidx.glance.layout.padding
import androidx.glance.layout.size
import androidx.glance.layout.width
import androidx.glance.layout.wrapContentHeight
import androidx.glance.semantics.contentDescription
import androidx.glance.semantics.semantics
import androidx.glance.text.FontWeight
import androidx.glance.text.Text
import androidx.glance.text.TextStyle
import androidx.glance.unit.ColorProvider as GlanceColor
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.update
import persona.core.contracts.DirectionTaskState
import persona.core.designsystem.AppStyle
import persona.core.designsystem.AppStyleStore
import persona.core.designsystem.ColorSchemeChoice
import persona.core.designsystem.PersonaPalette
import persona.core.designsystem.SystemSurfaces
import persona.core.designsystem.ThemeColor
import persona.core.designsystem.Tokens
import persona.core.designsystem.personaFaceBitmap
import kotlin.math.roundToInt

/**
 * Functional home-screen card for the Persona main conversation (`2×2` / `4×2` / `4×3`, default `4×2`):
 * shows the live [personaFaceBitmap] expression, restrained status, prioritized waiting approval/question/plan
 * banner, sanitized latest main-chat summary, urgent open to-dos on large cards, and a bottom quick action bar
 * ("Ask Persona…" + "New to-do") sized strictly against container height and `fontScale`.
 */
class MainChatWidget : GlanceAppWidget() {
    override val sizeMode: SizeMode = SizeMode.Exact

    override suspend fun provideGlance(context: Context, id: GlanceId) {
        val dependencies = SurfacesDependencies.of(context)
        val feed = dependencies.fleet()
        feed.settled()
        val assistant = dependencies.assistant()
        val sessions = dependencies.sessions()
        val interactions = dependencies.interactions()
        val runtime = dependencies.surfaces()

        provideContent {
            val revision by lookRevision.collectAsState()
            val look = remember(revision) {
                val appearance = AppStyleStore(context)
                MainChatWidgetLook.of(
                    style = appearance.style.value,
                    color = appearance.color.value,
                    scheme = appearance.scheme.value,
                )
            }
            val paired by feed.paired.collectAsState()
            val assistantSnapshot by assistant.assistant.collectAsState()
            val sessionList by sessions.sessions.collectAsState()
            val pendingList by interactions.pending.collectAsState()
            val settledPairing by runtime.mainChatLivePairing.collectAsState()
            val activePairing = if (paired) feed.activePairing()?.takeIf(String::isNotBlank) else null
            val appWidgetId = remember(id) {
                runCatching { GlanceAppWidgetManager(context).getAppWidgetId(id) }
                    .getOrDefault(AppWidgetManager.INVALID_APPWIDGET_ID)
            }
            val binding = remember(activePairing, appWidgetId, revision) {
                ConversationWidgetPrefs.read(context, appWidgetId, activePairing)
            }
            val conversationKey = binding?.conversationKey ?: ConversationWidgetPrefs.MAIN_CONVERSATION_KEY
            val live = MainChatWidgetState.build(
                paired = paired,
                assistant = assistantSnapshot,
                sessions = sessionList,
                interactions = pendingList,
                resources = context.resources,
                targetBinding = binding,
            )
            val liveSettled = MainChatWidgetState.isLiveSettled(
                paired = paired,
                activePairing = activePairing,
                settledPairing = settledPairing,
                assistant = assistantSnapshot,
                sessions = sessionList,
                interactions = pendingList,
            )
            val persisted = remember(paired, activePairing, conversationKey, liveSettled, revision) {
                if (paired && !liveSettled) MainChatWidgetStore.read(context, activePairing, conversationKey) else null
            }
            val snapshot = MainChatWidgetState.selectSnapshot(
                live = live,
                persisted = persisted,
                liveSettled = liveSettled,
            )
            Content(context, look, snapshot, appWidgetId)
        }
    }

    @Composable
    private fun Content(context: Context, look: MainChatWidgetLook, snapshot: MainChatWidgetSnapshot, appWidgetId: Int) {
        val size = LocalSize.current
        val fontScale = context.resources.configuration.fontScale
        val phase = snapshot.phase
        val hasStatusBadge = snapshot.paired && (phase == MainChatPhase.NeedsYou || phase == MainChatPhase.Working)
        val plan = MainChatWidgetLayout.compute(
            widthDp = size.width.value,
            heightDp = size.height.value,
            fontScale = fontScale,
            paired = snapshot.paired,
            hasPending = snapshot.paired && snapshot.pending != null,
            hasStatusBadge = hasStatusBadge,
            availableTodos = snapshot.todos.size,
        )

        val outerPadding = when {
            plan.tightPadding -> Tokens.Space.Standard.xs
            plan.compactPadding -> Tokens.Space.Standard.s
            else -> Tokens.Space.Standard.m
        }
        val sectionGap = when {
            plan.tightPadding -> Tokens.Space.Standard.xxs
            plan.compactPadding -> Tokens.Space.Standard.xs
            else -> Tokens.Space.Standard.sm
        }
        val cardIntent = when {
            !snapshot.paired -> SurfaceLinks.app(context)
            snapshot.bindingMissing -> ConversationWidgetConfigureActivity.reconfigureIntent(context, appWidgetId)
            snapshot.isMainChat -> SurfaceLinks.mainChat(context, snapshot.mainSession)
            snapshot.mainSession != null -> SurfaceLinks.conversation(context, snapshot.mainSession, assistant = snapshot.targetAssistant)
            else -> SurfaceLinks.mainChat(context, null)
        }
        val isDark = look.isDark(context)
        val faceDp = if (plan.compactHeader) Tokens.Size.icon else Tokens.Size.wellXS
        val density = context.resources.displayMetrics.density.coerceAtLeast(1f)
        val faceBitmap = personaFaceBitmap(
            state = phase.personaState,
            sizePx = (faceDp.value * density).roundToInt(),
            dark = isDark,
            needsYouRing = look.needsYouRingColor(isDark),
            density = density,
            compact = true,
        )

        Column(
            modifier = GlanceModifier
                .fillMaxSize()
                .appWidgetBackground()
                .mainChatCornerRadius()
                .background(look.card)
                .padding(outerPadding)
                .clickable(actionStartActivity(cardIntent)),
        ) {
            Header(
                context = context,
                look = look,
                plan = plan,
                snapshot = snapshot,
                faceDp = faceDp,
                faceBitmap = faceBitmap,
            )

            if (!snapshot.paired) {
                UnpairedBody(context, look, plan, sectionGap)
                return@Column
            }

            if (snapshot.bindingMissing) {
                Spacer(GlanceModifier.height(sectionGap))
                Text(
                    text = context.getString(R.string.shortcuts_widget_binding_missing_hint),
                    style = look.mutedBody,
                    maxLines = plan.summaryMaxLines.coerceAtLeast(2),
                )
                return@Column
            }

            if (plan.showPendingBanner && snapshot.pending != null) {
                Spacer(GlanceModifier.height(sectionGap))
                PendingBanner(context, look, snapshot.pending, snapshot.pendingCount, plan.compactPendingBanner)
            }

            if (plan.summaryMaxLines > 0) {
                Spacer(GlanceModifier.height(sectionGap))
                val summaryText = when {
                    snapshot.summary.isNotEmpty() -> snapshot.summary
                    phase == MainChatPhase.Working -> context.getString(R.string.shortcuts_main_chat_working_fallback)
                    else -> context.getString(R.string.shortcuts_main_chat_idle_greeting)
                }
                val summaryStyle = if (snapshot.summary.isNotEmpty()) look.body else look.mutedBody
                Text(summaryText, style = summaryStyle, maxLines = plan.summaryMaxLines)
            }

            val shownTodos = snapshot.todos.take(plan.todoRows)
            if (shownTodos.isNotEmpty()) {
                Spacer(GlanceModifier.height(Tokens.Space.Standard.sm))
                shownTodos.forEach { todo ->
                    TodoRow(context, look, todo, plan.showTodoStateLabel)
                }
            }

            if (plan.showActionBar) {
                Spacer(GlanceModifier.defaultWeight())
                Spacer(GlanceModifier.height(Tokens.Space.Standard.s))
                QuickActionBar(context, look, plan.expandedActionBar, snapshot)
            }
        }
    }

    @Composable
    private fun Header(
        context: Context,
        look: MainChatWidgetLook,
        plan: MainChatLayoutPlan,
        snapshot: MainChatWidgetSnapshot,
        faceDp: Dp,
        faceBitmap: android.graphics.Bitmap,
    ) {
        val statusText = when {
            !plan.showHeaderStatus -> null
            snapshot.phase == MainChatPhase.NeedsYou ->
                context.getString(R.string.shortcuts_main_chat_status_needs_you, snapshot.pendingCount.coerceAtLeast(1))
            snapshot.phase == MainChatPhase.Working ->
                context.getString(R.string.shortcuts_main_chat_status_thinking)
            else -> null
        }
        val statusStyle = when (snapshot.phase) {
            MainChatPhase.NeedsYou -> look.needsYouStatus
            MainChatPhase.Working -> look.workingStatus
            else -> look.caption
        }
        val timeText = if (plan.showHeaderTime) MainChatText.formatHeaderTime(snapshot.updatedAt, context) else null

        Row(
            modifier = GlanceModifier.fillMaxWidth(),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Image(
                provider = ImageProvider(faceBitmap),
                contentDescription = null,
                modifier = GlanceModifier.size(faceDp),
            )
            Spacer(GlanceModifier.width(Tokens.Space.Standard.s))
            Row(
                modifier = GlanceModifier.defaultWeight(),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                Text(
                    text = if (snapshot.isMainChat || snapshot.title.isNullOrBlank()) context.getString(R.string.shortcuts_main_chat_header_title) else snapshot.title,
                    style = if (plan.compactHeader) look.compactTitle else look.headerTitle,
                    maxLines = 1,
                )
                if (statusText != null) {
                    Spacer(GlanceModifier.width(Tokens.Space.Standard.sm))
                    Dot(
                        color = if (snapshot.phase == MainChatPhase.NeedsYou) look.needsYouDot else look.workingDot,
                        size = Tokens.Size.dotS,
                    )
                    Spacer(GlanceModifier.width(Tokens.Space.Standard.xs))
                    Text(statusText, style = statusStyle, maxLines = 1)
                }
            }
            if (timeText != null) {
                Spacer(GlanceModifier.width(Tokens.Space.Standard.xs))
                Text(timeText, style = look.micro, maxLines = 1)
            }
        }
    }

    @Composable
    private fun UnpairedBody(context: Context, look: MainChatWidgetLook, plan: MainChatLayoutPlan, sectionGap: Dp) {
        if (!plan.showUnpairedHeadline) return
        Spacer(GlanceModifier.height(sectionGap))
        Text(
            text = context.getString(R.string.shortcuts_not_paired_short),
            style = if (plan.compactHeader) look.compactTitle else look.headerTitle,
            maxLines = 1,
        )
        if (plan.summaryMaxLines > 0) {
            Spacer(GlanceModifier.height(Tokens.Space.Standard.xxs))
            Text(
                text = context.getString(R.string.shortcuts_main_chat_unpaired_hint),
                style = look.mutedBody,
                maxLines = plan.summaryMaxLines,
            )
        }
    }

    @Composable
    private fun PendingBanner(
        context: Context,
        look: MainChatWidgetLook,
        pending: MainChatPendingItem,
        pendingCount: Int,
        compact: Boolean,
    ) {
        val intent = SurfaceLinks.request(context, pending.toInteraction()) { pending.assistant }
        val vPad = if (compact) Tokens.Space.Standard.xxs else Tokens.Space.Standard.sm
        Row(
            modifier = GlanceModifier
                .fillMaxWidth()
                .cornerRadius(Tokens.Radius.Standard.control)
                .background(look.pendingFill)
                .padding(horizontal = Tokens.Space.Standard.s, vertical = vPad)
                .clickable(actionStartActivity(intent)),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Dot(look.needsYouDot, Tokens.Size.dotS)
            Spacer(GlanceModifier.width(Tokens.Space.Standard.s))
            Text(
                text = pending.title,
                style = look.pendingTitle,
                maxLines = 1,
                modifier = GlanceModifier.defaultWeight(),
            )
            val remaining = (pendingCount - 1).coerceAtLeast(0)
            if (remaining > 0) {
                Spacer(GlanceModifier.width(Tokens.Space.Standard.xs))
                Box(
                    modifier = GlanceModifier
                        .cornerRadius(Tokens.Radius.Standard.badge)
                        .background(look.needsYouDot)
                        .padding(horizontal = Tokens.Space.Standard.sm, vertical = Tokens.Space.Standard.xxs),
                    contentAlignment = Alignment.Center,
                ) {
                    Text(
                        text = context.getString(R.string.shortcuts_main_chat_pending_more, remaining),
                        style = look.badgeText,
                        maxLines = 1,
                    )
                }
            }
        }
    }

    @Composable
    private fun TodoRow(
        context: Context,
        look: MainChatWidgetLook,
        todo: MainChatTodoItem,
        showStateLabel: Boolean,
    ) {
        val dotColor = when (todo.taskState) {
            DirectionTaskState.NeedsYou -> look.needsYouDot
            DirectionTaskState.Stuck -> look.dangerDot
            DirectionTaskState.Review -> look.doneDot
            DirectionTaskState.Working -> look.workingDot
            DirectionTaskState.Planned -> look.idleDot
        }
        Row(
            modifier = GlanceModifier
                .fillMaxWidth()
                .padding(vertical = Tokens.Space.Standard.xxs),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Dot(dotColor, Tokens.Size.dotS)
            Spacer(GlanceModifier.width(Tokens.Space.Standard.s))
            Text(
                text = todo.title,
                style = look.todoTitle,
                maxLines = 1,
                modifier = GlanceModifier.defaultWeight(),
            )
            if (showStateLabel) {
                Spacer(GlanceModifier.width(Tokens.Space.Standard.xs))
                Text(
                    text = MainChatText.todoStateLabel(todo.taskState, context.resources),
                    style = look.micro,
                    maxLines = 1,
                )
            }
        }
    }

    @Composable
    private fun QuickActionBar(
        context: Context,
        look: MainChatWidgetLook,
        expanded: Boolean,
        snapshot: MainChatWidgetSnapshot,
    ) {
        val askHint = context.getString(R.string.shortcuts_main_chat_ask_hint)
        val newTodoLabel = context.getString(R.string.shortcuts_new_todo_label)
        val buttonSize = if (expanded) Tokens.MinTouch.android else Tokens.Size.slimBar
        val askIntent = if (!snapshot.isMainChat && snapshot.mainSession != null) {
            SurfaceLinks.askConversation(context, snapshot.mainSession, assistant = snapshot.targetAssistant)
        } else {
            SurfaceLinks.ask(context)
        }
        Row(
            modifier = GlanceModifier.fillMaxWidth(),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            val askModifier = GlanceModifier
                .defaultWeight()
                .let { base ->
                    if (expanded) base.wrapContentHeight().padding(horizontal = Tokens.Space.Standard.m, vertical = Tokens.Space.Standard.s)
                    else base.height(Tokens.Size.slimBar).padding(horizontal = Tokens.Space.Standard.m)
                }
                .cornerRadius(Tokens.Radius.Standard.panel)
                .background(look.inputFill)
                .clickable(actionStartActivity(askIntent))
                .semantics { contentDescription = askHint }
            Row(
                modifier = askModifier,
                verticalAlignment = Alignment.CenterVertically,
            ) {
                Image(
                    provider = ImageProvider(R.drawable.ic_widget_ask),
                    contentDescription = null,
                    modifier = GlanceModifier.size(Tokens.Size.markS),
                    colorFilter = ColorFilter.tint(look.accent),
                )
                Spacer(GlanceModifier.width(Tokens.Space.Standard.s))
                Text(text = askHint, style = look.inputHint, maxLines = 1)
            }
            Spacer(GlanceModifier.width(Tokens.Space.Standard.s))
            Box(
                modifier = GlanceModifier
                    .size(buttonSize)
                    .cornerRadius(Tokens.Radius.Standard.panel)
                    .background(look.actionFill)
                    .clickable(actionStartActivity(SurfaceLinks.newTodo(context)))
                    .semantics { contentDescription = newTodoLabel },
                contentAlignment = Alignment.Center,
            ) {
                Image(
                    provider = ImageProvider(R.drawable.ic_widget_new_todo),
                    contentDescription = null,
                    modifier = GlanceModifier.size(Tokens.Size.iconXS),
                    colorFilter = ColorFilter.tint(look.accent),
                )
            }
        }
    }

    @Composable
    private fun Dot(color: GlanceColor, size: Dp) {
        Box(GlanceModifier.size(size).cornerRadius(size / 2).background(color)) {}
    }

    private fun GlanceModifier.mainChatCornerRadius(): GlanceModifier =
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
            cornerRadius(android.R.dimen.system_app_widget_background_radius)
        } else {
            cornerRadius(Tokens.Radius.Standard.panel)
        }

    companion object {
        private val lookRevision = MutableStateFlow(0L)

        /** Redraws every placed [MainChatWidget] instance (no-op when none is placed). */
        suspend fun refresh(context: Context) {
            lookRevision.update { it + 1L }
            val _ = runCatching { MainChatWidget().updateAll(context.applicationContext) }
        }
    }
}

class MainChatWidgetReceiver : GlanceAppWidgetReceiver() {
    override val glanceAppWidget: GlanceAppWidget = MainChatWidget()

    override fun onDeleted(context: Context, appWidgetIds: IntArray) {
        ConversationWidgetPrefs.delete(context, appWidgetIds)
        super.onDeleted(context, appWidgetIds)
    }

    override fun onRestored(context: Context, oldWidgetIds: IntArray?, newWidgetIds: IntArray?) {
        ConversationWidgetPrefs.restore(context, oldWidgetIds, newWidgetIds)
        super.onRestored(context, oldWidgetIds, newWidgetIds)
    }
}

/**
 * Design-system tokens for [MainChatWidget], resolving the user's [AppStyle], [ThemeColor],
 * and [ColorSchemeChoice] into Glance day/night color pairs and typography roles with zero hard-coded values.
 */
internal class MainChatWidgetLook private constructor(
    val light: PersonaPalette,
    val dark: PersonaPalette,
    style: AppStyle,
    private val scheme: ColorSchemeChoice,
) {
    private val type = SystemSurfaces.type(style)

    private fun pair(pick: (PersonaPalette) -> Color): GlanceColor = when (scheme.dark) {
        true -> ColorProvider(day = pick(dark), night = pick(dark))
        false -> ColorProvider(day = pick(light), night = pick(light))
        null -> ColorProvider(day = pick(light), night = pick(dark))
    }

    val card = pair { it.surfaceOpaque }
    val accent = pair { it.accent }
    val inputFill = pair { it.sunken }
    val actionFill = pair { it.accentSoft }
    val pendingFill = pair { it.needsYou.tint }

    val needsYouDot = pair { it.needsYou.dot }
    val workingDot = pair { it.working.dot }
    val doneDot = pair { it.done.dot }
    val idleDot = pair { it.idle.dot }
    val dangerDot = pair { it.danger.dot }

    private val ink = pair { it.ink }
    private val ink2 = pair { it.ink2 }
    private val needsYouInk = pair { it.needsYou.text }
    private val workingInk = pair { it.working.text }
    private val onNeedsYouDot = pair { it.needsYou.onDot }

    val headerTitle = text(type.callout.copy(fontWeight = ComposeWeight.SemiBold), ink)
    val compactTitle = text(type.caption.copy(fontWeight = ComposeWeight.SemiBold), ink)
    val body = text(type.caption, ink)
    val mutedBody = text(type.caption, ink2)
    val pendingTitle = text(type.caption.copy(fontWeight = ComposeWeight.Medium), needsYouInk)
    val todoTitle = text(type.micro.copy(fontWeight = ComposeWeight.Medium), ink)
    val caption = text(type.caption, ink2)
    val micro = text(type.micro, ink2)
    val needsYouStatus = text(type.micro.copy(fontWeight = ComposeWeight.SemiBold), needsYouInk)
    val workingStatus = text(type.micro.copy(fontWeight = ComposeWeight.Medium), workingInk)
    val badgeText = text(type.micro.copy(fontWeight = ComposeWeight.SemiBold), onNeedsYouDot)
    val inputHint = text(type.caption, ink2)

    fun isDark(context: Context): Boolean = scheme.dark
        ?: ((context.resources.configuration.uiMode and Configuration.UI_MODE_NIGHT_MASK) == Configuration.UI_MODE_NIGHT_YES)

    fun needsYouRingColor(darkAppearance: Boolean): Color = (if (darkAppearance) dark else light).needsYou.dot

    private fun text(style: androidx.compose.ui.text.TextStyle, color: GlanceColor) = TextStyle(
        color = color,
        fontSize = style.fontSize,
        fontWeight = when {
            (style.fontWeight?.weight ?: 400) >= 600 -> FontWeight.Bold
            (style.fontWeight?.weight ?: 400) >= 500 -> FontWeight.Medium
            else -> FontWeight.Normal
        },
    )

    companion object {
        fun of(
            style: AppStyle,
            color: ThemeColor = ThemeColor.Default,
            scheme: ColorSchemeChoice = ColorSchemeChoice.Default,
        ) = MainChatWidgetLook(
            light = SystemSurfaces.palette(style, dark = false, color = color),
            dark = SystemSurfaces.palette(style, dark = true, color = color),
            style = style,
            scheme = scheme,
        )
    }
}
