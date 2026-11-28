/**
 * Pure localized presentation for server-emitted `TodoWait` (`schema/wire/todo-wait.schema.json`, #16168 / #16189).
 * Clients never derive waiting state from raw supervision, PR, or session work fields; they only format `TodoWait`.
 */

export interface TodoWaitContactShape {
  readonly channel: string;
  readonly sender?: string;
  readonly sent_at?: string;
  readonly url?: string;
  readonly request?: string;
}

export interface TodoWaitInput {
  readonly kind: string;
  readonly waiting_on?: string;
  readonly reason?: string;
  readonly wake_condition?: string;
  readonly wake_at?: string;
  readonly until?: string;
  readonly since?: string;
  readonly url?: string;
  readonly handoff_todo_id?: string;
  readonly contact?: TodoWaitContactShape;
  readonly pr_blockers?: readonly string[];
  readonly step?: string;
  readonly unknownKind?: string;
}

export interface TodoWaitRowSummary {
  readonly lead: string;
  readonly timing?: string;
  readonly timed: boolean;
  readonly isDue: boolean;
}

export interface TodoWaitDetailContact {
  readonly channel: string;
  readonly sender?: string;
  readonly sentAtText?: string;
  readonly request?: string;
  readonly url?: string;
}

export interface TodoWaitDetailModel {
  readonly kindTitle: string;
  readonly step?: string;
  readonly waitingOn?: string;
  readonly handoffTodoID?: string;
  readonly reason?: string;
  readonly blockers: string[];
  readonly blockersText?: string;
  readonly wakeCondition?: string;
  readonly wakeAtText?: string;
  readonly untilText?: string;
  readonly untilIsDue: boolean;
  readonly sinceText?: string;
  readonly sourceURL?: string;
  readonly isPR: boolean;
  readonly contact?: TodoWaitDetailContact;
}

export interface TodoWaitLabels {
  readonly kindPersonReply?: string;
  readonly kindMergeReview?: string;
  readonly kindPrMerge?: string;
  readonly kindExternalEvent?: string;
  readonly kindDate?: string;
  readonly kindTodoHandoff?: string;
  readonly kindAgent?: string;
  readonly kindUsageLimit?: string;
  readonly kindComputerOffline?: string;
  readonly kindScheduledReport?: string;
  readonly kindParked?: string;
  readonly kindUnknown?: string;
  readonly blockerReviewRequired?: string;
  readonly blockerChangesRequested?: string;
  readonly blockerChecksPending?: string;
  readonly blockerChecksFailing?: string;
  readonly blockerConflicts?: string;
  readonly usageLimitTarget?: string;
  readonly waitingToMergeTarget?: string;
  readonly waitingOnTarget?: string;
  readonly backAt?: string;
  readonly wakesAt?: string;
  readonly checkInDue?: string;
  readonly untilAt?: string;
  readonly sinceAt?: string;
  readonly itemSeparator?: string;
}

const DOT_SEPARATOR = '\u00A0· ';
const SHORT_WEEKDAYS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];
const SHORT_MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];

function trimmed(raw: string | undefined): string | undefined {
  if (typeof raw !== 'string') return undefined;
  const s = raw.trim();
  return s.length > 0 ? s : undefined;
}

function parseTimestampMs(raw: string | undefined): number | undefined {
  if (!raw) return undefined;
  const ms = Date.parse(raw);
  return Number.isFinite(ms) ? ms : undefined;
}

function safeHttpUrl(raw: string | undefined): string | undefined {
  const s = trimmed(raw);
  if (!s) return undefined;
  return /^https?:\/\/\S+$/i.test(s) ? s : undefined;
}

function pad2(n: number): string {
  return String(n).padStart(2, '0');
}

function localDayIndex(ms: number): number {
  const d = new Date(ms);
  return Math.floor(Date.UTC(d.getFullYear(), d.getMonth(), d.getDate()) / 86400000);
}

export function formatShortMoment(dateMs: number, nowMs: number): string {
  const d = new Date(dateMs);
  const now = new Date(nowMs);
  const hhmm = `${pad2(d.getHours())}:${pad2(d.getMinutes())}`;
  const dayDiff = Math.abs(localDayIndex(dateMs) - localDayIndex(nowMs));
  if (dayDiff === 0) return hhmm;
  if (dayDiff <= 6) {
    return `${SHORT_WEEKDAYS[d.getDay()]} ${hhmm}`;
  }
  const monthDay = `${SHORT_MONTHS[d.getMonth()]} ${d.getDate()}`;
  if (d.getFullYear() !== now.getFullYear()) {
    return `${monthDay}, ${d.getFullYear()}`;
  }
  return monthDay;
}

