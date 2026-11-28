import { Colors } from '../design/Colors';
import { ButtonStyles } from '../design/Controls';
import { IconActions } from '../design/IconActions';
import { PersonaSectionLabel, PersonaSheetHeader, PersonaStatusPill, PersonaStatusTone } from '../design/NormalizedPrimitives';
import { PhoneStyle } from '../design/PhoneStyle';
import { tr } from '../design/Strings';
import { Symbols } from '../design/Symbols';
import { Tokens } from '../design/Tokens';
import {
  ASSISTANT_SEARCH_FEATURE,
  AssistantSearchAnswer,
  AssistantSearchDebouncer,
  AssistantSearchHit,
  AssistantSearchMergedRow,
  AssistantSearchScope,
  AssistantSearchSessionInput,
  assistantSearchPath,
  assistantSearchSnippetParts,
  localAgentSearchMatches,
  localAssistantChatMatches,
  mergeAssistantSearchRows,
  parseAssistantSearchAnswer
} from '../core/AssistantSearch';
import { SearchTextPart } from '../core/PhoneSearchExcerpt';
import { herdrTabName } from '../core/SessionNaming';
import { ClientModel } from '../models/ClientModel';
import { sidebarConversationTitle } from '../models/AssistantRecentChats';
import { ResourceRow, Wire } from '../models/Wire';
import { Haptics } from '../platform/Haptics';
import { NativeEnvironment, nativeEnvironment } from '../platform/NativeEnvironment';

interface BoundSearchInput extends AssistantSearchSessionInput {
  readonly row: ResourceRow;
}

@ComponentV2
export struct AssistantSearchSheet {
  @Require @Param model: ClientModel;
  @Param initialScope: AssistantSearchScope = 'chats';
  @Param initialQuery: string = '';
  @Event onOpen: (row: ResourceRow, itemId: string) => void = () => {};
  @Event onClose: () => void = () => {};

  @Local private env: NativeEnvironment = nativeEnvironment;
  @Local private scope: AssistantSearchScope = 'chats';
  @Local private query: string = '';
  @Local private remoteAnswer: AssistantSearchAnswer | undefined = undefined;
  @Local private searchingRemote: boolean = false;
  @Local private remoteFailed: boolean = false;

  private debouncer: AssistantSearchDebouncer = new AssistantSearchDebouncer();
  private mounted: boolean = false;
  private scheduleSeq: number = 0;

  aboutToAppear(): void {
    this.mounted = true;
    this.scope = this.initialScope;
    this.query = this.initialQuery;
    this.scheduleRemoteSearch();
  }

  aboutToDisappear(): void {
    this.mounted = false;
    this.scheduleSeq++;
    this.debouncer.cancel();
  }

  @Monitor('model', 'model.profile', 'model.connected', 'model.features')
  onOwnerChanged(): void {
    this.debouncer.cancel();
    this.remoteAnswer = undefined;
    this.searchingRemote = false;
    this.remoteFailed = false;
    this.scheduleRemoteSearch();
  }

  private scheduleRemoteSearch(): void {
    const owner = this.model;
    const generation = owner.connectionGeneration();
    const modelScope = owner.scope();
    const seq = ++this.scheduleSeq;
    const supportsRemote = owner.demo || owner.features.includes(ASSISTANT_SEARCH_FEATURE);
    const hubKey = (owner.profile.id || 'demo') + '|' + (owner.profile.manager || '');
    const q = this.query.trim();
    if (!q || this.scope !== 'chats' || !owner.connected || !supportsRemote || this.debouncer.isUnsupported(hubKey)) {
      this.debouncer.cancel();
      this.remoteAnswer = undefined;
      this.searchingRemote = false;
      this.remoteFailed = false;
      return;
    }
    this.searchingRemote = true;
    this.remoteFailed = false;
    const scheduled = this.debouncer.schedule({
      query: q,
      scope: this.scope,
      connected: owner.connected,
      supportsRemote,
      hubKey,
      fetcher: async (term: string): Promise<AssistantSearchAnswer> => {
        const res = await owner.repo().call(assistantSearchPath(term));
        return parseAssistantSearchAnswer(res);
      },
      onResult: (answeredQuery: string, answer: AssistantSearchAnswer | undefined, failed: boolean) => {
        if (!this.mounted || seq !== this.scheduleSeq || owner !== this.model || !owner.owns(generation, modelScope)) {
          return;
        }
        this.searchingRemote = false;
        if (answeredQuery === this.query.trim()) {
          this.remoteAnswer = answer;
          this.remoteFailed = failed;
        }
      }
    });
    if (!scheduled) {
      this.searchingRemote = false;
    }
  }

