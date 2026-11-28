/**
 * Pure TypeScript Token Usage formatting, 12-week heatmap layout, cache hit-rate,
 * and project ranking logic aligned with iOS (`TokenUsage.swift`, `TokenCalendar.swift`,
 * `TokenProjects.swift`) and Go Hub (`usage_history.go`, `usage_projects.go`).
 */

export interface UsageTokens {
  readonly input: number;
  readonly cacheWrite: number;
  readonly cacheRead: number;
  readonly output: number;
  readonly requests: number;
  readonly total: number;
  readonly hitRate: number | null;
}

export interface UsageDay {
  readonly date: string;
  readonly tokens: UsageTokens;
}

export interface UsageDaysResponse {
  readonly zone: string;
  readonly stored: boolean;
  readonly days: UsageDay[];
}

export interface UsageHour {
  readonly t: number;
  readonly date: string;
  readonly hour: number;
  readonly tokens: UsageTokens;
}

export interface UsageMachineShare {
  readonly id: string;
  readonly name: string;
  readonly tokens: UsageTokens;
}

export interface UsageAgentShare {
  readonly agent: string;
  readonly tokens: UsageTokens;
}

export interface UsageHoursResponse {
  readonly zone: string;
  readonly stored: boolean;
  readonly hours: UsageHour[];
  readonly machines: UsageMachineShare[];
  readonly agents: UsageAgentShare[];
  readonly total: UsageTokens;
}

export type UsageProjectKind = 'repo' | 'local' | 'folder' | 'home' | 'temp' | 'assistant' | 'unknown';
export type UsageProjectGroup = 'development' | 'other' | 'unknown';
export type UsageProjectRangeKey = 'today' | '3d' | '7d';

export interface UsageProject {
  readonly key: string;
  readonly kind: UsageProjectKind;
  readonly group: UsageProjectGroup;
  readonly name: string;
  readonly host: string;
  readonly machines: number;
  readonly share: number;
  readonly tokens: UsageTokens;
}

export interface UsageProjectRange {
  readonly days: number;
  readonly total: UsageTokens;
  readonly projects: UsageProject[];
  readonly notSplit: UsageTokens;
}

export interface UsageProjectsResponse {
  readonly zone: string;
  readonly stored: boolean;
  readonly ranges: UsageProjectRange[];
}

export interface UsagePart {
  readonly key: string;
  readonly name: string;
  readonly tokens: UsageTokens;
}

export interface UsagePlace {
  readonly machine: string;
  readonly machineName: string;
  readonly place: string;
  readonly worktree: boolean;
  readonly tokens: UsageTokens;
}

export interface UsageProjectBreakdown {
  readonly days: number;
  readonly total: UsageTokens;
  readonly share: number;
  readonly cacheHit: number;
  readonly machines: UsagePart[];
  readonly agents: UsagePart[];
  readonly models: UsagePart[];
  readonly launchers: UsagePart[];
  readonly places: UsagePlace[];
}

export interface UsageProjectDetailResponse {
  readonly zone: string;
  readonly stored: boolean;
  readonly project: UsageProject;
  readonly ranges: UsageProjectBreakdown[];
  readonly days: UsageDay[];
  readonly hours: Array<{ readonly t: number; readonly hour: number; readonly tokens: UsageTokens }>;
}

export interface HeatmapCell {
  readonly column: number;
  readonly row: number;
  readonly date: string | null;
  readonly level: number;
  readonly tokens: UsageTokens;
  readonly isToday: boolean;
}

export interface HeatmapMonthLabel {
  readonly column: number;
  readonly month: number;
  readonly label: string;
}

export interface TokenHeatmapGrid {
  readonly weeks: number;
  readonly columns: HeatmapCell[][];
  readonly months: HeatmapMonthLabel[];
  readonly todayCell: { readonly column: number; readonly row: number } | null;
}

