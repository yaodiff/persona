package persona.feature.settings

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.semantics.selected
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.customActions
import androidx.compose.ui.semantics.CustomAccessibilityAction
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.unit.dp
import androidx.annotation.StringRes
import androidx.compose.ui.res.stringResource
import androidx.compose.foundation.Image
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.produceState
import androidx.compose.runtime.remember
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.ui.graphics.ImageBitmap
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.unit.Dp
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.launch
import persona.core.contracts.api.PersonaInfo
import persona.core.designsystem.*
import persona.core.designsystem.corner
import persona.core.designsystem.dense
import persona.core.designsystem.icons.PhIcons
import persona.core.domain.sort.AgentListDisplay
import persona.core.domain.sort.AgentListDisplaySettings
import persona.core.domain.sort.AgentRowStyle
import persona.core.domain.sort.AgentSeenRows

/**
 * Settings › Interface style: three styles, applied at once and remembered on this phone, then "Theme color" (iOS `ThemeColor`) and "Appearance"
 * (iOS `ColorSchemeChoice`); both default to what the app always did (the style's own colors, the system's appearance).
 * Each style option shows a small live preview drawn with the real components of that style. On Glass, "Agents list
 * layout" ([agentsDisplay]) last.
 */
@Composable
fun AppearanceSettings(modifier: Modifier = Modifier, agentsDisplay: AgentListDisplaySettings? = null) {
    val store = LocalAppStyleStore.current ?: return
    val current by store.style.collectAsState()
    val glass by store.glass.collectAsState()
    val palette = PersonaTheme.palette
    Column(modifier.fillMaxWidth()) {
        SidebarOptions(store)
        Spacer(Modifier.height(Tokens.Space.l))
        Text(stringResource(R.string.settings_style_intro), style = PersonaTheme.type.callout, color = palette.ink2)
        Spacer(Modifier.height(Tokens.Space.l))
        AppStyle.entries.forEach { style ->
            StyleOption(style, selected = style == current) { store.setStyle(style) }
            Spacer(Modifier.height(Tokens.Space.m))
        }
        ThemeColorOptions(store)
        ExtensionThemeOptions()
        SchemeOptions(store)
        DensityOptions(store)
        if (current == AppStyle.Glass) {
            JSectionHeader(stringResource(R.string.settings_style_transparency))
            JSegmented(GlassPreference.entries.map { JSegment(it.label) }, GlassPreference.entries.indexOf(glass), { store.setGlass(GlassPreference.entries[it]) })
            Spacer(Modifier.height(10.dp.dense))
            Text(
                stringResource(when (glass) {
                    GlassPreference.Auto -> R.string.settings_style_glass_auto
                    GlassPreference.Reduced -> R.string.settings_style_glass_reduced
                }),
                style = PersonaTheme.type.caption, color = palette.ink3,
            )
        }
        // Glass draws the Agents list in the chosen row style (#4338 A9); Brief and Board keep Compact and Detailed (decision 8).
        if (current == AppStyle.Glass && agentsDisplay != null) AgentsListLayout(agentsDisplay)
        if (agentsDisplay != null) SeenAgentsOptions(agentsDisplay)
    }
}

@Composable
private fun SeenAgentsOptions(settings: AgentListDisplaySettings) {
    val selected by settings.seenRows.collectAsState(AgentSeenRows.Stay)
    val scope = rememberCoroutineScope()
    JSectionHeader(stringResource(R.string.settings_seen_agents))
    AgentSeenRows.entries.forEach { choice ->
        val title = stringResource(when (choice) {
            AgentSeenRows.Stay -> R.string.settings_seen_agents_stay
            AgentSeenRows.Leave -> R.string.settings_seen_agents_leave
        })
        val isSelected = choice == selected
        Row(
            Modifier.fillMaxWidth().option(isSelected, onClick = {
                scope.launch { settings.setSeenRows(choice) }
            }).padding(Tokens.Space.m),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Text(title, Modifier.weight(1f), style = PersonaTheme.type.body, color = PersonaTheme.palette.ink)
            JRadio(isSelected, null)
        }
    }
}

