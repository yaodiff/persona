/**
 * Pure-TS Quick Command Palette builder for HarmonyOS phones, foldables, and tri-fold workbenches.
 * Zero ArkUI dependencies so host `node --test` suites can verify filtering, ordering, and visibility directly.
 */

import { herdrTabName } from './SessionNaming.ts';

export type QuickCommandCategory = 'agents' | 'workspaces' | 'actions';

export type QuickCommandTone = 'needs_you' | 'working' | 'unread' | 'idle' | 'accent' | 'neutral';

export interface QuickSessionInput {
  readonly id: string;
  readonly key?: string;
  readonly title: string;
  readonly subtitle?: string;
  readonly machineName?: string;
  readonly state?: string;
  readonly keywords?: readonly string[];
  readonly herdrTabName?: string;
  readonly preferHerdrTabName?: boolean;
  readonly data?: Record<string, Object>;
}

export interface QuickCommandEntry {
  readonly id: string;
  readonly category: QuickCommandCategory;
  readonly title: string;
  readonly subtitle: string;
  readonly badge: string;
  readonly tone: QuickCommandTone;
  readonly iconKey: string;
  readonly sessionId?: string;
  readonly sessionKey?: string;
  readonly workspaceKind?: string;
  readonly actionId?: string;
  readonly keywords: string[];
}

export interface QuickCommandGroup {
  readonly category: QuickCommandCategory;
  readonly entries: QuickCommandEntry[];
}

export interface QuickCommandLabels {
  readonly mainConversation?: string;
  readonly agents?: string;
  readonly inbox?: string;
  readonly todos?: string;
  readonly knowledge?: string;
  readonly health?: string;
  readonly machines?: string;
  readonly settings?: string;
  readonly toggleWorkbench?: string;
  readonly workbenchCode?: string;
  readonly workbenchMachines?: string;
  readonly openXiaoYi?: string;
  readonly openTokenUsage?: string;
  readonly toggleTheme?: string;
  readonly liveViewPreview?: string;
  readonly starredMessages?: string;
  readonly archivedChats?: string;
  readonly exportChat?: string;
  readonly standingAuth?: string;
  readonly fullSearch?: string;
  readonly allChats?: string;
  readonly needsYou?: string;
  readonly working?: string;
  readonly unread?: string;
  readonly idle?: string;
  readonly untitledAgent?: string;
}

export interface QuickCommandBuildOptions {
  readonly useHerdrTabName?: boolean;
  readonly inboxVisible?: boolean;
  readonly todosVisible?: boolean;
  readonly knowledgeVisible?: boolean;
  readonly showWorkbench?: boolean;
  readonly showLiveView?: boolean;
  readonly showStarred?: boolean;
  readonly showArchived?: boolean;
  readonly showExportChat?: boolean;
  readonly showStandingAuth?: boolean;
  readonly showFullSearch?: boolean;
  readonly showAllChats?: boolean;
  readonly isDark?: boolean;
  readonly labels?: QuickCommandLabels;
}

const MAX_EMPTY_QUERY_AGENTS = 6;

function normalizeSessionState(rawState: string | undefined): 'needs_you' | 'working' | 'unread' | 'idle' {
  const s = (rawState || '').trim().toLowerCase();
  if (s === 'needs_you' || s === 'blocked') return 'needs_you';
  if (s === 'working' || s === 'running') return 'working';
  if (s === 'unread' || s === 'done') return 'unread';
  return 'idle';
}

function sessionPriorityRank(state: 'needs_you' | 'working' | 'unread' | 'idle'): number {
  if (state === 'needs_you') return 0;
  if (state === 'working') return 1;
  if (state === 'unread') return 2;
  return 3;
}

function stateBadgeLabel(
  state: 'needs_you' | 'working' | 'unread' | 'idle',
  labels: QuickCommandLabels | undefined
): string {
  if (state === 'needs_you') return labels?.needsYou || 'Needs you';
  if (state === 'working') return labels?.working || 'Working';
  if (state === 'unread') return labels?.unread || 'Unread';
  return labels?.idle || 'Idle';
}

function resolveAgentTitle(
  session: QuickSessionInput,
  defaultPreferHerdrTab: boolean,
  fallbackTitle: string
): string {
  const preferTab = session.preferHerdrTabName !== undefined ? session.preferHerdrTabName : defaultPreferHerdrTab;
  if (preferTab) {
    const explicitTab = (session.herdrTabName || '').trim();
    if (explicitTab) return explicitTab;
    if (session.data) {
      const fromData = herdrTabName(session.data);
      if (fromData) return fromData;
    }
  }
  const clean = (session.title || '').trim();
  return clean || fallbackTitle;
}

