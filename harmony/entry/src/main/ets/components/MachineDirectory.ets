import { i18n } from '@kit.LocalizationKit';
import { relativeActivityDate } from '../core/PhoneDate';
import { LengthMetrics } from '@kit.ArkUI';
import { Colors } from '../design/Colors';
import { PhoneStyle } from '../design/PhoneStyle';
import { Motion } from '../design/Motion';
import { PhoneOutlineIcon } from '../design/PhoneOutlineIcon';
import { Symbols } from '../design/Symbols';
import { tr } from '../design/Strings';
import { Tokens } from '../design/Tokens';
import { Haptics } from '../platform/Haptics';
import { NeedsYouPulseDot } from './StatusMotionMarks';
import { workspaceGridColumns, workspaceGridGutter, workspaceHorizontalPadding } from '../core/WorkspaceGrid';
import { ClientModel } from '../models/ClientModel';
import { HubProfile, ResourceRow, Wire } from '../models/Wire';
import { DashboardPoint, MachineAgent, MachineAlert, MachineReading, formatMachineLoad, machineAgents, machineAlerts, machineBytes,
  machineConcern, machineAgentTimeBeside, machineKey, machineOnline, machinePending, machinePercent, machineTokens, machineTokenTotal,
  dashboardLabelTicks, dashboardTickLeft, machineTrace, machineAvatarSymbol, osFamily, percentOf } from '../models/MachineDashboard';
import { nativeEnvironment } from '../platform/NativeEnvironment';
import { MachineChart } from './MachineChart';
import { MachineSubscriptionUsage,SubscriptionAccount } from '../models/MachineSubscriptionUsage';
import { MachineSubscriptionCards } from './MachineSubscriptionCards';

const MARKS: Record<string,Resource> = {
  'macos':$r('app.media.machine_os_macos'), 'ubuntu':$r('app.media.machine_os_ubuntu'), 'debian':$r('app.media.machine_os_debian'),
  'fedora':$r('app.media.machine_os_fedora'), 'redhat':$r('app.media.machine_os_redhat'), 'centos':$r('app.media.machine_os_centos'),
  'rocky':$r('app.media.machine_os_rocky'), 'alma':$r('app.media.machine_os_alma'), 'arch':$r('app.media.machine_os_arch'),
  'alpine':$r('app.media.machine_os_alpine'), 'opensuse':$r('app.media.machine_os_opensuse'), 'nixos':$r('app.media.machine_os_nixos'),
  'linux':$r('app.media.machine_os_linux')
};
export function machineBrandMark(row:ResourceRow):Resource|undefined {
  const data = row.data();
  return MARKS[osFamily(Wire.object(data,'system'),Wire.text(data,'os'))];
}

// OS distro brand identity colors (light/dark pairs) for Linux/macOS computer badges.
const MARK_COLORS: Record<string,string[]> = {
  'ubuntu':['#E95420','#F26B3A'], 'debian':['#A80030','#E0245E'], 'fedora':['#3C6EB4','#51A2DA'], 'redhat':['#CC0000','#FF4D4D'],
  'centos':['#932279','#C14FA8'], 'rocky':['#10B981','#34D399'], 'alma':['#0F4266','#6CA6D9'], 'arch':['#1793D1','#3BAEE8'],
  'alpine':['#0D597F','#5AA9D6'], 'opensuse':['#4E8A1E','#73BA25'], 'nixos':['#5277C3','#7EBAE4']
};
const RANGES: string[] = ['1h','6h','24h'];
const RANGE_LABELS: string[] = ['machine_hour','machine_six_hours','machine_day'];
const RANGE_SECONDS: number[] = [3600,21600,86400];
const RANGE_STEPS: number[] = [10,60,240];
const HISTORY_FIELDS: string[] = ['cpu','mem','network','load'];
// 7-char #RRGGBB hex strings required by MachineChart CanvasRenderingContext2D gradient stops.
const FIELD_TINT: Record<string,string[]> = {
  'cpu':['#2A78D6','#3987E5'], 'mem':['#1BAF7A','#199E70'], 'network':['#EB6834','#D95926'],
  'upload':['#6250D6','#9085E9'], 'tokens':['#2C81DF','#4C98EC'], 'load':['#E87BA4','#D55181']
};
const NEUTRAL_SHADES: number[] = [0.86,0.66,0.46,0.28,0.20];

class HistorySeries {
  readonly points:DashboardPoint[];
  readonly ceiling:number;
  constructor(points:DashboardPoint[],ceiling:number) { this.points = points; this.ceiling = ceiling; }
}
const EMPTY_SERIES:HistorySeries = new HistorySeries([],1);

class LiveMetric {
  readonly key:string;
  readonly title:string;
  readonly value:string;
  readonly detail:string;
  readonly percent:number;
  readonly tint:ResourceColor;
  readonly alert:string;
  constructor(key:string,title:string,value:string,detail:string,percent:number,tint:ResourceColor,alert:string = '') {
    this.key = key; this.title = title; this.value = value; this.detail = detail;
    this.percent = percent; this.tint = tint; this.alert = alert;
  }
}

class ComputerCard {
  readonly key: string;
  readonly row: ResourceRow;
  readonly raw: Record<string,Object>;
  readonly system: Record<string,Object>;
  readonly sample?: MachineReading;
  readonly family: string;
  readonly avatarSymbol: string;
  readonly online: boolean;
  readonly pending: boolean;
  readonly agents: MachineAgent[];
  readonly alerts: MachineAlert[];
  readonly needs: number;
  readonly working: number;
  readonly idle: number;
  readonly priority: number;
  readonly tokens: number;
  readonly stamp: string;
  constructor(row:ResourceRow, sessions:ResourceRow[], sample?:MachineReading,pagedOrder:boolean=false) {
    this.row = row; this.raw = row.data(); this.sample = sample;
    this.key = machineKey(row.group,row.manager,row.id);
    this.system = sample && Object.keys(sample.system).length ? sample.system : Wire.object(this.raw,'system');
    this.online = machineOnline(row); this.pending = machinePending(this.raw);
    this.family = osFamily(this.system, Wire.text(this.raw,'os'));
    this.avatarSymbol = machineAvatarSymbol(Wire.text(this.raw,'icon'));
    this.agents = machineAgents(sessions,pagedOrder); this.alerts = this.online ? machineAlerts(sample,this.system) : [];
    this.needs = this.agents.filter((a:MachineAgent) => a.counted === 'needs_you').length;
    this.working = this.agents.filter((a:MachineAgent) => a.counted === 'working').length;
    this.idle = this.agents.length - this.needs - this.working;
    this.priority = machineConcern(this.raw,this.online,this.alerts,this.needs);
    this.tokens = sample ? machineTokenTotal(Wire.object(sample.usage,'today')) : 0;
    this.stamp = JSON.stringify([this.key,row.raw,sample?.latest,sample?.usage,this.needs,this.working,this.idle]);
  }
}

function clockLabel(time:number):string {
  const date = new Date(time*1000);
  return String(date.getHours()).padStart(2,'0')+':'+String(date.getMinutes()).padStart(2,'0');
}

@ComponentV2
struct MachineTimeAxis {
  @Require @Param end:number;
  @Require @Param seconds:number;
  @Local private axisWidth:number = 0;
  @Local private labelWidth:number = 0;

  @Computed get ticks():number[] { return dashboardLabelTicks(this.end,this.seconds,this.axisWidth,this.labelWidth); }
  build() {
    Stack({alignContent:Alignment.TopStart}) {
      // This native label reserves a full line and measures the actual scaled, tabular clock face.
      Text('00:00').fontSize(10).maxFontScale(Tokens.interfaceScaleFor(10)).fontFeature('tnum 1').visibility(Visibility.Hidden)
        .onAreaChange((_:Area,area:Area) => { this.labelWidth = Math.ceil(Number(area.width)); })
      if (this.ticks.length) {
        ForEach(this.ticks,(time:number) => {
          Text(clockLabel(time)).fontSize(10).maxFontScale(Tokens.interfaceScaleFor(10)).fontFeature('tnum 1').fontColor(Colors.secondary)
            .position({x:dashboardTickLeft(time,this.end,this.seconds,this.axisWidth,this.labelWidth),y:0})
        },(time:number) => String(time))
      } else {
        Flex({wrap:FlexWrap.Wrap,justifyContent:FlexAlign.SpaceBetween}) {
          Text(clockLabel(this.end-this.seconds)).fontSize(10).maxFontScale(Tokens.interfaceScaleFor(10)).fontFeature('tnum 1').fontColor(Colors.secondary)
          Text(clockLabel(this.end)).fontSize(10).maxFontScale(Tokens.interfaceScaleFor(10)).fontFeature('tnum 1').fontColor(Colors.secondary)
        }.width('100%')
      }
    }.width('100%').accessibilityLevel('no-hide-descendants')
      .onAreaChange((_:Area,area:Area) => { this.axisWidth = Number(area.width); })
  }
}