@Composable
private fun SidebarOptions(store: AppStyleStore) {
    val current by store.sidebar.collectAsState()
    JSectionHeader(stringResource(R.string.settings_sidebar))
    JSegmented(
        listOf(
            JSegment(stringResource(R.string.settings_sidebar_separate)),
            JSegment(stringResource(R.string.settings_sidebar_merged)),
        ),
        SidebarLayout.entries.indexOf(current),
        { store.setSidebar(SidebarLayout.entries[it]) },
        modifier = Modifier.testTag("settings-sidebar-layout"),
    )
}

/**
 * "Agents list layout" (iOS Settings › Appearance › Agents List Layout, `LayoutSettingsPage`): the four ways Glass draws
 * the Agents list, each with two agents drawn that way and what it shows; the same choice as the Agents page's ⋯ › Display.
 */
@Composable
private fun AgentsListLayout(settings: AgentListDisplaySettings) {
    val chosen by settings.display.collectAsState(AgentListDisplay.Default)
    val scope = rememberCoroutineScope()
    JSectionHeader(stringResource(R.string.settings_agents_layout))
    AgentListDisplay.entries.forEach { display ->
        LayoutOption(display, selected = display == chosen) {
            scope.launch {
                // Kept with the list's settings of the hub group the phone is paired with; none to keep while unpaired.
                try { settings.setDisplay(display) } catch (e: kotlinx.coroutines.CancellationException) { throw e } catch (_: Exception) {}
            }
        }
        Spacer(Modifier.height(Tokens.Space.m))
    }
}

/** One way to draw the Agents list (iOS `LayoutChoice`): its name, two agents drawn that way, and what it shows. */
@Composable
private fun LayoutOption(display: AgentListDisplay, selected: Boolean, onClick: () -> Unit) {
    val palette = PersonaTheme.palette
    Column(Modifier.option(selected, onClick).testTag("layout-agents-${display.name}").padding(Tokens.Space.m),
        verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text(display.label, Modifier.weight(1f), style = PersonaTheme.type.headline, color = palette.ink, maxLines = 1)
            // The card is the radio button; this one only shows the state, so a screen reader stops once.
            JRadio(selected, null)
        }
        LayoutPreview(display)
        Text(display.summary, style = PersonaTheme.type.caption, color = palette.ink2)
    }
}

/**
 * The two agents a layout option draws (iOS `LayoutSamples`): one waiting for you, one at work, on the page's color as
 * the list draws them. A picture: screen readers skip it and taps reach the option around it.
 */
@Composable
private fun LayoutPreview(display: AgentListDisplay) {
    val claude = AgentMarkSpec(AgentMarkKind.Claude, "Claude Code", null)
    Box(Modifier.fillMaxWidth().clip(PersonaTheme.shapes.cardShape).background(PersonaTheme.palette.background).clearAndSetSemantics {}) {
        Column(Modifier.fillMaxWidth().padding(Tokens.Space.s)) {
            LayoutSamples.forEachIndexed { index, sample ->
                AgentRow(
                    stringResource(sample.title), sample.status, onClick = {}, position = GroupPosition.of(index, LayoutSamples.size),
                    preview = stringResource(sample.preview), time = stringResource(sample.time),
                    // As the list words them: Needs you before the words; State Cards spell every state out at the foot.
                    statusText = sample.status.localizedLabel.takeIf { sample.status == Status.NeedsYou || display == AgentListDisplay.Cards },
                    compact = display == AgentListDisplay.CompactList, style = display.style ?: AgentRowStyle.Default,
                    agentMark = claude, place = RowPlace(sample.tab),
                )
            }
        }
        Box(Modifier.matchParentSize().pointerInput(Unit) {})
    }
}

