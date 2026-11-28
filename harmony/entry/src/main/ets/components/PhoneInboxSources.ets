import { inboxSourcesLink, subscriptionsPageId, subscriptionsPageUI } from '../core/WireCompatibility';
import { ClientModel } from '../models/ClientModel';
import { Wire, ResourceRow } from '../models/Wire';
import { ExtensionUINavigationEffect } from '../models/ExtensionUI';
import { InboxSourcesGraph, InboxSourceGroup, InboxSourcesNavigation, InboxSourcesFrame } from '../models/InboxSources';
import { ExtensionPage } from './ExtensionPage';
import { Colors } from '../design/Colors';
import { PhoneStyle } from '../design/PhoneStyle';
import { Symbols } from '../design/Symbols';
import { Tokens } from '../design/Tokens';
import { tr } from '../design/Strings';

@ComponentV2
export struct PhoneInboxSources {
  @Require @Param model:ClientModel;
  @Require @Param navigation:InboxSourcesNavigation;
  @Event onAsk:(text:string)=>void=(_text:string):void=>{};
  @Event onWeb:(address:string)=>void=(_address:string):void=>{};
  @Local private graph:InboxSourcesGraph=new InboxSourcesGraph();
  @Local private showingDetails:boolean=false;
  private owner:string='';
  private removeListener:(()=>void)|undefined=undefined;
  private refreshTimer:number=-1;

