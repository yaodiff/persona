import test from 'node:test';
import assert from 'node:assert/strict';
import { existsSync, readFileSync } from 'node:fs';
import { registerHooks } from 'node:module';
import { resolve } from 'node:path';

registerHooks({
  resolve(specifier, context, next) {
    if (context.parentURL?.includes('/entry/src/main/ets/core/') && specifier.startsWith('./') && !/\.[a-z]+$/i.test(specifier)) {
      return { url: new URL(specifier + '.ts', context.parentURL).href, shortCircuit: true };
    }
    return next(specifier, context);
  }
});

const {
  A2ATaskState,
  XiaoYiA2AEngine,
  classifyA2AIntent,
  extractMultimodalPrompt
} = await import('../entry/src/main/ets/core/XiaoYiA2AEngine.ts');

const MAIN_DIR = resolve(new URL('../entry/src/main', import.meta.url).pathname);

function makeSink() {
  const statuses = [];
  const artifacts = [];
  return {
    statuses,
    artifacts,
    updateStatus(taskId, state, messageText) {
      statuses.push({ taskId, state, messageText });
    },
    addArtifact(taskId, artifact) {
      artifacts.push({ taskId, ...artifact });
    }
  };
}

function makeBridge(overrides = {}) {
  const calls = [];
  const contexts = new Map();
  return {
    calls,
    contexts,
    async askPersona(question, conversationId, machineId, onProgress, isCancelled) {
      calls.push({ op: 'askPersona', question, conversationId, machineId });
      onProgress('Hello ', false);
      if (isCancelled()) return { answer: '', pending: false, machineId: 'm1', sessionId: 's1', deepLinkUri: 'persona://open?dest=assistant', askUserPrompt: '' };
      onProgress('Hello world!', true);
      return {
        answer: 'Hello world!',
        pending: false,
        machineId: 'm1',
        sessionId: 'main-1',
        deepLinkUri: 'persona://open?dest=session&machine=m1&session=main-1',
        askUserPrompt: ''
      };
    },
    async checkStatus() {
      calls.push({ op: 'checkStatus' });
      return {
        workingCount: 2,
        pendingCount: 1,
        unreadPersonalCount: 0,
        profileId: 'hub-primary',
        managerId: 'personal',
        topPending: {
          id: 'dec-1',
          machineId: 'm1',
          sessionId: 's1',
          kind: 'permission',
          status: 'open',
          summary: 'Run unit tests',
          toolName: 'Bash',
          createdAt: '2026-11-26T06:00:00Z',
          delegating: false,
          muted: false,
          quiet: false,
          planThen: '',
          hasRequiredFields: false
        },
        headline: '2 working, 1 need you.',
        detail: 'First pending: Run unit tests',
        spokenText: '2 working, 1 need you. First pending: Run unit tests'
      };
    },
    async isRecordingAllowed() {
      calls.push({ op: 'isRecordingAllowed' });
      return true;
    },
    async addTodo(title, notes) {
      calls.push({ op: 'addTodo', title, notes });
      return { id: 'todo-1', title, deepLinkUri: 'persona://open?dest=todos' };
    },
    async logWater(amountMl) {
      calls.push({ op: 'logWater', amountMl });
      return { recordedMl: amountMl || 250, todayMl: 1000, goalMl: 2000, deepLinkUri: 'persona://open?dest=health' };
    },
    async searchKnowledge(query) {
      calls.push({ op: 'searchKnowledge', query });
      return [{ path: 'docs/plan.md', line: 'HarmonyOS integration plan' }];
    },
    async sendAgentTask(prompt, machineName, sessionName) {
      calls.push({ op: 'sendAgentTask', prompt, machineName, sessionName });
      return { machineId: 'm1', machineName: 'Studio', sessionId: 's1', sessionTitle: 'Main', deepLinkUri: 'persona://open?dest=session&machine=m1&session=s1' };
    },
    async interruptSession(machineId, sessionId) {
      calls.push({ op: 'interruptSession', machineId, sessionId });
    },
    async getContextConversation(contextId) {
      return contexts.get(contextId);
    },
    async setContextConversation(contextId, machineId, sessionId) {
      contexts.set(contextId, { machineId, sessionId });
    },
    formatError(err) {
      return 'ERR:' + (err.message || 'failed');
    },
    ...overrides
  };
}

