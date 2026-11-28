import test from 'node:test';
import assert from 'node:assert/strict';
import { registerHooks, stripTypeScriptTypes } from 'node:module';
import { readFileSync } from 'node:fs';
import {
  FLEET_LIVE_VIEW_ID,
  FLEET_STALE_RENEW_MS,
  buildFleetLiveViewSpec,
  decideFleetStep,
  decodeFleetState,
  deriveFleetFromSessions,
  emptyFleetState,
  isFleetWorthUpdating,
  sampleFleetState,
  shouldClearFleetNow
} from '../entry/src/main/ets/core/FleetLiveState.ts';

const liveState = {
  systemEnabled: true,
  started: [],
  updated: [],
  stopped: [],
  startErrorCode: 0,
  updateErrorCode: 0,
  wants: [],
  storage: new Map()
};
globalThis.__personaLiveViewTest = liveState;

registerHooks({
  resolve(specifier, context, next) {
    if (specifier === '@kit.LiveViewKit') return { url: 'liveview-test:kit', shortCircuit: true };
    if (specifier === '@kit.AbilityKit') return { url: 'liveview-test:ability', shortCircuit: true };
    if (specifier === '@ohos.wantAgent') return { url: 'liveview-test:want', shortCircuit: true };
    if (specifier.endsWith('/design/Strings')) return { url: 'liveview-test:strings', shortCircuit: true };
    if (specifier.endsWith('/core/FleetLiveState')) {
      return { url: new URL('../core/FleetLiveState.ts', context.parentURL).href, shortCircuit: true };
    }
    if (specifier.endsWith('/core/HubModules')) {
      return { url: new URL('../core/HubModules.ts', context.parentURL).href, shortCircuit: true };
    }
    if (context.parentURL?.endsWith('.ets') && specifier.startsWith('.')) {
      return { url: new URL(specifier + '.ets', context.parentURL).href, shortCircuit: true };
    }
    return next(specifier, context);
  },
  load(url, context, next) {
    if (url === 'liveview-test:kit') {
      return {
        format: 'module',
        shortCircuit: true,
        source: `
          const s = globalThis.__personaLiveViewTest;
          export const liveViewManager = {
            LayoutType: { LAYOUT_TYPE_DEFAULT: -1, LAYOUT_TYPE_PROGRESS: 3, LAYOUT_TYPE_PICKUP: 4 },
            ExtensionType: { EXTENSION_TYPE_CAPSULE_TEXT: 2 },
            LineType: { LINE_TYPE_NORMAL_SOLID_LINE: 1 },
            IndicatorType: { INDICATOR_TYPE_UNDISPLAYED: 0 },
            CapsuleType: { CAPSULE_TYPE_TEXT: 1, CAPSULE_TYPE_TIMER: 2, CAPSULE_TYPE_PROGRESS: 3 },
            LifeCycleMode: { STOP_BY_APP: 0, AUTO_STOP_WHEN_APP_TERMINATE: 1 },
            async isLiveViewEnabled() { return s.systemEnabled; },
            async startLiveView(view) {
              if (s.startErrorCode) { const e = new Error('start'); e.code = s.startErrorCode; s.startErrorCode = 0; throw e; }
              s.started.push(view);
              return { resultCode: 0, message: 'ok' };
            },
            async updateLiveView(view) {
              if (s.updateErrorCode) { const e = new Error('update'); e.code = s.updateErrorCode; s.updateErrorCode = 0; throw e; }
              s.updated.push(view);
              return { resultCode: 0, message: 'ok' };
            },
            async stopLiveView(view) {
              s.stopped.push(view);
              return { resultCode: 0, message: 'ok' };
            }
          };
        `
      };
    }
    if (url === 'liveview-test:ability') {
      return {
        format: 'module',
        shortCircuit: true,
        source: `
          const s = globalThis.__personaLiveViewTest;
          export const common = {};
          export const bundleManager = {
            BundleFlag: { GET_BUNDLE_INFO_DEFAULT: 0 },
            getBundleInfoForSelfSync: () => ({ name: 'dev.example.persona' })
          };
          export const wantAgent = {
            OperationType: { START_ABILITY: 0 },
            WantAgentFlags: { UPDATE_PRESENT_FLAG: 0 },
            async getWantAgent(info) { s.wants.push(info); return info; }
          };
        `
      };
    }
    if (url === 'liveview-test:want') return { format: 'module', shortCircuit: true, source: 'export {};' };
    if (url === 'liveview-test:strings') return { format: 'module', shortCircuit: true, source: 'export const tr = key => key;' };
    if (url.endsWith('.ets')) {
      const source = readFileSync(new URL(url), 'utf8').replace(/@ObservedV2\s*/g, '').replace(/@Trace\s+/g, '');
      return { format: 'module', shortCircuit: true, source: stripTypeScriptTypes(source) };
    }
    return next(url, context);
  }
});

