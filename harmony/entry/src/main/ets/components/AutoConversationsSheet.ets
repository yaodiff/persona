import { SymbolGlyphModifier } from '@kit.ArkUI';
import { AssistantChatStateMark, AssistantChatTitle, assistantSourceSymbol } from './AssistantChatMarks';
import { Colors } from '../design/Colors';
import { ButtonStyles } from '../design/Controls';
import { PersonaSheetHeader } from '../design/NormalizedPrimitives';
import { PhoneStyle } from '../design/PhoneStyle';
import { tr, trError } from '../design/Strings';
import { Symbols } from '../design/Symbols';
import { Tokens } from '../design/Tokens';
import {
  AUTO_CONVERSATION_FEATURE,
  AutoConversationFilter,
  AutoConversationGroup,
  AutoConversationInput,
  AutoConversationSourceId,
  AutoConversationsPartition,
  autoConversationSourceLabelKey,
  filterAutoConversations,
  partitionAutoConversations,
  sourceOfAutoConversation
} from '../core/AutoConversations';
import { relativeActivityDate } from '../core/PhoneDate';
import { assistantChatState, sidebarConversationTitle, SidebarTaskHistory } from '../models/AssistantRecentChats';
import { ClientModel } from '../models/ClientModel';
import { segment } from '../models/HubRepository';
import { toastCenter } from '../models/ToastCenter';
import { ResourceRow, Wire } from '../models/Wire';
import { Haptics } from '../platform/Haptics';
import { NativeEnvironment, nativeEnvironment } from '../platform/NativeEnvironment';
import { i18n } from '@kit.LocalizationKit';

interface BoundAutoConversation extends AutoConversationInput {
  readonly row: ResourceRow;
}

@ComponentV2
export struct AutoConversationsSheet {
  @Require @Param model: ClientModel;
  @Param history?: SidebarTaskHistory = undefined;
  @Param initialFilter: AutoConversationFilter = 'all';
  @Event onOpen: (row: ResourceRow) => void = () => {};
  @Event onClose: () => void = () => {};

  @Local private env: NativeEnvironment = nativeEnvironment;
  @Local private filter: AutoConversationFilter = 'all';
  @Local private busyKeys: string[] = [];
  @Local private clock: number = Date.now();
  private clockTimer: number = -1;

  aboutToAppear(): void {
    this.filter = this.initialFilter;
    this.clockTimer = setInterval(() => { this.clock = Date.now(); }, 60000);
    if (this.history) {
      this.history.refresh(this.model, true).catch(() => undefined);
    }
  }

  aboutToDisappear(): void {
    if (this.clockTimer !== -1) {
      clearInterval(this.clockTimer);
      this.clockTimer = -1;
    }
  }

  @Monitor('model', 'model.profile', 'model.connectionGeneration')
  onModelReset(): void {
    this.filter = 'all';
    this.busyKeys = [];
    if (this.history) {
      this.history.refresh(this.model, true).catch(() => undefined);
    }
  }

  @Monitor('model.connected', 'model.conversations')
  onConversationsUpdated(): void {
    if (this.history) {
      this.history.refresh(this.model, true).catch(() => undefined);
    }
  }

  private toInput(row: ResourceRow): BoundAutoConversation {
    const data = row.data();
    const sid = row.session || row.id;
    return {
      row,
      id: row.id,
      session_id: sid,
      machine_id: row.machine,
      group: row.group,
      manager: row.manager,
      title: sidebarConversationTitle(row, tr),
      origin: Wire.text(data, 'origin'),
      kind: Wire.text(data, 'kind'),
      automatic: Wire.bool(data, 'automatic'),
      main: Wire.bool(data, 'main'),
      archived: Wire.bool(data, 'archived'),
      state: Wire.text(data, 'state') || row.state,
      status: Wire.text(data, 'status'),
      updated_at: Wire.text(data, 'updated_at'),
      created_at: Wire.text(data, 'created_at'),
      last_text: Wire.text(data, 'last_text') || row.detail
    };
  }

  @Computed get knownTaskIds(): string[] {
    if (!this.history) return [];
    const _rev = this.history.revision;
    return this.history.known(this.model.scope()).taskSessionIds(this.model.conversations);
  }

  @Computed get partition(): AutoConversationsPartition<BoundAutoConversation> {
    const inputs = this.model.conversations.map((row: ResourceRow) => this.toInput(row));
    return partitionAutoConversations(inputs, {
      mainId: this.model.mainId,
      mainMachineId: this.model.mainMachineId,
      group: this.model.profile.id,
      manager: this.model.profile.manager,
      hubSays: this.model.demo || this.model.features.includes(AUTO_CONVERSATION_FEATURE),
      knownTaskIds: this.knownTaskIds
    });
  }

