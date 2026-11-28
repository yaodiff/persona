import { LengthMetrics } from '@kit.ArkUI';
import { Symbols } from '../design/Symbols';
import { Colors } from '../design/Colors';
import { Motion } from '../design/Motion';
import { MotionTokens } from '../core/MotionTokens';
import { PhoneStyle } from '../design/PhoneStyle';
import { Tokens } from '../design/Tokens';
import { tr } from '../design/Strings';
import { ClientModel } from '../models/ClientModel';
import { Wire } from '../models/Wire';
import { nativeEnvironment, NativeEnvironment } from '../platform/NativeEnvironment';
import { Haptics } from '../platform/Haptics';

class WelcomeSuggestion {
  id: string;
  title: string;
  prompt: string;
  local: boolean;
  needs: string[];
  constructor(id: string, title: string, prompt: string, local: boolean = false, needs: string[] = []) {
    this.id = id; this.title = title; this.prompt = prompt; this.local = local; this.needs = needs;
  }
}

function builtInQuestions(): WelcomeSuggestion[] {
  return ['agenda', 'mail', 'agents', 'lark'].map((id: string) => {
    const title = tr('phone_chat_ask_' + id);
    return new WelcomeSuggestion(id, title, title, false, id === 'agenda' ? ['phone_calendar'] : []);
  });
}

function readinessQuestions(result: Record<string, Object>): WelcomeSuggestion[] {
  const seen = new Set<string>(), questions: WelcomeSuggestion[] = [];
  for (const item of Wire.rows(result, 'suggestions')) {
    const id = Wire.text(item, 'id'), title = Wire.text(item, 'title');
    if (!id || !title || seen.has(id)) continue;
    seen.add(id);
    questions.push(new WelcomeSuggestion(id, title, Wire.text(item, 'prompt') || title, Wire.bool(item, 'local'), Wire.strings(item, 'needs')));
  }
  return questions.length ? questions : builtInQuestions();
}

@ComponentV2
export struct AssistantWelcome {
  @Require @Param model: ClientModel;
  @Param canAsk: boolean = true;
  @Event onPlace: (name: string) => void = () => {};
  @Event onAsk: (prompt: string) => void = () => {};
  @Local suggestions: WelcomeSuggestion[] = [];
  @Local help: WelcomeSuggestion | undefined;
  @Local helpShown: boolean = false;
  @Local environment: NativeEnvironment = nativeEnvironment;
  @Local contentWidth: number = 360;
  @Local private pressedSuggestion: string = '';
  @Local private pressedPlace: string = '';
  private revision: number = 0;
  private ownerKey: string = '';

  aboutToAppear(): void { this.reload(); }
  aboutToDisappear(): void { this.revision++; this.ownerKey = ''; this.helpShown = false; }
  @Monitor('model.profile', 'model.selected', 'model.connected', 'model.features')
  contextChanged(): void { this.reload(); }

