import { EndpointConfig, HttpFailure } from '../platform/HubTransport';
import { LocalStore } from '../platform/LocalStore';
import { ChatMessage, ResourceRow, Wire } from './Wire';
import { HubRepository } from './HubRepository';
import { DemoTransport } from './DemoTransport';

type DemoRecord = Record<string, Object>;

function demoRecord(keys: string[], values: Object[]): DemoRecord { return Wire.payload(keys, values); }

function omitFields(source: DemoRecord, excluded: string[]): DemoRecord {
  const keys: string[] = [], values: Object[] = [];
  Object.keys(source).forEach((key: string) => {
    if (!excluded.includes(key)) { keys.push(key); values.push(source[key]); }
  });
  return Wire.payload(keys, values);
}

const DEMO_HUB: string = 'demo';
const DEMO_MANAGER: string = 'demo';
const DEMO_MACHINE: string = 'demo-mac';
const DEMO_BASE_URL: string = 'http://127.0.0.1:1/';
const DEMO_LARK_ACCOUNT: string = 'demo-lark-account';
const DEMO_LARK_CONVERSATION: string = 'demo-lark-room';
const DEMO_MAIL_ACCOUNT: string = 'demo-mail-account';
const MAX_TEXT_BYTES: number = 100000;
const MAX_BODY_BYTES: number = 256 * 1024;
const MAX_RESPONSE_BYTES: number = 4 * 1024 * 1024;
const MAX_TRANSCRIPT_ITEMS: number = 80;
const MAX_DATA_ROWS: number = 256;
const MAX_CACHED_OPERATIONS: number = 128;

interface DemoPath {
  parts: string[];
  query: Record<string, string>;
}

/** A synchronous roster for the demo entry screen; every call returns detached rows. */
export class DemoRoster {
  features: string[] = [];
  mainId: string = '';
  leaders: string[] = [];
  machines: ResourceRow[] = [];
  sessions: ResourceRow[] = [];
  conversations: ResourceRow[] = [];
  todos: ResourceRow[] = [];
  knowledge: ResourceRow[] = [];
  inbox: ResourceRow[] = [];
}

function utf8Bytes(value: string): number {
  let bytes = 0;
  for (let index = 0; index < value.length; index++) {
    const code = value.charCodeAt(index);
    if (code <= 0x7f) bytes++;
    else if (code <= 0x7ff) bytes += 2;
    else if (code >= 0xd800 && code <= 0xdbff && index + 1 < value.length) {
      const next = value.charCodeAt(index + 1);
      if (next >= 0xdc00 && next <= 0xdfff) { bytes += 4; index++; }
      else bytes += 3;
    } else bytes += 3;
  }
  return bytes;
}

function cloneRecord(value: DemoRecord): DemoRecord {
  const encoded = JSON.stringify(value);
  if (!encoded || utf8Bytes(encoded) > MAX_RESPONSE_BYTES) throw new HttpFailure(413, 'demo_payload_too_large');
  return Wire.record(JSON.parse(encoded) as Object);
}

function cloneRecords(values: DemoRecord[]): DemoRecord[] { return values.map((value: DemoRecord) => cloneRecord(value)); }

function nonEmpty(value: string, limit: number = 500): string {
  const trimmed = value.trim();
  if (!trimmed || utf8Bytes(trimmed) > limit) throw new HttpFailure(400, 'demo_invalid_input');
  return trimmed;
}

function textWithin(value: string, limit: number): string {
  if (utf8Bytes(value) > limit) throw new HttpFailure(413, 'demo_text_too_large');
  return value;
}

function safePath(value: string): boolean {
  if (!value || value.length > 512 || !value.startsWith('/demo/')) return false;
  if (/[\\\r\n\u0000-\u001f]/.test(value)) return false;
  return value.split('/').slice(1).every((part: string) => !!part && part !== '.' && part !== '..');
}

function queryDecode(value: string): string {
  try { return decodeURIComponent(value.replace(/\+/g, ' ')); }
  catch (_) { throw new HttpFailure(400, 'demo_invalid_path'); }
}

function parsePath(path: string): DemoPath {
  if (!path || path.length > 8192 || !path.startsWith('/api/v1/') || path.startsWith('//') || path.includes('#') ||
    /[\\\r\n\u0000-\u001f]/.test(path)) throw new HttpFailure(400, 'demo_invalid_path');
  const queryAt = path.indexOf('?'), pathname = queryAt < 0 ? path : path.slice(0, queryAt);
  const parts = pathname.split('/').slice(3);
  if (!parts.length || parts.some((part: string) => !part)) throw new HttpFailure(400, 'demo_invalid_path');
  const decoded = parts.map((part: string): string => {
    let value = '';
    try { value = decodeURIComponent(part); } catch (_) { throw new HttpFailure(400, 'demo_invalid_path'); }
    if (!value || value === '.' || value === '..' || value.includes('/') || value.includes('\\') || /[\u0000-\u001f]/.test(value))
      throw new HttpFailure(400, 'demo_invalid_path');
    return value;
  });
  const query: Record<string, string> = {};
  if (queryAt >= 0 && queryAt < path.length - 1) {
    for (const component of path.slice(queryAt + 1).split('&')) {
      if (!component) continue;
      const equals = component.indexOf('='), key = queryDecode(equals < 0 ? component : component.slice(0, equals));
      const value = queryDecode(equals < 0 ? '' : component.slice(equals + 1));
      if (!key || Object.keys(query).includes(key)) throw new HttpFailure(400, 'demo_invalid_path');
      query[key] = value;
    }
  }
  return { parts: decoded, query: query };
}

function rowFrom(data: DemoRecord, kind: string): ResourceRow {
  const row = ResourceRow.from(cloneRecord(data), kind);
  row.manager = Wire.text(data, 'manager', DEMO_MANAGER);
  return row;
}

function copyRows(data: DemoRecord[], kind: string): ResourceRow[] { return data.map((item: DemoRecord) => rowFrom(item, kind)); }

function lower(value: string): string { return value.toLowerCase(); }

/**
 * A bounded, in-memory HubRepository for the local experience. Its endpoint is deliberately inert,
 * its call override never reaches HubTransport, and its rows use only synthetic identities/paths.
 */
export class DemoRepository extends HubRepository {
  readonly features: string[] = ['managers', 'messaging', 'starred_messages', 'assistant_search', 'conversation_origin', 'agent_autoname'];
  private sessionsData: DemoRecord[] = [];
  private conversationsData: DemoRecord[] = [];
  private todosData: DemoRecord[] = [];
  private knowledgeData: DemoRecord[] = [];
  private inboxData: DemoRecord[] = [];
  private mailData: DemoRecord[] = [];
  private larkMessages: DemoRecord[] = [];
  private starredData: DemoRecord[] = [];
  private standingRevision: number = 0;
  private standingData: DemoRecord[] = [];
  private alwaysGrantsByConversation: Map<string, DemoRecord[]> = new Map();
  private leaderIds: string[] = [];
  private transcripts: Map<string, DemoRecord[]> = new Map();
  private pinsData: DemoRecord = {};
  private mutesData: DemoRecord = {};
  private defaultChatPermission:string='default';
  private inboxModelBinding:DemoRecord={};
  private inboxModelRevision:number=0;
  private interactionsData: DemoRecord[] = [];
  private todoDecisionsData: Map<string, DemoRecord[]> = new Map();
  private nextSuggestionsEnabled: boolean = true;
  private settledRuleSuggestions: Set<string> = new Set();
  private operationResults: Map<string, DemoRecord> = new Map();
  private operationOrder: string[] = [];
  private counter: number = 0;
  private messageSequence: number = 0;
  readonly mainId: string = 'demo-main';

  constructor(_local?: LocalStore) {
    // Do not give HubRepository the app database: local demo mutations have no remote receipt/journal.
    super(new EndpointConfig(DEMO_BASE_URL, DEMO_HUB, '', '', DEMO_MANAGER), undefined, () => {}, DEMO_MANAGER,
      new DemoTransport(new EndpointConfig(DEMO_BASE_URL, DEMO_HUB, '', '', DEMO_MANAGER)));
    this.reset();
  }

