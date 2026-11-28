import { util } from '@kit.ArkTS';
import { Colors } from '../design/Colors';
import { Motion } from '../design/Motion';
import { PersonaSectionLabel, PersonaSheetHeader, PersonaStatusPill } from '../design/NormalizedPrimitives';
import { PhoneStyle } from '../design/PhoneStyle';
import { Symbols } from '../design/Symbols';
import { tr } from '../design/Strings';
import { Tokens } from '../design/Tokens';
import {
  filterPersonalAgentSessions,
  parsePersonalAgentMessages,
  parsePersonalAgentSessionsPage,
  parsePersonalAgentSources,
  PersonalAgentMessage,
  PersonalAgentSession,
  personalAgentSessionPath,
  PersonalAgentSourceStatus
} from '../core/PersonalAgents';
import { ClientModel } from '../models/ClientModel';
import { Wire } from '../models/Wire';
import { Haptics } from '../platform/Haptics';
import { NativeEnvironment, nativeEnvironment } from '../platform/NativeEnvironment';

@ComponentV2
export struct PersonalAgentsSheet {
  @Require @Param model: ClientModel;
  @Event onClose: () => void = () => {};
  @Event onChanged: () => void = () => {};
  @Local private env: NativeEnvironment = nativeEnvironment;
  @Local private loading: boolean = true;
  @Local private loadError: string = '';
  @Local private query: string = '';
  @Local private sources: PersonalAgentSourceStatus[] = [];
  @Local private sessions: PersonalAgentSession[] = [];
  @Local private hiddenSessions: PersonalAgentSession[] = [];
  @Local private showHidden: boolean = false;
  @Local private activeSession: PersonalAgentSession | undefined = undefined;
  @Local private messages: PersonalAgentMessage[] = [];
  @Local private messagesLoading: boolean = false;
  @Local private replyDraft: string = '';
  @Local private sending: boolean = false;
  @Local private busySessionId: string = '';
  private loadSerial: number = 0;
  private detailSerial: number = 0;

  aboutToAppear(): void {
    this.loadAll().catch(() => undefined);
  }

  private async loadAll(): Promise<void> {
    const serial = ++this.loadSerial;
    const owner = this.model;
    const scope = owner.scope();
    const generation = owner.connectionGeneration();
    if (!owner.profile.id || (!owner.connected && !owner.demo)) {
      this.loading = false;
      this.loadError = 'offline';
      return;
    }
    this.loading = true;
    this.loadError = '';
    try {
      const repo = owner.repo();
      const results = await Promise.all([
        repo.call('/api/v1/agents'),
        repo.call('/api/v1/agents/sessions'),
        repo.call('/api/v1/agents/hidden').catch((): Record<string, Object> => ({}))
      ]);
      if (serial !== this.loadSerial || !owner.owns(generation, scope)) return;
      this.sources = parsePersonalAgentSources(results[0]);
      this.sessions = parsePersonalAgentSessionsPage(results[1]).sessions;
      this.hiddenSessions = parsePersonalAgentSessionsPage(results[2]).sessions;
    } catch (_) {
      if (serial !== this.loadSerial || !owner.owns(generation, scope)) return;
      this.loadError = 'failed';
    } finally {
      if (serial === this.loadSerial) {
        this.loading = false;
      }
    }
  }

  private async openSession(session: PersonalAgentSession): Promise<void> {
    Haptics.tap();
    this.activeSession = session;
    this.messages = [];
    this.replyDraft = '';
    this.messagesLoading = true;
    const serial = ++this.detailSerial;
    const owner = this.model;
    const scope = owner.scope();
    const generation = owner.connectionGeneration();
    try {
      const repo = owner.repo();
      const path = personalAgentSessionPath(session.id);
      const raw = await repo.call(path + '/messages?limit=30');
      if (serial !== this.detailSerial || !owner.owns(generation, scope)) return;
      this.messages = parsePersonalAgentMessages(raw);
      if (session.unreadCount > 0) {
        repo.call(path + '/read', 'POST', {}).catch(() => undefined);
      }
    } catch (_) {
      if (serial !== this.detailSerial || !owner.owns(generation, scope)) return;
    } finally {
      if (serial === this.detailSerial) {
        this.messagesLoading = false;
      }
    }
  }

