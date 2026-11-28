import { ResourceRow, Wire } from './Wire';

export class MachineReading {
  system: Record<string,Object>;
  latest: Record<string,Object>;
  points: Record<string,Object>[];
  usage: Record<string,Object>;
  constructor(data: Record<string,Object>) {
    this.system = Wire.object(data, 'system');
    this.latest = Wire.object(data, 'latest');
    this.points = Wire.rows(data, 'points');
    this.usage = Wire.object(data, 'usage');
  }
}

export class MachineAlert {
  readonly key: string;
  readonly severity: number;
  constructor(key: string, severity: number) { this.key = key; this.severity = severity; }
}

export class DashboardPoint {
  readonly time: number;
  readonly value: number;
  readonly alternate: number;
  constructor(time: number, value: number, alternate: number = -1) { this.time = time; this.value = value; this.alternate = alternate; }
}

export function machineKey(group: string, manager: string, id: string): string {
  return JSON.stringify([group, manager, id]);
}

export function machineOnline(row: ResourceRow): boolean {
  return row.state !== 'offline' && Wire.bool(row.data(), 'online', true);
}

export function machinePending(data: Record<string,Object>): boolean {
  return Wire.text(data, 'trust') === 'pending' || Wire.bool(data, 'pending_confirm') || Wire.bool(data, 'pending');
}

// Machine.symbol uses the Hub's explicit desktop/server roles. Earlier records default to a laptop.
export function machineAvatarSymbol(icon: string): string {
  return icon === 'server' || icon === 'desktop' ? icon : 'laptop';
}

export function osFamily(system: Record<string,Object>, reported: string): string {
  const identifiers = [Wire.text(system, 'distro')].concat(Wire.text(system,'like').split(/\s+/));
  const families: Record<string,string> = {
    'macos':'macos', 'ubuntu':'ubuntu', 'pop':'ubuntu', 'linuxmint':'ubuntu', 'elementary':'ubuntu', 'zorin':'ubuntu',
    'debian':'debian', 'raspbian':'debian', 'kali':'debian', 'fedora':'fedora', 'rhel':'redhat', 'redhat':'redhat',
    'centos':'centos', 'rocky':'rocky', 'almalinux':'alma', 'arch':'arch', 'manjaro':'arch', 'endeavouros':'arch',
    'alpine':'alpine', 'opensuse':'opensuse', 'opensuse-leap':'opensuse', 'opensuse-tumbleweed':'opensuse',
    'suse':'opensuse', 'sles':'opensuse', 'nixos':'nixos'
  };
  for (const id of identifiers) { const family = families[id.toLowerCase()]; if (family) return family; }
  const os = (Wire.text(system, 'os') || reported).toLowerCase();
  return ['darwin','macos','mac'].includes(os) ? 'macos' : os === 'linux' ? 'linux' : '';
}

export function percentOf(used: number, total: number): number { return used >= 0 && total > 0 ? 100 * used / total : -1; }

export function machineBytes(value: number): string {
  if (value < 0 || !Number.isFinite(value)) return '—';
  const units = ['B','KB','MB','GB','TB']; let unit = 0;
  let n = value;
  while (unit < units.length - 1 && n >= (unit === 0 ? 1024 : 1023.5)) { n /= 1024; unit++; }
  return (unit === 0 ? String(Math.floor(n)) : n.toFixed(n >= 99.95 ? 0 : n >= 9.995 ? 1 : 2)) + ' ' + units[unit];
}
export function machinePercent(v: number): string {
  return v < 0 || !Number.isFinite(v) ? '—' : v.toFixed(v > 0 && v < 10 ? 1 : 0) + '%';
}
export function formatMachineLoad(load: number, cores: number, perCoreTemplate: string): string {
  if (!Number.isFinite(load) || load < 0) return '—';
  const raw = (load >= 10 ? load.toFixed(1) : load.toFixed(2)).replace(/\.?0+$/, '');
  if (!Number.isFinite(cores) || cores <= 0) return raw;
  const perCore = (load / cores).toFixed(1);
  return raw + ' · ' + perCoreTemplate.replace('{per}', perCore);
}
export function machineTokens(value: number): string {
  if (value < 0 || !Number.isFinite(value)) return '—';
  const count = Math.round(value);
  if (count < 1000) return String(count);
  const units = ['K','M','B'];
  let unit = count >= 1e9 ? 2 : count >= 1e6 ? 1 : 0;
  for (;;) {
    const scaled = count / Math.pow(1000,unit+1);
    const precision = scaled >= 100 ? 1 : scaled >= 10 ? 10 : 100;
    const rounded = Math.round(scaled*precision)/precision;
    if (rounded < 1000 || unit === units.length-1) return String(rounded)+units[unit];
    unit++;
  }
}
export function machineTokenTotal(day: Record<string,Object>): number {
  return Wire.number(day,'in') + Wire.number(day,'out') + Wire.number(day,'cache_write') + Wire.number(day,'cache_read');
}
export function machineAlerts(reading: MachineReading | undefined, system: Record<string,Object>): MachineAlert[] {
  if (!reading) return [];
  const data = reading.latest;
  const cpu = Wire.number(data,'cpu',-1), load = Wire.number(data,'load',-1), cores = Wire.number(system,'cores');
  const memory = percentOf(Wire.number(data,'mem_used',-1), Wire.number(data,'mem_total'));
  const disk = percentOf(Wire.number(data,'disk_used',-1), Wire.number(data,'disk_total'));
  const result: MachineAlert[] = [];
  const add = (key:string, v:number, warning:number, critical:number):void => {
    if (v >= warning) result.push(new MachineAlert(key, v >= critical ? 2 : 1));
  };
  add('machine_high_cpu', cpu, 75, 90); add('machine_low_memory', memory, 80, 90);
  add('machine_full_disk', disk, 80, 90); if (cores > 0) add('machine_high_load', load / cores, 1, 2);
  return result.sort((a: MachineAlert,b: MachineAlert) => b.severity - a.severity);
}
export function machineConcern(data: Record<string,Object>, online: boolean, alerts: MachineAlert[], waiting: number): number {
  if (machinePending(data)) return 0;
  if (!online) return 1;
  if (alerts.length) return alerts[0].severity === 2 ? 2 : 3;
  if (waiting) return 4;
  return Wire.bool(data,'node_mismatch') || Wire.bool(data,'deploying') ? 5 : 6;
}

