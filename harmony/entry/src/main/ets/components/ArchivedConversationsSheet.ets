import { SymbolGlyphModifier } from '@kit.ArkUI';
import { sidebarConversationTitle } from '../models/AssistantRecentChats';
import { Colors } from '../design/Colors';
import { ButtonStyles } from '../design/Controls';
import { IconActions } from '../design/IconActions';
import { PersonaSheetHeader } from '../design/NormalizedPrimitives';
import { PhoneStyle } from '../design/PhoneStyle';
import { tr, trError } from '../design/Strings';
import { Symbols } from '../design/Symbols';
import { Tokens } from '../design/Tokens';
import { ClientModel } from '../models/ClientModel';
import { segment } from '../models/HubRepository';
import { toastCenter } from '../models/ToastCenter';
import { ResourceRow, Wire } from '../models/Wire';
import { Haptics } from '../platform/Haptics';

type ArchivedTab = 'chats' | 'agents';

/**
 * Archived assistant conversations & agent sessions list matching iOS `ArchivedConversationsView.swift`
 * and `server/internal/assistant/agent_archives.go`.
 */
@ComponentV2
export struct ArchivedConversationsSheet {
  @Require @Param model: ClientModel;
  @Param initialTab: ArchivedTab = 'chats';
  @Event onOpen: (row: ResourceRow) => void = () => {};
  @Event onClose: () => void = () => {};

  @Local private tab: ArchivedTab = 'chats';
  @Local private query: string = '';
  @Local private busyKeys: string[] = [];
  @Local private archivedAgents: ResourceRow[] = [];
  @Local private loadingAgents: boolean = false;
  @Local private agentsError: string = '';
  @Local private agentsLoaded: boolean = false;
  private agentsRevision: number = 0;

  aboutToAppear(): void {
    this.tab = this.initialTab;
    if (this.tab === 'agents') {
      void this.loadArchivedAgents();
    }
  }

  @Monitor('model', 'model.connected', 'model.profile')
  onModelChanged(): void {
    this.archivedAgents = [];
    this.agentsLoaded = false;
    this.agentsError = '';
    if (this.tab === 'agents') {
      void this.loadArchivedAgents();
    }
  }

  private selectTab(next: ArchivedTab): void {
    if (this.tab === next) return;
    Haptics.selection();
    this.tab = next;
    if (next === 'agents' && !this.agentsLoaded) {
      void this.loadArchivedAgents();
    }
  }

  private async loadArchivedAgents(): Promise<void> {
    const owner = this.model;
    const rev = ++this.agentsRevision;
    if (!owner.connected) {
      this.archivedAgents = [];
      this.loadingAgents = false;
      this.agentsError = tr('offline');
      return;
    }
    const generation = owner.connectionGeneration();
    const scope = owner.scope();
    this.loadingAgents = true;
    this.agentsError = '';
    try {
      const res = await owner.repo().call('/api/v1/sessions/archived');
      if (rev !== this.agentsRevision || owner !== this.model || !owner.owns(generation, scope)) return;
      const rows = Wire.rows(res, 'sessions').map((item: Record<string, Object>) => {
        const row = ResourceRow.from(item, 'session');
        row.group = owner.profile.id;
        row.manager = Wire.text(item, 'manager', owner.profile.manager);
        if (!row.machine) row.machine = Wire.text(item, 'machine_id');
        if (!row.session) row.session = Wire.text(item, 'session_id') || row.id;
        return row;
      });
      this.archivedAgents = rows;
      this.agentsLoaded = true;
      this.agentsError = '';
    } catch (err) {
      if (rev !== this.agentsRevision || owner !== this.model || !owner.owns(generation, scope)) return;
      this.agentsError = trError((err as Error).message || 'failed');
    } finally {
      if (rev === this.agentsRevision && owner === this.model && owner.owns(generation, scope)) {
        this.loadingAgents = false;
      }
    }
  }