  private targetKey(): string { return JSON.stringify([this.model.scope(), this.model.connectionGeneration(), this.model.target()]); }
  private async reload(): Promise<void> {
    const revision = ++this.revision, owner = this.model, key = this.targetKey();
    this.ownerKey = key; this.helpShown = false; this.help = undefined; this.suggestions = [];
    if (!owner.connected) return;
    if (!owner.features.includes('assistant_readiness')) { this.suggestions = builtInQuestions(); return; }
    try {
      const result = await owner.repo().call('/api/v1/assistant/readiness');
      if (revision === this.revision && this.model === owner && this.targetKey() === key) this.suggestions = readinessQuestions(result);
    } catch (_) {
      if (revision === this.revision && this.model === owner && this.targetKey() === key) this.suggestions = builtInQuestions();
    }
  }
  private greeting(): string {
    const hour = new Date().getHours();
    return tr(hour >= 5 && hour < 11 ? 'welcome_morning' : hour >= 11 && hour < 18 ? 'welcome_afternoon' : 'welcome_evening');
  }
  private mainConversation(): boolean {
    return !this.model.mainId || this.model.selected.id === this.model.mainId &&
      (!this.model.mainMachineId || this.model.selected.machine === this.model.mainMachineId);
  }
  private questionSymbol(id: string): Resource {
    if (id === 'today' || id === 'agenda') return Symbols.calendar;
    if (id === 'connect_mail' || id === 'mail') return Symbols.inbox;
    if (id === 'largest_folders') return Symbols.folder;
    if (id === 'agents') return Symbols.device;
    if (id === 'how_to_connect_model') return Symbols.gear;
    if (id === 'what_can_you_do') return $r('sys.symbol.questionmark_circle');
    return Symbols.persona;
  }
  private choose(item: WelcomeSuggestion): void {
    if (!this.model.connected || this.ownerKey !== this.targetKey() || !this.suggestions.includes(item)) return;
    if (item.local) { this.help = item; this.helpShown = true; return; }
    if (!this.canAsk) return;
    if (item.needs.length) this.getUIContext().getPromptAction().showToast({ message: tr('phone_chat_phone_read') });
    this.onAsk(item.prompt);
  }
  @Builder question(item: WelcomeSuggestion) {
    Button({type:ButtonType.Normal}) {
      Column({space:12}) {
        Row() {
          Stack({ alignContent: Alignment.Center }) {
            SymbolGlyph(this.questionSymbol(item.id)).fontSize('18vp').fontColor([this.questionTint(item.id)])
          }
            .width(Tokens.controlCompact)
            .height(Tokens.controlCompact)
            .borderRadius(Tokens.radiusControl)
            .backgroundColor(this.questionWell(item.id))
            .border({ width: PhoneStyle.surfaceEdgeWidth(0.5), color: PhoneStyle.cardBorder(this.environment.dark), radius: Tokens.radiusControl })
          Blank().layoutWeight(1)
          SymbolGlyph($r('sys.symbol.arrow_up_right')).fontSize('12vp').fontColor([Colors.tertiary]).accessibilityLevel('no')
        }.width('100%').alignItems(VerticalAlign.Top)
        Text(item.title).fontSize(Tokens.typeSecondary).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSecondary))
          .lineHeight(Tokens.lineHeightSecondary).fontWeight(FontWeight.Medium).fontColor(Colors.ink)
          .width('100%').textAlign(TextAlign.Start).maxLines(3).textOverflow({ overflow: TextOverflow.Ellipsis })
      }.width('100%').constraintSize({ minHeight: 106 }).alignItems(HorizontalAlign.Start).justifyContent(FlexAlign.SpaceBetween).padding(14)
    }.width('100%').padding(0)
      .backgroundColor(Colors.styleCard || Colors.codeSurface)
      .borderRadius(Tokens.radiusCard)
      .border({
        width: PhoneStyle.surfaceEdgeWidth(0.5),
        color: PhoneStyle.cardBorder(this.environment.dark),
        radius: Tokens.radiusCard
      })
      .shadow(PhoneStyle.cardElevation(this.environment.dark))
      .scale({
        x: this.pressedSuggestion === item.id ? MotionTokens.pressScale : 1,
        y: this.pressedSuggestion === item.id ? MotionTokens.pressScale : 1
      })
      .animation(Motion.release(this.environment.reduceMotion))
      .onTouch((event: TouchEvent) => {
        if (event.type === TouchType.Down) this.pressedSuggestion = item.id;
        else if (event.type === TouchType.Up || event.type === TouchType.Cancel) {
          if (this.pressedSuggestion === item.id) this.pressedSuggestion = '';
        }
      })
      .accessibilityText(item.title).enabled(item.local || this.canAsk)
      .onClick(() => { Haptics.tap(); this.choose(item); }).id('welcome.suggestion.' + item.id)
  }
  private questionTint(id: string): ResourceColor {
    if (id === 'today' || id === 'agenda') return Colors.needsMark;
    if (id === 'connect_mail' || id === 'mail') return Colors.ai;
    if (id === 'largest_folders') return Colors.workingMark;
    if (id === 'agents') return Colors.teal;
    if (id === 'lark') return Colors.positive;
    return Colors.link;
  }
  private questionWell(id: string): ResourceColor {
    if (id === 'today' || id === 'agenda') return Colors.needsWash;
    if (id === 'connect_mail' || id === 'mail') return Colors.aiWash;
    if (id === 'largest_folders') return Colors.workingWash;
    if (id === 'agents') return Colors.tealWash;
    if (id === 'lark') return Colors.unreadWash;
    return Colors.accentWell;
  }
  private placeTint(key: string): ResourceColor {
    if (key === 'projects') return Colors.workingMark;
    if (key === 'inbox') return Colors.ai;
    return Colors.positive;
  }
  @Builder helpSheet() {
    Column({ space: Tokens.page }) {
      Text(tr(this.help?.id === 'how_to_connect_model' ? 'phone_chat_help_model' : 'phone_chat_help_skills'))
        .fontSize(Tokens.body).fontColor(Colors.ink).width('100%')
      if (this.help?.id === 'how_to_connect_model') {
        Button(tr('open_settings')).onClick(() => {
          if (this.ownerKey !== this.targetKey()) return;
          this.helpShown = false; this.onPlace('settings');
        }).id('welcome.help.settings')
      }
    }.width('100%').padding({ left: 20, right: 20, top: 14, bottom: 32 }).alignItems(HorizontalAlign.Start).id('welcome.help')
  }
  build() {
    Column({space:20}) {
      Column({space:12}) {
        Stack({ alignContent: Alignment.Center }) {
          Image(this.environment.dark ? $r('app.media.welcome_app_icon_dark') : $r('app.media.welcome_app_icon'))
            .width(52).height(52).borderRadius(14).objectFit(ImageFit.Cover)
        }
          .width(56)
          .height(56)
          .borderRadius(16)
          .backgroundColor(Colors.surface)
          .border({ width: PhoneStyle.surfaceEdgeWidth(0.75), color: PhoneStyle.cardBorder(this.environment.dark), radius: 16 })
          .shadow(PhoneStyle.cardElevation(this.environment.dark))
          .accessibilityLevel('no')
        Column({space:Tokens.spaceSm}) {
          Text(this.greeting()).fontSize(Tokens.typeDisplay).maxFontScale(Tokens.chromeFontMaxScale).lineHeight(Tokens.lineHeightDisplay).fontWeight(FontWeight.Bold).letterSpacing(-0.4).fontColor(Colors.ink).width('100%')
          Text(tr(this.mainConversation() ? 'phone_chat_intro_main' : 'phone_chat_intro_side')).fontSize(Tokens.typeSecondary)
            .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSecondary))
            .lineHeight(Tokens.lineHeightSecondary).fontColor(Colors.secondary).width('100%')
        }.width('100%').alignItems(HorizontalAlign.Start)
      }.width('100%').alignItems(HorizontalAlign.Start)
      if (this.suggestions.length) {
        GridRow({columns: this.contentWidth >= 300 && this.environment.fontScale < 1.5 ? 2 : 1, gutter: { x: 10, y: 10 }}) {
          ForEach(this.suggestions, (item: WelcomeSuggestion) => {
            GridCol({span:1}) { this.question(item) }
          }, (item: WelcomeSuggestion) => item.id)
        }.width('100%')
      }
      Flex({ wrap: FlexWrap.Wrap, alignItems: ItemAlign.Center, space: { main: LengthMetrics.vp(8), cross: LengthMetrics.vp(8) } }) {
        ForEach(['projects','inbox','todo'], (key: string) => {
          Button({type:ButtonType.Capsule}) {
            Row({space:Tokens.spaceSm}) {
              SymbolGlyph(key === 'projects' ? Symbols.folder : key === 'inbox' ? Symbols.inbox : Symbols.check)
                .fontSize('14vp').fontColor([this.placeTint(key)])
              Text(tr(key === 'todo' ? 'todos' : key)).fontSize(Tokens.typeSecondary).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSecondary))
                .fontWeight(FontWeight.Medium).fontColor(Colors.ink)
            }
          }.padding({left:Tokens.spaceM,right:14,top:Tokens.spaceS,bottom:Tokens.spaceS}).constraintSize({minHeight:Tokens.controlBar})
            .backgroundColor(Colors.styleCard || Colors.codeSurface)
            .border({ width: PhoneStyle.surfaceEdgeWidth(0.5), color: PhoneStyle.cardBorder(this.environment.dark), radius: Tokens.radiusCapsule })
            .shadow(PhoneStyle.cardElevation(this.environment.dark))
            .scale({
              x: this.pressedPlace === key ? MotionTokens.pressScale : 1,
              y: this.pressedPlace === key ? MotionTokens.pressScale : 1
            })
            .animation(Motion.release(this.environment.reduceMotion))
            .onTouch((event: TouchEvent) => {
              if (event.type === TouchType.Down) this.pressedPlace = key;
              else if (event.type === TouchType.Up || event.type === TouchType.Cancel) {
                if (this.pressedPlace === key) this.pressedPlace = '';
              }
            })
            .onClick(() => { Haptics.tap(); this.onPlace(key); }).id('welcome.' + key)
        }, (key: string) => key)
      }.width('100%')
    }.alignItems(HorizontalAlign.Start).width('100%').padding({left:Tokens.page,right:Tokens.page,top:24,bottom:14})
      .onAreaChange((_old: Area, area: Area) => { const width = Number(area.width); if (width > 0) this.contentWidth = width; })
      .bindSheet($$this.helpShown, this.helpSheet(), { title: { title: this.help?.title || '' }, height: SheetSize.FIT_CONTENT, showClose: true })
      .id('chat.welcome')
  }
}
