import { AlwaysGrantsSection } from './AlwaysGrantsSection';
import { Colors } from '../design/Colors';
import { ButtonStyles } from '../design/Controls';
import { PersonaSectionLabel, PersonaSheetHeader, PersonaStatusPill } from '../design/NormalizedPrimitives';
import { PhoneStyle } from '../design/PhoneStyle';
import { tr, trError } from '../design/Strings';
import { Symbols } from '../design/Symbols';
import { Tokens } from '../design/Tokens';
import {
  filterOutRevokedStandingGrants,
  normalizeStandingSource,
  parseStandingAuthorizationsResponse,
  revokeStandingAuthorizationPath,
  StandingAuthorization,
  standingActionLabelKey,
  standingAuthorizationsPath,
  standingConditionLabelKey,
  standingFolderName
} from '../core/StandingAuth';
import { ClientModel } from '../models/ClientModel';
import { toastCenter } from '../models/ToastCenter';
import { ResourceRow, Wire } from '../models/Wire';
import { Haptics } from '../platform/Haptics';
import { NativeEnvironment, nativeEnvironment } from '../platform/NativeEnvironment';

type StandingAuthTab = 'standing' | 'session';

@ComponentV2
export struct StandingAuthSheet {
  @Require @Param model: ClientModel;
  @Param initialTab: StandingAuthTab = 'standing';
  @Event onClose: () => void = () => {};

  @Local private env: NativeEnvironment = nativeEnvironment;
  @Local private tab: StandingAuthTab = 'standing';
  @Local private grants: StandingAuthorization[] | undefined = undefined;
  @Local private loading: boolean = false;
  @Local private failureKind: '' | 'forbidden' | 'unavailable' = '';
  @Local private failureMessage: string = '';
  @Local private busyKeys: string[] = [];
  @Local private revokedIds: string[] = [];
  @Local private expandedIds: string[] = [];
  @Local private selectedConversationId: string = '';
  private readRevision: number = 0;

  aboutToAppear(): void {
    this.tab = this.initialTab;
    this.syncDefaultConversation();
    void this.loadStanding();
  }

  @Monitor('model', 'model.connected', 'model.profile')
  onModelChanged(): void {
    this.syncDefaultConversation();
    void this.loadStanding();
  }

  @Monitor('model.conversations')
  onConversationsChanged(): void {
    this.syncDefaultConversation();
  }

  private syncDefaultConversation(): void {
    const active = this.model.selected;
    if (active && active.kind === 'conversation') {
      const activeId = active.session || active.id;
      if (activeId) {
        if (!this.selectedConversationId || !this.conversationOptions().some((row: ResourceRow) => (row.session || row.id) === this.selectedConversationId)) {
          this.selectedConversationId = activeId;
          return;
        }
      }
    }
    const options = this.conversationOptions();
    if (!options.some((row: ResourceRow) => (row.session || row.id) === this.selectedConversationId)) {
      this.selectedConversationId = this.model.mainId || (options[0] ? (options[0].session || options[0].id) : '');
    }
  }

  private conversationOptions(): ResourceRow[] {
    return this.model.conversations.filter((row: ResourceRow) => !Wire.bool(row.data(), 'archived'));
  }

  @Computed get effectiveConversationId(): string {
    return this.selectedConversationId || this.model.mainId || '';
  }

  private grantBusyKey(id: string): string {
    return JSON.stringify([this.model.scope(), this.model.connectionGeneration(), id]);
  }

  private isRevoking(id: string): boolean {
    return this.busyKeys.includes(this.grantBusyKey(id));
  }

  private async loadStanding(): Promise<void> {
    const owner = this.model;
    const rev = ++this.readRevision;
    if (!owner.connected) {
      this.grants = undefined;
      this.loading = false;
      this.failureKind = 'unavailable';
      this.failureMessage = tr('offline');
      return;
    }
    const generation = owner.connectionGeneration();
    const scope = owner.scope();
    this.loading = true;
    this.failureKind = '';
    this.failureMessage = '';
    try {
      const res = await owner.repo().call(standingAuthorizationsPath());
      if (rev !== this.readRevision || owner !== this.model || !owner.owns(generation, scope)) return;
      const parsed = parseStandingAuthorizationsResponse(res);
      const revokingIds = parsed.authorizations
        .filter((item: StandingAuthorization) => this.isRevoking(item.id))
        .map((item: StandingAuthorization) => item.id);
      this.grants = filterOutRevokedStandingGrants(parsed.authorizations, revokingIds, this.revokedIds);
      this.failureKind = '';
      this.failureMessage = '';
    } catch (err) {
      if (rev !== this.readRevision || owner !== this.model || !owner.owns(generation, scope)) return;
      const errRec = err as Object as Record<string, Object>;
      const status = typeof errRec['status'] === 'number' ? Number(errRec['status']) : 0;
      if (status === 403) {
        this.grants = undefined;
        this.failureKind = 'forbidden';
        this.failureMessage = '';
        return;
      }
      const message = trError((err as Error).message || 'failed');
      if (this.grants !== undefined) {
        toastCenter.error(message);
      } else {
        this.failureKind = 'unavailable';
        this.failureMessage = message;
      }
    } finally {
      if (rev === this.readRevision && owner === this.model && owner.owns(generation, scope)) {
        this.loading = false;
      }
    }
  }

