import { LengthMetrics } from '@kit.ArkUI';
import { ProjectAgent } from '../models/ProjectCatalog';
import { Colors } from '../design/Colors';
import { PhoneStyle } from '../design/PhoneStyle';
import { Symbols } from '../design/Symbols';
import { Tokens } from '../design/Tokens';
import { tr } from '../design/Strings';
import { deduplicateAgentPlace } from '../core/FoldPosture';
import { nativeEnvironment } from '../platform/NativeEnvironment';
import { ProjectAgentPlaceLine } from './ProjectAgentPlaceLine';
import { NeedsYouPulseDot, WorkingArcSpinner } from './StatusMotionMarks';

@ComponentV2
export struct AgentDirectoryRowContent {
  @Require @Param agent: ProjectAgent;
  @Param display:string='avatar';
  @Param previewRow:boolean=false;
  @Param muted:boolean=false;
  @Param hideState:boolean=false;
  @Local measuredCardHeight:number=0;
  @Local titleRoom:number=0;
  @Param age: string = '';
  @Param place: string = '';
  @Param showsSource: boolean = false;
  @Param sourceTitle: string = '';
  @Param starter: boolean = false;
  @Param narrow: boolean = false;
  @Param leftInset: number = 20;
  @Param controlScale: number = 1;
  @Param fontScale: number = 1;
  @Param tint: ResourceColor = Colors.secondary;
  @Param activityList: boolean = false;
  @Param useHerdrTabName: boolean = false;
  @Param renaming: boolean = false;

  private displayTitle(): string {
    if (this.renaming) return tr('agent_renaming');
    return ((this.useHerdrTabName && this.agent.herdrTabName) ? this.agent.herdrTabName : this.agent.row.title) || tr('agent_untitled');
  }