@ComponentV2
export struct MachineDirectory {
  @Local liveGridWidth:number=0;
  @Local private paneWidth: number = 0;
  @Local private agentContentWidth: number = 0;
  @Require @Param model: ClientModel;
  @Param active: boolean = true;
  @Param settingsScope: boolean = false;
  @Param settingsHeading: boolean = false;
  @Param selected: ResourceRow | undefined = undefined;
  @Event onManage: () => void = () => {};
  @Event onMachine: (row: ResourceRow) => void = () => {};
  @Event onDetails: (row: ResourceRow) => void = () => {};
  @Event onSession: (row: ResourceRow) => void = () => {};
  @Local private readings: Map<string,MachineReading> = new Map();
  @Local private subscriptionUsage: MachineSubscriptionUsage = new MachineSubscriptionUsage();
  @Local private detailSample: MachineReading | undefined = undefined;
  @Local private chartEnd: number = Math.floor(Date.now()/1000);
  @Local private loading: boolean = false;
  @Local private failed: boolean = false;
  @Local private refreshing: boolean = false;
  @Local private expanded: boolean = false;
  @Local private range: number = 0;
  @Local private shownRange: number = 0;
  @Local private inspected: number = 0;
  private revision: number = 0;
  private timer: number = -1;
  private bound: string = '';

  aboutToAppear(): void { this.sync(true); }
  aboutToDisappear(): void { this.stop(); this.subscriptionUsage.cancel(); }
  @Monitor('active','settingsScope','model.profile','model.connected')
  contextChanged(): void { this.sync(true); }
  @Monitor('selected') selectedChanged(): void {
    if (this.bound!==JSON.stringify([this.model.scope(),this.selected?.key()])) this.sync(true);
  }

  private stop(): void {
    this.revision++;
    if (this.timer !== -1) clearTimeout(this.timer);
    this.timer = -1; this.loading = false; this.refreshing = false;
  }
  private sync(roster:boolean):void {
    this.stop();
    this.subscriptionUsage.retainOwners(this.model);
    if (!this.active || this.selected) this.subscriptionUsage.cancel();
    const identity = JSON.stringify([this.model.scope(),this.selected?.key()]);
    if (this.bound !== identity) {
      this.bound = identity; this.readings = new Map(); this.detailSample = undefined; this.expanded = false;
      this.inspected = 0; this.range = 0; this.shownRange = 0; this.failed = false;
    }
    if (this.active && this.model.connected) this.load(this.revision,roster);
  }
  private async load(revision:number, roster:boolean):Promise<void> {
    const root = this.model, epoch = root.connectionGeneration(), scope = root.scope();
    const live = ():boolean => this.revision === revision && this.active && root.owns(epoch,scope);
    this.loading = true;
    try {
      if (roster) {
        try { await root.refreshDirectory(); } catch (_) { /* The known roster can still be rendered. */ }
      }
      if (!live()) return;
      if (!this.selected) this.subscriptionUsage.refresh(root,roster).catch(()=>{});
      if (this.selected) {
        const target = this.selected, owner = root.directoryOwnerFor(target), range = this.range;
        if (!owner?.connected) { this.failed = true; return; }
        const ownerEpoch = owner.connectionGeneration(), ownerScope = owner.scope();
        const answer = await owner.repo().call('/api/v1/machines/'+encodeURIComponent(target.id)+'/metrics?range='+RANGES[range]);
        if (!live() || !owner.owns(ownerEpoch,ownerScope) || range !== this.range) return;
        const value = Wire.object(answer,'metrics');
        if (Wire.text(value,'machine_id') !== target.id) { this.failed = true; return; }
        this.detailSample = new MachineReading(value); this.shownRange = range; this.failed = false;
      } else {
        const next = new Map<string,MachineReading>();
        const visible = new Set<string>();
        for (const row of this.visibleRows) {
          const key = machineKey(row.group,row.manager,row.id);
          visible.add(key);
          const lastGood = this.readings.get(key);
          if (lastGood) next.set(key,lastGood);
        }
        const owners = root.directoryOwners().filter((o:ClientModel) => o.connected &&
          this.visibleRows.some((r:ResourceRow) => r.group === o.profile.id && r.manager === o.profile.manager));
        await Promise.all(owners.map(async (owner:ClientModel):Promise<void> => {
          const ownerEpoch = owner.connectionGeneration(), ownerScope = owner.scope();
          try {
            const response = await owner.repo().call('/api/v1/metrics');
            if (!live() || !owner.owns(ownerEpoch,ownerScope) || !Array.isArray(response['machines'])) return;
            for (const row of this.visibleRows) {
              if (row.group === owner.profile.id && row.manager === owner.profile.manager)
                next.delete(machineKey(row.group,row.manager,row.id));
            }
            for (const info of Wire.rows(response,'machines')) {
              const id = Wire.text(info,'machine_id');
              const key = machineKey(owner.profile.id,owner.profile.manager,id);
              if (visible.has(key)) next.set(key,new MachineReading(info));
            }
          } catch (_) { /* Monitoring is optional per Hub. Keep the registered machine visible. */ }
        }));
        if (!live()) return;
        this.readings = next; this.failed = false;
      }
      this.chartEnd = Math.floor(Date.now()/1000);
      if (this.inspected && this.inspected < this.chartEnd-RANGE_SECONDS[this.range]) this.inspected = 0;
    } catch (_) { if (live()) this.failed = true; }
    finally {
      if (live()) {
        this.loading = false; this.refreshing = false;
        this.timer = setTimeout(() => this.load(revision,false),this.selected && this.range > 0 ? 60000 : 10000);
      }
    }
  }
  private chooseRange(index:number):void {
    if (index === this.range || !RANGES[index]) return;
    this.range = index; this.failed = false; this.inspected = 0; this.sync(false);
  }
  @Computed get visibleRows(): ResourceRow[] {
    const profile = this.model.profile;
    return this.model.directoryMachines.filter((r:ResourceRow) => {
      if (this.selected) return r.group === this.selected.group && r.manager === this.selected.manager && r.id === this.selected.id;
      if (r.group !== profile.id) return !this.settingsScope;
      return !profile.manager || (!this.settingsScope && this.model.managers.length <= 1) || r.manager === profile.manager;
    });
  }
  @Computed get cards(): ComputerCard[] {
    const sessions = new Map<string,ResourceRow[]>();
    for (const s of this.model.directorySessions) {
      const key = machineKey(s.group,s.manager,s.machine);
      const list = sessions.get(key);
      if (list) list.push(s); else sessions.set(key,[s]);
    }
    return this.visibleRows.map((r:ResourceRow) => {
      const key = machineKey(r.group,r.manager,r.id);
      const owner=this.model.directoryOwnerFor(r);
      return new ComputerCard(r,sessions.get(key) || [],this.selected ? this.detailSample : this.readings.get(key),owner?.features.includes('sessions_list')===true);
    }).sort((a:ComputerCard,b:ComputerCard) => a.priority-b.priority);
  }
  @Computed get shares(): ComputerCard[] {
    return this.cards.filter((c:ComputerCard) => !!c.sample && Object.keys(Wire.object(c.sample.usage,'today')).length > 0)
      .sort((a:ComputerCard,b:ComputerCard) => b.tokens-a.tokens);
  }
  @Computed get total(): number { return this.shares.reduce((t:number,c:ComputerCard) => t+c.tokens,0); }
  @Computed get groupIDs():string[] {
    const ids = new Set<string>(this.cards.map((c:ComputerCard) => c.row.group || this.model.profile.id));
    return this.model.profiles.map((profile:HubProfile) => profile.id).filter((id:string) => ids.has(id));
  }
  private groupCards(group:string):ComputerCard[] {
    return this.cards.filter((c:ComputerCard) => (c.row.group || this.model.profile.id) === group);
  }

  private machineColumns(): number {
    return workspaceGridColumns(this.paneWidth, nativeEnvironment.fontScale, 3);
  }
  private machineSidePadding(): number {
    return workspaceHorizontalPadding(this.paneWidth);
  }
  private tint(key:string):string { return FIELD_TINT[key][nativeEnvironment.dark ? 1 : 0]; }

