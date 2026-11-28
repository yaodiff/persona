import { IconActions } from '../design/IconActions';
import { Symbols } from '../design/Symbols';
import { NativeEnvironment, nativeEnvironment } from '../platform/NativeEnvironment';
import { common } from '@kit.AbilityKit';
import { preferences } from '@kit.ArkData';
import { i18n } from '@kit.LocalizationKit';
import { PhoneStyle } from '../design/PhoneStyle';
import { fileIo, picker } from '@kit.CoreFileKit';
import { image } from '@kit.ImageKit';
import { ComposeListItemV2, ContentItemV2, LengthMetrics } from '@kit.ArkUI';
import { ClientModel, ClientViewLease } from '../models/ClientModel';
import { InboxAttachmentDraft, InboxMailComposeSnapshot, InboxMailMessage, InboxMessage, InboxWorkspace, InboxListSection, InboxQuickFilter, inboxTier, inboxTierLabel, inboxLineKey, inboxRowSourceLabel } from '../models/InboxWorkspace';
import { ResourceRow, Wire } from '../models/Wire';
import { BoundRow, reconcileBoundRows } from '../models/RowBinding';
import { MarkdownText } from './MarkdownText';
import { NeedsYouPulseDot } from './StatusMotionMarks';
import { Colors } from '../design/Colors';
import { Motion } from '../design/Motion';
import { ButtonStyles, SelectStyles } from '../design/Controls';
import { Tokens } from '../design/Tokens';
import { tr } from '../design/Strings';
import { Haptics } from '../platform/Haptics';
import { workspaceGridColumns, workspaceGridGutter, workspaceHorizontalPadding } from '../core/WorkspaceGrid';
import { isHubPageHidden } from '../core/HubModules';
import { InboxKBChatLink, parseInboxKBChat } from '../core/KBChatInbox';

const MAX_IMAGE_PIXELS: number = 40000000;
const MAX_IMAGE_EDGE: number = 8192;
const MAX_UPLOAD_BYTES: number = 32 * 1024 * 1024;

// Grouping belongs to this device, independently of the Hub and assistant being viewed.
@ObservedV2
class InboxGroupingPreference {
  @Trace byTime: boolean = false;
  @Trace saving: boolean = false;
  private store: preferences.Preferences | undefined;
  load(context: common.Context): void {
    if (this.store) return;
    try {
      this.store = preferences.getPreferencesSync(context, {name: 'persona_inbox'});
      this.byTime = this.store.getSync('grouping', 'source') === 'time';
    } catch (_) { /* The default grouping remains usable. */ }
  }
  async choose(byTime: boolean): Promise<boolean> {
    const store = this.store;
    if (!store || this.saving) return false;
    if (byTime === this.byTime) return true;
    this.saving = true;
    try {
      store.putSync('grouping', byTime ? 'time' : 'source');
      await store.flush(); this.byTime = byTime; return true;
    } catch (_) {
      try { store.putSync('grouping', this.byTime ? 'time' : 'source'); } catch (_) {}
      return false;
    } finally { this.saving = false; }
  }
}
const inboxGrouping = new InboxGroupingPreference();

class PickedInboxFile {
  name: string = '';
  mime: string = 'application/octet-stream';
  kind: string = 'file';
  data: ArrayBuffer = new ArrayBuffer(0);
}

function inboxMime(name: string): string {
  const ext = name.split('.').pop()?.toLowerCase() || '';
  if (ext === 'jpg' || ext === 'jpeg') return 'image/jpeg';
  if (ext === 'png') return 'image/png';
  if (ext === 'webp') return 'image/webp';
  if (ext === 'gif') return 'image/gif';
  if (ext === 'pdf') return 'application/pdf';
  if (ext === 'txt' || ext === 'md') return 'text/plain';
  if (ext === 'json') return 'application/json';
  if (ext === 'zip') return 'application/zip';
  return 'application/octet-stream';
}

function inboxBytesText(size: number): string {
  if (size < 1024) return String(size) + ' B';
  if (size < 1024 * 1024) return (size / 1024).toFixed(1) + ' KB';
  return (size / (1024 * 1024)).toFixed(1) + ' MB';
}

@ComponentV2
export struct InboxRemoteImage {
  @Require @Param workspace: InboxWorkspace;
  @Require @Param media: string;
  @Param name: string = 'image';
  @Param expectedMime: string = '';
  @Local pixels: image.PixelMap | undefined;
  @Local loading: boolean = false;
  @Local failed: boolean = false;
  @Local full: boolean = false;
  @Local ratio: number = 1;
  private active: boolean = true;

  private async load(): Promise<void> {
    if (this.pixels || this.loading || !this.active) return;
    const target = this.workspace.selected.key();
    this.loading = true; this.failed = false;
    let source: image.ImageSource | undefined;
    let mapped: image.PixelMap | undefined;
    try {
      const content = await this.workspace.downloadMessageMedia(this.media, this.name, this.expectedMime);
      if (!this.active || target !== this.workspace.selected.key()) return;
      source = image.createImageSource(content.data);
      const info = await source.getImageInfo();
      if (info.size.width <= 0 || info.size.height <= 0 || info.size.width > MAX_IMAGE_EDGE || info.size.height > MAX_IMAGE_EDGE ||
        info.size.width * info.size.height > MAX_IMAGE_PIXELS) throw new Error('image_limit');
      const scale = Math.min(1, 1800 / Math.max(info.size.width, info.size.height));
      mapped = await source.createPixelMap({ desiredSize: { width: Math.max(1, Math.round(info.size.width * scale)), height: Math.max(1, Math.round(info.size.height * scale)) } });
      if (!this.active || target !== this.workspace.selected.key()) { await mapped.release(); mapped = undefined; return; }
      this.ratio = info.size.width / info.size.height; this.pixels = mapped; mapped = undefined;
    } catch (_) { if (this.active && target === this.workspace.selected.key()) this.failed = true; }
    finally { await mapped?.release(); await source?.release(); this.loading = false; }
  }

  aboutToDisappear(): void { this.active = false; this.pixels?.release(); this.pixels = undefined; }

  @Builder fullView() {
    Column() {
      Row() { Text(this.name).layoutWeight(1).maxLines(1); Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('close')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('close', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('close')).bindTips(tr('close')).onClick(() => this.full = false) }
        .padding(Tokens.page).width('100%')
      if (this.pixels) Image(this.pixels).objectFit(ImageFit.Contain).width('100%').layoutWeight(1)
    }.width('100%').height('100%').backgroundColor($r('app.color.persona_canvas'))
  }

  build() {
    Column() {
      if (this.pixels) Image(this.pixels).width('100%').aspectRatio(this.ratio).objectFit(ImageFit.Contain)
        .onClick(() => this.full = true).accessibilityText(this.name)
      else if (this.loading) LoadingProgress().width(Tokens.touch).height(Tokens.touch)
      else Button({ type: ButtonType.Circle }) { if (IconActions.busy(this.failed ? 'retry' : 'image')) LoadingProgress().width(Tokens.symbol).height(Tokens.symbol).color(Colors.link); else SymbolGlyph(IconActions.symbol(this.failed ? 'retry' : 'image')).fontSize(Tokens.symbolFont).fontColor([IconActions.color(this.failed ? 'retry' : 'image', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr(this.failed ? 'retry' : 'image')).bindTips(tr(this.failed ? 'retry' : 'image')).onClick(() => this.load())
    }.width('100%').padding({ top: Tokens.gap, bottom: Tokens.gap })
      .onVisibleAreaChange([0, 1], (visible: boolean, ratio: number) => { if (visible && ratio > 0 && !this.failed) this.load(); })
      .bindContentCover($$this.full, this.fullView())
  }
}

@ComponentV2
export struct InboxMessageParts {
  @Require @Param workspace: InboxWorkspace;
  @Require @Param parts: Record<string, Object>[];
  @Event onLink: (address: string) => void = (_address: string) => {};
  @Event onSaveMedia: (media: string, name: string, mime: string) => void = (_media: string, _name: string, _mime: string) => {};

  private line(part: Record<string, Object>): string {
    const kind = Wire.text(part, 'kind');
    if (kind === 'emoji') return Wire.text(part, 'text', Wire.text(part, 'key'));
    if (kind === 'file') return Wire.text(part, 'name', tr('file'));
    if (kind === 'card') return Wire.text(part, 'fallback', tr('inbox_card_unsupported'));
    if (kind === 'share') {
      const fields = Wire.rows(part, 'fields').map((item: Record<string, Object>) => Wire.text(item, 'label') + ': ' + Wire.text(item, 'value'));
      return [Wire.text(part, 'title'), Wire.text(part, 'subtitle')].filter((value: string) => !!value).concat(fields).join('\n');
    }
    if (kind === 'forward') {
      const previews = Wire.rows(part, 'previews');
      const messages = Wire.rows(part, 'messages');
      const rows = previews.length ? previews : messages;
      const text = rows.map((item: Record<string, Object>) => {
        const sender = Wire.text(item, 'sender_name', Wire.text(Wire.object(item, 'sender'), 'name'));
        return (sender ? sender + ': ' : '') + Wire.text(item, 'text');
      }).filter((value: string) => !!value);
      return Wire.text(part, 'title') + (text.length ? '\n' + text.join('\n') : '');
    }
    const block = Wire.text(part, 'block'), list = Wire.object(part, 'list');
    const prefix = block === 'list_item' ? Wire.bool(list, 'ordered') ? String(Wire.number(list, 'start', 1)) + '. ' : '• ' : block === 'quote' ? '› ' : '';
    return prefix + Wire.text(part, 'text');
  }

  private partWeight(part: Record<string, Object>): FontWeight { return Wire.strings(part, 'marks').includes('bold') ? FontWeight.Bold : FontWeight.Normal; }
  private partStyle(part: Record<string, Object>): FontStyle { return Wire.strings(part, 'marks').includes('italic') ? FontStyle.Italic : FontStyle.Normal; }

  @Builder part(part: Record<string, Object>) {
    if (Wire.text(part, 'kind') === 'divider') Divider().strokeWidth(Tokens.hairline).width('100%')
    else if (Wire.text(part, 'kind') === 'image' || Wire.text(part, 'kind') === 'sticker') {
      if (Wire.text(part, 'media')) InboxRemoteImage({ workspace: this.workspace, media: Wire.text(part, 'media'), name: Wire.text(part, 'name', tr('image')), expectedMime: Wire.text(part, 'mime') })
      else if (Wire.text(part, 'text')) Text(Wire.text(part, 'text')).width('100%')
    } else if (Wire.text(part, 'kind') === 'file' || Wire.text(part, 'kind') === 'video' || Wire.text(part, 'kind') === 'audio') {
      Row({ space: Tokens.gap }) {
        Text(this.line(part)).layoutWeight(1).wordBreak(WordBreak.BREAK_WORD)
        if (Wire.text(part, 'media')) Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('download')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('download', true)]) }.attributeModifier(ButtonStyles.iconPrimary).accessibilityText(tr('save')).bindTips(tr('save')).onClick(() => this.onSaveMedia(Wire.text(part, 'media'), Wire.text(part, 'name', Wire.text(part, 'kind')), Wire.text(part, 'mime')))
      }.width('100%').alignItems(VerticalAlign.Center)
    } else if ((Wire.text(part, 'kind') === 'link' || Wire.text(part, 'kind') === 'text') && Wire.text(part, 'url')) {
      Text(this.line(part)).fontColor($r('sys.color.font_emphasize')).decoration({ type: TextDecorationType.Underline, color: $r('sys.color.font_emphasize') })
        .fontWeight(this.partWeight(part)).fontStyle(this.partStyle(part)).width('100%').onClick(() => this.onLink(Wire.text(part, 'url')))
    } else if (Wire.text(part, 'kind') === 'code') {
      Text(this.line(part)).fontFamily('monospace').fontSize(Tokens.meta).copyOption(CopyOptions.LocalDevice).width('100%')
        .padding(Tokens.inset).backgroundColor($r('sys.color.comp_background_tertiary'))
    } else if (Wire.text(part, 'kind') === 'share' && Wire.text(part, 'url')) {
      Column({ space: Tokens.gap }) {
        if (this.line(part)) Text(this.line(part)).fontSize(Tokens.body).width('100%').copyOption(CopyOptions.LocalDevice)
        Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('open')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('open', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('open')).bindTips(tr('open')).onClick(() => this.onLink(Wire.text(part, 'url')))
      }.width('100%').alignItems(HorizontalAlign.Start)
    } else if (Wire.text(part, 'kind') === 'system') {
      Text(this.line(part)).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).width('100%').textAlign(TextAlign.Center)
    } else if (this.line(part)) {
      Text(this.line(part)).fontSize(Tokens.body).fontColor($r('sys.color.font_primary')).wordBreak(WordBreak.BREAK_WORD)
        .fontWeight(this.partWeight(part)).fontStyle(this.partStyle(part)).copyOption(CopyOptions.LocalDevice).width('100%')
    }
  }

  build() {
    Column({ space: Tokens.gap }) {
      ForEach(this.parts, (part: Record<string, Object>) => this.part(part), (part: Record<string, Object>, index: number) =>
        String(index) + ':' + Wire.text(part, 'kind') + ':' + Wire.text(part, 'block_id'))
    }.width('100%').alignItems(HorizontalAlign.Start)
  }
}