  /** Reset only the synthetic in-memory world; no user or real-Hub storage is touched. */
  reset(): void {
    this.defaultChatPermission='default';this.inboxModelBinding={};this.inboxModelRevision=0;
    this.nextSuggestionsEnabled = true; this.settledRuleSuggestions = new Set(); this.todoDecisionsData = new Map();
    this.counter = 0; this.messageSequence = 0; this.transcripts = new Map(); this.operationResults = new Map(); this.operationOrder = [];
    this.starredData = [];
    this.leaderIds = [];
    this.standingRevision = 1;
    this.standingData = [
      demoRecord(
        ['id', 'machine_id', 'directory', 'actions', 'conditions', 'quote', 'source', 'granted_at', 'reversible'],
        ['sa_demo_release', DEMO_MACHINE, '/demo/ReleaseDesk', ['merge', 'push'], ['ci_green', 'no_conflicts'],
          'Always merge and push ReleaseDesk when all checks are green.', 'Main conversation', '2026-11-26T10:15:00Z', true]
      ),
      demoRecord(
        ['id', 'machine_id', 'directory', 'actions', 'conditions', 'quote', 'source', 'granted_at', 'reversible'],
        ['sa_demo_deploy', DEMO_MACHINE, '/demo/Notes', ['deploy-test', 'send'], [],
          'Deploy Notes to test and send summary updates.',
          'Suggested rule: the same answer 3 times in 14 days, confirmed with Always.', '2026-11-25T14:20:00Z', false]
      )
    ];
    this.alwaysGrantsByConversation = new Map();
    this.alwaysGrantsByConversation.set(this.mainId, [
      demoRecord(['key_hash', 'tool_name', 'created_at'], ['a'.repeat(64), 'Bash', '2026-11-27T08:50:00Z'])
    ]);
    this.pinsData = demoRecord(['spaces', 'sessions'], [[], []]);
    this.mutesData = demoRecord(['spaces', 'sessions', 'allowed'], [[], [], []]);
    const capabilities: DemoRecord = demoRecord(['read', 'send', 'approve', 'interrupt', 'terminal', 'close', 'rename'],
      [true, true, true, true, true, true, true]);
    const session = (id: string, title: string, state: string, status: string, cwd: string, branch: string,
      launcher: string, model: string, lastText: string): DemoRecord => demoRecord(
      ['id', 'session_id', 'machine_id', 'manager', 'title', 'state', 'status', 'cwd', 'repo_root', 'worktree', 'branch', 'git_branch', 'space_name',
        'source_kind', 'capabilities', 'pane_id', 'slot_name', 'launcher', 'model', 'last_text', 'assistant', 'archived', 'read_only', 'can_fork', 'updated_at'],
      [id, id, DEMO_MACHINE, DEMO_MANAGER, title, state, status, cwd, '/demo/ReleaseDesk', cwd, branch, branch, 'ReleaseDesk', 'live',
        cloneRecord(capabilities), 'demo:pane-' + id, title, launcher, model, lastText, false, false, false, true, '2026-11-27T09:00:00Z']);
    const changelog = session('demo-agent-changelog', 'Prepare the changelog', 'working', 'working', '/demo/ReleaseDesk', 'docs', 'codex', 'gpt-5.3-codex', 'Drafting v2.4 changelog & verifying diffs...');
    const changelogCaps = Wire.object(changelog, 'capabilities');
    changelogCaps['queue_send_now'] = true;
    changelog['capabilities'] = changelogCaps;
    changelog['queued'] = ['Include Mate XT 2 tri-fold notes', 'Double-check zh_CN strings'];
    changelog['queued_relayed'] = 0;
    const archivedAgent = session('demo-agent-archived', 'Audit Q4 release tags', 'idle', 'offline', '/demo/ReleaseDesk', 'release/q4', 'claude', 'claude-sonnet-4-6', 'Archived release audit session from last week.');
    archivedAgent['archived'] = true;
    archivedAgent['updated_at'] = '2026-11-24T17:15:00Z';
    this.sessionsData = [
      session('demo-agent-review', 'Review release checks', 'needs_you', 'idle', '/demo/ReleaseDesk', 'main', 'claude', 'claude-sonnet-4-6', 'Release audit ready · Awaiting plan approval'),
      changelog,
      session('demo-agent-notes', 'Organize project notes', 'unread', 'done', '/demo/Notes', 'main', 'claude', 'claude-opus-4-6', 'Grouped 14 architecture notes by milestone.'),
      archivedAgent
    ];
    const mainSuggestions: DemoRecord[] = [
      demoRecord(['text', 'risky'], ['Summarize open release blockers', false]),
      demoRecord(['text', 'risky'], ['Show recent token usage', false]),
      demoRecord(['text', 'risky'], ['Run git push origin main', true])
    ];
    this.conversationsData = [
      demoRecord(['id', 'session_id', 'machine_id', 'manager', 'title', 'main', 'kind', 'origin', 'archived', 'can_fork', 'suggestions', 'updated_at', 'last_text'],
        [this.mainId, this.mainId, DEMO_MACHINE, DEMO_MANAGER, 'Main conversation', true, 'main', 'user', false, false, mainSuggestions, '2026-11-27T09:00:00Z', 'Ready to explore the local Persona demo.']),
      demoRecord(['id', 'session_id', 'machine_id', 'manager', 'title', 'main', 'kind', 'origin', 'archived', 'can_fork', 'updated_at', 'last_text'],
        ['demo-recent-review', 'demo-recent-review', DEMO_MACHINE, DEMO_MANAGER, 'Release checklist', false, 'recent', 'user', false, true, '2026-11-27T08:40:00Z', 'Start with the release checks, then confirm the changelog and open issues.']),
      demoRecord(['id', 'session_id', 'machine_id', 'manager', 'title', 'main', 'kind', 'origin', 'automatic', 'archived', 'can_fork', 'updated_at', 'last_text'],
        ['demo-auto-summary', 'demo-auto-summary', DEMO_MACHINE, DEMO_MANAGER, 'Daily summary', false, 'automatic', 'task', true, false, true, '2026-11-27T07:30:00Z', 'Three sample tasks are ready to inspect in this local demo.']),
      demoRecord(['id', 'session_id', 'machine_id', 'manager', 'title', 'main', 'kind', 'origin', 'archived', 'can_fork', 'updated_at', 'last_text'],
        ['demo-archived-notes', 'demo-archived-notes', DEMO_MACHINE, DEMO_MANAGER, 'Q4 design review notes', false, 'recent', 'user', true, true, '2026-11-25T16:30:00Z', 'Summarized the Q4 Liquid Glass token alignment across iOS and HarmonyOS.'])
    ];
    this.transcripts.set(this.transcriptKey(DEMO_MACHINE, this.mainId), [
      this.item('assistant', 'Ready to explore the local Persona demo. Messages stay on this device and do not reach an agent or service.'),
      this.item('assistant', '### What you can preview offline\n- **Agents & Live Workbench** — inspect rich Markdown reports, code blocks, diffs, files, and terminal output\n- **Needs You Queue** — approve or decline the open release plan in *Review release checks*\n- **Machines & Quota** — browse live-shaped CPU, memory, per-core load, and subscription windows\n- **Inbox & To-Dos** — reply to sample threads, hand off items, or complete tasks')
    ]);
    this.transcripts.set(this.transcriptKey(DEMO_MACHINE, 'demo-recent-review'), [
      this.item('user', 'What should I review before the release?'),
      this.item('assistant', 'Start with the release checks, then confirm the changelog and open issues.\n\n| Area | Status | Notes |\n| --- | --- | --- |\n| Host tests | Pass | 42/42 suites green |\n| Diff review | Needs You | Awaiting plan sign-off |\n| Docs | Working | Changelog draft in progress |')
    ]);
    this.transcripts.set(this.transcriptKey(DEMO_MACHINE, 'demo-auto-summary'), [
      this.item('assistant', 'Three sample tasks are ready to inspect in this local demo:\n1. **Review release checks** (`needs_you`) — pre-release verification & open plan\n2. **Prepare the changelog** (`working`) — drafting highlights & bug fixes\n3. **Organize project notes** (`unread`) — finished architecture summary')
    ]);
    this.transcripts.set(this.transcriptKey(DEMO_MACHINE, 'demo-agent-review'), [
      this.item('user', 'Run the pre-release audit across ReleaseDesk and summarize what changed.'),
      this.item('assistant', 'The release checklist is ready for review.'),
      this.item('user', 'Show the detailed audit report and verification gate.'),
      this.item('assistant', '### Release Audit Summary\n\nAll host test suites passed and two files in `ReleaseDesk` are ready for sign-off.\n\n| Check | Result | Duration |\n| --- | --- | --- |\n| Unit & Domain Suite | Passed (42/42) | 1.8s |\n| Schema & Privacy Guard | Clean | 0.4s |\n| Bundle Packaging | Verified | 0.9s |\n\n```ts\nexport function verifyReleaseGate(checks: CheckResult[]): boolean {\n  return checks.every((item) => item.passed && !item.skipped);\n}\n```\n\n> A sample plan is waiting for your decision below.')
    ]);
    this.transcripts.set(this.transcriptKey(DEMO_MACHINE, 'demo-agent-changelog'), [
      this.item('user', 'Prepare a concise changelog draft.'),
      this.thinkingItem('Reviewing recent HarmonyOS UI commits and ReleaseDesk diff summary before drafting the v2.4 changelog.'),
      this.toolUseItem('Read', 'demo-changelog-read', demoRecord(['file_path'], ['/demo/ReleaseDesk/CHANGELOG.md'])),
      this.toolResultItem('demo-changelog-read', '# v2.4.0\n- Foldable 3-column Live Workbench\n- Liquid Glass card hierarchy'),
      this.toolUseItem('Edit', 'demo-changelog-edit', demoRecord(['file_path', 'old_string', 'new_string'], [
        '/demo/ReleaseDesk/src/release.ts',
        'export function verifyReleaseGate(checks: CheckResult[]): boolean {\n  return checks.every((item) => item.passed);\n}',
        'export function verifyReleaseGate(checks: CheckResult[]): boolean {\n  return checks.every((item) => item.passed && !item.skipped);\n}'
      ])),
      this.toolResultItem('demo-changelog-edit', 'Updated /demo/ReleaseDesk/src/release.ts (+1/-1)'),
      this.toolUseItem('Bash', 'demo-changelog-test', demoRecord(['command', 'description'], ['npm test -- release-gate', 'Run release-gate checks'])),
      this.toolResultItem('demo-changelog-test', '42 passed, 0 failed (1.4s)'),
      this.item('assistant', 'The local draft groups the sample changes by feature and fix.\n\n### Highlights\n- **Foldable Workbench**: side-by-side Pulse, Code, and Machines panes\n- **Liquid Glass**: borderless inset-grouped cards across light & dark schemes\n\n### Fixes\n- Normalized per-core load labels (`Load 2.4 · 0.2/core`)\n- Prevented duplicate row subtitles on narrow screens\n\n```typescript\nexport interface ReleaseGateCheck {\n  readonly suite: string;\n  readonly passed: number;\n  readonly skipped: number;\n}\n\nexport function verifyReleaseGate(checks: readonly ReleaseGateCheck[]): boolean {\n  return checks.length > 0 && checks.every((item) => item.passed > 0 && item.skipped === 0);\n}\n```')
    ]);
    this.transcripts.set(this.transcriptKey(DEMO_MACHINE, 'demo-agent-notes'), [
      this.item('user', 'Summarize the architectural notes in `/demo/Notes`.'),
      this.item('assistant', 'The sample notes are organized. This session is complete.\n\n- **Navigation**: 3-column tri-fold layout scales cleanly from 360vp to 1060vp+\n- **Offline Safety**: Demo data stays strictly in memory on-device')
    ]);
    this.transcripts.set(this.transcriptKey(DEMO_MACHINE, 'demo-archived-notes'), [
      this.item('user', 'Capture the Q4 design system alignment decisions.'),
      this.item('assistant', 'Summarized the Q4 Liquid Glass token alignment across iOS and HarmonyOS, including 44vp touch targets and semantic status washes.')
    ]);
    this.transcripts.set(this.transcriptKey(DEMO_MACHINE, 'demo-agent-archived'), [
      this.item('user', 'Audit the Q4 release tags and archive when done.'),
      this.item('assistant', 'Tagged and verified Q4 release artifacts. Ready for archive.')
    ]);
    const recentItems = this.transcripts.get(this.transcriptKey(DEMO_MACHINE, 'demo-recent-review')) || [];
    const reviewItems = this.transcripts.get(this.transcriptKey(DEMO_MACHINE, 'demo-agent-review')) || [];
    if (recentItems.length >= 2) {
      this.starredData.push(demoRecord(
        ['id', 'machine_id', 'session_id', 'item_id', 'conversation', 'role', 'title', 'speaker', 'written_at', 'starred_at', 'text'],
        ['demo-star-1', DEMO_MACHINE, 'demo-recent-review', Wire.text(recentItems[1], 'id'), 'assistant', 'assistant',
          'Release checklist', 'Persona', '2026-11-27T08:40:00Z', '2026-11-27T08:45:00Z', Wire.text(recentItems[1], 'text')]
      ));
    }
    if (reviewItems.length >= 4) {
      this.starredData.push(demoRecord(
        ['id', 'machine_id', 'session_id', 'item_id', 'conversation', 'role', 'title', 'speaker', 'written_at', 'starred_at', 'text'],
        ['demo-star-2', DEMO_MACHINE, 'demo-agent-review', Wire.text(reviewItems[3], 'id'), 'agent', 'assistant',
          'Review release checks', 'Claude', '2026-11-27T08:50:00Z', '2026-11-27T08:52:00Z', Wire.text(reviewItems[3], 'text')]
      ));
    }
    const demoRuleSuggestion = demoRecord(
      ['id', 'kind', 'text', 'creates', 'count', 'scope'],
      [
        'demo-rule-suggest-1',
        'task_trust',
        'Always start ReleaseDesk review tasks directly?',
        'tasks.trust',
        3,
        demoRecord(['machine_id', 'machine', 'directory'], [DEMO_MACHINE, 'My Mac', '/demo/ReleaseDesk'])
      ]
    );
    this.interactionsData = [demoRecord(['id', 'machine_id', 'session_id', 'manager', 'status', 'revision', 'suggestion', 'interaction'], [
      'demo-plan-review', DEMO_MACHINE, 'demo-agent-review', DEMO_MANAGER, 'open', 'demo-rev-1', cloneRecord(demoRuleSuggestion),
      demoRecord(['id', 'kind', 'plan', 'status', 'suggestion'], ['demo-plan-review', 'plan', 'Run the sample release checklist in this local demo?', 'open', cloneRecord(demoRuleSuggestion)])
    ])];
    this.todosData = [
      demoRecord(['id', 'manager', 'title', 'notes', 'state', 'status', 'stage', 'origin', 'created_at', 'updated_at', 'supervision', 'agents', 'spaces'], [
        'demo-todo-release', DEMO_MANAGER, 'Review the release checklist', 'Check the sample release notes and confirm the open plan.',
        'needs_you', 'needs_you', 'review', this.mainId, '2026-11-10T08:00:00Z', '2026-11-27T08:50:00Z',
        demoRecord(['state', 'pending_since'], ['user_check', '2026-11-27T08:50:00Z']),
        [demoRecord(
          ['machine_id', 'session_id', 'title', 'cwd', 'agent', 'source', 'linked_at'],
          [DEMO_MACHINE, 'demo-agent-review', 'Review release checks', '/demo/ReleaseDesk', '', 'manual', '2026-11-11T09:00:00Z'])], []]),
      demoRecord(['id', 'manager', 'title', 'notes', 'state', 'status', 'stage', 'origin', 'created_at', 'updated_at', 'agents', 'spaces', 'pr_url', 'wait'], [
        'demo-todo-docs', DEMO_MANAGER, 'Prepare release notes', 'Collect the sample changes in one concise note.',
        'working', 'working', 'develop', this.mainId, '2026-11-10T09:00:00Z', '2026-11-27T08:55:00Z', [demoRecord(
          ['machine_id', 'session_id', 'title', 'cwd', 'agent', 'source', 'linked_at'],
          [DEMO_MACHINE, 'demo-agent-changelog', 'Prepare the changelog', '/demo/ReleaseDesk', 'codex', 'manual', '2026-11-27T08:55:00Z'])], [],
        'https://github.com/example/releasedesk/pull/142',
        demoRecord(['kind', 'waiting_on', 'reason', 'url', 'pr_blockers', 'since'], [
          'pr_merge', 'PR #142', 'Update HarmonyOS release notes', 'https://github.com/example/releasedesk/pull/142',
          ['review_required', 'checks_pending'], '2026-11-27T08:00:00Z'
        ])]),
      demoRecord(['id', 'manager', 'title', 'notes', 'state', 'status', 'origin', 'done_at', 'created_at', 'updated_at', 'agents', 'spaces'], [
        'demo-todo-done', DEMO_MANAGER, 'Check the example project', 'This sample task is complete.', 'done', 'done',
        this.mainId, '2026-11-10T10:00:00Z', '2026-11-09T10:00:00Z', '2026-11-10T10:00:00Z', [], []])
    ];
    this.todoDecisionsData.set('demo-todo-release', [
      demoRecord(
        ['id', 'todo_id', 'question', 'choice', 'reason', 'source', 'source_machine', 'source_session', 'source_message', 'supersedes_id', 'superseded_by', 'created_at'],
        ['demo-dec-release-1', 'demo-todo-release', 'Rollout cadence for Wave 7?', 'Split across two builds', 'Keep initial diff smaller', 'manual', '', '', '', '', 'demo-dec-release-2', '2026-11-26T10:00:00Z']
      ),
      demoRecord(
        ['id', 'todo_id', 'question', 'choice', 'reason', 'source', 'source_machine', 'source_session', 'source_message', 'supersedes_id', 'superseded_by', 'created_at'],
        ['demo-dec-release-2', 'demo-todo-release', 'Rollout cadence for Wave 7?', 'Ship full parity pack together', 'All 5 features verified end-to-end', 'manual', '', '', '', 'demo-dec-release-1', '', '2026-11-27T08:20:00Z']
      ),
      demoRecord(
        ['id', 'todo_id', 'question', 'choice', 'reason', 'source', 'source_machine', 'source_session', 'source_message', 'supersedes_id', 'superseded_by', 'created_at'],
        ['demo-dec-release-3', 'demo-todo-release', 'Require zero hardcoded hex literals?', 'Enforce Colors.* and Tokens.*', 'Match Design System Wave 5 rules', 'card', DEMO_MACHINE, 'demo-agent-review', 'demo-item-card', '', '', '2026-11-27T08:45:00Z']
      )
    ]);
    this.todoDecisionsData.set('demo-todo-docs', [
      demoRecord(
        ['id', 'todo_id', 'question', 'choice', 'reason', 'source', 'source_machine', 'source_session', 'source_message', 'supersedes_id', 'superseded_by', 'created_at'],
        ['demo-dec-docs-1', 'demo-todo-docs', 'Target devices for changelog notes?', 'Phone, Foldable, and Mate XT 2', 'Cover all adaptive breakpoints', 'outcome', '', '', '', '', '', '2026-11-27T08:50:00Z']
      )
    ]);
    const demoChatNotes = this.knowledgeEntry('chats', 'demo-chat-notes', 'Planning discussion', 'A synthetic discussion about the sample release.', 'The team agreed to review the plan before preparing the release notes.', ['demo', 'planning']);
    demoChatNotes['recent'] = '## 2026-11-11\n- 08:15 Mina Example: Can someone review the sample release checklist?\n- 08:20 Demo user: Checking now.';
    const demoRecordingNotes = this.knowledgeEntry('recordings', 'demo-recording-notes', 'Planning notes', 'A synthetic recording entry.', 'No audio file or personal recording is stored in the local demo.', ['demo', 'planning']);
    const recMeta = Wire.object(demoRecordingNotes, 'meta');
    recMeta['started_at'] = '2026-11-27T08:00:00Z';
    recMeta['duration'] = 420;
    recMeta['complete'] = true;
    recMeta['bookmarks'] = [
      demoRecord(['at', 'label'], [140, 'Ship parity pack together']),
      demoRecord(['at', 'label'], [320, 'Heatmap window decision'])
    ];
    demoRecordingNotes['meta'] = recMeta;
    demoRecordingNotes['segments'] = [
      demoRecord(['start', 'speaker', 'text'], [18, 'Mina', 'Let us review the release checklist and confirm all verification gates before tagging.']),
      demoRecord(['start', 'speaker', 'text'], [140, 'Alex', 'Should we ship Token Usage and Workspace parity together in one parity pack?']),
      demoRecord(['start', 'speaker', 'text'], [235, 'Mina', 'Yes, ship together in one parity pack after full verification passes on HarmonyOS.']),
      demoRecord(['start', 'speaker', 'text'], [320, 'Alex', 'On compact phones, keep the 12-week heatmap compact so Usage by Project stays visible.']),
      demoRecord(['start', 'speaker', 'text'], [390, 'Mina', 'Agreed. Let us record the decision on the release checklist to-do.'])
    ];
    demoRecordingNotes['minutes'] = demoRecord(['version', 'markdown', 'actions', 'debates'], [
      1,
      '### Summary\nReviewed the release checklist and agreed on staged rollout timing.',
      [demoRecord(['id', 'what'], ['demo-action-1', 'Confirm release checklist sign-off'])],
      [
        demoRecord(['question', 'topic', 'status', 'at', 'positions', 'outcome'], [
          'Ship Token Usage and Workspace parity together?',
          'Release scope',
          'settled',
          140,
          [
            demoRecord(['stance', 'at', 'who'], ['Ship together in one parity pack', 142, 'Mina']),
            demoRecord(['stance', 'at', 'who'], ['Split across two minor builds', 188, 'Alex'])
          ],
          demoRecord(['text', 'at', 'decision'], ['Ship together in one parity pack after full verification', 240, 0])
        ]),
        demoRecord(['question', 'topic', 'status', 'at', 'positions'], [
          'Default heatmap window on compact phones',
          'Usage UI',
          'open',
          320,
          [
            demoRecord(['stance', 'at', 'who'], ['Show 12 weeks for full trend context', 325, 'Alex']),
            demoRecord(['stance', 'at', 'who'], ['Show 8 weeks on narrow screens', 360, 'Mina'])
          ]
        ])
      ]
    ]);
    this.knowledgeData = [
      this.knowledgeEntry('projects', 'demo-project-release', 'Release Desk', 'A sample project for trying the local workspace.', 'The Release Desk example keeps the checklist, release notes, and follow-up tasks together.', ['demo', 'release'], demoRecord(['kind', 'status', 'dirs'], ['short', 'active', [DEMO_MACHINE + ':/demo/ReleaseDesk']])),
      this.knowledgeEntry('docs', 'demo-doc-release', 'Release checklist', 'A short checklist for the example release.', '1. Review the plan.\n2. Check the changelog.\n3. Close or reopen the follow-up task.', ['demo', 'checklist']),
      demoChatNotes,
      this.knowledgeEntry('people', 'demo-person-reviewer', 'Alex Example', 'A sample reviewer in the demo workspace.', 'This contact is synthetic and is not connected to a real account.', ['demo', 'review']),
      this.knowledgeEntry('meetings', 'demo-meeting-kickoff', 'Release kickoff', 'A sample meeting record.', 'Review scope, identify risks, and agree on the next step.', ['demo', 'release']),
      this.knowledgeEntry('events', 'demo-event-check', 'Checklist review', 'A sample project event.', 'The example checklist was added to the Release Desk project.', ['demo', 'checklist']),
      demoRecordingNotes
    ];
    this.mailData = [demoRecord(['account_id', 'mailbox', 'uid', 'message_id', 'subject', 'from', 'to', 'date', 'text', 'seen', 'attachments'], [
      DEMO_MAIL_ACCOUNT, 'INBOX', 42, '<demo-mail-42@example.test>', 'Release notes review',
      demoRecord(['name', 'email'], ['Alex Example', 'alex@example.test']),
      [demoRecord(['name', 'email'], ['Demo Inbox', 'inbox@example.test'])], '2026-11-11T08:30:00Z',
      'Could you review the sample release notes and share any missing items?', false, []
    ])];
    this.inboxData = [
      demoRecord(['id', 'manager', 'kind', 'source', 'account', 'account_name', 'title', 'sender', 'summary', 'state', 'unread', 'version', 'received_at', 'conversation', 'handoffs'], [
        'demo-inbox-lark', DEMO_MANAGER, 'inbox', 'lark', DEMO_LARK_ACCOUNT, 'Demo Team', 'Release room', 'Mina Example',
        'Can someone review the sample release checklist?', 'unread', true, 'demo-lark-v1', '2026-11-11T08:15:00Z',
        demoRecord(['source', 'id', 'account', 'name', 'start_position', 'topic'],
          ['lark', DEMO_LARK_CONVERSATION, DEMO_LARK_ACCOUNT, 'Release room', '1001', false]), []]),
      demoRecord(['id', 'manager', 'kind', 'source', 'account', 'account_name', 'title', 'sender', 'summary', 'state', 'unread', 'version', 'received_at', 'mail', 'reply_target', 'handoffs'], [
        'demo-inbox-mail', DEMO_MANAGER, 'inbox', 'mail', DEMO_MAIL_ACCOUNT, 'Demo Mailbox', 'Release notes review', 'Alex Example',
        'Could you review the sample release notes?', 'unread', true, 'demo-mail-v1', '2026-11-11T08:30:00Z',
        demoRecord(['account_id', 'mailbox', 'uids', 'account_name', 'from'], [DEMO_MAIL_ACCOUNT, 'INBOX', [42], 'Demo Mailbox',
          demoRecord(['name', 'email'], ['Alex Example', 'alex@example.test'])]),
        demoRecord(['uid', 'version'], [42, 'demo-mail-target-v1']), []])
    ];
    this.larkMessages = [
      demoRecord(['id', 'position', 'sender', 'sender_name', 'text', 'mine', 'state', 'can_reply', 'thread_kind', 'reactions'], [
        'demo-lark-message-1', '1001', demoRecord(['id', 'name'], ['demo-mina', 'Mina Example']), 'Mina Example',
        'Can someone review the sample release checklist?', false, 'unread', true, 'message', []]),
      demoRecord(['id', 'position', 'sender', 'sender_name', 'text', 'mine', 'state', 'can_reply', 'thread_kind', 'reactions'], [
        'demo-lark-message-2', '1002', demoRecord(['id', 'name'], ['demo-you', 'Demo user']), 'Demo user',
        'I will check the sample plan first.', true, 'read', true, 'message', []]),
      demoRecord(['id', 'position', 'sender', 'sender_name', 'text', 'mine', 'state', 'can_reply', 'thread_kind', 'thread_root', 'reactions'], [
        'demo-lark-message-3', '1003', demoRecord(['id', 'name'], ['demo-mina', 'Mina Example']), 'Mina Example',
        'Thanks. Leave a note here when you are done.', false, 'read', true, 'message', 'demo-lark-message-1', []])
    ];
  }

