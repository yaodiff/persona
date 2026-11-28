/**
 * Pure TypeScript Markdown conversation exporter aligned with `apple/Persona/Views/Chat/ChatExport.swift`.
 * Zero ArkUI dependencies so host Node test suites can test formatting and truncation directly.
 */

export const CHAT_EXPORT_DEFAULT_LIMIT = 20000;

export interface ChatExportMessageInput {
  readonly id: string;
  readonly role: string;
  readonly text: string;
  readonly work?: string;
  readonly toolCount?: number;
  readonly thoughtCount?: number;
  readonly at?: number;
  readonly speaker?: string;
}

export interface ChatExportOptions {
  readonly title?: string;
  readonly machineName?: string;
  readonly modelName?: string;
  readonly userSpeaker?: string;
  readonly assistantSpeaker?: string;
  readonly workLabel?: string;
  readonly includeTimestamps?: boolean;
  readonly includeWorkSteps?: boolean;
  readonly limit?: number;
  readonly truncatedNotice?: string;
}

export interface ChatExportResult {
  readonly markdown: string;
  readonly messageCount: number;
  readonly workStepCount: number;
  readonly truncatedCount: number;
}

function pad2(value: number): string {
  return value < 10 ? '0' + String(value) : String(value);
}

/**
 * Formats an epoch timestamp (ms) into a deterministic `YYYY-MM-DD HH:MM` UTC string.
 */
export function formatExportTimestamp(ms: number | undefined): string {
  if (typeof ms !== 'number' || !Number.isFinite(ms) || ms <= 0) return '';
  const date = new Date(ms);
  if (Number.isNaN(date.getTime())) return '';
  const yyyy = date.getUTCFullYear();
  const mm = pad2(date.getUTCMonth() + 1);
  const dd = pad2(date.getUTCDate());
  const hh = pad2(date.getUTCHours());
  const min = pad2(date.getUTCMinutes());
  return `${yyyy}-${mm}-${dd} ${hh}:${min} UTC`;
}

function hasUnclosedBacktickFence(text: string): boolean {
  let count = 0;
  for (const line of text.split('\n')) {
    if (line.trimStart().startsWith('```')) count++;
  }
  return count % 2 === 1;
}

export function truncateExportText(
  rawText: string,
  limit: number = CHAT_EXPORT_DEFAULT_LIMIT,
  noticeTemplate?: string
): { text: string; truncated: boolean } {
  const normalized = (rawText || '').replace(/\r\n/g, '\n').trim();
  if (!normalized) return { text: '', truncated: false };
  const effectiveLimit = typeof limit === 'number' && Number.isFinite(limit) && limit > 0
    ? Math.floor(limit)
    : CHAT_EXPORT_DEFAULT_LIMIT;
  const scalars = Array.from(normalized);
  if (scalars.length <= effectiveLimit) {
    return { text: normalized, truncated: false };
  }
  const omitted = scalars.length - effectiveLimit;
  let clipped = scalars.slice(0, effectiveLimit).join('').trimEnd();
  if (hasUnclosedBacktickFence(clipped)) {
    clipped += '\n```';
  }
  const notice = (noticeTemplate || '… ({omitted} characters truncated)')
    .replace('{omitted}', String(omitted))
    .replace('{limit}', String(effectiveLimit));
  return {
    text: clipped + '\n\n_' + notice + '_',
    truncated: true
  };
}

/**
 * Formats conversation messages into a clean Markdown document.
 */
export function exportChatToMarkdown(
  messages: readonly ChatExportMessageInput[],
  options: ChatExportOptions = {}
): ChatExportResult {
  const title = (options.title || '').trim() || 'Conversation';
  const machine = (options.machineName || '').trim();
  const model = (options.modelName || '').trim();
  const userSpeaker = (options.userSpeaker || '').trim() || 'Me';
  const assistantSpeaker = (options.assistantSpeaker || '').trim() || 'Assistant';
  const workLabel = (options.workLabel || '').trim() || 'Work steps';
  const includeTimestamps = options.includeTimestamps !== false;
  const includeWorkSteps = options.includeWorkSteps === true;
  const limit = options.limit ?? CHAT_EXPORT_DEFAULT_LIMIT;

  const sections: string[] = [];
  const headerLines: string[] = [`# ${title}`];
  const metaParts: string[] = [];
  if (machine) metaParts.push(machine);
  if (model) metaParts.push(model);
  if (metaParts.length > 0) {
    headerLines.push(`> ${metaParts.join(' · ')}`);
  }
  sections.push(headerLines.join('\n\n'));

  let messageCount = 0;
  let workStepCount = 0;
  let truncatedCount = 0;

  for (const msg of messages) {
    if (msg.role === 'work') {
      if (!includeWorkSteps) continue;
      const bodyRaw = (msg.work || msg.text || '').trim();
      const summaryCounts: string[] = [];
      if (msg.toolCount && msg.toolCount > 0) summaryCounts.push(`${msg.toolCount} tools`);
      if (msg.thoughtCount && msg.thoughtCount > 0) summaryCounts.push(`${msg.thoughtCount} thoughts`);
      const content = bodyRaw || summaryCounts.join(' · ');
      if (!content) continue;
      workStepCount++;
      const clipped = truncateExportText(content, limit, options.truncatedNotice);
      if (clipped.truncated) truncatedCount++;
      const stamp = includeTimestamps ? formatExportTimestamp(msg.at) : '';
      const heading = stamp ? `### ${workLabel} · ${stamp}` : `### ${workLabel}`;
      sections.push(`${heading}\n\n${clipped.text}`);
      continue;
    }

    const clipped = truncateExportText(msg.text, limit, options.truncatedNotice);
    if (!clipped.text) continue;
    if (clipped.truncated) truncatedCount++;
    messageCount++;

    const speaker = (msg.speaker || '').trim() || (msg.role === 'user' ? userSpeaker : assistantSpeaker);
    const stamp = includeTimestamps ? formatExportTimestamp(msg.at) : '';
    const heading = stamp ? `## ${speaker} · ${stamp}` : `## ${speaker}`;
    sections.push(`${heading}\n\n${clipped.text}`);
  }

  return {
    markdown: sections.join('\n\n---\n\n'),
    messageCount: messageCount,
    workStepCount: workStepCount,
    truncatedCount: truncatedCount
  };
}