  @Builder agentMark(agent:ProjectAgent) {
    Stack({ alignContent: Alignment.BottomEnd }) {
      Stack() {
        if (agent.markRuntime.includes('claude')) Image($r('app.media.project_agent_claude')).width(17*this.controlScale).height(17*this.controlScale).objectFit(ImageFit.Contain)
        else if (agent.markRuntime.includes('codex') || agent.markRuntime.includes('openai'))
          Image($r('app.media.project_agent_openai')).width(17*this.controlScale).height(17*this.controlScale).fillColor(Colors.ink).objectFit(ImageFit.Contain)
        else if (agent.runtime) Text(agent.runtime.slice(0,1).toUpperCase()).fontSize('16vp').fontWeight(FontWeight.Medium).fontColor(Colors.ink)
        else SymbolGlyph(Symbols.agent).fontSize('17vp').fontColor([Colors.secondary])
      }.width(36*this.controlScale).height(36*this.controlScale).backgroundColor(Colors.subtle).borderRadius(18*this.controlScale)
      if (this.activityList && agent.state==='working' && agent.rank!==4)
        WorkingArcSpinner({diameter:17,tint:this.tint,surface:Colors.surface,showCoreDot:true}).offset({x:4.75,y:4.75})
      else if (this.activityList && agent.state==='working' && agent.rank===4)
        SymbolGlyph($r('sys.symbol.clock')).fontSize('17vp').fontColor([this.tint]).backgroundColor(Colors.surface).borderRadius(9)
      else if (agent.state==='needs_you' && !this.muted)
        NeedsYouPulseDot({dotSize:10,tint:this.tint,surface:Colors.surface,bordered:true}).offset({x:3,y:3})
      else if (agent.state && agent.state !== 'unknown') Circle().width(12).height(12).fill(this.tint)
        .stroke(Colors.surface).strokeWidth(2)
    }.width(36*this.controlScale).height(36*this.controlScale)
  }
  @Builder needsReply() {
    Text(tr('needs_you')).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).fontWeight(FontWeight.Medium).fontColor(Colors.needs)
      .padding({left:6,right:6,top:1,bottom:1}).backgroundColor(Colors.selection).borderRadius(6)
      .constraintSize({maxWidth:this.narrow ? '100%' : '72%'}).maxLines(1).textOverflow({overflow:TextOverflow.Ellipsis})
  }
  private previewLeading(): LengthMetrics {
    // The two-line iPhone subtitle adds leading between baselines; a single line keeps its intrinsic height.
    const pointSize = 15*Math.min(this.fontScale,Tokens.interfaceScaleFor(15));
    return LengthMetrics.vp(5+(pointSize-15)/6);
  }
  @Computed get avatarTimeBelow():boolean {
    if (!this.age || !this.titleRoom) return false;
    const ui=this.getUIContext();
    const timeWidth=ui.px2vp(Math.ceil(ui.getMeasureUtils().measureText({
      textContent:this.age,fontSize:Tokens.interfaceFontVp(12,this.fontScale)})));
    const remaining=this.titleRoom-timeWidth-6;
    if (remaining>=this.titleRoom*0.7) return false;
    const titleWidth=ui.px2vp(Math.ceil(ui.getMeasureUtils().measureText({textContent:this.displayTitle(),
      fontSize:Tokens.interfaceFontVp(17,this.fontScale),fontWeight:this.agent.state==='unread'?FontWeight.Bold:600})));
    return remaining<Math.min(titleWidth,this.titleRoom*0.7);
  }
  @Builder avatarTitle() {
    Text(this.displayTitle()).fontSize(17).maxFontScale(Tokens.interfaceScaleFor(17))
      .lineHeight(this.previewRow ? PhoneStyle.settingsExampleLineHeight(20,27.67,this.fontScale) : undefined)
      .letterSpacing(this.previewRow ? '-0.25vp' : undefined)
      .fontWeight(this.agent.state==='unread'?FontWeight.Bold:600).maxLines(1).textOverflow({overflow:TextOverflow.Ellipsis})
  }
  @Builder agentTitleAndTime() {
    Column({space:4}){
      if(this.avatarTimeBelow){
        this.avatarTitle()
        this.stamp()
      }else{
        Row({space:6}){
          Row(){this.avatarTitle()}.layoutWeight(1)
          this.stamp()
        }.width('100%')
      }
    }.width('100%').alignItems(HorizontalAlign.Start).onAreaChange((_:Area,now:Area)=>{
      const width=Number(now.width);
      if(Number.isFinite(width)&&width>0&&Math.abs(width-this.titleRoom)>.25)this.titleRoom=width;
    })
  }
  private displayPlace(): string {
    return deduplicateAgentPlace(this.displayTitle(), this.place);
  }
  @Builder avatarRow() {
    Row({space:12}) {
      this.agentMark(this.agent)
      Column({space:2}) {
        if(this.activityList){this.agentTitleAndTime()}
        else Flex({wrap:FlexWrap.Wrap,alignItems:ItemAlign.Baseline,space:{main:LengthMetrics.vp(8),cross:LengthMetrics.vp(4)}}) {
          Text(this.displayTitle()).fontSize(17).maxFontScale(Tokens.interfaceScaleFor(17))
            .fontWeight(this.agent.state === 'unread' ? FontWeight.Bold : 600).fontColor(Colors.ink)
            .constraintSize({maxWidth:'100%'}).flexGrow(1).maxLines(this.narrow ? 4 : 1).textOverflow({overflow:TextOverflow.Ellipsis})
          if (this.age) Text(this.age).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).fontColor(Colors.secondary).maxLines(1)
        }.width('100%')
        if (this.agent.preview) Row({space:8}) {
          Text(this.agent.preview).maxLines(this.previewRow && this.avatarTimeBelow && this.agent.state==='needs_you' ? 1 : this.narrow ? 4 : 2)
            .fontSize(15).maxFontScale(Tokens.interfaceScaleFor(15)).fontColor(Colors.secondary)
            .punctuationOverflow(this.previewRow).lineSpacing(this.previewLeading(),{onlyBetweenLines:true})
            .layoutWeight(1).textOverflow({overflow:TextOverflow.Ellipsis})
          if (this.agent.state === 'needs_you' && !this.narrow) this.needsReply()
        }.width('100%').alignItems(VerticalAlign.Top)
        if (this.agent.state === 'needs_you' && (!this.agent.preview || this.narrow)) this.needsReply()
        ProjectAgentPlaceLine({place:this.displayPlace(),modelName:this.agent.model,showsSource:this.showsSource,sourceTitle:this.sourceTitle,
          todoTitle:this.agent.todoTitle,sessionId:this.agent.row.id,starter:this.starter,fontScale:this.fontScale})
      }.alignItems(HorizontalAlign.Start).layoutWeight(1)
    }.width('100%').alignItems(VerticalAlign.Top).constraintSize({minHeight:56}).padding({
      left:this.previewRow?0:this.leftInset,right:this.previewRow?0:20,top:this.previewRow?4:15,bottom:this.previewRow?4:15
    })
  }
  @Builder tinyBrand() {
    if(this.agent.markRuntime.includes('codex') || this.agent.markRuntime.includes('openai'))
      Image($r('app.media.project_agent_openai')).width(14*this.controlScale).height(14*this.controlScale).objectFit(ImageFit.Contain)
    else if(this.agent.markRuntime.includes('claude'))
      Image($r('app.media.project_agent_claude')).width(14*this.controlScale).height(14*this.controlScale).objectFit(ImageFit.Contain)
    else SymbolGlyph(Symbols.agent).fontSize(String(14*this.controlScale)+'vp').fontColor([Colors.secondary])
  }
  @Builder mutedMark() {
    SymbolGlyph($r('sys.symbol.bell_slash')).fontSize(Tokens.interfaceFontVp(13,this.fontScale)).fontColor([Colors.secondary]).accessibilityText(tr('agent_group_muted'))
  }
  private reviewWord():string {
    return !this.muted && !this.hideState && [2,5].includes(this.agent.rank) ? tr(this.agent.stateLabel) : '';
  }
  private compactPreview():string {
    const status=this.reviewWord() || (!this.muted && !this.hideState && this.agent.rank===4 ? tr(this.agent.stateLabel) : '');
    return [status,this.agent.preview || this.displayPlace()].filter((part:string)=>!!part).join(' · ');
  }
  @Builder dot() {
    if(!this.hideState && !this.muted && this.agent.state!=='unknown')
      Stack(){
        if(this.agent.state==='working' && this.agent.rank!==4){
          WorkingArcSpinner({diameter:14*this.controlScale,tint:this.tint,surface:Color.Transparent,showCoreDot:true})
        }else if(this.agent.rank===4){
          SymbolGlyph($r('sys.symbol.clock')).fontSize(String(14*this.controlScale)+'vp').fontColor([this.tint])
        }else if(this.agent.state==='needs_you'){
          NeedsYouPulseDot({dotSize:8*this.controlScale,tint:this.tint,surface:Color.Transparent,bordered:false})
        }else{
          Circle().width(8*this.controlScale).height(8*this.controlScale).fill(this.tint)
        }
      }.width(14*this.controlScale).height(14*this.controlScale)
  }
  @Builder stamp() {
    if(this.age)Text(this.age).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).fontColor(Colors.secondary).maxLines(1)
  }
  @Builder topLine() {
    Flex({wrap:FlexWrap.Wrap,alignItems:ItemAlign.Center,space:{main:LengthMetrics.vp(6),cross:LengthMetrics.vp(2)}}){
      Row({space:4}){
        this.tinyBrand()
        Text(this.displayTitle()).fontSize(17).maxFontScale(Tokens.interfaceScaleFor(17)).fontColor(Colors.ink)
          .fontWeight(this.agent.state==='unread'?FontWeight.Bold:600).maxLines(1).textOverflow({overflow:TextOverflow.Ellipsis}).layoutWeight(1)
      }.flexGrow(1).constraintSize({minWidth:'42%',maxWidth:'100%'})
      this.stamp()
    }.width('100%')
  }
  @Builder compactRow() {
    Row({space:8}){
      this.dot()
      Column({space:2}){
        this.topLine()
        Row({space:4}){
          if(this.agent.state==='needs_you' && !this.hideState && !this.muted)this.needsReply()
          Text(this.compactPreview()).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13))
            .fontColor(Colors.secondary).maxLines(1).textOverflow({overflow:TextOverflow.Ellipsis}).layoutWeight(1)
          if(this.muted)this.mutedMark()
          if(this.showsSource)
            Text(this.sourceTitle || tr('terminal')).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontColor(Colors.ai)
              .maxLines(1).padding({left:5,right:5,top:1,bottom:1}).backgroundColor(Colors.subtle).borderRadius(7)
        }.width('100%')
      }.layoutWeight(1).alignItems(HorizontalAlign.Start)
    }.width('100%').alignItems(VerticalAlign.Top).constraintSize({minHeight:this.previewRow?0:44})
      .padding({left:this.previewRow?0:this.leftInset,right:this.previewRow?0:20,top:this.previewRow?2:16,bottom:this.previewRow?2:17})
  }
  @Builder statusCard() {
    Stack({alignContent:Alignment.Start}){
      Column({space:4}){
        this.topLine()
        if(this.agent.preview)Text(this.agent.preview).fontSize(15).maxFontScale(Tokens.interfaceScaleFor(15)).fontColor(Colors.secondary)
          .width('100%').maxLines(2).textOverflow({overflow:TextOverflow.Ellipsis})
        Row({space:8}){
          if(!this.hideState && !this.muted && this.agent.state!=='unknown' && (this.agent.state!=='idle' || !!this.reviewWord()))
            Text(tr(this.agent.stateLabel)).fontSize(13).fontWeight(FontWeight.Medium).maxFontScale(Tokens.interfaceScaleFor(13))
              .fontColor(this.tint).maxLines(1)
          ProjectAgentPlaceLine({place:this.displayPlace(),modelName:this.agent.model,showsSource:this.showsSource,sourceTitle:this.sourceTitle,
            todoTitle:this.agent.todoTitle,sessionId:this.agent.row.id,starter:this.starter,fontScale:this.fontScale})
        }.width('100%')
      }.width('100%').alignItems(HorizontalAlign.Start).padding({left:16,right:12,top:8,bottom:8})
        .onAreaChange((_old:Area,now:Area)=>{const h=Number(now.height);if(Number.isFinite(h)&&Math.abs(h-this.measuredCardHeight)>.25)this.measuredCardHeight=h;})
      if(this.measuredCardHeight>16 && !this.hideState && !this.muted && this.agent.state!=='unknown')
        Row().width(4*this.controlScale).height(this.measuredCardHeight-16).backgroundColor(this.tint).borderRadius(3).position({x:4,y:8})
    }.width('100%').borderRadius(14)
      .backgroundColor(this.agent.state==='needs_you'&&!this.muted?Colors.selection:Colors.sheetCard)
      .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.cardBorder(nativeEnvironment.dark),radius:14})
      .shadow(PhoneStyle.cardElevation(nativeEnvironment.dark)).id('agents.display.card')
  }
  @Builder singleLine() {
    Row({space:8}){
      this.tinyBrand()
      Stack(){ this.dot() }.width(16*this.controlScale)
      Text(this.displayTitle()).fontSize(15).maxFontScale(Tokens.interfaceScaleFor(15))
        .fontWeight(this.agent.state==='unread'?600:FontWeight.Regular).fontColor(Colors.ink)
        .maxLines(1).textOverflow({overflow:TextOverflow.Ellipsis}).layoutWeight(1)
      if(this.starter)Text(tr('session_space_starter')).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).fontColor(Colors.secondary).maxLines(1)
      if(this.displayPlace())Text(this.displayPlace()).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).fontColor(Colors.secondary)
        .constraintSize({maxWidth:'26%'}).maxLines(1).textOverflow({overflow:TextOverflow.Ellipsis})
      if(this.agent.todoTitle)SymbolGlyph($r('sys.symbol.list_checkmark')).fontSize(Tokens.interfaceFontVp(14,this.fontScale)).fontColor([Colors.link]).accessibilityText(this.agent.todoTitle)
      if(this.reviewWord())Text(this.reviewWord()).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontWeight(FontWeight.Medium).fontColor(this.tint).maxLines(1)
      if(this.muted)this.mutedMark()
      this.stamp()
    }.width('100%').constraintSize({minHeight:this.previewRow?0:44})
      .padding({left:this.previewRow?0:Math.max(0,this.leftInset-6),right:this.previewRow?0:10,top:this.previewRow?0:8,bottom:this.previewRow?0:8})
  }
  // BeautifulUI's native TaskRow uses a 24-point status column, then the words, then time and source.
  @Builder beautifulRow() {
    Row({space:10}){
      Stack(){ this.dot() }.width(24).height(24)
      Column({space:2}){
        Text(this.displayTitle()).fontSize(13).fontWeight(FontWeight.Medium)
          .maxFontScale(Tokens.interfaceScaleFor(13)).fontColor(Colors.ink).maxLines(2).textOverflow({overflow:TextOverflow.Ellipsis})
        if(this.agent.preview)Text(this.agent.preview).fontSize(12.5).maxFontScale(Tokens.interfaceScaleFor(13)).fontColor(Colors.secondary)
          .maxLines(1).textOverflow({overflow:TextOverflow.Ellipsis})
        Text([this.agent.runtime,this.displayPlace(),this.agent.todoTitle].filter((part:string)=>!!part).join(' · '))
          .fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).fontColor(Colors.tertiary).maxLines(1).textOverflow({overflow:TextOverflow.Ellipsis})
      }.layoutWeight(1).alignItems(HorizontalAlign.Start)
      Column({space:4}){
        this.stamp()
        if(this.showsSource)Text(this.sourceTitle || tr('terminal')).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).fontColor(Colors.secondary).maxLines(1)
        if(this.agent.state==='needs_you' && !this.muted && !this.hideState)this.needsReply()
        else if(this.reviewWord())Text(this.reviewWord()).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).fontColor(this.tint).maxLines(1)
        if(this.muted)this.mutedMark()
      }.alignItems(HorizontalAlign.End).constraintSize({maxWidth:'32%'})
    }.width('100%').alignItems(VerticalAlign.Top).padding(10).constraintSize({minHeight:44})
      .borderRadius(22).backgroundColor(Colors.sheetCard)
      .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.cardBorder(nativeEnvironment.dark),radius:22})
      .shadow(PhoneStyle.cardElevation(nativeEnvironment.dark))
  }
  build() {
    if(this.display==='list'){this.singleLine()}
    else if(this.display==='beautiful'){this.beautifulRow()}
    else if(this.display==='compact'){this.compactRow()}
    else if(this.display==='cards'){
      Column(){this.statusCard()}.width('100%').padding({left:this.previewRow?0:16+Math.max(0,this.leftInset-20),right:this.previewRow?0:16,top:this.previewRow?0:4,bottom:this.previewRow?0:4})
    }
    else{this.avatarRow()}
  }

}