  /** `seed` is an explicit reset for callers that want the original sample data again. */
  seed(): DemoRoster { this.reset(); return this.roster(); }

  roster(): DemoRoster {
    const roster = new DemoRoster(); roster.features = this.features.slice(); roster.mainId = this.mainId; roster.leaders = this.leaderIds.slice();
    roster.machines = copyRows([this.machineData()], 'machine'); roster.sessions = copyRows(this.activeSessions(), 'session');
    roster.conversations = copyRows(this.conversationsData, 'conversation'); roster.todos = copyRows(this.todosData, 'todo');
    roster.knowledge = copyRows(this.knowledgeData.filter((item: DemoRecord) => Wire.text(item, 'kind') === 'projects'), 'knowledge');
    roster.inbox = copyRows(this.inboxData, 'inbox');
    roster.sessions.forEach((row: ResourceRow) => row.group = DEMO_HUB);
    roster.machines.forEach((row: ResourceRow) => row.group = DEMO_HUB);
    roster.conversations.forEach((row: ResourceRow) => { row.group = DEMO_HUB; row.machine = DEMO_MACHINE; row.session = row.id; });
    roster.todos.forEach((row: ResourceRow) => row.group = DEMO_HUB);
    roster.knowledge.forEach((row: ResourceRow) => { row.group = DEMO_HUB; row.path = Wire.text(row.data(), 'kind'); });
    roster.inbox.forEach((row: ResourceRow) => row.group = DEMO_HUB);
    return roster;
  }

  /** Chat rows for the demo-specific ClientModel.loadChat path. */
  history(row: ResourceRow): ChatMessage[] {
    return this.historyItems(row).map((item: DemoRecord): ChatMessage => {
      const message = new ChatMessage(Wire.text(item, 'id'), Wire.text(item, 'role', 'assistant'), Wire.text(item, 'text'));
      message.at = Date.parse(Wire.text(item, 'ts')) || 0; message.itemId = message.id;
      return message;
    });
  }

  historyItems(row: ResourceRow): DemoRecord[] {
    const key = this.transcriptKey(row.machine || Wire.text(row.data(), 'machine_id'), row.session || row.id);
    return cloneRecords(this.transcripts.get(key) || []);
  }

  /** Add a user message and a clearly synthetic reply to this demo chat only. */
  sendLocal(row: ResourceRow, text: string): ChatMessage[] {
    const value = nonEmpty(text, MAX_TEXT_BYTES);
    const session = this.chatTarget(row);
    this.appendChat(Wire.text(session, 'machine_id'), Wire.text(session, 'session_id'), value, 'local-send-' + (++this.counter));
    return this.history(row);
  }

  async call(path: string, method: string = 'GET', body: DemoRecord = {}, operationId: string = '', admit?: () => boolean,
    _completedAsAck: boolean = false): Promise<DemoRecord> {
    const request = parsePath(path), parts = request.parts, query = request.query;
    const verb = method.toUpperCase();
    if (!['GET', 'POST', 'PUT', 'PATCH', 'DELETE'].includes(verb)) throw new HttpFailure(405, 'demo_unsupported');
    if (query['manager'] && query['manager'] !== DEMO_MANAGER) throw new HttpFailure(404, 'demo_owner_not_found');
    if (body !== undefined && body !== null && (typeof body !== 'object' || Array.isArray(body))) throw new HttpFailure(400, 'demo_invalid_input');
    const encodedBody = JSON.stringify(body || {});
    if (!encodedBody || utf8Bytes(encodedBody) > MAX_BODY_BYTES) throw new HttpFailure(413, 'demo_payload_too_large');
    const input = Wire.record(JSON.parse(encodedBody) as Object);
    if (admit && !admit()) throw new HttpFailure(0, 'request_updated', false);

    const resource = parts[0];
    if (resource==='model-config') return this.modelConfig(parts,verb,input);
    if (resource === 'assistant') return this.assistant(parts, query, verb, input, operationId);
    if (resource === 'managers') return this.managers(parts, verb);
    if (resource === 'machines') return this.machinesRoute(parts, query, verb);
    if (resource === 'metrics') return this.allMetricsRoute(verb);
    if (resource === 'usage') return this.usageRoute(parts, query, verb);
    if (resource === 'agents') return this.agentsRoute(parts, verb);
    if (resource === 'sessions') return this.sessionsRoute(parts, verb, input, operationId);
    if (resource === 'interactions') return this.interactions(parts, verb, input);
    if (resource === 'pins' || resource === 'mutes') return this.preferences(resource, parts, verb, input);
    if (resource === 'todos') return this.todosRoute(parts, query, verb, input);
    if (resource === 'kb-search') return this.searchKnowledge(query, verb);
    if (resource === 'kb') return this.knowledgeRoute(parts, query, verb, input);
    if (resource === 'inbox') return this.inboxRoute(parts, verb, input, operationId);
    if (resource === 'messaging') return this.messaging(parts, query, verb, input, operationId);
    if (resource === 'lark') return this.legacyLark(parts, query, verb, input);
    if (resource === 'mail') return this.mail(parts, query, verb, input);
    throw new HttpFailure(404, 'demo_unsupported');
  }

  /** Binary uploads are intentionally unavailable in the local demo. */
  async upload(_path: string, _bytes: ArrayBuffer, _admit?: () => boolean): Promise<DemoRecord> {
    throw new HttpFailure(501, 'demo_unsupported');
  }

  close(): void { /* The demo never connects its inert transport. */ }

  private machineData(): DemoRecord {
    const repositories: DemoRecord[] = [demoRecord(['path', 'name'], ['/demo/ReleaseDesk', 'ReleaseDesk'])];
    const system: DemoRecord = demoRecord(['os', 'distro', 'name', 'version', 'cores', 'memory'],
      ['darwin', 'macos', 'macOS Sequoia', '15.6', 12, 38654705664]);
    return demoRecord(['id', 'manager', 'name', 'machine_id', 'platform', 'os', 'online', 'trust', 'pending', 'pending_confirm', 'directories', 'repos', 'system'],
      [DEMO_MACHINE, DEMO_MANAGER, 'My Mac', DEMO_MACHINE, 'darwin', 'macOS', true, 'personal', false, false,
        ['/demo/ReleaseDesk', '/demo/Notes'], repositories, system]);
  }

  private activeSessions(): DemoRecord[] { return this.sessionsData.filter((item: DemoRecord) => !Wire.bool(item, 'archived')); }

  private knowledgeEntry(kind: string, id: string, title: string, summary: string, content: string, tags: string[], extra: DemoRecord = {}): DemoRecord {
    const meta = cloneRecord(extra);
    meta['importance'] = 'P2'; meta['relevance'] = 75; meta['tags'] = tags.slice();
    return demoRecord(['id', 'kind', 'manager', 'title', 'name', 'summary', 'content', 'meta', 'tags', 'updated_at', 'related'],
      [id, kind, DEMO_MANAGER, title, title, summary, content, meta, tags.slice(), '2026-11-11T09:00:00Z', []]);
  }

  private item(role: string, text: string, clientId: string = ''): DemoRecord {
    this.messageSequence++;
    const item = demoRecord(['id', 'seq', 'role', 'kind', 'text', 'ts'],
      ['demo-item-' + this.messageSequence, this.messageSequence, role, 'text', textWithin(text, MAX_TEXT_BYTES), '2026-11-11T09:00:00Z']);
    if (clientId) item['client_msg_id'] = clientId;
    return item;
  }

  private thinkingItem(text: string): DemoRecord {
    this.messageSequence++;
    return demoRecord(['id', 'seq', 'role', 'kind', 'text', 'ts'],
      ['demo-item-' + this.messageSequence, this.messageSequence, 'assistant', 'thinking', textWithin(text, MAX_TEXT_BYTES), '2026-11-27T08:56:00Z']);
  }

  private toolUseItem(tool: string, useId: string, input: DemoRecord): DemoRecord {
    this.messageSequence++;
    return demoRecord(['id', 'seq', 'role', 'kind', 'tool', 'tool_name', 'tool_use_id', 'input', 'ts'],
      ['demo-item-' + this.messageSequence, this.messageSequence, 'assistant', 'tool_use', tool, tool, useId, cloneRecord(input), '2026-11-27T08:56:05Z']);
  }

  private toolResultItem(useId: string, text: string): DemoRecord {
    this.messageSequence++;
    return demoRecord(['id', 'seq', 'role', 'kind', 'tool_use_id', 'text', 'ts'],
      ['demo-item-' + this.messageSequence, this.messageSequence, 'user', 'tool_result', useId, textWithin(text, MAX_TEXT_BYTES), '2026-11-27T08:56:10Z']);
  }

  private dequeueSessionQueue(record: DemoRecord, machine: string, session: string, appendReply: boolean): number {
    const raw = record['queued'];
    if (!Array.isArray(raw) || raw.length === 0) {
      record['queued'] = [];
      record['queued_relayed'] = 0;
      return 0;
    }
    const items: DemoRecord[] = [];
    for (const entry of raw as Object[]) {
      const text = typeof entry === 'string'
        ? entry.trim()
        : (entry && typeof entry === 'object' ? Wire.text(entry as DemoRecord, 'text', Wire.text(entry as DemoRecord, 'body')).trim() : '');
      if (text) items.push(this.item('user', text));
    }
    if (items.length > 0) {
      if (appendReply) {
        const reply = 'Processed queued follow-ups in the local demo.';
        items.push(this.item('assistant', reply));
        record['last_text'] = reply;
      }
      this.appendTranscript(machine, session, items);
    }
    record['queued'] = [];
    record['queued_relayed'] = 0;
    return items.length;
  }

  private transcriptKey(machine: string, session: string): string { return JSON.stringify([machine, session]); }

  private appendTranscript(machine: string, session: string, items: DemoRecord[]): void {
    const key = this.transcriptKey(machine, session), current = this.transcripts.get(key) || [];
    const next = current.concat(items);
    let kept = next.slice(-MAX_TRANSCRIPT_ITEMS);
    while (kept.length > 2 && utf8Bytes(JSON.stringify(kept)) > 1024 * 1024) kept = kept.slice(2);
    this.transcripts.set(key, kept);
  }

  private appendChat(machine: string, session: string, text: string, clientId: string): void {
    const target = this.findChat(machine, session);
    if (!target) throw new HttpFailure(404, 'demo_chat_not_found');
    const current = this.transcripts.get(this.transcriptKey(machine, session)) || [];
    if (current.some((item: DemoRecord) => Wire.text(item, 'client_msg_id') === clientId)) return;
    this.dequeueSessionQueue(target, machine, session, false);
    this.appendTranscript(machine, session, [this.item('user', text, clientId), this.item('assistant', 'Received in the local demo. No message was sent to a real agent or service.')]);
  }

  private chatTarget(row: ResourceRow): DemoRecord {
    if (!row || !['session', 'conversation'].includes(row.kind)) throw new HttpFailure(400, 'demo_chat_not_found');
    const machine = row.machine || Wire.text(row.data(), 'machine_id');
    const session = row.session || row.id || Wire.text(row.data(), 'session_id');
    if (!this.findChat(machine, session)) throw new HttpFailure(404, 'demo_chat_not_found');
    return demoRecord(['machine_id', 'session_id'], [machine, session]);
  }

  private findChat(machine: string, session: string): DemoRecord | undefined {
    if (machine !== DEMO_MACHINE || !session) return undefined;
    return this.sessionsData.concat(this.conversationsData).find((item: DemoRecord) =>
      Wire.text(item, 'machine_id') === machine && (Wire.text(item, 'session_id') || Wire.text(item, 'id')) === session);
  }

  private replaceChatRecord(previous: DemoRecord, next: DemoRecord): void {
    if (this.sessionsData.includes(previous)) this.sessionsData = this.sessionsData.map((item: DemoRecord) => item === previous ? next : item);
    else this.conversationsData = this.conversationsData.map((item: DemoRecord) => item === previous ? next : item);
  }

  private ensureRoom(rows: DemoRecord[]): void { if (rows.length >= MAX_DATA_ROWS) throw new HttpFailure(413, 'demo_limit_reached'); }

  private rememberOperation(id: string, result: DemoRecord): void {
    this.operationResults.set(id, cloneRecord(result)); this.operationOrder.push(id);
    while (this.operationOrder.length > MAX_CACHED_OPERATIONS) {
      const expired = this.operationOrder.shift() || '';
      if (expired && !this.operationOrder.includes(expired)) this.operationResults.delete(expired);
    }
  }