const { NativeLiveView, LIVE_VIEW_ACTION } = await import('../entry/src/main/ets/platform/NativeLiveView.ets');

const strings = {
  appTitle: 'Persona',
  needsYouCount: '{count} need you',
  workingCount: '{count} working',
  unreadCount: '{count} unread',
  allCaughtUp: 'All Caught Up',
  assistantReplying: 'Assistant replying',
  backgroundTag: 'Background',
  anonPermission: 'A permission request is waiting',
  anonPlan: 'A plan needs your approval',
  anonQuestion: 'A question needs your answer',
  anonScreen: 'A terminal needs you',
  anonWorking: 'Working',
  anonUnread: 'Finished · unread',
  capsuleNeeds: 'Needs {count}',
  capsuleWorking: 'Working {count}',
  capsuleUnread: 'Unread {count}',
  capsuleCaughtUp: 'Done',
  moreSuffix: '+{count}'
};

function resetLiveMocks() {
  liveState.systemEnabled = true;
  liveState.started = [];
  liveState.updated = [];
  liveState.stopped = [];
  liveState.startErrorCode = 0;
  liveState.updateErrorCode = 0;
  liveState.wants = [];
  liveState.storage = new Map();
}

test('FleetState decoding, privacy masking, single-vs-multi deep-link routing, and background progress match iOS spec', () => {
  const hubFrame = {
    type: 'fleet',
    fleet: {
      phase: 'needsYou',
      needsYou: 1,
      working: 2,
      unread: 0,
      workingSince: 1761700000,
      items: [
        { id: 'mac/s1', title: 'Fix TLS pin', place: 'Studio · persona', kind: 'permission', detail: 'Bash: cargo test', interaction: 'ix_1', since: 1761700010 },
        { id: 'mac/s2', title: 'UI audit', place: 'Studio · harmony', kind: 'working', detail: 'parity · 18/24', background: true, progress: { done: 18, started: 24 }, since: 1761700000 }
      ]
    }
  };

  // Lock screen privacy off -> masks titles & clears interaction IDs; single Needs-You agent deep-links directly
  // and capsule title stays ultra-compact (`!1`) for pill-cutout displays.
  const masked = decodeFleetState(hubFrame, false);
  assert.equal(masked.items[0].interaction, '');
  const maskedSpec = buildFleetLiveViewSpec(masked, strings, 1761700100000);
  assert.equal(maskedSpec.id, FLEET_LIVE_VIEW_ID);
  assert.equal(maskedSpec.title, '1 need you');
  assert.equal(maskedSpec.content[0].text, 'A permission request is waiting +1');
  assert.equal(maskedSpec.capsuleKind, 'text');
  assert.equal(maskedSpec.capsuleTitle, '!1');
  assert.equal(maskedSpec.capsuleContent, 'Working 2');
  assert.equal(maskedSpec.capsuleBackgroundColor, '#FFD93A3A');
  assert.equal(maskedSpec.isMute, false);
  assert.equal(maskedSpec.targetMachine, 'mac');
  assert.equal(maskedSpec.targetSession, 's1');

  // Multiple Needs-You items -> returns to full triage list instead of picking one session.
  const multiNeeds = decodeFleetState({
    type: 'fleet',
    fleet: {
      phase: 'needsYou',
      needsYou: 2,
      working: 0,
      unread: 0,
      items: [
        { id: 'mac/s1', title: 'A', kind: 'permission', detail: 'cmd' },
        { id: 'mac/s2', title: 'B', kind: 'plan', detail: 'plan' }
      ]
    }
  }, false);
  const multiSpec = buildFleetLiveViewSpec(multiNeeds, strings, 1761700100000);
  assert.equal(multiSpec.targetMachine, '');
  assert.equal(multiSpec.targetSession, '');
  assert.equal(multiSpec.capsuleTitle, '2');

  // Lock screen details on -> displays item titles and details.
  const detailed = decodeFleetState(hubFrame, true);
  assert.equal(detailed.items[0].interaction, 'ix_1');
  const detailedSpec = buildFleetLiveViewSpec(detailed, strings, 1761700100000);
  assert.ok(detailedSpec.content[0].text.includes('Fix TLS pin'));
  assert.ok(detailedSpec.content[0].text.includes('Background · parity · 18/24'));

  // Single active working session with background progress -> deep-links directly + uses PROGRESS layout & capsule.
  const singleBg = deriveFleetFromSessions([{
    machine: 'studio', session: 's9', title: 'Audit', place: 'Studio', state: 'working', detail: '',
    updatedAtSec: 1000, startedAtSec: 900, muted: false, backgroundWorking: true,
    backgroundDetail: 'audit · 3/4', progressDone: 3, progressStarted: 4, interactionKind: '', interactionId: ''
  }], true);
  const bgSpec = buildFleetLiveViewSpec(singleBg, strings, 1020000);
  assert.equal(bgSpec.event, 'PROGRESS');
  assert.equal(bgSpec.layoutKind, 'progress');
  assert.equal(bgSpec.capsuleKind, 'progress');
  assert.equal(bgSpec.progressPercent, 75);
  assert.equal(bgSpec.targetMachine, 'studio');
  assert.equal(bgSpec.targetSession, 's9');
});