  private actionText(action: string): string {
    const key = standingActionLabelKey(action);
    return key ? tr(key) : action;
  }

  private actionsHeadline(grant: StandingAuthorization): string {
    if (!grant.actions.length) return tr('standing_no_actions');
    return grant.actions.map((item: string) => this.actionText(item)).join(' · ');
  }

  private conditionText(condition: string): string {
    const key = standingConditionLabelKey(condition);
    return key ? tr(key) : condition;
  }

  private sourceText(source: string): string {
    const parsed = normalizeStandingSource(source);
    if (parsed.kind === 'suggested_rule') {
      return tr('standing_source_suggested_rule').replace('{count}', String(parsed.count));
    }
    if (parsed.kind === 'main_chat') return tr('main_chat');
    if (parsed.kind === 'a_chat') return tr('standing_source_chat');
    if (parsed.kind === 'settings') return tr('settings');
    return parsed.raw;
  }

  private placeText(grant: StandingAuthorization): string {
    const folder = standingFolderName(grant.directory) || grant.directory;
    const machine = this.model.machineName(grant.machine_id) || grant.machine_id;
    return tr('standing_place_format').replace('{folder}', folder).replace('{machine}', machine);
  }

  private formattedDate(raw: string): string {
    if (!raw) return '';
    const ms = Date.parse(raw);
    if (!Number.isFinite(ms)) return '';
    try {
      return new Date(ms).toLocaleDateString();
    } catch (_) {
      return '';
    }
  }

  private toggleExpanded(id: string): void {
    Haptics.selection();
    if (this.expandedIds.includes(id)) {
      this.expandedIds = this.expandedIds.filter((entry: string) => entry !== id);
    } else {
      this.expandedIds = this.expandedIds.concat([id]);
    }
  }

  private confirmRevoke(grant: StandingAuthorization): void {
    const owner = this.model;
    const generation = owner.connectionGeneration();
    const scope = owner.scope();
    const busyKey = this.grantBusyKey(grant.id);
    if (this.busyKeys.includes(busyKey)) return;
    const folder = standingFolderName(grant.directory) || grant.directory;
    const machine = owner.machineName(grant.machine_id) || grant.machine_id;
    const message = tr('standing_revoke_confirm_message')
      .replace('{folder}', folder)
      .replace('{machine}', machine)
      .replace('{actions}', this.actionsHeadline(grant));
    this.getUIContext().showAlertDialog({
      title: tr('standing_revoke_confirm_title'),
      message,
      autoCancel: true,
      alignment: DialogAlignment.Center,
      primaryButton: { value: tr('cancel'), action: () => {} },
      secondaryButton: {
        value: tr('standing_revoke_action'),
        fontColor: Colors.danger,
        action: () => {
          if (owner !== this.model || !owner.owns(generation, scope)) return;
          void this.revoke(grant);
        }
      }
    });
  }