  private toChatInput(row: ResourceRow): BoundSearchInput {
    const data = row.data();
    const sid = row.session || row.id;
    const isMain = !!this.model.mainId && sid === this.model.mainId &&
      (!this.model.mainMachineId || row.machine === this.model.mainMachineId);
    return {
      row,
      id: row.id,
      session_id: sid,
      machine_id: row.machine,
      title: isMain ? tr('main_chat') : sidebarConversationTitle(row, tr),
      last_text: Wire.text(data, 'last_text') || row.detail,
      updated_at: Wire.text(data, 'updated_at', Wire.text(data, 'created_at')),
      archived: Wire.bool(data, 'archived'),
      assistant: true,
      main: isMain
    };
  }

  private toAgentInput(row: ResourceRow): BoundSearchInput {
    const data = row.data();
    const sid = row.session || row.id;
    const preferTab = this.model.directoryOwnerFor(row)?.useHerdrTabName ?? this.model.useHerdrTabName;
    return {
      row,
      id: row.id,
      session_id: sid,
      machine_id: row.machine,
      title: row.title || tr('agent_untitled'),
      herdr_tab_name: herdrTabName(data),
      prefer_herdr_tab: preferTab,
      last_text: Wire.text(data, 'last_text') || row.detail,
      updated_at: Wire.text(data, 'updated_at', Wire.text(data, 'last_at')),
      archived: Wire.bool(data, 'archived'),
      assistant: Wire.bool(data, 'assistant'),
      is_subagent: Wire.bool(data, 'is_subagent'),
      cwd: Wire.text(data, 'cwd') || row.path,
      branch: Wire.text(data, 'git_branch') || Wire.text(data, 'branch'),
      machine_name: this.model.machineName(row.machine) || row.machine,
      state: row.state || Wire.text(data, 'status')
    };
  }

  @Computed get chatResults(): AssistantSearchMergedRow<BoundSearchInput>[] {
    const known = this.model.conversations.map((row: ResourceRow) => this.toChatInput(row));
    const local = localAssistantChatMatches(known, this.query);
    return mergeAssistantSearchRows(local, this.remoteAnswer, known);
  }

  @Computed get agentResults(): BoundSearchInput[] {
    const pool = this.model.directoryReady && this.model.directorySessions.length
      ? this.model.directorySessions
      : this.model.sessions;
    const inputs = pool.map((row: ResourceRow) => this.toAgentInput(row));
    return localAgentSearchMatches(inputs, this.query);
  }

  private agentDisplayTitle(input: BoundSearchInput): string {
    if (input.prefer_herdr_tab && input.herdr_tab_name) return input.herdr_tab_name;
    return input.title || tr('agent_untitled');
  }

  private agentStateTone(rawState: string | undefined): PersonaStatusTone {
    const s = (rawState || '').trim().toLowerCase();
    if (s === 'needs_you' || s === 'blocked') return 'needs';
    if (s === 'working' || s === 'running') return 'working';
    if (s === 'unread' || s === 'done') return 'unread';
    return 'neutral';
  }

  private agentStateLabel(rawState: string | undefined): string {
    const s = (rawState || '').trim().toLowerCase();
    if (s === 'needs_you' || s === 'blocked') return tr('needs_you');
    if (s === 'working' || s === 'running') return tr('working');
    if (s === 'unread' || s === 'done') return tr('agent_group_unread');
    return tr('idle');
  }

  private selectScope(next: AssistantSearchScope): void {
    if (this.scope === next) return;
    Haptics.selection();
    this.scope = next;
    this.scheduleRemoteSearch();
  }

  private openChat(row: ResourceRow, hit: AssistantSearchHit | undefined): void {
    Haptics.tap();
    this.onClose();
    this.onOpen(row, hit ? hit.item_id : '');
  }

