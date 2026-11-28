import { PhoneTextSizePreference, phoneTextSize } from '../platform/PhoneTextSizePreference';
import { ClientModel } from '../models/ClientModel';
import { DeviceLook } from '../models/DeviceLook';
import { ProjectAgent } from '../models/ProjectCatalog';
import { ResourceRow } from '../models/Wire';
import { AgentDirectoryRowContent } from './AgentDirectoryRowContent';
import { deviceLookPalette, deviceLookSurface, PhoneStyleSurface } from '../core/DeviceLook';
import { phoneInterfaceStyleChoices, phoneInterfaceStyleID } from '../core/PhoneInterfaceStyle';
import { phoneAppearancePages } from '../core/PhoneSettingsNavigation';
import { NativeEnvironment, nativeEnvironment } from '../platform/NativeEnvironment';
import { Colors } from '../design/Colors';
import { Motion } from '../design/Motion';
import { PhoneSettingsRow, PhoneStyle } from '../design/PhoneStyle';
import { Symbols } from '../design/Symbols';
import { Tokens } from '../design/Tokens';
import { tr } from '../design/Strings';
import { Haptics } from '../platform/Haptics';

const NAVIGATION: string[] = ['merged','separate'];
const DISPLAY: string[] = ['avatar','compact','cards','list'];
const SCHEME: string[] = ['system','light','dark'];

@ComponentV2
export struct PhoneAppearanceSettings {
  @Require @Param model: ClientModel;
  @Require @Param look: DeviceLook;
  @Require @Param route: string;
  @Param active: boolean = false;
  @Event onOpen: (page: string) => void = () => {};
  private owner: ClientModel | undefined;
  private ownerScope: string = '';
  private ownerGeneration: number = -1;
  aboutToAppear(): void {
    this.owner = this.model;
    this.ownerScope = this.model.scope();
    this.ownerGeneration = this.model.connectionGeneration();
    this.model.loadNextSuggestions().catch(() => undefined);
  }
  @Monitor('model.profile', 'model.connected')
  nextSuggestionsOwnerChanged(): void {
    this.model.loadNextSuggestions().catch(() => undefined);
  }
  aboutToDisappear(): void { this.owner = undefined; }
  @Local textSize: PhoneTextSizePreference = phoneTextSize;
  @Local environment: NativeEnvironment = nativeEnvironment;