  @Computed get archivedRows(): ResourceRow[] {
    const cleanQuery = this.query.trim().toLowerCase();
    const list = this.model.conversations.filter((row: ResourceRow) => Wire.bool(row.data(), 'archived'));
    const sorted = list.slice().sort((a: ResourceRow, b: ResourceRow) => {
      const ta = Date.parse(Wire.text(a.data(), 'updated_at', Wire.text(a.data(), 'created_at'))) || 0;
      const tb = Date.parse(Wire.text(b.data(), 'updated_at', Wire.text(b.data(), 'created_at'))) || 0;
      return tb - ta;
    });
    if (!cleanQuery) return sorted;
    return sorted.filter((row: ResourceRow) => {
      const title = sidebarConversationTitle(row, tr).toLowerCase();
      return title.includes(cleanQuery) || row.id.toLowerCase().includes(cleanQuery);
    });
  }

  @Computed get filteredAgents(): ResourceRow[] {
    const cleanQuery = this.query.trim().toLowerCase();
    const sorted = this.archivedAgents.slice().sort((a: ResourceRow, b: ResourceRow) => {
      const ta = Date.parse(Wire.text(a.data(), 'updated_at', Wire.text(a.data(), 'created_at'))) || 0;
      const tb = Date.parse(Wire.text(b.data(), 'updated_at', Wire.text(b.data(), 'created_at'))) || 0;
      return tb - ta;
    });
    if (!cleanQuery) return sorted;
    return sorted.filter((row: ResourceRow) => {
      const data = row.data();
      const haystack = [
        row.title,
        this.model.machineName(row.machine),
        row.machine,
        row.path,
        Wire.text(data, 'cwd'),
        Wire.text(data, 'launcher'),
        Wire.text(data, 'last_text')
      ].join(' ').toLowerCase();
      return haystack.includes(cleanQuery);
    });
  }

  private rowSessionId(row: ResourceRow): string {
    return row.session || row.id;
  }

  private rowBusyKey(row: ResourceRow): string {
    return JSON.stringify([this.model.scope(), this.model.connectionGeneration(), row.machine, this.rowSessionId(row)]);
  }

  private isBusy(row: ResourceRow): boolean {
    return this.busyKeys.includes(this.rowBusyKey(row));
  }

  private formattedUpdatedAt(row: ResourceRow): string {
    const raw = Wire.text(row.data(), 'updated_at', Wire.text(row.data(), 'created_at'));
    if (!raw) return '';
    const ms = Date.parse(raw);
    if (!Number.isFinite(ms)) return '';
    try {
      return new Date(ms).toLocaleDateString();
    } catch (_) {
      return '';
    }
  }

  private async unarchive(row: ResourceRow): Promise<void> {
    const owner = this.model;
    const id = this.rowSessionId(row);
    if (!owner.connected || !id) return;
    const path = '/api/v1/assistant/conversations/' + segment(id) + '/unarchive';
    if (owner.operationBlocked(path)) {
      toastCenter.error(tr('pending_action'));
      return;
    }
    const busyKey = this.rowBusyKey(row);
    if (this.busyKeys.includes(busyKey)) return;
    const generation = owner.connectionGeneration();
    const scope = owner.scope();
    this.busyKeys = this.busyKeys.concat([busyKey]);
    let ok = false;
    try {
      await owner.repo().call(path, 'POST', {}, '', () => owner === this.model && owner.owns(generation, scope));
      ok = true;
      if (owner === this.model && owner.owns(generation, scope)) {
        const updated = owner.conversations.map((item: ResourceRow) => {
          if (item !== row && this.rowSessionId(item) !== id) return item;
          const data = item.data();
          data['archived'] = false;
          const next = ResourceRow.from(data, item.kind);
          next.group = item.group;
          next.manager = item.manager;
          next.machine = item.machine;
          next.session = item.session || item.id;
          return next;
        });
        owner.conversations = updated;
        Haptics.success();
        toastCenter.success(tr('unarchived_toast'));
      }
    } catch (error) {
      if (owner === this.model && owner.owns(generation, scope)) {
        owner.reportError(error as Error);
        Haptics.error();
        toastCenter.error(trError(owner.error || 'failed'));
      }
    } finally {
      this.busyKeys = this.busyKeys.filter((entry: string) => entry !== busyKey);
    }
    if (ok && owner === this.model && owner.owns(generation, scope)) {
      try { await owner.refresh(); } catch (_) {}
    }
  }

