import { common, skillManager } from '@kit.AbilityKit';
import { AgentCardModel, parseAgentCardPayload } from '../core/AgentCardPayload';
import { workspaceHorizontalPadding } from '../core/WorkspaceGrid';
import { XIAOYI_INTENT_CATALOG } from '../core/XiaoYiIntentCatalog';
import { Colors } from '../design/Colors';
import { PhoneStyle } from '../design/PhoneStyle';
import { tr } from '../design/Strings';
import { Tokens } from '../design/Tokens';
import { ClientModel } from '../models/ClientModel';
import { AgentCardView } from '../pages/AgentCardPage';
import { Haptics } from '../platform/Haptics';
import { NativeEnvironment, nativeEnvironment } from '../platform/NativeEnvironment';
import { HeadlessDiagnosticReport, HeadlessHubService } from '../xiaoyi/HeadlessHubService';
import { IntentSuggestionsService } from '../xiaoyi/IntentSuggestionsService';
import { XiaoYiRuntimeContext } from '../xiaoyi/XiaoYiRuntimeContext';

@ComponentV2
struct XiaoYiInfoRow {
  @Require @Param label: string;
  @Require @Param value: string;
  @Param accent?: ResourceColor = undefined;

  build() {
    Flex({ wrap: FlexWrap.Wrap, justifyContent: FlexAlign.SpaceBetween, alignItems: ItemAlign.Center }) {
      Text(this.label)
        .fontSize(Tokens.typeBody)
        .maxFontScale(Tokens.interfaceFontMaxScale)
        .fontColor(Colors.ink)
        .margin({ right: Tokens.spaceS, top: Tokens.spaceXs, bottom: Tokens.spaceXs })
      Text(this.value)
        .fontSize(Tokens.typeSecondary)
        .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSecondary))
        .fontColor(this.accent || Colors.secondary)
        .margin({ top: Tokens.spaceXs, bottom: Tokens.spaceXs })
    }
    .width('100%')
    .constraintSize({ minHeight: Tokens.controlTouch })
    .padding({ left: Tokens.spaceL, right: Tokens.spaceL, top: Tokens.spaceXs, bottom: Tokens.spaceXs })
    .accessibilityGroup(true)
  }
}

/**
 * Native Settings and live self-diagnostics page for HarmonyOS 7 XiaoYi integration
 * (On-Device A2A Agent, Intents Kit, API 26 Skills, embedded AgentUI card preview, and opt-in XiaoYi Suggestions).
 */
@ComponentV2
export struct XiaoYiSettingsPage {
  @Require @Param model: ClientModel;
  @Param elevated: boolean = false;
  @Local suggestionsEnabled: boolean = false;
  @Local savingToggle: boolean = false;
  @Local checking: boolean = false;
  @Local cardPreviewShown: boolean = false;
  @Local skillCount: number = -1;
  @Local skillNames: string = '';
  @Local report: HeadlessDiagnosticReport | undefined = undefined;
  @Local statusNotice: string = '';
  @Local private environment: NativeEnvironment = nativeEnvironment;
  @Local private pageWidthVp: number = 0;
  private mounted: boolean = false;
  private checkRevision: number = 0;

  private readonly previewStatusCard: AgentCardModel = parseAgentCardPayload({
    cardType: 'status',
    title: 'Persona Status',
    summary: '2 working, 1 need you. First pending: Run unit tests on feat/harmony-xiaoyi',
    workingCount: 2,
    pendingCount: 1,
    deepLinkUri: 'persona://open?dest=agents',
    sessions: [
      { title: 'HarmonyOS XiaoYi integration', state: 'working', machine: 'Studio' },
      { title: 'Release verification checks', state: 'needs_you', machine: 'Studio' }
    ]
  });

  private readonly previewApprovalCard: AgentCardModel = parseAgentCardPayload({
    cardType: 'approval',
    title: 'Persona Confirmation',
    summary: 'Allow tool execution to run portable unit tests and verify HAP build outputs?',
    decisionId: 'preview-dec-1',
    decisionKind: 'permission',
    canApproveInline: true,
    deepLinkUri: 'persona://open?dest=agents'
  });

  aboutToAppear(): void {
    this.mounted = true;
    this.loadInitialState();
  }

  aboutToDisappear(): void {
    this.mounted = false;
    this.checkRevision++;
  }

  private hostContext(): common.Context | undefined {
    const ctx = this.getUIContext().getHostContext() as common.Context | undefined;
    return ctx || XiaoYiRuntimeContext.get();
  }

