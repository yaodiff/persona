import test from 'node:test';
import assert from 'node:assert/strict';
import { existsSync, readFileSync } from 'node:fs';
import { registerHooks, stripTypeScriptTypes } from 'node:module';
import {
  HUB_MODULES_MARKER,
  HUB_MODULE_PREFIX,
  LARK_MODULE_FEATURE,
  MAIL_MODULE_FEATURE,
  RECORDING_MODULE_FEATURE,
  extractPublishedModules,
  hasRegisteredPushRoute,
  isHubModuleAllowed,
  parseHiddenHubPages,
  shouldEndLiveViewOnBackground
} from '../entry/src/main/ets/core/HubModules.ts';
import {
  isRecordingModuleAllowed,
  parseStoredHubModules,
  serializeStoredHubModules
} from '../entry/src/main/ets/core/XiaoYiActions.ts';

const liveState = {
  systemEnabled: true,
  started: 0,
  stopped: 0
};
globalThis.__parityLiveViewState = liveState;

registerHooks({
  resolve(specifier, context, next) {
    if (specifier === '@kit.LiveViewKit') return { url: 'parity:liveview-kit', shortCircuit: true };
    if (specifier === '@kit.AbilityKit') return { url: 'parity:ability-kit', shortCircuit: true };
    if (specifier === '@ohos.wantAgent') return { url: 'parity:want-agent', shortCircuit: true };
    if (specifier === '@kit.ArkTS') return { url: 'parity:arkts', shortCircuit: true };
    if (context.parentURL?.endsWith('/ClientModel.ets')) {
      if ([
        '../core/BackgroundActivity', '../core/ChatForwardWire', '../core/ChatSendFeedback',
        '../core/DictationLanguage', '../core/Markdown', '../core/PhoneInterfaceStyle',
        './Wire', './AgentAttention', './AssistantView', './DemoRepository', './NavigationContext',
        './ChatDraftStore', './HubRepository', './MutationStore', './Compatibility',
        './InteractionDecision', '../core/ClientLocale', '../core/Domain', '../core/HubModules',
        '../core/ScopePolicy', '../core/StorageOwnership', '../core/MessageContent',
        '../core/TranscriptCache', '../core/XiaoYiActions', '../core/NextSuggestions'
      ].includes(specifier)) {
        const base = new URL(specifier, context.parentURL);
        return { url: base.href + (existsSync(new URL(base.href + '.ts')) ? '.ts' : '.ets'), shortCircuit: true };
      }
      return { url: 'parity-client:' + specifier, shortCircuit: true };
    }
    if (specifier.startsWith('@kit.') || specifier.startsWith('@ohos.')) return { url: 'parity:noop', shortCircuit: true };
    if (['../platform/HubTransport', '../platform/LocalStore', '../platform/CredentialVault', '../platform/Pairing', '../platform/NativeNotifications'].includes(specifier)) {
      return { url: 'parity-client:' + specifier, shortCircuit: true };
    }
    if (specifier.endsWith('/design/Strings')) return { url: 'parity:strings', shortCircuit: true };
    if (specifier.endsWith('/core/FleetLiveState')) {
      return { url: new URL('../core/FleetLiveState.ts', context.parentURL).href, shortCircuit: true };
    }
    if (specifier.endsWith('/core/HubModules')) {
      return { url: new URL('../core/HubModules.ts', context.parentURL).href, shortCircuit: true };
    }
    if (context.parentURL?.endsWith('.ets') && specifier.startsWith('.')) {
      const base = new URL(specifier, context.parentURL);
      return { url: base.href + (existsSync(new URL(base.href + '.ts')) ? '.ts' : '.ets'), shortCircuit: true };
    }
    return next(specifier, context);
  },
  load(url, context, next) {
    if (url === 'parity:liveview-kit') {
      return {
        format: 'module',
        shortCircuit: true,
        source: `
          const s = globalThis.__parityLiveViewState;
          export const liveViewManager = {
            LayoutType: { LAYOUT_TYPE_DEFAULT: -1, LAYOUT_TYPE_PROGRESS: 3, LAYOUT_TYPE_PICKUP: 4 },
            ExtensionType: { EXTENSION_TYPE_CAPSULE_TEXT: 2 },
            LineType: { LINE_TYPE_NORMAL_SOLID_LINE: 1 },
            IndicatorType: { INDICATOR_TYPE_UNDISPLAYED: 0 },
            CapsuleType: { CAPSULE_TYPE_TEXT: 1, CAPSULE_TYPE_TIMER: 2, CAPSULE_TYPE_PROGRESS: 3 },
            LifeCycleMode: { STOP_BY_APP: 0, AUTO_STOP_WHEN_APP_TERMINATE: 1 },
            async isLiveViewEnabled() { return s.systemEnabled; },
            async startLiveView() { s.started++; },
            async updateLiveView() {},
            async stopLiveView() { s.stopped++; }
          };
        `
      };
    }
    if (url === 'parity:ability-kit') {
      return {
        format: 'module',
        shortCircuit: true,
        source: `
          export const bundleManager = { BundleFlag: { GET_BUNDLE_INFO_DEFAULT: 0 }, getBundleInfoForSelfSync: () => ({ name: 'com.nwverse.persona' }) };
          export const common = {};
          export const wantAgent = { OperationType: { START_ABILITY: 0 }, WantAgentFlags: { UPDATE_PRESENT_FLAG: 0 }, async getWantAgent() { return {}; } };
        `
      };
    }
    if (url === 'parity:want-agent' || url === 'parity:noop') return { format: 'module', shortCircuit: true, source: 'export {};' };
    if (url === 'parity:arkts') {
      return {
        format: 'module',
        shortCircuit: true,
        source: `
          export const url = { URL: { parseURL: value => new URL(value) } };
          export const util = {
            TextEncoder: class { encodeInto(v) { return new TextEncoder().encode(v); } },
            TextDecoder: { create: () => new TextDecoder() },
            generateRandomUUID: () => 'parity-uuid'
          };
        `
      };
    }
    if (url === 'parity:strings') {
      return { format: 'module', shortCircuit: true, source: 'export const tr = k => k; export const Strings = { locale: "en" };' };
    }
    if (url.startsWith('parity-client:')) {
      const mod = url.slice('parity-client:'.length);
      if (mod === '../platform/NativeLiveView') {
        return {
          format: 'module',
          shortCircuit: true,
          source: `
            globalThis.__paritySetForegroundCalls = [];
            export const nativeLiveView = {
              setForeground(fg, hasPushRoute, isDemo) {
                globalThis.__paritySetForegroundCalls.push([fg, hasPushRoute, isDemo]);
              },
              syncFromModel: async () => {},
              receiveFleetEvent: async () => {},
              stopNow: async () => {}
            };
          `
        };
      }
      return {
        format: 'module',
        shortCircuit: true,
        source: `
          export class HttpFailure extends Error { constructor(status=500, code="", resultUnknown=false, resultPending=false){ super(code); this.status=status; this.code=code; this.resultUnknown=resultUnknown; this.resultPending=resultPending; } }
          export class HubTransport { close(){} }
          export class EndpointConfig {}
          export class CredentialVault {}
          export class LocalStore { async get(){return "";} async put(){} async remove(){} async close(){} allowHub(){} async purgeHub(){} }
          export class HubRepository {}
          export class TranscriptModel {}
          export class SessionWorkspace {}
          export class SshTunnel { initialize(){} stop(){} async bind(){} async acquire(){return undefined;} async forget(){} }
          export class KnowledgeRecording { static async forgetProfile(){} }
          export const dictationPreference = { choice:"auto", last:"", load(){}, remember(){} };
          export const deviceLook = { async forgetHub(){} };
          export class Pairing {}
          export class PairingTarget {}
          export class MutationRecord {}
          export class MutationStore { static forgetHub(){} }
          export class ChatAttachments { static allowHub(){} static async forgetHub(){} static suppressStoredSnapshot(){} static async commitStoredSnapshot(){} }
          export function clearImagePreviews() {}
          export const nativeNotifications = { async clear(){}, features: () => [], receive: () => {} };
          export const tr = k => k;
          export const Strings = { locale: "en", refresh(){} };
          export const i18n = { getSystemLanguage(){ return "en-US"; }, System: { setAppPreferredLanguage(){} } };
          export const common = {};
          export const segment = encodeURIComponent;
          export const withSourceContext = text => text;
          export const messageDisplay = text => text;
        `
      };
    }
    if (url.endsWith('.ets')) {
      const raw = readFileSync(new URL(url), 'utf8')
        .replace(/^@ObservedV2\s*$/gm, '')
        .replace(/^(\s*)@Trace\s+/gm, '$1');
      return { format: 'module', shortCircuit: true, source: stripTypeScriptTypes(raw, { mode: 'transform' }) };
    }
    return next(url, context);
  }
});

