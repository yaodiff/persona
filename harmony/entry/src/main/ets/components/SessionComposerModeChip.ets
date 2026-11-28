import { ClientModel } from '../models/ClientModel';
import { SessionComposerModes, validComposerModelID } from '../models/SessionComposerModes';
import { Wire } from '../models/Wire';
import { Colors } from '../design/Colors';
import { tr } from '../design/Strings';
import { Tokens } from '../design/Tokens';
import { Haptics } from '../platform/Haptics';

@ComponentV2
export struct SessionComposerModeChip {
  @Require @Param model:ClientModel;
  @Param active:boolean=false;
  @Event onMenu:(open:boolean)=>void=() => {};
  @Local modes:SessionComposerModes=new SessionComposerModes();
  @Local opened:boolean=false;
  @Local custom:boolean=false;
  @Local typed:string='';
  private owner:string='';
  private stamp():string { return JSON.stringify([this.model.scope(),this.model.connectionGeneration(),this.model.target()]); }
  private eligible():boolean { return this.active && !this.model.decision && this.modes.visible(this.model); }
  private update():void {
    if ((this.opened || this.custom) && (!this.eligible() || this.owner!==this.stamp())) { this.custom=false; this.typed=''; this.close(); }
    if (this.eligible()) this.modes.load(this.model); else this.modes.clear();
  }
  aboutToAppear():void { this.update(); }
  aboutToDisappear():void { this.close(); this.modes.clear(); }
  @Monitor('model.selected','model.sessions','model.connected','model.profile','active') changed():void { this.update(); }
  private close():void { if (this.opened) { this.opened=false; if (!this.custom) this.onMenu(false); } }
  private level(key:string):string { return ['auto','low','medium','high','xhigh','max'].includes(key) ? tr(key) : key; }
  private description():string {
    return [this.modes.modelName(this.model),this.level(this.modes.currentEffort(this.model)),
      this.modes.pendingEffort(this.model) ? tr('pending')+' '+this.level(this.modes.pendingEffort(this.model)) : ''].filter((v:string) => !!v).join(' ');
  }
  private begin():void {
    if (!this.eligible() || this.modes.saving) return;
    Haptics.selection();
    this.owner=this.stamp(); this.opened=true; this.onMenu(true); this.modes.load(this.model,!!this.modes.error);
  }
  private change(field:string,value:string):void {
    const key=this.owner; if (!key || key!==this.stamp()) return;
    this.modes.change(this.model,field,value,() => this.eligible() && key===this.stamp()).then((saved:boolean) => {
      if (saved && key===this.stamp() && field==='custom_model') { this.custom=false; this.typed=''; this.onMenu(false); }
      if (!saved && key===this.stamp() && this.modes.error) this.getUIContext().getPromptAction().showToast({message:tr(this.modes.error)});
    });
  }
  private modeLabel(value:string):string {
    if (value==='default') return tr('default');
    if (value==='plan') return tr('plan');
    if (value==='auto') return tr('auto');
    if (value==='acceptEdits') return tr('session_task_auto_edits');
    return value;
  }
  @Builder menu() {
    Menu() {
      if (this.modes.allowsTypedModel(this.model)) MenuItem({content:tr('flow_other_model')})
        .id('chat.mode.other').onClick(() => { if (this.owner===this.stamp()) { this.typed=''; this.custom=true; this.onMenu(true); } })
      if (this.modes.catalog.length) MenuItemGroup({header:tr('model')}) {
        ForEach(this.modes.catalog,(m:Record<string,Object>) => {
          MenuItem({content:Wire.text(m,'name') || Wire.text(m,'id')}).selectIcon(true).selected(this.modes.chosenModel(this.model)===Wire.text(m,'id'))
            .enabled(!this.modes.saving && this.modes.acceptsModel(this.model)).id('chat.mode.model.'+Wire.text(m,'id')).onClick(() => this.change('model',Wire.text(m,'id')))
        },(m:Record<string,Object>) => Wire.text(m,'id'))
      }
      if (this.modes.loading) MenuItem({content:tr('loading')}).enabled(false)
      if (this.modes.error) MenuItem({content:tr(this.modes.error)}).enabled(false)
      if (this.modes.efforts(this.model).length) MenuItemGroup({header:tr('effort')}) {
        ForEach(this.modes.efforts(this.model),(e:string) => {
          MenuItem({content:this.level(e)}).selectIcon(true).selected(this.modes.currentEffort(this.model)===e)
            .enabled(!this.modes.saving).id('chat.mode.effort.'+e).onClick(() => this.change('effort',e))
        },(e:string) => e)
      }
      if (this.modes.modeValues(this.model).length) MenuItem({content:tr('permission_mode'),builder:this.permissions}).id('chat.mode.permissions')
    }
  }
  @Builder permissions() {
    Menu() {
      ForEach(this.modes.modeValues(this.model),(m:string) => {
        MenuItem({content:this.modeLabel(m)}).selectIcon(true).selected(Wire.text(this.modes.data(this.model),'permission_mode')===m)
          .enabled(!this.modes.saving).id('chat.mode.permission.'+m).onClick(() => this.change('permission_mode',m))
      },(m:string)=>m)
    }
  }
  @Builder customModel() {
    Column({space:12}) {
      TextInput({text:this.typed,placeholder:tr('model_id')}).maxLength(257).id('chat.mode.other.input')
        .fontSize(17).onChange((value:string) => this.typed=value)
      Button(tr('apply')).enabled(!this.modes.saving && validComposerModelID(this.typed)).id('chat.mode.other.apply')
        .onClick(() => this.change('custom_model',this.typed))
    }.padding(16).width('100%')
  }
  build() {
    Row() {
      if (this.eligible()) Button({type:ButtonType.Normal}) {
        Row({space:4}) {
          if (this.modes.modelName(this.model)) Text(this.modes.modelName(this.model)).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13))
            .fontWeight(FontWeight.Medium).fontColor(Colors.ink).maxLines(1).textOverflow({overflow:TextOverflow.Ellipsis}).flexShrink(1)
          if (this.modes.currentEffort(this.model)) Text(this.level(this.modes.currentEffort(this.model))).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12))
            .fontColor(Colors.secondary).maxLines(1)
          if (this.modes.pendingEffort(this.model)) SymbolGlyph($r('sys.symbol.clock')).fontSize('12vp').fontColor([Colors.secondary])
        }.constraintSize({maxWidth:'100%'}).justifyContent(FlexAlign.End)
      }.height(40).constraintSize({maxWidth:'100%'}).padding({left:3,right:3}).backgroundColor(Color.Transparent)
        .accessibilityText(this.description()).id('chat.composer.mode').onClick(() => this.begin())
        .bindMenu(this.opened,this.menu,{placement:Placement.TopRight,onDisappear:() => this.close()})
    }.width('100%').alignItems(VerticalAlign.Center).justifyContent(FlexAlign.End)
      .bindSheet($$this.custom,this.customModel(),{title:{title:tr('flow_other_model')},height:SheetSize.MEDIUM,showClose:true,
        onDisappear:() => { this.typed=''; this.onMenu(false); }})
  }
}