test('extractMultimodalPrompt combines text, http/file URLs, file descriptors, and structured data safely', () => {
  const combined = extractMultimodalPrompt([
    { text: 'Summarize this' },
    { url: 'https://example.com/spec', filename: 'spec.html' },
    { url: 'javascript:alert(1)' },
    { filename: 'notes.pdf', mediaType: 'application/pdf' },
    { data: { topic: 'release' } }
  ]);
  assert.ok(combined.includes('Summarize this'));
  assert.ok(combined.includes('[spec.html] https://example.com/spec'));
  assert.ok(!combined.includes('javascript:'));
  assert.ok(combined.includes('[File: notes.pdf (application/pdf)]'));
  assert.ok(combined.includes('"topic":"release"'));
});

test('classifyA2AIntent routes bilingual quick commands and general questions cleanly', () => {
  assert.equal(classifyA2AIntent('新建待办：买咖啡').kind, 'add_todo');
  assert.equal(classifyA2AIntent('新建待办：买咖啡').arg, '买咖啡');
  assert.equal(classifyA2AIntent('Log water 350 ml').kind, 'log_water');
  assert.equal(classifyA2AIntent('Log water 350 ml').amountMl, 350);
  assert.equal(classifyA2AIntent('搜索知识库：发布流程').kind, 'search_kb');
  assert.equal(classifyA2AIntent('Send to agent: run tests').kind, 'send_agent');
  assert.equal(classifyA2AIntent('查看状态').kind, 'status');
  assert.equal(classifyA2AIntent('Approve pending').kind, 'inspect_approval');
  assert.equal(classifyA2AIntent('Start meeting recording').kind, 'start_recording');
  assert.equal(classifyA2AIntent('开始会议录音').kind, 'start_recording');
  assert.equal(classifyA2AIntent('会議を録音').kind, 'start_recording');
  assert.equal(classifyA2AIntent('What is the weather in Tokyo?').kind, 'ask_persona');
});

test('Execute streams incremental chunks, persists contextId mapping, and emits SUBMITTED -> WORKING -> COMPLETED', async () => {
  const bridge = makeBridge();
  const engine = new XiaoYiA2AEngine(bridge);
  const sink = makeSink();

  await engine.handleMethod('Execute', {
    taskId: 't-1',
    contextId: 'ctx-1',
    userInput: 'Explain today',
    parts: [{ text: 'Explain today' }],
    locale: 'en'
  }, sink);

  assert.deepEqual(sink.statuses.map((s) => s.state), [
    A2ATaskState.SUBMITTED,
    A2ATaskState.WORKING,
    A2ATaskState.COMPLETED
  ]);
  assert.equal(sink.artifacts.length, 2);
  assert.equal(sink.artifacts[0].append, false);
  assert.equal(sink.artifacts[0].lastChunk, false);
  assert.equal(sink.artifacts[0].parts[0].text, 'Hello ');
  assert.equal(sink.artifacts[1].append, true);
  assert.equal(sink.artifacts[1].lastChunk, true);
  assert.equal(sink.artifacts[1].parts[0].text, 'world!');
  assert.deepEqual(bridge.contexts.get('ctx-1'), { machineId: 'm1', sessionId: 'main-1' });
});