  @Computed get visibleItems(): BoundAutoConversation[] {
    return filterAutoConversations(this.partition, this.filter);
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

  private isBuiltIn(row: ResourceRow): boolean {
    const config = Wire.parse(this.model.sidebarConfig);
    const supervisor = Wire.text(config, 'background_supervisor');
    const sid = this.rowSessionId(row);
    return !!supervisor && sid === supervisor;
  }

  private isPinned(row: ResourceRow): boolean {
    const config = Wire.parse(this.model.sidebarConfig);
    const leaders = Wire.strings(config, 'leaders');
    return leaders.includes(this.rowSessionId(row));
  }

  private canMutate(row: ResourceRow): boolean {
    const sid = this.rowSessionId(row);
    if (!this.model.connected || !sid || this.isBuiltIn(row)) return false;
    return !this.model.conversations.some((item: ResourceRow) =>
      this.rowSessionId(item) === sid &&
      item.machine !== row.machine &&
      (!item.group || item.group === this.model.profile.id) &&
      (!item.manager || item.manager === this.model.profile.manager)
    );
  }

  private sourceOf(item: BoundAutoConversation): string {
    return sourceOfAutoConversation(item, this.knownTaskIds) || '';
  }

  private rowDetail(item: BoundAutoConversation): string {
    const source = this.sourceOf(item);
    const sourceText = this.filter === 'all' && source ? tr(autoConversationSourceLabelKey(source as AutoConversationSourceId)) : '';
    const state = assistantChatState(item.row);
    let activityText = '';
    if (state === 'needs_you') activityText = tr('phone_todo_section_needs');
    else if (state === 'working') activityText = tr('phone_todo_section_working');
    else if (state === 'unread') activityText = tr('phone_sidebar_unread');
    else {
      const locale = this.model.language === 'default' ? i18n.System.getSystemLanguage() : this.model.language;
      activityText = relativeActivityDate(
        Date.parse(item.updated_at || item.created_at || ''),
        this.clock,
        locale,
        tr('agent_time_just_now'),
        tr('kb_minutes_ago'),
        tr('kb_yesterday')
      );
    }
    return [sourceText, activityText].filter((part: string) => !!part).join(' · ');
  }

  private confirmPin(row: ResourceRow): void {
    if (!this.canMutate(row) || this.isBusy(row)) return;
    const owner = this.model;
    const generation = owner.connectionGeneration();
    const scope = owner.scope();
    const pinned = this.isPinned(row);
    const title = tr(pinned ? 'phone_chat_unpin_title' : 'phone_chat_pin_title');
    const message = pinned
      ? tr('phone_chat_unpin_message').replace('{name}', sidebarConversationTitle(row, tr))
      : tr('phone_chat_pin_message');
    this.getUIContext().showAlertDialog({
      title,
      message,
      autoCancel: true,
      alignment: DialogAlignment.Center,
      primaryButton: { value: tr('cancel'), action: () => {} },
      secondaryButton: {
        value: tr(pinned ? 'phone_chat_unpin' : 'phone_chat_pin'),
        action: () => {
          if (owner !== this.model || !owner.owns(generation, scope)) return;
          void this.togglePin(row, !pinned);
        }
      }
    });
  }

  private async togglePin(row: ResourceRow, pin: boolean): Promise<void> {
    const owner = this.model;
    const sid = this.rowSessionId(row);
    if (!this.canMutate(row) || !sid) return;
    const busyKey = this.rowBusyKey(row);
    if (this.busyKeys.includes(busyKey)) return;
    const path = '/api/v1/assistant/directions/' + segment(sid) + (pin ? '/make' : '/end');
    if (owner.operationBlocked(path)) {
      toastCenter.error(tr('pending_action'));
      return;
    }
    const generation = owner.connectionGeneration();
    const scope = owner.scope();
    this.busyKeys = this.busyKeys.concat([busyKey]);
    let ok = false;
    try {
      await owner.repo().call(path, 'POST', {}, '', () => owner === this.model && owner.owns(generation, scope));
      if (owner !== this.model || !owner.owns(generation, scope)) return;
      const config = Wire.parse(owner.sidebarConfig);
      const leaders = Wire.strings(config, 'leaders').filter((entry: string) => entry !== sid);
      if (pin) leaders.push(sid);
      config['leaders'] = leaders;
      owner.sidebarConfig = JSON.stringify(config);
      ok = true;
      Haptics.success();
      toastCenter.success(tr(pin ? 'phone_chat_pinned' : 'phone_chat_unpinned'));
    } catch (err) {
      if (owner === this.model && owner.owns(generation, scope)) {
        Haptics.error();
        toastCenter.error(trError((err as Error).message || 'failed'));
      }
    } finally {
      this.busyKeys = this.busyKeys.filter((entry: string) => entry !== busyKey);
    }
    if (ok && owner === this.model && owner.owns(generation, scope)) {
      owner.refresh().catch(() => {});
    }
  }

  private async archiveRow(row: ResourceRow): Promise<void> {
    const owner = this.model;
    const sid = this.rowSessionId(row);
    if (!this.canMutate(row) || !sid) return;
    const busyKey = this.rowBusyKey(row);
    if (this.busyKeys.includes(busyKey)) return;
    const path = '/api/v1/assistant/conversations/' + segment(sid) + '/archive';
    if (owner.operationBlocked(path)) {
      toastCenter.error(tr('pending_action'));
      return;
    }
    const generation = owner.connectionGeneration();
    const scope = owner.scope();
    this.busyKeys = this.busyKeys.concat([busyKey]);
    let ok = false;
    try {
      await owner.repo().call(path, 'POST', {}, '', () => owner === this.model && owner.owns(generation, scope));
      if (owner !== this.model || !owner.owns(generation, scope)) return;
      owner.conversations = owner.conversations.filter((item: ResourceRow) =>
        !(this.rowSessionId(item) === sid && item.machine === row.machine)
      );
      ok = true;
      Haptics.success();
      toastCenter.success(tr('phone_chat_archived'));
    } catch (err) {
      if (owner === this.model && owner.owns(generation, scope)) {
        Haptics.error();
        toastCenter.error(trError((err as Error).message || 'failed'));
      }
    } finally {
      this.busyKeys = this.busyKeys.filter((entry: string) => entry !== busyKey);
    }
    if (ok && owner === this.model && owner.owns(generation, scope)) {
      owner.refresh().catch(() => {});
    }
  }

  private filterChipSymbol(filterId: AutoConversationFilter): Resource {
    if (filterId === 'all') return Symbols.spaces;
    if (filterId === 'mine') return $r('sys.symbol.person');
    if (filterId === 'automatic') return $r('sys.symbol.wand_and_stars');
    return assistantSourceSymbol(filterId);
  }

  @Builder filterChip(label: string, count: number, filterId: AutoConversationFilter) {
    Button({ type: ButtonType.Capsule }) {
      Row({ space: Tokens.spaceXs }) {
        SymbolGlyph(this.filterChipSymbol(filterId))
          .fontSize('13vp')
          .fontColor([this.filter === filterId ? Colors.link : Colors.secondary])
        Text(label + ' ' + String(count))
          .fontSize(Tokens.typeCaption)
          .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption))
          .fontWeight(FontWeight.Medium)
          .fontColor(this.filter === filterId ? Colors.link : Colors.secondary)
          .maxLines(1)
      }
    }
    .constraintSize({ minHeight: Tokens.controlBar })
    .padding({ left: Tokens.spaceM, right: Tokens.spaceM })
    .margin({ right: Tokens.spaceS, bottom: Tokens.spaceS })
    .backgroundColor(this.filter === filterId ? Colors.needsWash : Colors.control)
    .accessibilitySelected(this.filter === filterId)
    .onClick(() => {
      Haptics.selection();
      this.filter = filterId;
    })
    .id('auto.chats.filter.' + filterId)
  }

  @Builder rowActionsMenu(row: ResourceRow) {
    Menu() {
      if (this.canMutate(row)) {
        MenuItem({
          content: tr(this.isPinned(row) ? 'phone_chat_unpin' : 'phone_chat_pin'),
          symbolStartIcon: new SymbolGlyphModifier($r('sys.symbol.pin'))
        })
          .enabled(!this.isBusy(row))
          .onClick(() => this.confirmPin(row))
          .id('auto.chats.menu.pin.' + this.rowSessionId(row))

        MenuItem({
          content: tr('archive'),
          symbolStartIcon: new SymbolGlyphModifier($r('sys.symbol.archivebox'))
        })
          .enabled(!this.isBusy(row))
          .onClick(() => { void this.archiveRow(row); })
          .id('auto.chats.menu.archive.' + this.rowSessionId(row))
      }
    }
  }

  @Builder chatCard(item: BoundAutoConversation) {
    Row({ space: Tokens.spaceM }) {
      Column({ space: Tokens.spaceXxs }) {
        AssistantChatTitle({
          title: item.title || tr('phone_new_chat_title'),
          source: this.sourceOf(item),
          identity: 'auto.chats.mark.' + (item.session_id || item.id)
        }).width('100%')

        if (this.rowDetail(item)) {
          Text(this.rowDetail(item))
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
        this.onClose();
        this.onOpen(item.row);
      })

      AssistantChatStateMark({
        state: assistantChatState(item.row),
        identity: 'auto.chats.state.' + (item.session_id || item.id)
      })

      if (this.canMutate(item.row)) {
        Button({ type: ButtonType.Circle }) {
          if (this.isBusy(item.row)) {
            LoadingProgress().width(14).height(14).color(Colors.link)
          } else {
            Row({ space: 2.5 }) {
              ForEach([0, 1, 2], (_dot: number) => {
                Circle().width(3).height(3).fill(Colors.secondary)
              }, (dot: number) => String(dot))
            }
          }
        }
        .width(Tokens.controlBar)
        .height(Tokens.controlBar)
        .padding(0)
        .backgroundColor(Color.Transparent)
        .accessibilityText(tr('more'))
        .enabled(!this.isBusy(item.row))
        .bindMenu(this.rowActionsMenu(item.row))
        .id('auto.chats.more.' + (item.session_id || item.id))
      }
    }
    .width('100%')
    .constraintSize({ minHeight: Tokens.controlTouch })
    .padding({ left: Tokens.spaceL, right: Tokens.spaceS, top: Tokens.spaceS, bottom: Tokens.spaceS })
    .backgroundColor(Colors.sheetCard)
    .borderRadius(PhoneStyle.cardRadius)
    .border({
      width: PhoneStyle.surfaceEdgeWidth(0.5),
      color: PhoneStyle.cardBorder(this.env.dark),
      radius: PhoneStyle.cardRadius
    })
    .shadow(PhoneStyle.cardElevation(this.env.dark))
    .alignItems(VerticalAlign.Center)
    .id('auto.chats.row.' + (item.session_id || item.id))
  }

  build() {
    Column({ space: 0 }) {
      PersonaSheetHeader({
        title: tr('flow_all_chats'),
        subtitle: this.visibleItems.length > 0 ? String(this.visibleItems.length) : '',
        closeIdentity: 'auto.chats.close',
        showDivider: true,
        onClose: () => this.onClose()
      })

      Column({ space: Tokens.spaceS }) {
        Flex({ wrap: FlexWrap.Wrap, alignItems: ItemAlign.Center }) {
          this.filterChip(tr('all'), this.partition.all.length, 'all')
          this.filterChip(tr('phone_all_chats_mine'), this.partition.mine.length, 'mine')
          this.filterChip(tr('automatic'), this.partition.auto.length, 'automatic')
          ForEach(this.partition.groups, (group: AutoConversationGroup<BoundAutoConversation>) => {
            this.filterChip(tr(autoConversationSourceLabelKey(group.source)), group.sessions.length, group.source)
          }, (group: AutoConversationGroup<BoundAutoConversation>) => group.source + ':' + String(group.sessions.length))
        }
        .width('100%')
        .id('auto.chats.filters')

        if (this.visibleItems.length === 0) {
          Column({ space: Tokens.spaceS }) {
            SymbolGlyph(Symbols.persona).fontSize('26vp').fontColor([Colors.secondary])
            Text(tr('phone_all_chats_empty'))
              .fontSize(Tokens.typeHeading)
              .maxFontScale(Tokens.interfaceFontMaxScale)
              .fontWeight(600)
              .fontColor(Colors.ink)
              .textAlign(TextAlign.Center)
            Text(tr('phone_all_chats_empty_detail'))
              .fontSize(Tokens.typeSecondary)
              .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSecondary))
              .fontColor(Colors.secondary)
              .textAlign(TextAlign.Center)
              .constraintSize({ maxWidth: 320 })
          }
          .width('100%')
          .layoutWeight(1)
          .justifyContent(FlexAlign.Center)
          .alignItems(HorizontalAlign.Center)
          .id('auto.chats.empty')
        } else {
          List({ space: Tokens.spaceS }) {
            ForEach(this.visibleItems, (item: BoundAutoConversation) => {
              if (this.canMutate(item.row)) {
                ListItem() {
                  this.chatCard(item)
                }
                .bindContextMenu(this.rowActionsMenu(item.row), ResponseType.LongPress)
              } else {
                ListItem() {
                  this.chatCard(item)
                }
              }
            }, (item: BoundAutoConversation) =>
              item.row.key() + ':' + (item.updated_at || '') + ':' + String(this.isPinned(item.row)) + ':' + String(this.isBusy(item.row)) + ':' + String(this.canMutate(item.row)))
          }
          .width('100%')
          .layoutWeight(1)
          .scrollBar(BarState.Auto)
        }
      }
      .width('100%')
      .layoutWeight(1)
      .padding({ left: Tokens.spaceL, right: Tokens.spaceL, top: Tokens.spaceM, bottom: Tokens.spaceL })
    }
    .width('100%')
    .height('100%')
    .backgroundColor(Colors.sheetBackground)
    .id('auto.chats.sheet')
  }
}