// Match the iPhone session row: short words can keep the time beside them at any size.
export function machineAgentTimeBeside(room: number, words: number, aside: number): boolean {
  return room <= 0 || aside <= 0 || room - aside - 6 >= Math.min(words, room * 0.7);
}

export class MachineAgent {
  readonly row: ResourceRow;
  readonly state: string;
  readonly counted: string;
  readonly sourceLabel: string;
  readonly note: string;
  readonly stateLabel: string;
  readonly todoTitle: string;
  readonly context: string;
  readonly updated: number;
  readonly submillisecond: number;
  readonly rank: number;
  constructor(row: ResourceRow) {
    this.row = row;
    const data = row.data(), children = Wire.object(data,'children'), work = Wire.object(data,'work');
    const own = Wire.text(data,'state') || row.state;
    const waiting = ['needs_you','blocked','waiting_for_user'].includes(own);
    const reply = Wire.bool(data,'awaits_reply') || Wire.bool(data,'awaiting_reply');
    this.state = waiting || Wire.number(children,'needs_you') > 0 ? 'needs_you' :
      own === 'working' || (Wire.number(children,'working') > 0 && own !== 'unread' && !(own === 'idle' && reply)) ? 'working' :
      ['idle','unread','offline'].includes(own) ? own : own === 'done' ? 'idle' : 'unknown';
    const background = Wire.bool(data,'background_waiting') && !Wire.number(children,'working') && !Wire.number(children,'needs_you');
    this.counted = this.state === 'working' && background ? 'idle' : this.state;
    this.rank = this.state === 'needs_you' ? 0 : this.state === 'working' ? 1 : 2;
    const attention = this.state === 'needs_you' ? Wire.text(data,'attention_reason') : '';
    const replyLine = reply && this.state !== 'working' ?
      (Wire.text(work,'phase') === 'blocked' && Wire.text(work,'on') === 'user' ? Wire.text(work,'line') : '') || Wire.text(data,'asks') : '';
    this.note = attention || replyLine || Wire.text(data,'last_text') || Wire.text(work,'detail') || row.detail;
    this.stateLabel = this.state === 'needs_you' ? 'needs_you' : reply && this.state !== 'working' ? 'agent_group_awaiting' :
      background ? 'agent_group_background' : Wire.bool(work,'just_finished') ? 'agent_group_just_finished' : '';
    this.todoTitle = Wire.text(Wire.object(data,'todo'),'title');
    const provider = Wire.text(data,'agent'), slot = Wire.text(data,'slot_name') || Wire.text(data,'workspace_name');
    this.context = [provider === 'claude' ? 'Claude' : provider === 'codex' ? 'Codex' : provider, slot].filter((s:string) => !!s).join(' · ');
    const pane = Wire.text(data,'pane_id'), prefix = pane.split(':')[0];
    this.sourceLabel = !pane || prefix==='orca' ? '' : prefix==='botmux' ? 'inbox_source_lark' : 'terminal';
    const stamp=Wire.text(data,'updated_at');
    this.updated = Date.parse(stamp);
    const fraction=stamp.match(/:\d{2}\.(\d+)/);
    this.submillisecond=fraction ? Number(fraction[1].slice(3,9).padEnd(6,'0')) : 0;
  }
}

