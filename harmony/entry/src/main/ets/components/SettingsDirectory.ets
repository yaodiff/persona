import { Tokens } from '../design/Tokens';
import { NativeEnvironment, nativeEnvironment } from '../platform/NativeEnvironment';
import { PhoneOutlineIcon } from '../design/PhoneOutlineIcon';
import { DictationPreference, dictationPreference } from '../platform/DictationPreference';
import { inputMethod } from '@kit.IMEKit';
import { i18n } from '@kit.LocalizationKit';
import { PhoneStyle, PhoneSectionTitle, PhoneSettingsRow } from '../design/PhoneStyle';
import { Colors } from '../design/Colors';
import { Symbols } from '../design/Symbols';
import { tr } from '../design/Strings';
import { supportedInterfaceLocale } from '../core/ClientLocale';
import { ClientModel } from '../models/ClientModel';
import { ResourceRow, Wire } from '../models/Wire';
import { MeetingPreferences, settingsMeetingRoleLabel } from '../models/MeetingPreferences';
import { machineOnline } from '../models/MachineDashboard';
import { PhoneDefaultPermissionSection } from './PhoneDefaultPermissionSection';
import { phoneSettingsRows,phoneSettingsSearchCatalog,phoneHubUpdateMessage } from '../core/PhoneSettingsNavigation';
import { Haptics } from '../platform/Haptics';
import { toastCenter } from '../models/ToastCenter';

const AGENT_OPTIONS:string[] = ['phone_agent_folders','phone_claude_config','phone_codex_config','phone_agy_config','triage'];
const LOCAL_SEARCH_ROUTES:string[] = ['phone_agent_folders','launchers','engine','schedules','privacy','memory','ssh','node_update','ledger','diagnostics'];
const LANGUAGES:string[] = ['default','zh-CN','en-US','ja-JP'];

function languageName(tag:string):string {
  const locale = supportedInterfaceLocale(tag);
  return locale === 'zh-Hans' ? '简体中文' : locale === 'ja' ? '日本語' : 'English';
}

function currentLanguage(model:ClientModel):string {
  return model.language === 'default' ? tr('phone_settings_system_language').replace('{language}',languageName(i18n.getSystemLanguage())) : languageName(model.language);
}

function settingIcon(id:string):Resource {
  switch (id) {
    case 'models': return $r('sys.symbol.square_stack_3d');
    case 'assistant': return Symbols.persona;
    case 'standing_authorizations': return $r('sys.symbol.checkmark_shield');
    case 'channels': return $r('sys.symbol.ellipsis_bubble');
    case 'connectors': return $r('sys.symbol.link');
    case 'extensions': return Symbols.apps;
    case 'phone_inbox_sources': return Symbols.inbox;
    case 'phone_live_screen': return $r('sys.symbol.desktop');
    case 'phone_native_alarms': return $r('sys.symbol.bell');
    case 'phone_health_reports': return $r('sys.symbol.heart');
    case 'phone_xiaoyi': return Symbols.persona;
    case 'phone_hubs': return $r('sys.symbol.link');
    case 'phone_pairing': return $r('sys.symbol.qrcode');
    case 'phone_settings_advanced': return $r('sys.symbol.slider_horizontal_2');
    case 'storage': return $r('sys.symbol.doc_plaintext');
    case 'about': return $r('sys.symbol.info_circle');
    case 'phone_knowledge_selection':
    case 'memory': return $r('sys.symbol.book');
    case 'phone_meeting_options': return Symbols.calendar;
    case 'phone_agent_folders':
    case 'phone_claude_config':
    case 'phone_codex_config':
    case 'phone_agy_config':
    case 'phone_agent_options':
    case 'triage': return $r('sys.symbol.slider_horizontal_2');
    case 'phone_agent_followup': return Symbols.inbox;
    case 'phone_agent_layout': return Symbols.spaces;
    case 'launchers': return $r('sys.symbol.play_fill');
    case 'engine': return $r('sys.symbol.computer');
    case 'machines': return $r('sys.symbol.desktop');
    case 'appearance': return Symbols.appearance;
    case 'phone_language_options': return $r('sys.symbol.textformat');
    case 'notifications': return $r('sys.symbol.bell');
    case 'translation': return $r('sys.symbol.translate');
    case 'phone': return $r('sys.symbol.phone_doc');
    case 'privacy': return $r('sys.symbol.checkmark_shield');
    case 'managers': return Symbols.agents;
    case 'ssh': return $r('sys.symbol.link');
    case 'ledger': return $r('sys.symbol.doc_plaintext');
    case 'hub_update':
    case 'node_update': return Symbols.refresh;
    case 'diagnostics': return $r('sys.symbol.info_circle');
    case 'schedules': return Symbols.calendar;
    default: return Symbols.gear;
  }
}