  private async revoke(grant: StandingAuthorization): Promise<void> {
    const owner = this.model;
    const list = this.grants;
    if (!owner.connected || !list) return;
    const idx = list.findIndex((item: StandingAuthorization) => item.id === grant.id);
    if (idx < 0) return;
    const busyKey = this.grantBusyKey(grant.id);
    if (this.busyKeys.includes(busyKey)) return;
    const path = revokeStandingAuthorizationPath(grant.id);
    if (owner.operationBlocked(path)) {
      toastCenter.error(tr('pending_action'));
      return;
    }
    const generation = owner.connectionGeneration();
    const scope = owner.scope();
    this.grants = list.filter((item: StandingAuthorization) => item.id !== grant.id);
    this.busyKeys = this.busyKeys.concat([busyKey]);
    try {
      await owner.repo().call(
        path,
        'DELETE',
        {},
        '',
        () => owner === this.model && owner.owns(generation, scope)
      );
      if (owner !== this.model || !owner.owns(generation, scope)) return;
      if (!this.revokedIds.includes(grant.id)) {
        this.revokedIds = this.revokedIds.concat([grant.id]);
      }
      Haptics.success();
      toastCenter.success(tr('standing_revoked_toast'));
    } catch (err) {
      if (owner !== this.model || !owner.owns(generation, scope)) return;
      const errRec = err as Object as Record<string, Object>;
      const status = typeof errRec['status'] === 'number' ? Number(errRec['status']) : 0;
      if (status === 404) {
        if (!this.revokedIds.includes(grant.id)) {
          this.revokedIds = this.revokedIds.concat([grant.id]);
        }
        Haptics.success();
        toastCenter.success(tr('standing_revoked_toast'));
        return;
      }
      const restored = (this.grants || []).slice();
      if (!restored.some((item: StandingAuthorization) => item.id === grant.id)) {
        restored.splice(Math.min(idx, restored.length), 0, grant);
        this.grants = restored;
      }
      Haptics.error();
      toastCenter.error(status === 403 ? tr('standing_forbidden_title') : tr('standing_revoke_failed_toast'));
    } finally {
      this.busyKeys = this.busyKeys.filter((entry: string) => entry !== busyKey);
    }
  }

  @Builder segmentSwitch() {
    Row({ space: Tokens.spaceXs }) {
      Button(tr('standing_tab_standing'), { type: ButtonType.Capsule })
        .layoutWeight(1)
        .height(Tokens.controlBar)
        .fontSize(Tokens.typeSecondary)
        .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSecondary))
        .fontWeight(this.tab === 'standing' ? FontWeight.Bold : FontWeight.Medium)
        .fontColor(this.tab === 'standing' ? Colors.ink : Colors.secondary)
        .backgroundColor(this.tab === 'standing' ? Colors.sheetCard : Color.Transparent)
        .accessibilitySelected(this.tab === 'standing')
        .onClick(() => {
          Haptics.selection();
          this.tab = 'standing';
        })
        .id('standing.tab.standing')

