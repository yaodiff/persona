import { FrameCallback } from '@kit.ArkUI';
import { agentReplyBodySize, liftedMessageWords, markdownProseRuns, MarkdownProseRun } from '../core/Markdown';
import { MessageLiftFrame, MessageLiftPlacement, placeMessageLift } from '../core/MessageLift';
import { Colors } from '../design/Colors';
import { IconActions } from '../design/IconActions';
import { tr } from '../design/Strings';
import { Symbols } from '../design/Symbols';
import { Tokens } from '../design/Tokens';
import { Motion } from '../design/Motion';
import { nativeEnvironment, NativeEnvironment } from '../platform/NativeEnvironment';
import { MarkdownText } from './MarkdownText';

class LiftFrameCallback extends FrameCallback {
  private action: () => void;
  constructor(action: () => void) { super(); this.action = action; }
  onFrame(_time: number): void { this.action(); }
}

export interface PhoneLiftPresentation {
  content: () => void;
  didDismiss: () => void;
}

/** The window keeps the underlying chat active while its selected message and handles sit above the navigation. */
@ObservedV2
export class PhoneMessageLiftHost {
  @Trace current: PhoneLiftPresentation | undefined;
  present(presentation: PhoneLiftPresentation): void {
    this.dismiss();
    this.current=presentation;
  }
  dismiss(): boolean {
    const item=this.current;
    if (!item) return false;
    this.current=undefined; item.didDismiss(); return true;
  }
}

@ComponentV2
export struct PhoneMessageLiftHostLayer {
  @BuilderParam content: () => void;
  build() { Stack({alignContent:Alignment.TopStart}) { this.content() }.width('100%').height('100%') }
}

@ComponentV2
export struct PhoneMessageLift {
  @Require @Param text: string;
  @Require @Param frame: MessageLiftFrame;
  @Param own: boolean = false;
  @Param hasMore: boolean = false;
  @Param canForward: boolean = false;
  @Event onForward: () => void = () => {};
  @Event onSelectMessages: () => void = () => {};
  @Param selectOnOpen: boolean = false;
  @BuilderParam moreMenu: () => void;
  @Event onCopyText: (text: string) => void = () => {};
  @Event onQuoteText: (text?: string) => void = () => {};
  @Event onClose: () => void = () => {};
  @Local environment: NativeEnvironment = nativeEnvironment;
  @Local selecting: boolean = false;
  @Local entered: boolean = false;
  @Local windowTop: number = 0;
  @Local windowHeight: number = 1;
  @Local windowWidth: number = 1;
  private controller: TextController = new TextController();
  private scroll: Scroller = new Scroller();
  private start: number = 0;
  private end: number = 0;
  private open: boolean = false;
  private readonly quoteId: TextMenuItemId = TextMenuItemId.of('persona.quote');

  aboutToAppear(): void { this.open = true; this.selecting = this.selectOnOpen; this.entered = this.environment.reduceMotion; }
  aboutToDisappear(): void { this.open = false; this.entered = false; }

  @Computed get words(): string { return liftedMessageWords(this.text,this.own); }
  @Computed get runs(): MarkdownProseRun[] { return markdownProseRuns(this.words); }
  @Computed get actionsHeight(): number { return Math.max(62,51 + 11 * Math.min(this.environment.fontScale,1.5)); }
  @Computed get multipleHeight(): number { return Math.max(44,44*Math.min(this.environment.fontScale,1.31)); }
  @Computed get barHeight(): number { return this.actionsHeight + (this.canForward ? this.multipleHeight : 0); }
  @Computed get barWidth(): number { return Math.min(Math.max(1,this.windowWidth-16),12+58*(3+(this.hasMore ? 1 : 0)+(this.canForward ? 1 : 0))); }
  @Computed get placement(): MessageLiftPlacement {
    const ui=this.getUIContext();
    const top=this.environment.isFullScreen && this.windowHeight>500 ? Math.max(Tokens.compactStatusInset,ui.px2vp(this.environment.safeTopPx)) :
      ui.px2vp(this.environment.safeTopPx);
    // NativeEnvironment stores the safe bottom's screen coordinate, as used by the keyboard and windows.
    const layerBottom=this.windowTop+this.windowHeight, systemBottom=ui.px2vp(this.environment.safeBottomPx);
    const bottom=systemBottom>this.windowTop ? Math.min(layerBottom,systemBottom) : layerBottom;
    return placeMessageLift(this.frame,Math.max(top,this.windowTop),bottom,this.barHeight);
  }