function settingTint(id:string):ResourceColor {
  switch (id) {
    case 'notifications':
    case 'phone_native_alarms':
    case 'phone_health_reports':
      return Colors.danger;
    case 'appearance':
    case 'phone_language_options':
      return Colors.indigo;
    case 'models':
    case 'assistant':
    case 'standing_authorizations':
    case 'privacy':
    case 'phone_xiaoyi':
      return Colors.ai;
    case 'machines':
    case 'phone_live_screen':
    case 'ssh':
      return Colors.teal;
    case 'channels':
    case 'phone_inbox_sources':
    case 'connectors':
      return Colors.positive;
    case 'phone_meeting_options':
    case 'schedules':
    case 'phone_knowledge_selection':
    case 'memory':
    case 'phone_agent_layout':
      return Colors.working;
    default:
      return Colors.iconAccent;
  }
}

function settingArtwork(id:string):Resource|undefined {
  switch (id) {
    case 'models': return $r('app.media.phone_settings_model');
    case 'assistant': return $r('app.media.phone_settings_profile');
    case 'channels': return $r('app.media.phone_settings_channels');
    case 'connectors': return $r('app.media.phone_settings_connectors');
    case 'phone_knowledge_selection': return $r('app.media.phone_settings_knowledge');
    case 'phone_meeting_options': return $r('app.media.phone_settings_meeting');
    case 'phone_language_options': return $r('app.media.phone_settings_language');
    case 'machines': return $r('app.media.phone_settings_machines');
    case 'phone_agent_options':
    case 'phone_agent_folders':
    case 'phone_claude_config':
    case 'phone_codex_config':
    case 'phone_agy_config': return $r('app.media.phone_settings_options');
    case 'triage': return $r('app.media.phone_settings_triage');
    case 'phone_agent_followup': return $r('app.media.phone_settings_followup');
    case 'phone_agent_layout': return $r('app.media.phone_settings_list');
    default: return undefined;
  }
}

function settingLabel(id:string, label:(page:string)=>string):string {
  if (id === 'phone_agent_options') return tr('settings_agent_options_title');
  if (id === 'phone_language_options') return tr('phone_settings_language_dictation');
  if (id === 'models') return tr('phone_settings_models');
  if (id === 'assistant') return tr('phone_settings_profile');
  if (id === 'standing_authorizations') return tr('standing_auth_title');
  if (id === 'appearance') return tr('phone_settings_display');
  if (id === 'phone_meeting_options') return tr('meetings');
  if (id === 'machines') return tr('phone_machines');
  if (id==='phone_knowledge_selection') return tr(id);
  if (AGENT_OPTIONS.includes(id) && id!=='triage' || id==='phone_agent_followup' || id==='phone_agent_layout') return tr(id);
  return tr(label(id));
}