test('Execute transitions to INPUT_REQUIRED for ask_user and approval inspection (never silent auto-approve)', async () => {
  const bridge = makeBridge({
    async askPersona() {
      return {
        answer: '',
        pending: false,
        machineId: 'm1',
        sessionId: 'main-1',
        deepLinkUri: 'persona://open?dest=session&machine=m1&session=main-1',
        askUserPrompt: 'Which branch should I check?'
      };
    }
  });
  const engine = new XiaoYiA2AEngine(bridge);
  const sink1 = makeSink();
  await engine.handleMethod('Execute', { taskId: 't-ask', contextId: 'c1', userInput: 'Check branch', parts: [], locale: 'en' }, sink1);
  assert.equal(sink1.statuses.at(-1).state, A2ATaskState.INPUT_REQUIRED);
  assert.equal(sink1.artifacts[0].metadata.cardType, 'answer');

  const sink2 = makeSink();
  await engine.handleMethod('Execute', { taskId: 't-app', contextId: 'c1', userInput: 'Approve pending', parts: [], locale: 'en' }, sink2);
  assert.equal(sink2.statuses.at(-1).state, A2ATaskState.INPUT_REQUIRED);
  assert.equal(sink2.artifacts[0].metadata.cardType, 'approval');
  assert.equal(sink2.artifacts[0].metadata.decisionId, 'dec-1');
  assert.equal(sink2.artifacts[0].metadata.canApproveInline, true);
  assert.equal(sink2.artifacts[0].metadata.profileId, 'hub-primary');
  assert.equal(sink2.artifacts[0].metadata.managerId, 'personal');
  assert.ok(sink2.artifacts[0].metadata.deepLinkUri.includes('profile=hub-primary'));
});

test('Execute refuses start_recording when Hub recording module is off and PerceptionSuggest omits the recording chip', async () => {
  const offBridge = makeBridge({
    async isRecordingAllowed() { return false; },
    async checkStatus() {
      return { workingCount: 0, pendingCount: 0, unreadPersonalCount: 0, headline: 'All clear.', detail: '', spokenText: 'All clear.' };
    }
  });
  const engine = new XiaoYiA2AEngine(offBridge, 100);

  const sinkRefused = makeSink();
  await engine.handleMethod('Execute', {
    taskId: 't-rec-off',
    contextId: '',
    userInput: '开始会议录音',
    parts: [{ text: '开始会议录音' }],
    locale: 'zh-CN'
  }, sinkRefused);
  assert.equal(sinkRefused.statuses.at(-1).state, A2ATaskState.REJECTED);
  assert.equal(sinkRefused.artifacts[0].parts[0].text, '这台 hub 关闭了会议录音。');
  assert.equal(sinkRefused.artifacts[0].metadata.deepLinkUri, 'persona://open?dest=recording');

  const sinkSug = makeSink();
  await engine.handleMethod('PerceptionSuggest', {
    taskId: 't-sug-rec-off',
    contextId: '',
    userInput: '',
    parts: [],
    locale: 'en'
  }, sinkSug);
  const chipIds = sinkSug.artifacts[0].parts[0].data.suggestions.map((c) => c.id);
  assert.ok(!chipIds.includes('start_recording'), `expected start_recording omitted when off, got ${chipIds.join(',')}`);

  // When recording module IS on, start_recording succeeds with autoStart=1
  const onEngine = new XiaoYiA2AEngine(makeBridge({ async isRecordingAllowed() { return true; } }));
  const sinkOn = makeSink();
  await onEngine.handleMethod('Execute', {
    taskId: 't-rec-on',
    contextId: '',
    userInput: 'Start meeting recording',
    parts: [{ text: 'Start meeting recording' }],
    locale: 'en'
  }, sinkOn);
  assert.equal(sinkOn.statuses.at(-1).state, A2ATaskState.COMPLETED);
  assert.equal(sinkOn.artifacts[0].metadata.deepLinkUri, 'persona://open?dest=recording&autoStart=1');
});