/** iOS's two layout samples: the one waiting for you on its "tests" tab, the one at work on its "widget" tab. */
private val LayoutSamples by lazy { listOf(LayoutSample(PreviewRows[0], "tests"), LayoutSample(PreviewRows[1], "widget")) }

private class LayoutSample(row: PreviewRow, val tab: String) {
    @StringRes val title = row.title
    val status = row.status
    @StringRes val preview = row.preview
    @StringRes val time = row.time
}

@Composable
private fun StyleOption(style: AppStyle, selected: Boolean, onClick: () -> Unit) {
    val palette = PersonaTheme.palette
    Row(
        Modifier.option(selected, onClick).padding(14.dp.dense),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        StylePreview(style)
        Spacer(Modifier.width(16.dp.dense))
        Column(Modifier.weight(1f)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text(style.label, style = PersonaTheme.type.title, color = palette.ink)
                if (style == AppStyle.Default) { Spacer(Modifier.width(8.dp.dense)); JTag(stringResource(R.string.settings_default)) }
            }
            Spacer(Modifier.height(4.dp.dense))
            Text(style.summary, style = PersonaTheme.type.callout, color = palette.ink2)
        }
        Spacer(Modifier.width(8.dp.dense))
        // The card is the radio button; this one only shows the state, so a screen reader stops once.
        JRadio(selected, null)
    }
}

/** A selectable option card: the whole card is the radio button, outlined in the accent (ink in Brief) when chosen. */
@Composable
private fun Modifier.option(selected: Boolean, onClick: () -> Unit, radio: Boolean = true): Modifier {
    val palette = PersonaTheme.palette
    val shape = PersonaTheme.shapes.cardShape
    return fillMaxWidth().clip(shape).background(palette.surface)
        .border(if (selected) 2.dp else 1.dp, if (selected) palette.accent.takeIf { PersonaTheme.style != AppStyle.Brief } ?: palette.ink else palette.line, shape)
        .clickable(role = if (radio) Role.RadioButton else Role.Button, onClick = onClick)
        .semantics(mergeDescendants = true) { if (radio) this.selected = selected }
}

/** The same two primary destinations as the shell, including in appearance previews. */
@Composable
private fun previewTabs() = listOf(
    NavItem("Persona", stringResource(R.string.settings_model_assistant), PhIcons.Sparkle),
    NavItem("Agents", "Agents", PhIcons.SquaresFour),
)

/** The sessions of the previewed space, as the list shows them (a finished, seen one has no state text). */
private val PreviewRows = listOf(
    PreviewRow(R.string.settings_preview_row1_title, Status.NeedsYou, R.string.settings_preview_row1_preview, R.string.settings_preview_row1_time),
    PreviewRow(R.string.settings_preview_row2_title, Status.Working, R.string.settings_preview_row2_preview, R.string.settings_preview_row2_time),
    PreviewRow(R.string.settings_preview_row3_title, Status.Idle, R.string.settings_preview_row3_preview, R.string.settings_preview_row3_time),
)

private data class PreviewRow(@StringRes val title: Int, val status: Status, @StringRes val preview: Int, @StringRes val time: Int)

/** What the previewed computer and space count: one session needs you, one works. */
private val PreviewCounts = listOf(Status.NeedsYou to 1, Status.Working to 1)

/**
 * The Agents tab at 0.3 of its size (320dp wide, near a phone's width), drawn with the style's own components
 * (docs/android/android-design.md 2.4): a computer, one of its spaces and its sessions on the page background (the board's
 * agent strips), and the same top navigation as the app. Glass shows its ambient light.
 */
