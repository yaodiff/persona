import { IconActions } from '../design/IconActions';
import { Colors } from '../design/Colors';
import { ButtonStyles } from '../design/Controls';
import { Binding, UIUtils } from '@kit.ArkUI';
import { common } from '@kit.AbilityKit';
import { dataSharePredicates } from '@kit.ArkData';
import { fileIo, picker } from '@kit.CoreFileKit';
import { photoAccessHelper } from '@kit.MediaLibraryKit';
import { ClientModel } from '../models/ClientModel';
import { HEALTH_SUGGESTIONS, HealthMemoryFile, HealthReport, HealthWorkspace } from '../models/HealthWorkspace';
import { ResourceRow, Wire } from '../models/Wire';
import { BoundRow, reconcileBoundRows } from '../models/RowBinding';
import { MarkdownText } from './MarkdownText';
import { Tokens } from '../design/Tokens';
import { tr } from '../design/Strings';
import { HealthToday } from './HealthToday';
import { HealthConversationHistory, HealthRowDivider } from './HealthConversationHistory';
import { PhoneDictationControl } from './PhoneDictationControl';
import { PhoneStyle } from '../design/PhoneStyle';
import { Motion } from '../design/Motion';
import { PhoneOutlineIcon } from '../design/PhoneOutlineIcon';
import { Haptics } from '../platform/Haptics';
import { NativeEnvironment, nativeEnvironment } from '../platform/NativeEnvironment';
import { compactDraftRoom } from '../core/ComposerSize';
import { workspaceGridColumns, workspaceGridGutter, workspaceHorizontalPadding } from '../core/WorkspaceGrid';

const HEALTH_REPORT_LIMIT: number = 32 * 1024 * 1024;
const HEALTH_REPORT_SELECTION_LIMIT: number = 8;

class HealthPickedReport {
  name: string = '';
  bytes: ArrayBuffer = new ArrayBuffer(0);
  constructor(name: string, bytes: ArrayBuffer) { this.name = name; this.bytes = bytes; }
}

@ComponentV2
export struct HealthWorkspacePage {
  @Require @Param model: ClientModel;
  @Param row: ResourceRow | undefined = undefined;
  @Param list: boolean = true;
  @Event onOpenResource: (row: ResourceRow) => void = (_row: ResourceRow) => {};
  @Event onChat: (row: ResourceRow) => void = (_row: ResourceRow) => {};
  @Event onLink: (url: string) => void = (_url: string) => {};
  @Local private environment: NativeEnvironment = nativeEnvironment;
  @Local private paneWidth: number = 0;
  @Local workspace: HealthWorkspace = new HealthWorkspace();
  @Local conversationRowBindings: BoundRow<ResourceRow>[] = [];
  @Local memoryRowBindings: BoundRow<HealthMemoryFile>[] = [];
  @Local reportRowBindings: BoundRow<HealthReport>[] = [];
  private rowBindingOwner: Object | undefined;
  private rowBindingScope: string = '';
  @Local editingMemory: boolean = false;
  @Local pickerBusy: boolean = false;
  @Local pickerError: string = '';
  private active: boolean = false;
  private routedKey: string = '';
  private pickerRevision: number = 0;

  aboutToAppear(): void {
    this.active = true; this.routedKey = ''; this.pickerRevision++;
    this.syncRowBindings();
    this.workspace.open(this.model).then(() => this.routeRow()).catch(() => undefined);
  }
  aboutToDisappear(): void {
    this.active = false; this.pickerRevision++; this.pickerBusy = false; this.pickerError = ''; this.workspace.hide();
  }

  @Monitor('row') rowChanged(): void { if (this.active) this.workspace.open(this.model).then(() => this.routeRow()).catch(() => undefined); }
  @Monitor('list') listChanged(): void { if (this.active) this.workspace.open(this.model).then(() => this.routeRow()).catch(() => undefined); }
  @Monitor('model.profile') profileChanged(): void {
    this.pickerRevision++; this.pickerBusy = false; this.pickerError = '';
    this.syncRowBindings();
    if (this.active) this.workspace.open(this.model).then(() => this.routeRow()).catch(() => undefined);
  }
  @Monitor('model.connected', 'model.features') connectionChanged(): void {
    if (!this.active) return;
    if (this.model.connected) this.workspace.refresh().catch(() => undefined);
    else this.workspace.intake.close();
  }
  @Monitor('workspace.conversations', 'workspace.reports', 'workspace.memories') rowsChanged(): void { this.syncRowBindings(); }