test('Execute streams >4096-char replies without silent truncation, rejects duplicate active taskId, and supports turn-1 onTarget cancel', async () => {
  const longText = 'A'.repeat(4096) + 'B'.repeat(1200);
  const longEngine = new XiaoYiA2AEngine(makeBridge({
    async askPersona(_q, _c, _m, onProgress) {
      onProgress(longText, true);
      return { answer: longText, pending: false, machineId: 'm1', sessionId: 's1', deepLinkUri: 'persona://open?dest=assistant', askUserPrompt: '' };
    }
  }));
  const sinkLong = makeSink();
  await longEngine.handleMethod('Execute', { taskId: 't-long', contextId: '', userInput: 'Long reply', parts: [], locale: 'en' }, sinkLong);
  assert.equal(sinkLong.artifacts.length, 2);
  assert.equal(sinkLong.artifacts[0].parts[0].text.length, 4096);
  assert.equal(sinkLong.artifacts[0].lastChunk, false);
  assert.equal(sinkLong.artifacts[1].parts[0].text.length, 1200);
  assert.equal(sinkLong.artifacts[1].append, true);
  assert.equal(sinkLong.artifacts[1].lastChunk, true);

  let releaseFirst;
  const turn1Bridge = makeBridge({
    async askPersona(_q, _c, _m, _p, isCancelled, onTarget) {
      onTarget?.('m-first', 's-first');
      await new Promise((r) => { releaseFirst = r; });
      return { answer: isCancelled() ? '' : 'late', pending: false, machineId: 'm-first', sessionId: 's-first', deepLinkUri: '', askUserPrompt: '' };
    }
  });
  const engine = new XiaoYiA2AEngine(turn1Bridge);
  const sink1 = makeSink();
  const sinkDup = makeSink();
  const sinkCancel = makeSink();
  const pending = engine.handleMethod('Execute', { taskId: 't-dup', contextId: '', userInput: 'First turn', parts: [], locale: 'en' }, sink1);
  await new Promise((r) => setTimeout(r, 10));
  await engine.handleMethod('Execute', { taskId: 't-dup', contextId: '', userInput: 'Duplicate', parts: [], locale: 'en' }, sinkDup);
  assert.equal(sinkDup.statuses[0].state, A2ATaskState.REJECTED);
  await engine.handleMethod('Cancel', { taskId: 't-dup', contextId: '', userInput: '', parts: [], locale: 'en' }, sinkCancel);
  releaseFirst();
  await pending;
  assert.ok(turn1Bridge.calls.some((c) => c.op === 'interruptSession' && c.machineId === 'm-first' && c.sessionId === 's-first'));
});

test('Execute transitions to AUTH_REQUIRED when unpaired and Cancel interrupts active session', async () => {
  const unpaired = new XiaoYiA2AEngine(makeBridge({
    async askPersona() { throw new Error('xiaoyi_not_paired'); }
  }));
  const sinkAuth = makeSink();
  await unpaired.handleMethod('Execute', { taskId: 't-auth', contextId: '', userInput: 'Hi', parts: [], locale: 'en' }, sinkAuth);
  assert.equal(sinkAuth.statuses.at(-1).state, A2ATaskState.AUTH_REQUIRED);

  let releaseAsk;
  const slowBridge = makeBridge({
    async getContextConversation() { return { machineId: 'm9', sessionId: 's9' }; },
    async askPersona(_q, _c, _m, _p, isCancelled) {
      await new Promise((r) => { releaseAsk = r; });
      return { answer: isCancelled() ? '' : 'Late', pending: false, machineId: 'm9', sessionId: 's9', deepLinkUri: '', askUserPrompt: '' };
    }
  });
  const engine = new XiaoYiA2AEngine(slowBridge);
  const sinkExec = makeSink();
  const sinkCancel = makeSink();
  const running = engine.handleMethod('Execute', { taskId: 't-slow', contextId: 'ctx-9', userInput: 'Long work', parts: [], locale: 'en' }, sinkExec);
  await new Promise((r) => setTimeout(r, 10));
  await engine.handleMethod('Cancel', { taskId: 't-slow', contextId: 'ctx-9', userInput: '', parts: [], locale: 'en' }, sinkCancel);
  releaseAsk();
  await running;
  assert.equal(sinkCancel.statuses.at(-1).state, A2ATaskState.CANCELED);
  assert.ok(slowBridge.calls.some((c) => c.op === 'interruptSession' && c.machineId === 'm9' && c.sessionId === 's9'));
  assert.equal(engine.activeTaskCount(), 0);
});