  private chooseWords(start: number, end: number): void {
    this.start=start; this.end=end;
    this.getUIContext().postFrameCallback(new LiftFrameCallback((): void => {
      if (this.open && this.selecting && this.start < this.end)
        this.controller.setTextSelection(this.start,this.end,{ menuPolicy:MenuPolicy.SHOW });
    }));
  }
  @Monitor('environment.fontScale') typeChanged(): void {
    if (this.selecting && this.start < this.end) this.chooseWords(this.start,this.end);
  }
  private dismissSelection(): void {
    this.controller.setTextSelection(0,0,{menuPolicy:MenuPolicy.HIDE});
    this.controller.closeSelectionMenu();
    this.selecting=false; this.start=0; this.end=0;
  }
  private finishCopy(text: string): void { if (text) this.onCopyText(text); this.onClose(); }
  private quote(text?: string): void { this.onQuoteText(text); this.onClose(); }
  private actions(items: TextMenuItem[]): TextMenuItem[] {
    return [{ id:this.quoteId,content:tr('chat_lift_quote') }, ...items];
  }
  private act(item: TextMenuItem, range: TextRange): boolean {
    const start = range.start ?? this.start, end = range.end ?? this.end;
    const value = this.words.slice(start,end);
    if (item.id.equals(this.quoteId)) { if (value.trim()) this.quote(value); return true; }
    if (item.id.equals(TextMenuItemId.COPY)) { if (value) this.finishCopy(value); return true; }
    // Search, Translate and the other actions offered by the installed system keep their native handlers.
    return false;
  }
  private select(): void { this.selecting=true; this.chooseWords(0,this.words.length); }