const { NativeLiveView } = await import('../entry/src/main/ets/platform/NativeLiveView.ets');
const {
  decodeInteractionDecision,
  interactionCanHandBack,
  interactionNoHandBack,
  interactionResolution
} = await import('../entry/src/main/ets/models/InteractionDecision.ets');
const { ClientModel } = await import('../entry/src/main/ets/models/ClientModel.ets');

test('HubModules constants and module flag checks align with iOS HubModules.allows', () => {
  assert.equal(HUB_MODULES_MARKER, 'modules');
  assert.equal(HUB_MODULE_PREFIX, 'module.');
  assert.equal(MAIL_MODULE_FEATURE, 'module.mail');
  assert.equal(LARK_MODULE_FEATURE, 'module.lark');
  assert.equal(RECORDING_MODULE_FEATURE, 'module.recording');

  // Older hub without `modules` marker -> fail-open for every module
  assert.equal(extractPublishedModules(['ws_notify', 'connectors']), null);
  assert.equal(isHubModuleAllowed(MAIL_MODULE_FEATURE, ['ws_notify', 'connectors']), true);
  assert.equal(isHubModuleAllowed(LARK_MODULE_FEATURE, ['ws_notify', 'connectors']), true);
  assert.equal(isHubModuleAllowed(RECORDING_MODULE_FEATURE, ['ws_notify', 'connectors']), true);

  // Live features with `modules` marker take precedence over remembered modules
  assert.equal(isHubModuleAllowed(MAIL_MODULE_FEATURE, ['modules', 'module.lark'], ['module.mail']), false);
  assert.equal(isHubModuleAllowed(LARK_MODULE_FEATURE, ['modules', 'module.lark'], []), true);
  assert.equal(isHubModuleAllowed(MAIL_MODULE_FEATURE, ['ws_notify'], []), true);

  // Empty live features fall back to remembered modules; null/undefined fail-open
  assert.equal(isHubModuleAllowed(MAIL_MODULE_FEATURE, [], ['module.recording']), false);
  assert.equal(isHubModuleAllowed(MAIL_MODULE_FEATURE, [], ['module.mail']), true);
  assert.equal(isHubModuleAllowed(MAIL_MODULE_FEATURE, [], null), true);
  assert.equal(isHubModuleAllowed(MAIL_MODULE_FEATURE, [], undefined), true);

  // XiaoYiActions keeps its existing exports and delegates cleanly
  assert.equal(isRecordingModuleAllowed(['modules', 'module.mail'], ['module.recording']), false);
  assert.equal(isRecordingModuleAllowed(['modules', 'module.recording'], []), true);
  assert.deepEqual(parseStoredHubModules(serializeStoredHubModules(['module.mail'])), ['module.mail']);
});