@Composable
private fun StylePreview(style: AppStyle) {
    // A picture: screen readers skip it (the option says which style it is) and taps reach the option around it,
    // not the headings, rows and tabs drawn in it.
    Box(Modifier.size(96.dp, 132.dp).clip(RoundedCornerShape(14.dp.corner)).clearAndSetSemantics {}) {
        Box(Modifier.requiredSize(320.dp, 440.dp).graphicsLayer { scaleX = 0.3f; scaleY = 0.3f }) {
            PersonaTheme(darkTheme = PersonaTheme.darkTheme, style = style) {
                CompositionLocalProvider(LocalReadingInset provides 0.dp, LocalGlassSource provides null) {
                    val palette = PersonaTheme.palette
                    Box(Modifier.fillMaxSize().background(palette.background).consumeWindowInsets(WindowInsets.navigationBars)) {
                        if (style == AppStyle.Glass) AmbientLight(Ambience.NeedsYou)
                        Column(Modifier.fillMaxSize()) {
                            HomeNavigation(previewTabs(), "Agents", {}, leading = { PersonaFace(PersonaState.Idle) })
                            Column(Modifier.fillMaxSize().padding(horizontal = pagePadding).padding(top = Tokens.Space.l)) {
                            Text("Agents", style = PersonaTheme.type.largeTitle, color = palette.ink, maxLines = 1)
                            AgentTreeHeader(
                                "MacBook", folded = false, onToggle = {}, first = true, icon = PhIcons.Laptop, online = true,
                                detail = stringResource(R.string.settings_preview_detail), counts = PreviewCounts, onMonitor = {},
                            )
                            AgentSpaceHeader("weather", folded = false, onToggle = {}, first = true, counts = PreviewCounts)
                            PreviewRows.forEachIndexed { index, row ->
                                AgentRow(
                                    stringResource(row.title), row.status, onClick = {}, depth = 1, position = GroupPosition.of(index, PreviewRows.size),
                                    preview = stringResource(row.preview), time = stringResource(row.time), statusText = row.status.localizedLabel.takeIf { row.status != Status.Idle },
                                )
                            }
                            }
                        }
                    }
                }
            }
        }
        Box(Modifier.matchParentSize().pointerInput(Unit) {})
    }
}

/**
 * Settings › Appearance › Avatar (docs/apple/persona.md): the built-in faces, the default first, then the hub's packages (a package is
 * downloaded when chosen and kept on the phone), applied at once and remembered on this phone, with the chosen one
 * in each state under them. Demo mode has only the default.
 */