  private async sendReply(): Promise<void> {
    const session = this.activeSession;
    const text = this.replyDraft.trim();
    if (!session || !session.canSend || !text || this.sending) return;
    this.sending = true;
    const owner = this.model;
    const scope = owner.scope();
    const generation = owner.connectionGeneration();
    try {
      const clientId = util.generateRandomUUID();
      await owner.repo().call(
        personalAgentSessionPath(session.id) + '/messages',
        'POST',
        Wire.payload(['clientId', 'text'], [clientId, text]),
        clientId
      );
      if (!owner.owns(generation, scope) || this.activeSession?.id !== session.id) return;
      this.replyDraft = '';
      await this.openSession(session);
    } catch (_) {
      // Keep draft on failure so user can retry.
    } finally {
      this.sending = false;
    }
  }

  private async hideSession(session: PersonalAgentSession): Promise<void> {
    const owner = this.model;
    const scope = owner.scope();
    const generation = owner.connectionGeneration();
    if (this.busySessionId || !owner.owns(generation, scope)) return;
    this.busySessionId = session.id;
    try {
      await owner.repo().call(personalAgentSessionPath(session.id) + '/hide', 'POST', {});
      if (this.model !== owner || !owner.owns(generation, scope)) return;
      this.sessions = this.sessions.filter((item: PersonalAgentSession) => item.id !== session.id);
      this.hiddenSessions = [session].concat(this.hiddenSessions.filter((item: PersonalAgentSession) => item.id !== session.id));
      this.onChanged();
    } catch (_) {
      if (this.model !== owner || !owner.owns(generation, scope)) return;
      // Reload to reconcile.
      await this.loadAll();
    } finally {
      if (this.busySessionId === session.id) {
        this.busySessionId = '';
      }
    }
  }

  private async unhideSession(session: PersonalAgentSession): Promise<void> {
    const owner = this.model;
    const scope = owner.scope();
    const generation = owner.connectionGeneration();
    if (this.busySessionId || !owner.owns(generation, scope)) return;
    this.busySessionId = session.id;
    try {
      await owner.repo().call(personalAgentSessionPath(session.id) + '/unhide', 'POST', {});
      if (this.model !== owner || !owner.owns(generation, scope)) return;
      this.hiddenSessions = this.hiddenSessions.filter((item: PersonalAgentSession) => item.id !== session.id);
      await this.loadAll();
      if (this.model !== owner || !owner.owns(generation, scope)) return;
      this.onChanged();
    } catch (_) {
      if (this.model !== owner || !owner.owns(generation, scope)) return;
      await this.loadAll();
    } finally {
      if (this.busySessionId === session.id) {
        this.busySessionId = '';
      }
    }
  }

  private filteredSessions(): PersonalAgentSession[] {
    return filterPersonalAgentSessions(this.sessions, this.query);
  }

  private sessionTitle(s: PersonalAgentSession): string {
    return s.title.trim() || s.sessionKey || s.id;
  }