  private skillsSummaryText(): string {
    if (this.skillCount < 0) return '…';
    if (this.skillCount === 0) return '0';
    return String(this.skillCount) + (this.skillNames ? ' · ' + this.skillNames : '');
  }

  private loadInitialState(): void {
    const rev = ++this.checkRevision;
    // Query declared API 26 Skills independently so preferences/store never delay skill discovery.
    this.queryDeclaredSkills().then((skills: string[]) => {
      if (!this.mounted || rev !== this.checkRevision) return;
      this.skillCount = skills.length;
      this.skillNames = skills.join(', ');
    }).catch(() => {
      if (!this.mounted || rev !== this.checkRevision) return;
      this.skillCount = 0;
      this.skillNames = '';
    });

    const ctx = this.hostContext();
    if (!ctx) return;
    IntentSuggestionsService.isEnabled(ctx).then((enabled: boolean) => {
      if (!this.mounted || rev !== this.checkRevision) return;
      this.suggestionsEnabled = enabled;
    }).catch(() => {});
  }

  private async queryDeclaredSkills(): Promise<string[]> {
    try {
      const flags = skillManager.SkillInfoFlag.GET_SKILL_INFO_WITH_DESCRIPTION |
        skillManager.SkillInfoFlag.GET_SKILL_INFO_WITH_SRC_ENTRIES;
      const infos = await skillManager.getSkillInfosForSelf(flags);
      const names: string[] = [];
      for (let i = 0; i < infos.length; i++) {
        if (infos[i].skillName) names.push(infos[i].skillName);
      }
      return names;
    } catch (_) {
      return [];
    }
  }

  private async toggleSuggestions(next: boolean): Promise<void> {
    const ctx = this.hostContext();
    if (!ctx || this.savingToggle) return;
    this.savingToggle = true;
    this.statusNotice = '';
    try {
      const saved = await IntentSuggestionsService.setEnabled(ctx, next);
      if (!this.mounted) return;
      if (saved) {
        this.suggestionsEnabled = next;
        if (next) {
          const outcome = await IntentSuggestionsService.shareStatusSuggestions(ctx, 0, 0);
          if (this.mounted && outcome.status === 'unregistered') {
            this.statusNotice = tr('xiaoyi_suggestions_unregistered');
          }
        }
      } else {
        this.statusNotice = tr('cache_unavailable');
      }
    } finally {
      if (this.mounted) this.savingToggle = false;
    }
  }

  private async runSelfCheck(): Promise<void> {
    const ctx = this.hostContext();
    if (!ctx || this.checking) return;
    const rev = ++this.checkRevision;
    this.checking = true;
    this.statusNotice = '';
    const hub = new HeadlessHubService(ctx);
    try {
      const result = await hub.runDiagnostics();
      const skills = await this.queryDeclaredSkills();
      if (!this.mounted || rev !== this.checkRevision) return;
      this.report = result;
      this.skillCount = skills.length;
      this.skillNames = skills.join(', ');
    } finally {
      await hub.close();
      if (this.mounted && rev === this.checkRevision) this.checking = false;
    }
  }

