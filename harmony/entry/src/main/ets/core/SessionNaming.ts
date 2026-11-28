/**
 * Herdr tab naming preference helpers matching iOS `SessionNaming.swift` and Web `agents-display.ts` (#13899).
 */

/**
 * Returns the live herdr tab label for a non-assistant session associated with a herdr slot/tab,
 * or `''` otherwise.
 */
export function herdrTabName(data: Record<string, Object>): string {
  if (!data || data['assistant'] === true) return '';
  const slotId = typeof data['slot_id'] === 'string' ? (data['slot_id'] as string).trim()
    : typeof data['slotId'] === 'string' ? (data['slotId'] as string).trim() : '';
  const tabId = typeof data['tab_id'] === 'string' ? (data['tab_id'] as string).trim()
    : typeof data['tabId'] === 'string' ? (data['tabId'] as string).trim() : '';
  if (!slotId && !tabId) return '';

  const rawSpaceKind = typeof data['space_kind'] === 'string' ? (data['space_kind'] as string).trim()
    : typeof data['spaceKind'] === 'string' ? (data['spaceKind'] as string).trim() : '';
  const rawHost = typeof data['host'] === 'string' ? (data['host'] as string).trim() : '';
  const rawHostKind = typeof data['host_kind'] === 'string' ? (data['host_kind'] as string).trim()
    : typeof data['hostKind'] === 'string' ? (data['hostKind'] as string).trim() : '';
  let kind = rawSpaceKind || rawHost || rawHostKind;
  if (!kind) {
    const rawPaneId = typeof data['pane_id'] === 'string' ? (data['pane_id'] as string).trim()
      : typeof data['paneId'] === 'string' ? (data['paneId'] as string).trim() : '';
    const colon = rawPaneId.indexOf(':');
    if (colon > 0) {
      kind = rawPaneId.slice(0, colon).trim();
    }
  }
  if (kind && kind !== 'herdr' && kind !== 'herdr-core') return '';

  const label = typeof data['slot_name'] === 'string' ? (data['slot_name'] as string).trim()
    : typeof data['slotName'] === 'string' ? (data['slotName'] as string).trim()
      : typeof data['tab_name'] === 'string' ? (data['tab_name'] as string).trim()
        : typeof data['tabName'] === 'string' ? (data['tabName'] as string).trim() : '';
  return label;
}

/**
 * Primary session title: uses `herdrTabName(data)` when `preferTab` is enabled and available;
 * otherwise preserves `title` unchanged so default-off rendering is pixel-identical.
 */
export function sessionDisplayTitle(title: string, data: Record<string, Object>, preferTab: boolean): string {
  if (!preferTab) return title;
  const tab = herdrTabName(data);
  return tab || title;
}

/**
 * Secondary CLI session title when `preferTab` promotes a differing herdr tab name.
 */
export function sessionDisplayTitleDetail(title: string, data: Record<string, Object>, preferTab: boolean): string {
  if (!preferTab) return '';
  const tab = herdrTabName(data);
  if (!tab) return '';
  const cli = (title || '').trim();
  if (!cli || cli === tab) return '';
  return cli;
}

export const AGENT_AUTONAME_FEATURE = 'agent_autoname';

export interface AutoRenameEligibilityInput {
  readonly kind: string;
  readonly machineId: string;
  readonly sessionId: string;
  readonly assistant: boolean;
  readonly connected: boolean;
  readonly isDemo: boolean;
  readonly features: readonly string[];
}

/**
 * Matches iOS `AppModel.autoRenameAgent` (#17550) & `APIClient.swift:2792`:
 * only coding Agent sessions (never assistant chats), and only when connected
 * to Demo or a hub advertising `agent_autoname`.
 */
export function canAutoRenameSession(input: AutoRenameEligibilityInput): boolean {
  if (!input.connected) return false;
  if (input.kind !== 'session' || input.assistant) return false;
  if (!input.machineId.trim() || !input.sessionId.trim()) return false;
  return input.isDemo || input.features.includes(AGENT_AUTONAME_FEATURE);
}

export function autoRenameSessionPath(machineId: string, sessionId: string): string {
  return '/api/v1/sessions/' + encodeURIComponent(machineId.trim()) + '/' + encodeURIComponent(sessionId.trim()) + '/autoname';
}

export function autoRenameSessionKey(scope: string, generation: number, machineId: string, sessionId: string): string {
  return JSON.stringify([scope, generation, machineId, sessionId]);
}

export function pendingAutoRenameTitle(namingKeys: readonly string[], key: string, renamingLabel: string): string {
  return namingKeys.includes(key) ? renamingLabel : '';
}

/**
 * Guards against applying a delayed HTTP autoname response over a newer manual rename.
 */
export function shouldApplyAutoRenameTitle(
  currentTitle: string,
  titleAtRequestStart: string,
  titleSource: string,
  manualRenameEpoch: number,
  requestStartEpoch: number
): boolean {
  if (manualRenameEpoch > requestStartEpoch) return false;
  if (titleSource === 'manual' && currentTitle !== titleAtRequestStart) return false;
  return true;
}