function matchesQuickQuery(
  tokens: readonly string[],
  title: string,
  subtitle: string,
  machineName: string,
  keywords: readonly string[]
): boolean {
  if (tokens.length === 0) return true;
  const fields: string[] = [title, subtitle, machineName];
  for (const kw of keywords) {
    if (kw) fields.push(kw);
  }
  const lowered = fields.map((f: string) => f.toLowerCase());
  for (const token of tokens) {
    let hit = false;
    for (const candidate of lowered) {
      if (candidate.includes(token)) {
        hit = true;
        break;
      }
    }
    if (!hit) return false;
  }
  return true;
}

function buildAgentEntries(
  tokens: readonly string[],
  sessions: readonly QuickSessionInput[],
  options: QuickCommandBuildOptions
): QuickCommandEntry[] {
  const preferTab = !!options.useHerdrTabName;
  const fallback = options.labels?.untitledAgent || 'Agent';

  interface RankedAgent {
    readonly rank: number;
    readonly index: number;
    readonly entry: QuickCommandEntry;
    readonly machineName: string;
  }

  const ranked: RankedAgent[] = [];
  for (let i = 0; i < sessions.length; i++) {
    const session = sessions[i];
    const state = normalizeSessionState(session.state);
    const title = resolveAgentTitle(session, preferTab, fallback);
    const machine = (session.machineName || '').trim();
    const subtitle = (session.subtitle || '').trim() || machine;
    const kw: string[] = [];
    if (session.title && session.title.trim() !== title) kw.push(session.title.trim());
    if (session.herdrTabName && session.herdrTabName.trim() !== title) kw.push(session.herdrTabName.trim());
    if (machine) kw.push(machine);
    kw.push(state);
    if (session.keywords) {
      for (const item of session.keywords) {
        if (item) kw.push(item);
      }
    }
    if (!matchesQuickQuery(tokens, title, subtitle, machine, kw)) continue;
    const entryId = 'agent:' + (session.key || session.id);
    ranked.push({
      rank: sessionPriorityRank(state),
      index: i,
      machineName: machine,
      entry: {
        id: entryId,
        category: 'agents',
        title: title,
        subtitle: subtitle,
        badge: stateBadgeLabel(state, options.labels),
        tone: state,
        iconKey: 'agent',
        sessionId: session.id,
        sessionKey: session.key || session.id,
        keywords: kw
      }
    });
  }

  ranked.sort((a: RankedAgent, b: RankedAgent) => {
    if (a.rank !== b.rank) return a.rank - b.rank;
    return a.index - b.index;
  });

  const limit = tokens.length === 0 ? MAX_EMPTY_QUERY_AGENTS : ranked.length;
  const result: QuickCommandEntry[] = [];
  for (let i = 0; i < ranked.length && i < limit; i++) {
    result.push(ranked[i].entry);
  }
  return result;
}