  private async unarchiveAgent(row: ResourceRow): Promise<void> {
    const owner = this.model;
    const machine = row.machine;
    const sid = this.rowSessionId(row);
    if (!owner.connected || !machine || !sid) return;
    const path = '/api/v1/sessions/' + segment(machine) + '/' + segment(sid) + '/unarchive';
    if (owner.operationBlocked(path)) {
      toastCenter.error(tr('pending_action'));
      return;
    }
    const busyKey = this.rowBusyKey(row);
    if (this.busyKeys.includes(busyKey)) return;
    const generation = owner.connectionGeneration();
    const scope = owner.scope();
    this.busyKeys = this.busyKeys.concat([busyKey]);
    let ok = false;
    try {
      await owner.repo().call(path, 'POST', {}, '', () => owner === this.model && owner.owns(generation, scope));
      ok = true;
      if (owner === this.model && owner.owns(generation, scope)) {
        this.archivedAgents = this.archivedAgents.filter((item: ResourceRow) =>
          !(item.machine === machine && this.rowSessionId(item) === sid)
        );
        Haptics.success();
        toastCenter.success(tr('unarchived_toast'));
      }
    } catch (error) {
      if (owner === this.model && owner.owns(generation, scope)) {
        owner.reportError(error as Error);
        Haptics.error();
        toastCenter.error(trError(owner.error || 'failed'));
      }
    } finally {
      this.busyKeys = this.busyKeys.filter((entry: string) => entry !== busyKey);
    }
    if (ok && owner === this.model && owner.owns(generation, scope)) {
      try { await owner.refresh(); } catch (_) {}
    }
  }

  @Builder rowContextMenu(row: ResourceRow, isAgent: boolean = false) {
    Menu() {
      MenuItem({
        content: tr('unarchive'),
        symbolStartIcon: new SymbolGlyphModifier($r('sys.symbol.archivebox'))
      })
        .enabled(!this.isBusy(row))
        .onClick(() => {
          if (isAgent) void this.unarchiveAgent(row);
          else void this.unarchive(row);
        })
        .id('archived.menu.unarchive.' + this.rowSessionId(row))
    }
  }

  @Builder searchBar() {
    Row({ space: Tokens.spaceS }) {
      SymbolGlyph(Symbols.search)
        .fontSize('15vp')
        .fontColor([Colors.secondary])
      TextInput({ text: this.query, placeholder: tr('archived_search_placeholder') })
        .layoutWeight(1)
        .height(Tokens.controlCompact)
        .fontSize(Tokens.typeSecondary)
        .fontColor(Colors.ink)
        .placeholderColor(Colors.secondary)
        .backgroundColor(Color.Transparent)
        .padding({ left: 0, right: 0 })
        .onChange((value: string) => {
          this.query = value;
        })
        .id('archived.search')
      if (this.query.length > 0) {
        Button({ type: ButtonType.Circle }) {
          SymbolGlyph(IconActions.symbol('close'))
            .fontSize('13vp')
            .fontColor([Colors.secondary])
        }
        .width(28)
        .height(28)
        .padding(0)
        .backgroundColor(Color.Transparent)
        .responseRegion({ x: -8, y: -8, width: Tokens.controlBar, height: Tokens.controlBar })
        .accessibilityText(tr('clear'))
        .onClick(() => {
          this.query = '';
        })
      }
    }
    .width('100%')
    .padding({ left: Tokens.spaceM, right: Tokens.spaceS, top: Tokens.spaceXs, bottom: Tokens.spaceXs })
    .backgroundColor(Colors.control)
    .borderRadius(Tokens.radiusCard)
    .alignItems(VerticalAlign.Center)
  }

