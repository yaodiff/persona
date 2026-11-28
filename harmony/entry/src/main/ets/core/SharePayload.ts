/** Pure incoming system-share payload parser. Stages shared content as a draft; never auto-sends. */

import type { XiaoYiRoute } from './XiaoYiRoutes';
import { createXiaoYiRoute, formatSharedDraftText } from './XiaoYiRoutes';

export interface ShareRecordInput {
  utd?: string;
  content?: string;
  uri?: string;
  title?: string;
  description?: string;
}

export interface ShareParseResult {
  hasContent: boolean;
  title: string;
  text: string;
  url: string;
  draftText: string;
  route?: XiaoYiRoute;
}

const MAX_RECORDS: number = 16;
const MAX_TITLE: number = 256;
const MAX_TEXT: number = 8000;
const MAX_URL: number = 2048;

function cleanText(raw: string, maxChars: number): string {
  const stripped = (raw || '').replace(/[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f]/g, '').trim();
  if (!stripped) return '';
  const chars = Array.from(stripped);
  return chars.length > maxChars ? chars.slice(0, maxChars).join('') : stripped;
}

function cleanHttpUrl(raw: string): string {
  const trimmed = (raw || '').trim();
  if (!trimmed || trimmed.length > MAX_URL || /[\u0000-\u001f\u007f\s]/.test(trimmed)) return '';
  return /^https?:\/\/[^/?#]+/i.test(trimmed) ? trimmed : '';
}

function isUrlText(value: string): boolean {
  return !!cleanHttpUrl(value);
}

/** Extract bounded title, text, and HTTP(S) URL from ShareKit records or Want fallback fields. */
export function parseShareRecords(
  records: readonly ShareRecordInput[],
  fallbackUri: string = '',
  fallbackParams?: Record<string, Object>
): ShareParseResult {
  let title = '';
  let url = '';
  const textParts: string[] = [];

  const capped = records.slice(0, MAX_RECORDS);
  for (let i = 0; i < capped.length; i++) {
    const rec = capped[i];
    const recTitle = cleanText(rec.title || '', MAX_TITLE);
    const recContent = cleanText(rec.content || '', MAX_TEXT);
    const recDesc = cleanText(rec.description || '', MAX_TEXT);
    const recUri = cleanHttpUrl(rec.uri || '');

    if (!title && recTitle) title = recTitle;
    if (!url && recUri) url = recUri;
    if (recContent) {
      if (!url && isUrlText(recContent)) {
        url = cleanHttpUrl(recContent);
      } else {
        textParts.push(recContent);
      }
    }
    if (recDesc && recDesc !== recContent && recDesc !== recTitle) {
      textParts.push(recDesc);
    }
  }

  if (!url && fallbackUri) {
    url = cleanHttpUrl(fallbackUri);
  }
  if (fallbackParams) {
    const paramText = fallbackParams['ohos.extra.param.key.shareAbstract'] ||
      fallbackParams['text'] ||
      fallbackParams['content'];
    const paramTitle = fallbackParams['ohos.extra.param.key.shareTitle'] || fallbackParams['title'];
    const paramUrl = fallbackParams['ohos.extra.param.key.shareUrl'] || fallbackParams['url'];
    if (!title && typeof paramTitle === 'string') title = cleanText(paramTitle, MAX_TITLE);
    if (!url && typeof paramUrl === 'string') url = cleanHttpUrl(paramUrl);
    if (textParts.length === 0 && typeof paramText === 'string') {
      const cleaned = cleanText(paramText, MAX_TEXT);
      if (cleaned) textParts.push(cleaned);
    }
  }

  const text = cleanText(textParts.join('\n'), MAX_TEXT);
  const draftText = formatSharedDraftText(title, text, url);
  if (!draftText) {
    return { hasContent: false, title: '', text: '', url: '', draftText: '' };
  }

  const route = createXiaoYiRoute('share');
  route.title = title;
  route.text = text;
  route.url = url;
  return {
    hasContent: true,
    title: title,
    text: text,
    url: url,
    draftText: draftText,
    route: route
  };
}