export interface RankedUsageProject {
  readonly project: UsageProject;
  readonly progressFraction: number;
  readonly sharePercent: number;
}

export interface RankedUsagePart {
  readonly part: UsagePart;
  readonly progressFraction: number;
  readonly sharePercent: number;
}

export interface RankedUsagePlace {
  readonly place: UsagePlace;
  readonly progressFraction: number;
  readonly sharePercent: number;
}

export interface ActivityBarItem {
  readonly id: string;
  readonly label: string;
  readonly shortLabel: string;
  readonly tokens: UsageTokens;
  readonly fraction: number;
  readonly isPeak: boolean;
}

export interface HourlyActivitySummary {
  readonly bars: ActivityBarItem[];
  readonly peakId: string;
  readonly totalTokens: number;
}

const SHORT_MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];

function trimmedDecimal(raw: string): string {
  if (!raw.includes('.')) return raw;
  let end = raw.length;
  while (end > 0 && raw[end - 1] === '0') end--;
  if (end > 0 && raw[end - 1] === '.') end--;
  return raw.slice(0, end);
}

/**
 * Token counts matching iOS `TokenFormat.count` (`TokenUsage.swift`):
 * K / M / B with 3 significant digits, no trailing zeros, and rollover (`999_500 -> 1M`).
 */
export class TokenFormat {
  private static readonly units: ReadonlyArray<readonly [number, string]> = [
    [1e3, 'K'],
    [1e6, 'M'],
    [1e9, 'B']
  ];

  static count(n: number): string {
    if (!Number.isFinite(n)) return '0';
    const sign = n < 0 ? '-' : '';
    const v = Math.abs(Math.trunc(n));
    if (v < 1000) return sign + String(v);
    let i = 0;
    for (let idx = TokenFormat.units.length - 1; idx >= 0; idx--) {
      if (v >= TokenFormat.units[idx][0]) {
        i = idx;
        break;
      }
    }
    while (true) {
      const unitValue = TokenFormat.units[i][0];
      const unitLabel = TokenFormat.units[i][1];
      const x = v / unitValue;
      const digits = x >= 100 ? 0 : x >= 10 ? 1 : 2;
      const scale = Math.pow(10, digits);
      const rounded = Math.round(x * scale) / scale;
      if (rounded >= 1000 && i + 1 < TokenFormat.units.length) {
        i += 1;
        continue;
      }
      return sign + trimmedDecimal(rounded.toFixed(digits)) + unitLabel;
    }
  }
}

function asObject(raw: Object | null | undefined): Record<string, Object> | null {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return null;
  return raw as Record<string, Object>;
}

function asNonNegInt(v: Object | undefined): number {
  return typeof v === 'number' && Number.isFinite(v) && v >= 0 ? Math.trunc(v) : 0;
}

function asNonNegNum(v: Object | undefined): number {
  return typeof v === 'number' && Number.isFinite(v) && v >= 0 ? v : 0;
}

function asText(v: Object | undefined): string {
  return typeof v === 'string' ? v : '';
}

export function emptyUsageTokens(): UsageTokens {
  return { input: 0, cacheWrite: 0, cacheRead: 0, output: 0, requests: 0, total: 0, hitRate: null };
}

export function parseUsageTokens(raw: Object | null | undefined): UsageTokens {
  const obj = asObject(raw);
  if (!obj) return emptyUsageTokens();
  const input = asNonNegInt(obj['in'] ?? obj['input']);
  const cacheWrite = asNonNegInt(obj['cache_write'] ?? obj['cacheWrite']);
  const cacheRead = asNonNegInt(obj['cache_read'] ?? obj['cacheRead']);
  const output = asNonNegInt(obj['out'] ?? obj['output']);
  const requests = asNonNegInt(obj['req'] ?? obj['requests']);
  const total = input + cacheWrite + cacheRead + output;
  const prompt = input + cacheWrite + cacheRead;
  const hitRate = prompt > 0 ? cacheRead / prompt : null;
  return { input, cacheWrite, cacheRead, output, requests, total, hitRate };
}

