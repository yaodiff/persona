import { i18n } from '@kit.LocalizationKit';
import { Colors } from '../design/Colors';
import { PhoneStyle } from '../design/PhoneStyle';
import { Motion } from '../design/Motion';
import { Tokens } from '../design/Tokens';
import { tr } from '../design/Strings';
import { Haptics } from '../platform/Haptics';
import { nativeEnvironment } from '../platform/NativeEnvironment';
import { workspaceGridColumns, workspaceGridGutter } from '../core/WorkspaceGrid';
import { ClientModel } from '../models/ClientModel';
import { deviceLook } from '../models/DeviceLook';
import { deviceLookIconAccent } from '../core/DeviceLook';
import { MachineSubscriptionUsage,SubscriptionAccount,SubscriptionProvider,SubscriptionWindow,
  subscriptionGroups,usageProviderTitle,usageTicks,usageTimelinePosition } from '../models/MachineSubscriptionUsage';

function words(key:string,first:string='',second:string=''):string {
  return tr('sub_usage_'+key).replace('{1}',first).replace('{2}',second);
}
function sampleAge(at:number,now:number):string {
  if (!(at>946684800000)) return '';
  const seconds=Math.max(0,Math.round((now-at)/1000));
  if (seconds<60) return words('seconds_ago',String(seconds));
  if (seconds<3600) return words('minutes_ago',String(Math.floor(seconds/60)));
  if (seconds<86400) return words('hours_ago',String(Math.floor(seconds/3600)));
  return words('days_ago',String(Math.floor(seconds/86400)));
}
function duration(reset:number,now:number):string {
  const minutes=Math.ceil(Math.max(0,reset-now)/60000);
  if (minutes<60) return words('mins',String(minutes));
  if (minutes<1440) return words('hours',String(Math.round(minutes/6)/10));
  const days=String(Math.floor(minutes/1440)),hours=Math.floor(minutes/60)%24;
  return hours ? words('days_hours',days,String(hours)) : words('days',days);
}
function resetLabel(at:number,now:number,locale:string):string {
  if (!Number.isFinite(at)) return '';
  const diff=at-now,minutes=Math.ceil(diff/60000);if(diff<=0)return words('soon');
  if (minutes<60) return words('reset_mins',String(minutes));
  const d=new Date(at),today=new Date(now);
  if (d.toDateString()===today.toDateString()) {
    const clock=/^zh(?:[-_](?:Hans|CN|SG)(?:[-_]|$)|$)/i.test(locale) ? String(d.getHours()).padStart(2,'0')+':'+String(d.getMinutes()).padStart(2,'0') : d.toLocaleTimeString(locale || undefined,{hour:'numeric',minute:'2-digit'});
    return words('reset_clock',clock);
  }
  const hours=Math.floor(minutes/60),days=Math.floor(hours/24);
  return days ? words('reset_days',String(days),String(hours%24)) : words('reset_hours',String(hours));
}