test('parseHiddenHubPages handles pages.hidden, hidden, hidden_pages, arrays, and malformed JSON fail-open', () => {
  assert.deepEqual([...parseHiddenHubPages('')].sort(), []);
  assert.deepEqual([...parseHiddenHubPages('{not valid json')].sort(), []);
  assert.deepEqual([...parseHiddenHubPages('[]')].sort(), []);
  assert.deepEqual([...parseHiddenHubPages('{"pages":{"hidden":["inbox","todos"]}}')].sort(), ['inbox', 'todos']);
  assert.deepEqual([...parseHiddenHubPages('{"hidden":[" Inbox ","TODO"]}')].sort(), ['inbox', 'todos']);
  assert.deepEqual([...parseHiddenHubPages('{"hidden_pages":["todos"]}')].sort(), ['todos']);
  assert.deepEqual([...parseHiddenHubPages('{"pages":["inbox"]}')].sort(), ['inbox']);
  assert.deepEqual([...parseHiddenHubPages('{"pages":{"hidden_pages":["inbox","todos"]}}')].sort(), ['inbox', 'todos']);
});

test('hasRegisteredPushRoute and shouldEndLiveViewOnBackground mirror iOS #16783 lifecycle without invented feature flags', () => {
  const hubModulesSrc = readFileSync(new URL('../entry/src/main/ets/core/HubModules.ts', import.meta.url), 'utf8');
  assert.doesNotMatch(hubModulesSrc, /HUB_PUSH_ROUTE_FEATURES|hasHubPushRoute|hms_push|live_activity_push|notify_push|live_view_push|push_route/);

  assert.equal(hasRegisteredPushRoute(false), false);
  assert.equal(hasRegisteredPushRoute(true), true);

  assert.equal(shouldEndLiveViewOnBackground(false, false, true), true);
  assert.equal(shouldEndLiveViewOnBackground(true, false, true), false);
  assert.equal(shouldEndLiveViewOnBackground(false, true, true), false);
  assert.equal(shouldEndLiveViewOnBackground(false, false, false), false);
});