@Composable
fun PersonaSettings(demo: Boolean, modifier: Modifier = Modifier) {
    val appearance = LocalAppStyleStore.current ?: return
    val packs = LocalPersonaStore.current?.takeUnless { demo }
    val chosenId by appearance.personaId.collectAsState()
    val offered by (packs?.offered ?: remember { MutableStateFlow(emptyList()) }).collectAsState()
    val downloading by (packs?.downloading ?: remember { MutableStateFlow(emptySet()) }).collectAsState()
    val error by (packs?.error ?: remember { MutableStateFlow(null) }).collectAsState()
    val scope = rememberCoroutineScope()
    // Shown at once on tap: the choice may wait for a refresh already fetching thumbnails.
    var asked by remember { mutableStateOf<String?>(null) }
    // New from Photo (#2407): with a hub that makes avatars from images, never in demo mode.
    val canUpload by (packs?.canUpload ?: remember { MutableStateFlow(false) }).collectAsState()
    val uploaded by (packs?.uploaded ?: remember { MutableStateFlow(emptySet()) }).collectAsState()
    val context = LocalContext.current
    var upload by remember { mutableStateOf<AvatarUploadModel?>(null) }
    var opening by remember { mutableStateOf(false) }
    var uploadError by remember { mutableStateOf<UiText?>(null) }
    var deleting by remember { mutableStateOf<PersonaInfo?>(null) }
    var deletingNow by remember { mutableStateOf<String?>(null) }
    val pick = androidx.activity.compose.rememberLauncherForActivityResult(
        androidx.activity.result.contract.ActivityResultContracts.PickVisualMedia(),
    ) { uri ->
        if (uri == null) return@rememberLauncherForActivityResult
        opening = true
        uploadError = null
        scope.launch {
            try { upload = AvatarUploadModel(decodeAvatarImage(context.contentResolver, uri)) }
            catch (e: AvatarImageException) {
                uploadError = when (e.reason) {
                    AvatarImageException.Reason.TooSmall -> UiText.Res(R.string.settings_avatar_upload_too_small, listOf(AvatarImage.MIN_SIDE))
                    AvatarImageException.Reason.Unreadable -> UiText.Res(R.string.settings_avatar_upload_unreadable)
                    AvatarImageException.Reason.Unprepared -> UiText.Res(R.string.settings_avatar_upload_unprepared)
                }
            } catch (_: OutOfMemoryError) { uploadError = UiText.Res(R.string.settings_avatar_upload_unprepared) }
            finally { opening = false }
        }
    }
    LaunchedEffect(packs) { packs?.refresh() }
    val chosen = if (demo || (chosenId != PersonaDesign.Default.id && offered.none { it.id == chosenId }))
        PersonaDesign.Default.id else chosenId
    val pack = offered.firstOrNull { it.id == chosen }?.takeIf { packs?.onPhone(it) == true }
    val design = PersonaDesign.fromId(chosen)
    val palette = PersonaTheme.palette
    val language = AppLanguages.shown(LocalContext.current).tag ?: "en"
    Column(modifier.fillMaxWidth()) {
        Text(stringResource(if (demo) R.string.settings_avatar_demo else R.string.settings_avatar_intro),
            style = PersonaTheme.type.callout, color = palette.ink2)
        Spacer(Modifier.height(Tokens.Space.l))
        (if (demo) listOf(PersonaDesign.Default) else PersonaDesign.entries).forEach { d ->
            PersonaOption(d.title, d.summary, selected = d.id == chosen, tag = stringResource(R.string.settings_default).takeIf { d == PersonaDesign.Default },
                onClick = { if (!demo) packs?.choose(d) ?: appearance.setFace(d) }) {
                PersonaFace(PersonaState.Idle, size = Tokens.Space.xxl * 2, design = d, pack = null)
            }
            Spacer(Modifier.height(Tokens.Space.m))
        }
        if (packs != null) offered.forEach { p ->
            val busy = p.id in downloading || p.id == asked || p.id == deletingNow
            val from = p.manifest.source?.extensionName?.let { stringResource(R.string.settings_avatar_from_extension, it) }
            val made = p.id in uploaded || p.manifest.uploaded
            val name = p.manifest.displayName(language)
            val summary = if (made) p.manifest.subtitle?.takeIf { it.isNotBlank() } ?: stringResource(R.string.settings_avatar_from_image)
                else p.manifest.about ?: p.manifest.subtitle ?: from ?: stringResource(if (packs.onPhone(p)) R.string.settings_avatar_on_hub else R.string.settings_avatar_download)
            val deleteLabel = stringResource(R.string.settings_avatar_delete)
            PersonaOption(name, summary, selected = p.id == chosen,
                onClick = { if (!busy) scope.launch { asked = p.id; try { packs.choose(p) } finally { if (asked == p.id) asked = null } } },
                // TalkBack reaches Delete from the row too, as VoiceOver's Delete Avatar action on iOS.
                modifier = if (made) Modifier.semantics { customActions = listOf(CustomAccessibilityAction(deleteLabel) { deleting = p; true }) } else Modifier,
                trailing = if (made) ({
                    JIconButton(PhIcons.Trash, deleteLabel, { deleting = p }, Modifier.testTag("avatar-delete-${p.id}"), enabled = p.id != deletingNow)
                }) else null) {
                Box(contentAlignment = Alignment.Center) {
                    PackAvatar(packs, p, Tokens.Space.xxl * 2)
                    if (busy) JSpinner(Modifier.size(Tokens.Space.Standard.xl))
                }
            }
            Spacer(Modifier.height(Tokens.Space.m))
        }
        if (packs != null && canUpload) {
            NewFromPhotoOption(onClick = {
                pick.launch(androidx.activity.result.PickVisualMediaRequest(androidx.activity.result.contract.ActivityResultContracts.PickVisualMedia.ImageOnly))
            }, enabled = !opening && upload == null)
            Spacer(Modifier.height(Tokens.Space.s))
            Text(stringResource(R.string.settings_avatar_upload_footer), style = PersonaTheme.type.caption, color = palette.ink2)
            Spacer(Modifier.height(Tokens.Space.m))
        }
        error?.let { JNotice(it, status = Status.NeedsYou); Spacer(Modifier.height(Tokens.Space.m)) }
        uploadError?.let { JNotice(it.text(), Modifier.testTag("avatar-upload-page-error"), status = Status.NeedsYou); Spacer(Modifier.height(Tokens.Space.m)) }
        upload?.let { model ->
            AvatarUploadSheet(model, onSend = { scope.launch { if (packs != null && model.send(packs)) upload = null } },
                onDismiss = { if (!model.busy) upload = null })
        }
        deleting?.let { p ->
            JDialog(onDismiss = { deleting = null }, title = stringResource(R.string.settings_avatar_delete_title, p.manifest.displayName(language)),
                text = stringResource(R.string.settings_avatar_delete_text), destructive = true,
                confirm = stringResource(R.string.settings_avatar_delete) to {
                    deleting = null
                    if (packs != null) scope.launch {
                        deletingNow = p.id
                        uploadError = null
                        try {
                            uploadError = when (val outcome = packs.delete(p.id)) {
                                is LookUploadOutcome.Refused -> UiText.Plain(outcome.words)
                                LookUploadOutcome.Unreachable -> UiText.Res(R.string.settings_avatar_upload_unreachable)
                                else -> null
                            }
                        } finally { deletingNow = null }
                    }
                })
        }
        JSectionHeader(stringResource(R.string.settings_avatar_states, pack?.manifest?.displayName(language) ?: design.title))
        Row(
            Modifier.fillMaxWidth().clip(PersonaTheme.shapes.cardShape).background(palette.surface).padding(vertical = Tokens.Space.m)
                .semantics(mergeDescendants = true) {},
        ) {
            PersonaState.entries.forEach { state ->
                Column(Modifier.weight(1f), horizontalAlignment = Alignment.CenterHorizontally) {
                    PersonaFace(state, size = Tokens.Space.xxl + Tokens.Space.s, design = design, pack = pack)
                    Spacer(Modifier.height(Tokens.Space.xs))
                    Text(state.label, style = PersonaTheme.type.caption, color = palette.ink2, maxLines = 1)
                }
            }
        }
    }
}