export function formatFullMoment(dateMs: number, nowMs: number): string {
  const d = new Date(dateMs);
  const now = new Date(nowMs);
  const hhmm = `${pad2(d.getHours())}:${pad2(d.getMinutes())}`;
  const base = `${SHORT_WEEKDAYS[d.getDay()]}, ${SHORT_MONTHS[d.getMonth()]} ${d.getDate()}`;
  if (d.getFullYear() !== now.getFullYear()) {
    return `${base}, ${d.getFullYear()}, ${hhmm}`;
  }
  return `${base}, ${hhmm}`;
}

export function waitKindLabel(kind: string, labels?: TodoWaitLabels): string {
  switch (kind) {
    case 'person_reply': return labels?.kindPersonReply || 'Waiting for reply';
    case 'merge_review': return labels?.kindMergeReview || 'Waiting for review';
    case 'pr_merge': return labels?.kindPrMerge || 'Waiting to merge';
    case 'external_event': return labels?.kindExternalEvent || 'Waiting on external event';
    case 'date': return labels?.kindDate || 'Waiting until date';
    case 'todo_handoff': return labels?.kindTodoHandoff || 'Waiting on another to-do';
    case 'agent': return labels?.kindAgent || 'Waiting on another Agent';
    case 'usage_limit': return labels?.kindUsageLimit || 'Usage limit reached';
    case 'computer_offline': return labels?.kindComputerOffline || 'Computer offline';
    case 'scheduled_report': return labels?.kindScheduledReport || 'Scheduled report';
    case 'parked': return labels?.kindParked || 'Parked';
    default: return labels?.kindUnknown || 'Waiting';
  }
}

export function waitBlockerLabel(blocker: string, labels?: TodoWaitLabels): string | undefined {
  switch (blocker) {
    case 'review_required': return labels?.blockerReviewRequired || 'Needs review';
    case 'changes_requested': return labels?.blockerChangesRequested || 'Changes requested';
    case 'checks_pending': return labels?.blockerChecksPending || 'Checks running';
    case 'checks_failing': return labels?.blockerChecksFailing || 'Checks failing';
    case 'conflicts': return labels?.blockerConflicts || 'Merge conflicts';
    default: return undefined;
  }
}

function resolveBlockerLabels(blockers: readonly string[] | undefined, labels?: TodoWaitLabels): string[] {
  if (!blockers || blockers.length === 0) return [];
  const out: string[] = [];
  for (const blocker of blockers) {
    const label = waitBlockerLabel(blocker, labels);
    if (label) out.push(label);
  }
  return out;
}

function effectiveKind(wait: TodoWaitInput): string {
  return wait.unknownKind ? wait.unknownKind : wait.kind;
}

function formatTemplate(template: string, token: string, value: string): string {
  return template.replace(token, value);
}