  private assistant(parts: string[], query: Record<string, string>, method: string, body: DemoRecord, _operationId: string): DemoRecord {
    if (parts.length === 2 && parts[1] === 'starred') {
      if (method === 'GET') {
        if (query['index'] === '1') {
          const refs = this.starredData.map((item: DemoRecord): DemoRecord => demoRecord(
            ['id', 'machine_id', 'session_id', 'item_id', 'conversation'],
            [Wire.text(item, 'id'), Wire.text(item, 'machine_id'), Wire.text(item, 'session_id'), Wire.text(item, 'item_id'), Wire.text(item, 'conversation')]
          ));
          return demoRecord(['refs', 'can_write'], [refs, true]);
        }
        return demoRecord(['stars', 'can_write'], [cloneRecords(this.starredData), true]);
      }
      if (method === 'POST') {
        const machine = nonEmpty(Wire.text(body, 'machine_id'), 200);
        const session = nonEmpty(Wire.text(body, 'session_id'), 200);
        const itemId = nonEmpty(Wire.text(body, 'item_id'), 512);
        const conversation = Wire.text(body, 'conversation');
        const role = Wire.text(body, 'role');
        if (!['assistant', 'agent'].includes(conversation) || !['user', 'assistant'].includes(role)) {
          throw new HttpFailure(400, 'demo_invalid_input');
        }
        const rawText = Wire.text(body, 'text').replace(/\u0000/g, '');
        if (!rawText.trim()) throw new HttpFailure(400, 'demo_invalid_input');
        const fp = Wire.text(body, 'source_fingerprint').toLowerCase();
        const existing = this.starredData.find((s: DemoRecord) =>
          Wire.text(s, 'machine_id') === machine &&
          Wire.text(s, 'session_id') === session &&
          Wire.text(s, 'item_id') === itemId &&
          Wire.text(s, 'conversation') === conversation &&
          Wire.text(s, 'source_fingerprint').toLowerCase() === fp
        );
        if (existing) return demoRecord(['star'], [cloneRecord(existing)]);
        this.ensureRoom(this.starredData);
        const scalars = Array.from(rawText);
        const clipped = scalars.length > 4000 ? scalars.slice(0, 4000).join('') : rawText;
        const omitted = Math.max(0, Wire.number(body, 'omitted_chars', 0)) + Math.max(0, scalars.length - 4000);
        const star = demoRecord(
          ['id', 'machine_id', 'session_id', 'item_id', 'conversation', 'role', 'title', 'speaker', 'written_at', 'starred_at', 'text'],
          ['demo-star-' + (++this.counter), machine, session, itemId, conversation, role,
            Wire.text(body, 'title').trim().slice(0, 200), Wire.text(body, 'speaker').trim().slice(0, 200),
            Wire.text(body, 'written_at') || '2026-11-27T09:05:00Z', '2026-11-27T09:10:00Z', clipped]
        );
        if (fp) star['source_fingerprint'] = fp;
        if (omitted > 0) star['omitted_chars'] = omitted;
        if (Wire.text(body, 'source_manager')) star['source_manager'] = Wire.text(body, 'source_manager');
        this.starredData.unshift(star);
        return demoRecord(['star'], [cloneRecord(star)]);
      }
    }
    if (parts.length === 3 && parts[1] === 'starred' && method === 'DELETE') {
      const targetId = parts[2];
      const idx = this.starredData.findIndex((item: DemoRecord) => Wire.text(item, 'id') === targetId);
      if (idx < 0) return demoRecord([], []);
      const removed = cloneRecord(this.starredData[idx]);
      this.starredData = this.starredData.filter((_: DemoRecord, i: number) => i !== idx);
      return demoRecord(['removed'], [removed]);
    }
    if (parts.length === 4 && parts[1] === 'rule-suggestions' && ['always', 'not-now'].includes(parts[3]) && method === 'POST') {
      const suggestionId = parts[2];
      if (!suggestionId || this.settledRuleSuggestions.has(suggestionId)) {
        throw new HttpFailure(404, 'rule_suggestion_gone');
      }
      this.settledRuleSuggestions.add(suggestionId);
      for (const req of this.interactionsData) {
        const inner = Wire.object(req, 'interaction');
        if (Wire.text(Wire.object(req, 'suggestion'), 'id') === suggestionId) {
          req['suggestion'] = demoRecord([], []);
        }
        if (Wire.text(Wire.object(inner, 'suggestion'), 'id') === suggestionId) {
          inner['suggestion'] = demoRecord([], []);
          req['interaction'] = inner;
        }
      }
      if (parts[3] === 'always') {
        return demoRecord(['kind', 'creates'], ['task_trust', 'tasks.trust']);
      }
      return demoRecord(['ok'], [true]);
    }
    if (parts.length===1 && method==='PUT') {
      const keys = Object.keys(body);
      if (!keys.length || keys.some((k: string) => !['default_permission_mode', 'next_suggestions'].includes(k))) {
        throw new HttpFailure(400, 'demo_invalid_input');
      }
      if (Wire.has(body, 'default_permission_mode')) {
        const mode=Wire.text(body,'default_permission_mode');
        if (!['default','acceptEdits','plan','auto','bypassPermissions'].includes(mode)) throw new HttpFailure(400,'demo_invalid_input');
        this.defaultChatPermission=mode;
      }
      if (Wire.has(body, 'next_suggestions')) {
        if (typeof body['next_suggestions'] !== 'boolean') throw new HttpFailure(400, 'demo_invalid_input');
        this.nextSuggestionsEnabled = body['next_suggestions'] === true;
        const mainConv = this.conversationsData.find((c: DemoRecord) => Wire.text(c, 'id') === this.mainId);
        if (mainConv) {
          mainConv['suggestions'] = this.nextSuggestionsEnabled
            ? [
              demoRecord(['text', 'risky'], ['Summarize open release blockers', false]),
              demoRecord(['text', 'risky'], ['Show recent token usage', false]),
              demoRecord(['text', 'risky'], ['Run git push origin main', true])
            ]
            : [];
        }
      }
    }
    if (parts.length === 1 && (method === 'GET' || method==='PUT')) return demoRecord(['main', 'machine_id','manager','default_permission_mode','next_suggestions','leaders','conversations'],
      [this.mainId, DEMO_MACHINE,DEMO_MANAGER,this.defaultChatPermission,this.nextSuggestionsEnabled,this.leaderIds.slice(),cloneRecords(this.conversationsData)]);
    if (parts.length === 2 && parts[1] === 'standing-authorizations' && method === 'GET') {
      return demoRecord(['revision', 'authorizations'], [this.standingRevision, cloneRecords(this.standingData)]);
    }
    if (parts.length === 3 && parts[1] === 'standing-authorizations' && method === 'DELETE') {
      const targetId = parts[2];
      const exists = this.standingData.some((item: DemoRecord) => Wire.text(item, 'id') === targetId);
      if (exists) {
        this.standingData = this.standingData.filter((item: DemoRecord) => Wire.text(item, 'id') !== targetId);
        this.standingRevision++;
      }
      return demoRecord(['ok', 'revision'], [true, this.standingRevision]);
    }
    if (parts.length === 2 && parts[1] === 'search' && method === 'GET') {
      return this.searchAssistantConversations(query);
    }
    if (parts.length === 4 && parts[1] === 'directions' && (parts[3] === 'make' || parts[3] === 'end') && method === 'POST') {
      const id = parts[2];
      const conv = this.conversationsData.find((item: DemoRecord) => (Wire.text(item, 'session_id') || Wire.text(item, 'id')) === id);
      if (!conv) throw new HttpFailure(404, 'demo_chat_not_found');
      if (Wire.bool(conv, 'main')) throw new HttpFailure(400, 'cannot_pin_main');
      const next = this.leaderIds.filter((entry: string) => entry !== id);
      if (parts[3] === 'make') next.push(id);
      this.leaderIds = next;
      return demoRecord(['ok', 'leaders'], [true, this.leaderIds.slice()]);
    }
    if (parts.length === 4 && parts[1] === 'conversations' && parts[3] === 'always' && method === 'GET') {
      const id = parts[2];
      const conv = this.conversationsData.find((item: DemoRecord) => (Wire.text(item, 'session_id') || Wire.text(item, 'id')) === id);
      if (!conv) throw new HttpFailure(404, 'demo_chat_not_found');
      return demoRecord(['grants'], [cloneRecords(this.alwaysGrantsByConversation.get(id) || [])]);
    }
    if (parts.length === 5 && parts[1] === 'conversations' && parts[3] === 'always' && method === 'DELETE') {
      const id = parts[2], keyHash = parts[4];
      const conv = this.conversationsData.find((item: DemoRecord) => (Wire.text(item, 'session_id') || Wire.text(item, 'id')) === id);
      if (!conv) throw new HttpFailure(404, 'demo_chat_not_found');
      const current = this.alwaysGrantsByConversation.get(id) || [];
      this.alwaysGrantsByConversation.set(id, current.filter((item: DemoRecord) => Wire.text(item, 'key_hash') !== keyHash));
      return demoRecord(['ok', 'restart_required'], [true, false]);
    }
    if (parts.length === 2 && parts[1] === 'conversations' && method === 'POST') {
      if (Wire.bool(body, 'main')) {
        const main = this.conversationsData.find((item: DemoRecord) => Wire.bool(item, 'main'));
        if (!main) throw new HttpFailure(404, 'demo_chat_not_found');
        return demoRecord(['session'], [cloneRecord(main)]);
      }
      this.ensureRoom(this.conversationsData);
      const id = 'demo-conversation-' + (++this.counter);
      const mode=Wire.text(body,'permission_mode',this.defaultChatPermission);
      if (!['default','acceptEdits','plan','auto','bypassPermissions'].includes(mode)) throw new HttpFailure(400,'demo_invalid_input');
      const conversation = demoRecord(['id', 'session_id', 'machine_id', 'manager', 'title', 'main', 'kind', 'archived', 'can_fork', 'permission_mode', 'updated_at'],
        [id, id, DEMO_MACHINE, DEMO_MANAGER, 'New conversation', false, 'recent', false, true, mode, '2026-11-27T09:10:00Z']);
      this.conversationsData.push(conversation); this.transcripts.set(this.transcriptKey(DEMO_MACHINE, id), []);
      return demoRecord(['session'], [cloneRecord(conversation)]);
    }
    if (parts.length === 4 && parts[1] === 'conversations' && (parts[3] === 'archive' || parts[3] === 'unarchive') && method === 'POST') {
      const conv = this.conversationsData.find((item: DemoRecord) => (Wire.text(item, 'session_id') || Wire.text(item, 'id')) === parts[2]);
      if (!conv) throw new HttpFailure(404, 'demo_chat_not_found');
      if (Wire.bool(conv, 'main')) throw new HttpFailure(400, 'cannot_archive_main');
      const archived = parts[3] === 'archive';
      conv['archived'] = archived;
      conv['updated_at'] = '2026-11-27T09:10:00Z';
      return demoRecord(['ok', 'archived'], [true, archived]);
    }
    if (parts.length === 4 && parts[1] === 'conversations' && parts[3] === 'feed' && method === 'GET') {
      if (!this.findChat(DEMO_MACHINE, parts[2])) throw new HttpFailure(404, 'demo_chat_not_found');
      if (parts[2] === this.mainId) {
        const emptyItems: DemoRecord[] = [];
        const emptyPlans: DemoRecord = {};
        const taskCards: DemoRecord[] = [
          demoRecord(['id', 'todo_id', 'state', 'at'], ['demo-card-release', 'demo-todo-release', 'needs_you', '2026-11-27T08:50:00Z']),
          demoRecord(['id', 'todo_id', 'state', 'at'], ['demo-card-docs', 'demo-todo-docs', 'working', '2026-11-27T08:55:00Z'])
        ];
        return demoRecord(['items', 'plans', 'task_cards'], [emptyItems, emptyPlans, taskCards]);
      }
      return demoRecord(['items'], [[]]);
    }
    throw new HttpFailure(404, 'demo_unsupported');
  }

  private searchAssistantConversations(query: Record<string, string>): DemoRecord {
    const rawQ = (query['q'] || '').trim();
    if (!rawQ) return demoRecord(['results', 'partial'], [[], false]);
    const needle = rawQ.toLowerCase();
    const limitRaw = Number(query['limit'] || '30');
    const limit = Number.isFinite(limitRaw) && limitRaw > 0 ? Math.min(100, Math.floor(limitRaw)) : 30;
    const results: DemoRecord[] = [];
    for (const conv of this.conversationsData) {
      const sid = Wire.text(conv, 'session_id') || Wire.text(conv, 'id');
      const machineId = Wire.text(conv, 'machine_id', DEMO_MACHINE);
      const title = Wire.text(conv, 'title');
      const archived = Wire.bool(conv, 'archived');
      const items = this.transcripts.get(this.transcriptKey(machineId, sid)) || [];
      const hits: DemoRecord[] = [];
      for (let i = items.length - 1; i >= 0; i--) {
        const item = items[i];
        const text = Wire.text(item, 'text').replace(/\r\n?|\n/g, ' ');
        const at = text.toLowerCase().indexOf(needle);
        if (at < 0) continue;
        const winStart = Math.max(0, at - 36);
        const winEnd = Math.min(text.length, at + rawQ.length + 84);
        const prefix = winStart > 0 ? '…' : '';
        const snippet = prefix + text.slice(winStart, winEnd) + (winEnd < text.length ? '…' : '');
        const matchStart = at - winStart + prefix.length;
        hits.push(demoRecord(
          ['item_id', 'seq', 'role', 'snippet', 'ranges'],
          [Wire.text(item, 'id'), Wire.number(item, 'seq', i + 1), Wire.text(item, 'role', 'assistant'), snippet,
            [[matchStart, matchStart + rawQ.length]]]
        ));
        if (hits.length >= 3) break;
      }
      const titleMatch = title.toLowerCase().includes(needle);
      if (hits.length > 0 || titleMatch) {
        results.push(demoRecord(
          ['session_id', 'machine_id', 'title', 'archived', 'title_match', 'hits'],
          [sid, machineId, title, archived, hits.length === 0 && titleMatch, hits]
        ));
      }
      if (results.length >= limit) break;
    }
    return demoRecord(['results', 'partial'], [results, false]);
  }

  private modelConfig(parts:string[],method:string,body:DemoRecord):DemoRecord {
    const fields=['provider','model','effort','permission_mode'];
    const provider=demoRecord(['id','name','kind','agent'],['launcher.demo','Demo Agent','launcher','claude']);
    const effective=demoRecord(['provider','model','permission_mode'],['launcher.demo','demo-model','default']);
    if (parts[1]==='providers' && parts[2]==='launcher.demo' && parts[3]==='models' && method==='GET')
      return demoRecord(['models'],[[demoRecord(['id','name'],['demo-model','Demo Model'])]]);
    if (parts[1]==='purposes' && parts[2]==='inbox.agent' && method==='PATCH') {
      if (Wire.number(body,'revision',-1)!==this.inboxModelRevision) throw new HttpFailure(409,'request_updated');
      for (const key of Object.keys(body)) {
        if (key!=='revision' && (!fields.includes(key) || body[key]!==null && typeof body[key]!=='string')) throw new HttpFailure(400,'demo_invalid_input');
        if (key==='permission_mode' && body[key]!==null && !['default','acceptEdits','auto','bypassPermissions'].includes(Wire.text(body,key))) throw new HttpFailure(400,'demo_invalid_input');
      }
      const next:DemoRecord={};
      for (const key of fields) {
        const value=Wire.has(body,key) ? body[key] : this.inboxModelBinding[key];
        if (typeof value==='string') next[key]=value;
      }
      this.inboxModelBinding=next;this.inboxModelRevision++;
    } else if (method!=='GET') throw new HttpFailure(404,'demo_unsupported');
    const noOverrides:DemoRecord={};
    const uses:DemoRecord[]=[demoRecord(['id','name'],['inbox.agent','Inbox AI twin'])];
    const parent=demoRecord(['id','category_id','name','kind','override','effective','revision','fields','uses'],
      ['agent.new','agent.new','Agents','launcher',noOverrides,effective,0,new Array<string>(),uses]);
    const live=cloneRecord(effective);for (const key of Object.keys(this.inboxModelBinding)) live[key]=this.inboxModelBinding[key];
    const twin=demoRecord(['id','category_id','parent','name','kind','override','effective','revision','fields'],
      ['inbox.agent','agent.new','agent.new','Inbox AI twin','launcher',cloneRecord(this.inboxModelBinding),live,this.inboxModelRevision,fields]);
    if (parts.length===1) return demoRecord(['purposes','providers'],[[parent,twin],[provider]]);
    if (parts.length===3 && parts[1]==='purposes' && ['agent.new','inbox.agent'].includes(parts[2]))
      return demoRecord(['purpose','providers'],[parts[2]==='inbox.agent' ? twin : parent,[provider]]);
    throw new HttpFailure(404,'demo_unsupported');
  }

  private managers(parts: string[], method: string): DemoRecord {
    if (parts.length !== 1 || method !== 'GET') throw new HttpFailure(404, 'demo_unsupported');
    const rows: DemoRecord[] = [demoRecord(['id', 'name', 'default'], [DEMO_MANAGER, 'Local demo', true])];
    return demoRecord(['default', 'managers'], [DEMO_MANAGER, rows]);
  }

  private machinesRoute(parts: string[], query: Record<string, string>, method: string): DemoRecord {
    if (parts.length === 1 && method === 'GET') return demoRecord(['machines'], [[this.machineData()]]);
    if (parts.length >= 2 && parts[1] === DEMO_MACHINE && method === 'GET') {
      if (parts.length === 2) return demoRecord(['machine'], [this.machineData()]);
      if (parts.length === 3 && parts[2] === 'metrics') return demoRecord(['metrics'], [this.demoMachineMetrics(query['range'] || '1h')]);
      if (parts.length === 3 && parts[2] === 'fs') return this.demoDirectory(query['path'] || '/demo/ReleaseDesk');
      if (parts.length === 4 && parts[2] === 'fs' && parts[3] === 'find') {
        return demoRecord(['files', 'truncated'], [['README.md', 'CHANGELOG.md', 'package.json', 'src/release.ts', 'tests/release.test.ts'], false]);
      }
      if (parts.length === 3 && parts[2] === 'file') return this.demoFileRead(query['path'] || '');
      if (parts.length === 4 && parts[2] === 'git' && parts[3] === 'status') return this.demoGitStatus();
      if (parts.length === 4 && parts[2] === 'git' && parts[3] === 'diff') return this.demoGitDiff(query['path'] || '');
    }
    throw new HttpFailure(404, 'demo_unsupported');
  }

  private allMetricsRoute(method: string): DemoRecord {
    if (method !== 'GET') throw new HttpFailure(404, 'demo_unsupported');
    return demoRecord(['machines'], [[this.demoMachineMetrics('1h')]]);
  }