  aboutToAppear():void {
    this.resetOwner();
    this.removeListener=this.model.observeEvents((raw:string):void=>{
      if (!this.navigation.current()?.graph || !raw.includes('"subscription"')) return;
      let frame:Record<string,Object>;
      try { frame=Wire.parse(raw); } catch (_) { return; }
      if (Wire.text(frame,'type')!=='subscription' || (Wire.has(frame,'manager') && Wire.text(frame,'manager')!==this.model.profile.manager)) return;
      if (this.refreshTimer!==-1) clearTimeout(this.refreshTimer);
      this.refreshTimer=setTimeout(()=>{this.refreshTimer=-1; if (this.navigation.current()?.graph) this.graph.load(this.model);},800);
    });
  }
  aboutToDisappear():void {
    this.removeListener?.(); this.removeListener=undefined;
    if (this.refreshTimer!==-1) clearTimeout(this.refreshTimer);
    this.graph.clear(); this.navigation.reset();
  }
  @Monitor('model.connected','model.profile','model.features') resetOwner():void {
    if (this.owner===this.model.scope() && this.model.connected && this.model.features.includes('inbox_sources')) return;
    this.owner=this.model.scope(); this.showingDetails=false; this.graph.clear(); this.navigation.reset();
  }
  @Computed get pageContext():Record<string,Object> {
    const manager=this.model.listedManagers().find((row:ResourceRow)=>row.id===this.model.profile.manager);
    return Wire.payload(['assistant','graph'],[manager?.title || '',this.model.features.includes('inbox_sources_graph')]);
  }
  private current(frame:InboxSourcesFrame):boolean {
    return this.model.connected && this.owner===this.model.scope() && this.navigation.current()?.serial===frame.serial && this.model.features.includes('inbox_sources');
  }
  private openAddress(frame:InboxSourcesFrame,address:string):void {
    if (!this.current(frame)) return;
    const link=inboxSourcesLink(address);
    if (link?.kind==='graph' && this.model.features.includes('inbox_sources_graph')) {
      this.navigation.push('',tr('phone_sources_graph'),true); this.graph.load(this.model);
    } else if (link?.kind==='ask') this.onAsk(link.text);
    else if (address.startsWith('https://')) this.onWeb(address);
  }
  private navigate(frame:InboxSourcesFrame,effect:ExtensionUINavigationEffect):void {
    if (this.current(frame) && effect.kind==='push' && effect.extensionId===subscriptionsPageId && effect.uiId===subscriptionsPageUI) {
      this.navigation.push(effect.state,effect.title);
    }
  }
  private read(source:Record<string,Object>):Record<string,Object> { return this.graph.item('reads',Wire.text(source,'read')); }
  private extensionFor(source:Record<string,Object>):Record<string,Object> { return this.graph.item('extensions',Wire.text(this.read(source),'extension')); }
  private destinations(source:Record<string,Object>):string {
    return this.graph.destinations(source).map((id:string)=>Wire.text(this.graph.item('destinations',id),'name',id)).join('、');
  }
  private filterTitle(key:string):string { return tr('phone_sources_'+key); }
  private readName(source:Record<string,Object>):string {
    const read=this.read(source);
    return Wire.bool(read,'direct') ? tr('phone_sources_direct') : Wire.text(read,'title');
  }
  private extensionIdAllowed(id: string): boolean {
    const normalized = id.trim().toLowerCase();
    if (!this.model.mailModuleAllowed() && (normalized === 'mail' || normalized.startsWith('mail:'))) return false;
    // `module.lark` gates only the Lark bot (feishu, feishu_bot, feishu-bot, lark_bot, larksync);
    // the Lark extension stays visible (#16073).
    if (!this.model.larkModuleAllowed() && ['feishu', 'feishu_bot', 'feishu-bot', 'lark_bot', 'larksync'].includes(normalized)) return false;
    return true;
  }
  private visibleGraphGroups(): InboxSourceGroup[] {
    return this.graph.groups().filter((group: InboxSourceGroup): boolean =>
      this.extensionIdAllowed(Wire.text(group.entry, 'id')));
  }
  private sourceEntryAllowed(source: Record<string, Object>): boolean {
    const extId = Wire.text(this.extensionFor(source), 'id') || Wire.text(source, 'read');
    return this.extensionIdAllowed(extId);
  }
  private visibleSourceCount(filter: string): number {
    return this.graph.sources().filter((row: Record<string, Object>): boolean =>
      this.sourceEntryAllowed(row) && this.graph.matches(row, filter)).length;
  }
  @Builder sourceRow(source:Record<string,Object>) {
    Button({type:ButtonType.Normal}) {
      Row({space:8}) {
        Text(this.readName(source)).fontSize(13).maxFontScale(Tokens.interfaceFontMaxScale).fontColor(Colors.secondary)
        Text(Wire.text(source,'name')).fontSize(17).maxFontScale(Tokens.interfaceFontMaxScale).fontWeight(FontWeight.Medium).fontColor(Colors.ink).layoutWeight(1)
        if (Wire.text(Wire.object(source,'badge'),'text')) Text(Wire.text(Wire.object(source,'badge'),'text')).fontSize(13).maxFontScale(Tokens.interfaceFontMaxScale).fontColor(Colors.secondary)
        ForEach(['inbox','kb'],(key:string)=>{
          SymbolGlyph(key==='inbox' ? Symbols.inbox : Symbols.folder).fontSize('15vp')
            .fontColor([Wire.bool(source,'reading') ? Colors.link : Colors.secondary]).opacity(this.graph.destinations(source).includes(key) ? 1 : 0).accessibilityLevel('no')
        },(key:string)=>key)
      }.width('100%')
    }.width('100%').constraintSize({minHeight:PhoneStyle.bar}).padding({left:16,right:12,top:6,bottom:6}).backgroundColor(Color.Transparent)
      .accessibilityText([Wire.text(source,'name'),this.readName(source),this.destinations(source)].filter((v:string)=>!!v).join('，'))
      .onClick(()=>{ this.graph.selected=Wire.text(source,'id'); this.showingDetails=true; }).id('graph.source.'+Wire.text(source,'id'))
  }
  @Builder graphGroup(group:InboxSourceGroup) {
    Column({space:4}) {
      Text(Wire.text(group.entry,'name')+'  '+String(group.sources.length)).fontSize(17).maxFontScale(Tokens.interfaceFontMaxScale).fontWeight(FontWeight.Bold).fontColor(Colors.ink)
        .width('100%').padding({left:12,top:12,right:12})
      ForEach(group.sources,(source:Record<string,Object>)=>{this.sourceRow(source)},(source:Record<string,Object>)=>Wire.text(source,'id'))
    }.width('100%').alignItems(HorizontalAlign.Start).backgroundColor(Colors.surface).borderRadius(PhoneStyle.cardRadius)
  }
  @Builder graphContent() {
    if (!Object.keys(this.graph.data).length && this.graph.loading) LoadingProgress().width(32).height(32)
    else if (this.graph.error) Button(tr('retry')).accessibilityDescription(tr(this.graph.error)).onClick(()=>this.graph.load(this.model)).id('graph.retry')
    else if (!this.visibleSourceCount('all')) Column({space:8}) {
      Text(tr('phone_sources_empty')).fontSize(20).fontWeight(FontWeight.Bold).fontColor(Colors.ink)
      Text(tr('phone_sources_empty_hint')).fontSize(15).fontColor(Colors.secondary).textAlign(TextAlign.Center)
    }.width('100%').padding(20).id('graph.empty')
    else Scroll() {
      Column({space:12}) {
        Text(tr('phone_sources_graph_hint')).fontSize(15).maxFontScale(Tokens.interfaceFontMaxScale).fontColor(Colors.secondary)
        Scroll() {
          Row({space:8}) {
            ForEach(['all','inbox','kb','idle'],(key:string)=>{
              Button(this.filterTitle(key)+' '+String(this.visibleSourceCount(key)),{type:ButtonType.Capsule})
                .fontSize(14).fontColor(this.graph.filter===key ? Colors.onAction : Colors.ink)
                .backgroundColor(this.graph.filter===key ? Colors.action : Colors.surface).accessibilitySelected(this.graph.filter===key)
                .onClick(()=>{this.graph.filter=key;}).id('graph.filter.'+key)
            },(key:string)=>key)
          }
        }.scrollable(ScrollDirection.Horizontal).scrollBar(BarState.Off).width('100%')
        ForEach(this.visibleGraphGroups(),(group:InboxSourceGroup)=>{this.graphGroup(group)},(group:InboxSourceGroup)=>Wire.text(group.entry,'id'))
      }.width('100%').alignItems(HorizontalAlign.Start).padding(PhoneStyle.page)
    }.width('100%').height('100%')
  }
  @Computed get picked():Record<string,Object> { return this.graph.item('sources',this.graph.selected); }
  private duplicateName():string {
    for (const id of Wire.strings(this.picked,'duplicates')) {
      const name=Wire.text(this.graph.item('sources',id),'name');
      if (name) return name;
    }
    return '';
  }
  @Builder details() {
    Scroll() {
      Column({space:12}) {
        Text(Wire.text(this.picked,'name')).fontSize(22).fontWeight(FontWeight.Bold).fontColor(Colors.ink)
        Text([Wire.text(this.extensionFor(this.picked),'name'),this.readName(this.picked),this.destinations(this.picked)].filter((v:string)=>!!v).join(' › '))
          .fontSize(17).fontColor(Colors.ink)
        if (Wire.text(this.picked,'line')) Text(Wire.text(this.picked,'line')).fontSize(15).fontColor(Colors.secondary)
        if (this.duplicateName()) Text(tr('phone_sources_duplicate').replace('{name}',this.duplicateName())).fontSize(15).fontColor(Colors.ink)
        if (inboxSourcesLink(Wire.text(this.picked,'ask'))?.kind==='ask' && this.sourceEntryAllowed(this.picked)) Button(tr('phone_sources_ask')).width('100%')
          .onClick(()=>{const frame=this.navigation.current();const ask=Wire.text(this.picked,'ask');this.showingDetails=false;if(frame)this.openAddress(frame,ask);}).id('graph.ask')
        Button(tr('phone_sources_open')).width('100%').onClick(()=>{
          const state=Wire.text(this.picked,'state'),frame=this.navigation.current();this.showingDetails=false;
          if (frame && this.current(frame)) this.navigation.push(state,Wire.text(this.picked,'name'));
        }).id('graph.open')
      }.width('100%').alignItems(HorizontalAlign.Start).padding(20)
    }.width('100%')
  }
  build() {
    Column() {
      if (!this.model.connected || !this.model.features.includes('inbox_sources')) Text(tr('extension_ui_offline')).fontColor(Colors.secondary)
      else ForEach(this.navigation.frames.slice(-1),(frame:InboxSourcesFrame)=>{
        if (frame.graph) this.graphContent()
        else ExtensionPage({model:this.model,extensionId:subscriptionsPageId,uiId:subscriptionsPageUI,state:frame.state,context:this.pageContext,
          compact:true,inboxSources:true,onTitle:(title:string)=>{if(this.current(frame) && title!==subscriptionsPageId && title!==subscriptionsPageUI)this.navigation.setTitle(frame.serial,title);},
          onNavigate:(effect:ExtensionUINavigationEffect)=>this.navigate(frame,effect),onBack:()=>{if(this.current(frame))this.navigation.back();},
          onLink:(address:string)=>this.openAddress(frame,address)})
      },(frame:InboxSourcesFrame)=>String(frame.serial))
    }.width('100%').height('100%').justifyContent(FlexAlign.Center).backgroundColor(Colors.grouped)
      .bindSheet($$this.showingDetails,this.details(),{height:SheetSize.MEDIUM,detents:[SheetSize.MEDIUM,SheetSize.LARGE],showClose:true})
  }
}