export function addUsageTokens(a: UsageTokens, b: UsageTokens): UsageTokens {
  const input = a.input + b.input;
  const cacheWrite = a.cacheWrite + b.cacheWrite;
  const cacheRead = a.cacheRead + b.cacheRead;
  const output = a.output + b.output;
  const requests = a.requests + b.requests;
  const total = input + cacheWrite + cacheRead + output;
  const prompt = input + cacheWrite + cacheRead;
  return {
    input,
    cacheWrite,
    cacheRead,
    output,
    requests,
    total,
    hitRate: prompt > 0 ? cacheRead / prompt : null
  };
}

export function sumUsageTokens(list: readonly UsageTokens[]): UsageTokens {
  let acc = emptyUsageTokens();
  for (const item of list) {
    acc = addUsageTokens(acc, item);
  }
  return acc;
}

export function cacheHitRatePercent(tokens: UsageTokens): number {
  if (tokens.hitRate === null || !Number.isFinite(tokens.hitRate)) return 0;
  return Math.round(tokens.hitRate * 1000) / 10;
}

export function sumLastDaysTokens(days: readonly UsageDay[], count: number): UsageTokens {
  if (!days.length || count <= 0) return emptyUsageTokens();
  const slice = days.slice(Math.max(0, days.length - count));
  let acc = emptyUsageTokens();
  for (const day of slice) {
    acc = addUsageTokens(acc, day.tokens);
  }
  return acc;
}

export function parseUsageDaysResponse(raw: Object | null | undefined): UsageDaysResponse {
  const root = asObject(raw);
  const usage = asObject(root?.['usage']) ?? root;
  const zone = asText(usage?.['zone']);
  const stored = usage?.['stored'] === true;
  const rawDays = Array.isArray(usage?.['days']) ? (usage?.['days'] as Object[]) : [];
  const days: UsageDay[] = [];
  for (const entry of rawDays) {
    const obj = asObject(entry);
    if (!obj) continue;
    const date = asText(obj['date']).trim();
    if (!date) continue;
    days.push({ date, tokens: parseUsageTokens(obj) });
  }
  return { zone, stored, days };
}

export function parseUsageHoursResponse(raw: Object | null | undefined): UsageHoursResponse {
  const root = asObject(raw);
  const usage = asObject(root?.['usage']) ?? root;
  const zone = asText(usage?.['zone']);
  const stored = usage?.['stored'] === true;
  const hours: UsageHour[] = [];
  const rawHours = Array.isArray(usage?.['hours']) ? (usage?.['hours'] as Object[]) : [];
  for (const entry of rawHours) {
    const obj = asObject(entry);
    if (!obj) continue;
    hours.push({
      t: asNonNegInt(obj['t']),
      date: asText(obj['date']),
      hour: asNonNegInt(obj['hour']),
      tokens: parseUsageTokens(obj)
    });
  }
  const machines: UsageMachineShare[] = [];
  const rawMachines = Array.isArray(usage?.['machines']) ? (usage?.['machines'] as Object[]) : [];
  for (const entry of rawMachines) {
    const obj = asObject(entry);
    if (!obj) continue;
    machines.push({
      id: asText(obj['id']),
      name: asText(obj['name']),
      tokens: parseUsageTokens(obj)
    });
  }
  const agents: UsageAgentShare[] = [];
  const rawAgents = Array.isArray(usage?.['agents']) ? (usage?.['agents'] as Object[]) : [];
  for (const entry of rawAgents) {
    const obj = asObject(entry);
    if (!obj) continue;
    agents.push({
      agent: asText(obj['agent']),
      tokens: parseUsageTokens(obj)
    });
  }
  const total = parseUsageTokens(usage?.['total']);
  return { zone, stored, hours, machines, agents, total };
}

