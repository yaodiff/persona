import { IconActions } from '../design/IconActions';
import { Colors } from '../design/Colors';
import { PhoneStyle } from '../design/PhoneStyle';
import { Motion } from '../design/Motion';
import { ButtonStyles } from '../design/Controls';
import { ComposeListItemV2, ContentItemV2, CapsuleSegmentButtonV2, SegmentButtonV2Items } from '@kit.ArkUI';
import { ClientModel } from '../models/ClientModel';
import { MarkdownText } from './MarkdownText';
import { ResourceRow, Wire } from '../models/Wire';
import { BoundRow, reconcileBoundRows } from '../models/RowBinding';
import { Tokens } from '../design/Tokens';
import { tr } from '../design/Strings';
import { Haptics } from '../platform/Haptics';
import { NativeEnvironment, nativeEnvironment } from '../platform/NativeEnvironment';
import { workspaceGridColumns, workspaceGridGutter, workspaceHorizontalPadding } from '../core/WorkspaceGrid';

@ComponentV2
export struct ResourcePage {
  @Require @Param model: ClientModel;
  @Param kind: string = 'todo';
  @Param list: boolean = true;
  @Event onOpen: (row: ResourceRow) => void = () => {};
  @Event onHandoff: (row: ResourceRow) => void = () => {};
  @Event onLink: (address: string) => void = () => {};
  @Local private environment: NativeEnvironment = nativeEnvironment;
  @Local private paneWidth: number = 0;
  @Local rowBindings: BoundRow<ResourceRow>[] = [];
  private bindingOwner: Object | undefined;
  private bindingScope: string = '';
  @Local search: string = '';
  @Local editing: boolean = false;
  @Local editingNew: boolean = false;
  @Local title: string = '';
  @Local content: string = '';
  @Local reply: string = '';
  @Local tag: string = '';
  @Local importance: string = '';
  @Local source: number = 0;
  aboutToAppear(): void { this.syncRows(); }
  @Monitor('model.resourceRows', 'model.profile', 'search', 'source', 'kind') rowsChanged(): void { this.syncRows(); }
  private syncRows(): void {
    const scope = JSON.stringify([this.model.scope(), this.model.profile.id, this.model.profile.manager, this.kind]);
    this.rowBindings = reconcileBoundRows(this.rowBindings, this.rows(), (row: ResourceRow) => row.key(),
      this.bindingOwner, this.model, this.bindingScope, scope);
    this.bindingOwner = this.model; this.bindingScope = scope;
  }
  private rows(): ResourceRow[] {
    return this.model.resourceRows.filter((row: ResourceRow) =>
      (!this.search || (row.title + ' ' + row.detail).toLowerCase().includes(this.search.toLowerCase())) &&
      (this.kind !== 'inbox' || this.source === 0 || Wire.text(row.data(), 'source') === (this.source === 1 ? 'lark' : 'mail')));
  }
  private resourceColumns(): number {
    return workspaceGridColumns(this.paneWidth, this.environment.fontScale, 2);
  }
  private resourceSidePadding(): number {
    return workspaceHorizontalPadding(this.paneWidth);
  }
  private edit(row?: ResourceRow): void {
    Haptics.tap();
    this.editingNew = !row; this.title = row?.title || ''; this.content = row?.detail || '';
    this.tag = row ? Wire.strings(row.data(), 'tags').join(', ') : '';
    this.importance = row ? Wire.text(row.data(), 'importance') : ''; this.editing = true;
  }
  private async save(): Promise<void> {
    const row = this.editingNew ? new ResourceRow('', this.title, this.kind) : this.model.selected;
    row.manager = this.editingNew ? this.model.profile.manager : row.manager;
    const result = row.kind === 'knowledge' ? await this.model.saveKnowledge(row, this.tag, this.importance) : await this.model.saveResource(row, this.title, this.content);
    if (result) { Haptics.success(); this.editing = false; }
  }
  private confirmReply(): void {
    const row = this.model.selected, text = this.reply.trim(), scope = this.model.scope(); if (!text) return;
    Haptics.tap();
    const data = row.data(), from = Wire.object(Wire.object(data, 'mail'), 'from');
    const recipient = Wire.text(from, 'email', Wire.text(data, 'sender', row.title));
    this.getUIContext().showAlertDialog({ title: tr('confirm_send'), message: this.model.profile.name + ' · ' + Wire.text(data, 'account_name') + '\n' + recipient + '\n' + text,
      primaryButton: { value: tr('cancel'), action: () => {} }, secondaryButton: { value: tr('send'), action: () => {
        if (this.model.scope() !== scope || this.model.selected.key() !== row.key()) return;
        this.model.replyTo(row, text).then((sent: boolean) => { if (sent && this.reply.trim() === text) { Haptics.success(); this.reply = ''; } });
      } } });
  }
  private confirmDelete(): void {
    Haptics.attention();
    const row = this.model.selected;
    this.getUIContext().showAlertDialog({ title: tr('delete'), message: row.title,
      primaryButton: { value: tr('cancel'), action: () => {} },
      secondaryButton: { value: tr('delete'), action: () => this.model.deleteResource(row) } });
  }
  @Builder editor() {
    Column({ space: Tokens.page }) {
      if (this.kind === 'knowledge') {
        TextInput({ text: this.tag, placeholder: tr('tags') }).onChange((value: string) => this.tag = value)
        Select([{ value: 'P0' }, { value: 'P1' }, { value: 'P2' }, { value: 'P3' }]).value(this.importance)
          .onSelect((index: number, value: string) => { Haptics.selection(); this.importance = value; })
      } else {
        TextInput({ text: this.title, placeholder: tr('title') }).onChange((value: string) => this.title = value).id('resource.title')
        TextArea({ text: this.content, placeholder: tr('content') }).onChange((value: string) => this.content = value).height(Tokens.inputExpanded)
      }
      if (this.model.error) Text(tr(this.model.error)).fontColor($r('app.color.persona_danger'))
      Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('save')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('save', true)]) }.attributeModifier(ButtonStyles.iconPrimary).accessibilityText(tr('save')).bindTips(tr('save')).enabled(this.kind === 'knowledge' || this.title.trim().length > 0).onClick(() => this.save()).id('resource.save')
    }.padding(Tokens.page).width('100%')
  }
  @Builder listContent() {
    Column({ space: Tokens.gap }) {
      Row({ space: Tokens.gap }) {
        Search({ value: this.search, placeholder: tr('search') }).onChange((value: string) => this.search = value).layoutWeight(1)
        if (this.kind === 'todo') Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('add')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('add', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('add')).bindTips(tr('add')).onClick(() => this.edit()).id('todo.add')
        if (this.kind === 'knowledge') {
          Select(['projects', 'chats', 'people', 'docs', 'meetings', 'events', 'recordings'].map((value: string): SelectOption => ({ value: tr(value) })))
            .value(tr(this.model.knowledgeKind)).onSelect((index: number) => {
              Haptics.selection();
              this.model.knowledgeKind = ['projects', 'chats', 'people', 'docs', 'meetings', 'events', 'recordings'][index]; this.model.listResources('knowledge');
            })
        }
      }.padding({ left: this.resourceSidePadding(), right: this.resourceSidePadding(), top: Tokens.inset, bottom: 2 })
      if (this.kind === 'inbox') {
        CapsuleSegmentButtonV2({ items: new SegmentButtonV2Items([{ text: tr('all') }, { text: 'Lark' }, { text: tr('mail') }]),
          selectedIndex: this.source, $selectedIndex: (index: number) => {
            Haptics.selection();
            Motion.run(this.getUIContext(), Motion.state(this.environment.reduceMotion), () => { this.source = index; });
          } }).padding({ left: this.resourceSidePadding(), right: this.resourceSidePadding() })
      }
      if (this.model.error) Text(tr(this.model.error)).fontSize(Tokens.meta).padding({ left: this.resourceSidePadding(), right: this.resourceSidePadding() })
      List({ space: 10 }) {
        ForEach(this.rowBindings, (binding: BoundRow<ResourceRow>) => {
          ListItem() {
            Column() {
              ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: binding.value.title || binding.value.id, secondaryText: binding.value.detail || undefined }) })
            }.width('100%').padding({ left: 6, right: 6, top: 2, bottom: 2 }).backgroundColor(Colors.surface).borderRadius(PhoneStyle.cardRadius)
              .border({ width: PhoneStyle.surfaceEdgeWidth(0.5), color: PhoneStyle.cardBorder(this.environment.dark), radius: PhoneStyle.cardRadius })
              .shadow(PhoneStyle.cardElevation(this.environment.dark))
          }.onClick(() => { Haptics.tap(); this.onOpen(binding.value); }).id('resource.' + binding.value.id)
        }, (binding: BoundRow<ResourceRow>) => binding.key)
        if (!this.rows().length && !this.model.busy) ListItem() { Text(tr(this.search ? 'no_results' : 'empty')).fontColor(Colors.secondary).padding(Tokens.page) }
      }.lanes(this.resourceColumns(), workspaceGridGutter(this.resourceColumns()))
        .padding({ left: this.resourceSidePadding(), right: this.resourceSidePadding(), top: 4, bottom: Tokens.page })
        .layoutWeight(1).width('100%')
    }.width('100%').height('100%').backgroundColor(Colors.grouped)
  }
  @Builder detailContent() {
    Column() {
      Scroll() {
        Column({ space: Tokens.page }) {
          Column() {
            MarkdownText({ text: this.model.selected.detail, onLink: (address: string) => this.onLink(address) })
          }.width('100%').padding(16).alignItems(HorizontalAlign.Start).backgroundColor(Colors.surface).borderRadius(PhoneStyle.cardRadius)
            .border({ width: PhoneStyle.surfaceEdgeWidth(0.5), color: PhoneStyle.cardBorder(this.environment.dark), radius: PhoneStyle.cardRadius })
            .shadow(PhoneStyle.cardElevation(this.environment.dark))
          Row({ space: Tokens.gap }) {
            if (this.model.selected.kind === 'todo') {
              Toggle({ type: ToggleType.Checkbox, isOn: this.model.selected.isDone() })
                .onChange((done: boolean) => {
                  if (done) Haptics.success(); else Haptics.tap();
                  this.model.saveResource(this.model.selected, this.model.selected.title, this.model.selected.detail, done);
                })
                .accessibilityText(tr('done'))
            }
            if (this.model.selected.kind !== 'inbox') Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('edit')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('edit', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('edit')).bindTips(tr('edit')).onClick(() => this.edit(this.model.selected))
            Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('handoff')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('handoff', true)]) }.attributeModifier(ButtonStyles.iconPrimary).accessibilityText(tr('handoff')).bindTips(tr('handoff')).onClick(() => { Haptics.tap(); this.onHandoff(this.model.selected); })
            if (this.model.selected.kind === 'todo') Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('delete')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('delete', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('delete')).bindTips(tr('delete')).onClick(() => this.confirmDelete())
            if (this.model.selected.kind === 'inbox') Button({ type: ButtonType.Circle }) { if (IconActions.busy(this.model.selected.isDone() ? 'reopen' : 'process')) LoadingProgress().width(Tokens.symbol).height(Tokens.symbol).color(Colors.link); else SymbolGlyph(IconActions.symbol(this.model.selected.isDone() ? 'reopen' : 'process')).fontSize(Tokens.symbolFont).fontColor([IconActions.color(this.model.selected.isDone() ? 'reopen' : 'process', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr(this.model.selected.isDone() ? 'reopen' : 'process')).bindTips(tr(this.model.selected.isDone() ? 'reopen' : 'process'))
              .onClick(() => { Haptics.tap(); this.model.processInbox(this.model.selected); })
          }.width('100%')
          if (this.model.error) Text(tr(this.model.error)).fontColor($r('app.color.persona_danger'))
        }.padding({ left: this.resourceSidePadding(), right: this.resourceSidePadding(), top: Tokens.page, bottom: Tokens.page }).alignItems(HorizontalAlign.Start).width('100%')
      }.layoutWeight(1).width('100%')
      if (this.model.selected.kind === 'inbox') {
        Column({ space: Tokens.gap }) {
          TextArea({ text: this.reply, placeholder: tr('reply') }).height(Tokens.inputExpanded).onChange((value: string) => this.reply = value).id('inbox.reply')
          Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('send')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('send', true)]) }.attributeModifier(ButtonStyles.iconPrimary).accessibilityText(tr('send')).bindTips(tr('send')).enabled(this.reply.trim().length > 0).onClick(() => this.confirmReply()).alignSelf(ItemAlign.End).id('inbox.send')
        }.width('100%').padding({ left: this.resourceSidePadding(), right: this.resourceSidePadding(), top: Tokens.gap, bottom: Tokens.inset })
      }
    }.width('100%').height('100%').backgroundColor(Colors.grouped)
  }
  build() {
    Column() {
      if (this.list) this.listContent()
      else this.detailContent()
    }.width('100%').height('100%')
      .onAreaChange((_old: Area, area: Area) => { const w = Number(area.width); if (Number.isFinite(w) && w > 0 && Math.abs(w - this.paneWidth) > 0.5) this.paneWidth = w; })
      .bindSheet($$this.editing, this.editor(), { title: { title: tr(this.editingNew ? 'create' : 'edit') }, height: SheetSize.LARGE, showClose: true })
  }
}