      Button(tr('standing_tab_session'), { type: ButtonType.Capsule })
        .layoutWeight(1)
        .height(Tokens.controlBar)
        .fontSize(Tokens.typeSecondary)
        .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSecondary))
        .fontWeight(this.tab === 'session' ? FontWeight.Bold : FontWeight.Medium)
        .fontColor(this.tab === 'session' ? Colors.ink : Colors.secondary)
        .backgroundColor(this.tab === 'session' ? Colors.sheetCard : Color.Transparent)
        .accessibilitySelected(this.tab === 'session')
        .onClick(() => {
          Haptics.selection();
          this.tab = 'session';
        })
        .id('standing.tab.session')
    }
    .width('100%')
    .padding(Tokens.spaceXs)
    .backgroundColor(Colors.control)
    .borderRadius(Tokens.radiusCapsule)
  }

  @Builder emptyView(symbol: Resource, title: string, message: string, identity: string, showRetry: boolean = false) {
    Column({ space: Tokens.spaceS }) {
      Stack({ alignContent: Alignment.Center }) {
        SymbolGlyph(symbol)
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

      if (message) {
        Text(message)
          .fontSize(Tokens.typeSecondary)
          .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSecondary))
          .fontColor(Colors.secondary)
          .textAlign(TextAlign.Center)
          .constraintSize({ maxWidth: 320 })
      }

      if (showRetry) {
        Button(tr('retry'), { type: ButtonType.Capsule })
          .constraintSize({ minWidth: 96, minHeight: Tokens.controlBar })
          .fontSize(Tokens.typeSecondary)
          .fontWeight(FontWeight.Medium)
          .fontColor(Colors.link)
          .backgroundColor(Colors.needsWash)
          .margin({ top: Tokens.spaceXs })
          .onClick(() => { void this.loadStanding(); })
          .id('standing.retry')
      }
    }
    .width('100%')
    .layoutWeight(1)
    .padding(Tokens.spaceXl)
    .justifyContent(FlexAlign.Center)
    .alignItems(HorizontalAlign.Center)
    .id(identity)
  }

  @Builder grantCard(grant: StandingAuthorization) {
    Column({ space: Tokens.spaceS }) {
      Row({ space: Tokens.spaceM }) {
        Column({ space: Tokens.spaceXxs }) {
          Text(this.actionsHeadline(grant))
            .fontSize(Tokens.typeBody)
            .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeBody))
            .fontWeight(FontWeight.Medium)
            .fontColor(Colors.ink)
            .maxLines(2)
            .textOverflow({ overflow: TextOverflow.Ellipsis })
          Text(this.placeText(grant))
            .fontSize(Tokens.typeCaption)
            .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption))
            .fontColor(Colors.secondary)
            .maxLines(1)
            .textOverflow({ overflow: TextOverflow.Ellipsis })
        }
        .layoutWeight(1)
        .alignItems(HorizontalAlign.Start)

        PersonaStatusPill({
          tone: grant.reversible ? 'teal' : 'neutral',
          label: tr(grant.reversible ? 'standing_reversible_yes' : 'standing_reversible_no')
        })
      }
      .width('100%')
      .alignItems(VerticalAlign.Top)

      if (grant.conditions.length > 0) {
        Text(tr('standing_only_when') + ': ' + grant.conditions.map((item: string) => this.conditionText(item)).join(' · '))
          .fontSize(Tokens.typeCaption)
          .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption))
          .fontColor(Colors.secondary)
          .width('100%')
      }

      if (this.expandedIds.includes(grant.id)) {
        Column({ space: Tokens.spaceXs }) {
          if (grant.directory) {
            Text(grant.directory)
              .fontSize(Tokens.typeCode)
              .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCode))
              .fontFamily('monospace')
              .fontColor(Colors.secondary)
              .width('100%')
          }
          if (grant.quote) {
            Text('“' + grant.quote + '”')
              .fontSize(Tokens.typeCaption)
              .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption))
              .fontColor(Colors.ink)
              .width('100%')
          }
          if (grant.source || this.formattedDate(grant.granted_at)) {
            Text([
              grant.source ? this.sourceText(grant.source) : '',
              this.formattedDate(grant.granted_at) ? tr('standing_since').replace('{date}', this.formattedDate(grant.granted_at)) : ''
            ].filter((part: string) => !!part).join(' · '))
              .fontSize(Tokens.typeCaption)
              .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption))
              .fontColor(Colors.secondary)
              .width('100%')
          }
        }
        .width('100%')
        .padding(Tokens.spaceM)
        .backgroundColor(Colors.control)
        .borderRadius(Tokens.radiusControl)
      }

      Row({ space: Tokens.spaceS }) {
        Button(tr(this.expandedIds.includes(grant.id) ? 'standing_hide_details' : 'standing_show_details'), { type: ButtonType.Normal })
          .buttonStyle(ButtonStyleMode.TEXTUAL)
          .constraintSize({ minWidth: Tokens.controlBar, minHeight: Tokens.controlBar })
          .padding({ left: Tokens.spaceS, right: Tokens.spaceS })
          .fontSize(Tokens.typeCaption)
          .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption))
          .fontColor(Colors.link)
          .backgroundColor(Color.Transparent)
          .responseRegion(ButtonStyles.touchRegion(Tokens.controlBar))
          .onClick(() => this.toggleExpanded(grant.id))
          .id('standing.details.' + grant.id)

        Blank().layoutWeight(1)

        Button({ type: ButtonType.Capsule }) {
          Row({ space: Tokens.spaceXs }) {
            if (this.isRevoking(grant.id)) {
              LoadingProgress().width(14).height(14).color(Colors.danger)
            }
            Text(tr('standing_revoke'))
              .fontSize(Tokens.typeCaption)
              .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption))
              .fontWeight(FontWeight.Medium)
              .fontColor(Colors.danger)
              .maxLines(1)
          }
        }
        .height(Tokens.controlBar)
        .padding({ left: Tokens.spaceM, right: Tokens.spaceM })
        .backgroundColor(Colors.dangerWash)
        .responseRegion(ButtonStyles.touchRegion(Tokens.controlBar))
        .enabled(!this.isRevoking(grant.id))
        .onClick(() => this.confirmRevoke(grant))
        .id('standing.revoke.' + grant.id)
      }
      .width('100%')
      .alignItems(VerticalAlign.Center)
    }
    .width('100%')
    .padding(Tokens.spaceL)
    .backgroundColor(Colors.sheetCard)
    .borderRadius(PhoneStyle.cardRadius)
    .border({
      width: PhoneStyle.surfaceEdgeWidth(0.5),
      color: PhoneStyle.cardBorder(this.env.dark),
      radius: PhoneStyle.cardRadius
    })
    .shadow(PhoneStyle.cardElevation(this.env.dark))
    .id('standing.row.' + grant.id)
  }

  @Builder standingContent() {
    if (this.loading && this.grants === undefined) {
      Column() {
        LoadingProgress().width(32).height(32).color(Colors.link)
      }
      .width('100%')
      .layoutWeight(1)
      .justifyContent(FlexAlign.Center)
      .alignItems(HorizontalAlign.Center)
    } else if (this.failureKind === 'forbidden' && this.grants === undefined) {
      this.emptyView(
        $r('sys.symbol.lock_fill'),
        tr('standing_forbidden_title'),
        tr('standing_forbidden_message'),
        'standing.forbidden'
      )
    } else if (this.failureKind === 'unavailable' && this.grants === undefined) {
      this.emptyView(
        $r('sys.symbol.exclamationmark_triangle_fill'),
        tr('standing_failed_title'),
        this.failureMessage,
        'standing.failed',
        true
      )
    } else if (!this.grants || this.grants.length === 0) {
      this.emptyView(
        $r('sys.symbol.checkmark_shield'),
        tr('standing_empty_title'),
        tr('standing_empty_message'),
        'standing.empty'
      )
    } else {
      List({ space: Tokens.spaceS }) {
        ForEach(this.grants, (grant: StandingAuthorization) => {
          ListItem() {
            this.grantCard(grant)
          }
        }, (grant: StandingAuthorization) => grant.id + ':' + String(this.expandedIds.includes(grant.id)))

        ListItem() {
          Text(tr('standing_footer_note'))
            .fontSize(Tokens.typeCaption)
            .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption))
            .fontColor(Colors.secondary)
            .width('100%')
            .padding({ left: Tokens.spaceXs, right: Tokens.spaceXs, top: Tokens.spaceXs, bottom: Tokens.spaceL })
        }
      }
      .width('100%')
      .layoutWeight(1)
      .scrollBar(BarState.Auto)
    }
  }

  @Builder sessionGrantsContent() {
    Scroll() {
      Column({ space: Tokens.spaceM }) {
        if (this.conversationOptions().length > 1) {
          Column({ space: Tokens.spaceXs }) {
            PersonaSectionLabel({ text: tr('standing_select_conversation') })
            Flex({ wrap: FlexWrap.Wrap, alignItems: ItemAlign.Center }) {
              ForEach(this.conversationOptions(), (conv: ResourceRow) => {
                Button(
                  (conv.session || conv.id) === this.model.mainId ? tr('main_chat') : (conv.title || tr('new_chat')),
                  { type: ButtonType.Capsule }
                )
                  .constraintSize({ minHeight: Tokens.controlBar })
                  .padding({ left: Tokens.spaceM, right: Tokens.spaceM })
                  .margin({ right: Tokens.spaceS, bottom: Tokens.spaceS })
                  .fontSize(Tokens.typeCaption)
                  .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption))
                  .fontWeight((conv.session || conv.id) === this.effectiveConversationId ? FontWeight.Bold : FontWeight.Medium)
                  .fontColor((conv.session || conv.id) === this.effectiveConversationId ? Colors.link : Colors.secondary)
                  .backgroundColor((conv.session || conv.id) === this.effectiveConversationId ? Colors.needsWash : Colors.control)
                  .onClick(() => {
                    Haptics.selection();
                    this.selectedConversationId = conv.session || conv.id;
                  })
                  .id('standing.conv.' + (conv.session || conv.id))
              }, (conv: ResourceRow) => conv.key())
            }
            .width('100%')
          }
          .width('100%')
          .alignItems(HorizontalAlign.Start)
        }

        if (this.effectiveConversationId) {
          AlwaysGrantsSection({
            model: this.model,
            conversationId: this.effectiveConversationId
          })
        } else {
          Text(tr('always_grants_empty'))
            .fontSize(Tokens.typeSecondary)
            .fontColor(Colors.secondary)
            .width('100%')
            .padding(Tokens.spaceL)
        }
      }
      .width('100%')
      .padding({ bottom: Tokens.spaceXl })
    }
    .width('100%')
    .layoutWeight(1)
    .align(Alignment.TopStart)
    .scrollBar(BarState.Auto)
  }

  build() {
    Column({ space: 0 }) {
      PersonaSheetHeader({
        title: tr('standing_auth_title'),
        subtitle: this.tab === 'standing' && this.grants && this.grants.length > 0 ? String(this.grants.length) : '',
        closeIdentity: 'standing.auth.close',
        showDivider: true,
        onClose: () => this.onClose()
      })

      Column({ space: Tokens.spaceM }) {
        this.segmentSwitch()
        if (this.tab === 'standing') {
          this.standingContent()
        } else {
          this.sessionGrantsContent()
        }
      }
      .width('100%')
      .layoutWeight(1)
      .padding({ left: Tokens.spaceL, right: Tokens.spaceL, top: Tokens.spaceM, bottom: Tokens.spaceL })
    }
    .width('100%')
    .height('100%')
    .backgroundColor(Colors.sheetBackground)
    .id('standing.auth.sheet')
  }
}
