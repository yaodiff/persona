import test from 'node:test';
import assert from 'node:assert/strict';
import { existsSync, readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import {
  INTENT_NAME_PATTERN,
  INTENT_VERSION_PATTERN,
  XIAOYI_INTENT_CATALOG,
  buildSafeSuggestionSeeds,
  isGracefulSuggestionError
} from '../entry/src/main/ets/core/XiaoYiIntentCatalog.ts';

const MAIN_DIR = resolve(new URL('../entry/src/main', import.meta.url).pathname);

test('all catalog intents match API 26 InsightIntent naming, versioning, and file-export contracts', () => {
  const seenNames = new Set();
  assert.equal(XIAOYI_INTENT_CATALOG.length, 10);

  for (const desc of XIAOYI_INTENT_CATALOG) {
    assert.match(desc.intentName, INTENT_NAME_PATTERN, desc.intentName);
    assert.match(desc.intentVersion, INTENT_VERSION_PATTERN, desc.intentVersion);
    assert.ok(!seenNames.has(desc.intentName), `duplicate intentName: ${desc.intentName}`);
    seenNames.add(desc.intentName);

    const filePath = resolve(MAIN_DIR, desc.srcEntry);
    assert.ok(existsSync(filePath), `missing intent file: ${desc.srcEntry}`);
    const src = readFileSync(filePath, 'utf8');
    assert.ok(src.includes(`intentName: '${desc.intentName}'`), `${desc.srcEntry} intentName mismatch`);
    assert.ok(src.includes(`domain: '${desc.domain}'`), `${desc.srcEntry} domain mismatch`);
    assert.ok(src.includes(`intentVersion: '${desc.intentVersion}'`), `${desc.srcEntry} intentVersion mismatch`);
    assert.ok(src.includes("abilityName: 'EntryAbility'"), `${desc.srcEntry} must bind EntryAbility`);
    assert.match(src, /export default class \w+ extends InsightIntentEntryExecutor<PersonaActionResultEntity>/, `${desc.srcEntry} export contract`);
    assert.ok(src.includes('@InsightIntentEntry('), `${desc.srcEntry} missing @InsightIntentEntry decorator`);
  }
});

test('insight_intent.json registers every catalog srcEntry with no duplicates or unknown keys', () => {
  const profilePath = resolve(MAIN_DIR, 'resources/base/profile/insight_intent.json');
  const parsed = JSON.parse(readFileSync(profilePath, 'utf8'));
  assert.deepEqual(Object.keys(parsed), ['insightIntentsSrcEntry']);
  assert.ok(Array.isArray(parsed.insightIntentsSrcEntry));
  const entries = parsed.insightIntentsSrcEntry.map((row) => {
    assert.deepEqual(Object.keys(row), ['srcEntry']);
    assert.ok(row.srcEntry.length <= 127);
    assert.ok(existsSync(resolve(MAIN_DIR, row.srcEntry)), row.srcEntry);
    return row.srcEntry;
  });
  assert.equal(new Set(entries).size, entries.length);
  assert.deepEqual(entries.slice().sort(), XIAOYI_INTENT_CATALOG.map((d) => d.srcEntry).sort());
});

test('ApproveFirstPending intent enforces fail-closed lock-screen gating through approveFirstPending()', () => {
  const approveSrc = readFileSync(resolve(MAIN_DIR, 'ets/xiaoyi/intents/ApproveFirstPendingIntent.ets'), 'utf8');
  assert.ok(approveSrc.includes('hub.approveFirstPending()'));
  assert.ok(!approveSrc.includes('bypassPermissions'));
  assert.ok(approveSrc.includes('UI_ABILITY_BACKGROUND'));
  const hubSrc = readFileSync(resolve(MAIN_DIR, 'ets/xiaoyi/HeadlessHubService.ets'), 'utf8');
  assert.ok(hubSrc.includes('screenLock.isScreenLocked()'));
  assert.ok(hubSrc.includes("throw new HeadlessHubFailure('xiaoyi_unlock_required')"));
});

test('StartPersonaRecording, AssistantSkill, and Index deep-link enforce fail-closed recording-module guard (#15985)', () => {
  const startRecSrc = readFileSync(resolve(MAIN_DIR, 'ets/xiaoyi/intents/StartPersonaRecordingIntent.ets'), 'utf8');
  assert.ok(startRecSrc.includes('assertRecordingModuleAllowed()'));
  assert.ok(startRecSrc.includes('xiaoyi_recording_module_off'));

  const indexSrc = readFileSync(resolve(MAIN_DIR, 'ets/pages/Index.ets'), 'utf8');
  assert.ok(indexSrc.includes('isRecordingModuleAllowed()'));
  assert.ok(indexSrc.includes("tr('xiaoyi_recording_module_off')"));

  const skillSrc = readFileSync(resolve(MAIN_DIR, '../../skills/persona-assistant/scripts/AssistantSkill.ets'), 'utf8');
  assert.ok(skillSrc.includes('prepareRecordingStart()'));

  const recordingPageSrc = readFileSync(resolve(MAIN_DIR, 'ets/components/KnowledgeWorkspacePage.ets'), 'utf8');
  assert.ok(recordingPageSrc.includes('isRecordingModuleAllowed()'));
  assert.ok(recordingPageSrc.includes('recording.canStop()'));

  const recordingViewSrc = readFileSync(resolve(MAIN_DIR, 'ets/components/KnowledgeRecordingView.ets'), 'utf8');
  assert.ok(recordingViewSrc.includes('recordingAllowed'));
  assert.ok(recordingViewSrc.includes('xiaoyi_recording_module_off'));

  const clientModelSrc = readFileSync(resolve(MAIN_DIR, 'ets/models/ClientModel.ets'), 'utf8');
  assert.ok(clientModelSrc.includes('static registerStatusHook('));
  assert.ok(clientModelSrc.includes('notifyStatusHooks()'));

  const suggestionsSrc = readFileSync(resolve(MAIN_DIR, 'ets/xiaoyi/IntentSuggestionsService.ets'), 'utf8');
  assert.ok(suggestionsSrc.includes('static scheduleStatusShare('));
  assert.ok(suggestionsSrc.includes('static resetSchedule('));
});

test('AppIntentEntity classes declare supportedQueryProperties and onQueryEntity', () => {
  const entitiesSrc = readFileSync(resolve(MAIN_DIR, 'ets/xiaoyi/intents/PersonaIntentEntities.ets'), 'utf8');
  assert.ok(entitiesSrc.includes("entityCategory: 'PersonaMachine'"));
  assert.ok(entitiesSrc.includes("supportedQueryProperties: ['name', 'online']"));
  assert.ok(entitiesSrc.includes("entityCategory: 'PersonaSession'"));
  assert.ok(entitiesSrc.includes("supportedQueryProperties: ['title', 'machine', 'status']"));
  assert.ok(entitiesSrc.includes('extends insightIntent.AppIntentEntity<PersonaMachineEntity>'));
  assert.ok(entitiesSrc.includes('extends insightIntent.AppIntentEntity<PersonaSessionEntity>'));
});

test('buildSafeSuggestionSeeds stays privacy-safe and bounded, and handles graceful Intents Kit error codes', () => {
  const withPending = buildSafeSuggestionSeeds(3, 2);
  assert.equal(withPending[0].intentName, 'CheckPersonaStatus');
  assert.ok(withPending[0].title.includes('3'));
  assert.equal(withPending[1].intentName, 'OpenPersonaDestination');
  assert.ok(withPending.every((s) => s.title.length <= 64));

  const idle = buildSafeSuggestionSeeds(0, 0);
  assert.deepEqual(idle.map((s) => s.intentName), ['AskPersona', 'AddPersonaTodo']);

  assert.equal(isGracefulSuggestionError(1000101101), true);
  assert.equal(isGracefulSuggestionError(1000101201), true);
  assert.equal(isGracefulSuggestionError(401), false);
});