function buildWorkspaceEntries(
  tokens: readonly string[],
  options: QuickCommandBuildOptions
): QuickCommandEntry[] {
  const labels = options.labels;
  const candidates: QuickCommandEntry[] = [
    {
      id: 'workspace:persona',
      category: 'workspaces',
      title: labels?.mainConversation || 'Main Conversation',
      subtitle: '',
      badge: '',
      tone: 'accent',
      iconKey: 'persona',
      workspaceKind: 'persona',
      keywords: ['main', 'conversation', 'chat', 'persona', 'assistant', '主对话', 'アシスタント']
    },
    {
      id: 'workspace:agents',
      category: 'workspaces',
      title: labels?.agents || 'Agents',
      subtitle: '',
      badge: '',
      tone: 'neutral',
      iconKey: 'agents',
      workspaceKind: 'agents',
      keywords: ['agents', 'sessions', 'claude', 'codex', '智能体', 'エージェント']
    }
  ];

  if (options.inboxVisible !== false) {
    candidates.push({
      id: 'workspace:inbox',
      category: 'workspaces',
      title: labels?.inbox || 'Inbox',
      subtitle: '',
      badge: '',
      tone: 'accent',
      iconKey: 'inbox',
      workspaceKind: 'inbox',
      keywords: ['inbox', 'mail', 'lark', 'messages', '收件箱', '受信トレイ']
    });
  }

  if (options.todosVisible !== false) {
    candidates.push({
      id: 'workspace:todos',
      category: 'workspaces',
      title: labels?.todos || 'Todos',
      subtitle: '',
      badge: '',
      tone: 'neutral',
      iconKey: 'todos',
      workspaceKind: 'todo',
      keywords: ['todos', 'tasks', 'todo', '待办', 'タスク']
    });
  }

  if (options.knowledgeVisible !== false) {
    candidates.push({
      id: 'workspace:knowledge',
      category: 'workspaces',
      title: labels?.knowledge || 'Knowledge',
      subtitle: '',
      badge: '',
      tone: 'accent',
      iconKey: 'knowledge',
      workspaceKind: 'knowledge',
      keywords: ['knowledge', 'docs', 'notes', 'meetings', '知识库', 'ナレッジ']
    });
  }

  candidates.push(
    {
      id: 'workspace:health',
      category: 'workspaces',
      title: labels?.health || 'Health',
      subtitle: '',
      badge: '',
      tone: 'neutral',
      iconKey: 'health',
      workspaceKind: 'health',
      keywords: ['health', 'water', 'wellness', '健康', 'ヘルス']
    },
    {
      id: 'workspace:machines',
      category: 'workspaces',
      title: labels?.machines || 'Machines',
      subtitle: '',
      badge: '',
      tone: 'neutral',
      iconKey: 'machines',
      workspaceKind: 'devices',
      keywords: ['machines', 'devices', 'hosts', 'cpu', '设备', 'マシン']
    },
    {
      id: 'workspace:settings',
      category: 'workspaces',
      title: labels?.settings || 'Settings',
      subtitle: '',
      badge: '',
      tone: 'neutral',
      iconKey: 'settings',
      workspaceKind: 'settings',
      keywords: ['settings', 'preferences', 'config', '设置', '設定']
    }
  );

  return candidates.filter((entry: QuickCommandEntry) =>
    matchesQuickQuery(tokens, entry.title, entry.subtitle, '', entry.keywords)
  );
}