@ComponentV2
struct SubscriptionMeter {
  @Require @Param window:SubscriptionWindow;
  @Require @Param expected:number;
  @Require @Param accent:ResourceColor;
  private thickness():number {
    if (Number.isFinite(this.window.protection)) return 12;
    return Number.isFinite(this.expected) ? 8 : 4;
  }
  @Local private span:number=0;
  private at(percent:number):number {return Math.max(0,Math.min(100,percent))/100*this.span;}
  private stripes():number[] {
    const width=this.span-this.at(this.window.protection);
    return width>0 ? Array.from({length:Math.ceil((width+this.thickness())/4)},(_:Object,index:number)=>index*4-this.thickness()) : [];
  }
  build(){
    Column() {
      if (Number.isFinite(this.window.protection)) Stack({alignContent:Alignment.BottomStart}) {
        Text(words('protect',String(Math.round(this.window.protection)))).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).fontColor(Colors.working)
          .width(72).textAlign(TextAlign.Center).maxLines(1).position({x:Math.min(Math.max(48,this.at(this.window.protection)),this.span-24)-36,y:0})
      }.width('100%').height(14)
      Stack({alignContent:Alignment.Start}) {
        Row().width('100%').height(this.thickness()).borderRadius(this.thickness()/2).backgroundColor(Colors.control)
        Row().width(this.at(this.window.percent)).height(this.thickness()).borderRadius(this.thickness()/2).backgroundColor(this.window.percent>=80 ? Colors.workingMark : this.accent)
          .animation(Motion.state(nativeEnvironment.reduceMotion))
        if (Number.isFinite(this.window.protection)) Stack({alignContent:Alignment.TopStart}) {
          ForEach(this.stripes(),(x:number)=>{
            Line().width(this.thickness()).height(this.thickness()).startPoint([0,this.thickness()]).endPoint([this.thickness(),0]).stroke(Colors.workingMark).strokeWidth(0.7).opacity(0.65)
              .position({x:x,y:0})
          },(x:number)=>String(x))
        }.width(Math.max(0,this.span-this.at(this.window.protection))).height(this.thickness()).clip(true).position({x:this.at(this.window.protection),y:0})
        if (Number.isFinite(this.expected)) Row().width(2).height(8).backgroundColor(Colors.secondary).position({x:this.at(this.expected)-1,y:0})
        if (Number.isFinite(this.window.protection)) Row().width(2).height(12).backgroundColor(Colors.workingMark).position({x:this.at(this.window.protection)-1,y:0})
      }.height(this.thickness()).width('100%').clip(true)
    }.width('100%').onAreaChange((_:Area,a:Area)=>{this.span=Number(a.width);}).accessibilityLevel('no')
  }
}

@ComponentV2
struct SubscriptionTimeline {
  @Require @Param accounts:SubscriptionAccount[];
  @Require @Param now:number;
  @Require @Param locale:string;
  @Local private plotWidth:number=0;
  private weekColor(): ResourceColor { return Colors.chartBlue; }
  private x(reset:number):number { return this.plotWidth*usageTimelinePosition((reset-this.now)/3600000); }
  private caption(h:number):string {return h===0 ? words('now') : h<24 ? words('axis_hours',String(h)) : words('axis_days',String(h/24));}
  @Builder lane(reset:number,color:ResourceColor,height:number) {
    Stack({alignContent:Alignment.Start}) {
      if (Number.isFinite(reset)) {
        Row().width(Math.max(11,this.x(reset))).height(height).borderRadius(height/2)
          .linearGradient({angle:90,colors:[[Color.Transparent,0],[color,1]]})
        Circle().width(11).height(11).fill(Colors.control).stroke(color).strokeWidth(2).position({x:this.x(reset)-11,y:2})
        Text(duration(reset,this.now)).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).fontWeight(600)
          .fontColor(this.x(reset)>this.plotWidth*0.8 ? Color.White : color).maxLines(1)
          .width(Math.max(0,this.x(reset)>this.plotWidth*0.8 ? this.x(reset)-11 : this.plotWidth-this.x(reset)-4))
          .textAlign(this.x(reset)>this.plotWidth*0.8 ? TextAlign.End : TextAlign.Start)
          .position({x:this.x(reset)>this.plotWidth*0.8 ? 0 : Math.max(11,this.x(reset))+4,y:0})
      }
    }.height(15).width('100%')
  }
  @Builder legend(title:string,color:ResourceColor) {
    Row({space:4}) { Row().width(16).height(6).borderRadius(Tokens.radiusCapsule).backgroundColor(color); Text(title).fontSize(Tokens.typeLabel).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeLabel)).fontColor(Colors.secondary).maxLines(1) }
  }
  build(){
    Column({space:4}) {
      Text(words('timeline')).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).fontColor(Colors.secondary)
      Row({space:8}) {
        Blank().width(64)
        Stack({alignContent:Alignment.TopStart}) {
          ForEach(usageTicks.filter((h:number)=>h!==6),(h:number)=>{
            Text(this.caption(h)).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).fontColor(h===0 ? Colors.ink : Colors.secondary).maxLines(1)
              .position({x:Math.max(0,Math.min(this.plotWidth-24,this.plotWidth*usageTimelinePosition(h)-9)),y:0})
          },(h:number)=>String(h))
        }.height(16).layoutWeight(1)
          .onAreaChange((_:Area,a:Area)=>{this.plotWidth=Number(a.width);})
      }.width('100%').accessibilityLevel('no')
      ForEach(this.accounts,(a:SubscriptionAccount,index:number)=>{
        Row({space:8}) {
          Column() {
            Text(a.name).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).fontColor(Colors.secondary).maxLines(1).textOverflow({overflow:TextOverflow.Ellipsis})
            if (a.plan) Text(a.plan).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).fontColor(Colors.tertiary).maxLines(1)
          }.width(64).alignItems(HorizontalAlign.Start)
          Stack({alignContent:Alignment.Start}) {
            ForEach(usageTicks,(h:number)=>{Row().width(h===0 ? 1.5 : 0.5).height('100%').backgroundColor(h===0 ? Colors.ink : Colors.divider)
              .position({x:Math.min(Math.max(0,this.plotWidth-1),this.plotWidth*usageTimelinePosition(h)),y:0})},(h:number)=>String(h))
            Column() {this.lane(a.planReset(300,this.now),Colors.teal,6);this.lane(a.planReset(10080,this.now),this.weekColor(),9)}.width('100%')
          }.layoutWeight(1).height(30)
        }.width('100%').height(30).backgroundColor(index%2===0 ? Colors.subtle : Color.Transparent).borderRadius(Tokens.radiusBadge)
          .accessibilityGroup(true).accessibilityText([a.name,Number.isFinite(a.planReset(300,this.now)) ? '5h '+resetLabel(a.planReset(300,this.now),this.now,this.locale) : '',
            Number.isFinite(a.planReset(10080,this.now)) ? '7d '+resetLabel(a.planReset(10080,this.now),this.now,this.locale) : ''].filter((s:string)=>!!s).join(', '))
      },(a:SubscriptionAccount)=>a.id)
      Row({space:12}) {this.legend(words('legend_short'),Colors.teal);this.legend(words('legend_week'),this.weekColor())}.accessibilityLevel('no')
    }.width('100%').alignItems(HorizontalAlign.Start).id('machines.usage.timeline')
  }
}