test('PerceptionSuggest prioritizes pending approvals and working tasks, and degrades cleanly on timeout or unpaired', async () => {
  const engine = new XiaoYiA2AEngine(makeBridge(), 100);
  const sink = makeSink();
  await engine.handleMethod('PerceptionSuggest', { taskId: 't-sug', contextId: '', userInput: '', parts: [], locale: 'zh-CN' }, sink);
  assert.equal(sink.statuses.at(-1).state, A2ATaskState.COMPLETED);
  const chips = sink.artifacts[0].parts[0].data.suggestions;
  assert.equal(chips.length, 4);
  assert.equal(chips[0].id, 'review_pending');
  assert.equal(chips[1].id, 'check_working');

  // Timeout budget fallback
  const slowEngine = new XiaoYiA2AEngine(makeBridge({
    async checkStatus() { return new Promise(() => {}); }
  }), 30);
  const sinkTimeout = makeSink();
  await slowEngine.handleMethod('PerceptionSuggest', { taskId: 't-to', contextId: '', userInput: '', parts: [], locale: 'en' }, sinkTimeout);
  assert.equal(sinkTimeout.statuses.at(-1).state, A2ATaskState.COMPLETED);
  assert.equal(sinkTimeout.artifacts[0].parts[0].data.suggestions[0].id, 'add_todo');

  // Unpaired fallback
  const authEngine = new XiaoYiA2AEngine(makeBridge({
    async checkStatus() { throw new Error('xiaoyi_not_paired'); }
  }), 100);
  const sinkUnpaired = makeSink();
  await authEngine.handleMethod('PerceptionSuggest', { taskId: 't-un', contextId: '', userInput: '', parts: [], locale: 'en' }, sinkUnpaired);
  assert.deepEqual(sinkUnpaired.artifacts[0].parts[0].data.suggestions.map((c) => c.id), ['pair_hub']);
});

test('agent_config.json conforms strictly to HarmonyOS API 26 agentCard.json schema', () => {
  const cfgPath = resolve(MAIN_DIR, 'resources/base/profile/agent_config.json');
  const cfg = JSON.parse(readFileSync(cfgPath, 'utf8'));
  assert.deepEqual(Object.keys(cfg), ['agentCards']);
  assert.equal(cfg.agentCards.length, 1);

  const card = cfg.agentCards[0];
  const schemaPath = '/Applications/DevEco-Studio.app/Contents/sdk/default/openharmony/toolchains/modulecheck/agentCard.json';
  if (existsSync(schemaPath)) {
    const schema = JSON.parse(readFileSync(schemaPath, 'utf8'));
    const cardSchema = schema.properties.agentCards.items;
    const allowedKeys = new Set(cardSchema.propertyNames.enum);
    for (const k of Object.keys(card)) assert.ok(allowedKeys.has(k), `disallowed key: ${k}`);
    for (const req of cardSchema.required) assert.ok(card[req] !== undefined && card[req] !== '', `missing required: ${req}`);
    const skillSchema = cardSchema.properties.skills.items;
    const allowedSkillKeys = new Set(skillSchema.propertyNames.enum);
    assert.ok(card.skills.length >= 6);
    for (const sk of card.skills) {
      for (const k of Object.keys(sk)) assert.ok(allowedSkillKeys.has(k), `disallowed skill key: ${k}`);
      for (const req of skillSchema.required) assert.ok(sk[req] !== undefined, `missing skill required: ${req}`);
    }
  }
  assert.equal(card.agentId, 'persona_harmony_agent');
  assert.equal(card.type, 'APP');
  assert.equal(card.capabilities.streaming, true);
  assert.equal(card.appInfo.abilityName, 'PersonaAgentExtensionAbility');
});