  private syncRowBindings(): void {
    const scope = JSON.stringify([this.model.scope(), this.model.profile.id, this.model.profile.manager, 'health']);
    this.conversationRowBindings = reconcileBoundRows(this.conversationRowBindings, this.workspace.conversations,
      (row: ResourceRow) => row.key(), this.rowBindingOwner, this.model, this.rowBindingScope, scope);
    this.reportRowBindings = reconcileBoundRows(this.reportRowBindings, this.workspace.reports,
      (report: HealthReport) => report.row.key(), this.rowBindingOwner, this.model, this.rowBindingScope, scope);
    this.memoryRowBindings = reconcileBoundRows(this.memoryRowBindings, this.workspace.memoryEntries(),
      (file: HealthMemoryFile) => file.name, this.rowBindingOwner, this.model, this.rowBindingScope, scope);
    this.rowBindingOwner = this.model; this.rowBindingScope = scope;
  }

  private send(text: string): void {
    if (!text.trim() || !this.active || this.workspace.busy || this.pickerBusy) return;
    Haptics.tap();
    this.workspace.startConversation(text).then((row: ResourceRow | undefined) => {
      if (row && this.active && this.workspace.isHealthConversation(row)) this.onChat(row);
    }).catch(() => undefined);
  }

  private routeRow(): void {
    if (!this.active || this.list || !this.row) return;
    const target = this.row.key();
    if (target === this.routedKey) return;
    this.routedKey = target;
    if (this.row.kind === 'file') this.onOpenResource(this.row);
    else if (this.workspace.isHealthConversation(this.row)) this.onChat(this.row);
  }