  private openAgent(row: ResourceRow): void {
    Haptics.tap();
    this.onClose();
    this.onOpen(row, '');
  }

  @Builder scopeSwitch() {
    Row({ space: Tokens.spaceXs }) {
      Button(tr('assistant_search_scope_chats'), { type: ButtonType.Capsule })
        .layoutWeight(1)
        .height(Tokens.controlBar)
        .fontSize(Tokens.typeSecondary)
        .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSecondary))
        .fontWeight(this.scope === 'chats' ? FontWeight.Bold : FontWeight.Medium)
        .fontColor(this.scope === 'chats' ? Colors.ink : Colors.secondary)
        .backgroundColor(this.scope === 'chats' ? Colors.sheetCard : Color.Transparent)
        .accessibilitySelected(this.scope === 'chats')
        .onClick(() => this.selectScope('chats'))
        .id('assistant.search.scope.chats')

      Button(tr('agents'), { type: ButtonType.Capsule })
        .layoutWeight(1)
        .height(Tokens.controlBar)
        .fontSize(Tokens.typeSecondary)
        .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSecondary))
        .fontWeight(this.scope === 'agents' ? FontWeight.Bold : FontWeight.Medium)
        .fontColor(this.scope === 'agents' ? Colors.ink : Colors.secondary)
        .backgroundColor(this.scope === 'agents' ? Colors.sheetCard : Color.Transparent)
        .accessibilitySelected(this.scope === 'agents')
        .onClick(() => this.selectScope('agents'))
        .id('assistant.search.scope.agents')
    }
    .width('100%')
    .padding(Tokens.spaceXs)
    .backgroundColor(Colors.control)
    .borderRadius(Tokens.radiusCapsule)
  }

  @Builder searchField() {
    Row({ space: Tokens.spaceS }) {
      SymbolGlyph(Symbols.search)
        .fontSize('16vp')
        .fontColor([Colors.secondary])
      TextInput({
        text: this.query,
        placeholder: tr(this.scope === 'chats' ? 'flow_search_chats' : 'search_sessions')
      })
        .layoutWeight(1)
        .height(Tokens.controlBar - Tokens.spaceXs)
        .fontSize(Tokens.typeSecondary)
        .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSecondary))
        .fontColor(Colors.ink)
        .placeholderColor(Colors.secondary)
        .backgroundColor(Color.Transparent)
        .padding({ left: 0, right: 0 })
        .enterKeyType(EnterKeyType.Search)
        .onChange((value: string) => {
          this.query = value;
          this.scheduleRemoteSearch();
        })
        .id('assistant.search.field')

      if (this.query.length > 0) {
        Button({ type: ButtonType.Circle }) {
          SymbolGlyph(IconActions.symbol('close'))
            .fontSize('13vp')
            .fontColor([Colors.secondary])
        }
        .width(28)
        .height(28)
        .padding(0)
        .backgroundColor(Colors.control)
        .responseRegion(ButtonStyles.touchRegion(28))
        .accessibilityText(tr('clear'))
        .onClick(() => {
          Haptics.tap();
          this.query = '';
          this.scheduleRemoteSearch();
        })
        .id('assistant.search.clear')
      }
    }
    .width('100%')
    .constraintSize({ minHeight: Tokens.controlBar })
    .padding({ left: Tokens.spaceM, right: Tokens.spaceS, top: Tokens.spaceXxs, bottom: Tokens.spaceXxs })
    .backgroundColor(Colors.sheetCard)
    .borderRadius(Tokens.radiusCard)
    .border({
      width: PhoneStyle.surfaceEdgeWidth(0.5),
      color: PhoneStyle.cardBorder(this.env.dark),
      radius: Tokens.radiusCard
    })
    .alignItems(VerticalAlign.Center)
  }

  @Builder chatRow(entry: AssistantSearchMergedRow<BoundSearchInput>) {
    Button({ type: ButtonType.Normal }) {
      Row({ space: Tokens.spaceM }) {
        Stack({ alignContent: Alignment.Center }) {
          SymbolGlyph(entry.session.main ? $r('sys.symbol.house') : Symbols.persona)
            .fontSize('16vp')
            .fontColor([entry.session.main ? Colors.link : Colors.secondary])
        }
        .width(Tokens.controlCompact)
        .height(Tokens.controlCompact)
        .borderRadius(Tokens.radiusControl)
        .backgroundColor(entry.session.main ? Colors.needsWash : Colors.control)
        .flexShrink(0)

        Column({ space: Tokens.spaceXxs }) {
          Text(entry.session.title)
            .fontSize(Tokens.typeBody)
            .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeBody))
            .fontWeight(FontWeight.Medium)
            .fontColor(Colors.ink)
            .maxLines(1)
            .textOverflow({ overflow: TextOverflow.Ellipsis })

          if (entry.hit && entry.hit.snippet) {
            Text() {
              ForEach(
                assistantSearchSnippetParts(entry.hit.snippet, entry.hit.ranges, this.env.fontScale),
                (part: SearchTextPart) => {
                  Span(part.text)
                    .fontWeight(part.matched ? FontWeight.Bold : FontWeight.Normal)
                    .fontColor(part.matched ? Colors.link : Colors.secondary)
                },
                (part: SearchTextPart, idx: number) => JSON.stringify([idx, part.matched, part.text])
              )
            }
            .fontSize(Tokens.typeSecondary)
            .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSecondary))
            .maxLines(2)
            .textOverflow({ overflow: TextOverflow.Ellipsis })
            .id('assistant.search.snippet')
          } else if (entry.session.last_text) {
            Text(entry.session.last_text)
              .fontSize(Tokens.typeSecondary)
              .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSecondary))
              .fontColor(Colors.secondary)
              .maxLines(1)
              .textOverflow({ overflow: TextOverflow.Ellipsis })
          }
        }
        .layoutWeight(1)
        .alignItems(HorizontalAlign.Start)

        SymbolGlyph(Symbols.expand)
          .fontSize('12vp')
          .fontColor([Colors.tertiary])
          .flexShrink(0)
      }
      .width('100%')
      .constraintSize({ minHeight: Tokens.controlTouch })
      .padding({ left: Tokens.spaceM, right: Tokens.spaceM, top: Tokens.spaceS, bottom: Tokens.spaceS })
      .alignItems(VerticalAlign.Center)
    }
    .width('100%')
    .padding(0)
    .backgroundColor(Colors.sheetCard)
    .borderRadius(PhoneStyle.cardRadius)
    .border({
      width: PhoneStyle.surfaceEdgeWidth(0.5),
      color: PhoneStyle.cardBorder(this.env.dark),
      radius: PhoneStyle.cardRadius
    })
    .onClick(() => this.openChat(entry.session.row, entry.hit))
    .id('assistant.search.chat.' + entry.session.session_id)
  }

  @Builder agentRow(item: BoundSearchInput) {
    Button({ type: ButtonType.Normal }) {
      Row({ space: Tokens.spaceM }) {
        Stack({ alignContent: Alignment.Center }) {
          SymbolGlyph(Symbols.agent)
            .fontSize('16vp')
            .fontColor([Colors.secondary])
        }
        .width(Tokens.controlCompact)
        .height(Tokens.controlCompact)
        .borderRadius(Tokens.radiusControl)
        .backgroundColor(Colors.control)
        .flexShrink(0)

        Column({ space: Tokens.spaceXxs }) {
          Text(this.agentDisplayTitle(item))
            .fontSize(Tokens.typeBody)
            .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeBody))
            .fontWeight(FontWeight.Medium)
            .fontColor(Colors.ink)
            .maxLines(1)
            .textOverflow({ overflow: TextOverflow.Ellipsis })

          Text([item.machine_name, item.branch, item.last_text || item.cwd].filter((s: string | undefined) => !!s).join(' · '))
            .fontSize(Tokens.typeSecondary)
            .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSecondary))
            .fontColor(Colors.secondary)
            .maxLines(1)
            .textOverflow({ overflow: TextOverflow.Ellipsis })
        }
        .layoutWeight(1)
        .alignItems(HorizontalAlign.Start)

        PersonaStatusPill({
          tone: this.agentStateTone(item.state),
          label: this.agentStateLabel(item.state)
        })
      }
      .width('100%')
      .constraintSize({ minHeight: Tokens.controlTouch })
      .padding({ left: Tokens.spaceM, right: Tokens.spaceM, top: Tokens.spaceS, bottom: Tokens.spaceS })
      .alignItems(VerticalAlign.Center)
    }
    .width('100%')
    .padding(0)
    .backgroundColor(Colors.sheetCard)
    .borderRadius(PhoneStyle.cardRadius)
    .border({
      width: PhoneStyle.surfaceEdgeWidth(0.5),
      color: PhoneStyle.cardBorder(this.env.dark),
      radius: PhoneStyle.cardRadius
    })
    .onClick(() => this.openAgent(item.row))
    .id('assistant.search.agent.' + item.session_id)
  }

  build() {
    Column({ space: 0 }) {
      PersonaSheetHeader({
        title: tr('assistant_full_search_title'),
        closeIdentity: 'assistant.search.close',
        showDivider: true,
        onClose: () => this.onClose()
      })

      Column({ space: Tokens.spaceM }) {
        this.scopeSwitch()
        this.searchField()

        if (this.scope === 'chats') {
          Row({ space: Tokens.spaceS }) {
            PersonaSectionLabel({
              text: this.query.trim()
                ? tr('phone_search_conversation_count').replace('{count}', String(this.chatResults.length))
                : tr('recent')
            })
            if (this.searchingRemote) {
              LoadingProgress().width(16).height(16).color(Colors.link)
            }
          }
          .width('100%')
          .alignItems(VerticalAlign.Center)

          if (this.chatResults.length === 0) {
            Column({ space: Tokens.spaceS }) {
              SymbolGlyph(Symbols.search).fontSize('26vp').fontColor([Colors.tertiary])
              Text(this.query.trim() ? tr('no_results') : tr('empty'))
                .fontSize(Tokens.typeSecondary)
                .fontColor(Colors.secondary)
            }
            .width('100%')
            .layoutWeight(1)
            .justifyContent(FlexAlign.Center)
            .alignItems(HorizontalAlign.Center)
            .id('assistant.search.empty')
          } else {
            List({ space: Tokens.spaceS }) {
              ForEach(this.chatResults, (entry: AssistantSearchMergedRow<BoundSearchInput>) => {
                ListItem() {
                  this.chatRow(entry)
                }
              }, (entry: AssistantSearchMergedRow<BoundSearchInput>) =>
                entry.session.row.key() + ':' + (entry.hit?.item_id || '') + ':' + (entry.hit?.snippet || ''))

              if (this.remoteAnswer?.partial || this.remoteFailed) {
                ListItem() {
                  Text(tr(this.remoteFailed ? 'flow_search_local' : 'flow_search_partial'))
                    .fontSize(Tokens.typeCaption)
                    .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption))
                    .fontColor(Colors.secondary)
                    .padding({ top: Tokens.spaceXs, bottom: Tokens.spaceM })
                }
              }
            }
            .width('100%')
            .layoutWeight(1)
            .scrollBar(BarState.Auto)
          }
        } else {
          PersonaSectionLabel({
            text: this.query.trim()
              ? tr('project_agent_count').replace('{count}', String(this.agentResults.length))
              : tr('recent')
          })

          if (this.agentResults.length === 0) {
            Column({ space: Tokens.spaceS }) {
              SymbolGlyph(Symbols.search).fontSize('26vp').fontColor([Colors.tertiary])
              Text(this.query.trim() ? tr('no_results') : tr('empty'))
                .fontSize(Tokens.typeSecondary)
                .fontColor(Colors.secondary)
            }
            .width('100%')
            .layoutWeight(1)
            .justifyContent(FlexAlign.Center)
            .alignItems(HorizontalAlign.Center)
            .id('assistant.search.empty')
          } else {
            List({ space: Tokens.spaceS }) {
              ForEach(this.agentResults, (item: BoundSearchInput) => {
                ListItem() {
                  this.agentRow(item)
                }
              }, (item: BoundSearchInput) => item.row.key() + ':' + item.title + ':' + (item.state || ''))
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
    .id('assistant.search.sheet')
  }
}