export function machineAgents(input: ResourceRow[],pagedOrder:boolean=false): MachineAgent[] {
  const ids = new Set<string>(input.map((s:ResourceRow) => s.session || s.id));
  const out: MachineAgent[] = [];
  for (const row of input) {
    const data = row.data(), parent = Wire.text(data,'folded_under');
    if (Wire.bool(data,'assistant') || Wire.text(data,'status') === 'offline' || row.state === 'offline' ||
      (parent && parent !== (row.session || row.id) && ids.has(parent))) continue;
    const agent = new MachineAgent(row), source = Wire.text(data,'source_kind');
    const runtimeChild = source === 'runtime' && (Wire.bool(data,'is_subagent') || !!Wire.text(data,'parent_session_id'));
    const codex = Wire.text(data,'agent') === 'codex' || source.startsWith('codex_');
    const root = Wire.text(data,'root_session_id');
    const codexChild = codex && (Wire.bool(data,'is_subagent') || !!Wire.text(data,'parent_session_id') ||
      !!Wire.text(data,'parent_thread_id') || !!root && root !== (row.session || row.id));
    if ((runtimeChild || codexChild) && agent.state !== 'needs_you') continue;
    out.push(agent);
  }
  return out.sort((a:MachineAgent,b:MachineAgent) => {
    if (a.rank!==b.rank || !pagedOrder) return a.rank-b.rank;
    // The paged Hub list orders equal-status rows by update time and then session ID.
    const at=Number.isFinite(a.updated) ? a.updated : -Infinity, bt=Number.isFinite(b.updated) ? b.updated : -Infinity;
    if (at!==bt) return bt-at;
    if (a.submillisecond!==b.submillisecond) return b.submillisecond-a.submillisecond;
    const aId=a.row.session || a.row.id,bId=b.row.session || b.row.id;
    return aId<bId ? -1 : aId>bId ? 1 : 0;
  });
}

export function machineTrace(reading: MachineReading, field: string, range: number, now: number): DashboardPoint[] {
  const last = now, start = last - range;
  if (field === 'tokens') {
    const recent = reading.usage['recent'];
    if (!Array.isArray(recent)) return [];
    const minute = Math.floor(last / 60) * 60;
    return (recent as Object[]).map((v:Object,i:number) => new DashboardPoint(minute - (recent.length - i - 1) * 60,
      typeof v === 'number' && Number.isFinite(v as number) ? v as number : -1)).filter((p:DashboardPoint) => p.time >= start);
  }
  const total = Wire.number(reading.latest,'mem_total') || Wire.number(reading.system,'memory');
  const points: DashboardPoint[] = [];
  for (const row of reading.points) {
    const time = Wire.number(row,'t');
    if (!time || time < start || time > last) continue;
    if (Wire.bool(row,'gap')) { points.push(new DashboardPoint(time,-1)); continue; }
    if (field === 'mem') points.push(new DashboardPoint(time,percentOf(Wire.number(row,'mem',-1),total)));
    else if (field === 'network') points.push(new DashboardPoint(time,Wire.number(row,'rx',-1),Wire.number(row,'tx',-1)));
    else points.push(new DashboardPoint(time,Wire.number(row,field,-1)));
  }
  return points;
}


// Time grid shared by the Canvas and its axis labels. Keep wall-clock marks stable as a poll advances the window.
export function dashboardTimeTicks(end:number, seconds:number): number[] {
  if (!Number.isFinite(end) || !Number.isFinite(seconds) || seconds <= 0) return [];
  const minutes = seconds <= 3600 ? 15 : seconds <= 21600 ? 60 : 240;
  const first = new Date((end-seconds)*1000);
  first.setSeconds(0,0);
  if (minutes < 60) first.setMinutes(Math.floor(first.getMinutes()/minutes)*minutes);
  else first.setHours(Math.floor(first.getHours()/(minutes/60))*(minutes/60),0);
  const ticks:number[] = [];
  for (let time=first.getTime()/1000; time<=end; time+=minutes*60) {
    if (time>=end-seconds) ticks.push(time);
  }
  return ticks;
}

export function dashboardTickLeft(time:number, end:number, seconds:number, width:number, labelWidth:number):number {
  const center = (time - (end-seconds)) * width / seconds;
  return Math.max(0,Math.min(width-labelWidth,center-labelWidth/2));
}

/** Keep the full clock grid when possible. On a crowded axis label both limits of the window. */
export function dashboardLabelTicks(end:number, seconds:number, width:number, labelWidth:number):number[] {
  const ticks = dashboardTimeTicks(end,seconds);
  if (!ticks.length || labelWidth <= 0 || width < 2*labelWidth+6) return [];
  const left = (time:number):number => dashboardTickLeft(time,end,seconds,width,labelWidth);
  const spacing = labelWidth+6;
  if (ticks.every((time:number,i:number) => i === 0 || left(time)-left(ticks[i-1]) >= spacing)) return ticks;
  const selected:number[] = [end-seconds];
  for (const time of ticks) {
    if (left(time)-left(selected[selected.length-1]) >= spacing && left(end)-left(time) >= spacing) selected.push(time);
  }
  selected.push(end);
  return selected;
}