function buildActionEntries(
  tokens: readonly string[],
  options: QuickCommandBuildOptions
): QuickCommandEntry[] {
  const labels = options.labels;
  const candidates: QuickCommandEntry[] = [];

  if (options.showWorkbench) {
    candidates.push(
      {
        id: 'action:toggle_workbench',
        category: 'actions',
        title: labels?.toggleWorkbench || 'Toggle Live Workbench',
        subtitle: 'Ctrl/⌘I',
        badge: '',
        tone: 'accent',
        iconKey: 'workbench',
        actionId: 'toggle_workbench',
        keywords: ['workbench', 'inspector', 'fold', 'toggle', '工作台', 'ワークベンチ']
      },
      {
        id: 'action:workbench_code',
        category: 'actions',
        title: labels?.workbenchCode || 'Switch to Code Tab',
        subtitle: 'Ctrl/⌘2',
        badge: '',
        tone: 'accent',
        iconKey: 'code',
        actionId: 'workbench_code',
        keywords: ['code', 'files', 'git', 'terminal', 'workbench', '代码', 'コード']
      },
      {
        id: 'action:workbench_machines',
        category: 'actions',
        title: labels?.workbenchMachines || 'Switch to Machines Tab',
        subtitle: 'Ctrl/⌘3',
        badge: '',
        tone: 'neutral',
        iconKey: 'machines',
        actionId: 'workbench_machines',
        keywords: ['machines', 'metrics', 'quota', 'cpu', 'load', '设备', 'マシン']
      }
    );
  }

  candidates.push(
    {
      id: 'action:open_token_usage',
      category: 'actions',
      title: labels?.openTokenUsage || 'Token Usage',
      subtitle: '',
      badge: '',
      tone: 'accent',
      iconKey: 'token_usage',
      actionId: 'open_token_usage',
      keywords: ['token', 'usage', 'heatmap', 'calendar', 'project', 'tokens', '用量', 'トークン']
    },
    {
      id: 'action:open_xiaoyi',
      category: 'actions',
      title: labels?.openXiaoYi || 'Open XiaoYi',
      subtitle: 'Ctrl/⌘0',
      badge: '',
      tone: 'accent',
      iconKey: 'xiaoyi',
      actionId: 'open_xiaoyi',
      keywords: ['xiaoyi', 'a2a', 'intent', 'skill', 'voice', '小艺', 'assistant']
    },
    {
      id: 'action:toggle_theme',
      category: 'actions',
      title: labels?.toggleTheme || 'Toggle Light / Dark Theme',
      subtitle: '',
      badge: '',
      tone: 'neutral',
      iconKey: options.isDark ? 'sun' : 'moon',
      actionId: 'toggle_theme',
      keywords: ['theme', 'dark', 'light', 'appearance', 'color', '主题', '深色', '浅色', 'テーマ']
    }
  );

  if (options.showLiveView !== false) {
    candidates.push({
      id: 'action:liveview_preview',
      category: 'actions',
      title: labels?.liveViewPreview || 'LiveView Preview',
      subtitle: '',
      badge: '',
      tone: 'accent',
      iconKey: 'liveview',
      actionId: 'liveview_preview',
      keywords: ['liveview', 'live', 'view', 'capsule', 'preview', '实况窗', 'プレビュー']
    });
  }

  if (options.showStarred === true) {
    candidates.push({
      id: 'action:open_starred',
      category: 'actions',
      title: labels?.starredMessages || 'Starred Messages',
      subtitle: '',
      badge: '',
      tone: 'accent',
      iconKey: 'star',
      actionId: 'open_starred',
      keywords: ['starred', 'star', 'saved', 'bookmark', '收藏', '星标', 'お気に入り', 'スター']
    });
  }

  if (options.showArchived === true) {
    candidates.push({
      id: 'action:open_archived',
      category: 'actions',
      title: labels?.archivedChats || 'Archived Chats',
      subtitle: '',
      badge: '',
      tone: 'neutral',
      iconKey: 'archive',
      actionId: 'open_archived',
      keywords: ['archived', 'archive', 'unarchive', '归档', 'アーカイブ']
    });
  }

  if (options.showExportChat === true) {
    candidates.push({
      id: 'action:export_chat',
      category: 'actions',
      title: labels?.exportChat || 'Export Chat',
      subtitle: '',
      badge: '',
      tone: 'accent',
      iconKey: 'export',
      actionId: 'export_chat',
      keywords: ['export', 'markdown', 'share', 'copy', 'transcript', '导出', 'エクスポート']
    });
  }

  if (options.showStandingAuth === true) {
    candidates.push({
      id: 'action:open_standing_auth',
      category: 'actions',
      title: labels?.standingAuth || 'Standing Authorizations',
      subtitle: '',
      badge: '',
      tone: 'accent',
      iconKey: 'standing_auth',
      actionId: 'open_standing_auth',
      keywords: ['standing', 'authorization', 'authorizations', 'grants', 'always', 'permissions', '授权', '权限', '常時許可']
    });
  }

  if (options.showFullSearch === true) {
    candidates.push({
      id: 'action:open_full_search',
      category: 'actions',
      title: labels?.fullSearch || 'Search Chats & Agents',
      subtitle: '',
      badge: '',
      tone: 'accent',
      iconKey: 'search',
      actionId: 'open_full_search',
      keywords: ['search', 'find', 'chats', 'agents', 'full', '搜索', '检索', '検索']
    });
  }

  if (options.showAllChats === true) {
    candidates.push({
      id: 'action:open_all_chats',
      category: 'actions',
      title: labels?.allChats || 'All Chats & Auto',
      subtitle: '',
      badge: '',
      tone: 'neutral',
      iconKey: 'all_chats',
      actionId: 'open_all_chats',
      keywords: ['all', 'chats', 'auto', 'automatic', 'inbox', 'tasks', '全部对话', '自动对话', 'すべてのチャット']
    });
  }

  return candidates.filter((entry: QuickCommandEntry) =>
    matchesQuickQuery(tokens, entry.title, entry.subtitle, '', entry.keywords)
  );
}

/**
 * Builds grouped, urgent-first Quick Command Palette entries matching `query`.
 */
export function buildQuickCommandGroups(
  query: string,
  sessions: readonly QuickSessionInput[],
  options: QuickCommandBuildOptions = {}
): QuickCommandGroup[] {
  const cleanQuery = (query || '').trim().toLowerCase();
  const tokens = cleanQuery ? cleanQuery.split(/\s+/).filter((part: string) => !!part) : [];

  const groups: QuickCommandGroup[] = [];
  const agents = buildAgentEntries(tokens, sessions, options);
  if (agents.length > 0) {
    groups.push({ category: 'agents', entries: agents });
  }
  const workspaces = buildWorkspaceEntries(tokens, options);
  if (workspaces.length > 0) {
    groups.push({ category: 'workspaces', entries: workspaces });
  }
  const actions = buildActionEntries(tokens, options);
  if (actions.length > 0) {
    groups.push({ category: 'actions', entries: actions });
  }
  return groups;
}