  private agentsRoute(parts: string[], method: string): DemoRecord {
    if (parts.length === 1 && method === 'GET') return demoRecord(['sources'], [[]]);
    if (parts.length === 2 && parts[1] === 'sessions' && method === 'GET') return demoRecord(['sessions', 'total', 'hasMore'], [[], 0, false]);
    if (parts.length === 2 && parts[1] === 'hidden' && method === 'GET') return demoRecord(['sessions'], [[]]);
    if (parts.length === 4 && parts[1] === 'sessions' && ['hide', 'unhide'].includes(parts[3]) && method === 'POST') return demoRecord(['ok'], [true]);
    throw new HttpFailure(404, 'demo_unsupported');
  }

  private demoUsageTokens(input: number, cacheWrite: number, cacheRead: number, output: number, req: number): DemoRecord {
    return demoRecord(['in', 'cache_write', 'cache_read', 'out', 'req'], [input, cacheWrite, cacheRead, output, req]);
  }

  private demoUsageDaysList(count: number): DemoRecord[] {
    const out: DemoRecord[] = [];
    const anchor = Date.UTC(2026, 10, 27);
    for (let i = count - 1; i >= 0; i--) {
      const d = new Date(anchor - i * 86400000);
      const dateStr = String(d.getUTCFullYear()).padStart(4, '0') + '-' + String(d.getUTCMonth() + 1).padStart(2, '0') + '-' + String(d.getUTCDate()).padStart(2, '0');
      const wave = (i * 7 + 3) % 11;
      const active = i % 9 !== 4;
      const input = active ? 42000 + wave * 18000 : 0;
      const cacheWrite = active ? 28000 + wave * 9000 : 0;
      const cacheRead = active ? 480000 + wave * 190000 : 0;
      const output = active ? 16000 + wave * 6500 : 0;
      const req = active ? 18 + wave * 4 : 0;
      out.push(demoRecord(['date', 'in', 'cache_write', 'cache_read', 'out', 'req'], [dateStr, input, cacheWrite, cacheRead, output, req]));
    }
    return out;
  }

  private demoUsageProjectsList(): DemoRecord[] {
    const p1 = demoRecord(['key', 'kind', 'group', 'name', 'host', 'machines', 'share', 'tokens'], [
      'git:example.test/example/releasedesk', 'repo', 'development', 'example/releasedesk', 'example.test', 1, 0.58,
      this.demoUsageTokens(240000, 120000, 2850000, 96000, 84)
    ]);
    const p2 = demoRecord(['key', 'kind', 'group', 'name', 'host', 'machines', 'share', 'tokens'], [
      'local:notes', 'local', 'development', 'notes', '', 1, 0.24,
      this.demoUsageTokens(96000, 48000, 1180000, 42000, 36)
    ]);
    const p3 = demoRecord(['key', 'kind', 'group', 'name', 'host', 'machines', 'share', 'tokens'], [
      'folder:Docs', 'folder', 'other', 'Docs', '', 1, 0.11,
      this.demoUsageTokens(44000, 21000, 520000, 18000, 17)
    ]);
    const p4 = demoRecord(['key', 'kind', 'group', 'name', 'host', 'machines', 'share', 'tokens'], [
      'assistant', 'assistant', 'other', '', '', 1, 0.07,
      this.demoUsageTokens(28000, 12000, 310000, 14000, 14)
    ]);
    return [p1, p2, p3, p4];
  }

  private usageRoute(parts: string[], query: Record<string, string>, method: string): DemoRecord {
    if (parts.length === 2 && parts[1] === 'windows' && method === 'GET') {
      const now = Date.now();
      const iso = (offsetMs: number): string => new Date(now + offsetMs).toISOString();
      const claude = demoRecord(['id', 'provider', 'name', 'plan', 'sampled_at', 'stale_at', 'stale', 'local', 'machines', 'sources', 'windows'], [
        'demo-claude', 'claude', 'Claude', 'Max', iso(-20000), iso(3600000), false, false, ['My Mac'], ['oauth'], [
          demoRecord(['label', 'used_percent', 'resets_at', 'window_minutes'], ['5h', 34, iso(3.2 * 3600000), 300]),
          demoRecord(['label', 'used_percent', 'resets_at', 'window_minutes'], ['7d', 52, iso(92 * 3600000), 10080])
        ]
      ]);
      const codex = demoRecord(['id', 'provider', 'name', 'plan', 'sampled_at', 'stale_at', 'stale', 'local', 'machines', 'sources', 'windows'], [
        'demo-codex', 'codex', 'Codex', 'Pro', iso(-35000), iso(3600000), false, false, ['My Mac'], ['cli'], [
          demoRecord(['label', 'used_percent', 'resets_at', 'window_minutes'], ['5h', 18, iso(4.1 * 3600000), 300]),
          demoRecord(['label', 'used_percent', 'resets_at', 'window_minutes'], ['7d', 41, iso(116 * 3600000), 10080])
        ]
      ]);
      return demoRecord(['accounts'], [[claude, codex]]);
    }
    if (parts.length === 2 && parts[1] === 'days' && method === 'GET') {
      const count = Math.max(1, Math.min(371, Number(query['days'] || '84') || 84));
      return demoRecord(['usage'], [demoRecord(['zone', 'stored', 'days'], ['UTC', true, this.demoUsageDaysList(count)])]);
    }
    if (parts.length === 2 && parts[1] === 'hours' && method === 'GET') {
      const total = this.demoUsageTokens(408000, 201000, 4860000, 170000, 151);
      const machines = [
        demoRecord(['id', 'name', 'in', 'cache_write', 'cache_read', 'out', 'req'], [DEMO_MACHINE, 'My Mac', 298000, 146000, 3540000, 124000, 109]),
        demoRecord(['id', 'name', 'in', 'cache_write', 'cache_read', 'out', 'req'], ['demo-linux-box', 'Build Linux', 110000, 55000, 1320000, 46000, 42])
      ];
      const agents = [
        demoRecord(['agent', 'in', 'cache_write', 'cache_read', 'out', 'req'], ['claude', 280000, 140000, 3400000, 115000, 102]),
        demoRecord(['agent', 'in', 'cache_write', 'cache_read', 'out', 'req'], ['codex', 128000, 61000, 1460000, 55000, 49])
      ];
      const hours: DemoRecord[] = [];
      const baseMs = Date.UTC(2026, 10, 27, 0, 0, 0);
      for (let h = 0; h < 24; h++) {
        const ts = Math.floor((baseMs + h * 3600000) / 1000);
        const wave = h >= 8 && h <= 22 ? 1 + ((h * 5 + 2) % 7) : (h % 3 === 0 ? 1 : 0);
        const input = wave * 8500;
        const cw = wave * 4200;
        const cr = wave * 95000;
        const output = wave * 3400;
        const req = wave * 4;
        hours.push(demoRecord(['t', 'date', 'hour', 'in', 'cache_write', 'cache_read', 'out', 'req'], ['2026-11-27', '2026-11-27', h, input, cw, cr, output, req]));
        hours[hours.length - 1]['t'] = ts;
      }
      return demoRecord(['usage'], [demoRecord(['zone', 'stored', 'hours', 'machines', 'agents', 'total'], ['UTC', true, hours, machines, agents, total])]);
    }
    if (parts.length === 2 && parts[1] === 'projects' && method === 'GET') {
      const projects7 = this.demoUsageProjectsList();
      const projects3 = projects7.slice(0, 3);
      const projects1 = projects7.slice(0, 2);
      const ranges = [
        demoRecord(['days', 'total', 'projects', 'not_split'], [1, this.demoUsageTokens(184000, 41000, 980000, 62000, 24), projects1, this.demoUsageTokens(0, 0, 0, 0, 0)]),
        demoRecord(['days', 'total', 'projects', 'not_split'], [3, this.demoUsageTokens(290000, 130000, 2800000, 112000, 72), projects3, this.demoUsageTokens(0, 0, 0, 0, 0)]),
        demoRecord(['days', 'total', 'projects', 'not_split'], [7, this.demoUsageTokens(408000, 201000, 4860000, 170000, 151), projects7, this.demoUsageTokens(0, 0, 0, 0, 0)])
      ];
      return demoRecord(['usage'], [demoRecord(['zone', 'stored', 'ranges'], ['UTC', true, ranges])]);
    }
    if (parts.length === 3 && parts[1] === 'projects' && parts[2] === 'detail' && method === 'GET') {
      const key = query['key'] || '';
      const projects = this.demoUsageProjectsList();
      const match = projects.find((p: DemoRecord) => Wire.text(p, 'key') === key) || projects[0];
      const tok = Wire.object(match, 'tokens');
      const primaryTok = this.demoUsageTokens(
        Math.round(Wire.number(tok, 'in', 100000) * 0.72),
        Math.round(Wire.number(tok, 'cache_write', 50000) * 0.72),
        Math.round(Wire.number(tok, 'cache_read', 1000000) * 0.72),
        Math.round(Wire.number(tok, 'out', 40000) * 0.72),
        Math.max(1, Math.round(Wire.number(tok, 'req', 30) * 0.72))
      );
      const secondaryTok = this.demoUsageTokens(
        Math.max(0, Wire.number(tok, 'in', 100000) - Wire.number(primaryTok, 'in', 0)),
        Math.max(0, Wire.number(tok, 'cache_write', 50000) - Wire.number(primaryTok, 'cache_write', 0)),
        Math.max(0, Wire.number(tok, 'cache_read', 1000000) - Wire.number(primaryTok, 'cache_read', 0)),
        Math.max(0, Wire.number(tok, 'out', 40000) - Wire.number(primaryTok, 'out', 0)),
        Math.max(1, Wire.number(tok, 'req', 30) - Wire.number(primaryTok, 'req', 0))
      );
      const projectHours: DemoRecord[] = [];
      const baseMs = Date.UTC(2026, 10, 27, 0, 0, 0);
      for (let h = 0; h < 24; h++) {
        const ts = Math.floor((baseMs + h * 3600000) / 1000);
        const weight = h >= 9 && h <= 21 ? ((h * 3 + 1) % 5) + 1 : 0;
        projectHours.push(demoRecord(['t', 'hour', 'tokens'], [
          ts, h, this.demoUsageTokens(weight * 4200, weight * 2100, weight * 52000, weight * 1800, weight * 2)
        ]));
      }
      const breakdown = (days: number): DemoRecord => demoRecord(
        ['days', 'total', 'share', 'cache_hit', 'machines', 'agents', 'models', 'launchers', 'places'],
        [
          days,
          cloneRecord(tok),
          Wire.number(match, 'share', 0.5),
          0.88,
          [
            demoRecord(['key', 'name', 'tokens'], [DEMO_MACHINE, 'My Mac', cloneRecord(primaryTok)]),
            demoRecord(['key', 'name', 'tokens'], ['demo-linux-box', 'Build Linux', cloneRecord(secondaryTok)])
          ],
          [
            demoRecord(['key', 'name', 'tokens'], ['claude', 'Claude', cloneRecord(primaryTok)]),
            demoRecord(['key', 'name', 'tokens'], ['codex', 'Codex', cloneRecord(secondaryTok)])
          ],
          [
            demoRecord(['key', 'name', 'tokens'], ['claude-sonnet-4-6', 'Claude Sonnet 4.6', cloneRecord(primaryTok)]),
            demoRecord(['key', 'name', 'tokens'], ['gpt-5.3-codex', 'GPT-5.3 Codex', cloneRecord(secondaryTok)])
          ],
          [
            demoRecord(['key', 'name', 'tokens'], ['claude', 'Claude Code', cloneRecord(primaryTok)]),
            demoRecord(['key', 'name', 'tokens'], ['', '', cloneRecord(secondaryTok)])
          ],
          [
            demoRecord(['machine', 'machine_name', 'place', 'worktree', 'tokens'], [DEMO_MACHINE, 'My Mac', '/demo/ReleaseDesk', false, cloneRecord(primaryTok)]),
            demoRecord(['machine', 'machine_name', 'place', 'worktree', 'tokens'], ['demo-linux-box', 'Build Linux', '/demo/ReleaseDesk/.worktrees/perf', true, cloneRecord(secondaryTok)])
          ]
        ]
      );
      return demoRecord(['usage'], [demoRecord(['zone', 'stored', 'project', 'ranges', 'days', 'hours'], [
        'UTC', true, cloneRecord(match), [breakdown(1), breakdown(3), breakdown(7)], this.demoUsageDaysList(7), projectHours
      ])]);
    }
    throw new HttpFailure(404, 'demo_unsupported');
  }

  private demoMachineMetrics(range: string): DemoRecord {
    const now = Math.floor(Date.now() / 1000);
    const span = range === '24h' ? 86400 : range === '6h' ? 21600 : 3600;
    const step = Math.floor(span / 24);
    const points: DemoRecord[] = [];
    for (let i = 0; i <= 24; i++) {
      const t = now - (24 - i) * step;
      const cpu = Math.round((20 + 11 * Math.sin(i * 0.45) + 4 * Math.cos(i * 0.9)) * 10) / 10;
      const mem = Math.round((18.2 + 0.8 * Math.sin(i * 0.3)) * 1024 * 1024 * 1024);
      const load = Math.round((1.8 + 0.7 * Math.sin(i * 0.4)) * 100) / 100;
      const rx = Math.round(310000 + 180000 * Math.abs(Math.sin(i * 0.55)));
      const tx = Math.round(92000 + 55000 * Math.abs(Math.cos(i * 0.55)));
      points.push(demoRecord(['t', 'cpu', 'mem', 'load', 'rx', 'tx'], [t, cpu, mem, load, rx, tx]));
    }
    const system = demoRecord(['os', 'distro', 'name', 'version', 'cores', 'memory'],
      ['darwin', 'macos', 'macOS Sequoia', '15.6', 12, 38654705664]);
    const latest = demoRecord(['cpu', 'load', 'mem_used', 'mem_total', 'disk_used', 'disk_total', 'net_rx', 'net_tx'],
      [28.4, 2.4, 19327352832, 38654705664, 412316860416, 1000204886016, 485000, 142000]);
    const today = demoRecord(['in', 'out', 'cache_write', 'cache_read'], [184000, 62000, 41000, 980000]);
    const usage = demoRecord(['per_minute', 'today', 'recent'], [12400, today, [8200, 9600, 11200, 10800, 13400, 12900, 11800, 12400]]);
    return demoRecord(['machine_id', 'system', 'latest', 'points', 'usage'], [DEMO_MACHINE, system, latest, points, usage]);
  }

  private demoDirectory(requestedPath: string): DemoRecord {
    const clean = safePath(requestedPath) ? requestedPath : '/demo/ReleaseDesk';
    const entry = (name: string, dir: boolean, size: number): DemoRecord =>
      demoRecord(['name', 'dir', 'symlink', 'link_target', 'link_broken', 'size', 'mtime', 'ignored'],
        [name, dir, false, '', false, size, '2026-11-27T08:50:00Z', false]);
    const rows: DemoRecord[] = clean === '/demo/Notes'
      ? [entry('architecture.md', false, 2480), entry('release-checklist.md', false, 1640)]
      : clean.endsWith('/src')
        ? [entry('release.ts', false, 1420), entry('index.ts', false, 680)]
        : clean.endsWith('/tests')
          ? [entry('release.test.ts', false, 1920)]
          : [entry('src', true, 0), entry('tests', true, 0), entry('CHANGELOG.md', false, 1840), entry('README.md', false, 1290), entry('package.json', false, 540)];
    return demoRecord(['path', 'truncated', 'entries'], [clean, false, rows]);
  }

  private demoFileRead(path: string): DemoRecord {
    const name = path.split('/').pop() || 'README.md';
    const bodies: Record<string, string> = {
      'CHANGELOG.md': '# v2.4.0\n\n## Highlights\n- Foldable 3-column Live Workbench for tri-fold & tablet layouts\n- iOS 26 borderless Liquid Glass card hierarchy\n\n## Fixes\n- Per-core machine load formatting (`Load 2.4 · 0.2/core`)\n',
      'release.ts': 'export interface CheckResult {\n  name: string;\n  passed: boolean;\n  skipped?: boolean;\n}\n\nexport function verifyReleaseGate(checks: CheckResult[]): boolean {\n  return checks.every((item) => item.passed && !item.skipped);\n}\n',
      'README.md': '# ReleaseDesk\n\nSynthetic sample workspace for offline Persona preview.\n\n1. Inspect files & diffs safely on-device.\n2. Review release checklist tasks and approval gates.\n'
    };
    const text = bodies[name] || ('// Sample preview file: ' + (safePath(path) ? path : '/demo/ReleaseDesk/' + name) + '\n');
    return demoRecord(['name', 'size', 'mime', 'text', 'offset', 'next', 'truncated'],
      [name, utf8Bytes(text), 'text/plain; charset=utf-8', text, 0, 0, false]);
  }