  @Builder barButton(key: string, symbol: Resource, id: string, action: () => void) {
    Button({type:ButtonType.Normal}) {
      Column({space:4}) {
        SymbolGlyph(symbol).fontSize(20).fontColor([Colors.ink])
        Text(tr(key)).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).fontColor(Colors.ink).maxLines(1)
      }.alignItems(HorizontalAlign.Center)
    }.width(58).height(this.actionsHeight).padding(0).backgroundColor(Color.Transparent)
      .accessibilityText(tr(key)).id('chat.lift.'+id).onClick(action)
  }
  @Builder selectionLayer() {
    // The native Text owns the range, handles and system menu. The message below keeps its Markdown rendering.
    Text(undefined,{controller:this.controller}) {
      ForEach(this.runs,(run: MarkdownProseRun) => {
        Span(run.text).fontSize(String(agentReplyBodySize(this.frame.fontScale,run.han && this.frame.fontScale!==1))+'vp').fontColor(Color.Transparent)
      })
    }.width('100%').fontSize(String(agentReplyBodySize(this.frame.fontScale))+'vp')
      .lineHeight(String(this.own ? 17*this.frame.fontScale*1.45 : agentReplyBodySize(this.frame.fontScale)*1.44)+'vp')
      .wordBreak(WordBreak.BREAK_WORD).copyOption(CopyOptions.LocalDevice)
      .editMenuOptions({onCreateMenu:(items: TextMenuItem[]): TextMenuItem[] => this.actions(items),
        onMenuItemClick:(item:TextMenuItem, range:TextRange):boolean => this.act(item,range)})
      .onTextSelectionChange((start:number,end:number) => {
        if (end>start) { this.start=start; this.end=end; this.selecting=true; }
      }).onCopy(() => this.onClose()).onAppear(() => {
        if (this.selectOnOpen) this.select();
      }).accessibilityText(this.words).accessibilityLevel(this.selecting ? 'yes' : 'no').id('chat.lift.text')
  }
  build() {
    Stack({alignContent:Alignment.TopStart}) {
      Rect().width('100%').height('100%').fill(this.environment.dark ? '#4D000000' : '#14000000')
        .opacity(this.entered ? 1 : 0).animation(Motion.reveal(this.environment.reduceMotion))
        .accessibilityText(tr('close')).accessibilityRole(AccessibilityRoleType.BUTTON).id('chat.lift.close')
        .onClick(() => { if (this.selecting) this.dismissSelection(); else this.onClose(); })
      Scroll(this.scroll) {
        Stack({alignContent:Alignment.TopStart}) {
          MarkdownText({text:this.text,chatReply:!this.own,agentReply:!this.own,agentPrompt:this.own,
            agentFontScale:this.frame.fontScale,fixedType:true,nativeTextSelection:false})
          this.selectionLayer()
        }.width('100%').padding({left:8,right:8,top:12,bottom:12})
      }.width(this.frame.width).height(this.placement.height).align(Alignment.TopStart)
        .backgroundColor(this.own ? Colors.control : Colors.surface).borderRadius(16).clip(true)
        .border({width:0.5,color:Colors.divider,radius:16}).shadow({radius:12,color:'#26000000',offsetY:4})
        .scale({x:this.entered ? 1 : 0.96,y:this.entered ? 1 : 0.96}).opacity(this.entered ? 1 : 0.85)
        .animation(Motion.release(this.environment.reduceMotion))
        .position({x:this.frame.left,y:this.placement.top-this.windowTop}).id('chat.lift.message')
        .onAppear(() => {
          this.getUIContext().postFrameCallback(new LiftFrameCallback((): void => {
            if (!this.open) return;
            if (!this.entered) Motion.run(this.getUIContext(), Motion.reveal(this.environment.reduceMotion), () => { this.entered = true; });
            if (this.placement.offset>0) this.scroll.scrollTo({xOffset:0,yOffset:this.placement.offset});
          }));
        })
      if (!this.selecting) {
        Column() {
        Row() {
          this.barButton('copy',IconActions.symbol('copy'),'copy',() => this.finishCopy(this.text))
          this.barButton('chat_lift_select',$r('sys.symbol.textformat'),'select',() => this.select())
          this.barButton('chat_lift_quote',$r('sys.symbol.ellipsis_bubble'),'quote',() => this.quote())
          if (this.canForward) this.barButton('chat_forward_action',$r('sys.symbol.rectangle_and_arrowshape_turn_up_right'),'forward',this.onForward)
          if (this.hasMore) {
            Button({type:ButtonType.Normal}) {
              Column({space:4}) {
                Image(Symbols.more).width(20).height(20).fillColor(Colors.ink)
                Text(tr('more')).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).fontColor(Colors.ink)
              }
            }.width(58).height(this.actionsHeight).padding(0).backgroundColor(Color.Transparent)
              .accessibilityText(tr('more')).id('chat.lift.more').bindMenu(this.moreMenu)
          }
        }.width('100%').height(this.actionsHeight).padding({left:6,right:6})
        if (this.canForward) {
          Button({type:ButtonType.Normal}) {
            Row({space:10}) {
              SymbolGlyph($r('sys.symbol.checkmark_circle')).fontSize(18).fontColor([Colors.ink])
              Text(tr('chat_lift_multiple')).fontSize(15).maxFontScale(Tokens.interfaceScaleFor(15)).fontColor(Colors.ink)
            }.width('100%').alignItems(VerticalAlign.Center)
          }.width('100%').height(this.multipleHeight).padding({left:16,right:16}).backgroundColor(Color.Transparent)
            .accessibilityText(tr('chat_lift_multiple')).id('chat.selectMessages').onClick(this.onSelectMessages)
        }
        }.width(this.barWidth).height(this.barHeight)
          .backgroundColor(Colors.surface).borderRadius(12).shadow({radius:10,color:'#26000000',offsetY:3})
          .opacity(this.entered ? 1 : 0).scale({x:this.entered ? 1 : 0.94,y:this.entered ? 1 : 0.94})
          .animation(Motion.state(this.environment.reduceMotion))
          .position({x:Math.max(8,Math.min(this.own ? this.frame.left+this.frame.width-this.barWidth : this.frame.left,this.windowWidth-this.barWidth-8)),
            y:this.placement.barTop-this.windowTop}).id('chat.lift.actions')
      }
    }.width('100%').height('100%').id('chat.lift').onAreaChange((_old:Area,area:Area) => {
      this.windowWidth=Number(area.width); this.windowHeight=Number(area.height); this.windowTop=Number(area.globalPosition.y);
    })
  }
}