/** A package's still face: from the package once on the phone, else the thumbnail the store fetched for this card. */
@Composable
private fun PackAvatar(store: PersonaStore, p: PersonaInfo, size: Dp) {
    val pictures by store.pictures.collectAsState()
    val pixels = with(LocalDensity.current) { size.roundToPx() }
    val dark = PersonaTheme.darkTheme
    val image by produceState<ImageBitmap?>(null, p.id, p.rev, pictures, dark) { value = store.avatar(p, pixels, dark)?.asImageBitmap() }
    Box(Modifier.size(size).clip(CircleShape).background(PersonaTheme.palette.surfaceStrong)) {
        image?.let { Image(it, contentDescription = null, Modifier.matchParentSize()) }
    }
}

@Composable
internal fun PersonaOption(
    title: String, summary: String, selected: Boolean, onClick: () -> Unit, tag: String? = null,
    /** False: an action rather than a choice (New from Photo). */
    radio: Boolean = true,
    modifier: Modifier = Modifier,
    /** Drawn before the radio: an uploaded avatar's delete button. */
    trailing: (@Composable () -> Unit)? = null,
    face: @Composable () -> Unit,
) {
    val palette = PersonaTheme.palette
    Row(
        modifier.option(selected, onClick, radio).padding(Tokens.Space.m),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        face()
        Spacer(Modifier.width(Tokens.Space.l))
        Column(Modifier.weight(1f)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text(title, style = PersonaTheme.type.title, color = palette.ink)
                tag?.let { Spacer(Modifier.width(Tokens.Space.s)); JTag(it) }
            }
            Spacer(Modifier.height(Tokens.Space.xs))
            Text(summary, style = PersonaTheme.type.callout, color = palette.ink2)
        }
        Spacer(Modifier.width(Tokens.Space.s))
        trailing?.invoke()
        if (radio) JRadio(selected, null)
    }
}