  private demoGitStatus(): DemoRecord {
    const file = (path: string, index: string, worktree: string, added: number, deleted: number): DemoRecord =>
      demoRecord(['path', 'orig_path', 'index', 'worktree', 'added', 'deleted', 'binary'], [path, '', index, worktree, added, deleted, false]);
    const commit = (hash: string, subject: string, date: string): DemoRecord =>
      demoRecord(['hash', 'subject', 'author', 'date'], [hash, subject, 'Demo Builder', date]);
    return demoRecord(['is_repo', 'root', 'branch', 'upstream', 'ahead', 'behind', 'gone', 'stashes', 'head', 'commits', 'files'], [
      true, '/demo/ReleaseDesk', 'main', 'origin/main', 1, 0, false, 0,
      commit('a1b2c3d', 'polish(ui): refine Liquid Glass spacing and per-core load', '2026-11-27T08:45:00Z'),
      [
        commit('a1b2c3d', 'polish(ui): refine Liquid Glass spacing and per-core load', '2026-11-27T08:45:00Z'),
        commit('9e8d7c6', 'feat(workbench): add Next Unread relay navigation', '2026-11-27T07:20:00Z')
      ],
      [
        file('src/release.ts', 'M', '.', 12, 3),
        file('CHANGELOG.md', '.', 'M', 8, 1)
      ]
    ]);
  }

  private demoGitDiff(path: string): DemoRecord {
    const target = path || 'src/release.ts';
    const diff = target === 'CHANGELOG.md'
      ? 'diff --git a/CHANGELOG.md b/CHANGELOG.md\n--- a/CHANGELOG.md\n+++ b/CHANGELOG.md\n@@ -1,4 +1,6 @@\n # Changelog\n \n+## v2.4.0\n+- Foldable 3-column Live Workbench & Next Unread relay\n+- iOS 26 borderless Liquid Glass refinement\n'
      : 'diff --git a/src/release.ts b/src/release.ts\n--- a/src/release.ts\n+++ b/src/release.ts\n@@ -1,6 +1,8 @@\n export interface CheckResult {\n   name: string;\n   passed: boolean;\n+  skipped?: boolean;\n }\n \n export function verifyReleaseGate(checks: CheckResult[]): boolean {\n-  return checks.every((item) => item.passed);\n+  return checks.every((item) => item.passed && !item.skipped);\n }\n';
    return demoRecord(['diff', 'truncated'], [diff, false]);
  }

  private sessionsRoute(parts: string[], method: string, body: DemoRecord, operationId: string): DemoRecord {
    if (parts.length === 1 && method === 'GET') return demoRecord(['sessions'], [cloneRecords(this.activeSessions())]);
    if (parts.length === 2 && parts[1] === 'archived' && method === 'GET')
      return demoRecord(['sessions'], [cloneRecords(this.sessionsData.filter((item: DemoRecord) => Wire.bool(item, 'archived')))]);
    if (parts.length === 1 && method === 'POST') return this.createAgentRecord(body);
    if (parts.length < 3 || parts[1] !== DEMO_MACHINE) throw new HttpFailure(404, 'demo_session_not_found');
    const sessionId = parts[2], action = parts[3] || '';
    const session = this.sessionsData.find((item: DemoRecord) => Wire.text(item, 'machine_id') === parts[1] && Wire.text(item, 'session_id') === sessionId);
    const record = session || this.conversationsData.find((item: DemoRecord) =>
      Wire.text(item, 'machine_id') === parts[1] && (Wire.text(item, 'session_id') || Wire.text(item, 'id')) === sessionId);
    if (!record) throw new HttpFailure(404, 'demo_session_not_found');
    if (!action && method === 'GET') return demoRecord(['session'], [cloneRecord(record)]);
    if (action === 'items' && method === 'GET') return this.transcript(record, sessionId);
    if (action === 'messages' && method === 'POST') {
      if (Wire.has(body, 'images') || Wire.has(body, 'files')) throw new HttpFailure(501, 'demo_unsupported');
      const text = nonEmpty(Wire.text(body, 'text'), MAX_TEXT_BYTES), clientId = nonEmpty(Wire.text(body, 'client_msg_id', operationId), 200);
      this.appendChat(DEMO_MACHINE, sessionId, text, clientId); return demoRecord(['accepted', 'state'], [true, 'done']);
    }
    if (action === 'rename' && method === 'POST') {
      const title = Wire.text(body, 'title'), tab = Wire.text(body, 'tab_name');
      if (!title.trim() && !tab.trim()) throw new HttpFailure(400, 'demo_invalid_input');
      const next = cloneRecord(record);
      if (title.trim()) {
        next['title'] = nonEmpty(title);
        next['title_source'] = 'manual';
        next['title_manual'] = true;
      }
      if (tab.trim()) next['slot_name'] = nonEmpty(tab);
      this.replaceChatRecord(record, next);
      return demoRecord(['session'], [cloneRecord(next)]);
    }
    if (action === 'autoname' && method === 'POST') {
      if (!session || Wire.bool(record, 'assistant')) throw new HttpFailure(400, 'assistant_session');
      const prevTitle = Wire.text(record, 'title');
      const autoMap: Record<string, string> = {
        'Review release checks': 'Release audit & plan sign-off',
        'Prepare the changelog': 'v2.4 changelog & diff check',
        'Organize project notes': 'Notes architecture milestone summary'
      };
      const generated = autoMap[prevTitle] || (prevTitle.startsWith('Summary: ') ? prevTitle : 'Summary: ' + (prevTitle || 'Agent session'));
      const next = cloneRecord(record);
      next['title'] = generated;
      next['title_source'] = 'auto';
      next['title_manual'] = false;
      this.replaceChatRecord(record, next);
      return demoRecord(['ok', 'title', 'title_source', 'changed'], [true, generated, 'auto', generated !== prevTitle]);
    }
    if ((action === 'unarchive' || action === 'archive') && method === 'POST') {
      if (!session || Wire.bool(record, 'assistant')) throw new HttpFailure(400, 'demo_unsupported');
      const archived = action === 'archive';
      record['archived'] = archived;
      record['updated_at'] = '2026-11-27T09:15:00Z';
      return demoRecord(['ok'], [true]);
    }
    if (action === 'seen' && method === 'POST') {
      record['state'] = 'idle'; record['unread'] = false;
      return demoRecord(['session'], [cloneRecord(record)]);
    }
    if (action === 'close' && method === 'POST') {
      if (!session) throw new HttpFailure(404, 'demo_unsupported');
      record['status'] = 'done'; record['state'] = 'done'; record['closed_at'] = '2026-11-11T09:30:00Z';
      return demoRecord(['session'], [cloneRecord(record)]);
    }
    if (!action && method === 'DELETE') {
      if (!session) throw new HttpFailure(404, 'demo_unsupported');
      this.sessionsData = this.sessionsData.filter((item: DemoRecord) => item !== record);
      this.interactionsData = this.interactionsData.filter((item: DemoRecord) => Wire.text(item, 'session_id') !== sessionId);
      this.transcripts.delete(this.transcriptKey(DEMO_MACHINE, sessionId));
      return demoRecord(['deleted'], [true]);
    }
    if (parts.length === 5 && parts[3] === 'queue' && parts[4] === 'send-now' && method === 'POST') {
      const dequeued = this.dequeueSessionQueue(record, DEMO_MACHINE, sessionId, true);
      if (dequeued === 0) throw new HttpFailure(409, 'nothing_queued');
      return demoRecord(['session', 'state'], [cloneRecord(record), 'done']);
    }
    if (action === 'queue' && parts.length === 4 && method === 'POST') {
      const rawKeep = body['keep'];
      if (!Array.isArray(rawKeep)) throw new HttpFailure(400, 'demo_invalid_input');
      const rawQueued = Array.isArray(record['queued']) ? (record['queued'] as Object[]) : [];
      const relayedCount = Math.min(Math.max(Math.floor(Wire.number(record, 'queued_relayed', 0)), 0), rawQueued.length);
      const ownCount = rawQueued.length - relayedCount;
      if (ownCount === 0) throw new HttpFailure(409, 'nothing_queued');
      const ownEntries = rawQueued.slice(0, ownCount);
      const relayedEntries = rawQueued.slice(ownCount);
      const keepIndices: number[] = [];
      const seen = new Set<number>();
      for (const candidate of rawKeep as Object[]) {
        if (typeof candidate !== 'number' || !Number.isInteger(candidate) || candidate < 0 || candidate >= ownCount || seen.has(candidate)) {
          throw new HttpFailure(400, 'demo_invalid_input');
        }
        seen.add(candidate);
        keepIndices.push(candidate);
      }
      const nextOwn = keepIndices.map((idx: number): Object => ownEntries[idx]);
      record['queued'] = nextOwn.concat(relayedEntries);
      record['queued_relayed'] = relayedEntries.length;
      const previousStrings = ownEntries.map((entry: Object): string => {
        if (typeof entry === 'string') return entry;
        if (entry && typeof entry === 'object') return Wire.text(entry as DemoRecord, 'text', Wire.text(entry as DemoRecord, 'body'));
        return '';
      });
      return demoRecord(['queue'], [previousStrings]);
    }
    if (action === 'fork' && parts.length === 4 && method === 'POST') {
      if (Wire.bool(record, 'main')) throw new HttpFailure(400, 'fork_unsupported');
      const atItemId = Wire.text(body, 'at_item_id').trim();
      const sourceItems = cloneRecords(this.transcripts.get(this.transcriptKey(DEMO_MACHINE, sessionId)) || []);
      let copiedItems = sourceItems;
      if (atItemId) {
        const idx = sourceItems.findIndex((item: DemoRecord) => Wire.text(item, 'id') === atItemId);
        if (idx < 0) throw new HttpFailure(409, 'fork_item_missing');
        copiedItems = sourceItems.slice(0, idx + 1);
      }
      const newId = 'demo-fork-' + (++this.counter);
      const sourceTitle = Wire.text(record, 'title', 'Chat');
      const forkedTitle = 'Branch: ' + sourceTitle;
      if (session) {
        this.ensureRoom(this.sessionsData);
        const forked = cloneRecord(record);
        forked['id'] = newId;
        forked['session_id'] = newId;
        forked['title'] = forkedTitle;
        forked['status'] = 'idle';
        forked['state'] = 'idle';
        forked['queued'] = [];
        forked['queued_relayed'] = 0;
        forked['can_fork'] = true;
        forked['updated_at'] = '2026-11-27T09:10:00Z';
        this.sessionsData.unshift(forked);
      } else {
        this.ensureRoom(this.conversationsData);
        const forked = cloneRecord(record);
        forked['id'] = newId;
        forked['session_id'] = newId;
        forked['title'] = forkedTitle;
        forked['main'] = false;
        forked['kind'] = 'recent';
        forked['archived'] = false;
        forked['can_fork'] = true;
        forked['updated_at'] = '2026-11-27T09:10:00Z';
        this.conversationsData.push(forked);
      }
      this.transcripts.set(this.transcriptKey(DEMO_MACHINE, newId), copiedItems);
      return demoRecord(['session_id'], [newId]);
    }
    if (action === 'interrupt' && method === 'POST') {
      record['status'] = 'interrupted';
      record['state'] = 'idle';
      record['last_text'] = 'Interrupted in local demo.';
      return demoRecord(['state'], ['done']);
    }
    if (action === 'screen' && method === 'GET') {
      const text = [
        '$ npm test -- release-gate',
        '  ✓ verifyReleaseGate enforces zero skipped checks (1.4ms)',
        '  ✓ privacy check clean across changed files (0.9ms)',
        '  ✓ bundle assets & localization keys verified (1.1ms)',
        '',
        'Summary: 42 passed, 0 failed (local demo preview)'
      ].join('\n');
      return demoRecord(['text'], [text]);
    }
    throw new HttpFailure(404, 'demo_unsupported');
  }

  private transcript(record: DemoRecord, session: string): DemoRecord {
    const items = cloneRecords(this.transcripts.get(this.transcriptKey(Wire.text(record, 'machine_id'), session)) || []);
    const last = items.length ? Wire.number(items[items.length - 1], 'seq') : 0;
    return demoRecord(['items', 'from_seq', 'to_seq', 'has_more', 'more_after', 'backlog'],
      [items, items.length ? Wire.number(items[0], 'seq') : 0, last, false, false, false]);
  }

  private createAgentRecord(body: DemoRecord): DemoRecord {
    const machine = Wire.text(body, 'machine_id'), requestedCwd = Wire.text(body, 'cwd'), cwd = requestedCwd || '/demo/ReleaseDesk';
    const launcher = Wire.text(body, 'launcher');
    const prompt = nonEmpty(Wire.text(body, 'prompt'), MAX_TEXT_BYTES);
    if (machine !== DEMO_MACHINE || !safePath(cwd) || !['', 'codex', 'claude', 'gemini'].includes(launcher)) throw new HttpFailure(400, 'demo_invalid_input');
    this.ensureRoom(this.sessionsData);
    const todoId = Wire.text(body, 'todo_id');
    const todo = todoId ? this.todosData.find((item: DemoRecord) => Wire.text(item, 'id') === todoId) : undefined;
    if (todoId && !todo) throw new HttpFailure(404, 'demo_todo_not_found');
    const id = 'demo-agent-' + (++this.counter), title = prompt.split(/\r?\n/)[0].trim().slice(0, 80) || 'New agent';
    const capabilities = demoRecord(['read', 'send', 'approve', 'interrupt', 'terminal', 'close', 'rename'],
      [true, true, true, true, true, true, true]);
    const session = demoRecord(['id', 'session_id', 'machine_id', 'manager', 'title', 'state', 'status', 'cwd', 'repo_root', 'worktree',
      'branch', 'source_kind', 'assistant', 'archived', 'can_fork', 'capabilities', 'created_at', 'updated_at'],
      [id, id, DEMO_MACHINE, DEMO_MANAGER, title, 'working', 'working', cwd, cwd, cwd, 'demo', 'live', false, false, true,
        capabilities, '2026-11-11T09:30:00Z', '2026-11-11T09:30:00Z']);
    if (launcher) session['launcher'] = launcher;
    const tab = Wire.text(body, 'tab_name').trim();
    if (tab) session['slot_name'] = nonEmpty(tab);
    this.sessionsData.unshift(session);
    if (todo) {
      const agents = Wire.rows(todo, 'agents').filter((item: DemoRecord) => Wire.text(item, 'session_id') !== id);
      agents.push(demoRecord(['machine_id', 'session_id', 'title', 'cwd', 'agent', 'source', 'linked_at'],
        [DEMO_MACHINE, id, title, cwd, launcher, 'manual', '2026-11-11T09:30:00Z']));
      todo['agents'] = agents;
    }
    this.transcripts.set(this.transcriptKey(DEMO_MACHINE, id), [this.item('user', prompt), this.item('assistant', 'A sample agent was created inside the local demo.')]);
    return demoRecord(['session'], [cloneRecord(session)]);
  }

  private interactions(parts: string[], method: string, body: DemoRecord): DemoRecord {
    if (parts.length === 1 && method === 'GET') return demoRecord(['interactions'],
      [cloneRecords(this.interactionsData.filter((item: DemoRecord) => Wire.text(item, 'status') === 'open'))]);
    if (parts.length === 3 && parts[2] === 'resolve' && method === 'POST') {
      const request = this.interactionsData.find((item: DemoRecord) => Wire.text(item, 'id') === parts[1]);
      if (!request || Wire.text(request, 'status') !== 'open') throw new HttpFailure(409, 'request_updated');
      const interaction = Wire.object(request, 'interaction');
      if (Wire.text(interaction, 'kind') !== 'plan' || typeof body['approve'] !== 'boolean' || Object.keys(body).some((key: string) => !['approve', 'feedback'].includes(key)))
        throw new HttpFailure(400, 'demo_invalid_resolution');
      request['status'] = 'resolved'; request['resolution'] = cloneRecord(body);
      interaction['status'] = 'resolved'; request['interaction'] = interaction;
      const session = this.sessionsData.find((item: DemoRecord) => Wire.text(item, 'session_id') === Wire.text(request, 'session_id'));
      if (session) { session['status'] = body['approve'] ? 'working' : 'interrupted'; session['state'] = body['approve'] ? 'working' : 'idle'; }
      if (session) this.appendTranscript(DEMO_MACHINE, Wire.text(session, 'session_id'), [this.item('assistant', body['approve'] ? 'Plan approved in the local demo.' : 'Plan declined in the local demo.')]);
      return demoRecord(['status'], ['resolved']);
    }
    throw new HttpFailure(404, 'demo_unsupported');
  }

