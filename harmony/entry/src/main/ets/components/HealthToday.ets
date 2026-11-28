import { i18n } from '@kit.LocalizationKit';
import { Colors } from '../design/Colors';
import { PhoneStyle } from '../design/PhoneStyle';
import { Motion } from '../design/Motion';
import { Tokens } from '../design/Tokens';
import { tr } from '../design/Strings';
import { Haptics } from '../platform/Haptics';
import { clientLocale } from '../core/ClientLocale';
import { healthGoalWidth } from '../core/HealthGoalLabel';
import { healthIntakePageId } from '../core/WireCompatibility';
import { ClientModel } from '../models/ClientModel';
import { HealthIntakeModel, IntakePortion, intakeAmount } from '../models/HealthIntake';
import { Wire } from '../models/Wire';
import { NativeEnvironment, nativeEnvironment } from '../platform/NativeEnvironment';
import { ExtensionPage } from './ExtensionPage';

@ComponentV2
export struct HealthToday {
  @Require @Param intake: HealthIntakeModel;
  @Require @Param model: ClientModel;
  @Param resolving: boolean = false;
  @Param unavailable: string = '';
  @Event onLink: (url: string) => void = (_url:string) => {};
  @Local private env: NativeEnvironment = nativeEnvironment;
  @Local private kind: string = 'water';
  @Local private amount: string = '';
  @Local private detail: boolean = false;
  @Local private detailClient: ClientModel | undefined = undefined;
  @Local private demoWaterMl: number = 1500;
  @Local private demoProteinGrams: number = 48;
  private amountOwner: string = '';
  private amountDialog: number = -1;
  private dialogEpoch: number = 0;
  private mounted: boolean = false;
  private clock: number = -1;
  aboutToAppear(): void { this.mounted=true;this.syncDayClock(); }
  aboutToDisappear(): void { this.mounted=false;this.syncDayClock();this.dismiss(); }
  @Monitor('env.foreground', 'intake.supported') syncDayClock(): void {
    if (this.clock!==-1) { clearInterval(this.clock);this.clock=-1; }
    if (!this.mounted || !this.env.foreground || !this.intake.supported || this.model.demo) return;
    this.intake.refreshDay();this.clock=setInterval(() => this.intake.refreshDay(),60000);
  }
  @Monitor('model.profile', 'model.connected', 'intake.ownerKey') ownershipChanged(): void { this.dismiss(); }
  private dismiss(): void {
    this.detail=false;this.detailClient=undefined;this.dialogEpoch++;this.amount='';this.amountOwner='';
    const id=this.amountDialog;this.amountDialog=-1;
    if (id>=0) {
      try { this.getUIContext().getPromptAction().closeCustomDialog(id); }
      catch (_) {}
    }
  }
  private metric(regular:number,large:number): number { return PhoneStyle.formSize(regular,large,this.env.fontScale); }
  private get systemLocale(): string { return clientLocale(i18n.System.getSystemLanguage()); }
  private words(n:number): string {
    const options:Intl.NumberFormatOptions={maximumFractionDigits:1,minimumFractionDigits:0,useGrouping:true};
    return new Intl.NumberFormat(this.systemLocale,options).format(n);
  }
  private title(kind:string): string { return tr('intake_'+kind); }
  private unit(kind:string): string { return tr(kind==='water'?'intake_ml':'intake_g'); }
  // ArkUI retains the ring's child callbacks. Read the current traced snapshot inside each callback.
  private portion(kind: string): IntakePortion | undefined {
    if (this.model?.demo) {
      return kind === 'water' ? new IntakePortion(this.demoWaterMl, 2000) : new IntakePortion(this.demoProteinGrams, 60);
    }
    return kind === 'water' ? this.intake.data?.water : this.intake.data?.protein;
  }
  private amountLabel(kind: string): string {
    const value = this.portion(kind);
    return value ? this.words(value.today) : '';
  }
  private progressRatio(kind: string): number {
    const value = this.portion(kind);
    return value ? Math.max(0, Math.min(1, value.today / Math.max(1, value.goal))) : 0;
  }
  private goal(kind: string): string {
    const value = this.portion(kind);
    return value ? tr('intake_goal').replace('{goal}', this.words(value.goal)).replace('{unit}', this.unit(kind)) : '';
  }
  private goalWidth(label: string): number {
    const ui = this.getUIContext();
    const fontSize = Tokens.interfaceFontVp(12, this.env.fontScale);
    return healthGoalWidth(label, this.metric(105, 135.3), (text: string): number =>
      ui.px2vp(Math.ceil(ui.getMeasureUtils().measureText({ textContent: text, fontSize: fontSize }))));
  }
  private ratioLabel(kind: string): string {
    const value = this.portion(kind);
    return value ? tr('intake_ratio').replace('{today}', this.words(value.today)).replace('{goal}', this.words(value.goal)).replace('{unit}', this.unit(kind)) : '';
  }
  private tint(kind:string): ResourceColor { return kind==='water' ? Colors.healthWater : Colors.healthProtein; }
  private tintWash(kind:string): ResourceColor { return kind==='water' ? Colors.healthWaterWash : Colors.healthProteinWash; }
  private primaryNumberInk(): ResourceColor {
    return Colors.systemControls && Colors.styleID==='glass' ? $r('app.color.persona_switcher_ink') : Colors.ink;
  }
  private openRing(kind:string): void {
    const client=this.intake.currentClient();if (!client || this.intake.busy) return;
    Haptics.tap();
    this.detailClient=client;this.kind=kind;this.detail=true;
  }
  private async ask(kind:string): Promise<void> {
    const demo=this.model.demo, client=demo ? this.model : this.intake.currentClient();
    if (!client || (!demo && this.intake.busy) || this.amountDialog!==-1) return;
    Haptics.tap();
    this.kind=kind;this.amount='';this.amountOwner=demo ? 'demo' : this.intake.ownerKey;this.amountDialog=-2;
    const version=++this.dialogEpoch;
    try {
      const prompt=this.getUIContext().getPromptAction();
      const id=await prompt.openCustomDialog({builder:() => {this.amountEditor();},alignment:DialogAlignment.Center,
        autoCancel:true,onDidDisappear:() => {if (version===this.dialogEpoch) {this.amountDialog=-1;this.amount='';this.amountOwner='';}}});
      const stillCurrent = demo ? this.model.demo && client===this.model : client===this.intake.currentClient();
      if (!this.mounted || version!==this.dialogEpoch || !stillCurrent) {
        if (version===this.dialogEpoch) this.amountDialog=-1;
        try { prompt.closeCustomDialog(id); } catch (_) {}
        return;
      }
      this.amountDialog=id;
    } catch (_) { if (version===this.dialogEpoch) this.amountDialog=-1; }
  }
  private submit(): void {
    const value=intakeAmount(this.kind,this.amount),kind=this.kind;
    if (value===undefined) return;
    if (this.model.demo) {
      if (this.amountOwner!=='demo') return;
      Haptics.success();
      this.dismiss();
      if (kind==='water') this.demoWaterMl=Math.round((this.demoWaterMl+value)*10)/10;
      else this.demoProteinGrams=Math.round((this.demoProteinGrams+value)*10)/10;
      return;
    }
    if (!this.amountOwner || this.amountOwner!==this.intake.ownerKey || !this.intake.currentClient() || this.intake.busy) return;
    Haptics.success();
    this.dismiss();this.intake.record(kind,value);
  }
  @Builder amountEditor() {
    Column({space:12}) {
      Text(tr(this.kind==='water'?'intake_water_amount':'intake_protein_amount')).fontSize(17).fontWeight(600).fontColor(Colors.ink)
      Text(tr(this.kind==='water'?'intake_water_hint':'intake_protein_hint')).fontSize(13).fontColor(Colors.secondary).textAlign(TextAlign.Center)
      TextInput({text:this.amount,placeholder:this.unit(this.kind)}).type(InputType.NUMBER_DECIMAL)
        .onChange((text:string) => {this.amount=text;}).id('health.intake.amount').defaultFocus(true)
      Row({space:8}) {
        Button(tr('cancel')).layoutWeight(1).buttonStyle(ButtonStyleMode.TEXTUAL).onClick(() => { Haptics.tap(); this.dismiss(); }).id('health.intake.cancel')
        Button(tr('intake_log')).layoutWeight(1).buttonStyle(ButtonStyleMode.TEXTUAL).enabled(intakeAmount(this.kind,this.amount)!==undefined && (this.model.demo || !this.intake.busy))
          .onClick(() => this.submit()).id('health.intake.submit')
      }.width('100%')
    }.width('84%').constraintSize({maxWidth:360}).padding(20).backgroundColor(Colors.surface).borderRadius(Tokens.radiusPanel)
      .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.cardBorder(this.env.dark),radius:Tokens.radiusPanel})
      .shadow(PhoneStyle.cardElevation(this.env.dark))
  }
  @Builder detailPage() {
    Column() {
      Row() {
        Text(this.title(this.kind)).layoutWeight(1).fontSize(20).fontWeight(600).fontColor(Colors.ink)
        Button({type:ButtonType.Circle}) { SymbolGlyph($r('sys.symbol.xmark')).fontSize('17vp').fontColor([Colors.secondary]) }
          .width(40).height(40).accessibilityText(tr('close')).onClick(() => { Haptics.tap(); this.detail=false; }).id('health.intake.detail.close')
      }.width('100%').padding({left:20,right:16,top:10,bottom:6})
      if (this.detailClient && this.detailClient===this.intake.currentClient()) {
        ExtensionPage({model:this.detailClient,extensionId:healthIntakePageId,uiId:'pages',healthIntake:true,
          state:JSON.stringify(Wire.payload(['kind'],[this.kind])),context:Wire.payload(['locale'],[this.systemLocale]),
          active:this.detail,authorized:this.model.connected && this.intake.available,
          onLink:(url:string)=>this.onLink(url),onBack:()=>{this.detail=false;}}).layoutWeight(1)
      }
    }.width('100%').height('100%').backgroundColor(Colors.grouped).id('health.intake.detail.'+this.kind)
  }
  @Builder ring(kind:string) {
    Button({type:ButtonType.Normal}) {
      Column() {
        Stack() {
          Progress({value:this.progressRatio(kind),total:1,type:ProgressType.Ring})
            .width(this.metric(105,135.3)).height(this.metric(105,135.3)).color(this.tint(kind))
            .backgroundColor(this.tintWash(kind)).style({strokeWidth:9,enableSmoothEffect:!this.env.reduceMotion}).accessibilityLevel('no')
          Column() {
            Text(this.amountLabel(kind)).fontSize(Tokens.typeTitle).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeTitle)).fontWeight(FontWeight.Bold).fontFeature('tnum 1').fontColor(this.primaryNumberInk())
              .maxLines(1).minFontSize('13vp').maxFontSize(Tokens.interfaceFontVp(Tokens.typeTitle,this.env.fontScale)).textAlign(TextAlign.Center)
            Text(this.unit(kind)).fontSize(Tokens.typeLabel).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeLabel)).fontColor($r('sys.color.font_tertiary')).maxLines(1)
          }.width(this.metric(69.1,91)).alignItems(HorizontalAlign.Center)
        }
        Text(this.title(kind)).fontSize(Tokens.typeCaption).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption)).fontWeight(600).fontColor(Colors.ink).maxLines(1).margin({top:6})
        Text(this.goal(kind)).fontSize(Tokens.typeLabel).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeLabel)).fontColor(Colors.secondary)
          .width(this.goalWidth(this.goal(kind))).textAlign(TextAlign.Start).margin({top:3})
      }.alignItems(HorizontalAlign.Center)
    }.padding(0).backgroundColor(Color.Transparent).buttonStyle(ButtonStyleMode.TEXTUAL)
      .accessibilityText(this.title(kind)).accessibilityDescription(this.ratioLabel(kind))
      .onClick(() => this.openRing(kind)).id('health.intake.ring.'+kind)
  }
  @Builder action(kind:string,key:string,id:string) {
    Button({type:ButtonType.Capsule}) {
      Text(tr(key)).fontSize(Tokens.typeLabel).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeLabel)).fontWeight(FontWeight.Medium).fontColor(Colors.link)
        .maxLines(1).minFontSize('8vp').maxFontSize(Tokens.interfaceFontVp(Tokens.typeLabel,this.env.fontScale)).textAlign(TextAlign.Center)
    }.layoutWeight(1).width('100%').height(this.metric(58,71.7)).padding({left:4,right:4})
      .backgroundColor(Colors.healthActionSurface).border({width:0.6,color:Colors.hairlineGlass})
      .onClick(() => {
        if (id==='water') {
          Haptics.success();
          if (this.model.demo) this.demoWaterMl+=250;
          else this.intake.record('water');
        } else this.ask(kind).catch(() => {});
      }).id('health.intake.log.'+id)
  }
  @Builder water() {
    Column({space:18}) {
      this.ring('water')
      if (this.env.fontScale>=1.5) Column({space:8}) {
        this.action('water','intake_cup','water')
        this.action('water','intake_other','water-amount')
      }.width('100%').height(this.metric(58,71.7)*2+8)
      else Row({space:6}) {
        this.action('water','intake_cup','water')
        this.action('water','intake_other','water-amount')
      }.width('100%')
    }.width('100%').alignItems(HorizontalAlign.Center).padding({left:12,right:12,top:16,bottom:14})
      .backgroundColor(Colors.surface).borderRadius(Tokens.radiusPanel)
      .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.cardBorder(this.env.dark),radius:Tokens.radiusPanel})
      .shadow(PhoneStyle.cardElevation(this.env.dark))
  }
  @Builder protein() {
    Column({space:18}) {
      this.ring('protein')
      Row() { this.action('protein','intake_log_grams','protein') }.width('100%')
    }.width('100%').alignItems(HorizontalAlign.Center).padding({left:12,right:12,top:16,bottom:14})
      .backgroundColor(Colors.surface).borderRadius(Tokens.radiusPanel)
      .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.cardBorder(this.env.dark),radius:Tokens.radiusPanel})
      .shadow(PhoneStyle.cardElevation(this.env.dark))
  }
  @Builder totals() {
    GridRow({columns:this.env.fontScale>=2.0 ? 1 : 2,gutter:{x:10,y:10}}) {
      GridCol({span:1}) { this.water() }
      GridCol({span:1}) { this.protein() }
    }.width('100%').enabled(this.model.demo || !this.intake.busy).id('health.intake.today')
  }
  @Builder feedback() {
    if (this.intake.notice || this.intake.error) Row({space:8}) {
      Text(tr(this.intake.error || this.intake.notice)).fontSize(14).maxFontScale(Tokens.interfaceScaleFor(14))
        .fontColor(this.intake.error?Colors.danger:Colors.ink).layoutWeight(1)
      if (this.intake.actionId) Button(tr(this.intake.error?'retry':'intake_undo')).buttonStyle(ButtonStyleMode.TEXTUAL)
        .onClick(() => { Haptics.tap(); this.intake.act(this.intake.actionId); }).id('health.intake.feedback.action')
      else if (this.intake.error && !this.intake.data && this.intake.supported) Button(tr('retry')).buttonStyle(ButtonStyleMode.TEXTUAL)
        .onClick(() => { Haptics.tap(); this.intake.refresh(); }).id('health.intake.retry')
    }.width('100%').padding({left:14,right:10,top:8,bottom:8}).backgroundColor(Colors.surface).borderRadius(PhoneStyle.cardRadius)
      .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.cardBorder(this.env.dark),radius:PhoneStyle.cardRadius})
      .shadow(PhoneStyle.cardElevation(this.env.dark)).animation(Motion.state(this.env.reduceMotion)).id('health.intake.feedback')
    if (this.intake.refreshError) Row({space:8}) {
      Text(tr(this.intake.refreshError)).fontSize(14).fontColor(Colors.secondary).layoutWeight(1)
      Button(tr('retry')).buttonStyle(ButtonStyleMode.TEXTUAL).onClick(() => { Haptics.tap(); this.intake.refresh(); }).id('health.intake.retry')
    }.width('100%').padding({left:14,right:10,top:6,bottom:6}).backgroundColor(Colors.surface).borderRadius(PhoneStyle.cardRadius)
      .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.cardBorder(this.env.dark),radius:PhoneStyle.cardRadius})
      .shadow(PhoneStyle.cardElevation(this.env.dark))
  }
  build() {
    Column({space:8}) {
      Text(tr('intake_today')).fontSize(15).maxFontScale(Tokens.interfaceScaleFor(15)).fontWeight(FontWeight.Medium).fontColor(Colors.secondary)
        .height(this.metric(36,44)).width('100%').padding({left:12}).id('health.intake.heading')
      if (this.model.demo) {
        this.totals()
      } else if (!this.model.connected || !this.model.features.includes('health_intake') || (!this.intake.available && !this.resolving) || (!this.intake.data && !this.intake.error)) {
        Column({space:8}) {
          if (!this.model.connected) Text(tr('offline')).fontSize(15).fontColor(Colors.secondary)
          else if (!this.model.features.includes('health_intake')) Text(tr('health_intake_update')).fontSize(15).fontColor(Colors.secondary).id('health.intake.update')
          else if (!this.intake.available && !this.resolving) Text(tr(this.unavailable || 'health_default_manager_unavailable')).fontSize(15).fontColor(Colors.secondary)
          else LoadingProgress().width(30).height(30)
        }.width('100%').alignItems(HorizontalAlign.Start).padding({left:20,right:20,top:20,bottom:16})
          .backgroundColor(Colors.surface).borderRadius(Tokens.radiusPanel)
          .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.cardBorder(this.env.dark),radius:Tokens.radiusPanel})
          .shadow(PhoneStyle.cardElevation(this.env.dark))
      } else if (this.intake.data) {
        this.totals()
      }
      this.feedback()
    }.width('100%').alignItems(HorizontalAlign.Start)
      .bindSheet($$this.detail,this.detailPage(),{height:SheetSize.LARGE,dragBar:true,showClose:false,onDisappear:() => {this.detail=false;this.detailClient=undefined;if(this.mounted)this.intake.refresh();}})
  }
}