  private safeLink(url: string): void { if (/^https?:\/\//i.test(url.trim())) this.onLink(url.trim()); }

  @Builder reportUploadMenu() {
    Menu() {
      MenuItem({ content: tr('health_upload_pdf') }).enabled(!this.pickerBusy && this.workspace.canUploadReports())
        .onClick(() => this.pickReportDocuments())
      MenuItem({ content: tr('health_upload_photos') }).enabled(!this.pickerBusy && this.workspace.canUploadReports())
        .onClick(() => this.pickReportPhotos())
    }
  }

  private pickerOwner(scope: string, manager: string): boolean {
    return this.active && this.model.scope() === scope && this.model.profile.manager === manager;
  }

  private pickerCurrent(scope: string, manager: string, revision: number): boolean {
    return revision === this.pickerRevision && this.pickerOwner(scope, manager);
  }

  private reportName(uri: string): string {
    const leaf = uri.substring(uri.lastIndexOf('/') + 1);
    try { return decodeURIComponent(leaf); } catch (_) { return leaf; }
  }

  private reportPickerError(failure: Error): string {
    return failure.message === 'health_report_file_too_large' ? 'health_report_file_too_large' : 'health_report_file_read_failed';
  }

  private readReport(uri: string, name: string = ''): HealthPickedReport {
    const info = fileIo.statSync(uri);
    if (!Number.isSafeInteger(info.size) || info.size <= 0) throw new Error('health_report_file_read_failed');
    if (info.size > HEALTH_REPORT_LIMIT) throw new Error('health_report_file_too_large');
    const file = fileIo.openSync(uri, fileIo.OpenMode.READ_ONLY);
    try {
      const bytes = new ArrayBuffer(info.size);
      let offset = 0;
      while (offset < info.size) {
        const chunk = new ArrayBuffer(Math.min(256 * 1024, info.size - offset));
        const read = fileIo.readSync(file.fd, chunk, { length: chunk.byteLength });
        if (read <= 0 || read > chunk.byteLength) throw new Error('health_report_file_read_failed');
        new Uint8Array(bytes, offset, read).set(new Uint8Array(chunk, 0, read));
        offset += read;
      }
      if (offset !== info.size) throw new Error('health_report_file_read_failed');
      return new HealthPickedReport(name || this.reportName(uri), bytes);
    } finally { fileIo.closeSync(file.fd); }
  }

  private appendReportPath(path: string): void {
    if (!this.workspace.appendReportPath(path, tr('health_report_prompt')))
      this.pickerError = 'health_report_file_read_failed';
  }

  private async pickReportDocuments(): Promise<void> {
    const scope = this.model.scope(), manager = this.model.profile.manager;
    const revision = ++this.pickerRevision;
    this.pickerBusy = true; this.pickerError = '';
    try {
      const options = new picker.DocumentSelectOptions();
      options.maxSelectNumber = HEALTH_REPORT_SELECTION_LIMIT;
      options.fileSuffixFilters = ['PDF (.pdf)|.pdf'];
      const uris = await new picker.DocumentViewPicker(this.getUIContext().getHostContext() as common.UIAbilityContext).select(options);
      if (!this.pickerCurrent(scope, manager, revision)) return;
      for (const uri of uris) {
        if (!this.pickerCurrent(scope, manager, revision)) return;
        const report = this.readReport(uri);
        const path = await this.workspace.uploadReport(report.name, report.bytes);
        if (!this.pickerCurrent(scope, manager, revision)) return;
        if (!path) return;
        this.appendReportPath(path);
      }
    } catch (failure) {
      if (this.pickerCurrent(scope, manager, revision)) this.pickerError = this.reportPickerError(failure as Error);
    } finally { if (this.pickerCurrent(scope, manager, revision)) this.pickerBusy = false; }
  }

  private async pickReportPhotos(): Promise<void> {
    const scope = this.model.scope(), manager = this.model.profile.manager;
    const revision = ++this.pickerRevision;
    const host = this.getUIContext().getHostContext();
    if (!host) { this.pickerError = 'health_report_file_read_failed'; return; }
    this.pickerBusy = true; this.pickerError = '';
    let helper: photoAccessHelper.PhotoAccessHelper | undefined;
    try {
      const options = new photoAccessHelper.PhotoSelectOptions();
      options.MIMEType = photoAccessHelper.PhotoViewMIMETypes.IMAGE_TYPE;
      options.maxSelectNumber = HEALTH_REPORT_SELECTION_LIMIT;
      const selected = await new photoAccessHelper.PhotoViewPicker().select(options);
      if (!this.pickerCurrent(scope, manager, revision)) return;
      helper = photoAccessHelper.getPhotoAccessHelper(host as common.UIAbilityContext);
      for (const uri of selected.photoUris) {
        if (!this.pickerCurrent(scope, manager, revision)) return;
        const predicates = new dataSharePredicates.DataSharePredicates().equalTo('uri', uri);
        const result = await helper.getAssets({ fetchColumns: ['uri', 'display_name', 'media_type'], predicates: predicates });
        let asset: photoAccessHelper.PhotoAsset | undefined;
        try {
          if (!this.pickerCurrent(scope, manager, revision)) return;
          if (result.getCount() !== 1) throw new Error('health_report_file_read_failed');
          asset = await result.getFirstObject();
        } finally { result.close(); }
        if (!this.pickerCurrent(scope, manager, revision)) return;
        if (!asset || asset.photoType !== photoAccessHelper.PhotoType.IMAGE || asset.uri !== uri) throw new Error('health_report_file_read_failed');
        const report = this.readReport(asset.uri, asset.displayName || this.reportName(asset.uri));
        const path = await this.workspace.uploadReport(report.name, report.bytes);
        if (!this.pickerCurrent(scope, manager, revision)) return;
        if (!path) return;
        this.appendReportPath(path);
      }
    } catch (failure) {
      if (this.pickerCurrent(scope, manager, revision)) this.pickerError = this.reportPickerError(failure as Error);
    } finally {
      try { await helper?.release(); } catch (_) {}
      if (this.pickerCurrent(scope, manager, revision)) this.pickerBusy = false;
    }
  }

  private confirmDeleteMemory(file: HealthMemoryFile): void {
    if (!this.workspace.canEditMemory() || this.workspace.selectedMemory?.name !== file.name) return;
    const generation = this.model.connectionGeneration(), scope = this.model.scope(), target = file.name;
    this.getUIContext().showAlertDialog({ title: tr('health_memory_delete_title'), message: file.name,
      primaryButton: { value: tr('cancel'), action: () => {} },
      secondaryButton: { value: tr('delete'), action: () => {
        if (!this.active || !this.model.owns(generation, scope) || this.workspace.selectedMemory?.name !== target) return;
        this.workspace.deleteMemory().catch(() => false);
      } } });
  }

  private confirmDeleteReport(report: HealthReport): void {
    const generation = this.model.connectionGeneration(), scope = this.model.scope(), target = report.row.key();
    this.getUIContext().showAlertDialog({ title: tr('health_report_delete_title'), message: tr('health_report_delete_message'),
      primaryButton: { value: tr('cancel'), action: () => {} },
      secondaryButton: { value: tr('delete'), action: () => {
        if (!this.active || !this.model.owns(generation, scope) || !this.workspace.reports.some((item: HealthReport) => item.row.key() === target)) return;
        this.workspace.deleteReport(report).catch(() => false);
      } } });
  }

  private openReport(report: HealthReport, record: boolean): void {
    Haptics.tap();
    const row = this.workspace.openReport(report, record);
    if (row) this.onOpenResource(row);
  }

  private healthColumns(): number {
    return workspaceGridColumns(this.paneWidth, this.environment.fontScale, 2);
  }

  private healthSidePadding(): number {
    return workspaceHorizontalPadding(this.paneWidth);
  }

  @Builder memoryDetail(binding: Binding<HealthMemoryFile>) {
    Column({ space: Tokens.gap }) {
      Row({ space: Tokens.gap }) {
        Text(binding.value.name === 'MEMORY.md' ? tr('health_memory_index') : binding.value.name).fontSize(Tokens.body).fontWeight(FontWeight.Medium).layoutWeight(1)
        if (binding.value.name !== 'MEMORY.md' && !this.editingMemory) Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('health_memory_edit')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('health_memory_edit', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('health_memory_edit')).bindTips(tr('health_memory_edit'))
          .onClick(() => { Haptics.tap(); this.editingMemory = true; }).id('health.memory.edit')
      }.width('100%')
      if (this.editingMemory && binding.value.name !== 'MEMORY.md') {
        TextArea({ text: this.workspace.memoryDraft, placeholder: tr('health_memory_content') }).height(Tokens.inputExpanded)
          .onChange((value: string) => this.workspace.setMemoryDraft(value)).id('health.memory.editor')
        if (this.workspace.memoryError) Text(tr(this.workspace.memoryError)).fontSize(Tokens.meta)
          .fontColor($r('app.color.persona_danger')).width('100%')
        Row({ space: Tokens.gap }) {
          Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('cancel')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('cancel', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('cancel')).bindTips(tr('cancel')).onClick(() => {
            Haptics.tap();
            this.editingMemory = false; this.workspace.openMemory(binding.value);
          })
          Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('save')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('save', true)]) }.attributeModifier(ButtonStyles.iconPrimary).accessibilityText(tr('save')).bindTips(tr('save')).enabled(this.workspace.canSaveMemory())
            .onClick(() => this.workspace.saveMemory().then((saved: boolean) => { if (saved) { Haptics.success(); this.editingMemory = false; } }).catch(() => false))
            .id('health.memory.save')
          Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('delete')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('delete', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('delete')).bindTips(tr('delete')).enabled(this.workspace.canEditMemory() && !this.workspace.busy)
            .onClick(() => { Haptics.attention(); this.confirmDeleteMemory(binding.value); }).id('health.memory.delete')
        }.width('100%')
      } else {
        MarkdownText({ text: this.workspace.memoryDraft, onLink: (url: string) => this.safeLink(url) })
        if (this.workspace.memoryError) Text(tr(this.workspace.memoryError)).fontSize(Tokens.meta)
          .fontColor($r('app.color.persona_danger')).width('100%')
        if (binding.value.name !== 'MEMORY.md') Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('delete')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('delete', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('delete')).bindTips(tr('delete')).enabled(this.workspace.canEditMemory() && !this.workspace.busy)
          .onClick(() => { Haptics.attention(); this.confirmDeleteMemory(binding.value); }).id('health.memory.delete')
      }
    }.width('100%').alignItems(HorizontalAlign.Start).padding(Tokens.inset)
      .backgroundColor($r('sys.color.comp_background_tertiary')).borderRadius(PhoneStyle.cardRadius)
      .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.cardBorder(this.environment.dark),radius:PhoneStyle.cardRadius})
      .animation(Motion.layout(this.environment.reduceMotion))
  }

  @Builder conversationSection() {
    HealthConversationHistory({model:this.model,workspace:this.workspace,rows:this.conversationRowBindings,
      onOpen:(row: ResourceRow) => { if (this.active) { Haptics.tap(); this.onChat(row); } }})
  }

  @Builder memorySection() {
    Column({ space: Tokens.gap }) {
      Text(tr('health_memory')).fontSize(Tokens.body).fontWeight(600).fontColor(Colors.ink).width('100%')
      if (this.workspace.loading && !this.workspace.memories.length) LoadingProgress().width(Tokens.touch).height(Tokens.touch)
      else if (!this.workspace.memoryEntries().length && !this.workspace.indexMemory()) Text(tr('health_no_memory'))
        .fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).width('100%')
      ForEach(this.memoryRowBindings, (binding: BoundRow<HealthMemoryFile>) => Button({ type: ButtonType.Normal }) { Row({ space: Tokens.inset }) { Text(binding.value.name).fontSize(Tokens.body).fontColor(Colors.ink).layoutWeight(1); SymbolGlyph($r('sys.symbol.chevron_right')).fontSize(Tokens.meta).fontColor([Colors.secondary]) }.width('100%').constraintSize({ minHeight: Tokens.touch }) }.buttonStyle(ButtonStyleMode.TEXTUAL).backgroundColor(Color.Transparent).padding({ left: Tokens.page, right: Tokens.page }).width('100%')
        .onClick(() => { Haptics.tap(); this.editingMemory = false; this.workspace.openMemory(binding.value); }).id('health.memory.' + binding.value.name),
        (binding: BoundRow<HealthMemoryFile>) => binding.key)
      if (this.workspace.indexMemory()) Button({ type: ButtonType.Normal }) { Row({ space: Tokens.inset }) { Text(tr('health_memory_index')).fontSize(Tokens.body).fontColor(Colors.ink).layoutWeight(1); SymbolGlyph($r('sys.symbol.chevron_right')).fontSize(Tokens.meta).fontColor([Colors.secondary]) }.width('100%').constraintSize({ minHeight: Tokens.touch }) }.buttonStyle(ButtonStyleMode.TEXTUAL).backgroundColor(Color.Transparent).width('100%').padding({ left: Tokens.page, right: Tokens.page })
        .onClick(() => { Haptics.tap(); this.editingMemory = false; const index = this.workspace.indexMemory(); if (index) this.workspace.openMemory(index); })
        .id('health.memory.index')
      if (this.workspace.selectedMemory) this.memoryDetail(UIUtils.makeBinding((): HealthMemoryFile => this.workspace.selectedMemory || new HealthMemoryFile()))
    }.width('100%').alignItems(HorizontalAlign.Start).padding(16).backgroundColor(Colors.surface).borderRadius(Tokens.radiusPanel)
      .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.cardBorder(this.environment.dark),radius:Tokens.radiusPanel})
      .shadow(PhoneStyle.cardElevation(this.environment.dark))
  }

  @Builder reportsSection() {
    Column({ space: Tokens.gap }) {
      Text(tr('health_checkup_reports')).fontSize(Tokens.body).fontWeight(600).fontColor(Colors.ink).width('100%')
      if (this.workspace.loading && !this.workspace.reports.length) LoadingProgress().width(Tokens.touch).height(Tokens.touch)
      else if (!this.workspace.reports.length && !this.workspace.error) Text(tr('health_no_reports'))
        .fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).width('100%')
      ForEach(this.reportRowBindings, (binding: BoundRow<HealthReport>) => {
        Column({ space: Tokens.fine }) {
          Text(binding.value.title || binding.value.name).fontSize(Tokens.body).fontWeight(FontWeight.Medium).maxLines(2).width('100%')
          if (!binding.value.recordPath) Text(tr('health_report_not_summarized')).fontSize(Tokens.meta)
            .fontColor($r('sys.color.font_secondary')).width('100%')
          Row({ space: Tokens.gap }) {
            if (binding.value.recordPath) Button({ type: ButtonType.Normal }) { Row({ space: Tokens.inset }) { Text(tr('health_report_summary')).fontSize(Tokens.body).fontColor(Colors.ink).layoutWeight(1); SymbolGlyph($r('sys.symbol.chevron_right')).fontSize(Tokens.meta).fontColor([Colors.secondary]) }.width('100%').constraintSize({ minHeight: Tokens.touch }) }.buttonStyle(ButtonStyleMode.TEXTUAL).backgroundColor(Color.Transparent).width('100%').padding({ left: Tokens.page, right: Tokens.page }).layoutWeight(1)
              .onClick(() => this.openReport(binding.value, true)).id('health.report.summary.' + binding.value.name)
            if (binding.value.path) Button({ type: ButtonType.Normal }) { Row({ space: Tokens.inset }) { Text(tr('health_report_original')).fontSize(Tokens.body).fontColor(Colors.ink).layoutWeight(1); SymbolGlyph($r('sys.symbol.chevron_right')).fontSize(Tokens.meta).fontColor([Colors.secondary]) }.width('100%').constraintSize({ minHeight: Tokens.touch }) }.buttonStyle(ButtonStyleMode.TEXTUAL).backgroundColor(Color.Transparent).width('100%').padding({ left: Tokens.page, right: Tokens.page }).layoutWeight(1)
              .onClick(() => this.openReport(binding.value, false)).id('health.report.original.' + binding.value.name)
            Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('delete')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('delete', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('delete')).bindTips(tr('delete')).enabled(!this.workspace.busy)
              .onClick(() => { Haptics.attention(); this.confirmDeleteReport(binding.value); }).id('health.report.delete.' + binding.value.name)
          }.width('100%')
        }.width('100%').alignItems(HorizontalAlign.Start).padding(Tokens.inset)
          .backgroundColor($r('sys.color.comp_background_tertiary')).borderRadius(PhoneStyle.cardRadius)
          .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.cardBorder(this.environment.dark),radius:PhoneStyle.cardRadius})
      }, (binding: BoundRow<HealthReport>) => binding.key)
    }.width('100%').alignItems(HorizontalAlign.Start).padding(16).backgroundColor(Colors.surface).borderRadius(Tokens.radiusPanel)
      .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.cardBorder(this.environment.dark),radius:Tokens.radiusPanel})
      .shadow(PhoneStyle.cardElevation(this.environment.dark))
  }

  private reportTint(): ResourceColor { return Colors.healthRose; }

  @Builder suggestionLabel(key: string, upload: boolean) {
    Row({space:16}) {
      Stack() {
        if (upload) {
          SymbolGlyph($r('sys.symbol.doc_plaintext')).fontSize(String(PhoneStyle.formSize(24,32,this.environment.fontScale))+'vp')
            .fontColor([this.reportTint()]).accessibilityLevel('no')
        } else {
          PhoneOutlineIcon({name:'sparkles',iconScale:PhoneStyle.formSize(1,4/3,this.environment.fontScale),color:this.reportTint()})
        }
      }.width(PhoneStyle.formSize(24,32,this.environment.fontScale))
      Text(tr(key)).fontSize(17).maxFontScale(Tokens.interfaceScaleFor(17)).fontColor(upload ? this.reportTint() : Colors.ink)
        .layoutWeight(1).maxLines(1).textOverflow({overflow:TextOverflow.Ellipsis})
    }.width('100%').padding({left:20,right:20})
  }

  @Builder suggestionDivider(key: string) {
    if (key !== HEALTH_SUGGESTIONS[HEALTH_SUGGESTIONS.length-1]) {
      HealthRowDivider(36+PhoneStyle.formSize(24,32,this.environment.fontScale))
    }
  }

  @Builder suggestions() {
    Column() {
      Text(tr('health_try_asking')).fontSize(15).maxFontScale(Tokens.interfaceScaleFor(15)).fontColor(Colors.secondary)
        .width('100%').height(PhoneStyle.formSize(40.3,47.7,this.environment.fontScale)).padding({left:20,right:20})
      Column() {
        Button({type:ButtonType.Normal}) { this.suggestionLabel('health_upload_report', true) }
          .width('100%').constraintSize({minHeight:PhoneStyle.formSize(52.3,59.3,this.environment.fontScale)})
          .padding(0).buttonStyle(ButtonStyleMode.TEXTUAL).backgroundColor(Color.Transparent)
          .overlay(this.suggestionDivider('health_upload_report'),{align:Alignment.Bottom})
          .accessibilityText(tr('health_upload_report')).bindMenu(this.reportUploadMenu)
          .enabled(!this.pickerBusy && this.workspace.canUploadReports()).id('health.report.upload')
        if (this.workspace.error) Text(tr(this.workspace.error)).fontSize(15).maxFontScale(Tokens.interfaceScaleFor(15))
          .fontColor(Colors.secondary).width('100%').padding({left:20,right:20})
        if (this.pickerBusy || this.workspace.reportUploadState === 'sending') Row({space:Tokens.gap}) {
          LoadingProgress().width(Tokens.touch).height(Tokens.touch)
          Text(tr('health_report_uploading')).fontSize(15).maxFontScale(Tokens.interfaceScaleFor(15)).fontColor(Colors.secondary)
        }.width('100%').padding({left:20,right:20})
        if (this.pickerError) Text(tr(this.pickerError)).fontSize(15).maxFontScale(Tokens.interfaceScaleFor(15))
          .fontColor(Colors.danger).width('100%').padding({left:20,right:20})
        ForEach(HEALTH_SUGGESTIONS, (key: string) => {
          Button({type:ButtonType.Normal}) { this.suggestionLabel(key, false) }
            .width('100%').constraintSize({minHeight:PhoneStyle.formSize(52.3,59.3,this.environment.fontScale)})
            .padding(0).buttonStyle(ButtonStyleMode.TEXTUAL).backgroundColor(Color.Transparent)
            .overlay(this.suggestionDivider(key),{align:Alignment.Bottom})
            .accessibilityText(tr(key)).enabled(this.workspace.canStartConversation() && !this.pickerBusy)
            .onClick(() => this.send(tr(key))).id('health.suggestion.'+key)
        }, (key: string) => key)
      }.width('100%').alignItems(HorizontalAlign.Start).backgroundColor(Colors.surface).borderRadius(Tokens.radiusPanel)
        .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.cardBorder(this.environment.dark),radius:Tokens.radiusPanel})
        .shadow(PhoneStyle.cardElevation(this.environment.dark)).clip(true)
    }.width('100%').alignItems(HorizontalAlign.Start)
  }

  @Builder privacy() {
    Column({space:Tokens.gap}) {
      Row({space:Tokens.gap}) {
        SymbolGlyph($r('sys.symbol.checkmark_shield')).fontSize(Tokens.interfaceFontVp(17,this.environment.fontScale))
          .fontColor([Colors.ink]).accessibilityLevel('no')
        Text(tr('privacy')).fontSize(Tokens.typeHeading).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeHeading)).fontWeight(600).fontColor(Colors.ink)
      }
      Text(tr('health_privacy_note')).fontSize(Tokens.typeSecondary).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSecondary)).fontColor(Colors.secondary)
        .width('100%').id('health.privacy.note')
    }.width('100%').alignItems(HorizontalAlign.Start).padding({left:20,right:20,top:16,bottom:16})
      .backgroundColor(Colors.surface).borderRadius(Tokens.radiusPanel)
      .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.cardBorder(this.environment.dark),radius:Tokens.radiusPanel})
      .shadow(PhoneStyle.cardElevation(this.environment.dark)).id('health.privacy')
  }

  @Builder home() {
    Scroll() {
      Column({ space: 14 }) {
        HealthToday({intake:this.workspace.intake,model:this.model,resolving:this.workspace.loading,unavailable:this.workspace.error,onLink:(link:string) => this.onLink(link)})
        if (this.healthColumns() > 1) {
          GridRow({ columns: this.healthColumns(), gutter: { x: workspaceGridGutter(this.healthColumns()), y: 14 } }) {
            GridCol({ span: 1 }) { this.suggestions() }
            GridCol({ span: 1 }) {
              Column({ space: 14 }) {
                this.reportsSection()
                this.conversationSection()
              }.width('100%').alignItems(HorizontalAlign.Start)
            }
            GridCol({ span: 1 }) { this.memorySection() }
            GridCol({ span: 1 }) { this.privacy() }
          }.width('100%')
        } else {
          this.suggestions()
          this.reportsSection()
          this.conversationSection()
          this.memorySection()
          this.privacy()
        }
      }.padding({left:this.healthSidePadding(),right:this.healthSidePadding(),bottom:Tokens.page,top:PhoneStyle.formSize(4,0,this.environment.fontScale)})
        .width('100%').alignItems(HorizontalAlign.Start)
    }.scrollable(ScrollDirection.Vertical).scrollBar(BarState.Auto).layoutWeight(1).width('100%')
  }

  private draftOwner(): string {
    return this.workspace.composerOwner();
  }

  private draftActive(owner: string): boolean {
    return this.active && this.model.connected && owner===this.draftOwner() && this.workspace.canStartConversation() && !this.pickerBusy;
  }

  private composerControl(): number {
    return PhoneStyle.formSize(48.3,68,this.environment.fontScale);
  }

  @Computed get composerBottomPadding(): number {
    const env = this.environment;
    if (!env.isFullScreen || !env.safeBottomPx || env.keyboardHeightPx > 0) return Tokens.gap;
    const ui = this.getUIContext();
    const height = ui.px2vp(env.windowHeightPx);
    const room = compactDraftRoom(ui.px2vp(env.windowWidthPx), height, this.paneWidth, ui.px2vp(env.safeTopPx),
      Math.max(0, height - ui.px2vp(env.safeBottomPx)), 0, env.isFullScreen, env.nativeHoverAvailable);
    return Tokens.gap + (room?.bottomLift ?? 0);
  }

  @Builder composer() {
    Column() {
      Row({space:8}) {
      Button({type:ButtonType.Circle}) {
        SymbolGlyph($r('sys.symbol.plus')).fontSize(String(PhoneStyle.formSize(22,30,this.environment.fontScale))+'vp').fontColor([Colors.ink])
      }.width(this.composerControl()).height(this.composerControl()).padding(0).backgroundColor(Color.Transparent).accessibilityText(tr('health_upload_report'))
        .bindMenu(this.reportUploadMenu).enabled(!this.pickerBusy && this.workspace.canUploadReports()).id('health.composer.add')
      TextArea({ text: this.workspace.draft, placeholder: tr('health_ask_placeholder') })
        .height(this.workspace.draft.includes('\n') ? 80 : this.composerControl()).fontSize(17).maxFontScale(Tokens.interfaceScaleFor(17))
        .padding({left:0,right:0,top:PhoneStyle.formSize(13,8,this.environment.fontScale),bottom:4}).fontColor(Colors.ink).backgroundColor(Color.Transparent).layoutWeight(1)
        .onChange((value:string) => this.workspace.setDraft(value)).id('health.composer')
      if (this.workspace.draft.trim()) {
        Button({type:ButtonType.Circle}) {
          SymbolGlyph(IconActions.symbol('send')).fontSize(19).fontColor([Colors.onAction])
        }.attributeModifier(ButtonStyles.iconPrimary).accessibilityText(tr('send')).bindTips(tr('send'))
          .enabled(this.draftActive(this.draftOwner())).onClick(() => this.send(this.workspace.draft)).id('health.send')
      } else {
        PhoneDictationControl({owner:this.draftOwner(),target:this.composerControl(),symbolSize:PhoneStyle.formSize(21,28,this.environment.fontScale),resting:true,
          active:this.model.connected && this.workspace.canStartConversation() && !this.pickerBusy,language:this.model.language,
          chooseLanguage:() => this.model.dictationLocaleForChat(),onListeningIn:(locale:string) => this.model.rememberDictationLocale(locale),
          allowed:(owner:string):boolean => this.draftActive(owner),
          keyboard:() => this.getUIContext().getFocusController().requestFocus('health.composer'),
          insert:(text:string,owner:string):boolean => {
            if (!this.draftActive(owner) || !text) return false;
            const draft=this.workspace.draft,separator=/[a-zA-Z0-9]$/.test(draft) ? ' ' : '';
            if (draft.length+separator.length+text.length>Tokens.maxMessage) return false;
            this.workspace.setDraft(draft+separator+text);
            return true;
          }})
      }
    }.width('100%').padding({left:4,right:4,top:2,bottom:2}).backgroundColor(Colors.surface).borderRadius(this.composerControl()/2+2)
      .outline({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.floatingBorder(this.environment.dark),radius:this.composerControl()/2+2})
      .shadow(PhoneStyle.cardElevation(this.environment.dark))
    }.width('100%').padding({left:this.healthSidePadding(),right:this.healthSidePadding(),top:8,bottom:this.composerBottomPadding})
  }

  build() {
    Column() {
      this.home()
      this.composer()
    }.width('100%').height('100%').backgroundColor(Colors.grouped)
      .onAreaChange((_old: Area, area: Area) => { this.paneWidth = Number(area.width); })
  }
}