@ComponentV2
export struct MachineSubscriptionCards {
  @Require @Param source:MachineSubscriptionUsage;
  @Require @Param model:ClientModel;
  @Param active:boolean=true;
  @Local private now:number=Date.now();
  @Local private gridWidth:number=0;
  private timer:number=-1;
  aboutToAppear():void {this.ticking();}
  aboutToDisappear():void {clearInterval(this.timer);}
  @Monitor('active') ticking():void {clearInterval(this.timer);if(this.active){this.now=Date.now();this.timer=setInterval(()=>{if(this.model.isAppInForeground())this.now=Date.now();},1000);}}
  @Computed get groups():SubscriptionProvider[] {return subscriptionGroups(this.source.accounts,this.now);}
  @Computed get visibleGroups():SubscriptionProvider[] {return this.groups.filter((g:SubscriptionProvider)=>!this.source.hidden.includes(g.provider));}
  private cardColumns():number {return workspaceGridColumns(this.gridWidth,nativeEnvironment.fontScale,3);}
  private accent():ResourceColor {
    const palette=deviceLook.palette(this.model.style),dark=nativeEnvironment.dark;
    return deviceLookIconAccent(deviceLook.accentId,this.model.style,dark,dark ? palette.dark : palette.light);
  }
  private locale():string {return !this.model.language || this.model.language==='default' ? i18n.System.getSystemLanguage() : this.model.language;}
  private state(w:SubscriptionWindow,a:SubscriptionAccount):string {
    const pace=w.pace(this.now,a.outdated(this.now));return !Number.isFinite(pace) ? '' : w.percent>pace+5 ? 'over' : w.percent<pace-5 ? 'under' : '';
  }
  private sources(a:SubscriptionAccount,separator:string=' · '):string {return (a.machines.length ? a.machines : a.local ? [words('source_local')] : []).concat(a.sources).join(separator);}
  private note(a:SubscriptionAccount):string {return [a.plan,this.sources(a)].filter((s:string)=>!!s).join(' · ');}
  private age(a:SubscriptionAccount):string {const age=sampleAge(a.sampled,this.now);return a.outdated(this.now) ? age ? words('outdated_age',age) : words('outdated') : age;}
  @Builder small(value:string,tint:ResourceColor,strong:boolean=false) {
    Text(value).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).fontWeight(strong ? FontWeight.Medium : FontWeight.Normal).fontColor(tint).maxLines(1).textOverflow({overflow:TextOverflow.Ellipsis})
  }
  @Builder filter() {
    Menu() {
      ForEach(this.groups,(g:SubscriptionProvider)=>{
        MenuItem({content:usageProviderTitle(g.provider,words('other'))}).selected(!this.source.hidden.includes(g.provider)).selectIcon(true).enabled(this.source.filterReady)
          .onClick(()=>{ Haptics.selection(); this.source.setShown(this.model,g.provider,this.source.hidden.includes(g.provider)); }).id('machines.usage.filter.'+g.provider)
      },(g:SubscriptionProvider)=>g.provider)
    }
  }
  @Builder windowRow(a:SubscriptionAccount,w:SubscriptionWindow) {
    Column({space:4}) {
      if (w.model) this.small(w.model,Colors.secondary)
      Row({space:8}) {
        Text(w.label).width(Tokens.interfaceFontVp(28,nativeEnvironment.fontScale)).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).fontColor(Colors.secondary).maxLines(1)
        this.small(String(Math.round(w.percent))+'%',w.percent>=80 ? Colors.working : Colors.ink,true)
        if (this.state(w,a)) this.small(words(this.state(w,a)),this.state(w,a)==='over' ? Colors.working : Colors.positive)
        Blank().layoutWeight(1)
        this.small(resetLabel(w.reset,this.now,this.locale()),Colors.tertiary)
      }.width('100%')
      SubscriptionMeter({window:w,expected:w.pace(this.now,a.outdated(this.now)),accent:this.accent()})
    }.alignItems(HorizontalAlign.Start).width('100%').accessibilityGroup(true)
      .accessibilityText([a.name,w.label,w.model,words('used',String(Math.round(w.percent))+'%'),Number.isFinite(w.protection) ? words('protect',String(Math.round(w.protection))) : '',
        this.state(w,a) ? words(this.state(w,a)) : '',resetLabel(w.reset,this.now,this.locale())].filter((v:string)=>!!v).join(', '))
  }
  @Builder account(a:SubscriptionAccount) {
    Column({space:8}) {
      Column({space:2}) {
        Row({space:8}) {Text(a.name).fontSize(15).maxFontScale(Tokens.interfaceScaleFor(15)).fontColor(Colors.ink).fontWeight(600).maxLines(1).layoutWeight(1);this.small(this.age(a),Colors.secondary)}.width('100%')
        if (this.note(a)) Text(this.note(a)).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).fontColor(Colors.secondary).maxLines(1)
          .accessibilityText(!this.sources(a) ? this.note(a) : a.plan ? words('source_plan',a.plan,this.sources(a,'、')) : words('source_named',this.sources(a,'、')))
        if (['error','failed','unreachable','rejected'].includes(a.problem)) Text(words(a.problem)).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).fontColor(Colors.working)
      }.alignItems(HorizontalAlign.Start).width('100%')
      ForEach(a.liveWindows(this.now),(w:SubscriptionWindow)=>{this.windowRow(a,w)},(w:SubscriptionWindow,index:number)=>JSON.stringify([w.label,w.minutes,w.model,index]))
    }.alignItems(HorizontalAlign.Start).width('100%').opacity(a.outdated(this.now) ? 0.6 : 1)
  }
  @Builder card(g:SubscriptionProvider) {
    Column({space:12}) {
      Row({space:8}) {
        Text(usageProviderTitle(g.provider,words('other'))).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontWeight(600).fontColor(Colors.ink).maxLines(1)
        this.small(words('account_count',String(g.accounts.length)),Colors.secondary);Blank().layoutWeight(1);this.small(sampleAge(g.sampled,this.now),Colors.tertiary)
      }.width('100%').accessibilityGroup(true)
      ForEach(g.accounts,(a:SubscriptionAccount,index:number)=>{
        if (index>0) Divider().color(Colors.divider).strokeWidth(0.5)
        this.account(a)
      },(a:SubscriptionAccount)=>a.id)
      if (g.accounts.some((a:SubscriptionAccount)=>Number.isFinite(a.planReset(300,this.now)) || Number.isFinite(a.planReset(10080,this.now)))) {
        Divider().color(Colors.divider).strokeWidth(0.5)
        SubscriptionTimeline({accounts:g.accounts.filter((a:SubscriptionAccount)=>Number.isFinite(a.planReset(300,this.now)) || Number.isFinite(a.planReset(10080,this.now))),now:this.now,locale:this.locale()})
      }
    }.alignItems(HorizontalAlign.Start).width('100%').padding(14).backgroundColor(Colors.surface).borderRadius(PhoneStyle.cardRadius)
      .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.cardBorder(nativeEnvironment.dark),radius:PhoneStyle.cardRadius})
      .shadow(PhoneStyle.cardElevation(nativeEnvironment.dark)).id('usage.card.'+g.provider)
  }
  @Builder filterIcon() {
    Stack() {
      Circle().width('100%').height('100%').fill(this.source.hidden.length ? this.accent() : Color.Transparent).stroke(this.accent()).strokeWidth(1.6)
      Column({space:1.7}) {
        ForEach([10,7,3.5],(width:number)=>{
          Row().width(width).height(1.4).borderRadius(Tokens.radiusCapsule).backgroundColor(this.source.hidden.length ? Colors.onAction : this.accent())
        },(width:number)=>String(width))
      }
    }.width(Tokens.interfaceFontVp(20,nativeEnvironment.fontScale)).height(Tokens.interfaceFontVp(20,nativeEnvironment.fontScale)).accessibilityLevel('no')
  }
  build(){
    Column({space:10}) {
      Row() {
        Text(words('heading')).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontWeight(600).fontColor(Colors.secondary).maxLines(1).layoutWeight(1)
        Button({type:ButtonType.Circle}) {this.filterIcon()}
          .width(Tokens.interfaceFontVp(20,nativeEnvironment.fontScale)).height(Tokens.interfaceFontVp(20,nativeEnvironment.fontScale)).padding(0)
          .responseRegion({x:-12,y:-12,width:44,height:44}).backgroundColor(Color.Transparent).accessibilityText(words('filter'))
          .onClick(() => Haptics.tap()).bindMenu(this.filter).id('machines.usage.filter')
      }.width('100%').padding({left:4,right:4})
      if (this.cardColumns() > 1 && this.visibleGroups.length > 1) {
        GridRow({columns:this.cardColumns(),gutter:{x:workspaceGridGutter(this.cardColumns()),y:workspaceGridGutter(this.cardColumns())}}) {
          ForEach(this.visibleGroups,(g:SubscriptionProvider)=>{
            GridCol({span:1}) { this.card(g) }
          },(g:SubscriptionProvider)=>g.cardKey())
        }.width('100%')
      } else {
        ForEach(this.visibleGroups,(g:SubscriptionProvider)=>{this.card(g)},(g:SubscriptionProvider)=>g.cardKey())
      }
    }.width('100%').alignItems(HorizontalAlign.Start).padding({top:8})
      .onAreaChange((_:Area,area:Area)=>{ const w=Number(area.width); if (Number.isFinite(w) && w>0 && Math.abs(w-this.gridWidth)>0.5) this.gridWidth=w; })
      .id('machines.usage')
  }
}
