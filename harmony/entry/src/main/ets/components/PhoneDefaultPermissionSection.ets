import { PhoneStyle } from '../design/PhoneStyle';
import { PhoneOutlineIcon } from '../design/PhoneOutlineIcon';
import { Colors } from '../design/Colors';
import { Tokens } from '../design/Tokens';
import { tr } from '../design/Strings';
import { conversationPermissionModes } from '../core/PhoneSettingsPermissions';
import { ClientModel } from '../models/ClientModel';
import { PhoneAgentSettings } from '../models/PhoneAgentSettings';
import { Wire } from '../models/Wire';
import { NativeEnvironment, nativeEnvironment } from '../platform/NativeEnvironment';

@ComponentV2
export struct PhoneDefaultPermissionSection {
  @Require @Param model:ClientModel;
  @Param elevated:boolean=false;
  @Param emptyInset:number=0;
  @Local private environment:NativeEnvironment=nativeEnvironment;
  @Local form:PhoneAgentSettings=new PhoneAgentSettings();
  aboutToAppear():void { this.open(); }
  aboutToDisappear():void { this.form.close(); }
  @Monitor('model.profile','model.connected') ownerChanged():void { this.open(); }
  private open():void { this.form.open(this.model,'phone_default_permissions').catch(()=>undefined); }
  @Computed get mode():string { return Wire.text(this.form.data,'default_permission_mode'); }
  @Computed get iconScale():number { return Math.min(this.environment.fontScale,Tokens.interfaceFontMaxScale); }
  @Computed get selectionTint():ResourceColor { return Colors.systemControls && Colors.styleID==='glass' ? Colors.iconAccent : Colors.link; }
  private name(value:string):string { return conversationPermissionModes.includes(value) ? tr('assistant_feed_permission_'+value) : value; }
  @Builder modeMenu() {
    Menu() {
      ForEach(conversationPermissionModes.includes(this.mode) ? conversationPermissionModes : conversationPermissionModes.concat([this.mode]),(value:string) => {
        MenuItem({content:this.name(value)}).selectIcon(true).selected(this.mode===value)
          .onClick(()=>{ this.form.setConversationPermission(value).catch(()=>undefined); }).id('settings.default_permission.'+value)
      },(value:string)=>value)
    }
  }
  build() {
    if (this.mode) { ListItem() {
      Column() {
        Text(tr('phone_default_permissions')).fontSize(Tokens.body).maxFontScale(Tokens.interfaceFontMaxScale)
          .fontWeight(600).fontColor(PhoneStyle.settingsSecondary).width('100%').textAlign(TextAlign.Start)
          .constraintSize({minHeight:PhoneStyle.formSize(40.33,47.67,this.environment.fontScale)})
          .padding({left:20,right:20,top:6,bottom:6}).id('settings.default_permission_heading')
        Column() {
          Button({type:ButtonType.Normal}) {
            Row({space:PhoneStyle.formSize(8,5,this.environment.fontScale)}) {
              Image($r('app.media.phone_settings_permission')).supportSvg2(true).colorFilter(Colors.iconAccent)
                .width(40*this.iconScale).height(28*this.iconScale).objectFit(ImageFit.Contain).accessibilityLevel('no')
              Text(tr('phone_new_conversation_permissions')).fontSize(Tokens.body).maxFontScale(Tokens.interfaceFontMaxScale)
                .fontColor(PhoneStyle.settingsLabel).layoutWeight(1).maxLines(2).textOverflow({overflow:TextOverflow.Ellipsis})
              Row({space:PhoneStyle.formSize(5,7,this.environment.fontScale)}) {
                Text(this.name(this.mode)).fontSize(Tokens.body).maxFontScale(Tokens.interfaceFontMaxScale)
                  .fontColor(this.selectionTint).maxLines(2).textOverflow({overflow:TextOverflow.Ellipsis}).textAlign(TextAlign.End).flexShrink(1)
                Stack() {
                  PhoneOutlineIcon({name:'choices',color:this.selectionTint}).scale({x:this.iconScale,y:this.iconScale})
                }.width(11*this.iconScale).height(15*this.iconScale).accessibilityLevel('no')
              }.constraintSize({maxWidth:'48%'}).alignItems(VerticalAlign.Center)
            }.width('100%').constraintSize({minHeight:PhoneStyle.groupedRowHeight(this.environment.fontScale)})
              .padding({left:PhoneStyle.formSize(12,9,this.environment.fontScale),right:20,top:5,bottom:5})
          }.width('100%').constraintSize({minHeight:PhoneStyle.groupedRowHeight(this.environment.fontScale)}).padding(0).backgroundColor(Color.Transparent)
            .accessibilityText(tr('phone_new_conversation_permissions')+' · '+this.name(this.mode))
            .enabled(!this.form.saving && this.model.connected && !Wire.bool(this.form.data,'read_only'))
            .bindMenu(this.modeMenu).id('settings.default_permission_mode')
          if (conversationPermissionModes.includes(this.mode)) {
            Divider().strokeWidth(Tokens.divider).color(PhoneStyle.settingsSeparator)
              .margin({left:PhoneStyle.formSize(60,68,this.environment.fontScale),right:20})
            Row() {
              Text(tr(this.mode==='bypassPermissions' ? 'header_permission_full_detail' : 'header_permission_detail_'+this.mode))
                .fontSize(Tokens.caption).maxFontScale(Tokens.interfaceScaleFor(Tokens.caption))
                .fontColor(this.mode==='bypassPermissions' ? Colors.danger : PhoneStyle.settingsSecondary)
            }.width('100%').constraintSize({minHeight:PhoneStyle.formSize(52.33,59.33,this.environment.fontScale)})
              .padding({left:20,right:20,top:8,bottom:8}).id('settings.default_permission_hint')
          }
          if (this.form.notice) Text(tr(this.form.notice)).fontSize(Tokens.caption).fontColor(Colors.danger)
            .width('100%').padding({left:20,right:20,bottom:8}).id('settings.default_permission_error')
        }.width('100%').clip(true).alignItems(HorizontalAlign.Start).backgroundColor(Colors.card(this.elevated)).borderRadius(PhoneStyle.settingsGroupRadius)
          .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.cardBorder(this.environment.dark),radius:PhoneStyle.settingsGroupRadius})
          .shadow(PhoneStyle.cardElevation(this.environment.dark))
        Text(tr('phone_default_permission_footer')).fontSize(Tokens.caption).maxFontScale(Tokens.interfaceScaleFor(Tokens.caption))
          .lineHeight(PhoneStyle.settingsExampleLineHeight(15.67,22,this.environment.fontScale)).fontColor(PhoneStyle.settingsSecondary)
          .width('100%').padding({left:20,right:20,top:9,bottom:5}).id('settings.default_permission_footer')
      }.width('100%').alignItems(HorizontalAlign.Start)
    } } else if (this.form.loading || this.form.error) { ListItem() {
      if (this.form.loading) LoadingProgress().height(24).width(24)
      else Column({space:6}) {
        Text(tr(this.form.error)).fontSize(14).fontColor(Colors.danger)
        Button(tr('retry'),{buttonStyle:ButtonStyleMode.TEXTUAL}).onClick(()=>{ this.form.read().catch(()=>undefined); })
      }.width('100%')
    } } else if (this.emptyInset>0) { ListItem() { Row().width('100%').height(this.emptyInset).accessibilityLevel('no') } }
  }
}