  private offlineManager(c:ComputerCard):string {
    const owner = this.model.directoryOwnerFor(c.row);
    if (!owner || !c.row.manager) return '';
    const manager = owner.managers.find((r:ResourceRow) => r.id === c.row.manager);
    return manager && Wire.has(manager.data(),'online') && !Wire.bool(manager.data(),'online') ? manager.title || manager.id : '';
  }
  private reason(c:ComputerCard):string {
    const manager = this.offlineManager(c);
    if (!c.online && manager) return tr('machine_problem_manager').replace('{name}',manager);
    const key = Wire.text(c.raw,'error_code'), raw = Wire.text(c.raw,'error');
    const messages:Record<string,string> = {'node_service_down':'machine_problem_service','link_offline':'machine_problem_link',
      'never_connected':'machine_problem_never','node_outdated':'machine_problem_version'};
    if (messages[key]) return tr(messages[key]);
    if (/timed? ?out|timeout/i.test(raw)) return tr('machine_problem_timeout');
    return raw;
  }
  private status(c:ComputerCard):string {
    if (c.pending) return tr('machine_pending');
    if (!c.online && Wire.text(c.raw,'error_code') === 'never_connected') return tr('machine_never');
    return tr(c.online ? 'machine_status_online' : 'offline');
  }
  private hardware(c:ComputerCard):string {
    let name = Wire.text(c.system,'name');
    if (!name) {
      const os = Wire.text(c.system,'os') || Wire.text(c.raw,'os');
      name = (c.family === 'macos' ? 'macOS' : os) + (Wire.text(c.system,'version') ? ' '+Wire.text(c.system,'version') : '');
    }
    const cores = Wire.number(c.system,'cores'), memory = Wire.number(c.system,'memory');
    return [name,cores ? tr('machine_cpu_cores').replace('{count}',String(cores)) : '',memory ? machineBytes(memory) : '']
      .filter((v:string) => !!v).join(' · ') || Wire.text(c.raw,'hostname') || Wire.text(c.raw,'ssh_host') || c.row.id;
  }
  private osColor(c:ComputerCard): ResourceColor {
    if (!c.online) return Colors.secondary;
    if (c.family === 'macos') return Colors.ink;
    return MARK_COLORS[c.family]?.[nativeEnvironment.dark ? 1 : 0] || Colors.link;
  }
  @Builder osMark(c:ComputerCard) {
    Stack() {
      Rect().width(42).height(42).radius(11).fill(this.osColor(c)).opacity(0.13)
      if (MARKS[c.family]) Image(MARKS[c.family]).width(23.5).height(23.5).objectFit(ImageFit.Contain)
        .supportSvg2(true).colorFilter(this.osColor(c))
      else SymbolGlyph(Symbols.device).fontSize('23vp').fontColor([this.osColor(c)])
    }.width(42).height(42).accessibilityLevel('no')
  }
  @Builder count(label:string,n:number,tint:ResourceColor,dot:ResourceColor) {
    Row({space:6}) {
      Circle().width(PhoneStyle.sidebarMetric(7,nativeEnvironment.fontScale)).height(PhoneStyle.sidebarMetric(7,nativeEnvironment.fontScale))
        .fill(n ? dot : Colors.tertiary)
      Text(String(n)).fontSize(Tokens.body).maxFontScale(Tokens.interfaceFontMaxScale).fontWeight(FontWeight.Medium).fontColor(n ? tint : Colors.secondary)
      Text(label).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).fontColor(Colors.secondary).maxLines(1)
    }.constraintSize({minWidth:'30%'}).flexGrow(1).flexShrink(0)
  }
  @Builder tag(a:MachineAlert) {
    Row({space:Tokens.spaceXs}) {
      SymbolGlyph(a.severity === 2 ? $r('sys.symbol.exclamationmark_circle_fill') : $r('sys.symbol.exclamationmark_triangle_fill')).fontSize('12vp').fontColor([a.severity === 2 ? Colors.danger : Colors.working])
      Text(tr(a.key)).fontSize(Tokens.typeLabel).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeLabel)).fontWeight(FontWeight.Medium).fontColor(Colors.ink)
    }.padding({left:Tokens.spaceS,right:Tokens.spaceS,top:Tokens.spaceXs,bottom:Tokens.spaceXs}).borderRadius(Tokens.radiusCapsule)
      .backgroundColor(a.severity === 2 ? Colors.dangerWash : Colors.workingWash)
  }
  @Builder summary(c:ComputerCard, selected:boolean) {
    Column({space:Tokens.inset}) {
      Row({space:Tokens.inset}) {
        this.osMark(c)
        Column({space:2}) {
          if (!selected) Text(c.row.title || c.row.id).fontSize(17).maxFontScale(Tokens.interfaceFontMaxScale).lineHeight(this.overviewTextHeight(20.33,27.67)).fontWeight(FontWeight.Bold).fontColor(Colors.ink).maxLines(1)
          Text(this.hardware(c)).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).lineHeight(selected ? undefined : this.overviewTextHeight(14.33,21.67)).fontColor(Colors.secondary).maxLines(1).textOverflow({overflow:TextOverflow.Ellipsis})
          if (selected && c.online && c.sample && Wire.has(c.sample.latest,'load'))
            Text(tr('machine_load')+' '+formatMachineLoad(Wire.number(c.sample.latest,'load',-1),Wire.number(c.system,'cores'),tr('machine_load_per_core')))
              .fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).fontColor(Colors.secondary).maxLines(1).textOverflow({overflow:TextOverflow.Ellipsis})
        }.layoutWeight(1).alignItems(HorizontalAlign.Start)
        Column({space:2}) {
          Row({space:4}) {
            if (c.online && !c.pending) NeedsYouPulseDot({dotSize:6,tint:Colors.positive,surface:Colors.card(this.selected !== undefined),bordered:false})
            Text(this.status(c)).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).lineHeight(selected ? undefined : this.overviewTextHeight(14.33,21.67)).fontWeight(FontWeight.Medium).fontColor(c.pending ? Colors.working : c.online ? Colors.positive : Colors.secondary)
          }.alignItems(VerticalAlign.Center)
          if (!c.online && this.ageLabel(Date.parse(Wire.text(c.raw,'last_seen')))) Text(this.ageLabel(Date.parse(Wire.text(c.raw,'last_seen'))))
            .fontSize(selected ? 11 : 12).maxFontScale(Tokens.interfaceScaleFor(selected ? 11 : 12))
            .lineHeight(selected ? undefined : this.overviewTextHeight(14.33,21.67)).fontColor(selected ? Colors.secondary : Colors.tertiary)
        }.alignItems(HorizontalAlign.End)
      }.width('100%')
      if (c.alerts.length > 0) Flex({wrap:FlexWrap.Wrap,space:{main:LengthMetrics.vp(6),cross:LengthMetrics.vp(3)}}) {
        ForEach(c.alerts,(a:MachineAlert) => { this.tag(a) },(a:MachineAlert) => a.key+String(a.severity))
      }.width('100%')
      if (c.online) Flex({wrap:FlexWrap.Wrap,space:{main:LengthMetrics.vp(8),cross:LengthMetrics.vp(4)}}) {
        this.count(tr('needs_you'),c.needs,Colors.needs,Colors.needsMark)
        this.count(tr('working'),c.working,Colors.working,Colors.workingMark)
        this.count(tr('machine_agent_idle'),c.idle,Colors.ink,Colors.tertiary)
      }.width('100%').padding({left:12,right:12,top:8,bottom:8}).borderRadius(Tokens.radiusControl).backgroundColor($r('app.color.machine_count_surface'))
      else if (this.reason(c)) Column({space:selected ? 4 : 2}) {
        Text(this.reason(c)).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).lineHeight(selected ? undefined : this.overviewTextHeight(14.33,21.67))
          .lineSpacing(LengthMetrics.vp(selected ? 0 : 2),{onlyBetweenLines:true}).fontColor(Colors.secondary).maxLines(selected ? 6 : 3)
        if (Wire.text(c.raw,'error') && Wire.text(c.raw,'error') !== this.reason(c))
          Text(Wire.text(c.raw,'error')).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).lineHeight(selected ? undefined : this.overviewTextHeight(15.67,23)).fontColor(Colors.tertiary).maxLines(selected ? 7 : 1).textOverflow({overflow:TextOverflow.Ellipsis})
      }.width('100%').alignItems(HorizontalAlign.Start)
      if (Wire.bool(c.raw,'node_mismatch') || Wire.bool(c.raw,'deploying')) Text(tr(Wire.bool(c.raw,'deploying') ? 'admin_node_deploying' : 'machine_update_needed'))
        .fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontColor(Colors.working)
      if (!selected && c.online && c.sample && Object.keys(c.sample.latest).length) this.metrics(c.sample,Wire.number(c.system,'cores'))
    }.width('100%').padding(14).alignItems(HorizontalAlign.Start).backgroundColor(Colors.card(this.selected !== undefined)).borderRadius(PhoneStyle.cardRadius)
      .border({width:c.alerts.length ? 0.8 : PhoneStyle.surfaceEdgeWidth(0.5),
        color:c.alerts.length ? (c.alerts[0]?.severity === 2 ? Colors.danger : Colors.working) : PhoneStyle.cardBorder(nativeEnvironment.dark),
        radius:PhoneStyle.cardRadius})
      .shadow(PhoneStyle.cardElevation(nativeEnvironment.dark))
      .onClick(() => { Haptics.tap(); if (selected) this.onDetails(c.row); else this.onMachine(c.row); })
      .accessibilityGroup(true).accessibilityText([c.row.title,this.status(c),this.hardware(c),c.online ? [c.needs+' '+tr('needs_you'),c.working+' '+tr('working'),c.idle+' '+tr('machine_agent_idle')].join(' · ') : this.reason(c),
        ...c.alerts.map((a:MachineAlert) => tr(a.key))].filter((v:string) => !!v).join(' · ')).id(selected ? 'machines.selected' : 'machines.row')
  }

  private ageLabel(stamp: number): string {
    const locale = this.model.language === 'default' ? i18n.getSystemLanguage() : this.model.language;
    return relativeActivityDate(stamp, this.chartEnd * 1000, locale, tr('agent_time_just_now'), tr('kb_minutes_ago'), tr('kb_yesterday'));
  }
  private historyTitle(key: string): string {
    if (key !== 'mem') return this.label(key);
    const sample = this.detailSample;
    const total = sample ? Wire.number(sample.latest, 'mem_total', Wire.number(sample.system, 'memory')) : 0;
    const bytes = total || Wire.number(this.cards[0]?.system || {}, 'memory');
    return bytes > 0 ? this.label(key) + ' (' + tr('machine_total_bytes').replace('{size}', machineBytes(bytes)) + ')' : this.label(key);
  }
  private label(key:string):string { return key === 'cpu' ? 'CPU' : key === 'tokens' ? 'Tokens' : tr(key === 'mem' ? 'phone_ram' : key === 'network' ? 'phone_network' : 'machine_load'); }
  private present(key:string,n:number):string {
    if (n < 0) return '—';
    if (key === 'cpu' || key === 'mem') return machinePercent(n);
    if (key === 'network') return machineBytes(n)+'/s';
    if (key === 'tokens') return tr('machine_per_minute').replace('{value}',machineTokens(n));
    return n.toFixed(n >= 10 ? 1 : 2);
  }
  private latest(m:MachineReading,key:string):number {
    if (key === 'mem') return percentOf(Wire.number(m.latest,'mem_used',-1),Wire.number(m.latest,'mem_total'));
    if (key === 'network') return Wire.number(m.latest,'net_rx',-1);
    if (key === 'tokens') return Wire.number(m.usage,'per_minute',-1);
    return Wire.number(m.latest,key,-1);
  }
  private metricDetail(m:MachineReading,key:string,cores:number = Wire.number(m.system,'cores')):string {
    if (key === 'cpu') return Wire.has(m.latest,'load') ?
      formatMachineLoad(Wire.number(m.latest,'load',-1),cores,tr('machine_load_per_core')) : '';
    if (key === 'mem') return Wire.has(m.latest,'mem_used') && Wire.number(m.latest,'mem_total') > 0 ?
      machineBytes(Wire.number(m.latest,'mem_used'))+' / '+machineBytes(Wire.number(m.latest,'mem_total')) : '';
    if (key === 'network') return Wire.has(m.latest,'net_tx') ? '↑ '+this.present('network',Wire.number(m.latest,'net_tx',-1)) : '';
    return Object.keys(Wire.object(m.usage,'today')).length ? tr('machine_today')+' '+machineTokens(machineTokenTotal(Wire.object(m.usage,'today'))) : '';
  }
  @Builder metricCell(m:MachineReading,key:string,cores:number = Wire.number(m.system,'cores')) {
    Column({space:4}) {
      Row({space:3}) {
        Text(this.label(key)).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).fontColor(Colors.secondary).maxLines(1)
        if (key === 'cpu' && this.latest(m,key) >= 75 || key === 'mem' && this.latest(m,key) >= 80)
          SymbolGlyph($r('sys.symbol.exclamationmark_circle')).fontSize('10vp').fontColor([this.latest(m,key) >= 90 ? Colors.danger : Colors.working])
      }
      Text((key === 'network' ? '↓ ' : '') + this.present(key,this.latest(m,key))).fontSize(Tokens.body).maxFontScale(Tokens.interfaceFontMaxScale)
        .minFontSize(Tokens.interfaceFontVp(12,nativeEnvironment.fontScale)).maxFontSize(Tokens.interfaceFontVp(Tokens.body,nativeEnvironment.fontScale))
        .fontWeight(FontWeight.Bold).fontColor(Colors.ink).maxLines(1)
      Text(this.metricDetail(m,key,cores) || ' ').fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12))
        .minFontSize(Tokens.interfaceFontVp(8,nativeEnvironment.fontScale)).maxFontSize(Tokens.interfaceFontVp(12,nativeEnvironment.fontScale)).fontColor(Colors.secondary).maxLines(1)
      MachineChart({points:machineTrace(m,key,3600,this.chartEnd),end:this.chartEnd,tint:this.tint(key),
        ceiling:key === 'cpu' || key === 'mem' ? 100 : 0,alternateTint:key === 'network' ? this.tint('upload') : '',dark:nativeEnvironment.dark,
        chartHeight:PhoneStyle.sidebarMetric(30,nativeEnvironment.fontScale)}).margin({top:2})
    }.layoutWeight(1).alignItems(HorizontalAlign.Start)
  }
  @Builder metrics(m:MachineReading,cores:number = Wire.number(m.system,'cores')) {
    if (Math.min(nativeEnvironment.fontScale,Tokens.interfaceScaleFor(12)) < 1.4) Row({space:7}) {
      this.metricCell(m,'cpu',cores); this.metricCell(m,'mem',cores); this.metricCell(m,'network',cores)
      if (Object.keys(m.usage).length) this.metricCell(m,'tokens',cores)
    }.width('100%')
    else Column({space:8}) {
      Row({space:9}) { this.metricCell(m,'cpu',cores); this.metricCell(m,'mem',cores) }.width('100%')
      Row({space:9}) { this.metricCell(m,'network',cores); if (Object.keys(m.usage).length) this.metricCell(m,'tokens',cores) }.width('100%')
    }.width('100%')
  }

  private totalFor(key:string):number { return this.shares.reduce((v:number,c:ComputerCard) => v + Wire.number(Wire.object(c.sample!.usage,'today'),key),0); }
  private hitRate():string {
    const read = this.totalFor('cache_read'), input = read+this.totalFor('cache_write')+this.totalFor('in');
    return input ? Math.round(100*read/input)+'%' : '—';
  }
  private overviewTextHeight(regular: number, large: number): string {
    return String(PhoneStyle.formSize(regular,large,nativeEnvironment.fontScale))+'vp';
  }
  @Builder stat(key:string,value:string) {
    Column({space:2}) {
      Text(tr(key)).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).lineHeight(this.overviewTextHeight(14.33,21.67)).fontColor(Colors.secondary)
      Text(value).fontSize(17).maxFontScale(Tokens.interfaceFontMaxScale).lineHeight(this.overviewTextHeight(20.33,27.67)).fontWeight(FontWeight.Bold).fontColor(Colors.ink).maxLines(1).minFontSize(12).maxFontSize(17)
    }.alignItems(HorizontalAlign.Start).layoutWeight(1)
  }
  @Builder tokensCard() {
    Column({space:Tokens.inset}) {
      Text(tr('phone_today_tokens')).fontSize(17).maxFontScale(Tokens.interfaceFontMaxScale).lineHeight(this.overviewTextHeight(20.33,27.67)).fontWeight(FontWeight.Bold).fontColor(Colors.ink)
      Row({space:8}) {
        this.stat('phone_total',machineTokens(this.total))
        this.stat('phone_rate',tr('machine_per_minute').replace('{value}',machineTokens(this.shares.reduce((s:number,c:ComputerCard) => s+Wire.number(c.sample!.usage,'per_minute'),0))))
        this.stat('phone_hit_rate',this.hitRate())
      }.width('100%')
      if (this.total > 0) {
        Row({space:2}) {
          ForEach(this.shares.filter((c:ComputerCard) => c.tokens>0),(c:ComputerCard,i:number) => {
            Blank().height(PhoneStyle.sidebarMetric(7,nativeEnvironment.fontScale)).layoutWeight(c.tokens).backgroundColor(Colors.ink).borderRadius(Tokens.radiusBadge).opacity(NEUTRAL_SHADES[Math.min(i,4)])
          },(c:ComputerCard,i:number) => c.key+String(c.tokens)+String(i))
        }.width('100%')
        Flex({wrap:FlexWrap.Wrap,space:{main:LengthMetrics.vp(6),cross:LengthMetrics.vp(6)}}) {
          ForEach(this.shares.filter((c:ComputerCard) => c.tokens>0),(c:ComputerCard,i:number) => {
            Row({space:4}) {
              Circle().width(PhoneStyle.sidebarMetric(7,nativeEnvironment.fontScale)).height(PhoneStyle.sidebarMetric(7,nativeEnvironment.fontScale)).fill(Colors.ink).opacity(NEUTRAL_SHADES[Math.min(i,4)])
              Text(c.row.title).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).lineHeight(this.overviewTextHeight(14.33,21.67)).fontColor(Colors.ink).maxLines(1)
              Text(machineTokens(c.tokens)).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).lineHeight(this.overviewTextHeight(14.33,21.67)).fontColor(Colors.secondary).maxLines(1)
            }
          },(c:ComputerCard,i:number) => c.key+String(c.tokens)+String(i))
        }.width('100%')
      }
    }.width('100%').alignItems(HorizontalAlign.Start).padding(14).backgroundColor(Colors.card(this.selected !== undefined)).borderRadius(PhoneStyle.cardRadius)
      .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.cardBorder(nativeEnvironment.dark),radius:PhoneStyle.cardRadius})
      .shadow(PhoneStyle.cardElevation(nativeEnvironment.dark)).id('machines.tokens')
  }
  @Builder statusBadge(value:string,tint:ResourceColor,alert:boolean = false) {
    Row({space:4}) {
      if (alert) SymbolGlyph($r('sys.symbol.exclamationmark_circle')).fontSize('12vp').fontColor([tint])
      else Circle().width(6).height(6).fill(tint)
      Text(value).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontWeight(FontWeight.Medium).fontColor(Colors.ink)
    }
  }
  @Builder homeStatus() {
    Flex({wrap:FlexWrap.Wrap,space:{main:LengthMetrics.vp(16),cross:LengthMetrics.vp(4)}}) {
      this.statusBadge(tr('machine_online_ratio').replace('{online}',String(this.cards.filter((c:ComputerCard) => c.online).length)).replace('{total}',String(this.cards.length)),
        this.cards.every((c:ComputerCard) => c.online) ? Colors.positive : Colors.secondary)
      if (this.cards.some((c:ComputerCard) => c.alerts.length > 0)) this.statusBadge(tr('machine_running_high').replace('{count}',String(this.cards.filter((c:ComputerCard) => c.alerts.length>0).length)),
        this.cards.some((c:ComputerCard) => c.alerts[0]?.severity === 2) ? Colors.danger : Colors.working,true)
      if (this.cards.some((c:ComputerCard) => c.needs>0)) this.statusBadge(tr('machine_waiting_agents').replace('{count}',String(this.cards.reduce((v:number,c:ComputerCard) => v+c.needs,0))),Colors.needs)
    }.width('100%').padding({left:4,right:4})
  }

  @Builder groupHeader(group:string) {
    if (this.groupCards(group).length) Row({space:6}) {
      Circle().width(7).height(7).fill(this.groupCards(group).some((c:ComputerCard) => this.model.directoryOwnerFor(c.row)?.connected) ? Colors.positive : Colors.secondary)
      Text(this.model.groupName(this.groupCards(group)[0].row) || group).fontSize(15).maxFontScale(Tokens.interfaceScaleFor(15)).fontWeight(FontWeight.Medium)
        .fontColor(Colors.secondary).maxLines(2).textOverflow({overflow:TextOverflow.Ellipsis}).flexShrink(1)
      Text(String(this.groupCards(group).filter((c:ComputerCard) => c.online).length)+'/'+String(this.groupCards(group).length)).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontColor(Colors.secondary)
    }.width('100%').alignItems(VerticalAlign.Center).padding({left:4,right:4,top:10,bottom:1}).accessibilityGroup(true).id('machines.group.'+group)
  }

  @Builder section(title:string,count:string = '') {
    Row({space:6}) {
      Text(title).fontSize(17).maxFontScale(Tokens.interfaceFontMaxScale).fontWeight(FontWeight.Bold).fontColor(Colors.ink)
      if (count) Text(count).fontSize(14).maxFontScale(Tokens.interfaceScaleFor(14)).fontColor(Colors.secondary)
    }.width('100%').padding({left:5,top:12,bottom:2})
  }
  private agentInk(a:MachineAgent):ResourceColor {
    return a.state === 'needs_you' ? Colors.needs : a.state === 'working' ? Colors.working : a.state === 'unread' ? Colors.unread : Colors.secondary;
  }
  private agentTextWidth(text: string, size: number, weight: FontWeight = FontWeight.Normal): number {
    const ui = this.getUIContext();
    return ui.px2vp(ui.getMeasureUtils().measureText({ textContent: text, fontSize: Tokens.interfaceFontVp(size,nativeEnvironment.fontScale), fontWeight: weight }));
  }
  private agentTimeFits(a: MachineAgent): boolean {
    const metadata = [a.stateLabel ? tr(a.stateLabel) : '', a.context, a.todoTitle].filter((v:string) => !!v).join(' · ');
    const words = Math.max(this.agentTextWidth(a.row.title || tr('agent_untitled'),17,FontWeight.Medium), this.agentTextWidth(a.note,15), this.agentTextWidth(metadata,13));
    const aside = Math.max(Number.isFinite(a.updated) ? this.agentTextWidth(this.ageLabel(a.updated),12) : 0,
      a.sourceLabel ? this.agentTextWidth(tr(a.sourceLabel),12) + 12 : 0);
    return machineAgentTimeBeside(this.agentContentWidth,words,aside);
  }
  @Builder agentWords(a: MachineAgent) {
    Column({space:4}) {
      Text(a.row.title || tr('agent_untitled')).fontSize(17).maxFontScale(Tokens.interfaceScaleFor(17))
        .fontWeight(a.state === 'unread' ? FontWeight.Bold : FontWeight.Medium).fontColor(Colors.ink)
        .maxLines(2).textOverflow({overflow:TextOverflow.Ellipsis}).id('machines.agent.title')
      if (a.note) Text(a.note).fontSize(15).maxFontScale(Tokens.interfaceScaleFor(15)).fontColor(Colors.secondary)
        .lineSpacing(LengthMetrics.vp(3), {onlyBetweenLines:true}).maxLines(2).textOverflow({overflow:TextOverflow.Ellipsis}).id('machines.agent.preview')
      Row({space:4}) {
        Text() {
          if (a.stateLabel) Span(tr(a.stateLabel)).fontWeight(FontWeight.Medium).fontColor(this.agentInk(a))
          if (a.context) Span((a.stateLabel ? ' · ' : '')+a.context)
        }.fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontColor(Colors.secondary)
          .maxLines(1).textOverflow({overflow:TextOverflow.Ellipsis}).flexShrink(1)
        if (a.todoTitle) SymbolGlyph($r('sys.symbol.list_checkmark')).fontSize('13vp').fontColor([Colors.secondary]).accessibilityText(a.todoTitle)
      }.width('100%').padding({top:1}).id('machines.agent.metadata')
    }.width('100%').alignItems(HorizontalAlign.Start)
  }
  @Builder agentTime(a: MachineAgent, beside: boolean) {
    Column({space:4}) {
      if (Number.isFinite(a.updated)) Text(this.ageLabel(a.updated)).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12))
        .fontColor(Colors.secondary).maxLines(1).id('machines.agent.time')
      if (a.sourceLabel) Text(tr(a.sourceLabel)).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12))
        .fontColor(Colors.link).borderRadius(Tokens.radiusBadge).padding({left:6,right:6,top:1,bottom:1}).backgroundColor(Colors.control).id('machines.agent.source')
    }.alignItems(HorizontalAlign.End)
      // Align the time label to the title’s first baseline when they share a line.
      .padding({top:beside && Number.isFinite(a.updated) ?
        17*Math.min(nativeEnvironment.fontScale,Tokens.interfaceScaleFor(17)) - 12*Math.min(nativeEnvironment.fontScale,Tokens.interfaceScaleFor(12)) : 0})
  }
  @Computed get systemSessionAvatar(): boolean { return Colors.systemControls && Colors.styleID === 'glass'; }
  private avatarInk(a:MachineAgent): ResourceColor {
    if (!this.systemSessionAvatar) return this.agentInk(a);
    if (a.state === 'needs_you') return Colors.needsMark;
    if (a.state === 'working') return Colors.workingMark;
    if (a.state === 'unread') return Colors.unreadMark;
    return Colors.secondary;
  }
  @Builder agentRow(a:MachineAgent, computer:ComputerCard) {
    Row({space:8}) {
      Row({space:12}) {
        Stack() {
          Rect().width(44).height(44).radius(13.2).fill(this.systemSessionAvatar ? $r('app.color.machine_session_avatar_fill') : Colors.control)
          if (computer.avatarSymbol === 'laptop') {
            Rect().width(19.5).height(10.5).radius(0.7).fill(this.avatarInk(a)).opacity(0.25)
              .translate({y:-0.4})
          }
          PhoneOutlineIcon({name:computer.avatarSymbol,color:this.avatarInk(a),iconScale:0.8})
        }.width(44).height(44)
        Column() {
          if (this.agentTimeFits(a)) {
            Row({space:6}) {
              Column() { this.agentWords(a) }.layoutWeight(1)
              this.agentTime(a,true)
            }.width('100%').alignItems(VerticalAlign.Top)
          } else {
            Column({space:4}) {
              this.agentWords(a)
              this.agentTime(a,false)
            }.width('100%').alignItems(HorizontalAlign.Start)
          }
        }.layoutWeight(1).padding({top:4,bottom:4}).onAreaChange((_:Area,area:Area) => {
          const width=Number(area.width);
          if (Number.isFinite(width) && width>0 && Math.abs(width-this.agentContentWidth)>0.5) this.agentContentWidth=width;
        })
      }.layoutWeight(1).alignItems(VerticalAlign.Top)
      SymbolGlyph(Symbols.expand).fontSize('12vp').width(8).fontColor([Colors.secondary])
    }.width('100%').padding({left:16,right:16,top:12,bottom:12}).onClick(() => { Haptics.tap(); this.onSession(a.row); })
      .accessibilityGroup(true).accessibilityText([a.row.title || tr('agent_untitled'),a.note,tr(a.state),a.context,
        a.todoTitle,Number.isFinite(a.updated) ? this.ageLabel(a.updated) : '',a.sourceLabel ? tr(a.sourceLabel) : ''].filter((v:string) => !!v).join('、')).id('machines.agent')
  }
  @Builder agentsCard(c:ComputerCard) {
    Column() {
      ForEach(this.expanded ? c.agents : c.agents.slice(0,5),(a:MachineAgent,i:number) => {
        if (i > 0) Divider().strokeWidth(0.5).color(Colors.divider).margin({left:14})
        this.agentRow(a,c)
      },(a:MachineAgent) => a.row.key()+a.row.raw)
      if (c.agents.length>5 && !this.expanded) {
        Divider().strokeWidth(0.5).color(Colors.divider).margin({left:14})
        Button(tr('machine_all_agents').replace('{count}',String(c.agents.length)),{buttonStyle:ButtonStyleMode.TEXTUAL})
          .height(44).width('100%').fontSize(15).maxFontScale(Tokens.interfaceScaleFor(15)).fontColor(Colors.link).onClick(() => { Haptics.tap(); this.expanded = true; }).id('machines.show_all')
      }
      if (!c.agents.length) Text(tr(c.online ? 'machine_no_agents' : 'machine_offline_agents')).fontSize(15).maxFontScale(Tokens.interfaceScaleFor(15)).fontColor(Colors.secondary).width('100%').padding(14)
    }.width('100%').backgroundColor(Colors.card(this.selected !== undefined)).borderRadius(PhoneStyle.cardRadius)
      .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.cardBorder(nativeEnvironment.dark),radius:PhoneStyle.cardRadius})
      .shadow(PhoneStyle.cardElevation(nativeEnvironment.dark)).clip(true)
  }
  // ArkUI keeps the view for each metric when a poll or range changes. Its reactive reads
  // belong inside that view, rather than in the ForEach creation callback.
  @Computed get historySeries():Map<string,HistorySeries> {
    const result = new Map<string,HistorySeries>();
    const sample = this.detailSample;
    if (sample) {
      for (const key of HISTORY_FIELDS) {
        const points = machineTrace(sample,key,RANGE_SECONDS[this.shownRange],this.chartEnd);
        result.set(key,new HistorySeries(points,this.chartCeiling(key,points)));
      }
    }
    return result;
  }
  private series(key:string):HistorySeries { return this.historySeries.get(key) || EMPTY_SERIES; }
  private currentChartValue(key:string,upload:boolean):number {
    const sample = this.detailSample;
    return sample ? this.historyValue(sample,key,this.series(key).points,this.cards[0]?.online === true,upload) : -1;
  }
  private historyValue(m:MachineReading,key:string,points:DashboardPoint[],online:boolean,upload:boolean = false):number {
    if (!this.inspected) return online ? (upload ? Wire.number(m.latest,'net_tx',-1) : this.latest(m,key)) : -1;
    const point = points.find((p:DashboardPoint) => p.time === this.inspected);
    return upload ? point?.alternate ?? -1 : point?.value ?? -1;
  }
  private chartCeiling(key:string,points:DashboardPoint[]):number {
    if (key === 'cpu' || key === 'mem') return 100;
    const peak = points.reduce((highest:number,p:DashboardPoint) => Math.max(highest,p.value,p.alternate),0);
    return peak > 0 ? peak * 1.15 : 1;
  }
  private liveMetrics(c:ComputerCard,m:MachineReading):LiveMetric[] {
    const sample = m.latest;
    const cpu = this.latest(m,'cpu'), mem = this.latest(m,'mem');
    const diskUsed = Wire.number(sample,'disk_used',-1), diskTotal = Wire.number(sample,'disk_total',-1);
    const swapUsed = Wire.number(sample,'swap_used',-1), swapTotal = Wire.number(sample,'swap_total',-1);
    const cores = Wire.number(c.system,'cores'), disk = percentOf(diskUsed,diskTotal);
    const loadValue = this.latest(m,'load');
    const loadPerCore = loadValue >= 0 && cores > 0 ? tr('machine_load_per_core').replace('{per}',(loadValue/cores).toFixed(1)) : '';
    const loadDetail = [loadPerCore, cores ? tr('machine_cpu_cores').replace('{count}',String(cores)) : ''].filter((v:string) => !!v).join(' · ');
    const tiles = [
      new LiveMetric('cpu','CPU',this.present('cpu',cpu),'',cpu,this.tint('cpu'),'machine_high_cpu'),
      new LiveMetric('mem',this.label('mem'),this.present('mem',mem),this.metricDetail(m,'mem'),mem,this.tint('mem'),'machine_low_memory'),
      new LiveMetric('network',this.label('network'),'↓ '+this.present('network',this.latest(m,'network')),this.metricDetail(m,'network'),-1,this.tint('network')),
      new LiveMetric('load',this.label('load'),formatMachineLoad(loadValue,0,''),loadDetail,-1,this.tint('load'),'machine_high_load'),
      new LiveMetric('disk',tr('machine_disk'),machinePercent(disk),disk >= 0 ? machineBytes(diskUsed)+' / '+machineBytes(diskTotal) : '',disk,Colors.secondary,'machine_full_disk'),
      new LiveMetric('swap',tr('machine_swap'),swapTotal === 0 ? tr('disabled') : swapTotal > 0 ? machineBytes(swapUsed) : '—',
        swapTotal > 0 ? tr('machine_total_bytes').replace('{size}',machineBytes(swapTotal)) : '',percentOf(swapUsed,swapTotal),Colors.secondary)
    ];
    const battery = Wire.number(sample,'battery_percent',-1), temperature = Wire.number(sample,'temp_c',-1);
    if (battery >= 0) tiles.push(new LiveMetric('battery',tr('machine_battery'),machinePercent(battery),
      Wire.bool(sample,'battery_charging') ? tr('machine_charging') : '',battery,Colors.secondary));
    if (temperature > 0) tiles.push(new LiveMetric('temperature',tr('machine_temperature'),temperature.toFixed(1)+' °C','',temperature/125*100,Colors.secondary));
    return tiles;
  }
  @Builder liveTile(tile:LiveMetric,alert:MachineAlert|undefined) {
    Column({space:3}) {
      Row({space:5}) {
        Text(tile.title).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontColor(Colors.secondary).maxLines(2).flexShrink(1)
        if (alert) SymbolGlyph($r('sys.symbol.exclamationmark_circle_fill')).fontSize('13vp')
          .fontColor([alert.severity === 2 ? Colors.danger : Colors.working]).accessibilityText(tr(alert.key))
      }
      Text(tile.value).fontSize(22).maxFontScale(Tokens.interfaceScaleFor(22)).minFontSize(16).maxFontSize(22).maxLines(1).fontWeight(FontWeight.Bold).fontColor(Colors.ink)
      Text(tile.detail || ' ').fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).minFontSize(9).maxFontSize(12)
        .fontColor(Colors.secondary).maxLines(2).width('100%').textOverflow({overflow:TextOverflow.Ellipsis})
      if (tile.percent >= 0) Progress({value:Math.min(100,tile.percent),total:100,type:ProgressType.Linear})
        .width('100%').height(4).style({strokeWidth:4}).color(alert ? alert.severity === 2 ? Colors.danger : Colors.working : tile.tint)
        .backgroundColor(Colors.control).accessibilityLevel('no')
      else Blank().height(4)
    }.width('100%').alignItems(HorizontalAlign.Start).padding(12).backgroundColor(Colors.card(this.selected !== undefined))
      .borderRadius(PhoneStyle.cardRadius)
      .border({width:alert ? 0.8 : PhoneStyle.surfaceEdgeWidth(0.5),
        color:alert ? (alert.severity === 2 ? Colors.danger : Colors.working) : PhoneStyle.cardBorder(nativeEnvironment.dark),
        radius:PhoneStyle.cardRadius})
      .shadow(PhoneStyle.cardElevation(nativeEnvironment.dark)).accessibilityGroup(true).id('machines.now.'+tile.key)
  }
  @Computed get liveTiles():LiveMetric[] {
    const card = this.cards[0], sample = this.detailSample;
    return card?.online && sample && Object.keys(sample.latest).length ? this.liveMetrics(card,sample) : [];
  }
  @Builder liveGrid() {
    // The phone keeps two equally flexible tiles. Expand to 3/4 tiles on Mate X7 / Mate XT wide panes and fold only a genuinely narrow pane.
    GridRow({columns:this.liveGridWidth>0 && this.liveGridWidth<258 ? 1 : this.liveGridWidth>=840 ? 4 : this.liveGridWidth>=560 ? 3 : 2,gutter:{x:10,y:10}}) {
      ForEach(this.liveTiles,(tile:LiveMetric) => {
        GridCol({span:1}) { this.liveTile(tile,this.cards[0]?.alerts.find((a:MachineAlert) => a.key === tile.alert)) }
      },(tile:LiveMetric) => JSON.stringify([tile.key,tile.value,tile.detail,tile.percent,this.cards[0]?.alerts,nativeEnvironment.dark]))
    }.width('100%').onAreaChange((_:Area,area:Area)=>{this.liveGridWidth=Number(area.width);}).id('machines.now')
  }
  @Builder seriesValue(key:string,upload:boolean = false) {
    Row({space:5}) {
      if (key === 'network') {
        Circle().width(7).height(7).fill(this.tint(upload ? 'upload' : key))
        Text(tr(upload ? 'machine_net_up' : 'machine_net_down')).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).fontColor(Colors.secondary)
      }
      Text(key === 'load'
        ? formatMachineLoad(this.currentChartValue(key,upload),Wire.number(this.cards[0]?.system || {},'cores'),tr('machine_load_per_core'))
        : this.present(key,this.currentChartValue(key,upload)))
        .fontSize(key === 'network' ? 18 : 24).maxFontScale(Tokens.interfaceScaleFor(key === 'network' ? 18 : 24)).fontWeight(FontWeight.Bold).fontColor(Colors.ink)
    }.accessibilityGroup(true)
  }
  // The horizontal time labels are outside the canvas. Scale the remaining plot and its vertical axis together.
  @Computed get historyPlotHeight():number { return PhoneStyle.formSize(131,171.33,nativeEnvironment.fontScale); }
  @Builder historyPlot(key:string) {
    Row({space:6}) {
      Column({space:5}) {
        MachineChart({points:this.series(key).points,end:this.chartEnd,seconds:RANGE_SECONDS[this.shownRange],step:RANGE_STEPS[this.shownRange],
          tint:this.tint(key),alternateTint:key === 'network' ? this.tint('upload') : '',
          ceiling:this.series(key).ceiling,chartHeight:this.historyPlotHeight,selectedTime:this.inspected,interactive:true,
          dark:nativeEnvironment.dark,pick:(time:number) => this.inspected=time}).id('machines.chart.'+key)
        MachineTimeAxis({end:this.chartEnd,seconds:RANGE_SECONDS[this.shownRange]})
      }.layoutWeight(1)
      Column() {
        Text(this.present(key,this.series(key).ceiling)).fontSize(11).maxFontScale(Tokens.interfaceScaleFor(11)).fontColor(Colors.secondary)
        Text(this.present(key,this.series(key).ceiling/2)).fontSize(11).maxFontScale(Tokens.interfaceScaleFor(11)).fontColor(Colors.secondary)
        Text(this.present(key,0)).fontSize(11).maxFontScale(Tokens.interfaceScaleFor(11)).fontColor(Colors.secondary)
      }.height(this.historyPlotHeight).alignItems(HorizontalAlign.Start).justifyContent(FlexAlign.SpaceBetween)
        .accessibilityLevel('no-hide-descendants')
    }.width('100%').alignItems(VerticalAlign.Top)
  }
  @Builder chartRow(key:string) {
    Column({space:Tokens.inset}) {
      Row({space:8}) {
        Text(this.historyTitle(key)).fontSize(16).maxFontScale(Tokens.interfaceScaleFor(16)).fontWeight(FontWeight.Medium).fontColor(Colors.ink).layoutWeight(1)
        if (this.inspected) Text(clockLabel(this.inspected)).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontColor(Colors.secondary)
      }.width('100%')
      Flex({wrap:FlexWrap.Wrap,space:{main:LengthMetrics.vp(13),cross:LengthMetrics.vp(4)}}) {
        this.seriesValue(key)
        if (key === 'network') this.seriesValue(key,true)
      }.width('100%')
      this.historyPlot(key)
    }.width('100%').padding(14).alignItems(HorizontalAlign.Start).backgroundColor(Colors.card(this.selected !== undefined)).borderRadius(PhoneStyle.cardRadius)
      .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.cardBorder(nativeEnvironment.dark),radius:PhoneStyle.cardRadius})
      .shadow(PhoneStyle.cardElevation(nativeEnvironment.dark))
  }
  @Builder monitor() {
    Column({space:Tokens.inset}) {
      if (this.liveTiles.length) this.liveGrid()
      Row({space:2}) {
        ForEach(RANGE_LABELS,(key:string,i:number) => {
          Button({type:ButtonType.Normal}) {
            Text(tr(key)).fontSize(14).maxFontScale(Tokens.navigationTextMaxScale).textAlign(TextAlign.Center)
              .fontWeight(this.range === i ? FontWeight.Bold : FontWeight.Normal).fontColor(this.range === i ? Colors.ink : Colors.secondary)
          }.layoutWeight(1).constraintSize({minHeight:44}).padding({left:3,right:3,top:5,bottom:5}).borderRadius(Tokens.radiusControl)
            .backgroundColor(this.range === i ? Colors.surface : Color.Transparent)
            .animation(Motion.state(nativeEnvironment.reduceMotion))
            .accessibilityText(tr(key)).accessibilitySelected(this.range === i)
            .onTouch((event:TouchEvent) => { if (event.type === TouchType.Down && this.range !== i) Haptics.selection(); })
            .onClick(() => this.chooseRange(i)).id('machines.range.'+RANGES[i])
        },(key:string) => key)
      }.width('100%').padding(3).backgroundColor(Colors.control).borderRadius(Tokens.radiusControl)
      if (this.detailSample) {
        if (this.inspected || (this.loading && this.shownRange !== this.range)) Row() {
          if (this.loading && this.shownRange !== this.range) LoadingProgress().width(18).height(18).margin({right:6})
          Text(this.inspected ? clockLabel(this.inspected) : tr(RANGE_LABELS[this.shownRange]))
            .fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontColor(Colors.secondary).layoutWeight(1)
          if (this.inspected) Button(tr('machine_now'),{buttonStyle:ButtonStyleMode.TEXTUAL}).constraintSize({minHeight:44})
            .fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontColor(Colors.link).onClick(() => { Haptics.tap(); this.inspected=0; }).id('machines.chart.latest')
        }.width('100%').constraintSize({minHeight:44}).padding({left:4,right:4})
        if (this.detailSample.points.some((p:Record<string,Object>) => !Wire.bool(p,'gap'))) {
          if (this.machineColumns() > 1) {
            GridRow({columns:this.machineColumns(),gutter:{x:workspaceGridGutter(this.machineColumns()),y:workspaceGridGutter(this.machineColumns())}}) {
              ForEach(HISTORY_FIELDS,(key:string) => {
                GridCol({span:1}) { this.chartRow(key) }
              },(key:string) => key)
            }.width('100%')
          } else {
            ForEach(HISTORY_FIELDS,(key:string) => { this.chartRow(key) },(key:string) => key)
          }
          Text(tr('machine_history_hint')).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).fontColor(Colors.secondary)
        } else Text(tr('machine_no_samples')).fontSize(14).maxFontScale(Tokens.interfaceScaleFor(14)).fontColor(Colors.secondary).padding(8)
      } else if (this.loading) LoadingProgress().width(26).height(26).margin(12)
      if (this.failed) Row({space:7}) {
        Text(tr('machine_monitor_unavailable')).fontSize(14).maxFontScale(Tokens.interfaceScaleFor(14)).fontColor(Colors.secondary).layoutWeight(1)
        Button(tr('retry'),{buttonStyle:ButtonStyleMode.TEXTUAL}).maxFontScale(Tokens.interfaceFontMaxScale).fontColor(Colors.link).onClick(() => { Haptics.tap(); this.sync(false); })
      }.width('100%')
    }.width('100%').alignItems(HorizontalAlign.Start).id('machines.monitor')
  }
  @Builder property(label:string,value:string) {
    Row({space:10}) {
      Text(tr(label)).fontSize(15).maxFontScale(Tokens.interfaceScaleFor(15)).fontColor(Colors.ink)
      Text(value).fontSize(14).maxFontScale(Tokens.interfaceScaleFor(14)).fontColor(Colors.secondary).textAlign(TextAlign.End).layoutWeight(1)
    }.width('100%').constraintSize({minHeight:40})
  }
  @Builder systemCard(c:ComputerCard) {
    Column() {
      if (Wire.text(c.system,'name')) this.property('machine_os',Wire.text(c.system,'name'))
      if (Wire.text(c.system,'cpu')) this.property('CPU',Wire.text(c.system,'cpu'))
      if (Wire.text(c.system,'arch') || Wire.text(c.raw,'arch')) this.property('machine_arch',Wire.text(c.system,'arch') || Wire.text(c.raw,'arch'))
      if (Wire.number(c.system,'memory')>0) this.property('phone_ram',machineBytes(Wire.number(c.system,'memory')))
      if (Wire.text(c.raw,'hostname')) this.property('machine_hostname',Wire.text(c.raw,'hostname'))
      if (this.detailSample && Wire.number(this.detailSample.latest,'disk_total')) this.property('machine_disk',
        machineBytes(Wire.number(this.detailSample.latest,'disk_used',-1))+' / '+machineBytes(Wire.number(this.detailSample.latest,'disk_total')))
      Button(tr('phone_machine_manage'),{buttonStyle:ButtonStyleMode.TEXTUAL}).maxFontScale(Tokens.interfaceFontMaxScale).fontColor(Colors.link).width('100%')
        .onClick(() => { Haptics.tap(); this.onDetails(c.row); }).id('machines.management')
    }.width('100%').padding({left:14,right:14,top:4,bottom:4}).backgroundColor(Colors.card(this.selected !== undefined)).borderRadius(PhoneStyle.cardRadius)
      .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.cardBorder(nativeEnvironment.dark),radius:PhoneStyle.cardRadius})
      .shadow(PhoneStyle.cardElevation(nativeEnvironment.dark))
  }
  build() {
    Refresh({refreshing:$$this.refreshing}) {
      List({space:Tokens.inset}) {
        if (this.selected) {
          if (this.cards.length) {
            ListItem() { this.summary(this.cards[0],true) }
            ListItem() { this.section(tr('agent'),this.cards[0].online ? String(this.cards[0].agents.length) : '') }
            ListItem() { this.agentsCard(this.cards[0]) }
            ListItem() { this.section(tr('machine_monitor')) }
            ListItem() { this.monitor() }
            ListItem() { this.section(tr('system')) }
            ListItem() { this.systemCard(this.cards[0]) }
          } else if (this.loading) ListItem() { LoadingProgress().width(28).height(28) }
          else ListItem() { Text(tr('machine_time_unavailable')).fontSize(15).maxFontScale(Tokens.interfaceScaleFor(15)).fontColor(Colors.secondary).padding(14) }
        } else {
          if (this.settingsHeading) ListItem() {
            Text(tr('phone_machines')).fontSize(34).maxFontScale(Tokens.interfaceScaleFor(34)).fontWeight(FontWeight.Bold)
              .fontColor(Colors.ink).width('100%').accessibilityRole(AccessibilityRoleType.TITLE_BAR).id('machines.settings.title')
          }
          if (this.cards.length) ListItem() { this.homeStatus() }
          if (this.shares.length) ListItem() { this.tokensCard() }
          if (!this.settingsScope && this.model.profiles.length > 1 && this.groupIDs.length) {
            ForEach(this.groupIDs,(group:string) => {
              ListItem() { this.groupHeader(group) }
              if (this.machineColumns() > 1) {
                ListItem() {
                  GridRow({columns:this.machineColumns(),gutter:{x:workspaceGridGutter(this.machineColumns()),y:workspaceGridGutter(this.machineColumns())}}) {
                    ForEach(this.groupCards(group),(c:ComputerCard) => {
                      GridCol({span:1}) { this.summary(c,false) }
                    },(c:ComputerCard) => c.stamp)
                  }.width('100%')
                }
              } else {
                ForEach(this.groupCards(group),(c:ComputerCard) => { ListItem() { this.summary(c,false) } },(c:ComputerCard) => c.stamp)
              }
            },(group:string) => group)
          } else if (this.machineColumns() > 1) {
            ListItem() {
              GridRow({columns:this.machineColumns(),gutter:{x:workspaceGridGutter(this.machineColumns()),y:workspaceGridGutter(this.machineColumns())}}) {
                ForEach(this.cards,(c:ComputerCard) => {
                  GridCol({span:1}) { this.summary(c,false) }
                },(c:ComputerCard) => c.stamp)
              }.width('100%')
            }
          } else {
            ForEach(this.cards,(c:ComputerCard) => { ListItem() { this.summary(c,false) } },(c:ComputerCard) => c.stamp)
          }
          if (this.subscriptionUsage.accounts.some((a:SubscriptionAccount)=>a.visible(this.chartEnd*1000))) ListItem() {
            MachineSubscriptionCards({source:this.subscriptionUsage,model:this.model,active:this.active})
          }
          if (!this.cards.length && !this.loading) ListItem() {
            Column({space:8}) {
              SymbolGlyph(Symbols.device).fontSize('34vp').fontColor([Colors.secondary]).accessibilityLevel('no')
              Text(tr('machine_empty_title')).fontSize(20).maxFontScale(Tokens.interfaceScaleFor(20)).fontWeight(FontWeight.Bold).fontColor(Colors.ink)
              Text(tr('machine_empty_detail')).fontSize(15).maxFontScale(Tokens.interfaceScaleFor(15)).fontColor(Colors.secondary).textAlign(TextAlign.Center)
            }.width('100%').padding({left:24,right:24,top:35,bottom:16}).accessibilityGroup(true)
          }
          if (this.loading && !this.cards.length) ListItem() { LoadingProgress().width(28).height(28) }
          ListItem() { Button(tr('phone_machine_manage'),{buttonStyle:ButtonStyleMode.TEXTUAL}).fontSize(15).maxFontScale(Tokens.interfaceScaleFor(15)).fontColor(Colors.link)
            .onClick(() => { Haptics.tap(); this.onManage(); }).id('machines.manage') }
        }
      }.width('100%').height('100%').padding({left:this.machineSidePadding(),right:this.machineSidePadding(),
        top:this.selected ? PhoneStyle.detailListTop : PhoneStyle.formSize(20,16,nativeEnvironment.fontScale),bottom:24}).id('machines.list')
    }.onRefreshing(() => {
      this.sync(true); this.refreshing=this.active && this.model.connected;
    }).width('100%').height('100%').backgroundColor(Colors.listGroup(this.selected !== undefined))
      .onAreaChange((_:Area,area:Area)=>{ const w=Number(area.width); if (Number.isFinite(w) && w>0 && Math.abs(w-this.paneWidth)>0.5) this.paneWidth=w; })
  }
}
