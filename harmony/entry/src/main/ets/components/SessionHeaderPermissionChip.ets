import { Colors } from '../design/Colors';
import { Symbols } from '../design/Symbols';
import { tr } from '../design/Strings';
import { Tokens } from '../design/Tokens';
import { ClientModel } from '../models/ClientModel';
import { SessionComposerModes } from '../models/SessionComposerModes';
import { SessionHeaderPermissions } from '../models/SessionHeaderPermissions';
import { nativeEnvironment } from '../platform/NativeEnvironment';
import { Motion } from '../design/Motion';
import { Haptics } from '../platform/Haptics';

@ComponentV2
export struct SessionHeaderPermissionChip {
  @Require @Param model: ClientModel;
  @Require @Param permission: SessionHeaderPermissions;
  @Local private command: SessionComposerModes = new SessionComposerModes();
  @Local private opened: boolean = false;
  @Event onMenuChange:(open:boolean) => void=() => {};
  @Monitor('opened') menuChanged():void { this.onMenuChange(this.opened); }
  private mounted: boolean = false;
  private owner: string = '';

  aboutToAppear(): void { this.mounted=true; }
  aboutToDisappear(): void { this.mounted=false; this.opened=false; this.command.clear(); }
  @Monitor('model.profile','model.selected','model.connected') refreshOwner(): void {
    if (this.owner!==this.stamp()) { this.opened=false; this.command.clear(); }
  }
  private stamp(): string { return JSON.stringify([this.model.scope(),this.model.connectionGeneration(),this.model.target()]); }
  private current(stamp:string): boolean { return this.mounted && this.owner===stamp && this.stamp()===stamp; }
  private allows(): boolean { return this.model.connected && this.model.canCompose() && !this.model.decision && this.permission.switchable; }
  private name(id:string): string { const key=this.permission.nameKey(id);return key ? tr(key) : id; }
  private label(): string {
    return this.permission.current ? tr('header_permission_title').replace('{mode}',this.name(this.permission.current)) : tr('permission_mode');
  }
  private icon(id:string): Resource {
    if (id==='default' && !this.permission.listed) return $r('sys.symbol.hand_raised_fingers_spread');
    if (id==='plan') return $r('sys.symbol.list_bullet');
    if (id==='acceptEdits') return Symbols.edit;
    if (id==='auto') return $r('sys.symbol.wand_and_stars');
    if (id==='bypassPermissions') return $r('sys.symbol.security_shield');
    return $r('sys.symbol.lock_shield');
  }
  private tint(): ResourceColor {
    const id=this.permission.current;
    return id==='bypassPermissions' ? Colors.danger : id==='plan' ? Colors.link :
      id==='acceptEdits' || id==='auto' ? Colors.working : Colors.secondary;
  }
  private detail(id:string): string { const k=this.permission.detailKey(id);return k ? tr(k) : ''; }
  private pick(id:string): void {
    const stamp=this.owner;
    if (!this.current(stamp) || !this.allows() || !this.permission.permits(id)) return;
    this.opened=false;
    const perform=():void => {
      if (this.current(stamp) && this.allows() && this.permission.permits(id)) {
        this.command.change(this.model,'header_permission',id,():boolean => this.current(stamp) && this.allows()).catch(() => {});
      }
    };
    const confirm=this.permission.confirmation(id);
    if (!confirm) { perform(); return; }
    this.getUIContext().showAlertDialog({title:tr(confirm==='enter' ? 'header_permission_enter' : 'header_permission_leave'),
      message:confirm==='leave' ? tr('header_permission_leave_detail') : tr('header_permission_full_detail')+
        (this.permission.restarts ? '\n'+tr('header_permission_restart') : ''),
      primaryButton:{value:tr('cancel'),action:() => {}},secondaryButton:{value:this.name(id),action:perform}});
  }
  @Builder menu() {
    Menu() {
      MenuItemGroup({header:this.label()+(!this.permission.running ? '\n'+tr('offline') : '')+(this.permission.pending ? '\n'+tr('header_permission_pending') : '')}) {
        ForEach(this.permission.choices,(value:string) => {
          MenuItem({content:this.name(value),labelInfo:this.detail(value)})
            .selectIcon(true).selected(this.permission.current===value).enabled(this.allows() && !this.command.saving)
            .onClick(() => this.pick(value)).id('chat.header.permission.'+value)
        },(value:string):string => value)
        if (this.permission.unavailable) MenuItem({content:this.name('bypassPermissions'),labelInfo:tr('header_permission_start_only')})
          .enabled(false).id('chat.header.permission.unavailable')
        if (this.command.error) MenuItem({content:tr(this.command.error)}).enabled(false)
      }
    }
  }
  build() {
    Button({type:ButtonType.Capsule}) {
      Stack() {
        if (['plan','auto','acceptEdits','bypassPermissions'].includes(this.permission.current)) {
          Rect().width('100%').height('100%').radius(28).fill(this.tint()).opacity(0.12)
            .animation(Motion.state(nativeEnvironment.reduceMotion))
        } else Rect().width('100%').height('100%').radius(28).fill(Colors.control)
        if (this.command.saving) LoadingProgress().width(16).height(16)
        else SymbolGlyph(this.icon(this.permission.current)).fontSize(Tokens.interfaceFontVp(15,nativeEnvironment.fontScale)).fontColor([this.tint()])
      }.height(30*Math.min(nativeEnvironment.fontScale,1.32))
        .width(Math.max(35,35*Math.min(nativeEnvironment.fontScale,1.32)))
    }.width(Math.max(44,35*Math.min(nativeEnvironment.fontScale,1.32))).height(44).padding(0).backgroundColor(Color.Transparent)
      .accessibilityText(this.label()).accessibilityDescription(this.permission.pending ? tr('header_permission_pending') : '')
      .enabled(!this.command.saving).onClick(() => {
        Haptics.selection();
        this.owner=this.stamp();this.command.load(this.model,false,false);this.opened=true;
      }).bindMenu(this.opened,this.menu,{placement:Placement.BottomRight,onDisappear:() => {this.opened=false;}})
      .id('chat.permissionMode')
  }
}