test('Fleet lifecycle step, immediate Needs-You clearing (#8636), and stale-renew throttling mirror LiveActivityController', () => {
  const needs = sampleFleetState('needsYou', false, 100000);
  const doneOnly = sampleFleetState('done', false, 100000);
  const empty = emptyFleetState(false);

  // Foreground starts for needsYou/working, never starts solely for finished-unread.
  assert.equal(decideFleetStep(needs, false, true), 'start');
  assert.equal(decideFleetStep(needs, false, false), 'nothing');
  assert.equal(decideFleetStep(doneOnly, false, true), 'nothing');
  assert.equal(decideFleetStep(doneOnly, true, true), 'update');
  assert.equal(decideFleetStep(empty, true, true), 'endSoon');
  assert.equal(decideFleetStep(empty, false, true), 'nothing');

  // Last Needs You going empty clears immediately; done-only going empty waits for end timer.
  assert.equal(shouldClearFleetNow(needs, empty), true);
  assert.equal(shouldClearFleetNow(doneOnly, empty), false);

  // Identical state is throttled until the 10-minute renew window.
  assert.equal(isFleetWorthUpdating(needs, 1000, needs, 1000 + 60_000), false);
  assert.equal(isFleetWorthUpdating(needs, 1000, needs, 1000 + FLEET_STALE_RENEW_MS), true);
});

test('NativeLiveView starts, deduplicates, recovers 1003500006/1003500009, persists prefs, and stops on Forget Hub', async () => {
  resetLiveMocks();
  const controller = new NativeLiveView();
  const storage = {
    async get(k) { return liveState.storage.get(k); },
    async put(k, v) { liveState.storage.set(k, v); }
  };
  await controller.initialize({}, storage);
  const fakeModel = { profile: { id: 'hub-1', manager: 'work' } };

  // Start from Hub fleet event.
  const frame = {
    type: 'fleet',
    fleet: {
      phase: 'needsYou',
      needsYou: 1,
      working: 0,
      unread: 0,
      items: [{ id: 'studio/s1', title: 'Approve', kind: 'permission', detail: 'Bash: ls' }]
    }
  };
  await controller.receiveFleetEvent(fakeModel, frame, 5000);
  assert.equal(controller.active, true);
  assert.equal(liveState.started.length, 1);
  assert.equal(liveState.started[0].lifeCycleMode, 1);
  assert.equal(liveState.wants[0].wants[0].action, LIVE_VIEW_ACTION);
  assert.deepEqual(liveState.wants[0].wants[0].parameters, { profile: 'hub-1', manager: 'work', machine: 'studio', session: 's1' });

  // Duplicate event within renew window does not re-call updateLiveView.
  await controller.receiveFleetEvent(fakeModel, frame, 9000);
  assert.equal(liveState.updated.length, 0);

  // Toggling lockDetails updates the active LiveView immediately and persists preferences.
  await controller.setLockDetails(true);
  assert.equal(liveState.updated.length, 1);
  assert.equal(JSON.parse(liveState.storage.get('live_view_prefs')).lockDetails, true);

  // Clearing the last Needs You sends an immediate empty update before the 60s end timer.
  await controller.receiveFleetEvent(fakeModel, { type: 'fleet', fleet: { phase: 'none', needsYou: 0, working: 0, unread: 0, items: [] } }, 12000);
  assert.equal(liveState.updated.length, 2);

  // Forgetting an unrelated Hub keeps the active LiveView; forgetting its owning Hub stops it.
  await controller.stopNow('other-hub');
  assert.equal(controller.active, true);
  await controller.stopNow('hub-1');
  assert.equal(controller.active, false);
  assert.equal(liveState.stopped.length, 1);

  // Recovering from 1003500006 (already exists) falls back to updateLiveView cleanly.
  liveState.startErrorCode = 1003500006;
  const ok = await controller.publishSample('working', 20000);
  assert.equal(ok, true);
  assert.equal(liveState.updated.length, 3);
  await controller.stopNow();
});