@ComponentV2
export struct SettingsDirectory {
  @Param elevated: boolean = false;
  @Param advanced:boolean = false;
  @Require @Param model: ClientModel;
  @Param ownerName: string = '';
  @Param avatar: string = '';
  @Param initialQuery: string = '';
  @Param showDismissSearch: boolean = false;
  @Local private environment:NativeEnvironment=nativeEnvironment;
  @Event onDismissSearch: () => void = () => {};
  @Local query: string = '';
  @Local meeting:MeetingPreferences=new MeetingPreferences();
  @Require @Param label: (page: string) => string;
  @Event onOpen: (page: string) => void = () => {};
  @Event onQueryChange: (query: string) => void = () => {};
  private field: TextInputController = new TextInputController();
  private mounted: boolean = false;
  private statusRevision:number=0;
  @Local hubUpdate:Record<string,Object>={};

  aboutToAppear(): void { this.mounted=true; this.query=this.initialQuery; this.meeting.open(this.model); this.readHubStatus(); }
  aboutToDisappear():void { this.mounted=false; this.statusRevision++; this.meeting.close(); }
  @Monitor('model.profile','model.features','model.connected') meetingOwnerChanged():void { this.meeting.open(this.model);this.readHubStatus(); }
  private async readHubStatus():Promise<void> {
    const owner=this.model,revision=++this.statusRevision;
    this.hubUpdate={};
    if (!this.mounted || owner.demo || !owner.connected) return;
    const epoch=owner.connectionGeneration(),scope=owner.scope();
    try {
      const update=await owner.repo().call('/api/v1/hub/update');
      if (this.mounted && revision===this.statusRevision && this.model===owner && owner.owns(epoch,scope)) this.hubUpdate=update;
    } catch (_) { /* Older hubs omit this optional status. */ }
  }