function parseProjectKind(raw: string): UsageProjectKind {
  if (raw === 'repo' || raw === 'local' || raw === 'folder' || raw === 'home' || raw === 'temp' || raw === 'assistant') {
    return raw;
  }
  return 'unknown';
}

function parseProjectGroup(raw: string): UsageProjectGroup {
  if (raw === 'development' || raw === 'other') return raw;
  return 'unknown';
}

function parseUsageProject(raw: Object | undefined): UsageProject | null {
  const obj = asObject(raw);
  if (!obj) return null;
  const key = asText(obj['key']);
  if (!key) return null;
  return {
    key,
    kind: parseProjectKind(asText(obj['kind'])),
    group: parseProjectGroup(asText(obj['group'])),
    name: asText(obj['name']),
    host: asText(obj['host']),
    machines: asNonNegInt(obj['machines']),
    share: asNonNegNum(obj['share']),
    tokens: parseUsageTokens(obj['tokens'])
  };
}

export function parseUsageProjectsResponse(raw: Object | null | undefined): UsageProjectsResponse {
  const root = asObject(raw);
  const usage = asObject(root?.['usage']) ?? root;
  const zone = asText(usage?.['zone']);
  const stored = usage?.['stored'] === true;
  const ranges: UsageProjectRange[] = [];
  const rawRanges = Array.isArray(usage?.['ranges']) ? (usage?.['ranges'] as Object[]) : [];
  for (const entry of rawRanges) {
    const obj = asObject(entry);
    if (!obj) continue;
    const projects: UsageProject[] = [];
    const rawProjects = Array.isArray(obj['projects']) ? (obj['projects'] as Object[]) : [];
    for (const p of rawProjects) {
      const parsed = parseUsageProject(p);
      if (parsed) projects.push(parsed);
    }
    ranges.push({
      days: asNonNegInt(obj['days']),
      total: parseUsageTokens(obj['total']),
      projects,
      notSplit: parseUsageTokens(obj['not_split'])
    });
  }
  return { zone, stored, ranges };
}

function parseUsageParts(raw: Object | undefined): UsagePart[] {
  if (!Array.isArray(raw)) return [];
  const out: UsagePart[] = [];
  for (const entry of raw as Object[]) {
    const obj = asObject(entry);
    if (!obj) continue;
    out.push({
      key: asText(obj['key']),
      name: asText(obj['name']),
      tokens: parseUsageTokens(obj['tokens'])
    });
  }
  return out;
}

function parseUsagePlaces(raw: Object | undefined): UsagePlace[] {
  if (!Array.isArray(raw)) return [];
  const out: UsagePlace[] = [];
  for (const entry of raw as Object[]) {
    const obj = asObject(entry);
    if (!obj) continue;
    out.push({
      machine: asText(obj['machine']),
      machineName: asText(obj['machine_name']),
      place: asText(obj['place']),
      worktree: obj['worktree'] === true,
      tokens: parseUsageTokens(obj['tokens'])
    });
  }
  return out;
}