@ObservedV2
class BoundInboxSection {
  key:string;
  tier:string;
  sourceGroup:string;
  @Trace count:number=0;
  @Trace rows:BoundRow<ResourceRow>[] = [];
  constructor(tier:string,sourceGroup:string) {
    this.tier=tier;this.sourceGroup=sourceGroup;this.key=sourceGroup ? tier+'/'+sourceGroup : tier;
  }
}

@ComponentV2
export struct InboxWorkspacePage {
  @Require @Param model: ClientModel;
  @Param row: ResourceRow | undefined = undefined;
  @Param list: boolean = true;
  @Param phoneList: boolean = false;
  @Param visible: boolean = true;
  @Param handoffRequested: boolean = false;
  @Event onHandoffConsumed: () => void = (): void => {};
  @Param refreshRevision: number = 0;
  @Param composeRevision: number = 0;
  @Param toolbarActions: boolean = false;
  @Event onComposeAvailability:(enabled:boolean)=>void=()=>{};
  @Event onChanged: () => void = (): void => {};
  @Event onOpen: (row: ResourceRow) => void = (_row: ResourceRow) => {};
  @Event onOpenResource: (row: ResourceRow) => void = (_row: ResourceRow) => {};
  @Event onHandoff: (row: ResourceRow) => void = (_row: ResourceRow) => {};
  @Event onLink: (address: string) => void = (_address: string) => {};
  @Local environment: NativeEnvironment = nativeEnvironment;
  @Local private paneWidth: number = 0;
  @Local workspace: InboxWorkspace = new InboxWorkspace();
  @Local grouping: InboxGroupingPreference = inboxGrouping;
  @Local itemRowBindings: BoundRow<ResourceRow>[] = [];
  @Local sections: BoundInboxSection[] = [];
  @Local choices: InboxQuickFilter[] = [];
  @Local expandedTiers: string[] = [];
  @Local collapsedSources:string[] = [];
  private foldRevision:number=0;
  private foldLease:ClientViewLease|undefined;
  private rowBindingOwner: Object | undefined;
  private rowBindingScope: string = '';
  @Local detailOpen: boolean = false;
  @Local sheetOpen: boolean = false;
  @Local activeSheet: string = '';
  @Local handoffText: string = '';
  @Local pickerBusy: boolean = false;
  @Local mediaBusy: boolean = false;
  @Local error: string = '';
  private active: boolean = false;
  private pendingHandoff: boolean = false;
  private handoffLoaded: boolean = false;
  private stopEvents: (() => void) | undefined;

  aboutToAppear(): void {
    if (this.list) this.grouping.load(this.uiContext());
    this.active = true; this.pendingHandoff = this.handoffRequested; this.handoffLoaded = false;
    this.stopEvents = this.model.observeEvents((payload: string): void => {
      if (!this.active || !this.visible || !this.list) return;
      try { if (Wire.text(Wire.parse(payload), 'type') === 'inbox') this.workspace.refresh().catch(() => undefined); } catch (_) {}
    });
    this.detailOpen = !this.list && !!this.row;
    this.syncItemRows();
    this.workspace.open(this.model, this.row).then(() => {
      if (this.active && this.row && this.list === false) this.detailOpen = true;
      this.syncItemRows(); this.handoffLoaded = true; this.consumeHandoff();
    });
  }

  aboutToDisappear(): void { this.active = false; this.stopEvents?.(); this.stopEvents = undefined; this.workspace.close(); }

  @Monitor('model.connected') connectionChanged(): void {
    if (!this.active) return;
    if (!this.model.connected) this.workspace.onDisconnected();
    else this.workspace.reconnect(this.row);
  }

  @Monitor('model.profile') profileChanged(): void {
    if (!this.active) return;
    this.syncItemRows();
    this.workspace.close().then(() => {
      if (this.active) this.workspace.open(this.model, this.row).then(() => this.syncItemRows());
    });
  }

  private consumeHandoff(): void {
    if (this.handoffRequested && this.pendingHandoff && this.handoffLoaded && this.active && this.visible && this.row && this.workspace.selected.key() === this.row.key() && !this.workspace.isReference()) { this.pendingHandoff = false; this.onHandoffConsumed(); this.openHandoff(); }
  }
  @Monitor('handoffRequested') handoffRequestedChanged(): void { this.pendingHandoff = this.handoffRequested; if (this.pendingHandoff) this.consumeHandoff(); }
  @Monitor('model.inbox', 'refreshRevision', 'visible') listRefreshRequested(): void {
    this.consumeHandoff();
    if (this.active && this.visible && this.list) this.workspace.refresh().catch(() => undefined);
  }
  @Monitor('composeRevision') composeRequested():void {
    if (this.active && this.visible && this.list && !this.sheetOpen && this.model.mailModuleAllowed() && this.workspace.canComposeMail()) this.openMailCompose();
  }
  @Monitor('visible','model.connected','model.features','workspace.filterAddresses','workspace.writeState','workspace.handoffBusy','workspace.attachmentBusy') composeAvailabilityChanged():void {
    if (this.active && this.visible && this.list) this.onComposeAvailability(this.model.mailModuleAllowed() && this.workspace.canComposeMail());
  }
  private acknowledged(changed: boolean): void { if (changed && this.active && this.visible) this.onChanged(); }

  @Monitor('grouping.byTime', 'workspace.items', 'workspace.search', 'workspace.sourceFilter', 'workspace.accountFilter', 'workspace.kindFilter','workspace.statusFilter','workspace.doneItems','workspace.haveDoneDays','workspace.waitingAgents') itemRowsChanged(): void {
    this.syncItemRows();
  }

  private syncItemRows(): void {
    const scope = JSON.stringify([this.model.scope(), this.model.profile.id, this.model.profile.manager]);
    const view=this.workspace.listView(tr('phone_inbox_mention'),(row:ResourceRow) => this.requestLabel(row), this.grouping.byTime);
    const same=this.model === this.rowBindingOwner && scope === this.rowBindingScope;
    const previous=same ? this.sections : [];
    if (!same) this.restoreFolds();
    this.choices=view.choices;
    const rows=view.sections.flatMap((section:InboxListSection) => section.rows);
    this.itemRowBindings = reconcileBoundRows(this.itemRowBindings, rows, (row: ResourceRow) => inboxLineKey(row),
      this.rowBindingOwner, this.model, this.rowBindingScope, scope);
    const bindings=new Map<string,BoundRow<ResourceRow>>();
    this.itemRowBindings.forEach((b:BoundRow<ResourceRow>) => bindings.set(inboxLineKey(b.value),b));
    const next:BoundInboxSection[]=[];
    for (const section of view.sections) {
      const top=previous.find((s:BoundInboxSection) => s.key===section.key) || new BoundInboxSection(section.key,'');
      top.count=section.rows.length;
      top.rows=section.groups.length ? [] : section.rows.map((row:ResourceRow) => bindings.get(inboxLineKey(row))!);
      next.push(top);
      for (const group of section.groups) {
        const sub=previous.find((s:BoundInboxSection) => s.tier===section.key && s.sourceGroup===group.key) || new BoundInboxSection(section.key,group.key);
        sub.count=group.rows.length;sub.rows=group.rows.map((row:ResourceRow) => bindings.get(inboxLineKey(row))!);next.push(sub);
      }
    }
    this.sections=next;
    this.rowBindingOwner = this.model; this.rowBindingScope = scope;
  }

  @Monitor('sheetOpen') sheetChanged(): void {
    if (this.sheetOpen) return;
    if (this.activeSheet === 'mail') this.workspace.closeMailCompose();
    this.activeSheet = '';
  }

  private uiContext(): common.UIAbilityContext { return this.getUIContext().getHostContext() as common.UIAbilityContext; }
  private optionList(values: string[]): SelectOption[] { return values.map((value: string): SelectOption => ({ value: value })); }
  private sourceIndex(): number { return Math.max(0, this.workspace.sourceOptions.indexOf(this.workspace.sourceFilter)); }
  private accountIndex(): number { return Math.max(0, this.workspace.accountOptions.indexOf(this.workspace.accountFilter)); }
  private mailAccountIndex(): number { return Math.max(0, this.workspace.mailAccounts.findIndex((item: Record<string, Object>) => Wire.text(item, 'id') === this.workspace.mailComposeAccount)); }
  private sourceLabel(value: string): string { return tr(this.workspace.sourceName(value)); }
  private doneLabel(row: ResourceRow): string { return this.workspace.isHandled(row) ? tr('done') : tr('inbox_open'); }
  private rowSource(row: ResourceRow): string {
    const data = row.data();
    return inboxRowSourceLabel(data, this.sourceLabel(Wire.text(data, 'source')));
  }
  private openListHandoff(row: ResourceRow): void {
    const copy = ResourceRow.from(row.data(), row.kind); copy.id = row.id; copy.title = row.title; copy.manager = row.manager; copy.group = row.group; copy.path = row.path;
    const data = row.data(); data['open_handoff'] = true; copy.raw = JSON.stringify(data); this.onOpen(copy);
  }
  private openRow(row: ResourceRow): void {
    Haptics.tap();
    this.error = '';
    this.onOpen(row);
  }
  private inboxColumns(): number {
    return workspaceGridColumns(this.paneWidth, this.environment.fontScale, 2);
  }
  private inboxSidePadding(): number {
    return workspaceHorizontalPadding(this.paneWidth);
  }
  private rowNeedsPulse(row: ResourceRow): boolean {
    if (row.kind === 'session') return true;
    const data = row.data();
    return !this.workspace.isHandled(row) && (Wire.bool(data, 'urgent') || inboxTier(data) === 'action');
  }
  private async refreshList(): Promise<void> { await this.workspace.refresh(); }
  private openMailCompose(): void {
    if (!this.model.mailModuleAllowed()) return;
    this.activeSheet = 'mail'; this.sheetOpen = true; this.workspace.openMailCompose();
  }
  private openHandoff(): void { if (!this.workspace.canWrite()) return; this.handoffText = this.workspace.handoffDraft || tr('inbox_handoff_preset_summary'); this.activeSheet = 'handoff'; this.sheetOpen = true; }
  private chain(): Record<string, Object>[] { return Wire.rows(this.workspace.detail, 'chain'); }
  private handling(): Record<string, Object> { return Wire.object(this.workspace.detail, 'handling'); }
  private suggestion(): Record<string, Object> { return Wire.object(this.workspace.detail, 'suggestion'); }
  private connector(): Record<string, Object> { return Wire.object(this.workspace.detail, 'connector'); }
  private quote(message: InboxMessage): Record<string, Object> { return Wire.object(message.data(), 'reply_to'); }
  private hasQuote(message: InboxMessage): boolean { return Object.keys(this.quote(message)).length > 0; }
  private messageParts(message: InboxMessage): Record<string, Object>[] { return Wire.rows(message.data(), 'parts'); }
  private messageDecorations(message: InboxMessage): Record<string, Object>[] { return Wire.rows(message.data(), 'decorations'); }
  private messageReactions(message: InboxMessage): Record<string, Object>[] { return Wire.rows(message.data(), 'reactions'); }
  private messageThread(message: InboxMessage): Record<string, Object> { return Wire.object(message.data(), 'thread'); }
  private messageHistory(message: InboxMessage): Record<string, Object>[] { return Wire.rows(message.data(), 'edit_history'); }
  private canOpenMessageThread(message: InboxMessage): boolean {
    return this.workspace.isTopicList ? this.workspace.capability('topics') || this.workspace.isLark() :
      this.workspace.capability('threads') || this.workspace.isLark();
  }