test('NativeLiveView.setForeground ends active non-demo LiveView on background without push route', async () => {
  liveState.started = 0;
  liveState.stopped = 0;
  const lv = new NativeLiveView();
  const ok = await lv.publishSample('working', 1_700_000_000_000);
  assert.equal(ok, true);
  assert.equal(lv.active, true);

  // With push route -> stays active on background
  lv.setForeground(false, true, false);
  await new Promise((r) => setTimeout(r, 0));
  assert.equal(lv.active, true);
  assert.equal(liveState.stopped, 0);

  // Demo mode without push route -> stays active on background
  lv.setForeground(false, false, true);
  await new Promise((r) => setTimeout(r, 0));
  assert.equal(lv.active, true);
  assert.equal(liveState.stopped, 0);

  // Real hub without push route -> ends immediately on background
  lv.setForeground(false, false, false);
  await new Promise((r) => setTimeout(r, 0));
  assert.equal(lv.active, false);
  assert.equal(liveState.stopped, 1);

  // Default arguments preserve backward compatibility
  lv.setForeground(true);
  lv.setForeground(false);
  await new Promise((r) => setTimeout(r, 0));
  assert.equal(liveState.stopped, 1);
});

test('InteractionDecision enforces no_hand_back and can_hand_back=false on dismiss', () => {
  const normal = decodeInteractionDecision({
    interaction: { id: 'ix_1', machine_id: 'm1', session_id: 's1', kind: 'permission', tool_name: 'Bash' }
  });
  assert.equal(interactionNoHandBack(normal), false);
  assert.equal(interactionCanHandBack(normal), true);
  assert.deepEqual(interactionResolution(normal, 'dismiss', {}), { dismiss: true });

  const noHandBack = decodeInteractionDecision({
    interaction: { id: 'ix_2', machine_id: 'm1', session_id: 's1', kind: 'permission', tool_name: 'Bash', no_hand_back: true }
  });
  assert.equal(interactionNoHandBack(noHandBack), true);
  assert.equal(interactionCanHandBack(noHandBack), false);
  assert.throws(() => interactionResolution(noHandBack, 'dismiss', {}), /unsupported/);

  const cannotHandBack = decodeInteractionDecision({
    interaction: { id: 'ix_3', machine_id: 'm1', session_id: 's1', kind: 'permission', tool_name: 'Bash', can_hand_back: false }
  });
  assert.equal(interactionNoHandBack(cannotHandBack), true);
  assert.equal(interactionCanHandBack(cannotHandBack), false);
  assert.throws(() => interactionResolution(cannotHandBack, 'dismiss', {}), /unsupported/);

  const canHandBackExplicit = decodeInteractionDecision({
    interaction: { id: 'ix_4', machine_id: 'm1', session_id: 's1', kind: 'permission', tool_name: 'Bash', can_hand_back: true }
  });
  assert.equal(interactionNoHandBack(canHandBackExplicit), false);
  assert.equal(interactionCanHandBack(canHandBackExplicit), true);
  assert.deepEqual(interactionResolution(canHandBackExplicit, 'dismiss', {}), { dismiss: true });

  const askUser = decodeInteractionDecision({
    interaction: { id: 'ix_5', machine_id: 'm1', session_id: 's1', kind: 'question', tool_name: 'ask_user' }
  });
  assert.equal(interactionCanHandBack(askUser), false);
  assert.throws(() => interactionResolution(askUser, 'dismiss', {}), /unsupported/);
});