export function parseUsageProjectDetailResponse(raw: Object | null | undefined): UsageProjectDetailResponse | null {
  const root = asObject(raw);
  const usage = asObject(root?.['usage']) ?? root;
  if (!usage) return null;
  const project = parseUsageProject(usage['project']);
  if (!project) return null;
  const zone = asText(usage['zone']);
  const stored = usage['stored'] === true;
  const ranges: UsageProjectBreakdown[] = [];
  const rawRanges = Array.isArray(usage['ranges']) ? (usage['ranges'] as Object[]) : [];
  for (const entry of rawRanges) {
    const obj = asObject(entry);
    if (!obj) continue;
    ranges.push({
      days: asNonNegInt(obj['days']),
      total: parseUsageTokens(obj['total']),
      share: asNonNegNum(obj['share']),
      cacheHit: asNonNegNum(obj['cache_hit']),
      machines: parseUsageParts(obj['machines']),
      agents: parseUsageParts(obj['agents']),
      models: parseUsageParts(obj['models']),
      launchers: parseUsageParts(obj['launchers']),
      places: parseUsagePlaces(obj['places'])
    });
  }
  const days: UsageDay[] = [];
  const rawDays = Array.isArray(usage['days']) ? (usage['days'] as Object[]) : [];
  for (const entry of rawDays) {
    const obj = asObject(entry);
    if (!obj) continue;
    const date = asText(obj['date']).trim();
    if (!date) continue;
    days.push({ date, tokens: parseUsageTokens(obj['tokens'] ?? obj) });
  }
  const hours: Array<{ readonly t: number; readonly hour: number; readonly tokens: UsageTokens }> = [];
  const rawHours = Array.isArray(usage['hours']) ? (usage['hours'] as Object[]) : [];
  for (const entry of rawHours) {
    const obj = asObject(entry);
    if (!obj) continue;
    hours.push({
      t: asNonNegInt(obj['t']),
      hour: asNonNegInt(obj['hour']),
      tokens: parseUsageTokens(obj['tokens'] ?? obj)
    });
  }
  return { zone, stored, project, ranges, days, hours };
}

/**
 * 5-step intensity buckets (0..4) matching iOS `HeatLevels.levels` (`UsageHistory.swift`):
 * 0 for <= 0; otherwise 1..count by `ceil(count * rank / nonZeroCount)`.
 */
export function heatLevels(values: readonly number[], count: number = 4): number[] {
  if (count <= 0) return values.map(() => 0);
  const sorted = values.filter((v: number) => v > 0).sort((a: number, b: number) => a - b);
  if (sorted.length === 0) return values.map(() => 0);
  return values.map((v: number): number => {
    if (v <= 0) return 0;
    let lo = 0;
    let hi = sorted.length;
    while (lo < hi) {
      const mid = (lo + hi) >> 1;
      if (sorted[mid] <= v) lo = mid + 1;
      else hi = mid;
    }
    return Math.max(1, Math.min(count, Math.ceil((count * lo) / sorted.length)));
  });
}

function parseUtcDay(dateStr: string): Date | null {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(dateStr.trim());
  if (!m) return null;
  const year = Number(m[1]);
  const month = Number(m[2]);
  const day = Number(m[3]);
  const d = new Date(Date.UTC(year, month - 1, day));
  if (d.getUTCFullYear() !== year || d.getUTCMonth() !== month - 1 || d.getUTCDate() !== day) return null;
  return d;
}

function formatUtcDay(d: Date): string {
  const y = String(d.getUTCFullYear()).padStart(4, '0');
  const m = String(d.getUTCMonth() + 1).padStart(2, '0');
  const day = String(d.getUTCDate()).padStart(2, '0');
  return `${y}-${m}-${day}`;
}

function addUtcDays(d: Date, offset: number): Date {
  return new Date(d.getTime() + offset * 86400000);
}

/** Monday = row 0, Sunday = row 6. */
function mondayRow(d: Date): number {
  return (d.getUTCDay() + 6) % 7;
}

/**
 * Builds a 12-week (or `weeks` columns × 7 weekdays) heatmap matrix matching `TokenCalendarLayout`.
 */