  @Builder sessionRow(session: PersonalAgentSession, hidden: boolean) {
    Row({ space: Tokens.spaceM }) {
      Stack({ alignContent: Alignment.Center }) {
        Text(this.sessionTitle(session).slice(0, 1).toUpperCase())
          .fontSize(Tokens.typeSecondary)
          .fontWeight(FontWeight.Bold)
          .fontColor(Colors.ai)
      }
      .width(36)
      .height(36)
      .borderRadius(18)
      .backgroundColor(Colors.aiWash)
      .flexShrink(0)

      Column({ space: Tokens.spaceXxs }) {
        Row({ space: Tokens.spaceS }) {
          Text(this.sessionTitle(session))
            .fontSize(Tokens.typeSecondary)
            .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSecondary))
            .fontWeight(FontWeight.Medium)
            .fontColor(Colors.ink)
            .maxLines(1)
            .textOverflow({ overflow: TextOverflow.Ellipsis })
            .layoutWeight(1)
          if (session.provider) {
            PersonaStatusPill({ tone: 'neutral', label: session.provider })
          }
        }
        .width('100%')

        if (session.previewText) {
          Text(session.previewText)
            .fontSize(Tokens.typeCaption)
            .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption))
            .fontColor(Colors.secondary)
            .maxLines(2)
            .textOverflow({ overflow: TextOverflow.Ellipsis })
        }
      }
      .layoutWeight(1)
      .alignItems(HorizontalAlign.Start)

      if (session.unreadCount > 0 && !hidden) {
        PersonaStatusPill({
          tone: 'unread',
          label: session.unreadCount > 99 ? '99+' : String(session.unreadCount)
        })
      }

      if (hidden) {
        Button(tr('personal_agents_restore'), { type: ButtonType.Capsule })
          .height(32)
          .constraintSize({ minWidth: Tokens.controlBar })
          .responseRegion({ x: 0, y: -6, width: '100%', height: Tokens.controlBar })
          .padding({ left: Tokens.spaceM, right: Tokens.spaceM })
          .backgroundColor(Colors.control)
          .fontColor(Colors.link)
          .fontSize(Tokens.typeLabel)
          .enabled(!this.busySessionId)
          .onClick(() => {
            Haptics.tap();
            this.unhideSession(session).catch(() => undefined);
          })
          .id('personal.agents.restore.' + session.id)
      } else {
        Button(tr('personal_agents_hide'), { type: ButtonType.Normal })
          .buttonStyle(ButtonStyleMode.TEXTUAL)
          .fontSize(Tokens.typeLabel)
          .fontColor(Colors.secondary)
          .constraintSize({ minWidth: Tokens.controlBar, minHeight: Tokens.controlBar })
          .padding({ left: Tokens.spaceS, right: Tokens.spaceS })
          .enabled(!this.busySessionId)
          .onClick(() => {
            Haptics.tap();
            this.hideSession(session).catch(() => undefined);
          })
          .id('personal.agents.hide.' + session.id)
      }
    }
    .width('100%')
    .padding({ left: Tokens.spaceL, right: Tokens.spaceM, top: Tokens.spaceM, bottom: Tokens.spaceM })
    .alignItems(VerticalAlign.Center)
    .onClick(() => {
      if (!hidden) {
        this.openSession(session).catch(() => undefined);
      }
    })
    .id('personal.agents.row.' + session.id)
  }

  @Builder sessionTranscript(session: PersonalAgentSession) {
    Column({ space: 0 }) {
      PersonaSheetHeader({
        title: this.sessionTitle(session),
        subtitle: session.provider,
        leadingMark: 'navigationBack',
        closeLabel: tr('back'),
        closeIdentity: 'personal.agents.detail.back',
        showDivider: true,
        onClose: () => {
          this.detailSerial++;
          this.activeSession = undefined;
          this.messages = [];
        }
      })

      if (this.messagesLoading) {
        Column() {
          LoadingProgress().width(Tokens.controlCompact).height(Tokens.controlCompact)
        }
        .width('100%')
        .layoutWeight(1)
        .justifyContent(FlexAlign.Center)
        .alignItems(HorizontalAlign.Center)
      } else {
        Scroll() {
          Column({ space: Tokens.spaceM }) {
            if (this.messages.length === 0) {
              Text(tr('empty'))
                .fontSize(Tokens.typeSecondary)
                .fontColor(Colors.secondary)
                .width('100%')
                .textAlign(TextAlign.Center)
                .padding(Tokens.spaceXl)
            } else {
              ForEach(this.messages, (msg: PersonalAgentMessage) => {
                Column({ space: Tokens.spaceXs }) {
                  Text(msg.role === 'user' ? tr('you') : this.sessionTitle(session))
                    .fontSize(Tokens.typeLabel)
                    .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeLabel))
                    .fontWeight(FontWeight.Medium)
                    .fontColor(Colors.secondary)
                  Text(msg.plainText)
                    .fontSize(Tokens.typeSecondary)
                    .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSecondary))
                    .fontColor(Colors.ink)
                    .copyOption(CopyOptions.LocalDevice)
                }
                .width('100%')
                .alignItems(HorizontalAlign.Start)
                .padding(Tokens.spaceM)
                .backgroundColor(msg.role === 'user' ? Colors.selection : Colors.surface)
                .borderRadius(Tokens.radiusCard)
                .border({ width: PhoneStyle.surfaceEdgeWidth(0.5), color: PhoneStyle.cardBorder(this.env.dark), radius: Tokens.radiusCard })
              }, (msg: PersonalAgentMessage, idx: number) => msg.externalId || String(idx))
            }
          }
          .width('100%')
          .padding({ left: Tokens.spaceL, right: Tokens.spaceL, top: Tokens.spaceM, bottom: Tokens.spaceL })
        }
        .width('100%')
        .layoutWeight(1)
        .align(Alignment.TopStart)
      }

      if (session.canSend) {
        Row({ space: Tokens.spaceS }) {
          TextInput({ text: this.replyDraft, placeholder: tr('reply') })
            .layoutWeight(1)
            .height(Tokens.controlBar)
            .fontSize(Tokens.typeSecondary)
            .backgroundColor(Colors.surface)
            .borderRadius(Tokens.radiusCapsule)
            .padding({ left: Tokens.spaceL, right: Tokens.spaceL })
            .onChange((v: string) => { this.replyDraft = v; })
            .onSubmit(() => { this.sendReply().catch(() => undefined); })
          Button({ type: ButtonType.Circle }) {
            SymbolGlyph(Symbols.send).fontSize('18vp').fontColor([Colors.onAction])
          }
          .width(Tokens.controlBar)
          .height(Tokens.controlBar)
          .backgroundColor(Colors.action)
          .enabled(!!this.replyDraft.trim() && !this.sending)
          .onClick(() => {
            Haptics.tap();
            this.sendReply().catch(() => undefined);
          })
          .id('personal.agents.send')
        }
        .width('100%')
        .padding({ left: Tokens.spaceL, right: Tokens.spaceL, top: Tokens.spaceS, bottom: Tokens.spaceL })
        .backgroundColor(Colors.sheetBackground)
      }
    }
    .width('100%')
    .height('100%')
  }

  build() {
    Column({ space: 0 }) {
      if (this.activeSession) {
        this.sessionTranscript(this.activeSession)
      } else {
        PersonaSheetHeader({
          title: tr('personal_agents'),
          closeIdentity: 'personal.agents.close',
          showDivider: true,
          onClose: () => this.onClose()
        })

        Row({ space: Tokens.spaceS }) {
          SymbolGlyph(Symbols.search)
            .fontSize(Tokens.interfaceFontVp(16, this.env.fontScale))
            .fontColor([Colors.secondary])
          TextInput({ text: this.query, placeholder: tr('search') })
            .layoutWeight(1)
            .height(Tokens.controlBar - Tokens.spaceSm)
            .fontSize(Tokens.typeSecondary)
            .fontColor(Colors.ink)
            .backgroundColor(Color.Transparent)
            .padding({ left: 0, right: 0 })
            .onChange((v: string) => { this.query = v; })
            .id('personal.agents.search')
        }
        .width('calc(100% - 32vp)')
        .margin({ top: Tokens.spaceM, bottom: Tokens.spaceS })
        .padding({ left: Tokens.spaceM, right: Tokens.spaceM, top: Tokens.spaceXs, bottom: Tokens.spaceXs })
        .backgroundColor(Colors.surface)
        .borderRadius(Tokens.radiusSheetCard)
        .border({ width: PhoneStyle.surfaceEdgeWidth(0.5), color: PhoneStyle.cardBorder(this.env.dark), radius: Tokens.radiusSheetCard })

        if (this.loading && this.sessions.length === 0) {
          Column() {
            LoadingProgress().width(Tokens.controlCompact).height(Tokens.controlCompact)
          }
          .width('100%')
          .layoutWeight(1)
          .justifyContent(FlexAlign.Center)
          .alignItems(HorizontalAlign.Center)
        } else if (this.loadError && this.sessions.length === 0) {
          Column({ space: Tokens.spaceM }) {
            Text(tr(this.loadError))
              .fontSize(Tokens.typeSecondary)
              .fontColor(Colors.secondary)
            Button(tr('retry'), { type: ButtonType.Capsule })
              .height(Tokens.controlCompact)
              .padding({ left: Tokens.spaceL, right: Tokens.spaceL })
              .backgroundColor(Colors.action)
              .fontColor(Colors.onAction)
              .onClick(() => { this.loadAll().catch(() => undefined); })
          }
          .width('100%')
          .layoutWeight(1)
          .justifyContent(FlexAlign.Center)
          .alignItems(HorizontalAlign.Center)
        } else {
          Scroll() {
            Column({ space: Tokens.spaceL }) {
              if (this.filteredSessions().length === 0) {
                Text(tr(this.query.trim() ? 'no_results' : 'empty'))
                  .fontSize(Tokens.typeSecondary)
                  .fontColor(Colors.secondary)
                  .width('100%')
                  .textAlign(TextAlign.Center)
                  .padding(Tokens.spaceXl)
              } else {
                Column() {
                  ForEach(this.filteredSessions(), (session: PersonalAgentSession, idx: number) => {
                    if (idx > 0) {
                      Divider().strokeWidth(Tokens.divider).color(Colors.divider).margin({ left: 64 })
                    }
                    this.sessionRow(session, false)
                  }, (session: PersonalAgentSession) => session.id + ':' + String(session.unreadCount) + ':' + session.previewText)
                }
                .width('100%')
                .backgroundColor(Colors.surface)
                .borderRadius(Tokens.radiusCard)
                .border({ width: PhoneStyle.surfaceEdgeWidth(0.5), color: PhoneStyle.cardBorder(this.env.dark), radius: Tokens.radiusCard })
                .shadow(PhoneStyle.cardElevation(this.env.dark))
                .clip(true)
              }

              if (this.hiddenSessions.length > 0) {
                Column({ space: Tokens.spaceS }) {
                  Button({ type: ButtonType.Normal }) {
                    Row({ space: Tokens.spaceS }) {
                      Text(tr('personal_agents_removed').replace('{count}', String(this.hiddenSessions.length)))
                        .fontSize(Tokens.typeSectionHeader)
                        .fontWeight(600)
                        .fontColor(Colors.secondary)
                        .layoutWeight(1)
                      SymbolGlyph(this.showHidden ? Symbols.down : Symbols.expand)
                        .fontSize('12vp')
                        .fontColor([Colors.secondary])
                    }
                    .width('100%')
                    .padding({ left: Tokens.spaceXs, right: Tokens.spaceXs })
                  }
                  .width('100%')
                  .constraintSize({ minHeight: Tokens.controlBar })
                  .backgroundColor(Color.Transparent)
                  .onClick(() => {
                    Haptics.tap();
                    Motion.run(this.getUIContext(), Motion.state(this.env.reduceMotion), () => {
                      this.showHidden = !this.showHidden;
                    });
                  })
                  .id('personal.agents.removed.toggle')

                  if (this.showHidden) {
                    Column() {
                      ForEach(this.hiddenSessions, (session: PersonalAgentSession, idx: number) => {
                        if (idx > 0) {
                          Divider().strokeWidth(Tokens.divider).color(Colors.divider).margin({ left: 64 })
                        }
                        this.sessionRow(session, true)
                      }, (session: PersonalAgentSession) => 'hidden:' + session.id)
                    }
                    .width('100%')
                    .backgroundColor(Colors.surface)
                    .borderRadius(Tokens.radiusCard)
                    .border({ width: PhoneStyle.surfaceEdgeWidth(0.5), color: PhoneStyle.cardBorder(this.env.dark), radius: Tokens.radiusCard })
                    .clip(true)
                  }
                }
                .width('100%')
              }
            }
            .width('100%')
            .padding({ left: Tokens.spaceL, right: Tokens.spaceL, top: Tokens.spaceS, bottom: Tokens.spaceXl })
          }
          .width('100%')
          .layoutWeight(1)
          .align(Alignment.TopStart)
        }
      }
    }
    .width('100%')
    .height('100%')
    .backgroundColor(Colors.sheetBackground)
    .id('personal.agents.sheet')
  }
}
