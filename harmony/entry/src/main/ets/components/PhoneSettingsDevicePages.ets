import { bundleManager, common } from '@kit.AbilityKit';
import { storageStatistics } from '@kit.CoreFileKit';
import { Colors } from '../design/Colors';
import { PhoneStyle } from '../design/PhoneStyle';
import { Tokens } from '../design/Tokens';
import { tr } from '../design/Strings';
import { ClientModel } from '../models/ClientModel';
import { HubProfile } from '../models/Wire';
import { workoutAccess, WorkoutAccessResult } from '../platform/PhoneWorkoutAccess';
import { screenScrollPreference, ScreenScrollPreference } from '../platform/ScreenScrollPreference';
import { screenScrollChoices } from '../core/ScreenWheel';
import { LicenseNotices } from './LicenseNotices';

@ComponentV2
export struct PhoneSettingsDevicePages {
  @Require @Param model:ClientModel;
  @Require @Param page:string;
  @Param elevated:boolean=false;
  @Event onOpen:(page:string)=>void=()=>{};
  @Event onPair:()=>void=()=>{};
  @Local version:string='';
  @Local buildNumber:string='';
  @Local cacheBytes:number=-1;
  @Local dataBytes:number=-1;
  @Local storageError:boolean=false;
  @Local licenses:boolean=false;
  @Local screenScroll:ScreenScrollPreference=screenScrollPreference;
  private active:boolean=false;
  private healthRevision:number=0;
  @Local workoutState:string='';
  @Local workoutCode:number=0;
  aboutToAppear():void { this.active=true;this.load().catch(() => {}); }
  aboutToDisappear():void {this.active=false;this.healthRevision++;}
  @Monitor('page') changed():void { this.load().catch(() => {}); }
  private hostContext():common.Context | undefined {
    try { return this.getUIContext().getHostContext(); }
    catch (_) { return undefined; }
  }
  private async load():Promise<void> {
    const healthRevision=++this.healthRevision;
    if (this.page==='phone_health_reports') {
      this.workoutState='';this.workoutCode=0;
      let answer:WorkoutAccessResult=new WorkoutAccessResult('unknown');
      try {
        answer=await workoutAccess(this.hostContext());
      } catch (_) {
        answer=new WorkoutAccessResult('unknown');
      }
      if (this.active && this.page==='phone_health_reports' && healthRevision===this.healthRevision) {
        this.workoutState=answer.state;this.workoutCode=answer.code;
      }
    }
    if (this.page==='phone_live_screen') this.screenScroll.load(this.hostContext());
    if (this.page==='about') {
      try {
        const info=bundleManager.getBundleInfoForSelfSync(bundleManager.BundleFlag.GET_BUNDLE_INFO_DEFAULT);
        this.version=info.versionName;this.buildNumber=String(info.versionCode);
      } catch (_) { this.version='';this.buildNumber=''; }
    }
    if (this.page==='storage') {
      this.storageError=false;
      try {
        const stats=await storageStatistics.getCurrentBundleStats();
        if (this.active && this.page==='storage') { this.cacheBytes=stats.cacheSize;this.dataBytes=stats.dataSize; }
      } catch (_) { if (this.active && this.page==='storage') this.storageError=true; }
    }
  }
  private bytes(value:number):string {
    if (value<0) return '…';
    if (value<1000) return value+' B';
    return value<1000000 ? (value/1000).toFixed(1)+' KB' : value<1000000000 ? (value/1000000).toFixed(1)+' MB' : (value/1000000000).toFixed(1)+' GB';
  }
  @Builder info(label:string,value:string) {
    Row({space:10}) {
      Text(label).fontSize(17).maxFontScale(Tokens.interfaceFontMaxScale).fontColor(Colors.ink).layoutWeight(1)
      Text(value).fontSize(15).maxFontScale(Tokens.interfaceScaleFor(15)).fontColor(Colors.secondary)
    }.width('100%').constraintSize({minHeight:48}).padding({left:20,right:20}).accessibilityGroup(true)
  }
  @Builder action(label:string,id:string,tap:()=>void) {
    Button(label,{type:ButtonType.Normal,buttonStyle:ButtonStyleMode.TEXTUAL})
      .width('100%').constraintSize({minHeight:48}).fontColor(Colors.link).onClick(tap).id(id)
  }
  @Builder notices() { LicenseNotices() }
  build() {
    List() {
      ListItem() { Column() {
        if (this.page==='about') {
          this.info(tr('version'),this.version || tr('unknown'))
          this.info(tr('phone_settings_build'),this.buildNumber || tr('unknown'))
          this.action(tr('diagnostics'),'settings.about.diagnostics',()=>this.onOpen('diagnostics'))
          this.action(tr('open_source_licenses'),'settings.about.licenses',()=>{this.licenses=true;})
        } else if (this.page==='phone_health_reports') {
          this.info(tr('phone_auto_workout_reports'),tr('ks_off'))
          Column() {
          if (!this.workoutState) LoadingProgress().height(28).width(28)
          else if (this.workoutState==='granted' || this.workoutState==='not_granted') {
            this.info(tr('phone_workout_access'),tr('phone_workout_'+this.workoutState))
          } else { Text(tr('phone_workout_'+this.workoutState)+(this.workoutCode ? ' ('+this.workoutCode+')':''))
            .fontSize(15).fontColor(Colors.secondary).width('100%').padding(16) }
          }.width('100%').id('settings.health.access.'+(this.workoutState || 'checking'))
          Text(tr('phone_workout_access_note')).fontSize(13).fontColor(Colors.secondary).padding(16)
          this.action(tr('phone_workout_check'),'settings.health.check',()=>{this.load().catch(() => {});})
        } else if (this.page==='phone_live_screen') {
          Text(tr('phone_screen_scroll_speed')).fontSize(15).fontWeight(FontWeight.Medium).fontColor(Colors.secondary).padding(12)
          ForEach(screenScrollChoices,(choice:string)=>{
            Button({type:ButtonType.Normal}) { Row({space:8}) {
              Text(tr('phone_screen_'+choice)).fontSize(17).fontColor(Colors.ink).layoutWeight(1)
              if (this.screenScroll.choice===choice) SymbolGlyph($r('sys.symbol.checkmark')).fontSize(17).fontColor([Colors.link])
            }.width('100%').padding({left:20,right:20}) }.width('100%').constraintSize({minHeight:48}).backgroundColor(Color.Transparent)
              .accessibilitySelected(this.screenScroll.choice===choice).enabled(this.screenScroll.ready && !this.screenScroll.saving)
              .onClick(()=>{this.screenScroll.choose(choice).then((saved:boolean)=>{if(this.active && !saved)this.getUIContext().getPromptAction().showToast({message:tr('cache_unavailable')});});})
              .id('settings.live_screen.speed.'+choice)
          },(choice:string)=>choice)
          Text(tr('phone_screen_scroll_hint')).fontSize(13).fontColor(Colors.secondary).padding(16)
        } else if (this.page==='storage') {
          this.info(tr('phone_settings_data_size'),this.bytes(this.dataBytes))
          this.info(tr('phone_settings_cache_size'),this.bytes(this.cacheBytes))
          if (this.storageError) this.action(tr('retry'),'settings.storage.retry',()=>{this.load().catch(() => {});})
        } else if (this.page==='phone_hubs') {
          ForEach(this.model.profiles,(profile:HubProfile)=>{
            Button({type:ButtonType.Normal}) {
              this.info(profile.name || profile.hubId,profile.id===this.model.profile.id ? tr(this.model.connected ? 'online':'offline') : '')
            }.padding(0).width('100%').backgroundColor(Color.Transparent).accessibilitySelected(profile.id===this.model.profile.id)
              .onClick(()=>{ if (profile.id!==this.model.profile.id) this.model.selectProfile(profile).catch((err:Error)=>this.model.reportError(err)); })
          },(profile:HubProfile)=>profile.id)
          this.action(tr('ssh_settings'),'settings.hub.ssh',()=>this.onOpen('ssh'))
          this.action(tr('diagnostics'),'settings.hub.diagnostics',()=>this.onOpen('diagnostics'))
          this.action(tr('connect'),'settings.hub.add',()=>this.onPair())
        } else if (this.page==='phone_pairing') {
          this.action(tr('connect'),'settings.pairing.connect',()=>this.onPair())
          this.action(tr('phone_machines'),'settings.pairing.machines',()=>this.onOpen('machines'))
        }
      }.width('100%').backgroundColor(Colors.card(this.elevated)).borderRadius(PhoneStyle.cardRadius).clip(true) }
    }.width('100%').height('100%').padding({left:16,right:16,top:28,bottom:24}).backgroundColor(Colors.listGroup(this.elevated))
      .bindSheet($$this.licenses,this.notices(),{title:{title:tr('open_source_licenses')},height:SheetSize.LARGE,showClose:true})
  }
}