test('ClientModel exposes module, page visibility, push route helpers, and background() passes push route and demo', async () => {
  const model = new ClientModel();
  // Default fail-open before hello
  assert.equal(model.mailModuleAllowed(), true);
  assert.equal(model.larkModuleAllowed(), true);
  assert.equal(model.inboxPageVisible(), true);
  assert.equal(model.todosPageVisible(), true);
  assert.equal(model.hasPushRoute(), false);

  // Hub announces `modules` without mail or lark, plus hidden pages
  model.features = ['modules', 'module.recording', 'ws_notify'];
  model.sidebarConfig = JSON.stringify({ pages: { hidden: ['inbox', 'todos'] } });
  assert.equal(model.mailModuleAllowed(), false);
  assert.equal(model.larkModuleAllowed(), false);
  assert.equal(model.inboxPageVisible(), false);
  assert.equal(model.todosPageVisible(), false);
  assert.equal(model.hasPushRoute(), false);

  globalThis.__paritySetForegroundCalls = [];
  await model.background();
  assert.deepEqual(globalThis.__paritySetForegroundCalls, [[false, false, false]]);

  // Hub announces mail & lark; Harmony has no Push Kit registration yet so hasPushRoute remains false on live Hub
  model.features = ['modules', 'module.mail', 'module.lark', 'ws_notify'];
  model.sidebarConfig = JSON.stringify({ pages: { hidden: ['todos'] } });
  assert.equal(model.mailModuleAllowed(), true);
  assert.equal(model.larkModuleAllowed(), true);
  assert.equal(model.inboxPageVisible(), true);
  assert.equal(model.todosPageVisible(), false);
  assert.equal(model.hasPushRoute(), false);

  globalThis.__paritySetForegroundCalls = [];
  await model.background();
  assert.deepEqual(globalThis.__paritySetForegroundCalls, [[false, false, false]]);

  // applyNavigationConfig clears stale hidden / hidden_pages / pages when a subsequent update un-hides them
  model.applyNavigationConfig({ hidden: ['inbox'], hidden_pages: ['todos'] });
  assert.equal(model.inboxPageVisible(), false);
  assert.equal(model.todosPageVisible(), false);
  model.applyNavigationConfig({ pages: { hidden: [] } });
  assert.equal(model.inboxPageVisible(), true);
  assert.equal(model.todosPageVisible(), true);

  // Demo mode stays fail-open and resets sidebarConfig / rememberedHubModules cleanly
  model.sidebarConfig = JSON.stringify({ hidden: ['inbox', 'todos'] });
  model.rememberedHubModules = [];
  model.useDemo();
  assert.equal(model.demo, true);
  assert.equal(model.sidebarConfig, '');
  assert.equal(model.rememberedHubModules, null);
  assert.equal(model.mailModuleAllowed(), true);
  assert.equal(model.larkModuleAllowed(), true);
  assert.equal(model.inboxPageVisible(), true);
  assert.equal(model.todosPageVisible(), true);
  assert.equal(model.hasPushRoute(), true);

  globalThis.__paritySetForegroundCalls = [];
  await model.background();
  assert.deepEqual(globalThis.__paritySetForegroundCalls, [[false, true, true]]);
});