  private confirmReply(): void {
    const value = this.workspace.replyDraft.trim();
    if (!value || !this.workspace.canReplyToItem() || !this.workspace.canWrite()) return;
    const fingerprint = this.workspace.replyConfirmationSnapshot(value);
    const message = this.workspace.replyAccountLabel() + '\n' + this.workspace.replyRecipient() + '\n\n' + value;
    this.getUIContext().showAlertDialog({ title: tr('confirm_send'), message: message,
      primaryButton: { value: tr('cancel'), action: () => {} },
      secondaryButton: { value: tr('send'), action: () => {
        if (fingerprint === this.workspace.replyConfirmationSnapshot(value)) this.workspace.replyInbox(value, fingerprint).then((changed: boolean) => this.acknowledged(changed));
      } } });
  }

  private confirmConversationSend(): void {
    const value = this.workspace.replyDraft.trim();
    if (!value || !this.workspace.canSendGeneric()) return;
    const fingerprint = this.workspace.replyConfirmationSnapshot(value);
    const destination = this.workspace.replyAccountLabel() + '\n' + this.workspace.replyRecipient() + '\n\n' + value;
    this.getUIContext().showAlertDialog({ title: tr('confirm_send'), message: destination,
      primaryButton: { value: tr('cancel'), action: () => {} },
      secondaryButton: { value: tr('send'), action: () => {
        if (fingerprint === this.workspace.replyConfirmationSnapshot(value))
          this.workspace.sendConversation(value, this.workspace.threadReplyTo, this.workspace.threadReply).then((changed: boolean) => this.acknowledged(changed));
      } } });
  }

  private confirmAttachmentSend(): void {
    const item = this.workspace.attachment;
    if (!item || !this.workspace.canSendGeneric()) return;
    const fingerprint = this.workspace.replyConfirmationSnapshot('attachment:' + item.name + ':' + item.mime + ':' + String(item.size));
    const message = this.workspace.replyAccountLabel() + '\n' + this.workspace.replyRecipient() + '\n' + item.name + ' · ' + inboxBytesText(item.size);
    this.getUIContext().showAlertDialog({ title: tr('confirm_send'), message: message,
      primaryButton: { value: tr('cancel'), action: () => {} },
      secondaryButton: { value: tr('send'), action: () => {
        if (fingerprint === this.workspace.replyConfirmationSnapshot('attachment:' + item.name + ':' + item.mime + ':' + String(item.size)) &&
          this.workspace.attachment === item) this.workspace.sendAttachment().then((changed: boolean) => this.acknowledged(changed));
      } } });
  }

  private confirmHandoff(): void {
    const text = this.handoffText.trim(), row = this.workspace.selected;
    if (!text || !row.id) return;
    const fingerprint = this.workspace.replyConfirmationSnapshot('handoff:' + text);
    this.getUIContext().showAlertDialog({ title: tr('inbox_handoff_title'), message: row.title + '\n' + row.manager + '\n\n' + text,
      primaryButton: { value: tr('cancel'), action: () => {} },
      secondaryButton: { value: tr('send'), action: () => {
        if (fingerprint !== this.workspace.replyConfirmationSnapshot('handoff:' + text)) return;
        this.workspace.handoff(text).then((target: ResourceRow | undefined) => {
          if (this.active && row.key() === this.workspace.selected.key() && this.workspace.writeState === 'handed_off') {
            this.sheetOpen = false; this.acknowledged(true);
            if (target) this.onHandoff(target);
          }
        });
      } } });
  }

  private confirmCompose(): void {
    const snapshot = this.workspace.mailComposeSnapshot();
    if (!this.workspace.mailComposeValid(snapshot)) return;
    const confirmation = this.workspace.mailComposeConfirmation(snapshot);
    const message = this.workspace.mailAccountName(snapshot.accountId) + '\n' +
      tr('to') + ': ' + this.workspace.mailComposeRecipients(snapshot) +
      (snapshot.cc ? '\n' + tr('cc') + ': ' + this.workspace.mailComposeCcRecipients(snapshot) : '') +
      (snapshot.bcc ? '\n' + tr('bcc') + ': ' + this.workspace.mailComposeBccRecipients(snapshot) : '') +
      '\n' + tr('subject') + ': ' + snapshot.subject + '\n\n' + snapshot.text;
    this.getUIContext().showAlertDialog({ title: tr('confirm_send'), message: message,
      primaryButton: { value: tr('cancel'), action: () => {} },
      secondaryButton: { value: tr('send'), action: () => {
        this.workspace.sendMailCompose(snapshot, confirmation).then((sent: boolean) => { if (sent) { this.sheetOpen = false; this.acknowledged(true); } });
      } } });
  }

  private async pickConversationAttachment(): Promise<void> {
    this.pickerBusy = true; this.error = '';
    const scope = this.model.scope(), target = this.workspace.selected.key();
    let source: image.ImageSource | undefined;
    try {
      const options = new picker.DocumentSelectOptions(); options.maxSelectNumber = 1;
      const uris = await new picker.DocumentViewPicker(this.uiContext()).select(options);
      if (!uris.length) return;
      if (scope !== this.model.scope() || target !== this.workspace.selected.key()) throw new Error('request_updated');
      const uri = uris[0], info = fileIo.statSync(uri);
      if (!info.size || info.size > MAX_UPLOAD_BYTES) throw new Error('attachment_limit');
      const file = fileIo.openSync(uri, fileIo.OpenMode.READ_ONLY);
      let data = new ArrayBuffer(0);
      try { data = new ArrayBuffer(info.size); if (fileIo.readSync(file.fd, data) !== info.size) throw new Error('file_read_failed'); }
      finally { fileIo.closeSync(file.fd); }
      const leaf = uri.substring(uri.lastIndexOf('/') + 1);
      const picked = new PickedInboxFile();
      try { picked.name = decodeURIComponent(leaf); } catch (_) { picked.name = leaf; }
      if (!picked.name) picked.name = 'attachment';
      picked.mime = inboxMime(picked.name); picked.kind = picked.mime.startsWith('image/') ? 'image' : 'file'; picked.data = data;
      if (picked.kind === 'image') {
        if (!/^image\/(jpeg|png|webp|gif)$/.test(picked.mime) || data.byteLength > 7.5 * 1024 * 1024) throw new Error('attachment_limit');
        source = image.createImageSource(data);
        const dimensions = await source.getImageInfo();
        if (scope !== this.model.scope() || target !== this.workspace.selected.key()) throw new Error('request_updated');
        if (dimensions.size.width <= 0 || dimensions.size.height <= 0 || dimensions.size.width > MAX_IMAGE_EDGE || dimensions.size.height > MAX_IMAGE_EDGE ||
          dimensions.size.width * dimensions.size.height > MAX_IMAGE_PIXELS) throw new Error('attachment_limit');
      }
      this.workspace.setAttachment(new InboxAttachmentDraft(picked.name, picked.mime, picked.kind, picked.data));
    } catch (failure) { this.error = (failure as Error).message || 'failed'; }
    finally { await source?.release(); this.pickerBusy = false; }
  }

  private async saveMailAttachment(message: InboxMailMessage, attachment: Record<string, Object>): Promise<void> {
    this.mediaBusy = true; this.error = '';
    const scope = this.model.scope(), target = this.workspace.selected.key();
    try {
      const downloaded = await this.workspace.downloadMailAttachment(message, attachment);
      if (scope !== this.model.scope() || target !== this.workspace.selected.key()) throw new Error('request_updated');
      const options = new picker.DocumentSaveOptions(); options.newFileNames = [downloaded.name];
      const uris = await new picker.DocumentViewPicker(this.uiContext()).save(options);
      if (!uris.length) return;
      if (scope !== this.model.scope() || target !== this.workspace.selected.key()) throw new Error('request_updated');
      const output = fileIo.openSync(uris[0], fileIo.OpenMode.READ_WRITE);
      try { if (fileIo.writeSync(output.fd, downloaded.data) !== downloaded.data.byteLength) throw new Error('file_write_failed'); }
      finally { fileIo.closeSync(output.fd); }
    } catch (failure) { this.error = (failure as Error).message || 'failed'; }
    finally { this.mediaBusy = false; }
  }

  private async saveMessageMedia(media: string, name: string, mime: string): Promise<void> {
    this.mediaBusy = true; this.error = '';
    const scope = this.model.scope(), target = this.workspace.selected.key();
    try {
      const downloaded = await this.workspace.downloadMessageMedia(media, name, mime);
      if (scope !== this.model.scope() || target !== this.workspace.selected.key()) throw new Error('request_updated');
      const options = new picker.DocumentSaveOptions(); options.newFileNames = [downloaded.name];
      const uris = await new picker.DocumentViewPicker(this.uiContext()).save(options);
      if (!uris.length) return;
      if (scope !== this.model.scope() || target !== this.workspace.selected.key()) throw new Error('request_updated');
      const output = fileIo.openSync(uris[0], fileIo.OpenMode.READ_WRITE);
      try { if (fileIo.writeSync(output.fd, downloaded.data) !== downloaded.data.byteLength) throw new Error('file_write_failed'); }
      finally { fileIo.closeSync(output.fd); }
    } catch (failure) { this.error = (failure as Error).message || 'failed'; }
    finally { this.mediaBusy = false; }
  }

  @Builder inboxSearch(weighted: boolean) {
    Search({ value: this.workspace.search, placeholder: tr('phone_inbox_search') }).maxFontScale(Tokens.interfaceFontMaxScale)
      .onChange((value: string) => this.workspace.setSearch(value)).id('inbox.search').width('100%').layoutWeight(weighted ? 1 : 0)
  }
  @Builder inboxActions() {
    Badge({ count: (this.workspace.sourceFilter !== 'all' ? 1 : 0) + (this.workspace.accountFilter !== 'all' ? 1 : 0),
      style: { color: Colors.onAction, badgeColor: Colors.action, borderWidth: 0, borderColor: Color.Transparent, badgeSize: Tokens.body, fontSize: Tokens.caption } }) {
      Button({ type: ButtonType.Circle }) { SymbolGlyph(Symbols.filter).fontSize(Tokens.symbolFont).fontColor([Colors.link]) }
        .attributeModifier(ButtonStyles.chrome).accessibilityText(tr('agent_filter_button') + ' · ' + this.sourceLabel(this.workspace.sourceFilter) + ' · ' + (this.workspace.accountFilter === 'all' ? tr('all') : this.workspace.accountName(this.workspace.accountFilter)))
        .bindMenu(this.inboxFilters).id('inbox.filters')
    }
  }
  @Builder inboxFilters() {
    Menu() {
      MenuItemGroup({ header: tr('source') }) {
        ForEach(this.workspace.sourceOptions, (value: string) => {
          MenuItem({ content: this.sourceLabel(value) }).selected(this.workspace.sourceFilter === value).selectIcon(true)
            .onClick(() => this.workspace.sourceFilter = value)
        }, (value: string) => value)
      }
      MenuItemGroup({ header: tr('account') }) {
        ForEach(this.workspace.accountOptions, (value: string) => {
          MenuItem({ content: value === 'all' ? tr('all') : this.workspace.accountName(value) }).selected(this.workspace.accountFilter === value).selectIcon(true)
            .onClick(() => this.workspace.accountFilter = value)
        }, (value: string) => value)
      }
      if (!this.toolbarActions) {
        MenuItem({content:tr('refresh')}).enabled(!this.workspace.loading).onClick(() => this.refreshList())
        if (this.model.mailModuleAllowed()) MenuItem({content:tr('compose')}).enabled(this.workspace.canComposeMail()).onClick(() => this.openMailCompose())
      }
    }.width(Tokens.popupWidth)
  }