  private preferences(kind: string, parts: string[], method: string, body: DemoRecord): DemoRecord {
    if (parts.length !== 1) throw new HttpFailure(404, 'demo_unsupported');
    const current = kind === 'pins' ? this.pinsData : this.mutesData;
    if (method === 'GET') return cloneRecord(current);
    if (method !== 'PUT') throw new HttpFailure(404, 'demo_unsupported');
    const spaces = this.safeStringList(body, 'spaces'), sessions = this.safeStringList(body, 'sessions');
    const next = demoRecord(['spaces', 'sessions'], [spaces, sessions]);
    if (kind === 'mutes') next['allowed'] = this.safeStringList(body, 'allowed');
    if (Object.keys(body).some((key: string) => !['spaces', 'sessions', 'allowed'].includes(key)) || kind === 'pins' && Wire.has(body, 'allowed'))
      throw new HttpFailure(400, 'demo_invalid_input');
    if (kind === 'pins') this.pinsData = next; else this.mutesData = next;
    return cloneRecord(next);
  }

  private safeStringList(body: DemoRecord, key: string): string[] {
    const value = body[key];
    if (!Array.isArray(value) || (value as Object[]).length > 512 || (value as Object[]).some((item: Object) => typeof item !== 'string' || utf8Bytes(item as string) > 512))
      throw new HttpFailure(400, 'demo_invalid_input');
    return (value as Object[]).map((item: Object) => item as string).filter((item: string, index: number, all: string[]) => all.indexOf(item) === index);
  }

  private todosRoute(parts: string[], query: Record<string, string>, method: string, body: DemoRecord): DemoRecord {
    if (parts.length === 1 && method === 'GET') return demoRecord(['todos'], [cloneRecords(this.todosData)]);
    if (parts.length === 1 && method === 'POST') {
      this.ensureRoom(this.todosData);
      const title = nonEmpty(Wire.text(body, 'title')), notes = textWithin(Wire.text(body, 'notes'), 20000);
      const todo = demoRecord(['id', 'manager', 'title', 'notes', 'state', 'status', 'origin', 'created_at', 'updated_at', 'agents', 'spaces'],
        ['demo-todo-' + (++this.counter), DEMO_MANAGER, title, notes, 'not_started', 'not_started', 'manual',
          '2026-11-11T09:30:00Z', '2026-11-11T09:30:00Z', [], []]);
      this.todosData.unshift(todo); return demoRecord(['todo'], [cloneRecord(todo)]);
    }
    if (parts.length < 2) throw new HttpFailure(404, 'demo_todo_not_found');
    const id = parts[1], todo = this.todosData.find((item: DemoRecord) => Wire.text(item, 'id') === id);
    if (!todo) throw new HttpFailure(404, 'demo_todo_not_found');
    if (parts.length === 2 && method === 'GET') return demoRecord(['todo'], [cloneRecord(todo)]);
    if (parts.length === 2 && method === 'PATCH') {
      const allowed = ['title','notes','done','due_at','due_date','remind_at','priority','project'];
      if (Object.keys(body).some((key: string) => !allowed.includes(key))) throw new HttpFailure(400, 'demo_invalid_input');
      let updated = cloneRecord(todo);
      if (Wire.has(body, 'title')) updated['title'] = nonEmpty(Wire.text(body, 'title'));
      if (Wire.has(body, 'notes')) updated['notes'] = textWithin(Wire.text(body, 'notes'), 20000);
      if (Wire.has(body,'due_at') || Wire.has(body,'due_date')) {
        const at=Wire.text(body,'due_at'), day=Wire.text(body,'due_date');
        if (at && day || at && !Number.isFinite(Date.parse(at)) || day && !/^\d{4}-\d{2}-\d{2}$/.test(day)) throw new HttpFailure(400,'demo_invalid_input');
        updated=omitFields(updated,['due_at','due_date']); if (at) updated['due_at']=at; if (day) updated['due_date']=day;
      }
      if (Wire.has(body,'remind_at')) {
        const at=Wire.text(body,'remind_at'); if (at && !Number.isFinite(Date.parse(at))) throw new HttpFailure(400,'demo_invalid_input');
        updated=omitFields(updated,['remind_at','reminded_at']); if (at) updated['remind_at']=at;
      }
      if (Wire.has(body,'priority')) {
        const priority=Wire.text(body,'priority'); if (!['','normal','low','high'].includes(priority)) throw new HttpFailure(400,'demo_invalid_input');
        updated['priority']=priority==='normal' ? '' : priority;
      }
      if (Wire.has(body,'project')) {
        const slug=Wire.text(body,'project');
        if (slug && !this.knowledgeData.some((row:DemoRecord)=>Wire.text(row,'kind')==='projects' && Wire.text(row,'id')===slug))
          throw new HttpFailure(400,'demo_invalid_input');
        if (slug) updated['project']=slug; else updated=omitFields(updated,['project']);
      }
      if (Wire.has(body, 'done')) {
        if (typeof body['done'] !== 'boolean') throw new HttpFailure(400, 'demo_invalid_input');
        if (body['done']) { updated['done_at'] = '2026-11-11T09:30:00Z'; updated['done_by'] = 'demo'; updated['state'] = 'done'; updated['status'] = 'done'; }
        else {
          updated = omitFields(updated, ['done_at', 'done_by']);
          updated['reopened_at'] = '2026-11-11T09:30:00Z'; updated['state'] = 'not_started'; updated['status'] = 'not_started';
        }
      }
      updated['updated_at'] = '2026-11-11T09:30:00Z'; this.todosData = this.todosData.map((item: DemoRecord) => item === todo ? updated : item);
      return demoRecord(['todo'], [cloneRecord(updated)]);
    }
    if (parts.length === 2 && method === 'DELETE') {
      this.todosData = this.todosData.filter((item: DemoRecord) => item !== todo); return demoRecord(['deleted'], [true]);
    }
    if (parts.length === 3 && parts[2] === 'decisions') {
      const list = this.todoDecisionsData.get(id) || [];
      if (method === 'GET') {
        const includeAll = query['all'] === '1' || query['all'] === 'true';
        const filtered = includeAll ? list : list.filter((d: DemoRecord) => !Wire.text(d, 'superseded_by'));
        return demoRecord(['decisions'], [cloneRecords(filtered)]);
      }
      if (method === 'POST') {
        const question = nonEmpty(Wire.text(body, 'question'), 1000);
        const choice = nonEmpty(Wire.text(body, 'choice'), 1000);
        const reason = textWithin(Wire.text(body, 'reason').trim(), 4000);
        const supersedesId = Wire.text(body, 'supersedes_id').trim();
        const nextId = 'demo-dec-' + (++this.counter);
        const nextList = list.map((entry: DemoRecord) => cloneRecord(entry));
        if (supersedesId) {
          const target = nextList.find((entry: DemoRecord) => Wire.text(entry, 'id') === supersedesId);
          if (!target) throw new HttpFailure(404, 'decision_not_found');
          target['superseded_by'] = nextId;
        }
        const created = demoRecord(
          ['id', 'todo_id', 'question', 'choice', 'reason', 'source', 'source_machine', 'source_session', 'source_message', 'supersedes_id', 'superseded_by', 'created_at'],
          [nextId, id, question, choice, reason, 'manual', '', '', '', supersedesId, '', '2026-11-27T09:15:00Z']
        );
        nextList.push(created);
        this.todoDecisionsData.set(id, nextList);
        return demoRecord(['decision', 'inserted'], [cloneRecord(created), true]);
      }
    }
    if (parts.length === 4 && parts[2] === 'decisions' && method === 'PATCH') {
      const decisionId = parts[3];
      const list = (this.todoDecisionsData.get(id) || []).map((entry: DemoRecord) => cloneRecord(entry));
      const row = list.find((entry: DemoRecord) => Wire.text(entry, 'id') === decisionId);
      if (!row) throw new HttpFailure(404, 'decision_not_found');
      if (!Wire.has(body, 'supersedes_id') || typeof body['supersedes_id'] !== 'string') {
        throw new HttpFailure(400, 'demo_invalid_input');
      }
      const nextSup = Wire.text(body, 'supersedes_id').trim();
      const prevSup = Wire.text(row, 'supersedes_id');
      if (prevSup) {
        const oldTarget = list.find((entry: DemoRecord) => Wire.text(entry, 'id') === prevSup);
        if (oldTarget && Wire.text(oldTarget, 'superseded_by') === decisionId) oldTarget['superseded_by'] = '';
      }
      if (nextSup) {
        const newTarget = list.find((entry: DemoRecord) => Wire.text(entry, 'id') === nextSup);
        if (!newTarget || nextSup === decisionId) throw new HttpFailure(400, 'demo_invalid_input');
        newTarget['superseded_by'] = decisionId;
      }
      row['supersedes_id'] = nextSup;
      this.todoDecisionsData.set(id, list);
      return demoRecord(['decision'], [cloneRecord(row)]);
    }
    if (parts.length === 5 && parts[2] === 'agents' && ['PUT', 'DELETE'].includes(method)) {
      const machine = parts[3], sessionId = parts[4];
      const agent = this.sessionsData.find((item: DemoRecord) => Wire.text(item, 'machine_id') === machine && Wire.text(item, 'session_id') === sessionId);
      if (!agent || machine !== DEMO_MACHINE) throw new HttpFailure(404, 'demo_agent_not_found');
      const rows = Wire.rows(todo, 'agents').filter((item: DemoRecord) => !(Wire.text(item, 'machine_id') === machine && Wire.text(item, 'session_id') === sessionId));
      if (method === 'PUT') rows.push(demoRecord(['machine_id', 'session_id', 'title', 'cwd', 'agent', 'source', 'linked_at'],
        [machine, sessionId, Wire.text(agent, 'title'), Wire.text(agent, 'cwd'), Wire.text(agent, 'launcher'), 'manual', '2026-11-11T09:30:00Z']));
      todo['agents'] = rows; return demoRecord(['todo'], [cloneRecord(todo)]);
    }
    throw new HttpFailure(404, 'demo_unsupported');
  }

  private knowledgeKinds(): string[] { return ['projects', 'chats', 'people', 'docs', 'meetings', 'events', 'recordings']; }

  private searchKnowledge(query: Record<string, string>, method: string): DemoRecord {
    if (method !== 'GET') throw new HttpFailure(404, 'demo_unsupported');
    const needle = lower(nonEmpty(query['q'] || '', 2000));
    const matches = this.knowledgeData.map((entry: DemoRecord) => {
      const body = [Wire.text(entry, 'title'), Wire.text(entry, 'summary'), Wire.text(entry, 'content'), Wire.strings(Wire.object(entry, 'meta'), 'tags').join(' ')].join('\n');
      const lines = body.split('\n').filter((line: string) => line.toLowerCase().includes(needle)).slice(0, 20);
      const lineHits: DemoRecord[] = lines.map((line: string): DemoRecord => demoRecord(['line'], [line]));
      return demoRecord(['entry', 'in_name', 'count', 'hits'],
        [cloneRecord(entry), Wire.text(entry, 'title').toLowerCase().includes(needle), lines.length, lineHits]);
    }).filter((item: DemoRecord) => Wire.bool(item, 'in_name') || Wire.number(item, 'count') > 0);
    if (query['results'] === '1') return demoRecord(['results'], [cloneRecords(matches)]);
    if (query['messages'] === '0') {
      const hits: DemoRecord[] = matches.map((item: DemoRecord): DemoRecord => {
        const entry = Wire.object(item, 'entry');
        return demoRecord(['path', 'line'], [Wire.text(entry, 'kind') + '/' + Wire.text(entry, 'id'),
          Wire.text(Wire.rows(item, 'hits')[0] || {}, 'line', Wire.text(entry, 'title'))]);
      });
      return demoRecord(['hits'], [hits]);
    }
    return demoRecord(['results'], [cloneRecords(matches)]);
  }

  private knowledgeRoute(parts: string[], query: Record<string, string>, method: string, body: DemoRecord): DemoRecord {
    if (parts.length < 2 || !(this.knowledgeKinds().includes(parts[1]) || parts[1] === 'moments')) throw new HttpFailure(404, 'demo_unsupported');
    const kind = parts[1];
    if (kind === 'moments' && parts.length === 3 && method === 'GET' && parts[2] === 'demo-moment-release')
      return demoRecord(['markdown'], ['The sample release checklist was added to Release Desk.']);
    if (parts.length === 2 && method === 'GET') {
      const tag = query['tag'] || '', items = this.knowledgeData.filter((item: DemoRecord) => Wire.text(item, 'kind') === kind &&
        (!tag || Wire.strings(Wire.object(item, 'meta'), 'tags').includes(tag)));
      const tags: string[] = [];
      this.knowledgeData.filter((item: DemoRecord) => Wire.text(item, 'kind') === kind).forEach((item: DemoRecord) => {
        Wire.strings(Wire.object(item, 'meta'), 'tags').forEach((tagValue: string) => { if (!tags.includes(tagValue)) tags.push(tagValue); });
      });
      return demoRecord(['items', 'tags'], [cloneRecords(items), tags]);
    }
    if (parts.length === 2 && method === 'POST') {
      this.ensureRoom(this.knowledgeData);
      const title = nonEmpty(Wire.text(body, 'title')), summary = textWithin(Wire.text(body, 'summary'), 10000);
      const content = textWithin(Wire.text(body, 'content'), MAX_TEXT_BYTES), tags = this.bodyStringArray(body, 'tags');
      const entry = this.knowledgeEntry(kind, 'demo-' + kind + '-' + (++this.counter), title, summary || content.slice(0, 500), content, tags);
      this.knowledgeData.unshift(entry); return demoRecord(['item'], [cloneRecord(entry)]);
    }
    if (parts.length !== 3) throw new HttpFailure(404, 'demo_unsupported');
    const entry = this.knowledgeData.find((item: DemoRecord) => Wire.text(item, 'kind') === kind && Wire.text(item, 'id') === parts[2]);
    if (!entry) throw new HttpFailure(404, 'demo_knowledge_not_found');
    if (method === 'GET') {
      const q = query['q'] || '', lines = q ? Wire.text(entry, 'content').split('\n').filter((line: string) => line.toLowerCase().includes(q.toLowerCase())) : [];
      const matches: DemoRecord[] = lines.map((line: string, index: number): DemoRecord => demoRecord(['line', 'index'], [line, index]));
      const reply = demoRecord(['detail', 'matches', 'agents'], [cloneRecord(entry), matches,
        cloneRecords(this.sessionsData.filter((session: DemoRecord) => Wire.text(session, 'project_id') === Wire.text(entry, 'id')))]);
      if (kind === 'chats' && Wire.text(entry, 'id') === 'demo-chat-notes') {
        reply['inbox'] = [demoRecord(['id', 'title', 'tier', 'status', 'summary', 'updated_at', 'messages'], [
          'demo-inbox-lark', 'Release room', 'action', 'action', 'Can someone review the sample release checklist?', '2026-11-11T08:15:00Z',
          [demoRecord(['day', 'line'], ['2026-11-11', '- 08:15 Mina Example: Can someone review the sample release checklist?'])]
        ])];
      }
      return reply;
    }
    if (method === 'PUT') {
      if (Object.keys(body).some((key: string) => !['tags', 'importance', 'relevance', 'status', 'project_kind'].includes(key))) throw new HttpFailure(400, 'demo_invalid_input');
      const updated = cloneRecord(entry), meta = cloneRecord(Wire.object(entry, 'meta'));
      const tags = Wire.has(body, 'tags') ? this.bodyStringArray(body, 'tags') : [];
      let importance = '', relevance = -1, status = '', projectKind = '';
      if (Wire.has(body, 'importance')) { importance = nonEmpty(Wire.text(body, 'importance'), 2); if (!['P0', 'P1', 'P2', 'P3'].includes(importance)) throw new HttpFailure(400, 'demo_invalid_input'); }
      if (Wire.has(body, 'relevance')) { relevance = Wire.number(body, 'relevance', -1); if (!Number.isInteger(relevance) || relevance < 0 || relevance > 100) throw new HttpFailure(400, 'demo_invalid_input'); }
      if (Wire.has(body, 'status')) { status = Wire.text(body, 'status'); if (!['active', 'paused', 'done', 'ignored'].includes(status)) throw new HttpFailure(400, 'demo_invalid_input'); }
      if (Wire.has(body, 'project_kind')) { projectKind = Wire.text(body, 'project_kind'); if (!['short', 'long'].includes(projectKind)) throw new HttpFailure(400, 'demo_invalid_input'); }
      if (Wire.has(body, 'tags')) { meta['tags'] = tags; updated['tags'] = tags.slice(); }
      if (importance) meta['importance'] = importance;
      if (relevance >= 0) meta['relevance'] = relevance;
      if (status) meta['status'] = status;
      if (projectKind) meta['kind'] = projectKind;
      updated['meta'] = meta; updated['updated_at'] = '2026-11-11T09:30:00Z';
      this.knowledgeData = this.knowledgeData.map((item: DemoRecord) => item === entry ? updated : item);
      return demoRecord([], []);
    }
    throw new HttpFailure(404, 'demo_unsupported');
  }