export function buildTokenHeatmap(days: readonly UsageDay[], weeks: number = 12): TokenHeatmapGrid {
  if (!days.length || weeks <= 0) {
    return { weeks: 0, columns: [], months: [], todayCell: null };
  }
  const last = days[days.length - 1];
  const today = parseUtcDay(last.date);
  if (!today) {
    return { weeks: 0, columns: [], months: [], todayCell: null };
  }

  const byDate = new Map<string, UsageTokens>();
  for (const day of days) {
    const existing = byDate.get(day.date);
    byDate.set(day.date, existing ? addUsageTokens(existing, day.tokens) : day.tokens);
  }

  const todayRow = mondayRow(today);
  const monday = addUtcDays(today, -todayRow);
  const first = addUtcDays(monday, -7 * (weeks - 1));

  const cellDates: Array<Array<string | null>> = [];
  const shownTotals: number[] = [];
  for (let c = 0; c < weeks; c++) {
    const colDates: Array<string | null> = [];
    for (let r = 0; r < 7; r++) {
      const d = addUtcDays(first, 7 * c + r);
      if (d.getTime() > today.getTime()) {
        colDates.push(null);
      } else {
        const key = formatUtcDay(d);
        colDates.push(key);
        shownTotals.push(byDate.get(key)?.total ?? 0);
      }
    }
    cellDates.push(colDates);
  }

  const steps = heatLevels(shownTotals, 4);
  let stepIdx = 0;
  const columns: HeatmapCell[][] = [];
  for (let c = 0; c < weeks; c++) {
    const col: HeatmapCell[] = [];
    for (let r = 0; r < 7; r++) {
      const key = cellDates[c][r];
      if (key === null) {
        col.push({
          column: c,
          row: r,
          date: null,
          level: -1,
          tokens: emptyUsageTokens(),
          isToday: false
        });
      } else {
        const level = steps[stepIdx++] ?? 0;
        col.push({
          column: c,
          row: r,
          date: key,
          level,
          tokens: byDate.get(key) ?? emptyUsageTokens(),
          isToday: c === weeks - 1 && r === todayRow
        });
      }
    }
    columns.push(col);
  }

  const months: HeatmapMonthLabel[] = [];
  let lastMonth: number | null = null;
  for (let c = 0; c < weeks; c++) {
    const d = addUtcDays(first, 7 * c);
    const starts = d.getUTCDate() <= 7;
    const month = d.getUTCMonth();
    if (!starts || (lastMonth !== null && month === lastMonth)) continue;
    if (months.length > 0 && c - months[months.length - 1].column < 3) {
      months.pop();
    }
    months.push({ column: c, month, label: SHORT_MONTHS[month] });
    lastMonth = month;
  }

  return {
    weeks,
    columns,
    months,
    todayCell: { column: weeks - 1, row: todayRow }
  };
}

export function rangeDaysForKey(range: UsageProjectRangeKey): number {
  if (range === 'today') return 1;
  if (range === '3d') return 3;
  return 7;
}

export function selectProjectRange(projects: UsageProjectsResponse, range: UsageProjectRangeKey): UsageProjectRange | null {
  if (!projects.ranges.length) return null;
  const wantedDays = rangeDaysForKey(range);
  return projects.ranges.find((r: UsageProjectRange) => r.days === wantedDays) ?? projects.ranges[projects.ranges.length - 1];
}

export function selectProjectBreakdown(detail: UsageProjectDetailResponse, range: UsageProjectRangeKey): UsageProjectBreakdown | null {
  if (!detail.ranges.length) return null;
  const wantedDays = rangeDaysForKey(range);
  return detail.ranges.find((r: UsageProjectBreakdown) => r.days === wantedDays) ?? detail.ranges[detail.ranges.length - 1];
}

/**
 * Ranks a range's projects largest-first and normalizes progress fractions against the largest item (`0..1`).
 */
export function rankProjectsForRange(projects: UsageProjectsResponse, range: UsageProjectRangeKey): RankedUsageProject[] {
  const selected = selectProjectRange(projects, range);
  if (!selected || !selected.projects.length) return [];
  const sorted = selected.projects.slice().sort((a: UsageProject, b: UsageProject) => {
    if (b.tokens.total !== a.tokens.total) return b.tokens.total - a.tokens.total;
    return a.key.localeCompare(b.key);
  });
  const top = Math.max(1, sorted[0].tokens.total, selected.notSplit.total);
  const whole = Math.max(1, selected.total.total);
  return sorted.map((project: UsageProject): RankedUsageProject => ({
    project,
    progressFraction: Math.max(0, Math.min(1, project.tokens.total / top)),
    sharePercent: Math.round(((project.share > 0 ? project.share : project.tokens.total / whole) * 1000)) / 10
  }));
}