  @Builder sourceFilterControl() {
    Select(this.optionList(this.workspace.sourceOptions.map((value: string) => this.sourceLabel(value)))).attributeModifier(SelectStyles.inline)
      .selected(this.sourceIndex()).value(this.sourceLabel(this.workspace.sourceFilter)).onSelect((index: number) => this.workspace.sourceFilter = this.workspace.sourceOptions[index])
      .constraintSize({ minHeight: Tokens.touch }).width('100%')
  }
  @Builder accountFilterControl() {
    Select(this.optionList(this.workspace.accountOptions.map((value: string) => value === 'all' ? tr('all') : this.workspace.accountName(value)))).attributeModifier(SelectStyles.inline)
      .selected(this.accountIndex()).value(this.workspace.accountFilter === 'all' ? tr('all') : this.workspace.accountName(this.workspace.accountFilter)).onSelect((index: number) => this.workspace.accountFilter = this.workspace.accountOptions[index])
      .constraintSize({ minHeight: Tokens.touch }).width('100%')
  }
  private pickChoice(key:string): void {
    Haptics.selection();
    Motion.run(this.getUIContext(), Motion.state(this.environment.reduceMotion), () => {
      this.workspace.statusFilter=key;
    });
    if (key === 'done' && this.workspace.haveDoneDays && !this.workspace.doneItems.length && !this.workspace.historyEnded) this.workspace.loadOlderDone();
  }
  private async chooseGrouping(byTime: boolean): Promise<void> {
    Haptics.selection();
    if (!await this.grouping.choose(byTime)) this.workspace.error = 'cache_unavailable';
  }
  @Builder groupingOptions() {
    Menu() {
      MenuItemGroup({header:tr('grouping')}) {
        MenuItem({content:tr('phone_inbox_by_source')}).selectIcon(true).selected(!this.grouping.byTime)
          .onClick(() => this.chooseGrouping(false)).id('inbox.group.source')
        MenuItem({content:tr('phone_inbox_by_time')}).selectIcon(true).selected(this.grouping.byTime)
          .onClick(() => this.chooseGrouping(true)).id('inbox.group.time')
      }
    }
  }
  @Builder groupingButton() {
    Button({type:ButtonType.Circle}) {
      Column({space:3}) {
        ForEach([17,12,7],(size:number) => {
          Rect().width(PhoneStyle.sidebarMetric(size,this.environment.fontScale)).height(1.6).radius(0.8).fill(Colors.ink)
        },(size:number) => String(size))
      }
    }.width(PhoneStyle.sidebarMetric(44,this.environment.fontScale)).height(PhoneStyle.sidebarMetric(44,this.environment.fontScale))
      .padding(0).backgroundColor(Color.Transparent).enabled(!this.grouping.saving)
      .accessibilityText(tr('phone_inbox_grouping_mode').replace('{name}',tr(this.grouping.byTime ? 'phone_inbox_by_time' : 'phone_inbox_by_source')))
      .bindMenu(this.groupingOptions).id('inbox.grouping')
  }
  private countInk(key: string): ResourceColor {
    if (Colors.systemControls) return key === 'action' ? $r('app.color.phone_filter_count_ink') : Color.White;
    return this.workspace.statusFilter === key ? Colors.selectedFilterInk : key === 'action' ? Colors.needs : Colors.secondary;
  }
  private countFill(key: string): ResourceColor {
    if (!Colors.systemControls) return Color.Transparent;
    if (key !== 'action') return $r('app.color.phone_filter_neutral_count');
    return this.workspace.statusFilter === key ? $r('app.color.phone_filter_count_fill') : $r('app.color.phone_filter_idle_count_fill');
  }
  @Builder choiceBar() {
    Scroll() {
      Row({space:4}) {
        ForEach(this.choices,(choice:InboxQuickFilter) => {
          Button({type:ButtonType.Capsule}) {
            Row({space:5}) {
              Text(tr(inboxTierLabel(choice.key))).fontSize(14).maxFontScale(Tokens.navigationTextMaxScale).maxLines(1)
                .fontWeight(!Colors.systemControls && this.workspace.statusFilter === choice.key ? FontWeight.Bold : FontWeight.Medium)
                .fontColor(this.workspace.statusFilter === choice.key ? Colors.selectedFilterInk : Colors.ink).id('inbox.choice.label.' + choice.key)
              if (choice.count && ['action','working','fyi'].includes(choice.key)) Text(choice.count > 99 ? '99+' : String(choice.count)).fontSize(Tokens.typeLabel)
                .maxFontScale(Tokens.navigationTextMaxScale).maxLines(1).fontWeight(FontWeight.Bold)
                .fontColor(this.countInk(choice.key))
                .textAlign(TextAlign.Center).padding({left:6,right:6}).constraintSize({minWidth:PhoneStyle.formSize(20.3,23,this.environment.fontScale)})
                .height(PhoneStyle.formSize(17.7,25.7,this.environment.fontScale))
                .backgroundColor(this.countFill(choice.key)).borderRadius(Tokens.radiusCapsule)
            }.padding({left:8,right:8}).height(PhoneStyle.formSize(26.3,33.7,this.environment.fontScale)).borderRadius(Tokens.radiusCapsule)
              .backgroundColor(this.workspace.statusFilter === choice.key ? Colors.selectedFilterFill : Color.Transparent)
              .animation(Motion.state(this.environment.reduceMotion))
          }.height(PhoneStyle.sidebarMetric(44,this.environment.fontScale))
            .constraintSize({minWidth:PhoneStyle.sidebarMetric(44,this.environment.fontScale)}).padding(0).backgroundColor(Color.Transparent)
            .accessibilityText([tr(inboxTierLabel(choice.key)),choice.count ? String(choice.count) : ''].filter((v:string) => !!v).join(' · '))
            .accessibilitySelected(this.workspace.statusFilter === choice.key).onClick(() => this.pickChoice(choice.key)).id('inbox.choice.'+choice.key)
        },(choice:InboxQuickFilter) => choice.key + '/' + String(choice.count))
      }.padding({left:4,right:0})
    }.scrollable(ScrollDirection.Horizontal).scrollBar(BarState.Off).align(Alignment.Start).width('100%').id('inbox.status')
  }
  private chipTouchRegion(): Rectangle {
    const height = PhoneStyle.sidebarMetric(44, this.environment.fontScale);
    const visible = PhoneStyle.formSize(30, 37.3, this.environment.fontScale);
    return {x:0, y:(visible-height)/2, width:'100%', height:height};
  }
  @Builder sourceChip(value:string, account:boolean) {
    Button({type:ButtonType.Capsule}) {
      Row({space:4}) {
        if (value!=='all') SymbolGlyph(account ? Symbols.inbox : value==='lark' ? Symbols.persona : Symbols.apps)
          .fontSize('13vp').fontColor([this.chipSelected(value,account) ? Colors.onAction : Colors.ink])
        Text(account ? this.workspace.filterAccountName(value) : this.sourceLabel(value)).fontSize(Tokens.typeCaption)
          .maxFontScale(Tokens.navigationTextMaxScale).maxLines(1).fontColor(this.chipSelected(value,account) ? Colors.onAction : Colors.ink)
      }
    }.height(PhoneStyle.formSize(30,37.3,this.environment.fontScale)).responseRegion(this.chipTouchRegion()).padding({left:13,right:13,top:3,bottom:3})
      .backgroundColor(this.chipSelected(value,account) ? Colors.action : Colors.control)
      .animation(Motion.state(this.environment.reduceMotion)).accessibilitySelected(this.chipSelected(value,account))
      .onClick(() => {
        Haptics.selection();
        const clear=this.chipSelected(value,account);
        Motion.run(this.getUIContext(), Motion.state(this.environment.reduceMotion), () => {
          this.workspace.sourceFilter=account || clear ? 'all' : value; this.workspace.accountFilter=account && !clear ? value : 'all';
          if (value==='all') this.workspace.kindFilter='';
        });
      })
      .id('inbox.'+(account ? 'account.' : 'source.')+value)
  }
  private chipSelected(value:string, account:boolean):boolean {
    return account ? this.workspace.accountFilter===value : this.workspace.sourceFilter===value && this.workspace.accountFilter==='all' && (value!=='all' || !this.workspace.kindFilter);
  }
  @Builder sourceBar() {
    Scroll() {
      Row({space:6}) {
        ForEach(this.workspace.channelOptions,(value:string) => {
          this.sourceChip(value,false)
        },(value:string) => value)
        ForEach(this.workspace.sourceAccounts,(value:string) => {
          this.sourceChip(value,true)
        },(value:string) => value)
        Divider().vertical(true).height(19).color(Colors.divider)
        ForEach(['mention','dm','group','mail','agent'],(kind:string) => {
          Button(tr(kind==='mail' || kind==='agent' ? kind : 'phone_inbox_'+kind),{type:ButtonType.Capsule})
            .height(PhoneStyle.formSize(30,37.3,this.environment.fontScale)).responseRegion(this.chipTouchRegion()).padding({left:13,right:13,top:3,bottom:3})
            .fontSize(Tokens.typeCaption).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption)).backgroundColor(this.workspace.kindFilter===kind ? Colors.action : Colors.subtle)
            .animation(Motion.state(this.environment.reduceMotion))
            .fontColor(this.workspace.kindFilter===kind ? Colors.onAction : Colors.ink).accessibilitySelected(this.workspace.kindFilter===kind)
            .onClick(() => { Haptics.selection(); Motion.run(this.getUIContext(), Motion.state(this.environment.reduceMotion), () => { this.workspace.kindFilter=this.workspace.kindFilter===kind ? '' : kind; }); }).id('inbox.kind.'+kind)
        },(kind:string) => kind)
      }
    }.scrollable(ScrollDirection.Horizontal).scrollBar(BarState.Off).width('100%').align(Alignment.Start)
      .padding({left:this.inboxSidePadding(),right:this.inboxSidePadding()}).id('inbox.sources')
  }

  private rowTime(row:ResourceRow):string {
    const d=row.data();
    const stamp=Date.parse(row.kind==='session' ? Wire.text(d,'inbox_asked_at') : Wire.text(d,'at') || Wire.text(d,'updated_at') || Wire.text(d,'created_at'));
    if (!Number.isFinite(stamp)) return '';
    const date=new Date(stamp);
    return String(date.getHours()).padStart(2,'0')+':'+String(date.getMinutes()).padStart(2,'0');
  }
  private rowPreview(row:ResourceRow):string {
    const data=row.data();
    if (row.kind==='session') return row.detail;
    return row.detail || Wire.text(data,'last_text') || Wire.text(data,'preview') || Wire.text(data,'where');
  }
  private requestLabel(row:ResourceRow):string {
    const d=row.data(), kind=Wire.text(d,'inbox_request_kind');
    if (kind==='plan') return tr('phone_agent_plan_approval');
    if (kind==='permission') return tr('phone_agent_needs_approval');
    if (kind==='question') {
      const agent=Wire.text(d,'inbox_request_tool')==='ask_user' ? 'Persona' : Wire.text(d,'agent_label') || Wire.text(d,'agent') || tr('agent');
      return tr('phone_agent_asks').replace('{agent}',agent==='claude' ? 'Claude' : agent==='codex' ? 'Codex' : agent);
    }
    return tr('needs_you');
  }
  private get listFill():ResourceColor {
    return this.phoneList && Colors.styleID==='glass' ? Colors.listBackground : Colors.surface;
  }
  @Builder requestAvatar(row:ResourceRow, identity:string) {
    Stack({ alignContent: Alignment.TopEnd }) {
      Stack() {
        if (this.phoneList && row.kind==='session' && Wire.text(row.data(),'inbox_request_kind')==='question') {
          SymbolGlyph($r('sys.symbol.message_fill')).fontSize('19vp').fontColor([Colors.needsMark])
          Text('?').fontSize('11vp').fontWeight(FontWeight.Bold).fontColor($r('app.color.phone_inbox_request_well'))
            .offset({y:-1}).accessibilityLevel('no')
        } else {
          SymbolGlyph(this.rowSymbol(row)).fontSize(this.phoneList && row.kind==='session' ? '17vp' : '18vp')
            .fontColor([this.phoneList && row.kind==='session' ? Colors.needsMark : Colors.link])
        }
      }.width(40).height(40).borderRadius(this.phoneList && row.kind==='session' ? Tokens.radiusCard : Tokens.radiusControl)
        .backgroundColor(this.phoneList && row.kind==='session' ? $r('app.color.phone_inbox_request_well') : Colors.subtle)
      if (this.rowNeedsPulse(row)) {
        NeedsYouPulseDot({ dotSize: 7, tint: Colors.needsMark, surface: Colors.surface, bordered: true })
          .offset({ x: 3, y: -3 })
      }
    }.width(40).height(40).flexShrink(0).clip(false)
      .id('inbox.row.icon.'+identity)
  }
  private rowSymbol(row:ResourceRow):Resource {
    const data=row.data();
    if (row.kind==='session') {
      const kind=Wire.text(data,'inbox_request_kind');
      if (kind==='plan') return this.phoneList ? $r('sys.symbol.text_clipboard_fill') : Symbols.document;
      if (kind==='question') return $r('sys.symbol.questionmark_circle');
      if (kind==='permission') return $r('sys.symbol.hand_raised_fingers_spread');
      return Symbols.agent;
    }
    const source=Wire.text(data,'source');
    return source==='mail' ? Symbols.inbox : source==='lark' || source==='feishu' ? Symbols.persona : Symbols.apps;
  }
  private groupOpen(key:string):boolean {
    return this.workspace.statusFilter !== 'all' || !!this.workspace.search.trim() || this.expandedTiers.includes(key) || !['done','noise'].includes(key);
  }
  private subheading(section:BoundInboxSection):string {
    const group=section.sourceGroup;
    if (group==='agents') return tr('phone_inbox_waiting_agents');
    if (group.startsWith('time:d:')) {
      const date = new Date(group.slice(7) + 'T12:00:00');
      const locale = this.model.language === 'default' ? i18n.System.getSystemLanguage() : this.model.language;
      return date.toLocaleDateString(locale, {weekday:'short',month:'short',day:'numeric'});
    }
    if (group === 'time:today') return tr('machine_today');
    if (group === 'time:yesterday') return tr('kb_yesterday');
    if (group === 'time:week') return tr('phone_inbox_week_ago');
    if (group === 'time:month') return tr('phone_inbox_month_ago');
    if (group.startsWith('mail:')) return this.workspace.filterAccountName(group.slice(5)) || tr('mail');
    if (group.startsWith('c:')) return this.workspace.filterAccountName(group.slice(2)) || this.sourceLabel('connector');
    return this.sourceLabel(group);
  }
  private restoreFolds():void {
    this.expandedTiers=[];this.collapsedSources=[];this.foldLease=undefined;
    const revision=++this.foldRevision;
    if (!this.list || !this.model.profile.id) return;
    const lease=this.model.captureView('inbox.group-folds');
    this.foldLease=lease;
    lease.read().then((raw:string) => {
      if (!raw || !this.active || this.foldLease!==lease || revision!==this.foldRevision) return;
      const saved=Wire.parse(raw);
      this.expandedTiers=Wire.strings(saved,'expanded');this.collapsedSources=Wire.strings(saved,'collapsed');
    }).catch(() => {});
  }
  private toggleSection(section:BoundInboxSection):void {
    if (section.sourceGroup) {
      Haptics.tap();
      Motion.run(this.getUIContext(), Motion.state(this.environment.reduceMotion), () => {
        this.collapsedSources=this.collapsedSources.includes(section.key) ? this.collapsedSources.filter((s:string) => s!==section.key) :
          this.collapsedSources.concat([section.key]);
      });
    } else if (this.workspace.statusFilter==='all' && ['done','noise'].includes(section.tier)) {
      Haptics.tap();
      Motion.run(this.getUIContext(), Motion.state(this.environment.reduceMotion), () => {
        this.expandedTiers=this.expandedTiers.includes(section.tier) ? this.expandedTiers.filter((s:string) => s!==section.tier) :
          this.expandedTiers.concat([section.tier]);
      });
    } else return;
    this.foldRevision++;
    this.foldLease?.write(JSON.stringify({expanded:this.expandedTiers,collapsed:this.collapsedSources})).catch(() => {});
  }
  @Builder sectionHeading(section:BoundInboxSection) {
    Row({space:5}) {
      if (section.sourceGroup) SymbolGlyph(this.collapsedSources.includes(section.key) ? Symbols.expand : Symbols.down).fontSize('11vp').fontColor([Colors.secondary])
      Text(section.sourceGroup ? this.subheading(section) : tr(inboxTierLabel(section.tier))+' ·')
        .fontSize(Tokens.typeSectionHeader).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSectionHeader)).fontWeight(FontWeight.Medium).fontColor(section.sourceGroup ? Colors.secondary : Colors.ink)
      Text(String(section.count)).fontSize(Tokens.typeCaption).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption)).fontColor(section.sourceGroup ? Colors.secondary : Colors.ink).layoutWeight(1)
      if (!section.sourceGroup && this.workspace.statusFilter==='all' && ['done','noise'].includes(section.tier))
        SymbolGlyph(this.groupOpen(section.tier) ? Symbols.down : Symbols.expand).fontSize('13vp').fontColor([Colors.secondary])
    }.constraintSize({minHeight:PhoneStyle.sidebarMetric(44,this.environment.fontScale)}).width('100%').padding({left:4,right:4,top:4,bottom:4})
      .accessibilityGroup(true).onClick(() => this.toggleSection(section)).id('inbox.section.'+section.key)
  }
  @Builder inboxList() {
    Column({ space: Tokens.gap }) {
      Row() {
        Column() { this.choiceBar() }.layoutWeight(1)
        if (!['done','working'].includes(this.workspace.statusFilter)) this.groupingButton()
        if (!this.toolbarActions) this.inboxActions()
      }.width('100%').padding({left:this.inboxSidePadding(),right:this.inboxSidePadding(),top:8})
      Column() { this.sourceBar() }.width('100%').margin({top:5})
      if (this.workspace.error) Text(tr(this.workspace.error)).fontSize(Tokens.meta).maxFontScale(Tokens.interfaceScaleFor(Tokens.meta)).fontColor($r('app.color.persona_danger')).padding({ left: this.inboxSidePadding(), right: this.inboxSidePadding() })
      if (this.workspace.loading) LoadingProgress().width(Tokens.touch).height(Tokens.touch)
      List({ space: 10 }) {
        ForEach(this.sections,(section:BoundInboxSection) => {
          if (!section.sourceGroup || this.groupOpen(section.tier)) ListItemGroup({header:() => this.sectionHeading(section), space: 8}) {
            if (this.groupOpen(section.tier) && !this.collapsedSources.includes(section.key)) {
        ForEach(section.rows, (binding: BoundRow<ResourceRow>) => {
          ListItem() {
            Row({ space: 12 }) {
              this.requestAvatar(binding.value,binding.key)
              Column({ space: 4 }) {
                Row({space:5}) {
                  Text(binding.value.title || tr('inbox')).fontSize(Tokens.typeHeading).maxFontScale(Tokens.interfaceFontMaxScale).fontWeight(FontWeight.Medium).fontColor(Colors.ink)
                    .layoutWeight(1).maxLines(binding.value.kind === 'session' ? 1 : 2).textOverflow({overflow:TextOverflow.Ellipsis})
                  if (this.rowTime(binding.value))
                    Text(this.rowTime(binding.value)).fontSize(Tokens.typeLabel).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeLabel)).fontColor(Colors.secondary)
                }.width('100%').alignItems(VerticalAlign.Top)
                if (this.rowPreview(binding.value)) Text(this.rowPreview(binding.value)).fontSize(Tokens.typeSecondary).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSecondary)).fontColor(binding.value.kind==='session' ? Colors.ink : Colors.secondary).maxLines(2).textOverflow({overflow:TextOverflow.Ellipsis})
                Flex({wrap:FlexWrap.Wrap,space:{main:LengthMetrics.vp(5),cross:LengthMetrics.vp(2)}}) {
                  Text(binding.value.kind === 'session' ? this.requestLabel(binding.value) : tr(inboxTierLabel(inboxTier(binding.value.data())))).fontSize(Tokens.typeLabel).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeLabel))
                    .fontWeight(FontWeight.Medium).fontColor(Colors.link)
                  Text(binding.value.kind === 'session' ? '· '+(this.model.machineName(binding.value.machine) || tr('agent')) : this.rowSource(binding.value)).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12))
                    .fontColor(Colors.secondary).id('inbox.row.source.' + binding.key)
                }.width('100%')
              }.layoutWeight(1).alignItems(HorizontalAlign.Start)
            }.width('100%').constraintSize({minHeight:PhoneStyle.formSize(86,108,this.environment.fontScale)}).alignItems(VerticalAlign.Top)
              .padding({left:14,right:14,top:12,bottom:12}).backgroundColor(Colors.surface).borderRadius(PhoneStyle.cardRadius)
              .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.cardBorder(this.environment.dark),radius:PhoneStyle.cardRadius})
              .shadow(PhoneStyle.cardElevation(this.environment.dark))
          }.onClick(() => this.openRow(binding.value)).accessibilityGroup(true).id('inbox.' + inboxLineKey(binding.value))
        }, (binding: BoundRow<ResourceRow>) => binding.key)
            }
          }
        },(section:BoundInboxSection) => section.key)
        if (this.workspace.statusFilter === 'done' && !this.workspace.historyEnded && (this.workspace.haveDoneDays || this.workspace.doneItems.length)) ListItemGroup() {
          ListItem() {
            Button(tr('flow_inbox_older_done'),{buttonStyle:ButtonStyleMode.TEXTUAL}).fontSize(14).maxFontScale(Tokens.interfaceScaleFor(14)).enabled(!this.workspace.historyBusy)
              .onClick(() => { Haptics.tap(); this.workspace.loadOlderDone(); }).id('inbox.history.more')
          }
        }
        if (!this.itemRowBindings.length && !this.workspace.loading) ListItemGroup() {
          ListItem() { Text(tr(this.workspace.search.trim() ? 'no_results' : 'flow_inbox_section_empty')).fontSize(15).maxFontScale(Tokens.interfaceScaleFor(15)).fontColor(Colors.secondary).padding(Tokens.page).width('100%') }
        }
      }.lanes(this.inboxColumns(), workspaceGridGutter(this.inboxColumns()))
        .layoutWeight(1).width('100%').margin({top:12}).padding({left:this.inboxSidePadding(),right:this.inboxSidePadding(),bottom:Tokens.inset})
      Row() { this.inboxSearch(true) }.width('100%').padding({left:this.inboxSidePadding(),right:this.inboxSidePadding(),top:3,bottom:8})
    }.width('100%').height('100%').backgroundColor(Colors.grouped)
      .onAreaChange((_old: Area, area: Area) => { const w = Number(area.width); if (Number.isFinite(w) && w > 0 && Math.abs(w - this.paneWidth) > 0.5) this.paneWidth = w; })
  }

  @Builder header() {
    Row({ space: Tokens.gap }) {
      Text(this.workspace.isReference() ? this.workspace.referenceSourceLabel() + (this.workspace.replyAccountLabel() ? ' · ' + this.workspace.replyAccountLabel() : '')
        : this.rowSource(this.workspace.selected)).fontSize(Tokens.meta).maxFontScale(Tokens.interfaceScaleFor(Tokens.meta)).fontColor($r('sys.color.font_secondary')).maxLines(1).layoutWeight(1)
      Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('refresh')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('refresh', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('refresh')).bindTips(tr('refresh')).enabled(!this.workspace.detailLoading)
        .onClick(() => this.workspace.refreshDetail())
    }.width('100%').padding({ left: Tokens.page, right: Tokens.page, top: Tokens.gap })
  }

  @Builder rowStatus() {
    Row({ space: Tokens.gap }) {
      Text(this.workspace.isHandled() ? tr('done') : tr('inbox_open')).fontSize(Tokens.meta).maxFontScale(Tokens.interfaceScaleFor(Tokens.meta))
        .fontColor($r('sys.color.font_secondary'))
      if (Wire.bool(this.workspace.detail, 'urgent')) Text(tr('inbox_urgent')).fontSize(Tokens.meta).maxFontScale(Tokens.interfaceScaleFor(Tokens.meta)).fontColor($r('app.color.persona_danger'))
      if (Wire.text(this.workspace.detail, 'tier')) Text(tr('inbox_tier_' + Wire.text(this.workspace.detail, 'tier'))).fontSize(Tokens.meta).maxFontScale(Tokens.interfaceScaleFor(Tokens.meta)).fontColor($r('sys.color.font_secondary'))
      if (Wire.text(this.workspace.detail, 'folder')) Text(Wire.text(this.workspace.detail, 'folder')).fontSize(Tokens.meta).maxFontScale(Tokens.interfaceScaleFor(Tokens.meta)).fontColor($r('sys.color.font_secondary'))
    }.width('100%')
  }

  private linkedKnowledgeChat(): InboxKBChatLink | null {
    if (this.workspace.isReference()) return null;
    if (isHubPageHidden('knowledge', this.model.sidebarConfig, this.model.demo)) return null;
    return parseInboxKBChat(this.workspace.kbChat);
  }

  private openKnowledgeChat(link: InboxKBChatLink): void {
    Haptics.tap();
    const row = new ResourceRow(link.id, this.workspace.selected.title || link.id, 'knowledge');
    row.path = link.path;
    row.manager = this.workspace.selected.manager || this.model.profile.manager;
    row.group = this.workspace.selected.group || this.model.profile.id;
    this.onOpenResource(row);
  }

  @Builder kbChatCard() {
    if (this.linkedKnowledgeChat()) {
      Button({ type: ButtonType.Normal }) {
        Row({ space: Tokens.spaceM }) {
          SymbolGlyph($r('sys.symbol.books_vertical_fill'))
            .fontSize('18vp')
            .fontColor([Colors.ai])
          Column({ space: Tokens.spaceXxs }) {
            Text(tr('kb_chat_knowledge_page'))
              .fontSize(Tokens.typeSecondary)
              .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSecondary))
              .fontWeight(FontWeight.Medium)
              .fontColor(Colors.ink)
              .maxLines(1)
            Text(this.workspace.selected.title || this.linkedKnowledgeChat()!.id)
              .fontSize(Tokens.typeLabel)
              .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeLabel))
              .fontColor(Colors.secondary)
              .maxLines(1)
              .textOverflow({ overflow: TextOverflow.Ellipsis })
          }
          .layoutWeight(1)
          .alignItems(HorizontalAlign.Start)
          SymbolGlyph(Symbols.expand)
            .fontSize('12vp')
            .fontColor([Colors.tertiary])
        }
        .width('100%')
        .padding({ left: Tokens.spaceL, right: Tokens.spaceM, top: Tokens.spaceM, bottom: Tokens.spaceM })
        .alignItems(VerticalAlign.Center)
      }
      .width('100%')
      .padding(0)
      .backgroundColor(Colors.surface)
      .borderRadius(PhoneStyle.cardRadius)
      .border({ width: PhoneStyle.surfaceEdgeWidth(0.5), color: PhoneStyle.cardBorder(this.environment.dark), radius: PhoneStyle.cardRadius })
      .shadow(PhoneStyle.cardElevation(this.environment.dark))
      .onClick(() => {
        const link = this.linkedKnowledgeChat();
        if (link) this.openKnowledgeChat(link);
      })
      .id('inbox.detail.kb_chat')
    }
  }

  @Builder summary() {
    Column({ space: Tokens.gap }) {
      this.rowStatus()
      if (Wire.text(this.workspace.detail, 'sender')) Text(Wire.text(this.workspace.detail, 'sender')).fontSize(Tokens.body).maxFontScale(Tokens.interfaceScaleFor(Tokens.body)).width('100%')
      if (Wire.text(this.workspace.detail, 'summary')) MarkdownText({ text: Wire.text(this.workspace.detail, 'summary'), onLink: (address: string) => this.onLink(address) })
      if (Wire.text(this.workspace.detail, 'preview')) MarkdownText({ text: Wire.text(this.workspace.detail, 'preview'), onLink: (address: string) => this.onLink(address) })
      if (Wire.text(this.workspace.detail, 'reason')) Text(Wire.text(this.workspace.detail, 'reason')).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary'))
      if (Wire.text(this.workspace.detail, 'recap')) MarkdownText({ text: Wire.text(this.workspace.detail, 'recap'), onLink: (address: string) => this.onLink(address) })
    }.width('100%').alignItems(HorizontalAlign.Start)
  }

  @Builder trail() {
    if (this.chain().length || Object.keys(this.handling()).length || Object.keys(this.suggestion()).length > 0 || Wire.rows(this.workspace.detail, 'handoffs').length) {
      Column({ space: Tokens.gap }) {
        Text(tr('inbox_processing')).fontSize(Tokens.title).maxFontScale(Tokens.interfaceScaleFor(Tokens.title)).fontWeight(FontWeight.Medium)
        ForEach(this.chain(), (entry: Record<string, Object>) => {
          ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: Wire.text(entry, 'text', Wire.text(entry, 'kind')),
            secondaryText: [Wire.text(entry, 'detail'), Wire.text(entry, 'at')].filter((value: string) => !!value).join(' · ') || undefined }) })
        }, (entry: Record<string, Object>) => Wire.text(entry, 'id'))
        if (Object.keys(this.handling()).length) ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr('inbox_handling_' + Wire.text(this.handling(), 'state')),
          secondaryText: [Wire.text(this.handling(), 'owner'), Wire.text(this.handling(), 'error')].filter((value: string) => !!value).join(' · ') || undefined }) })
        if (Object.keys(this.suggestion()).length) {
          ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr('inbox_suggestion_' + Wire.text(this.suggestion(), 'kind')),
            secondaryText: Wire.text(this.suggestion(), 'note') || undefined }) })
          if (Wire.text(this.suggestion(), 'text')) MarkdownText({ text: Wire.text(this.suggestion(), 'text'), onLink: (address: string) => this.onLink(address) })
        }
        ForEach(Wire.rows(this.workspace.detail, 'handoffs'), (entry: Record<string, Object>) => {
          ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr('inbox_handed_to'),
            secondaryText: Wire.text(entry, 'machine_id') + ' · ' + Wire.text(entry, 'session_id') + ' · ' + Wire.text(entry, 'at') }) })
        }, (entry: Record<string, Object>, index: number) => String(index) + ':' + Wire.text(entry, 'session_id'))
        ForEach(Wire.rows(this.workspace.detail, 'related'), (entry: Record<string, Object>) => {
          ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: Wire.text(entry, 'title'), secondaryText: Wire.text(entry, 'source') }) })
        }, (entry: Record<string, Object>) => Wire.text(entry, 'id'))
      }.width('100%').alignItems(HorizontalAlign.Start)
    }
  }

  @Builder messageCard(message: InboxMessage) {
    Column({ space: Tokens.gap }) {
      Row({ space: Tokens.gap }) {
        Text(message.sender || (message.mine ? tr('you') : tr('message'))).fontSize(Tokens.meta).maxFontScale(Tokens.interfaceScaleFor(Tokens.meta)).fontWeight(FontWeight.Medium).layoutWeight(1)
        if (message.state) Text(tr('inbox_message_' + message.state)).fontSize(Tokens.meta).maxFontScale(Tokens.interfaceScaleFor(Tokens.meta)).fontColor($r('sys.color.font_secondary'))
        if (Wire.bool(message.data(), 'recalled')) Text(tr('inbox_recalled')).fontSize(Tokens.meta).maxFontScale(Tokens.interfaceScaleFor(Tokens.meta)).fontColor($r('sys.color.font_secondary'))
      }.width('100%')
      if (this.hasQuote(message)) {
        Text(Wire.text(this.quote(message), 'sender_name') + ': ' + Wire.text(this.quote(message), 'summary')).fontSize(Tokens.meta)
          .fontColor($r('sys.color.font_secondary')).width('100%')
      }
      if (this.messageParts(message).length) InboxMessageParts({ workspace: this.workspace, parts: this.messageParts(message),
        onLink: (address: string) => this.onLink(address), onSaveMedia: (media: string, name: string, mime: string) => this.saveMessageMedia(media, name, mime) })
      else if (message.text) MarkdownText({ text: message.text, onLink: (address: string) => this.onLink(address) })
      ForEach(this.messageDecorations(message), (item: Record<string, Object>) => {
        Text(Wire.text(item, 'text')).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).width('100%')
      }, (item: Record<string, Object>, index: number) => String(index) + Wire.text(item, 'text'))
      if (Wire.bool(message.data(), 'edited')) Text(tr('inbox_edited')).fontSize(Tokens.meta).maxFontScale(Tokens.interfaceScaleFor(Tokens.meta)).fontColor($r('sys.color.font_secondary'))
      ForEach(this.messageReactions(message), (reaction: Record<string, Object>) => {
        if (this.workspace.canReact(message)) Button(Wire.text(reaction, 'key') + ' · ' + String(Wire.number(reaction, 'count')),
          { buttonStyle: ButtonStyleMode.TEXTUAL }).height(Tokens.touch).onClick(() => this.workspace.react(message, Wire.text(reaction, 'key')))
        else Text(Wire.text(reaction, 'key') + ' · ' + String(Wire.number(reaction, 'count'))).fontSize(Tokens.meta)
      }, (reaction: Record<string, Object>) => Wire.text(reaction, 'key'))
      if (this.canOpenMessageThread(message) && (this.workspace.isTopicList || Object.keys(this.messageThread(message)).length)) {
        Button(tr('inbox_open_thread') + (Wire.number(this.messageThread(message), 'count') ? ' · ' + String(Wire.number(this.messageThread(message), 'count')) : ''),
          { buttonStyle: ButtonStyleMode.TEXTUAL }).maxFontScale(Tokens.interfaceFontMaxScale).height(Tokens.touch).onClick(() => this.workspace.openThread(message))
      }
      if (!this.workspace.isThreadReference() && this.workspace.canReplyToMessage(message)) Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('reply')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('reply', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('reply')).bindTips(tr('reply')).onClick(() => this.workspace.setThreadTarget(message.id, false))
      if (this.workspace.canReact(message) && this.workspace.quickReactions.length) {
        Row({ space: Tokens.fine }) {
          ForEach(this.workspace.quickReactions, (key: string) => {
            Button(key, { buttonStyle: ButtonStyleMode.TEXTUAL }).height(Tokens.touch).onClick(() => this.workspace.react(message, key))
          }, (key: string) => key)
        }.width('100%')
      }
      if (this.workspace.capability('edit_history') && this.messageHistory(message).length) {
        ForEach(this.messageHistory(message), (entry: Record<string, Object>) => {
          Text(Wire.text(entry, 'text', new InboxMessage(entry).text)).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).width('100%')
        }, (entry: Record<string, Object>, index: number) => String(index) + Wire.text(entry, 'edited_at'))
      }
    }.width('100%').padding(Tokens.inset).alignItems(HorizontalAlign.Start)
      .backgroundColor(Colors.surface).borderRadius(PhoneStyle.cardRadius)
      .border({ width: PhoneStyle.surfaceEdgeWidth(0.5), color: PhoneStyle.cardBorder(this.environment.dark), radius: PhoneStyle.cardRadius })
      .shadow(PhoneStyle.cardElevation(this.environment.dark))
      .onVisibleAreaChange([0, 1], (visible: boolean, ratio: number) => { if (visible && ratio > 0) this.workspace.markMessageVisible(message); })
  }

  @Builder conversation() {
    if (this.workspace.hasConversation()) {
      Column({ space: Tokens.gap }) {
        Row({ space: Tokens.gap }) {
          Text(tr(this.workspace.isTopicList ? 'inbox_topics' : 'inbox_conversation')).fontSize(Tokens.title).maxFontScale(Tokens.interfaceScaleFor(Tokens.title)).fontWeight(FontWeight.Medium).layoutWeight(1)
          if (this.workspace.threadRoot && !this.workspace.isThreadReference()) Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('back')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('back', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('back')).bindTips(tr('back')).onClick(() => this.workspace.closeThread())
          Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('refresh')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('refresh', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('refresh')).bindTips(tr('refresh')).enabled(!this.workspace.feedLoading).onClick(() => this.workspace.refreshConversation())
        }.width('100%')
        if (this.workspace.feedHasOlder) Button({ type: ButtonType.Circle }) { if (IconActions.busy(this.workspace.feedLoading ? 'loading' : 'inbox_older')) LoadingProgress().width(Tokens.symbol).height(Tokens.symbol).color(Colors.link); else SymbolGlyph(IconActions.symbol(this.workspace.feedLoading ? 'loading' : 'inbox_older')).fontSize(Tokens.symbolFont).fontColor([IconActions.color(this.workspace.feedLoading ? 'loading' : 'inbox_older', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr(this.workspace.feedLoading ? 'loading' : 'inbox_older')).bindTips(tr(this.workspace.feedLoading ? 'loading' : 'inbox_older')).enabled(!this.workspace.feedLoading)
          .onClick(() => this.workspace.loadOlder())
        ForEach(this.workspace.feedMessages, (message: InboxMessage) => this.messageCard(message), (message: InboxMessage) => message.id)
        if (this.workspace.feedLoading) LoadingProgress().width(Tokens.touch).height(Tokens.touch)
      }.width('100%').alignItems(HorizontalAlign.Start)
    }
  }

  @Builder threadReferenceContent() {
    if (this.workspace.isThreadReference() && !this.workspace.hasConversation()) {
      Column({ space: Tokens.gap }) {
        Text(this.workspace.workspaceThreadTitle() || this.workspace.referenceThreadId).fontSize(Tokens.title).maxFontScale(Tokens.interfaceScaleFor(Tokens.title)).fontWeight(FontWeight.Medium).width('100%')
        if (this.workspace.referenceThreadId) Text(this.workspace.referenceThreadId).fontSize(Tokens.meta)
          .fontColor($r('sys.color.font_secondary')).copyOption(CopyOptions.LocalDevice).width('100%')
      }.width('100%').alignItems(HorizontalAlign.Start).padding(Tokens.inset)
        .backgroundColor($r('sys.color.comp_background_tertiary'))
    }
  }

  private shownMailSubject(message: InboxMailMessage): string {
    if (!this.workspace.showOriginal && this.workspace.translatedMailUid === message.uid)
      return Wire.text(this.workspace.mailTranslation, 'subject', message.subject);
    return message.subject;
  }
  private shownMailText(message: InboxMailMessage): string {
    if (!this.workspace.showOriginal && this.workspace.translatedMailUid === message.uid && Wire.text(this.workspace.mailTranslation, 'text'))
      return Wire.text(this.workspace.mailTranslation, 'text');
    return message.bodyText();
  }
  private hasMailTranslation(message: InboxMailMessage): boolean { return this.workspace.translatedMailUid === message.uid; }

  @Builder mailMessage(message: InboxMailMessage) {
    Column({ space: Tokens.gap }) {
      Text(this.shownMailSubject(message)).fontSize(Tokens.title).maxFontScale(Tokens.interfaceScaleFor(Tokens.title)).fontWeight(FontWeight.Medium).width('100%')
      Text(message.from + (message.to ? ' · ' + message.to : '')).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).width('100%')
      if (message.date) Text(message.date).fontSize(Tokens.meta).maxFontScale(Tokens.interfaceScaleFor(Tokens.meta)).fontColor($r('sys.color.font_secondary')).width('100%')
      if (this.workspace.showOriginal || !Wire.text(this.workspace.mailTranslation, 'text'))
        MarkdownText({ text: this.shownMailText(message), onLink: (address: string) => this.onLink(address) })
      else Text(this.shownMailText(message)).fontSize(Tokens.body).width('100%').copyOption(CopyOptions.LocalDevice)
      if (Wire.rows(message.data(), 'attachments').length) {
        ForEach(Wire.rows(message.data(), 'attachments'), (attachment: Record<string, Object>) => {
          Row({ space: Tokens.gap }) {
            Text(Wire.text(attachment, 'filename', tr('attachment')) + ' · ' + inboxBytesText(Wire.number(attachment, 'size'))).layoutWeight(1).maxLines(2)
            Button({ type: ButtonType.Circle }) { if (IconActions.busy(this.mediaBusy ? 'loading' : 'save')) LoadingProgress().width(Tokens.symbol).height(Tokens.symbol).color(Colors.onAction); else SymbolGlyph(IconActions.symbol('download')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('download', true)]) }.attributeModifier(ButtonStyles.iconPrimary).accessibilityText(tr(this.mediaBusy ? 'loading' : 'save')).bindTips(tr(this.mediaBusy ? 'loading' : 'save')).enabled(!this.mediaBusy).onClick(() => this.saveMailAttachment(message, attachment))
          }.width('100%')
        }, (attachment: Record<string, Object>) => Wire.text(attachment, 'id'))
      }
      Row({ space: Tokens.gap }) {
        Select(this.optionList(['zh-Hans', 'en', 'ja'])).attributeModifier(SelectStyles.inline)
          .selected(this.workspace.translationTarget === 'zh-Hans' ? 0 : this.workspace.translationTarget === 'en' ? 1 : 2)
          .onSelect((index: number) => this.workspace.translationTarget = ['zh-Hans', 'en', 'ja'][index]).layoutWeight(1)
        Button({ type: ButtonType.Circle }) { if (IconActions.busy(this.workspace.translationBusy ? 'loading' : this.workspace.translationError ? 'retry' : 'translation')) LoadingProgress().width(Tokens.symbol).height(Tokens.symbol).color(Colors.link); else SymbolGlyph(IconActions.symbol(this.workspace.translationBusy ? 'loading' : this.workspace.translationError ? 'retry' : 'translation')).fontSize(Tokens.symbolFont).fontColor([IconActions.color(this.workspace.translationBusy ? 'loading' : this.workspace.translationError ? 'retry' : 'translation', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr(this.workspace.translationBusy ? 'loading' : this.workspace.translationError ? 'retry' : 'translation')).bindTips(tr(this.workspace.translationBusy ? 'loading' : this.workspace.translationError ? 'retry' : 'translation')).enabled(!this.workspace.translationBusy).onClick(() => this.workspace.translateMail(message))
        if (this.hasMailTranslation(message)) Button({ type: ButtonType.Circle }) { if (IconActions.busy(this.workspace.showOriginal ? 'inbox_show_translation' : 'inbox_show_original')) LoadingProgress().width(Tokens.symbol).height(Tokens.symbol).color(Colors.link); else SymbolGlyph(IconActions.symbol(this.workspace.showOriginal ? 'inbox_show_translation' : 'inbox_show_original')).fontSize(Tokens.symbolFont).fontColor([IconActions.color(this.workspace.showOriginal ? 'inbox_show_translation' : 'inbox_show_original', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr(this.workspace.showOriginal ? 'inbox_show_translation' : 'inbox_show_original')).bindTips(tr(this.workspace.showOriginal ? 'inbox_show_translation' : 'inbox_show_original')).onClick(() => this.workspace.toggleOriginal())
      }.width('100%')
      if (this.workspace.translationError) Text(tr(this.workspace.translationError)).fontSize(Tokens.meta).maxFontScale(Tokens.interfaceScaleFor(Tokens.meta)).fontColor($r('app.color.persona_danger')).width('100%')
      if (this.hasMailTranslation(message) && Wire.bool(this.workspace.mailTranslation, 'clipped')) Text(tr('inbox_translation_clipped')).fontSize(Tokens.meta).maxFontScale(Tokens.interfaceScaleFor(Tokens.meta)).fontColor($r('sys.color.font_secondary'))
    }.width('100%').padding(Tokens.inset).alignItems(HorizontalAlign.Start)
      .backgroundColor(Colors.surface).borderRadius(PhoneStyle.cardRadius)
      .border({ width: PhoneStyle.surfaceEdgeWidth(0.5), color: PhoneStyle.cardBorder(this.environment.dark), radius: PhoneStyle.cardRadius })
      .shadow(PhoneStyle.cardElevation(this.environment.dark))
      .onVisibleAreaChange([0, 1], (visible: boolean, ratio: number) => { if (visible && ratio > 0) this.workspace.markMailVisible(message.uid); })
  }

  @Builder mailContent() {
    if (this.workspace.isMail()) {
      if (this.model.mailModuleAllowed()) {
        Column({ space: Tokens.gap }) {
          ForEach(this.workspace.mailMessages, (message: InboxMailMessage) => this.mailMessage(message), (message: InboxMailMessage) => String(message.uid))
        }.width('100%').alignItems(HorizontalAlign.Start)
      } else if (Wire.text(this.workspace.detail, 'context')) {
        Column({ space: Tokens.gap }) {
          MarkdownText({ text: Wire.text(this.workspace.detail, 'context'), onLink: (address: string) => this.onLink(address) })
        }.width('100%').padding(Tokens.inset).alignItems(HorizontalAlign.Start)
          .backgroundColor(Colors.surface).borderRadius(PhoneStyle.cardRadius)
          .border({ width: PhoneStyle.surfaceEdgeWidth(0.5), color: PhoneStyle.cardBorder(this.environment.dark), radius: PhoneStyle.cardRadius })
          .shadow(PhoneStyle.cardElevation(this.environment.dark))
      }
    }
  }

  @Builder connectorContent() {
    if (Object.keys(this.connector()).length) {
      Column({ space: Tokens.gap }) {
        if (Wire.text(this.connector(), 'label')) Text(Wire.text(this.connector(), 'label')).fontSize(Tokens.title).width('100%')
        if (Wire.text(this.connector(), 'reply_to')) Text(tr('inbox_reply_to') + ': ' + Wire.text(this.connector(), 'reply_to')).fontSize(Tokens.meta).maxFontScale(Tokens.interfaceScaleFor(Tokens.meta)).fontColor($r('sys.color.font_secondary')).width('100%')
        if (Wire.text(this.connector(), 'url')) Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('open')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('open', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('open')).bindTips(tr('open')).onClick(() => this.onLink(Wire.text(this.connector(), 'url')))
      }.width('100%').alignItems(HorizontalAlign.Start)
    }
  }

  private conversationSendKey(): string {
    if (this.workspace.writeState === 'unknown') return 'result_unknown';
    if (this.workspace.attachmentBusy || this.workspace.handoffBusy) return 'send';
    return ['sending', 'pending'].includes(this.workspace.writeState) ? this.workspace.writeState : 'send';
  }
  private attachmentSendKey(): string { return this.workspace.attachmentBusy ? 'sending' : 'inbox_send_attachment'; }
  private mailSendKey(): string {
    if (this.workspace.mailComposeState === 'unknown') return 'result_unknown';
    if (this.workspace.mailComposeState === 'sending') return 'sending';
    if (this.workspace.mailComposeBusy) return this.workspace.mailComposeReady ? 'sending' : 'loading';
    return this.workspace.mailComposeState === 'pending' ? 'pending' : 'send';
  }
  private handoffSendKey(): string {
    if (this.workspace.writeState === 'unknown') return 'result_unknown';
    if (this.workspace.handoffBusy) return 'sending';
    return this.workspace.writeState === 'pending' ? 'pending' : 'inbox_handoff_send';
  }

  @Builder replyBox() {
    if ((this.workspace.isReference() ? this.workspace.isGeneric() && this.workspace.hasConversation() && this.workspace.canReplyReferenceThread()
      : this.workspace.canReplyToItem() || this.workspace.isGeneric()) && !this.workspace.isHandled() &&
      !(this.workspace.isGeneric() && this.workspace.threadRoot && !this.workspace.threadReplyTo)) {
      Column({ space: Tokens.gap }) {
        if (this.workspace.replyTargetChanged) Text(tr('inbox_reply_target_changed')).fontSize(Tokens.meta).maxFontScale(Tokens.interfaceScaleFor(Tokens.meta)).fontColor($r('app.color.persona_danger')).width('100%')
        if (this.workspace.threadReplyTo) Row({ space: Tokens.gap }) {
          Text(tr(this.workspace.threadReply ? 'inbox_thread_reply' : 'inbox_message_reply') + ' · ' + this.workspace.threadReplyTo)
            .fontSize(Tokens.meta).maxFontScale(Tokens.interfaceScaleFor(Tokens.meta)).fontColor($r('sys.color.font_secondary')).layoutWeight(1)
          Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('close')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('close', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('close')).bindTips(tr('close')).onClick(() => this.workspace.setThreadTarget('', false))
        }.width('100%')
        TextArea({ text: this.workspace.replyDraft, placeholder: tr('reply') }).height(Tokens.inputExpanded)
          .enabled(this.workspace.draftReady && !this.workspace.draftLoading && !['sending', 'pending', 'unknown'].includes(this.workspace.writeState))
          .onChange((value: string) => this.workspace.setReplyDraft(value)).id('inbox.reply')
        if (this.workspace.isGeneric()) {
          if (this.workspace.attachment) Row({ space: Tokens.gap }) {
            Text(this.workspace.attachment.name + ' · ' + inboxBytesText(this.workspace.attachment.size)).layoutWeight(1)
            Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('remove')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('remove', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('remove') + ' ' + this.workspace.attachment.name).bindTips(tr('remove') + ' ' + this.workspace.attachment.name).enabled(this.workspace.canWrite()).onClick(() => this.workspace.removeAttachment())
            Button({ type: ButtonType.Circle }) { if (IconActions.busy(this.attachmentSendKey())) LoadingProgress().width(Tokens.symbol).height(Tokens.symbol).color(Colors.link); else SymbolGlyph(IconActions.symbol(this.attachmentSendKey())).fontSize(Tokens.symbolFont).fontColor([IconActions.color(this.attachmentSendKey(), false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr(this.attachmentSendKey())).bindTips(tr(this.attachmentSendKey()))
              .enabled(this.workspace.canSendGeneric()).onClick(() => this.confirmAttachmentSend())
          }.width('100%')
          Row({ space: Tokens.gap }) {
            if (this.workspace.canSendGeneric() && this.workspace.capability('attachments')) {
              Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('attachment')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('attachment', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('attachment')).bindTips(tr('attachment')).enabled(!this.pickerBusy && !this.workspace.attachmentBusy)
                .onClick(() => this.pickConversationAttachment())
            }

            Button({ type: ButtonType.Circle }) { if (IconActions.busy(this.conversationSendKey())) LoadingProgress().width(Tokens.symbol).height(Tokens.symbol).color(Colors.onAction); else SymbolGlyph(IconActions.symbol(this.conversationSendKey())).fontSize(Tokens.symbolFont).fontColor([IconActions.color(this.conversationSendKey(), true)]) }.attributeModifier(ButtonStyles.iconPrimary).accessibilityText(tr(this.conversationSendKey())).bindTips(tr(this.conversationSendKey()))
              .enabled(this.workspace.replyDraft.trim().length > 0 && this.workspace.canSendGeneric() && !['sending', 'pending', 'unknown'].includes(this.workspace.writeState))
              .onClick(() => this.confirmConversationSend())
          }.width('100%')
        } else {
          Button({ type: ButtonType.Circle }) { if (IconActions.busy(this.conversationSendKey())) LoadingProgress().width(Tokens.symbol).height(Tokens.symbol).color(Colors.onAction); else SymbolGlyph(IconActions.symbol(this.conversationSendKey())).fontSize(Tokens.symbolFont).fontColor([IconActions.color(this.conversationSendKey(), true)]) }.attributeModifier(ButtonStyles.iconPrimary).accessibilityText(tr(this.conversationSendKey())).bindTips(tr(this.conversationSendKey()))
            .enabled(this.workspace.replyDraft.trim().length > 0 && this.workspace.draftReady && this.workspace.canWrite())
            .onClick(() => this.confirmReply())
        }
        if (this.workspace.writeState) Text(tr('inbox_write_' + this.workspace.writeState)).fontSize(Tokens.meta).maxFontScale(Tokens.interfaceScaleFor(Tokens.meta)).fontColor($r('sys.color.font_secondary')).width('100%')
      }.width('100%').padding(Tokens.inset).alignItems(HorizontalAlign.Start)
    }
  }

  @Builder detailContent() {
    Column() {
      this.header()
      if (this.workspace.detailLoading) LoadingProgress().width(Tokens.touch).height(Tokens.touch)
      if (this.error) Text(tr(this.error)).fontSize(Tokens.meta).maxFontScale(Tokens.interfaceScaleFor(Tokens.meta)).fontColor($r('app.color.persona_danger')).padding(Tokens.inset).width('100%')
      if (this.workspace.error) Text(tr(this.workspace.error)).fontSize(Tokens.meta).maxFontScale(Tokens.interfaceScaleFor(Tokens.meta)).fontColor($r('app.color.persona_danger')).padding(Tokens.inset).width('100%')
      Scroll() {
        Column({ space: Tokens.section }) {
          if (this.workspace.isReference()) {
            this.threadReferenceContent()
            this.conversation()
          } else {
            this.summary()
            this.kbChatCard()
            this.connectorContent()
            this.mailContent()
            this.conversation()
            this.trail()
          }
        }.width('100%').padding(Tokens.page).alignItems(HorizontalAlign.Start)
      }.layoutWeight(1).width('100%').align(Alignment.TopStart)
      if (!this.workspace.isReference()) Row({ space: Tokens.gap }) {
        Button({ type: ButtonType.Circle }) { if (IconActions.busy(this.workspace.isHandled() ? 'reopen' : 'process')) LoadingProgress().width(Tokens.symbol).height(Tokens.symbol).color(Colors.link); else SymbolGlyph(IconActions.symbol(this.workspace.isHandled() ? 'reopen' : 'process')).fontSize(Tokens.symbolFont).fontColor([IconActions.color(this.workspace.isHandled() ? 'reopen' : 'process', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr(this.workspace.isHandled() ? 'reopen' : 'process')).bindTips(tr(this.workspace.isHandled() ? 'reopen' : 'process')).enabled(this.workspace.canWrite())
          .onClick(() => { Haptics.tap(); this.workspace.processInbox().then((changed: boolean) => { if (changed) Haptics.success(); this.acknowledged(changed); }); })
        Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('handoff')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('handoff', true)]) }.attributeModifier(ButtonStyles.iconPrimary).accessibilityText(tr('handoff')).bindTips(tr('handoff')).enabled(this.workspace.canWrite()).onClick(() => { Haptics.tap(); this.openHandoff(); })
      }.width('100%').padding({ left: Tokens.page, right: Tokens.page, bottom: Tokens.footer })
      this.replyBox()
    }.width('100%').height('100%')
  }

  @Builder sheetContent() {
    if (this.activeSheet === 'mail' && this.model.mailModuleAllowed()) this.mailComposer()
    else if (this.activeSheet === 'handoff') this.handoffComposer()
  }

  @Builder mailComposer() {
    Column({ space: Tokens.gap }) {
      if (this.workspace.mailAccounts.length > 1) Select(this.optionList(this.workspace.mailAccounts.map((item: Record<string, Object>) => this.workspace.mailAccountName(Wire.text(item, 'id'))))).attributeModifier(SelectStyles.inline)
        .selected(this.mailAccountIndex()).onSelect((index: number) => this.workspace.selectMailComposeAccount(Wire.text(this.workspace.mailAccounts[index], 'id')))
      TextInput({ text: this.workspace.mailComposeTo, placeholder: tr('to') }).enabled(this.workspace.mailComposeReady && !this.workspace.mailComposeBlocked)
        .onChange((value: string) => this.workspace.setMailComposeField('to', value))
      TextInput({ text: this.workspace.mailComposeCc, placeholder: tr('cc') }).enabled(this.workspace.mailComposeReady && !this.workspace.mailComposeBlocked)
        .onChange((value: string) => this.workspace.setMailComposeField('cc', value))
      TextInput({ text: this.workspace.mailComposeBcc, placeholder: tr('bcc') }).enabled(this.workspace.mailComposeReady && !this.workspace.mailComposeBlocked)
        .onChange((value: string) => this.workspace.setMailComposeField('bcc', value))
      TextInput({ text: this.workspace.mailComposeSubject, placeholder: tr('subject') }).enabled(this.workspace.mailComposeReady && !this.workspace.mailComposeBlocked)
        .onChange((value: string) => this.workspace.setMailComposeField('subject', value))
      TextArea({ text: this.workspace.mailComposeText, placeholder: tr('message') }).height(Tokens.inputExpanded)
        .enabled(this.workspace.mailComposeReady && !this.workspace.mailComposeBlocked).onChange((value: string) => this.workspace.setMailComposeField('text', value))
      if (this.workspace.mailComposeError) Text(tr(this.workspace.mailComposeError)).fontSize(Tokens.meta).maxFontScale(Tokens.interfaceScaleFor(Tokens.meta)).fontColor($r('app.color.persona_danger'))
      if (this.workspace.mailComposeBlocked) Text(tr('inbox_write_' + this.workspace.mailComposeState)).fontSize(Tokens.meta).maxFontScale(Tokens.interfaceScaleFor(Tokens.meta)).fontColor($r('app.color.persona_danger'))
      Button({ type: ButtonType.Circle }) { if (IconActions.busy(this.mailSendKey())) LoadingProgress().width(Tokens.symbol).height(Tokens.symbol).color(Colors.onAction); else SymbolGlyph(IconActions.symbol(this.mailSendKey())).fontSize(Tokens.symbolFont).fontColor([IconActions.color(this.mailSendKey(), true)]) }.attributeModifier(ButtonStyles.iconPrimary).accessibilityText(tr(this.mailSendKey())).bindTips(tr(this.mailSendKey())).enabled(this.workspace.mailComposeValid() && !this.workspace.mailComposeBusy).onClick(() => this.confirmCompose()).alignSelf(ItemAlign.End)
    }.width('100%').padding(Tokens.page).alignItems(HorizontalAlign.Start)
  }

  @Builder handoffComposer() {
    Column({ space: Tokens.gap }) {
      TextArea({ text: this.handoffText, placeholder: tr('inbox_handoff_instruction') }).height(Tokens.inputExpanded)
        .enabled(!['pending', 'unknown'].includes(this.workspace.writeState))
        .onChange((value: string) => { this.handoffText = value; this.workspace.handoffDraft = value; })
      if (this.workspace.error) Text(tr(this.workspace.error)).fontSize(Tokens.meta).maxFontScale(Tokens.interfaceScaleFor(Tokens.meta)).fontColor($r('app.color.persona_danger'))
      Button({ type: ButtonType.Circle }) { if (IconActions.busy(this.handoffSendKey())) LoadingProgress().width(Tokens.symbol).height(Tokens.symbol).color(Colors.onAction); else SymbolGlyph(IconActions.symbol(this.handoffSendKey())).fontSize(Tokens.symbolFont).fontColor([IconActions.color(this.handoffSendKey(), true)]) }.attributeModifier(ButtonStyles.iconPrimary).accessibilityText(tr(this.handoffSendKey())).bindTips(tr(this.handoffSendKey()))
        .enabled(!!this.handoffText.trim() && this.workspace.canWrite()).onClick(() => this.confirmHandoff()).alignSelf(ItemAlign.End)
    }.width('100%').padding(Tokens.page).alignItems(HorizontalAlign.Start)
  }

  build() {
    Column() {
      if (this.detailOpen) this.detailContent()
      else this.inboxList()
    }.width('100%').height('100%')
      .bindSheet($$this.sheetOpen, this.sheetContent(), { title: { title: tr(this.activeSheet === 'mail' ? 'mail_compose' : 'inbox_handoff_title') }, height: SheetSize.LARGE, showClose: true })
  }
}