  @Builder emptyState(title: string, subtitle: string) {
    Column({ space: Tokens.spaceS }) {
      Stack({ alignContent: Alignment.Center }) {
        SymbolGlyph($r('sys.symbol.archivebox'))
          .fontSize('26vp')
          .fontColor([Colors.secondary])
      }
      .width(56)
      .height(56)
      .borderRadius(Tokens.radiusPanel)
      .backgroundColor(Colors.control)

      Text(title)
        .fontSize(Tokens.typeHeading)
        .maxFontScale(Tokens.interfaceFontMaxScale)
        .fontWeight(600)
        .fontColor(Colors.ink)
        .textAlign(TextAlign.Center)

      if (subtitle) {
        Text(subtitle)
          .fontSize(Tokens.typeSecondary)
          .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSecondary))
          .fontColor(Colors.secondary)
          .textAlign(TextAlign.Center)
          .constraintSize({ maxWidth: 320 })
      }
    }
    .width('100%')
    .layoutWeight(1)
    .padding(Tokens.spaceXl)
    .justifyContent(FlexAlign.Center)
    .alignItems(HorizontalAlign.Center)
  }

  @Builder scopeSwitch() {
    Row({ space: Tokens.spaceXs }) {
      Button(tr('assistant_search_scope_chats'), { type: ButtonType.Capsule })
        .layoutWeight(1)
        .height(Tokens.controlBar)
        .fontSize(Tokens.typeSecondary)
        .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSecondary))
        .fontWeight(this.tab === 'chats' ? FontWeight.Bold : FontWeight.Medium)
        .fontColor(this.tab === 'chats' ? Colors.ink : Colors.secondary)
        .backgroundColor(this.tab === 'chats' ? Colors.sheetCard : Color.Transparent)
        .accessibilitySelected(this.tab === 'chats')
        .onClick(() => this.selectTab('chats'))
        .id('archived.tab.chats')

      Button(tr('agents'), { type: ButtonType.Capsule })
        .layoutWeight(1)
        .height(Tokens.controlBar)
        .fontSize(Tokens.typeSecondary)
        .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSecondary))
        .fontWeight(this.tab === 'agents' ? FontWeight.Bold : FontWeight.Medium)
        .fontColor(this.tab === 'agents' ? Colors.ink : Colors.secondary)
        .backgroundColor(this.tab === 'agents' ? Colors.sheetCard : Color.Transparent)
        .accessibilitySelected(this.tab === 'agents')
        .onClick(() => this.selectTab('agents'))
        .id('archived.tab.agents')
    }
    .width('100%')
    .padding(Tokens.spaceXs)
    .backgroundColor(Colors.control)
    .borderRadius(Tokens.radiusCapsule)
  }

  private agentSubtitle(row: ResourceRow): string {
    const machine = this.model.machineName(row.machine) || row.machine;
    const folder = (row.path || Wire.text(row.data(), 'cwd')).split(/[\\/]/).filter((part: string) => !!part).pop() || '';
    const date = this.formattedUpdatedAt(row);
    return [machine, folder, date].filter((part: string) => !!part).join(' · ');
  }

  build() {
    Column({ space: 0 }) {
      PersonaSheetHeader({
        title: tr('archived_chats'),
        subtitle: this.tab === 'chats'
          ? (this.archivedRows.length > 0 ? String(this.archivedRows.length) : '')
          : (this.filteredAgents.length > 0 ? String(this.filteredAgents.length) : ''),
        closeIdentity: 'archived.close',
        showDivider: true,
        onClose: () => this.onClose()
      })

      Column({ space: Tokens.spaceM }) {
        this.scopeSwitch()

        if (this.tab === 'chats') {
          if (this.model.conversations.some((row: ResourceRow) => Wire.bool(row.data(), 'archived'))) {
            this.searchBar()
          }

          if (this.archivedRows.length === 0) {
            this.emptyState(
              this.query.trim() ? tr('archived_search_empty') : tr('archived_empty_title'),
              this.query.trim() ? '' : tr('archived_empty_message')
            )
          } else {
            List({ space: Tokens.spaceS }) {
              ForEach(this.archivedRows, (row: ResourceRow) => {
                ListItem() {
                  Row({ space: Tokens.spaceM }) {
                    Column({ space: Tokens.spaceXxs }) {
                      Text(sidebarConversationTitle(row, tr))
                        .fontSize(Tokens.typeBody)
                        .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeBody))
                        .fontWeight(FontWeight.Medium)
                        .fontColor(Colors.ink)
                        .maxLines(1)
                        .textOverflow({ overflow: TextOverflow.Ellipsis })
                      if (this.formattedUpdatedAt(row)) {
                        Text(this.formattedUpdatedAt(row))
                          .fontSize(Tokens.typeCaption)
                          .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption))
                          .fontColor(Colors.secondary)
                          .maxLines(1)
                      }
                    }
                    .layoutWeight(1)
                    .alignItems(HorizontalAlign.Start)
                    .onClick(() => {
                      Haptics.tap();
                      this.onOpen(row);
                    })

                    Button({ type: ButtonType.Capsule }) {
                      Row({ space: Tokens.spaceXs }) {
                        if (this.isBusy(row)) {
                          LoadingProgress().width(13).height(13).color(Colors.link)
                        } else {
                          SymbolGlyph($r('sys.symbol.archivebox'))
                            .fontSize('12vp')
                            .fontColor([Colors.link])
                        }
                        Text(tr('unarchive'))
                          .fontSize(Tokens.typeCaption)
                          .fontWeight(FontWeight.Medium)
                          .fontColor(Colors.link)
                          .maxLines(1)
                      }
                    }
                    .height(Tokens.controlBar)
                    .padding({ left: Tokens.spaceM, right: Tokens.spaceM })
                    .responseRegion(ButtonStyles.touchRegion(Tokens.controlBar))
                    .backgroundColor(Colors.needsWash)
                    .enabled(!this.isBusy(row))
                    .onClick(() => { void this.unarchive(row); })
                    .id('archived.unarchive.' + this.rowSessionId(row))
                  }
                  .width('100%')
                  .constraintSize({ minHeight: Tokens.controlBar })
                  .padding({ left: Tokens.spaceL, right: Tokens.spaceM, top: Tokens.spaceM, bottom: Tokens.spaceM })
                  .backgroundColor(Colors.sheetCard)
                  .borderRadius(Tokens.radiusCard)
                  .border({
                    width: PhoneStyle.surfaceEdgeWidth(0.5),
                    color: Colors.hairlineGlass,
                    radius: Tokens.radiusCard
                  })
                  .alignItems(VerticalAlign.Center)
                  .bindContextMenu(this.rowContextMenu(row, false), ResponseType.LongPress)
                  .id('archived.row.' + this.rowSessionId(row))
                }
              }, (row: ResourceRow) => row.key() + ':' + Wire.text(row.data(), 'updated_at'))
            }
            .width('100%')
            .layoutWeight(1)
            .scrollBar(BarState.Auto)
          }
        } else {
          if (this.archivedAgents.length > 0 || this.query.trim()) {
            this.searchBar()
          }

          if (this.loadingAgents && !this.agentsLoaded) {
            Column() {
              LoadingProgress().width(32).height(32).color(Colors.link)
            }
            .width('100%')
            .layoutWeight(1)
            .justifyContent(FlexAlign.Center)
            .alignItems(HorizontalAlign.Center)
          } else if (this.agentsError && !this.agentsLoaded) {
            Column({ space: Tokens.spaceS }) {
              Text(this.agentsError)
                .fontSize(Tokens.typeSecondary)
                .fontColor(Colors.danger)
                .textAlign(TextAlign.Center)
              Button(tr('retry'), { type: ButtonType.Capsule })
                .constraintSize({ minWidth: 96, minHeight: Tokens.controlBar })
                .fontSize(Tokens.typeSecondary)
                .fontColor(Colors.link)
                .backgroundColor(Colors.needsWash)
                .onClick(() => { void this.loadArchivedAgents(); })
                .id('archived.agents.retry')
            }
            .width('100%')
            .layoutWeight(1)
            .justifyContent(FlexAlign.Center)
            .alignItems(HorizontalAlign.Center)
          } else if (this.filteredAgents.length === 0) {
            this.emptyState(
              this.query.trim() ? tr('archived_search_empty') : tr('archived_agents_empty_title'),
              this.query.trim() ? '' : tr('archived_agents_empty_message')
            )
          } else {
            List({ space: Tokens.spaceS }) {
              ForEach(this.filteredAgents, (row: ResourceRow) => {
                ListItem() {
                  Row({ space: Tokens.spaceM }) {
                    Column({ space: Tokens.spaceXxs }) {
                      Text(row.title || tr('agent_untitled'))
                        .fontSize(Tokens.typeBody)
                        .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeBody))
                        .fontWeight(FontWeight.Medium)
                        .fontColor(Colors.ink)
                        .maxLines(1)
                        .textOverflow({ overflow: TextOverflow.Ellipsis })
                      if (this.agentSubtitle(row)) {
                        Text(this.agentSubtitle(row))
                          .fontSize(Tokens.typeCaption)
                          .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption))
                          .fontColor(Colors.secondary)
                          .maxLines(1)
                          .textOverflow({ overflow: TextOverflow.Ellipsis })
                      }
                    }
                    .layoutWeight(1)
                    .alignItems(HorizontalAlign.Start)
                    .onClick(() => {
                      Haptics.tap();
                      this.onOpen(row);
                    })

                    Button({ type: ButtonType.Capsule }) {
                      Row({ space: Tokens.spaceXs }) {
                        if (this.isBusy(row)) {
                          LoadingProgress().width(13).height(13).color(Colors.link)
                        } else {
                          SymbolGlyph($r('sys.symbol.archivebox'))
                            .fontSize('12vp')
                            .fontColor([Colors.link])
                        }
                        Text(tr('unarchive'))
                          .fontSize(Tokens.typeCaption)
                          .fontWeight(FontWeight.Medium)
                          .fontColor(Colors.link)
                          .maxLines(1)
                      }
                    }
                    .height(Tokens.controlBar)
                    .padding({ left: Tokens.spaceM, right: Tokens.spaceM })
                    .responseRegion(ButtonStyles.touchRegion(Tokens.controlBar))
                    .backgroundColor(Colors.needsWash)
                    .enabled(!this.isBusy(row))
                    .onClick(() => { void this.unarchiveAgent(row); })
                    .id('archived.agent.unarchive.' + this.rowSessionId(row))
                  }
                  .width('100%')
                  .constraintSize({ minHeight: Tokens.controlBar })
                  .padding({ left: Tokens.spaceL, right: Tokens.spaceM, top: Tokens.spaceM, bottom: Tokens.spaceM })
                  .backgroundColor(Colors.sheetCard)
                  .borderRadius(Tokens.radiusCard)
                  .border({
                    width: PhoneStyle.surfaceEdgeWidth(0.5),
                    color: Colors.hairlineGlass,
                    radius: Tokens.radiusCard
                  })
                  .alignItems(VerticalAlign.Center)
                  .bindContextMenu(this.rowContextMenu(row, true), ResponseType.LongPress)
                  .id('archived.agent.row.' + this.rowSessionId(row))
                }
              }, (row: ResourceRow) => row.key() + ':' + Wire.text(row.data(), 'updated_at'))
            }
            .width('100%')
            .layoutWeight(1)
            .scrollBar(BarState.Auto)
          }
        }
      }
      .width('100%')
      .layoutWeight(1)
      .padding({ left: Tokens.spaceL, right: Tokens.spaceL, top: Tokens.spaceM, bottom: Tokens.spaceL })
    }
    .width('100%')
    .height('100%')
    .backgroundColor(Colors.sheetBackground)
    .id('archived.sheet')
  }
}