  @Computed get managers():ResourceRow[] {
    return this.model.listedManagers().filter((r:ResourceRow) => !r.isInstallingManager());
  }
  private currentManager():ResourceRow | undefined {
    return this.managers.find((r:ResourceRow) => r.id === this.model.profile.manager);
  }
  private account():string { return this.currentManager()?.title || this.ownerName || tr('persona'); }
  private subtitle(id:string):string {
    if (id==='phone_hubs') return tr(!this.model.connected ? 'offline' : this.model.ssh.phase==='connected' ? 'phone_hub_connected_ssh' : 'phone_hub_connected');
    if (id==='hub_update' || id==='phone_settings_advanced') {
      const message=phoneHubUpdateMessage(Wire.text(this.hubUpdate,'state'),Wire.bool(Wire.object(this.hubUpdate,'latest'),'newer'),Wire.text(this.hubUpdate,'channel'));
      return message ? tr(message) : id==='hub_update' ? Wire.text(this.hubUpdate,'version') : '';
    }
    if (id==='phone_meeting_options') return this.meeting.ready ? tr(settingsMeetingRoleLabel(this.meeting.role)) : '';
    if (id === 'phone_language_options') return currentLanguage(this.model);
    if (id === 'machines') {
      const profile = this.model.profile;
      const roster = this.model.directoryMachines.filter((r:ResourceRow) => r.group === profile.id && (!profile.manager || r.manager === profile.manager));
      const machines = roster.length ? roster : this.model.machines;
      if (!machines.length && !this.model.connected) return '';
      return tr('machine_online_ratio').replace('{online}',String(machines.filter((r:ResourceRow) => machineOnline(r)).length))
        .replace('{total}',String(machines.length));
    }
    return '';
  }
  @Computed get results():string[] {
    const query = this.query.trim().toLowerCase();
    if (!query) return [];
    const words = query.split(/\s+/);
    const listed=phoneSettingsSearchCatalog.filter((id:string) => {
      if (id==='phone_native_alarms' && !canIUse('SystemCapability.Notification.ReminderAgent')) return false;
      const aliases=tr('phone_search_alias_'+id);
      const terms = [settingLabel(id,this.label),aliases].join(' ').toLocaleLowerCase();
      return words.every((word:string) => terms.includes(word));
    });
    return listed.concat(LOCAL_SEARCH_ROUTES.filter((id:string)=>settingLabel(id,this.label).toLocaleLowerCase()===query));
  }
  private rows(group:string):string[] {
    const screens=this.model.machines.some((machine:ResourceRow):boolean=>{
      const data=machine.data();
      return ['computer','browser'].some((service:string):boolean=>{
        const capability=Wire.object(data,service);
        return Wire.bool(capability,'enabled') && Wire.bool(capability,'screen_sharing');
      });
    });
    return phoneSettingsRows(this.advanced,group,this.model.features,!!this.model.profile.id&&!this.model.demo,screens,canIUse('SystemCapability.Notification.ReminderAgent'));
  }
  private restoreSearchFocus(): void {
    const query=this.initialQuery, scope=this.model.scope();
    if (!this.elevated || !query.trim()) return;
    const ui=this.getUIContext();
    setTimeout(() => {
      if (!this.mounted || scope!==this.model.scope() || query!==this.query) return;
      ui.getFocusController().requestFocus('settings.search');
      this.field.caretPosition(this.query.length);
    },0);
  }
  private searchSize(regular:number,largest:number):number {
    return PhoneStyle.formSize(regular,largest,this.environment.fontScale);
  }
  private searchTextSize():string {
    return this.showDismissSearch ? String(this.searchSize(16.5,22)*Math.min(this.environment.fontScale,1))+'vp' : Tokens.interfaceFontVp(16,this.environment.fontScale);
  }
  private openEntry(id:string):void {
    this.field.stopEditing();
    this.onOpen(id);
  }
  @Builder managerMenu(scope:string) {
    Menu() {
      ForEach(this.managers,(m:ResourceRow) => {
        MenuItem({content:m.title || m.id}).selected(m.id === this.model.profile.manager).selectIcon(true)
          .onClick(() => this.model.selectListedManager(m.id,scope))
      },(m:ResourceRow) => m.id)
    }
  }
  @Builder identity() {
    Button({type:ButtonType.Normal}) { Row({space:12}) {
      Stack({alignContent:Alignment.BottomEnd}) {
        Text(this.account().slice(0,1)).fontSize('17vp').fontWeight(FontWeight.Bold).fontColor(Color.White)
          .width(38).height(38).textAlign(TextAlign.Center).backgroundColor(Colors.ai).borderRadius(Tokens.radiusCapsule)
        Circle().width(10).height(10).fill(this.model.connected ? Colors.positive : Colors.secondary)
          .stroke(Colors.card(this.elevated)).strokeWidth(2)
      }.width(38).height(38)
      Column({space:2}) {
        Text(this.currentManager() ? tr('settings_assistant_named').replace('{name}',this.account()) : tr('phone_settings_assistant'))
          .fontSize(16).maxFontScale(Tokens.interfaceScaleFor(16)).fontWeight(FontWeight.Medium).fontColor(Colors.ink).maxLines(1).textOverflow({overflow:TextOverflow.Ellipsis})
        Text(tr(this.model.connected ? 'online' : 'offline'))
          .fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).fontColor(this.model.connected ? Colors.positive : Colors.secondary).maxLines(1)
      }.layoutWeight(1).alignItems(HorizontalAlign.Start)
      if (this.currentManager()) {
        Row({space:4}) {
          Text(tr('phone_settings_switch')).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontWeight(FontWeight.Medium).fontColor(Colors.link)
          SymbolGlyph($r('sys.symbol.chevron_down')).fontSize('12vp').fontColor([Colors.tertiary]).accessibilityLevel('no')
        }
        .padding({left:10,right:8,top:5,bottom:5})
        .backgroundColor(Colors.control)
        .borderRadius(Tokens.capsule)
      }
    }.width('100%').padding({left:15,right:14,top:12,bottom:12})
      }.width('100%').constraintSize({minHeight:PhoneStyle.formSize(62,74,this.environment.fontScale)}).padding(0)
      .backgroundColor(Colors.card(this.elevated))
      .borderRadius(PhoneStyle.cardRadius)
      .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.cardBorder(this.environment.dark),radius:PhoneStyle.cardRadius})
      .shadow(PhoneStyle.cardElevation(this.environment.dark))
      .margin({bottom:12})
      .accessibilityText(tr('settings_this_assistant')+' · '+this.account())
      .bindMenu(this.managerMenu(this.model.scope())).id('settings.assistant_switch')
  }
  @Builder directoryFootnote(text:string, id:string, beforeNextCard:number=0) {
    ListItem() {
      Text(text).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontColor(Colors.secondary)
        .lineHeight(PhoneStyle.settingsExampleLineHeight(15.67,22,this.environment.fontScale))
        .width('100%').padding({left:20,right:18,top:this.searchSize(9,10),bottom:5})
        .id(id)
    }.margin({bottom:beforeNextCard})
  }
  @Builder section(title:string, entries?:string[], homeScope:string='') {
    ListItem() {
      Column() {
        if (homeScope) {
          Text(title).fontSize(17).maxFontScale(Tokens.interfaceScaleFor(17)).fontWeight(600).fontColor(Colors.secondary)
            .width('100%').constraintSize({minHeight:this.searchSize(40.33,47.67)}).textAlign(TextAlign.Start)
            .padding({left:20,right:20,top:6,bottom:6}).id('settings.group.'+homeScope)
        } else if (title) PhoneSectionTitle({text:title})
        Column() {
          ForEach(entries ?? this.results,(id:string,ix:number) => {
            PhoneSettingsRow({elevated:this.elevated,label:settingLabel(id,this.label),detail:this.subtitle(id),symbol:settingIcon(id),artwork:settingArtwork(id),tint:settingTint(id),
              identity:id==='phone_settings_advanced' ? 'settings.advanced' : 'settings.'+id,last:ix+1===(entries ?? this.results).length,action:() => this.openEntry(id)})
          },(id:string,ix:number) => JSON.stringify([id,ix,this.subtitle(id),(entries ?? this.results).length]))
          if (!(entries ?? this.results).length) Text(tr('no_results')).fontSize(16).maxFontScale(Tokens.interfaceScaleFor(16)).fontColor(Colors.secondary).width('100%').padding(15).id('settings.no_results')
        }.width('100%').clip(true).backgroundColor(Colors.card(this.elevated)).borderRadius(PhoneStyle.cardRadius)
          .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.cardBorder(this.environment.dark),radius:PhoneStyle.cardRadius})
          .shadow(PhoneStyle.cardElevation(this.environment.dark))
      }.width('100%')
    }.margin({top:homeScope === 'hub' ? 17.5 : 0})
  }

  private systemSearchMaterial():boolean {
    return this.elevated && Colors.systemControls && Colors.styleID === 'glass';
  }
  private searchGradient():LinearGradientOptions | undefined {
    if (!this.systemSearchMaterial()) return undefined;
    const height = this.searchSize(44,58);
    const highlight = $r('app.color.settings_search_highlight');
    const center = $r('app.color.settings_search_center');
    return {direction:GradientDirection.Bottom,colors:[[highlight,0],[Colors.sheetControl,10/height],
      [center,23/height],[center,1-4/height],[$r('app.color.settings_search_lower_rim'),1]]};
  }
  @Builder clearSearchButton() {
    Button({type:ButtonType.Circle}) {
      Stack() {
        Circle().width(this.searchSize(17.33,23.33)).height(this.searchSize(17.33,23.33)).fill(Colors.ink)
        PhoneOutlineIcon({name:'navigationClose',color:Colors.sheetControl,iconScale:this.searchSize(7.3,10)/17})
      }.accessibilityLevel('no')
    }.width(PhoneStyle.bar).height(PhoneStyle.bar).padding(0).backgroundColor(Color.Transparent)
      .accessibilityText(tr('clear')).onClick(()=>{
        this.query=''; this.onQueryChange(''); this.getUIContext().getFocusController().requestFocus('settings.search'); this.field.caretPosition(0);
      }).id('settings.search.clear')
  }
  build() {
    List() {
      if (!this.advanced) ListItem() {
        Row({space:this.showDismissSearch ? 11 : 8}) {
          Row({space:this.showDismissSearch ? 8 : 7}) {
          SymbolGlyph($r('sys.symbol.magnifyingglass')).fontSize(String(this.showDismissSearch ? this.searchSize(20,27.33) : 16)+'vp')
            .fontColor([this.showDismissSearch ? Colors.ink : Colors.secondary]).accessibilityLevel('no')
          TextInput({text:this.query,placeholder:tr('phone_settings_search'),controller:this.field})
            .fontSize(this.searchTextSize()).placeholderFont({size:this.searchTextSize()})
            .height(PhoneStyle.formSize(44,58,nativeEnvironment.fontScale)).padding(0)
            .layoutWeight(1).backgroundColor(Color.Transparent).cancelButton({style:this.showDismissSearch ? CancelButtonStyle.INVISIBLE : CancelButtonStyle.INPUT})
            .enterKeyType(EnterKeyType.Search).onSubmit(() => this.field.stopEditing())
            .enableKeyboardOnFocus(true).onAppear(() => this.restoreSearchFocus())
            .defaultFocus(this.elevated && this.initialQuery.trim().length>0)
            .onChange((v:string) => { this.query = v; this.onQueryChange(v); }).id('settings.search')
          if (this.showDismissSearch && this.query.length) this.clearSearchButton()
        }.padding({left:this.showDismissSearch ? 12.33 : 12,right:this.showDismissSearch && this.query.length ? this.searchSize(2.33,5.67) : 8})
          .layoutWeight(1).borderRadius(Tokens.radiusCapsule).backgroundColor(this.elevated ? Colors.sheetControl : Colors.control)
          .linearGradient(this.searchGradient())
          .outlineWidth(this.systemSearchMaterial() ? 0.33 : 0).outlineColor($r('app.color.settings_search_outline')).outlineRadius(Tokens.radiusCapsule)
          .shadow({radius:this.systemSearchMaterial() ? 4 : 0,color:$r('app.color.settings_search_shadow'),offsetY:2})
          if (this.showDismissSearch && this.query.trim()) Button({type:ButtonType.Circle}) {
            PhoneOutlineIcon({name:'navigationClose',color:Colors.ink,iconScale:this.searchSize(1.02,23.33/17)})
          }.width(PhoneStyle.formSize(44,58,nativeEnvironment.fontScale)).height(PhoneStyle.formSize(44,58,nativeEnvironment.fontScale)).padding(0).backgroundColor(Colors.card(this.elevated)).accessibilityText(tr('close'))
            .onClick(() => { this.field.stopEditing(); this.onDismissSearch(); }).id('settings.close')
        }.width('100%').margin({bottom:10})
      }
      if (this.query.trim() && !this.advanced) {
        this.section(tr('settings_search_results'))
      } else {
        if (!this.advanced) ListItem() { this.identity() }
        if (this.rows('assistant').length) this.section(this.advanced ? this.account() : '',this.rows('assistant'))
        if (!this.advanced && this.currentManager()) {
          this.directoryFootnote(tr('phone_settings_owner_note').replace('{name}',this.account()),'settings.assistant.footer')
        }
        if (this.rows('device').length) this.section(tr('phone_settings_device'),this.rows('device'),this.advanced ? '' : 'device')
        if (this.rows('hub').length) {
          this.section(tr('phone_settings_hub'),this.rows('hub'),this.advanced ? '' : 'hub')
          if (!this.advanced) this.directoryFootnote(tr('phone_settings_common_groups_footer'),'settings.hub.footer',17.5)
        }
        if (!this.advanced) {
          this.section('',['phone_settings_advanced'])
          this.directoryFootnote(tr('phone_advanced_footer'),'settings.advanced.footer')
        }
      }
    }.width('100%').height('100%').padding({left:PhoneStyle.page,right:PhoneStyle.page,top:this.advanced ? 12 : this.showDismissSearch && this.query.trim() ? 16 : 0,bottom:24})
      .backgroundColor(Colors.listGroup(this.elevated)).id(this.advanced ? 'settings.advanced.list' : 'settings.directory')
  }
}