  @Builder cardGroup(title: string) {
    Text(title)
      .fontSize(Tokens.typeSectionHeader)
      .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSectionHeader))
      .fontWeight(600)
      .fontColor(Colors.secondary)
      .width('100%')
      .padding({ left: Tokens.spaceL, right: Tokens.spaceL, top: Tokens.spaceM, bottom: Tokens.spaceXs })
  }

  build() {
    if (this.cardPreviewShown) {
      Column() {
        Scroll() {
          Column() {
            Column({ space: Tokens.section }) {
              AgentCardView({ card: this.previewStatusCard })
              AgentCardView({ card: this.previewApprovalCard })
              Button(tr('back'), { type: ButtonType.Capsule })
                .width('100%')
                .constraintSize({ minHeight: Tokens.touch })
                .fontSize(Tokens.body)
                .maxFontScale(Tokens.interfaceFontMaxScale)
                .backgroundColor(Colors.control)
                .fontColor(Colors.ink)
                .onClick(() => { Haptics.tap(); this.cardPreviewShown = false; })
                .id('settings.xiaoyi.cardpreview.close')
            }
            .width('100%')
            .constraintSize({ maxWidth: 740 })
          }
          .width('100%')
          .alignItems(HorizontalAlign.Center)
          .padding({
            left: workspaceHorizontalPadding(this.pageWidthVp || 390),
            right: workspaceHorizontalPadding(this.pageWidthVp || 390),
            top: Tokens.inset,
            bottom: Tokens.section
          })
        }
        .width('100%')
        .layoutWeight(1)
      }
      .width('100%')
      .height('100%')
      .backgroundColor(Colors.listGroup(this.elevated))
      .onAreaChange((_old: Area, now: Area) => {
        const w = Number(now.width);
        if (Number.isFinite(w) && w > 0 && w !== this.pageWidthVp) this.pageWidthVp = w;
      })
      .id('settings.xiaoyi.cardpreview.view')
    } else {
      List({ space: Tokens.inset }) {
        ListItem() {
          Column() {
            Column() {
              this.cardGroup(tr('xiaoyi_section_capabilities'))
              Column() {
                XiaoYiInfoRow({
                  label: tr('xiaoyi_agent_card_label'),
                  value: tr('xiaoyi_agent_card_ready').replace('{count}', '6')
                })
                XiaoYiInfoRow({
                  label: tr('xiaoyi_intents_count_label'),
                  value: String(XIAOYI_INTENT_CATALOG.length)
                })
                XiaoYiInfoRow({
                  label: tr('xiaoyi_skills_runtime_label'),
                  value: this.skillsSummaryText()
                })
              }
              .width('100%')
              .backgroundColor(Colors.card(this.elevated))
              .borderRadius(PhoneStyle.cardRadius)
              .border({
                width: PhoneStyle.surfaceEdgeWidth(0.5),
                color: PhoneStyle.cardBorder(this.environment.dark),
                radius: PhoneStyle.cardRadius
              })
              .shadow(PhoneStyle.cardElevation(this.environment.dark))
              .clip(true)
            }.width('100%').constraintSize({ maxWidth: 740 })
          }.width('100%').alignItems(HorizontalAlign.Center)
        }

        ListItem() {
          Column() {
            Column() {
              this.cardGroup(tr('xiaoyi_section_preferences'))
              Column() {
                Row({ space: Tokens.gap }) {
                  Column({ space: Tokens.fine }) {
                    Text(tr('xiaoyi_suggestions_toggle'))
                      .fontSize(Tokens.body)
                      .maxFontScale(Tokens.interfaceFontMaxScale)
                      .fontColor(Colors.ink)
                      .width('100%')
                    Text(tr('xiaoyi_suggestions_hint'))
                      .fontSize(Tokens.caption)
                      .maxFontScale(Tokens.interfaceScaleFor(Tokens.caption))
                      .fontColor(Colors.secondary)
                      .width('100%')
                  }.layoutWeight(1).alignItems(HorizontalAlign.Start)
                  Toggle({ type: ToggleType.Switch, isOn: this.suggestionsEnabled })
                    .enabled(!this.savingToggle)
                    .onChange((isOn: boolean) => {
                      if (isOn !== this.suggestionsEnabled) {
                        Haptics.selection();
                        this.toggleSuggestions(isOn);
                      }
                    })
                    .id('settings.xiaoyi.suggestions')
                }
                .width('100%')
                .constraintSize({ minHeight: Tokens.touch })
                .padding({ left: Tokens.page, right: Tokens.page, top: Tokens.inset, bottom: Tokens.inset })

                Text(tr('xiaoyi_lock_screen_notice'))
                  .fontSize(Tokens.caption)
                  .maxFontScale(Tokens.interfaceScaleFor(Tokens.caption))
                  .fontColor(Colors.secondary)
                  .width('100%')
                  .padding({ left: Tokens.page, right: Tokens.page, bottom: Tokens.inset })

                if (this.statusNotice) {
                  Text(this.statusNotice)
                    .fontSize(Tokens.caption)
                    .maxFontScale(Tokens.interfaceScaleFor(Tokens.caption))
                    .fontColor(Colors.secondary)
                    .width('100%')
                    .padding({ left: Tokens.page, right: Tokens.page, bottom: Tokens.inset })
                }
              }
              .width('100%')
              .backgroundColor(Colors.card(this.elevated))
              .borderRadius(PhoneStyle.cardRadius)
              .border({
                width: PhoneStyle.surfaceEdgeWidth(0.5),
                color: PhoneStyle.cardBorder(this.environment.dark),
                radius: PhoneStyle.cardRadius
              })
              .shadow(PhoneStyle.cardElevation(this.environment.dark))
              .clip(true)
            }.width('100%').constraintSize({ maxWidth: 740 })
          }.width('100%').alignItems(HorizontalAlign.Center)
        }

        ListItem() {
          Column() {
            Column() {
              this.cardGroup(tr('xiaoyi_section_diagnostics'))
              Column() {
                Button(this.checking ? tr('xiaoyi_diag_running') : tr('xiaoyi_diag_run'), {
                  type: ButtonType.Normal,
                  buttonStyle: ButtonStyleMode.TEXTUAL
                })
                  .width('100%')
                  .constraintSize({ minHeight: Tokens.touch })
                  .fontSize(Tokens.body)
                  .maxFontScale(Tokens.interfaceFontMaxScale)
                  .fontColor(Colors.link)
                  .enabled(!this.checking)
                  .onClick(() => { Haptics.tap(); this.runSelfCheck(); })
                  .id('settings.xiaoyi.selfcheck')

                Divider().color(Colors.divider).margin({ left: Tokens.page, right: Tokens.page })

                Button(tr('xiaoyi_diag_preview_card'), {
                  type: ButtonType.Normal,
                  buttonStyle: ButtonStyleMode.TEXTUAL
                })
                  .width('100%')
                  .constraintSize({ minHeight: Tokens.touch })
                  .fontSize(Tokens.body)
                  .maxFontScale(Tokens.interfaceFontMaxScale)
                  .fontColor(Colors.link)
                  .onClick(() => { Haptics.tap(); this.cardPreviewShown = true; })
                  .id('settings.xiaoyi.cardpreview')

                if (this.report) {
                  Divider().color(Colors.divider).margin({ left: Tokens.page, right: Tokens.page })
                  XiaoYiInfoRow({
                    label: tr('xiaoyi_diag_hub_paired'),
                    value: this.report.paired ? (this.report.profileName || tr('xiaoyi_diag_pass')) : tr('xiaoyi_diag_attention'),
                    accent: this.report.paired ? Colors.positive : Colors.working
                  })
                  XiaoYiInfoRow({
                    label: tr('xiaoyi_diag_credential'),
                    value: this.report.credentialReadable ? tr('xiaoyi_diag_pass') : tr('xiaoyi_diag_attention'),
                    accent: this.report.credentialReadable ? Colors.positive : Colors.working
                  })
                  XiaoYiInfoRow({
                    label: tr('xiaoyi_diag_transport'),
                    value: this.report.sshTunnelOnly
                      ? tr('xiaoyi_diag_ssh_warning')
                      : this.report.hubReachable
                        ? tr('xiaoyi_diag_reachable').replace('{online}', String(this.report.onlineMachines)).replace('{total}', String(this.report.totalMachines))
                        : tr('xiaoyi_diag_attention'),
                    accent: this.report.hubReachable && !this.report.sshTunnelOnly ? Colors.positive : Colors.working
                  })
                  if (this.report.paired) {
                    XiaoYiInfoRow({
                      label: tr('xiaoyi_diag_recording_module'),
                      value: this.report.recordingAllowed ? tr('xiaoyi_diag_on') : tr('xiaoyi_diag_off'),
                      accent: this.report.recordingAllowed ? Colors.positive : Colors.secondary
                    })
                  }
                  if (this.report.errorCode) {
                    Text(tr(this.report.errorCode).replace('{host}', this.report.hubHost || ''))
                      .fontSize(Tokens.caption)
                      .maxFontScale(Tokens.interfaceScaleFor(Tokens.caption))
                      .fontColor(Colors.secondary)
                      .width('100%')
                      .padding({ left: Tokens.page, right: Tokens.page, bottom: Tokens.inset })
                  }
                }
              }
              .width('100%')
              .backgroundColor(Colors.card(this.elevated))
              .borderRadius(PhoneStyle.cardRadius)
              .border({
                width: PhoneStyle.surfaceEdgeWidth(0.5),
                color: PhoneStyle.cardBorder(this.environment.dark),
                radius: PhoneStyle.cardRadius
              })
              .shadow(PhoneStyle.cardElevation(this.environment.dark))
              .clip(true)
            }.width('100%').constraintSize({ maxWidth: 740 })
          }.width('100%').alignItems(HorizontalAlign.Center)
        }
      }
      .width('100%')
      .height('100%')
      .padding({
        left: workspaceHorizontalPadding(this.pageWidthVp || 390),
        right: workspaceHorizontalPadding(this.pageWidthVp || 390),
        top: Tokens.inset,
        bottom: Tokens.section
      })
      .backgroundColor(Colors.listGroup(this.elevated))
      .onAreaChange((_old: Area, now: Area) => {
        const w = Number(now.width);
        if (Number.isFinite(w) && w > 0 && w !== this.pageWidthVp) this.pageWidthVp = w;
      })
      .id('settings.xiaoyi.page')
    }
  }
}