export function rankUsageParts(parts: readonly UsagePart[]): RankedUsagePart[] {
  const nonZero = parts.filter((p: UsagePart) => p.tokens.total > 0);
  if (!nonZero.length) return [];
  const sorted = nonZero.slice().sort((a: UsagePart, b: UsagePart) => {
    if (b.tokens.total !== a.tokens.total) return b.tokens.total - a.tokens.total;
    return a.key.localeCompare(b.key);
  });
  const top = Math.max(1, sorted[0].tokens.total);
  const sum = Math.max(1, sorted.reduce((acc: number, item: UsagePart) => acc + item.tokens.total, 0));
  return sorted.map((part: UsagePart): RankedUsagePart => ({
    part,
    progressFraction: Math.max(0, Math.min(1, part.tokens.total / top)),
    sharePercent: Math.round((part.tokens.total / sum) * 1000) / 10
  }));
}

export function formatSharePercent(percent: number): string {
  if (percent > 0 && percent < 0.1) return '<0.1%';
  if (percent >= 10) return `${Math.round(percent)}%`;
  return `${Number(percent.toFixed(1))}%`;
}

export function formatHourLabel(hour: number): string {
  const h = Math.max(0, Math.min(23, Math.trunc(hour)));
  return String(h).padStart(2, '0') + ':00';
}

export function buildRecentHourlyActivity(hours: readonly UsageHour[], maxBars: number = 24): HourlyActivitySummary {
  if (!hours.length || maxBars <= 0) {
    return { bars: [], peakId: '', totalTokens: 0 };
  }
  const sorted = hours.slice().sort((a: UsageHour, b: UsageHour) => {
    if (a.t !== b.t) return a.t - b.t;
    if (a.date !== b.date) return a.date.localeCompare(b.date);
    return a.hour - b.hour;
  });
  const sliced = sorted.slice(Math.max(0, sorted.length - maxBars));
  let peak = 0;
  let peakId = '';
  let totalTokens = 0;
  for (let i = 0; i < sliced.length; i++) {
    const t = sliced[i].tokens.total;
    totalTokens += t;
    if (t >= peak) {
      peak = t;
      peakId = (sliced[i].date || String(sliced[i].t)) + ':' + String(sliced[i].hour);
    }
  }
  const denom = Math.max(1, peak);
  const bars: ActivityBarItem[] = sliced.map((entry: UsageHour): ActivityBarItem => {
    const id = (entry.date || String(entry.t)) + ':' + String(entry.hour);
    const shortClock = formatHourLabel(entry.hour);
    const shortDate = entry.date ? entry.date.slice(5) : '';
    return {
      id,
      label: shortDate ? shortDate + ' ' + shortClock : shortClock,
      shortLabel: shortClock,
      tokens: entry.tokens,
      fraction: entry.tokens.total > 0 ? Math.max(0.06, Math.min(1, entry.tokens.total / denom)) : 0,
      isPeak: id === peakId && peak > 0
    };
  });
  return { bars, peakId, totalTokens };
}