@ComponentV2
export struct PhoneSettingsOptions {
  @Param elevated: boolean = false;
  @Local dictation: DictationPreference = dictationPreference;
  @Local environment: NativeEnvironment = nativeEnvironment;
  @Require @Param model:ClientModel;
  @Require @Param page:string;
  @Require @Param label:(page:string)=>string;
  @Event onOpen:(page:string)=>void = () => {};
  private mounted:boolean = false;

  @Computed get inlinePickerScale(): number {
    return PhoneStyle.formSize(1,1.35,this.environment.fontScale);
  }

  aboutToAppear():void { this.mounted = true; this.dictation.load(this.getUIContext().getHostContext()); }
  private dictationLabel(choice: string): string {
    return choice === 'zh-CN' ? languageName(choice) : tr(choice === 'keyboard' ? 'phone_system_input' : 'auto');
  }
  @Builder dictationMenu() {
    Menu() {
      ForEach(['auto','zh-CN','keyboard'], (choice: string) => {
        MenuItem({content: this.dictationLabel(choice)}).selected(this.dictation.choice === choice).selectIcon(true)
          .enabled(!this.dictation.saving).onClick(() => {
            Haptics.selection();
            this.dictation.choose(choice).then((saved: boolean) => {
              if (!saved && this.mounted) toastCenter.error(tr('cache_unavailable'));
            });
          }).id('settings.dictation.' + choice)
      }, (choice: string) => choice)
      MenuItem({content: tr('phone_keyboard_settings')}).onClick(() => {
        inputMethod.getSetting().showOptionalInputMethods().catch(() => {
          if (this.mounted) toastCenter.error(tr('failed'));
        });
      }).id('settings.dictation.system')
    }
  }
  aboutToDisappear():void { this.mounted = false; }
  private choose(language:string):void {
    const owner=this.model;
    if (language===owner.language || owner.languageChanging) return;
    Haptics.selection();
    const scope=owner.scope(), generation=owner.connectionGeneration();
    owner.setLanguage(language).then(() => {
      if (this.mounted && this.model===owner && owner.owns(generation,scope) && language==='default' && owner.language==='default')
        toastCenter.info(tr('language_after_restart'));
    }).catch(() => {});
  }
  build() {
    List() {
      if (this.page === 'phone_agent_options') {
        PhoneDefaultPermissionSection({model:this.model,elevated:this.elevated,emptyInset:17.5})
        ListItem() {
        Column() {
          ForEach(AGENT_OPTIONS,(id:string,ix:number) => {
            PhoneSettingsRow({elevated:this.elevated,insetGrouped:true,label:settingLabel(id,this.label),symbol:settingIcon(id),artwork:settingArtwork(id),tint:settingTint(id),
              identity:'settings.'+id,last:ix === AGENT_OPTIONS.length-1,action:() => this.onOpen(id)})
          },(id:string) => id)
        }.width('100%').clip(true).backgroundColor(Colors.card(this.elevated)).borderRadius(PhoneStyle.settingsGroupRadius)
          .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.cardBorder(this.environment.dark),radius:PhoneStyle.settingsGroupRadius})
          .shadow(PhoneStyle.cardElevation(this.environment.dark))
        }.margin({top:17.5})
        ListItem() { Text(tr('phone_agent_tools_note')).fontSize(Tokens.caption).maxFontScale(Tokens.interfaceScaleFor(Tokens.caption))
          .lineHeight(PhoneStyle.settingsExampleLineHeight(15.67,22,this.environment.fontScale)).fontColor(PhoneStyle.settingsSecondary)
          .width('100%').padding({left:20,right:20,top:9,bottom:5}) }
      } else {
        ListItem() {
        Column() {
          ForEach(LANGUAGES,(lang:string,ix:number) => {
            Row({space:7}) {
              Text(lang==='default' ? tr('phone_settings_system_language').replace('{language}',languageName(i18n.getSystemLanguage())) : languageName(lang))
                .fontSize(17).maxFontScale(Tokens.interfaceScaleFor(17)).fontColor(Colors.ink).layoutWeight(1)
              if (this.model.language===lang) {
                Stack() {
                  Stack() { PhoneOutlineIcon({name:'inlinePickerCheck',color:Colors.link}) }
                    .width(15).height(14.67).scale({x:this.inlinePickerScale,y:this.inlinePickerScale})
                    .hitTestBehavior(HitTestMode.None).accessibilityLevel('no')
                }.width(15*this.inlinePickerScale).height(14.67*this.inlinePickerScale)
                  .margin({right:4.33*this.inlinePickerScale}).flexShrink(0)
              }
            }.width('100%').constraintSize({minHeight:PhoneStyle.groupedRowHeight(this.environment.fontScale)}).padding({left:20,right:20,top:6,bottom:6})
              .enabled(!this.model.languageChanging).accessibilitySelected(this.model.language===lang).accessibilityGroup(true)
              .onClick(() => this.choose(lang)).id('settings.language.'+lang)
            if (ix<LANGUAGES.length-1) Divider().strokeWidth(0.5).color(Colors.divider).margin({left:20})
          },(lang:string) => lang)
        }.width('100%').clip(true).backgroundColor(Colors.card(this.elevated)).borderRadius(PhoneStyle.cardRadius)
          .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.cardBorder(this.environment.dark),radius:PhoneStyle.cardRadius})
          .shadow(PhoneStyle.cardElevation(this.environment.dark))
        }
        ListItem() { Text(tr('phone_language_system_note')).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontColor(Colors.secondary).width('100%').padding({left:20,right:20,top:7,bottom:20}) }
        ListItem() {
          Row({space:8}) {
            Text(tr('phone_dictation')).fontSize(16).maxFontScale(Tokens.interfaceScaleFor(16)).fontColor(Colors.ink).layoutWeight(1)
            Text(this.dictationLabel(this.dictation.choice)).fontSize(15).maxFontScale(Tokens.interfaceScaleFor(15)).fontColor(Colors.link)
            SymbolGlyph(Symbols.expand).fontSize('12vp').fontColor([Colors.secondary])
          }.constraintSize({minHeight:PhoneStyle.groupedRowHeight(this.environment.fontScale)}).width('100%').padding({left:20,right:20,top:7,bottom:7})
            .backgroundColor(Colors.card(this.elevated)).borderRadius(PhoneStyle.cardRadius)
            .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.cardBorder(this.environment.dark),radius:PhoneStyle.cardRadius})
            .shadow(PhoneStyle.cardElevation(this.environment.dark))
            .accessibilityGroup(true)
            .bindMenu(this.dictationMenu()).accessibilityRole(AccessibilityRoleType.BUTTON).id('settings.dictation.choice')
        }
        ListItem() { Text(tr('phone_dictation_automatic_note')).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontColor(Colors.secondary).width('100%').padding({left:20,right:20,top:7,bottom:10}) }
      }
    }.width('100%').height('100%').padding({left:16,right:16,top:this.page==='phone_agent_options' ? 0 : 35,bottom:24})
      .backgroundColor(Colors.listGroup(this.elevated))
  }
}