  private selected(page: string): string {
    if (page === 'phone_look_color') return this.look.accentId;
    if (page === 'phone_look_style') return phoneInterfaceStyleID(this.model.style);
    if (page === 'phone_look_navigation') return this.look.phoneNavigation;
    if (page === 'phone_look_layout') return this.look.agentDisplay;
    return SCHEME[this.model.appearance] || 'system';
  }
  private label(page: string, value: string): string {
    if (page === 'phone_look_color') return ['style','avatar'].includes(value) ? tr('device_look_accent_'+value) : tr('phone_look_color_'+value);
    if (page === 'phone_look_style' && (value === 'brief' || value === 'board')) return tr('app_style_'+value);
    return tr(page+'_'+value);
  }
  private options(page: string): string[] {
    if (page === 'phone_look_style') return phoneInterfaceStyleChoices(this.model.style);
    if (page === 'phone_look_navigation') return NAVIGATION;
    if (page === 'phone_look_layout') return DISPLAY;
    return SCHEME;
  }
  private select(page: string, value: string): void {
    const owner = this.owner;
    if (!this.active || !this.look.ready || this.route !== page || !owner || owner !== this.model ||
      !owner.owns(this.ownerGeneration,this.ownerScope)) return;
    if (page === 'phone_look_color') this.look.setAccent(value);
    else if (page === 'phone_look_style') owner.setStyle(value);
    else if (page === 'phone_look_navigation') this.look.setPhoneNavigation(value);
    else if (page === 'phone_look_layout') this.look.setAgentDisplay(value);
    else if (page === 'phone_look_scheme') {
      const mode = SCHEME.indexOf(value);
      if (mode >= 0) owner.setAppearance(mode);
    }
  }
  private swatch(key: string): string {
    const palette = deviceLookPalette(key,this.model.style,this.look.avatarColors);
    return this.environment.dark ? palette.dark.brand : palette.light.brand;
  }
  private styleSurface(style: string): PhoneStyleSurface {
    return deviceLookSurface(style,this.look.accentId,this.environment.dark,this.look.palette(style));
  }
  private styleAccent(style: string): string {
    const palette = this.look.palette(style);
    return this.environment.dark ? palette.dark.brand : palette.light.brand;
  }
  private previewBubble(style: string): string {
    const colors=this.look.palette(style);
    return this.environment.dark ? colors.dark.userBubble : colors.light.userBubble;
  }
  private previewPage(style: string): ResourceColor {
    if (style==='glass') return '#1A'+this.styleAccent(style).slice(1);
    return this.styleSurface(style).page || Colors.sheetBackground;
  }
  private previewRadius(style: string): number {
    if (style === 'clay') return 10.8;
    if (style === 'island') return 11.7;
    return style === 'lark' ? 4.5 : 9;
  }
  private icon(page: string): Resource {
    if (page === 'phone_look_navigation') return $r('sys.symbol.open_sidebar');
    if (page === 'phone_look_layout') return Symbols.spaces;
    if (page === 'phone_look_scheme') return $r('sys.symbol.circle_lefthalf_inset_filled');
    return Symbols.appearance;
  }
  private artwork(page: string): Resource | undefined {
    if (page==='phone_look_color') return $r('app.media.phone_settings_swatch');
    if (page==='phone_look_navigation') return $r('app.media.phone_settings_sidebar');
    if (page==='phone_look_layout') return $r('app.media.phone_settings_list');
    return undefined;
  }
  private colorOrigin(key:string): string {
    if (key==='avatar') return this.look.avatarName || tr('phone_look_avatar');
    if (key==='style') {
      const style=phoneInterfaceStyleID(this.model.style);
      const origin=style==='clay' ? 'lavender' : style==='island' ? 'teal' : style==='lark' ? 'lark' : 'blue';
      return this.label('phone_look_color',origin);
    }
    if (key==='coral') return 'Claude';
    const owner=key==='lavender' ? 'clay' : key==='teal' ? 'island' : key==='lark' ? 'lark' : 'glass';
    return this.label('phone_look_style',owner);
  }
  private pageTint(page: string): ResourceColor {
    if (page === 'phone_look_style') return Colors.ai;
    if (page === 'phone_look_navigation') return Colors.teal;
    if (page === 'phone_look_layout') return Colors.working;
    if (page === 'phone_look_scheme') return '#5856D6';
    return Colors.iconAccent;
  }
  @Builder overview() {
    Column() {
      ForEach(phoneAppearancePages, (page: string) => {
        PhoneSettingsRow({label:tr(page),detail:this.label(page,this.selected(page)),elevated:true,symbol:this.icon(page),
          artwork:this.artwork(page),tint:this.pageTint(page),last:false,identity:'settings.appearance.'+page.slice('phone_look_'.length),
          action:() => { if (this.active && this.route === 'appearance') this.onOpen(page); }}).accessibilityRole(AccessibilityRoleType.BUTTON)
      }, (page: string) => page)
      this.chatSwitch(true)
      this.chatSwitch(false)
    }.width('100%').backgroundColor(Colors.card(true)).borderRadius(PhoneStyle.cardRadius).clip(true)
      .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.cardBorder(this.environment.dark),radius:PhoneStyle.cardRadius})
      .shadow(PhoneStyle.cardElevation(this.environment.dark))
  }
  @Builder chatSwitch(glow: boolean) {
    Column() {
      Row({space:12}) {
        SymbolGlyph(glow ? $r('sys.symbol.wand_and_stars') : $r('sys.symbol.keyboard'))
          .fontSize('22vp').fontColor([glow ? Colors.ai : Colors.iconAccent]).width(32).accessibilityLevel('no')
        Column({space:3}) {
          Text(tr(glow ? 'phone_look_glow' : 'phone_look_return')).fontSize(17).maxFontScale(Tokens.interfaceScaleFor(17)).fontColor(Colors.ink)
          Text(tr(glow ? 'phone_look_glow_note' : 'phone_look_return_note')).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontColor(Colors.secondary)
        }.layoutWeight(1).alignItems(HorizontalAlign.Start)
        Toggle({type:ToggleType.Switch,isOn:glow ? this.look.composerGlow : this.look.returnSends})
          .selectedColor(Colors.positive).enabled(this.active && this.look.ready)
          .accessibilityText(tr(glow ? 'phone_look_glow' : 'phone_look_return')).accessibilityDescription(tr(glow ? 'phone_look_glow_note':'phone_look_return_note'))
          .id(glow ? 'settings.appearance.glow' : 'settings.appearance.return')
          .onChange((on:boolean) => { if (this.active && this.route==='appearance') { Haptics.tap(); this.look.setChatInteraction(glow ? 'ui.composerGlow' : 'ui.returnSends',on); } })
      }.padding({left:14,right:16,top:12,bottom:12}).width('100%').constraintSize({minHeight:PhoneStyle.formSize(glow ? 85.7 : 68,glow ? 132.7 : 106.7,this.environment.fontScale)})
      if (glow) Divider().color(Colors.divider).strokeWidth(0.5).margin({left:60})
    }.width('100%')
  }
  @Builder herdrTabSwitch() {
    Row({ space: 12 }) {
      Column({ space: 3 }) {
        Text(tr('agents_use_herdr_tab_name'))
          .fontSize(17)
          .maxFontScale(Tokens.interfaceScaleFor(17))
          .fontColor(Colors.ink)
        Text(tr('agents_use_herdr_tab_name_hint'))
          .fontSize(13)
          .maxFontScale(Tokens.interfaceScaleFor(13))
          .fontColor(Colors.secondary)
      }.layoutWeight(1).alignItems(HorizontalAlign.Start)
      Toggle({ type: ToggleType.Switch, isOn: this.model.useHerdrTabName })
        .selectedColor(Colors.positive)
        .enabled(!this.model.agentsDisplaySaving && this.model.connected && !this.model.demo)
        .accessibilityText(tr('agents_use_herdr_tab_name'))
        .accessibilityDescription(tr('agents_use_herdr_tab_name_hint'))
        .id('settings.appearance.herdr_tab_name')
        .onChange((value: boolean) => {
          if (this.active && value !== this.model.useHerdrTabName) {
            Haptics.selection();
            this.model.setUseHerdrTabName(value);
          }
        })
    }.width('100%').padding({ left: 16, right: 16, top: 12, bottom: 12 })
      .backgroundColor(Colors.card(true)).borderRadius(PhoneStyle.cardRadius)
      .border({ width: PhoneStyle.surfaceEdgeWidth(0.5), color: PhoneStyle.cardBorder(this.environment.dark), radius: PhoneStyle.cardRadius })
      .shadow(PhoneStyle.cardElevation(this.environment.dark))
      .margin({ top: 16 })
  }
  @Builder nextSuggestionsSwitch() {
    if (this.model.nextSuggestionsValue !== null) {
      Column({ space: Tokens.spaceXs }) {
        Row({ space: Tokens.spaceM }) {
          SymbolGlyph($r('sys.symbol.wand_and_stars'))
            .fontSize('22vp')
            .fontColor([Colors.ai])
            .width(32)
            .accessibilityLevel('no')
          Column({ space: Tokens.spaceXxs }) {
            Text(tr('next_suggestions_title'))
              .fontSize(Tokens.typeBody)
              .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeBody))
              .fontColor(Colors.ink)
            Text(tr('next_suggestions_note'))
              .fontSize(Tokens.typeCaption)
              .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption))
              .fontColor(Colors.secondary)
          }
          .layoutWeight(1)
          .alignItems(HorizontalAlign.Start)
          Toggle({ type: ToggleType.Switch, isOn: this.model.nextSuggestionsValue === true })
            .selectedColor(Colors.positive)
            .enabled(this.active && !this.model.nextSuggestionsSaving && (this.model.connected || this.model.demo))
            .accessibilityText(tr('next_suggestions_title'))
            .accessibilityDescription(tr('next_suggestions_note'))
            .onChange((value: boolean) => {
              if (this.active && this.model.nextSuggestionsValue !== null && value !== this.model.nextSuggestionsValue) {
                Haptics.selection();
                this.model.setNextSuggestions(value).catch(() => undefined);
              }
            })
            .id('settings.appearance.next_suggestions')
        }
        .width('100%')
        .constraintSize({ minHeight: Tokens.controlTouch })
        .padding({ left: 14, right: 16, top: 12, bottom: 12 })
        .backgroundColor(Colors.card(true))
        .borderRadius(PhoneStyle.cardRadius)
        .border({ width: PhoneStyle.surfaceEdgeWidth(0.5), color: PhoneStyle.cardBorder(this.environment.dark), radius: PhoneStyle.cardRadius })
        .shadow(PhoneStyle.cardElevation(this.environment.dark))
      }
      .width('100%')
      .margin({ top: 16 })
    }
  }
  @Builder caption(value: string) {
    Text(tr(value)).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontColor(Colors.secondary)
      .width('100%').padding({left:16,right:16,top:7,bottom:9})
  }
  @Builder sample() {
    Column({space:8}) {
      Row() {
        Blank()
        Text(tr('phone_look_sample_ask')).fontSize(14).maxFontScale(Tokens.interfaceScaleFor(14))
          .fontColor(this.environment.dark ? this.look.palette(this.model.style).dark.userBubbleText : this.look.palette(this.model.style).light.userBubbleText)
          .padding({left:12,right:12,top:8,bottom:8}).borderRadius(16)
          .backgroundColor(this.environment.dark ? this.look.palette(this.model.style).dark.userBubble : this.look.palette(this.model.style).light.userBubble)
          .animation(Motion.state(this.environment.reduceMotion))
      }.width('100%')
      Column({space:4}) {
        Text(tr('phone_look_sample_reply')).fontSize(14).maxFontScale(Tokens.interfaceScaleFor(14)).fontColor(Colors.ink)
        Text(tr('phone_look_sample_link')).fontSize(14).maxFontScale(Tokens.interfaceScaleFor(14)).fontColor(Colors.link)
      }.width('100%').alignItems(HorizontalAlign.Start).padding(12).backgroundColor(Colors.card(true)).borderRadius(14)
        .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.cardBorder(this.environment.dark),radius:14})
      Row({space:8}) {
        Text(tr('phone_look_sample_add')).fontSize(14).maxFontScale(Tokens.interfaceScaleFor(14)).fontWeight(FontWeight.Medium).fontColor(Colors.onAction)
          .padding({left:14,right:14,top:7,bottom:7}).backgroundColor(Colors.action).borderRadius(Tokens.capsule)
        Text(tr('phone_look_sample_skip')).fontSize(14).maxFontScale(Tokens.interfaceScaleFor(14)).fontColor(Colors.ink)
          .padding({left:14,right:14,top:7,bottom:7}).backgroundColor(Colors.control).borderRadius(Tokens.capsule)
      }
    }.alignItems(HorizontalAlign.Start).width('100%').padding(12).backgroundColor(Colors.sheetBackground).borderRadius(16)
      .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.cardBorder(this.environment.dark),radius:16})
      .accessibilityGroup(true).id('settings.appearance.preview')
  }
  @Builder colors() {
    Column({space:16}) {
      this.sample()
      Flex({wrap:FlexWrap.Wrap,alignItems:ItemAlign.Start}) {
        ForEach(this.look.accentOptions(), (key: string) => {
          Column({space:5}) {
            Stack() {
              Circle().width(32).height(32).fill(this.swatch(key))
              if (this.look.accentId === key) SymbolGlyph(Symbols.check).fontSize('18vp').fontColor([Color.White])
            }.width(40).height(40).borderWidth(this.look.accentId===key ? 2 : 0).borderColor(this.swatch(key)).borderRadius(20)
              .scale({x:this.look.accentId===key ? 1.06 : 1,y:this.look.accentId===key ? 1.06 : 1})
              .animation(Motion.state(this.environment.reduceMotion))
            Text(this.label('phone_look_color',key)).maxLines(1).fontSize(12).fontWeight(this.look.accentId===key ? FontWeight.Bold : FontWeight.Medium)
              .maxFontScale(Tokens.interfaceScaleFor(12)).fontColor(Colors.ink).textAlign(TextAlign.Center)
            Text(this.colorOrigin(key)).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).fontColor(Colors.secondary).maxLines(1)
          }.width('25%').padding({left:3,right:3,top:5,bottom:7}).accessibilityGroup(true)
            .accessibilityRole(AccessibilityRoleType.BUTTON).accessibilitySelected(this.look.accentId===key)
            .accessibilityText(this.label('phone_look_color',key)+' · '+this.colorOrigin(key)).enabled(this.active && this.look.ready)
            .onClick(() => { Haptics.selection(); this.select('phone_look_color',key); }).id('settings.appearance.color.'+key)
        },(key:string) => key)
      }.width('100%')
    }.width('100%').padding(16).backgroundColor(Colors.card(true)).borderRadius(PhoneStyle.cardRadius)
      .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.cardBorder(this.environment.dark),radius:PhoneStyle.cardRadius})
      .shadow(PhoneStyle.cardElevation(this.environment.dark))
  }
  @Builder textSizeChoices() {
    Row({space:8}) {
      Text(tr('phone_look_text_size')).fontSize(17).maxFontScale(Tokens.interfaceScaleFor(17)).fontColor(Colors.ink).layoutWeight(1)
      Row({space:2}) {
        ForEach(['small','standard','large'], (key:string) => {
          Button({type:ButtonType.Capsule}) {
            Text(tr('phone_look_text_'+key)).fontSize(14).fontWeight(FontWeight.Medium).maxFontScale(Tokens.interfaceScaleFor(14))
              .fontColor(this.textSize.choice===key ? $r('app.color.phone_filter_segment_ink') : Colors.ink)
          }.layoutWeight(1).height(32).padding({left:3,right:3}).borderRadius(Tokens.capsule)
            .responseRegion({x:0,y:-6,width:'100%',height:44})
            .backgroundColor(this.textSize.choice===key ? $r('app.color.phone_filter_segment_fill') : Color.Transparent)
            .animation(Motion.state(this.environment.reduceMotion))
            .accessibilityText(tr('phone_look_text_size')+' · '+tr('phone_look_text_'+key)).accessibilitySelected(this.textSize.choice===key)
            .enabled(this.active && this.textSize.ready && !this.textSize.saving)
            .onClick(() => { if (this.active && this.route==='phone_look_style') { Haptics.selection(); this.textSize.choose(key); } }).id('settings.appearance.text.'+key)
        },(key:string)=>key)
      }.width(PhoneStyle.formSize(134,160,this.environment.fontScale)).padding(2).backgroundColor(Colors.control).borderRadius(Tokens.capsule)
    }.width('100%').padding({left:20,right:20,top:12,bottom:12}).backgroundColor(Colors.card(true)).borderRadius(PhoneStyle.cardRadius)
      .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.cardBorder(this.environment.dark),radius:PhoneStyle.cardRadius})
      .shadow(PhoneStyle.cardElevation(this.environment.dark))
  }
  @Builder styles() {
    Flex({wrap:FlexWrap.Wrap,alignItems:ItemAlign.Start}) {
      ForEach(this.options('phone_look_style'),(style:string) => {
        Column({space:8}) {
          Column({space:6}) {
            Row().width('100%').height(16).backgroundColor(this.styleSurface(style).card || Colors.card(true))
              .borderRadius(this.previewRadius(style)).shadow({radius:style==='clay' || style==='island' ? 3 : 0,
                offsetY:2,color:this.styleSurface(style).shadow || Color.Transparent})
            Row() {
              Blank()
              Row().width(40).height(12).borderRadius(6).backgroundColor(this.previewBubble(style))
            }.width('100%')
            Row().width(46).height(8).borderRadius(4).backgroundColor(Colors.secondary).opacity(0.25)
            Row({space:4}) {
              Circle().width(10).height(10).fill(this.styleAccent(style))
              Row().layoutWeight(1).height(8).borderRadius(4).backgroundColor(this.styleAccent(style)).opacity(0.35)
            }
          }.width('100%').height(PhoneStyle.formSize(120,157,this.environment.fontScale)).padding(12)
            .alignItems(HorizontalAlign.Start).justifyContent(FlexAlign.Center)
            .backgroundColor(this.previewPage(style)).borderRadius(14)
            .borderWidth(this.selected('phone_look_style')===style ? 2.5 : 1)
            .borderColor(this.selected('phone_look_style')===style ? this.styleAccent(style) : PhoneStyle.cardBorder(this.environment.dark))
            .shadow(this.selected('phone_look_style')===style ? PhoneStyle.cardElevation(this.environment.dark) : {radius:0,color:Color.Transparent})
            .animation(Motion.state(this.environment.reduceMotion))
          Row({space:3}) {
            if (this.selected('phone_look_style')===style) SymbolGlyph(Symbols.check).fontSize('12vp').fontColor([Colors.iconAccent])
            Text(this.label('phone_look_style',style)).fontSize(13).fontWeight(this.selected('phone_look_style')===style ? FontWeight.Bold : FontWeight.Medium).maxFontScale(Tokens.interfaceScaleFor(13)).maxLines(1)
              .fontColor(Colors.ink).textOverflow({overflow:TextOverflow.Ellipsis})
          }
          if (!['brief','board'].includes(style)) Text(tr('phone_look_style_'+style+'_note')).fontSize(12)
            .maxFontScale(Tokens.interfaceScaleFor(12)).maxLines(1).fontColor(Colors.secondary).textOverflow({overflow:TextOverflow.Ellipsis})
        }.width('33.33%').padding({left:5,right:5,top:6,bottom:10}).accessibilityGroup(true).accessibilityRole(AccessibilityRoleType.BUTTON)
          .accessibilityText(this.label('phone_look_style',style)+(['brief','board'].includes(style) ? '' : ' · '+tr('phone_look_style_'+style+'_note'))).accessibilitySelected(this.selected('phone_look_style')===style)
          .enabled(this.active && this.look.ready).onClick(() => { Haptics.selection(); this.select('phone_look_style',style); }).id('settings.appearance.style.'+style)
      }, (style:string) => style)
    }.width('100%')
  }
  @Builder sketchLine(width: number, color: ResourceColor = Colors.control) {
    Row().width(width).height(6).borderRadius(3).backgroundColor(color)
  }
  @Builder sketchSidebar(merged: boolean, wide: boolean) {
    Column({space:4}) {
      this.sketchLine(38)
      this.sketchLine(42)
      this.sketchLine(36)
      if (merged) {
        this.sketchLine(26,Colors.iconAccent)
        this.sketchLine(40)
        this.sketchLine(20,Colors.needsMark)
        Row().width(46).height(14).borderRadius(3).backgroundColor(Colors.control)
        Row().width(40).height(14).borderRadius(3).backgroundColor(Colors.control)
      }
    }.width(wide ? 50 : 56).height('100%').alignItems(HorizontalAlign.Start).padding(4)
      .backgroundColor(Colors.sheetBackground).borderRadius(3)
  }
  @Builder navigationSketch(key: string, wide: boolean) {
    Stack({alignContent:Alignment.Top}) {
      Row({space:4}) {
        if (key==='merged' || wide) this.sketchSidebar(key==='merged',wide)
        Column({space:4}) {
          if (key!=='merged' || wide) {
            this.sketchLine(50)
            this.sketchLine(64)
            this.sketchLine(40)
            Blank()
            if (!wide) Row().width('100%').height(16).borderRadius(8).backgroundColor(Colors.control)
          }
        }.height('100%').layoutWeight(1).alignItems(HorizontalAlign.Start).clip(true)
          .padding({top:key==='separate' && !wide ? 24 : 4})
      }.width('100%').height('100%').padding({top:wide && key==='separate' ? 16 : 4,
        left:wide && key==='separate' ? 0 : 4,right:wide && key==='separate' ? 0 : 4,bottom:4})
      if (key==='separate') Row().width(40).height(8).borderRadius(4).backgroundColor(Colors.iconAccent).margin({top:8})
    }.width(wide ? 150 : 76).height(wide ? 110 : 140).backgroundColor(Colors.card(true))
      .borderWidth(0.5).borderColor(Colors.divider).borderRadius(wide ? 10 : 14).clip(true)
  }
  @Builder navigationSample(key: string) {
    Row({space:16}) { this.navigationSketch(key,false); this.navigationSketch(key,true); }
      .width('100%').padding(12).alignItems(VerticalAlign.Bottom).backgroundColor(Colors.sheetBackground).borderRadius(14).accessibilityLevel('no-hide-descendants')
  }
  private sampleAgent(waiting: boolean): ProjectAgent {
    const title = waiting ? 'phone_look_example_review' : 'phone_look_example_build';
    const words = tr(title + '_note');
    const data: Record<string,Object> = {
      'machine_id':'layout', 'session_id':waiting ? 'layout-1' : 'layout-2', 'title':tr(title), 'cwd':'/',
      'status':waiting ? 'blocked' : 'working', 'state':waiting ? 'needs_you' : 'working',
      'space_name':'weather', 'slot_name':waiting ? 'tests' : 'widget',
      'pane_id':waiting ? 'p-layout-1' : 'p-layout-2', 'last_text':words,
      'attention_reason':waiting ? words : ''
    };
    return new ProjectAgent(ResourceRow.from(data,'session'),data);
  }
  @Builder agentSample(key: string) {
    Column() {
      Column({space:8}) {
        ForEach([this.sampleAgent(true),this.sampleAgent(false)], (agent: ProjectAgent) => {
          AgentDirectoryRowContent({agent:agent,display:key,previewRow:true,activityList:true,
            place:agent.tab,showsSource:true,sourceTitle:tr('terminal'),
            age:agent.state==='needs_you' ? '2'+tr('agent_time_minute_suffix') : tr('agent_time_just_now'),
            fontScale:this.environment.fontScale,controlScale:PhoneStyle.formSize(36,47.3,this.environment.fontScale)/36,
            tint:agent.state==='needs_you' ? Colors.needsMark : Colors.workingMark})
        }, (agent:ProjectAgent) => agent.row.id)
      }.width('100%').padding(key==='cards' ? 0 : 12)
        .backgroundColor(key==='cards' ? Color.Transparent : this.styleSurface(phoneInterfaceStyleID(this.model.style)).card || Colors.sheetBackground).borderRadius(14)
    }.width('100%').padding(8).backgroundColor(Colors.sheetBackground).borderRadius(12).accessibilityLevel('no-hide-descendants')
  }
  private choiceInset(page: string): number {
    if (page==='phone_look_layout') return PhoneStyle.formSize(19,32,this.environment.fontScale);
    return page==='phone_look_navigation' ? 18 : 12;
  }
  @Builder choices(page: string) {
    Column() {
      ForEach(this.options(page), (key: string, index: number) => {
        Column({space:8}) {
          Row({space:8}) {
            if (page==='phone_look_scheme') SymbolGlyph(key==='dark' ? $r('sys.symbol.moon') : key==='light' ? $r('sys.symbol.sun_max') : Symbols.appearance)
              .fontSize('19vp').fontColor([Colors.ink])
            Text(this.label(page,key)).fontSize(17).fontWeight(page==='phone_look_scheme' ? FontWeight.Regular : FontWeight.Medium)
              .maxFontScale(Tokens.interfaceScaleFor(17)).fontColor(Colors.ink).layoutWeight(1)
              .lineHeight(page==='phone_look_layout' ? PhoneStyle.settingsExampleLineHeight(20,27.67,this.environment.fontScale) : undefined)
            if (this.selected(page)===key) SymbolGlyph(Symbols.check).fontSize('17vp').fontColor([Colors.iconAccent]).accessibilityLevel('no')
          }.width('100%')
          if (page==='phone_look_navigation') this.navigationSample(key)
          if (page==='phone_look_layout') this.agentSample(key)
          if ((page==='phone_look_layout' || page==='phone_look_navigation'))
            Text(tr(page+'_'+key+'_note')).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontColor(Colors.secondary)
              .punctuationOverflow(page==='phone_look_layout').width('100%')
              .lineHeight(page==='phone_look_layout' ? PhoneStyle.settingsExampleLineHeight(15.67,23.5,this.environment.fontScale) : undefined)
        }.width('100%').padding({left:20,right:20,top:this.choiceInset(page),bottom:this.choiceInset(page)})
          .constraintSize({minHeight:page==='phone_look_scheme' ? PhoneStyle.formSize(52,59,this.environment.fontScale) :
            page==='phone_look_navigation' ? PhoneStyle.formSize(289.7,340.3,this.environment.fontScale) : 44})
          .accessibilityGroup(true).accessibilityRole(AccessibilityRoleType.BUTTON).accessibilitySelected(this.selected(page)===key)
          .accessibilityText(this.label(page,key)+(page==='phone_look_navigation' || page==='phone_look_layout' ? ' · '+tr(page+'_'+key+'_note') : '')).enabled(this.active && this.look.ready)
          .onClick(() => { Haptics.selection(); this.select(page,key); }).id('settings.appearance.'+page.slice('phone_look_'.length)+'.'+key)
        if (index<this.options(page).length-1) Divider().color(Colors.divider).strokeWidth(0.5).margin({left:16})
      },(key:string) => key)
    }.width('100%').backgroundColor(Colors.card(true)).borderRadius(PhoneStyle.cardRadius).clip(true)
      .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.cardBorder(this.environment.dark),radius:PhoneStyle.cardRadius})
      .shadow(PhoneStyle.cardElevation(this.environment.dark))
  }
  build() {
    ForEach([this.route], (page: string) => {
      Scroll() {
        Column({space:0}) {
          if (page==='appearance') {
            Text(tr('phone_settings_display')).fontSize(34).fontWeight(FontWeight.Bold).maxFontScale(Tokens.interfaceScaleFor(34))
              .width('100%').margin({top:3.7,bottom:7.3}).id('settings.appearance.title')
            Text(tr('phone_look_section')).fontSize(15).maxFontScale(Tokens.interfaceScaleFor(15)).fontColor(Colors.secondary)
              .width('100%').height(PhoneStyle.formSize(40.3,47.5,this.environment.fontScale)).padding({left:20}).id('settings.appearance.section')
            this.overview()
            this.nextSuggestionsSwitch()
            if (this.model.agentsDisplaySupported) this.herdrTabSwitch()
          }
          else if (page==='phone_look_color') { this.colors(); this.caption('phone_look_color_footer'); }
          else if (page==='phone_look_style') { this.styles(); this.caption('phone_look_style_footer'); this.textSizeChoices(); this.caption('phone_look_text_footer'); }
          else {
            if (page==='phone_look_layout') Text(tr('phone_look_layout_header')).fontSize(15).maxFontScale(Tokens.interfaceScaleFor(15))
              .fontColor(Colors.secondary).width('100%').height(PhoneStyle.formSize(40.3,47.7,this.environment.fontScale)).padding({left:20})
            this.choices(page)
            if (page==='phone_look_layout' && this.model.agentsDisplaySupported) this.herdrTabSwitch()
            if (page==='phone_look_scheme' || page==='phone_look_navigation') this.caption(page+'_footer')
          }
        }.width('100%').alignItems(HorizontalAlign.Start).padding({left:16,right:16,top:page==='appearance' || page==='phone_look_layout' ? 0 : 35,bottom:28})
      }.width('100%').height('100%').scrollBar(BarState.Off).align(Alignment.TopStart)
        .backgroundColor(Colors.sheetBackground).id('settings.appearance.page.'+page)
    },(page:string)=>page)
  }
}
