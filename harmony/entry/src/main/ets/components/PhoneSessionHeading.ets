import { i18n } from '@kit.LocalizationKit';
import { Colors } from '../design/Colors';
import { IconActions } from '../design/IconActions';
import { PhoneIconButton } from '../design/PhoneStyle';
import { PhoneOutlineIcon } from '../design/PhoneOutlineIcon';
import { tr, trError } from '../design/Strings';
import { Symbols } from '../design/Symbols';
import { Tokens } from '../design/Tokens';
import { AgentHeaderWords, AgentSessionInfo, agentHeaderWords, sessionInfoPath } from '../models/AgentSessionInfo';
import { ClientModel } from '../models/ClientModel';
import { SessionSpace } from '../models/SessionSpace';
import { Wire } from '../models/Wire';
import { nativeEnvironment } from '../platform/NativeEnvironment';
import { Haptics } from '../platform/Haptics';
import { NeedsYouPulseDot, WorkingArcSpinner } from './StatusMotionMarks';

class InfoLine {
  readonly id: string;
  readonly label: string;
  readonly value: string;
  readonly icon?: Resource;
  constructor(id: string, label: string, value: string, icon?: Resource) {
    this.id = id; this.label = label; this.value = value; this.icon = icon;
  }
}

@ComponentV2
export struct PhoneSessionHeading {
  @Require @Param model: ClientModel;
  @Require @Param space: SessionSpace;
  @Require @Param heading: string;
  @Param slim:boolean=false;
  @Event expand:() => void=() => {};
  @Event onSheetChange:(shown:boolean) => void=() => {};
  @Monitor('info.shown') sheetChanged():void { this.onSheetChange(this.info.shown); }
  @Local info: AgentSessionInfo = new AgentSessionInfo();
  @Local available: number = 0;
  aboutToDisappear(): void { this.info.close(); }
  @Monitor('model','model.profile','model.selected','model.connected') retarget(): void {
    if (this.info.shown && !this.info.owns(this.model)) this.info.close();
  }
  @Computed get words(): AgentHeaderWords {
    const row = this.model.selected;
    return agentHeaderWords(row,this.model.machineName(row.machine),this.space.title || Wire.text(row.data(),'space_name'),this.space.tabs.length > 0,tr);
  }
  private mark(): string {
    const data = this.model.selected.data();
    return Wire.text(data,'agent',Wire.text(data,'launcher')).toLowerCase();
  }
  private tone(): ResourceColor {
    const state = this.words.state;
    return state === 'needs_you' ? Colors.needs : state === 'working' ? Colors.working : state === 'unread' ? Colors.unread : Colors.secondary;
  }
  @Computed get visibleSubtitle(): string {
    if (!this.model.connected) return tr('offline');
    if (this.words.short === this.words.full || !this.available) return this.words.full;
    const ui = this.getUIContext(), scale = nativeEnvironment.fontScale;
    const width = ui.px2vp(ui.getMeasureUtils().measureText({textContent:this.words.full,fontSize:Tokens.interfaceFontVp(12,scale)}));
    // A quiet compact title keeps the place first. An expanded or accessibility-size bar can also name its machine.
    const compactQuiet = ['idle','offline','unknown'].includes(this.words.state) && this.available <= 270 && scale <= 1.1;
    return compactQuiet || width + 30 > this.available ? this.words.short : this.words.full;
  }
  private markSize(): number { return 12*Math.min(nativeEnvironment.fontScale,Tokens.navigationTextMaxScale); }
  private raw(key: string): string { return Wire.text(this.info.detail,key,Wire.text(this.model.selected.data(),key)); }
  private folder(): string {
    const host = this.model.machines.find((machine) => machine.id === this.model.selected.machine);
    return sessionInfoPath(this.raw('cwd'),host ? Wire.text(host.data(),'home') : '');
  }
  private effort(): string {
    const value = this.raw('effort');
    return value === 'xhigh' ? tr('phone_session_extra_high') : ['low','medium','high','max'].includes(value) ? tr(value) : value;
  }
  private permission(): string {
    const value = this.raw('permission_mode');
    return ['default','plan','auto'].includes(value) ? tr('assistant_feed_permission_'+value) : value;
  }
  private tokenCount(key: string): string {
    const n = this.info.detail[key];
    if (typeof n !== 'number' || !Number.isFinite(n)) return '';
    const text = n >= 1000000 ? String(Math.round(n/100000)/10)+'M' : n >= 1000 ? String(Math.round(n/100)/10)+'K' : String(n);
    return text + ' tokens';
  }
  private started(): string {
    const date = new Date(this.raw('started_at'));
    if (!Number.isFinite(date.getTime())) return '';
    const locale = this.model.language === 'default' ? i18n.System.getSystemLanguage() : this.model.language;
    return date.toLocaleString(locale,{year:'numeric',month:'short',day:'numeric',hour:'numeric',minute:'2-digit'});
  }
  @Builder emblem() {
    if (this.mark().includes('claude')) Image($r('app.media.project_agent_claude')).width(this.markSize()).height(this.markSize()).objectFit(ImageFit.Contain)
    else if (this.mark().includes('codex') || this.mark()==='openai') Image($r('app.media.project_agent_openai')).width(this.markSize()).height(this.markSize()).objectFit(ImageFit.Contain)
    else SymbolGlyph(Symbols.agent).fontSize('12vp').fontColor([Colors.secondary])
  }
  private runtimeRows(): InfoLine[] {
    const agent = this.mark().includes('claude') ? 'Claude Code' : this.mark().includes('codex') ? 'Codex' : tr('version');
    return [
      new InfoLine('model',tr('model'),this.raw('model').split('/').pop() || ''),
      new InfoLine('launcher',tr('launcher'),this.raw('launcher'),$r('sys.symbol.bolt')),
      new InfoLine('version',agent,this.raw('version')),
      new InfoLine('permissions',tr('phone_session_permissions'),this.permission(),$r('sys.symbol.lock_open')),
      new InfoLine('effort',tr('phone_session_effort'),this.effort(),Symbols.health),
    ];
  }
  private usageRows(): InfoLine[] {
    const turns = this.info.detail['turns'];
    return [
      new InfoLine('context',tr('phone_session_context'),this.tokenCount('context_tokens'),Symbols.health),
      new InfoLine('turns',tr('chat'),typeof turns === 'number' ?
        tr(turns === 1 ? 'phone_session_turn_one' : 'phone_session_turns').replace('{n}',String(turns)) : '',Symbols.persona),
      new InfoLine('output',tr('phone_session_output'),this.tokenCount('output_tokens'),$r('sys.symbol.textformat')),
      new InfoLine('started',tr('phone_session_started'),this.started(),$r('sys.symbol.clock')),
    ];
  }
  private locationRows(): InfoLine[] {
    return [
      new InfoLine('folder',tr('phone_session_folder'),this.folder(),Symbols.folder),
      new InfoLine('branch',tr('branch'),this.raw('git_branch'),$r('sys.symbol.arrow_triangle_divide')),
    ];
  }
  @Computed get infoScale(): number {
    return Math.min(nativeEnvironment.fontScale,Tokens.interfaceFontMaxScale);
  }
  private iconColumn(): number { return 8+20*this.infoScale; }
  @Builder entry(line: InfoLine) {
    Row({space:13}) {
      Stack() {
        if (line.icon) {
          SymbolGlyph(line.icon).fontSize(String(22*this.infoScale)+'vp').fontColor([Colors.link])
        } else {
          PhoneOutlineIcon({name:line.id === 'version' ? 'package' : 'cube',iconScale:this.infoScale})
        }
      }.width(this.iconColumn()).accessibilityLevel('no').id('session.info.icon.'+line.id)
      Text(line.label).fontSize(17).maxFontScale(Tokens.interfaceScaleFor(17)).fontColor(Colors.ink)
        .maxLines(1).textOverflow({overflow:TextOverflow.Ellipsis}).layoutWeight(1)
      Text(line.value).fontSize(15).maxFontScale(Tokens.interfaceScaleFor(15)).fontColor(Colors.secondary)
        .fontFamily(line.id === 'folder' || line.id === 'branch' ? 'monospace' : 'sans-serif').textAlign(TextAlign.End)
        .constraintSize({maxWidth:'59%'}).maxLines(2).textOverflow({overflow:TextOverflow.Ellipsis}).ellipsisMode(EllipsisMode.CENTER)
    }.width('100%').constraintSize({minHeight:30+20*this.infoScale}).padding({left:16,right:18,top:7,bottom:7})
      .accessibilityGroup(true).id('session.info.'+line.id)
  }
  @Builder rows(lines: InfoLine[]) {
    Column() {
      ForEach(lines.filter((line: InfoLine) => !!line.value),(line: InfoLine,index: number) => {
        Stack({alignContent:Alignment.TopStart}) {
          this.entry(line)
          if (index > 0) {
            Divider().strokeWidth(Tokens.divider).color(Colors.divider).margin({left:29+this.iconColumn(),right:18})
              .hitTestBehavior(HitTestMode.None).accessibilityLevel('no').id('session.info.divider.'+line.id)
          }
        }.width('100%')
      },(line: InfoLine) => JSON.stringify([line.id,line.label,line.value]))
    }.width('100%').backgroundColor(Colors.surface).borderRadius(20).clip(true)
  }
  @Builder sectionTitle(key: string) {
    Text(tr(key)).width('100%').fontSize(15).maxFontScale(Tokens.interfaceScaleFor(15)).fontWeight(500)
      .fontColor(Colors.secondary).padding({left:16,top:18,bottom:8})
  }
  @Builder statusDot() {
    if (this.words.state === 'working') {
      WorkingArcSpinner({ diameter: 11, tint: Colors.working, surface: Color.Transparent, showCoreDot: false })
    } else if (this.words.state === 'needs_you') {
      NeedsYouPulseDot({ dotSize: 7, tint: Colors.needs, surface: Colors.canvas, bordered: false })
    } else {
      Circle().width(7).height(7).fill(this.tone()).accessibilityLevel('no')
    }
  }
  @Builder summary() {
    Column({space:6}) {
      Text(this.heading).width('100%').fontSize(22).maxFontScale(Tokens.interfaceScaleFor(22)).fontWeight(600)
        .fontColor(Colors.ink).maxLines(2).textOverflow({overflow:TextOverflow.Ellipsis}).id('session.info.title')
      Row({space:5}) {
        Row({space:4}) {
          this.statusDot()
          Text(this.words.status).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).fontWeight(600)
            .fontColor(this.tone()).maxLines(1)
        }.padding({left:7,right:7,top:4,bottom:4}).backgroundColor(Colors.control).borderRadius(50)
          .accessibilityGroup(true).id('session.info.status')
        if (this.words.location) {
          Text(this.words.location).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).fontColor(Colors.secondary)
            .maxLines(1).textOverflow({overflow:TextOverflow.Ellipsis}).ellipsisMode(EllipsisMode.CENTER)
            .flexShrink(1).id('session.info.place')
        }
      }.width('100%')
    }.width('100%').alignItems(HorizontalAlign.Start).padding({left:4,right:4,bottom:4})
  }
  @Builder contents() {
    Column() {
      Row() {
        PhoneIconButton({symbol:IconActions.symbol('close'),label:tr('close'),identity:'session.info.close',action:() => this.info.close()})
        Text(tr('phone_session_info')).layoutWeight(1).textAlign(TextAlign.Center).fontSize(17)
          .maxFontScale(Tokens.interfaceScaleFor(17)).fontWeight(600).fontColor(Colors.ink)
        Blank().width(44)
      }.width('100%').padding({left:14,right:14,top:6,bottom:5})
      Scroll() {
        Column() {
          this.summary()
          if (this.info.loading) LoadingProgress().width(24).height(24).margin({top:14})
          if (this.info.error) Text(trError(this.info.error)).fontSize(14).fontColor(Colors.secondary).width('100%').margin({top:12})
          if (Object.keys(this.info.detail).length) {
            this.sectionTitle('phone_session_runtime')
            this.rows(this.runtimeRows())
            if (!Wire.bool(this.info.detail,'usage_unknown')) {
              this.sectionTitle('phone_session_usage')
              this.rows(this.usageRows())
            }
            this.sectionTitle('location')
            this.rows(this.locationRows())
          }
        }.width('100%').padding({left:24,right:24,top:38,bottom:32}).alignItems(HorizontalAlign.Start)
      }.width('100%').layoutWeight(1)
    }.width('100%').height('100%').backgroundColor(Colors.grouped).id('session.info')
  }
  @Builder compactTitle() {
    Row() {
      Button({type:ButtonType.Capsule}) {
        Row({space:6}) {
          if (this.model.connected) {
            this.emblem()
            this.statusDot()
          } else LoadingProgress().width(12).height(12)
          Text(this.model.connected ? this.heading : tr('offline')).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13))
            .fontWeight(600).fontColor(Colors.ink).maxLines(1).textOverflow({overflow:TextOverflow.Ellipsis}).flexShrink(1)
        }.padding({left:12,right:12})
      }.padding(0).constraintSize({maxWidth:'100%'}).height(28*Math.min(nativeEnvironment.fontScale,Tokens.navigationTextMaxScale))
        .backgroundColor(Colors.control).accessibilityText(tr('agent_reading_expand_header').replace('{title}',this.model.connected ? this.heading : tr('offline')))
        .onClick(() => { Haptics.tap(); this.expand(); }).id('chat.compactTitle')
    }.width('100%').height(40).justifyContent(FlexAlign.Center)
  }
  build() {
    if (this.slim) this.compactTitle()
    else Column({space:2}) {
      Text(this.heading).fontSize(17).maxFontScale(Tokens.navigationTextMaxScale).fontWeight(600).fontColor(Colors.ink)
        .maxLines(1).textOverflow({overflow:TextOverflow.Ellipsis})
      Row({space:4}) {
        this.emblem()
        this.statusDot()
        Text() {
          if (this.model.connected && ['needs_you','working','unread'].includes(this.words.state)) {
            Span(this.words.status).fontColor(this.tone()).fontWeight(600)
            Span(this.visibleSubtitle.slice(this.words.status.length)).fontColor(Colors.secondary)
          } else Span(this.visibleSubtitle).fontColor(Colors.secondary)
        }.fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).fontColor(Colors.secondary)
          .maxLines(1).textOverflow({overflow:TextOverflow.Ellipsis}).flexShrink(1).id('chat.title.subtitle')
      }.justifyContent(FlexAlign.Center).constraintSize({maxWidth:'100%'})
    }.width('100%').alignItems(HorizontalAlign.Center).justifyContent(FlexAlign.Center).constraintSize({minHeight:44})
      .onAreaChange((_:Area,area:Area) => { this.available = Number(area.width); })
      .onClick(() => { Haptics.tap(); this.info.open(this.model).catch(() => undefined); }).accessibilityGroup(true).accessibilityRole(AccessibilityRoleType.BUTTON)
      .accessibilityText(tr('phone_session_info_action').replace('{title}',this.heading)).id('chat.title')
      .bindSheet(this.info.shown,this.contents(),{height:'56%',detents:['56%',SheetSize.LARGE],dragBar:true,showClose:false,
        backgroundColor:Colors.grouped,onWillDismiss:(action:DismissSheetAction) => { this.info.close(); action.dismiss(); }})
  }
}