test('UI files wire module guards, keep lark extension visible, skip disabled channel/connector Hub loads, and guard openMailCompose', () => {
  const index = readFileSync(new URL('../entry/src/main/ets/pages/Index.ets', import.meta.url), 'utf8');
  const inboxPage = readFileSync(new URL('../entry/src/main/ets/components/InboxWorkspacePage.ets', import.meta.url), 'utf8');
  const inboxWorkspace = readFileSync(new URL('../entry/src/main/ets/models/InboxWorkspace.ets', import.meta.url), 'utf8');
  const sources = readFileSync(new URL('../entry/src/main/ets/components/PhoneInboxSources.ets', import.meta.url), 'utf8');
  const settingsPage = readFileSync(new URL('../entry/src/main/ets/components/SettingsPage.ets', import.meta.url), 'utf8');
  const form = readFileSync(new URL('../entry/src/main/ets/components/InteractionDecisionForm.ets', import.meta.url), 'utf8');

  // Index.ets hidden page & mail compose guards
  assert.match(index, /if \(this\.model\.inboxPageVisible\(\)\) this\.spaceRow\('inbox'\)/);
  assert.match(index, /if \(this\.model\.todosPageVisible\(\)\) this\.spaceRow\('todo'\)/);
  assert.match(index, /if \(this\.model\.inboxPageVisible\(\)\) this\.railSpaceButton\('inbox'\)/);
  assert.match(index, /if \(this\.model\.todosPageVisible\(\)\) this\.railSpaceButton\('todo'\)/);
  assert.match(index, /if \(dest === 'todos'\) \{ this\.openSpace\(this\.model\.todosPageVisible\(\) \? 'todo' : 'persona'\); return; \}/);
  assert.match(index, /if \(dest === 'inbox'\) \{ this\.openSpace\(this\.model\.inboxPageVisible\(\) \? 'inbox' : 'persona'\); return; \}/);
  assert.match(index, /if \(this\.model\.mailModuleAllowed\(\)\) MenuItem\(\{content:tr\('mail_compose'\)\}\)/);

  // InboxWorkspace.ets & InboxWorkspacePage.ets mail module guard keeps stored items readable & blocks openMailCompose
  assert.match(inboxPage, /this\.model\.mailModuleAllowed\(\)/);
  assert.match(inboxPage, /Wire\.text\(this\.workspace\.detail,\s*'context'\)/);
  assert.match(inboxWorkspace, /async openMailCompose\(\): Promise<void> \{\s*if \(!this\.mailAllowed\(\)\) return;/);

  // PhoneInboxSources.ets keeps 'lark' extension visible when module.lark is off; only bot IDs are filtered
  assert.match(sources, /this\.model\.mailModuleAllowed\(\)/);
  assert.match(sources, /this\.model\.larkModuleAllowed\(\)/);
  const extAllowedBlock = sources.slice(sources.indexOf('private extensionIdAllowed('), sources.indexOf('private visibleGraphGroups('));
  assert.match(extAllowedBlock, /\['feishu',\s*'feishu_bot',\s*'feishu-bot',\s*'lark_bot',\s*'larksync'\]\.includes\(normalized\)/);
  assert.doesNotMatch(extAllowedBlock, /['"]lark['"]/);

  // SettingsPage.ets separates Lark bot (larksync/feishu/feishu-bot/lark_bot) from Lark extension and skips Hub load when off
  assert.match(settingsPage, /this\.model\.mailModuleAllowed\(\)/);
  assert.match(settingsPage, /this\.model\.larkModuleAllowed\(\)/);
  const connectorBlock = settingsPage.slice(settingsPage.indexOf('private connectorAllowed('), settingsPage.indexOf('private channelAllowed('));
  const channelBlock = settingsPage.slice(settingsPage.indexOf('private channelAllowed('), settingsPage.indexOf('@Builder connectorDetailPage('));
  assert.match(connectorBlock, /\['larksync',\s*'feishu_bot',\s*'feishu-bot',\s*'lark_bot'\]\.includes\(normalized\)/);
  assert.doesNotMatch(connectorBlock, /['"]lark['"]/);
  assert.match(channelBlock, /\['feishu',\s*'feishu_bot',\s*'feishu-bot',\s*'lark_bot'\]\.includes\(normalized\)/);
  assert.doesNotMatch(channelBlock, /['"]lark['"]/);
  assert.match(settingsPage, /if \(page === 'channel_detail' && !this\.channelAllowed\(row\.id\) \|\|[\s\S]*?page === 'connector_detail' && !this\.connectorAllowed\(row\.id\)\) \{[\s\S]*?return;\s*\}/);

  // InteractionDecisionForm.ets hides handback menu when disallowed
  assert.match(form, /interactionCanHandBack\(this\.request\)/);
  assert.doesNotMatch(form, /interactionToolName\(this\.request\)\s*!==\s*'ask_user'/);
});