  private bodyStringArray(body: DemoRecord, key: string): string[] {
    const value = body[key];
    if (value === undefined) return [];
    if (!Array.isArray(value) || (value as Object[]).length > 64 || (value as Object[]).some((item: Object) => typeof item !== 'string' || utf8Bytes(item as string) > 120))
      throw new HttpFailure(400, 'demo_invalid_input');
    return (value as Object[]).map((item: Object) => (item as string).trim().replace(/^#/, '')).filter((item: string, index: number, all: string[]) => !!item && all.indexOf(item) === index);
  }

  private inboxRoute(parts: string[], method: string, body: DemoRecord, operationId: string): DemoRecord {
    if (parts.length === 1 && method === 'GET') return demoRecord(['items'], [cloneRecords(this.inboxData)]);
    if (parts.length < 2) throw new HttpFailure(404, 'demo_inbox_not_found');
    const item = this.inboxData.find((value: DemoRecord) => Wire.text(value, 'id') === parts[1]);
    if (!item) throw new HttpFailure(404, 'demo_inbox_not_found');
    if (parts.length === 2 && method === 'GET') {
      const reply = demoRecord(['item'], [cloneRecord(item)]);
      if (Wire.text(item, 'id') === 'demo-inbox-lark') {
        reply['kb_chat'] = demoRecord(['id', 'path'], ['demo-chat-notes', 'chats']);
      }
      return reply;
    }
    const action = parts[2] || '';
    if (action === 'done' && method === 'POST') {
      if (typeof body['done'] !== 'boolean') throw new HttpFailure(400, 'demo_invalid_input');
      if (body['done']) { item['done'] = true; item['done_at'] = '2026-11-11T09:30:00Z'; item['state'] = 'done'; item['unread'] = false; }
      else {
        const updated = omitFields(item, ['done_at']); updated['done'] = false; updated['state'] = 'idle';
        this.inboxData = this.inboxData.map((current: DemoRecord) => current === item ? updated : current);
        return cloneRecord(updated);
      }
      return cloneRecord(item);
    }
    if (action === 'reply' && method === 'POST') return this.replyInbox(item, body, operationId);
    if (action === 'handoff' && method === 'POST') return this.handoffInbox(item, body, operationId);
    throw new HttpFailure(404, 'demo_unsupported');
  }

  private replyInbox(item: DemoRecord, body: DemoRecord, operationId: string): DemoRecord {
    const text = nonEmpty(Wire.text(body, 'text'), MAX_TEXT_BYTES), clientId = nonEmpty(Wire.text(body, 'client_id', operationId), 200);
    if (operationId && operationId !== clientId) throw new HttpFailure(400, 'demo_invalid_input');
    const cached = this.operationResults.get(clientId); if (cached) return cloneRecord(cached);
    if (Wire.text(item, 'source') === 'mail') {
      const target = Wire.object(item, 'reply_target');
      if (Wire.number(body, 'expected_uid', -1) !== Wire.number(target, 'uid') || Wire.text(body, 'expected_version') !== Wire.text(target, 'version'))
        throw new HttpFailure(409, 'mail_reply_target_changed');
    }
    this.appendInboxReply(item, text, clientId);
    const version = Wire.text(item, 'version'); item['version'] = version + '-reply';
    const target = Wire.object(item, 'reply_target');
    if (Wire.has(target, 'version')) target['version'] = Wire.text(target, 'version') + '-reply';
    item['reply_target'] = target;
    const updated = cloneRecord(item); this.rememberOperation(clientId, updated); return updated;
  }

  private appendInboxReply(item: DemoRecord, text: string, clientId: string, threadRoot: string = '', replyTo: string = ''): void {
    const source = Wire.text(item, 'source');
    if (source === 'lark') {
      const message = demoRecord(['id', 'position', 'sender', 'sender_name', 'text', 'mine', 'state', 'can_reply', 'thread_kind', 'client_id', 'reactions'],
        ['demo-lark-message-' + (++this.messageSequence), String(1000 + this.messageSequence),
          demoRecord(['id', 'name'], ['demo-you', 'Demo user']), 'Demo user', text, true, 'read', true, 'message', clientId, []]);
      if (replyTo) message['reply_to'] = replyTo;
      if (threadRoot) message['thread_root'] = threadRoot;
      this.larkMessages.push(message);
      this.larkMessages = this.larkMessages.slice(-MAX_TRANSCRIPT_ITEMS);
      this.rememberOperation('messaging:' + clientId,
        demoRecord(['text', 'reply_to', 'thread_root'], [text, replyTo, threadRoot]));
    }
    item['summary'] = 'Demo reply: ' + text.slice(0, 160); item['last_reply'] = demoRecord(['client_id', 'text'], [clientId, text]);
    item['unread'] = false; if (Wire.text(item, 'state') === 'unread') item['state'] = 'idle';
  }

  private handoffInbox(item: DemoRecord, body: DemoRecord, operationId: string): DemoRecord {
    const text = nonEmpty(Wire.text(body, 'text'), MAX_TEXT_BYTES), clientId = nonEmpty(Wire.text(body, 'client_id', operationId), 200);
    if (operationId && operationId !== clientId) throw new HttpFailure(400, 'demo_invalid_input');
    const cached = this.operationResults.get(clientId); if (cached) return cloneRecord(cached);
    this.ensureRoom(this.conversationsData);
    const sessionId = 'demo-inbox-followup-' + (++this.counter), title = 'Follow up: ' + Wire.text(item, 'title', 'Inbox item');
    const capabilities = demoRecord(['read', 'send', 'approve', 'interrupt', 'terminal', 'close', 'rename'],
      [true, true, true, true, true, true, true]);
    const session = demoRecord(['id', 'session_id', 'machine_id', 'manager', 'title', 'main', 'kind', 'status', 'state', 'source_kind',
      'cwd', 'repo_root', 'capabilities'],
      [sessionId, sessionId, DEMO_MACHINE, DEMO_MANAGER, title, false, 'recent', 'working', 'working', 'live',
        '/demo/ReleaseDesk', '/demo/ReleaseDesk', capabilities]);
    this.conversationsData.push(session); this.transcripts.set(this.transcriptKey(DEMO_MACHINE, sessionId), [
      this.item('user', 'Follow up on “' + Wire.text(item, 'title') + '”: ' + text, clientId),
      this.item('assistant', 'This follow-up conversation is local to the demo.')
    ]);
    const handoffs = Wire.rows(item, 'handoffs').concat([demoRecord(['machine_id', 'session_id', 'created_at'],
      [DEMO_MACHINE, sessionId, '2026-11-11T09:30:00Z'])]);
    item['handoffs'] = handoffs;
    const result = demoRecord(['item', 'session'], [cloneRecord(item), demoRecord(['machine_id', 'session_id'], [DEMO_MACHINE, sessionId])]);
    this.rememberOperation(clientId, result); return cloneRecord(result);
  }

  private messaging(parts: string[], query: Record<string, string>, method: string, body: DemoRecord, operationId: string): DemoRecord {
    if (parts.length === 1 && method === 'GET') {
      const sources: DemoRecord[] = [demoRecord(['id', 'name', 'capabilities'], ['lark', 'Feishu demo', ['accounts', 'threads', 'topics']])];
      return demoRecord(['sources'], [sources]);
    }
    if (parts.length < 2 || parts[1] !== 'lark') throw new HttpFailure(404, 'demo_unsupported');
    const account = query['account'] || '';
    if (account && account !== DEMO_LARK_ACCOUNT) throw new HttpFailure(409, 'account_changed');
    const rest = parts.slice(2);
    if (rest.length === 1 && rest[0] === 'accounts' && method === 'GET') {
      const accounts: DemoRecord[] = [demoRecord(['id', 'name', 'email'], [DEMO_LARK_ACCOUNT, 'Demo Team', 'team@example.test'])];
      return demoRecord(['accounts', 'default'], [accounts, DEMO_LARK_ACCOUNT]);
    }
    if (rest.length === 1 && rest[0] === 'status' && method === 'GET')
      return demoRecord(['account', 'name', 'configured', 'connected', 'signed_in'], [DEMO_LARK_ACCOUNT, 'Demo Team', true, true, true]);
    if (rest.length === 1 && rest[0] === 'me' && method === 'GET')
      return demoRecord(['account', 'user_id', 'name'], [DEMO_LARK_ACCOUNT, 'demo-user', 'Demo user']);
    if (rest.length === 3 && rest[0] === 'conversations' && rest[1] === DEMO_LARK_CONVERSATION && rest[2] === 'messages') {
      if (method === 'GET') return demoRecord(['account', 'messages', 'topic', 'has_older', 'older_cursor'],
        [DEMO_LARK_ACCOUNT, cloneRecords(this.larkMessages.filter((item: DemoRecord) => !Wire.text(item, 'thread_root'))), false, false, '']);
      if (method === 'POST') {
        const text = nonEmpty(Wire.text(body, 'text'), MAX_TEXT_BYTES), clientId = nonEmpty(Wire.text(body, 'client_id', operationId), 200);
        const replyTo = Wire.text(body, 'reply_to');
        if (Wire.text(body, 'expect_user') !== 'demo-user') throw new HttpFailure(403, 'unknown_sender');
        if (operationId && clientId !== operationId) throw new HttpFailure(400, 'demo_invalid_input');
        if (Wire.has(body, 'reply_to') && !replyTo || Wire.has(body, 'thread') && typeof body['thread'] !== 'boolean')
          throw new HttpFailure(400, 'demo_invalid_input');
        const thread = Wire.bool(body, 'thread');
        if (thread && !replyTo) throw new HttpFailure(400, 'demo_invalid_input');
        let threadRoot = '';
        if (replyTo) {
          const parent = this.larkMessages.find((item: DemoRecord) => Wire.text(item, 'id') === replyTo);
          if (!parent) throw new HttpFailure(409, 'reply_target_changed');
          if (!Wire.bool(parent, 'can_reply', true)) throw new HttpFailure(409, 'reply_target_changed');
          if (thread) threadRoot = Wire.text(parent, 'thread_root') || Wire.text(parent, 'id');
        }
        const cached = this.operationResults.get('messaging:' + clientId);
        if (cached) {
          if (Wire.text(cached, 'text') !== text || Wire.text(cached, 'reply_to') !== replyTo || Wire.text(cached, 'thread_root') !== threadRoot)
            throw new HttpFailure(409, 'demo_client_id_conflict');
          return demoRecord(['state'], ['done']);
        }
        const previous = this.larkMessages.find((item: DemoRecord) => Wire.text(item, 'client_id') === clientId);
        if (previous) {
          if (Wire.text(previous, 'text') !== text || Wire.text(previous, 'reply_to') !== replyTo || Wire.text(previous, 'thread_root') !== threadRoot)
            throw new HttpFailure(409, 'demo_client_id_conflict');
          this.rememberOperation('messaging:' + clientId,
            demoRecord(['text', 'reply_to', 'thread_root'], [text, replyTo, threadRoot]));
          return demoRecord(['state'], ['done']);
        }
        this.appendInboxReply(this.inboxData[0], text, clientId, threadRoot, replyTo);
        return demoRecord(['state'], ['done']);
      }
    }
    if (rest.length === 3 && rest[0] === 'conversations' && rest[1] === DEMO_LARK_CONVERSATION && rest[2] === 'read' && method === 'POST') {
      const ids = this.bodyStringArray(body, 'message_ids'), position = Wire.text(body, 'position');
      if (!position || ids.length > 100) throw new HttpFailure(400, 'demo_invalid_input');
      this.larkMessages = this.larkMessages.map((message: DemoRecord): DemoRecord => {
        if (!ids.includes(Wire.text(message, 'id'))) return message;
        const updated = cloneRecord(message); updated['state'] = 'read'; return updated;
      });
      this.inboxData[0]['unread'] = false; if (Wire.text(this.inboxData[0], 'state') === 'unread') this.inboxData[0]['state'] = 'idle';
      return demoRecord(['account', 'read'], [DEMO_LARK_ACCOUNT, true]);
    }
    if (rest.length === 2 && rest[0] === 'messages' && method === 'GET') {
      const message = this.larkMessages.find((item: DemoRecord) => Wire.text(item, 'id') === rest[1]);
      if (!message) throw new HttpFailure(404, 'demo_message_not_found');
      return demoRecord(['account', 'message'], [DEMO_LARK_ACCOUNT, cloneRecord(message)]);
    }
    if (rest.length === 3 && rest[0] === 'messages' && rest[2] === 'thread' && method === 'GET') return this.larkThread(rest[1]);
    throw new HttpFailure(404, 'demo_unsupported');
  }

  private larkThread(rootId: string): DemoRecord {
    const root = this.larkMessages.find((item: DemoRecord) => Wire.text(item, 'id') === rootId && !Wire.text(item, 'thread_root'));
    if (!root) throw new HttpFailure(404, 'demo_message_not_found');
    const messages = [root].concat(this.larkMessages.filter((item: DemoRecord) => Wire.text(item, 'thread_root') === rootId));
    return demoRecord(['account', 'root', 'messages', 'topic', 'has_older', 'older_cursor'],
      [DEMO_LARK_ACCOUNT, demoRecord(['id'], [rootId]), cloneRecords(messages), false, false, '']);
  }

  private legacyLark(parts: string[], query: Record<string, string>, method: string, body: DemoRecord): DemoRecord {
    if (query['ol_account'] && query['ol_account'] !== DEMO_LARK_ACCOUNT) throw new HttpFailure(409, 'account_changed');
    if (parts.length === 4 && parts[1] === 'conversations' && parts[3] === 'messages' && method === 'GET')
      return demoRecord(['messages', 'topic', 'has_older'],
        [cloneRecords(this.larkMessages.filter((item: DemoRecord) => !Wire.text(item, 'thread_root'))), false, false]);
    if (parts.length === 4 && parts[1] === 'messages' && parts[3] === 'thread' && method === 'GET') return this.larkThread(parts[2]);
    if (parts.length === 4 && parts[1] === 'conversations' && parts[3] === 'read' && method === 'POST') {
      const ids = this.bodyStringArray(body, 'message_ids');
      this.larkMessages = this.larkMessages.map((message: DemoRecord): DemoRecord => {
        if (!ids.includes(Wire.text(message, 'id'))) return message;
        const updated = cloneRecord(message); updated['state'] = 'read'; return updated;
      });
      this.inboxData[0]['unread'] = false; this.inboxData[0]['state'] = 'idle'; return demoRecord(['read'], [true]);
    }
    throw new HttpFailure(404, 'demo_unsupported');
  }

  private mail(parts: string[], query: Record<string, string>, method: string, body: DemoRecord): DemoRecord {
    if (parts.length === 2 && parts[1] === 'accounts' && method === 'GET') {
      const accounts: DemoRecord[] = [demoRecord(['id', 'name', 'email'], [DEMO_MAIL_ACCOUNT, 'Demo Mailbox', 'inbox@example.test'])];
      return demoRecord(['accounts'], [accounts]);
    }
    if (parts.length < 3 || parts[1] !== 'messages' || parts[2] !== DEMO_MAIL_ACCOUNT) throw new HttpFailure(404, 'demo_unsupported');
    const uid = Number(parts[3]), message = this.mailData.find((item: DemoRecord) => Wire.number(item, 'uid') === uid);
    if (!message || query['mailbox'] && query['mailbox'] !== Wire.text(message, 'mailbox')) throw new HttpFailure(404, 'demo_mail_not_found');
    if (parts.length === 4 && method === 'GET') return demoRecord(['message'], [cloneRecord(message)]);
    if (parts.length === 5 && parts[4] === 'flags' && method === 'POST') {
      if (Wire.text(body, 'mailbox', 'INBOX') !== Wire.text(message, 'mailbox') || typeof body['seen'] !== 'boolean') throw new HttpFailure(400, 'demo_invalid_input');
      message['seen'] = body['seen'];
      if (body['seen']) { const item = this.inboxData.find((value: DemoRecord) => Wire.text(value, 'source') === 'mail'); if (item) { item['state'] = 'idle'; item['unread'] = false; } }
      return demoRecord(['message'], [cloneRecord(message)]);
    }
    throw new HttpFailure(501, 'demo_unsupported');
  }
}