export function buildProjectActivityBars(
  detail: UsageProjectDetailResponse,
  range: UsageProjectRangeKey
): HourlyActivitySummary {
  if (range === 'today') {
    const rawHours = detail.hours;
    if (!rawHours.length) return { bars: [], peakId: '', totalTokens: 0 };
    const sorted = rawHours.slice().sort((a, b) => a.hour - b.hour);
    let peak = 0;
    let peakId = '';
    let totalTokens = 0;
    for (const h of sorted) {
      totalTokens += h.tokens.total;
      if (h.tokens.total >= peak) {
        peak = h.tokens.total;
        peakId = 'h:' + String(h.hour);
      }
    }
    const denom = Math.max(1, peak);
    const bars: ActivityBarItem[] = sorted.map((h): ActivityBarItem => {
      const id = 'h:' + String(h.hour);
      const lbl = formatHourLabel(h.hour);
      return {
        id,
        label: lbl,
        shortLabel: lbl,
        tokens: h.tokens,
        fraction: h.tokens.total > 0 ? Math.max(0.06, Math.min(1, h.tokens.total / denom)) : 0,
        isPeak: id === peakId && peak > 0
      };
    });
    return { bars, peakId, totalTokens };
  }
  const count = rangeDaysForKey(range);
  const sliced = detail.days.slice(Math.max(0, detail.days.length - count));
  if (!sliced.length) return { bars: [], peakId: '', totalTokens: 0 };
  let peak = 0;
  let peakId = '';
  let totalTokens = 0;
  for (const d of sliced) {
    totalTokens += d.tokens.total;
    if (d.tokens.total >= peak) {
      peak = d.tokens.total;
      peakId = d.date;
    }
  }
  const denom = Math.max(1, peak);
  const bars: ActivityBarItem[] = sliced.map((d: UsageDay): ActivityBarItem => ({
    id: d.date,
    label: d.date,
    shortLabel: d.date.slice(5),
    tokens: d.tokens,
    fraction: d.tokens.total > 0 ? Math.max(0.06, Math.min(1, d.tokens.total / denom)) : 0,
    isPeak: d.date === peakId && peak > 0
  }));
  return { bars, peakId, totalTokens };
}

export function rankMachineShares(machines: readonly UsageMachineShare[]): RankedUsagePart[] {
  const parts: UsagePart[] = machines.map((m: UsageMachineShare): UsagePart => ({
    key: m.id || m.name,
    name: m.name || m.id,
    tokens: m.tokens
  }));
  return rankUsageParts(parts);
}

export function rankAgentShares(agents: readonly UsageAgentShare[]): RankedUsagePart[] {
  const parts: UsagePart[] = agents.map((a: UsageAgentShare): UsagePart => ({
    key: a.agent,
    name: a.agent,
    tokens: a.tokens
  }));
  return rankUsageParts(parts);
}

export function rankUsagePlaces(places: readonly UsagePlace[]): RankedUsagePlace[] {
  const nonZero = places.filter((p: UsagePlace) => p.tokens.total > 0);
  if (!nonZero.length) return [];
  const sorted = nonZero.slice().sort((a: UsagePlace, b: UsagePlace) => {
    if (b.tokens.total !== a.tokens.total) return b.tokens.total - a.tokens.total;
    return a.place.localeCompare(b.place);
  });
  const top = Math.max(1, sorted[0].tokens.total);
  const sum = Math.max(1, sorted.reduce((acc: number, item: UsagePlace) => acc + item.tokens.total, 0));
  return sorted.map((place: UsagePlace): RankedUsagePlace => ({
    place,
    progressFraction: Math.max(0, Math.min(1, place.tokens.total / top)),
    sharePercent: Math.round((place.tokens.total / sum) * 1000) / 10
  }));
}

export function formatAgentDisplayName(key: string, notSplitLabel: string): string {
  const clean = key.trim().toLowerCase();
  if (!clean) return notSplitLabel;
  if (clean === 'claude') return 'Claude';
  if (clean === 'codex') return 'Codex';
  if (clean === 'gemini') return 'Gemini';
  if (clean === 'antigravity' || clean === 'agy') return 'Antigravity';
  if (clean === 'opencode') return 'OpenCode';
  return key.trim();
}

export function formatLauncherDisplayName(
  key: string,
  terminalLabel: string,
  names?: Record<string, string>
): string {
  const clean = key.trim();
  if (!clean) return terminalLabel;
  const named = names ? names[clean] : undefined;
  if (named && named.trim()) return named.trim();
  return clean;
}