/**
 * "Theme color": "Match style" (the default), "Match avatar" (only while the chosen package brings colors, or while it is chosen), then
 * the five fixed accents, each with its swatch in this appearance. Choosing one drops an extension's role theme, which
 * would otherwise go over it.
 */
@Composable
private fun ThemeColorOptions(store: AppStyleStore) {
    val chosen by store.color.collectAsState()
    val extension by store.themeKey.collectAsState()
    val pack = LocalPersonaPack.current
    val personaAccent = ThemeColors.persona(pack?.manifest?.colors)
    val options = buildList {
        add(ThemeColor.Style)
        if (personaAccent != null || chosen == ThemeColor.Persona) add(ThemeColor.Persona)
        addAll(ThemeColor.presets)
    }
    val dark = PersonaTheme.darkTheme
    JSectionHeader(stringResource(R.string.settings_theme_color))
    Column {
        options.forEachIndexed { index, option ->
            val swatch = when (option) {
                ThemeColor.Style -> null
                ThemeColor.Persona -> personaAccent?.swatch(dark)
                else -> option.preset?.swatch(dark)
            }
            JGroupItem(GroupPosition.of(index, options.size)) {
                JRow(option.label, chevron = false,
                    subtitle = when (option) {
                        ThemeColor.Style -> stringResource(R.string.settings_theme_color_style_detail)
                        ThemeColor.Persona -> if (personaAccent == null) stringResource(R.string.settings_theme_color_persona_none)
                            else stringResource(R.string.settings_theme_color_persona_detail, pack?.manifest?.name.orEmpty())
                        else -> null
                    },
                    leading = { ColorSwatch(swatch) },
                    onClick = { store.setColor(option); store.setTheme(null) },
                    trailing = { JRadio(extension == null && option == chosen, null) })
            }
        }
    }
    SettingsCaption(stringResource(R.string.settings_theme_color_note), Modifier.padding(vertical = Tokens.Space.s))
}

/** A dot of the accent; the style's own is shown as the current accent, ringed, as "whatever the style has". */
@Composable
private fun ColorSwatch(color: androidx.compose.ui.graphics.Color?) {
    val palette = PersonaTheme.palette
    Box(Modifier.size(Tokens.Size.iconXS).clip(CircleShape)
        .background(color ?: palette.surfaceStrong)
        .border(Tokens.Space.Standard.xxs / 2, if (color == null) palette.accent else palette.line, CircleShape))
}

/** "Layout density": "Standard" (the default) or "Compact", which tightens the space around and between things (#5564). */
@Composable
private fun DensityOptions(store: AppStyleStore) {
    val density by store.density.collectAsState()
    JSectionHeader(stringResource(R.string.settings_density))
    JSegmented(LayoutDensity.entries.map { JSegment(it.label) }, LayoutDensity.entries.indexOf(density), { store.setDensity(LayoutDensity.entries[it]) })
    Spacer(Modifier.height(Tokens.Space.s))
    SettingsCaption(stringResource(R.string.settings_density_note))
}

/** "Appearance": "Match system" (the default), "Light", "Dark", on this phone. */
@Composable
private fun SchemeOptions(store: AppStyleStore) {
    val scheme by store.scheme.collectAsState()
    JSectionHeader(stringResource(R.string.settings_scheme))
    JSegmented(ColorSchemeChoice.entries.map { JSegment(it.label) }, ColorSchemeChoice.entries.indexOf(scheme), { store.setScheme(ColorSchemeChoice.entries[it]) })
    Spacer(Modifier.height(Tokens.Space.s))
    SettingsCaption(stringResource(R.string.settings_scheme_note))
}