export function rowSummary(wait: TodoWaitInput, nowMs: number, labels?: TodoWaitLabels): TodoWaitRowSummary {
  const kind = effectiveKind(wait);
  const target = trimmed(wait.waiting_on);
  const reason = trimmed(wait.reason);
  const sep = labels?.itemSeparator || ', ';
  const blockerList = resolveBlockerLabels(wait.pr_blockers, labels);
  const blockers = blockerList.length > 0 ? blockerList.join(sep) : undefined;

  let lead: string;
  if (kind === 'usage_limit') {
    if (target) {
      lead = labels?.usageLimitTarget
        ? formatTemplate(labels.usageLimitTarget, '{target}', target)
        : `${target} reached its usage limit`;
    } else {
      lead = waitKindLabel('usage_limit', labels);
    }
  } else if (kind === 'pr_merge') {
    const detail = blockers ?? reason;
    if (target && detail) {
      lead = target + DOT_SEPARATOR + detail;
    } else if (target) {
      lead = labels?.waitingToMergeTarget
        ? formatTemplate(labels.waitingToMergeTarget, '{target}', target)
        : `Waiting to merge ${target}`;
    } else if (detail) {
      lead = detail;
    } else {
      lead = waitKindLabel('pr_merge', labels);
    }
  } else {
    if (target && reason) {
      lead = reason.toLowerCase().includes(target.toLowerCase()) ? reason : target + DOT_SEPARATOR + reason;
    } else if (reason) {
      lead = reason;
    } else if (target) {
      lead = labels?.waitingOnTarget
        ? formatTemplate(labels.waitingOnTarget, '{target}', target)
        : `Waiting on ${target}`;
    } else {
      lead = waitKindLabel(kind, labels);
    }
  }

  const step = trimmed(wait.step);
  if (step && !lead.startsWith(step)) {
    lead = step + DOT_SEPARATOR + lead;
  }

  const wakeMs = parseTimestampMs(wait.wake_at);
  const untilMs = parseTimestampMs(wait.until);
  const sinceMs = parseTimestampMs(wait.since);

  let timing: string | undefined;
  let timed = false;
  let isDue = false;

  if (wakeMs !== undefined) {
    const formatted = formatShortMoment(wakeMs, nowMs);
    timed = true;
    if (kind === 'usage_limit') {
      timing = labels?.backAt ? formatTemplate(labels.backAt, '{time}', formatted) : `Back ${formatted}`;
    } else {
      timing = labels?.wakesAt ? formatTemplate(labels.wakesAt, '{time}', formatted) : `Wakes ${formatted}`;
    }
  } else if (untilMs !== undefined) {
    const formatted = formatShortMoment(untilMs, nowMs);
    timed = true;
    if (untilMs <= nowMs) {
      isDue = true;
      timing = labels?.checkInDue ? formatTemplate(labels.checkInDue, '{time}', formatted) : `Check-in due (${formatted})`;
    } else {
      timing = labels?.untilAt ? formatTemplate(labels.untilAt, '{time}', formatted) : `Until ${formatted}`;
    }
  } else if (trimmed(wait.wake_condition)) {
    timing = trimmed(wait.wake_condition);
  } else if (sinceMs !== undefined) {
    const formatted = formatShortMoment(sinceMs, nowMs);
    timed = true;
    timing = labels?.sinceAt ? formatTemplate(labels.sinceAt, '{time}', formatted) : `Since ${formatted}`;
  }

  return { lead, timing, timed, isDue };
}

function buildContact(raw: TodoWaitContactShape | undefined, nowMs: number): TodoWaitDetailContact | undefined {
  if (!raw) return undefined;
  const channel = trimmed(raw.channel);
  if (!channel) return undefined;
  const sentMs = parseTimestampMs(raw.sent_at);
  return {
    channel,
    sender: trimmed(raw.sender),
    sentAtText: sentMs !== undefined ? formatFullMoment(sentMs, nowMs) : undefined,
    request: trimmed(raw.request),
    url: safeHttpUrl(raw.url)
  };
}

export function detailLines(wait: TodoWaitInput, nowMs: number, labels?: TodoWaitLabels): TodoWaitDetailModel {
  const kind = effectiveKind(wait);
  const wakeMs = parseTimestampMs(wait.wake_at);
  const untilMs = parseTimestampMs(wait.until);
  const sinceMs = parseTimestampMs(wait.since);

  const wakeAtText = wakeMs !== undefined ? formatFullMoment(wakeMs, nowMs) : undefined;
  const showSeparateUntil = untilMs !== undefined && (wakeMs === undefined || Math.abs(wakeMs - untilMs) >= 1000);
  const untilText = showSeparateUntil && untilMs !== undefined ? formatFullMoment(untilMs, nowMs) : undefined;
  const untilIsDue = untilMs !== undefined ? untilMs <= nowMs : false;
  const sinceText = sinceMs !== undefined ? formatFullMoment(sinceMs, nowMs) : undefined;

  const contact = buildContact(wait.contact, nowMs);
  const sourceURL = safeHttpUrl(wait.url);
  const dedupedSourceURL = sourceURL && sourceURL === contact?.url ? undefined : sourceURL;
  const sep = labels?.itemSeparator || ', ';
  const blockers = resolveBlockerLabels(wait.pr_blockers, labels);

  return {
    kindTitle: waitKindLabel(kind, labels),
    step: trimmed(wait.step),
    waitingOn: trimmed(wait.waiting_on),
    handoffTodoID: trimmed(wait.handoff_todo_id),
    reason: trimmed(wait.reason),
    blockers,
    blockersText: blockers.length > 0 ? blockers.join(sep) : undefined,
    wakeCondition: trimmed(wait.wake_condition),
    wakeAtText,
    untilText,
    untilIsDue,
    sinceText,
    sourceURL: dedupedSourceURL,
    isPR: kind === 'pr_merge' || kind === 'merge_review',
    contact
  };
}
