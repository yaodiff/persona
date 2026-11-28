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
  buildInlineCardResolution,
  nextInlineConfirmStep,
  parseAgentCardPayload
} = await import('../entry/src/main/ets/core/AgentCardPayload.ts');
const { parseShareRecords } = await import('../entry/src/main/ets/core/SharePayload.ts');

const ENTRY_DIR = resolve(new URL('../entry', import.meta.url).pathname);
const MAIN_DIR = resolve(ENTRY_DIR, 'src/main');

test('parseAgentCardPayload decodes all 4 variants safely and restricts inline approval to permission/plan', () => {
  const status = parseAgentCardPayload({
    cardType: 'status',
    title: 'Persona Status',
    summary: '2 working, 1 need you',
    workingCount: 2,
    pendingCount: 1,
    deepLinkUri: 'persona://open?dest=agents',
    sessions: [
      { title: 'Build', state: 'working', machine: 'm1' },
      { title: 'Docs', state: 'idle', machine: 'm1' },
      { title: 'Test', state: 'needs_you', machine: 'm2' },
      { title: 'Overflow', state: 'idle', machine: 'm2' }
    ]
  });
  assert.equal(status.variant, 'status');
  assert.equal(status.sessions.length, 3);
  assert.equal(status.canApproveInline, false);

  const permCard = parseAgentCardPayload({
    cardType: 'approval',
    decisionId: 'dec-42',
    decisionKind: 'permission',
    canApproveInline: true,
    summary: 'Allow git status',
    deepLinkUri: 'javascript:bad()'
  });
  assert.equal(permCard.canApproveInline, true);
  assert.equal(permCard.deepLinkUri, 'persona://open?dest=agents');
  assert.equal(permCard.profileId, '');
  assert.deepEqual(buildInlineCardResolution('permission'), { allow: true });
  assert.deepEqual(buildInlineCardResolution('plan'), { approve: true, mode: 'acceptEdits' });
  assert.equal(buildInlineCardResolution('question'), undefined);

  const scopedCard = parseAgentCardPayload({
    cardType: 'approval',
    decisionId: 'dec-99',
    decisionKind: 'plan',
    canApproveInline: true,
    deepLinkUri: 'persona://open?dest=session&profile=hub-b&manager=work&machine=m2&session=s2'
  });
  assert.equal(scopedCard.profileId, 'hub-b');
  assert.equal(scopedCard.managerId, 'work');

  const questionCard = parseAgentCardPayload({
    cardType: 'approval',
    decisionId: 'dec-q',
    decisionKind: 'question',
    canApproveInline: true
  });
  assert.equal(questionCard.canApproveInline, false);
});

test('inline two-step confirmation state machine transitions cleanly and AgentCardPage avoids sub-windows', () => {
  let step = 'idle';
  step = nextInlineConfirmStep(step, 'request');
  assert.equal(step, 'confirming');
  step = nextInlineConfirmStep(step, 'cancel');
  assert.equal(step, 'idle');
  step = nextInlineConfirmStep(step, 'request');
  step = nextInlineConfirmStep(step, 'confirm');
  assert.equal(step, 'submitting');
  step = nextInlineConfirmStep(step, 'error');
  assert.equal(step, 'failed');
  step = nextInlineConfirmStep(step, 'request');
  step = nextInlineConfirmStep(step, 'confirm');
  step = nextInlineConfirmStep(step, 'ok');
  assert.equal(step, 'approved');

  const pageSrc = readFileSync(resolve(MAIN_DIR, 'ets/pages/AgentCardPage.ets'), 'utf8');
  assert.ok(!pageSrc.includes('showAlertDialog'), 'AgentUIExtensionAbility forbids sub-window dialogs');
  assert.ok(!pageSrc.includes('openCustomDialog'), 'AgentUIExtensionAbility forbids custom dialog sub-windows');
  assert.ok(pageSrc.includes('resolveDecision'));
});

test('parseShareRecords extracts bounded text/title/url and rejects unsafe URI schemes', () => {
  const fromShare = parseShareRecords([
    { title: 'HarmonyOS Guide', content: 'Key architectural notes', uri: 'https://example.com/guide' }
  ]);
  assert.equal(fromShare.hasContent, true);
  assert.equal(fromShare.route?.dest, 'share');
  assert.equal(fromShare.url, 'https://example.com/guide');
  assert.ok(fromShare.draftText.includes('HarmonyOS Guide'));
  assert.ok(fromShare.draftText.includes('https://example.com/guide'));

  const unsafe = parseShareRecords([{ content: '', uri: 'javascript:alert(1)' }]);
  assert.equal(unsafe.hasContent, false);

  const fallback = parseShareRecords([], 'https://example.com/page', { text: 'Excerpt' });
  assert.equal(fallback.hasContent, true);
  assert.equal(fallback.url, 'https://example.com/page');
  assert.equal(fallback.text, 'Excerpt');
});

test('module.json5 skillProfiles enforce 3-way match with skills/<name>/SKILL.md and srcEntries', () => {
  const moduleCfg = JSON.parse(readFileSync(resolve(MAIN_DIR, 'module.json5'), 'utf8'));
  const profiles = moduleCfg.module.skillProfiles;
  assert.ok(Array.isArray(profiles) && profiles.length === 2);

  const nameRegex = /^[a-z0-9]+(-[a-z0-9]+)*$/;
  const semverRegex = /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$/;
  const seen = new Set();

  for (const profile of profiles) {
    assert.match(profile.name, nameRegex);
    assert.match(profile.version, semverRegex);
    assert.equal(profile.abilityName, 'EntryAbility');
    assert.ok(['private', 'system', 'public'].includes(profile.visibility));
    assert.ok(!seen.has(profile.name));
    seen.add(profile.name);

    const skillMdPath = resolve(ENTRY_DIR, 'skills', profile.name, 'SKILL.md');
    assert.ok(existsSync(skillMdPath), `missing SKILL.md for ${profile.name}`);
    const md = readFileSync(skillMdPath, 'utf8');
    assert.match(md, new RegExp(`^---[\\s\\S]*?name:\\s*${profile.name}\\b`), `frontmatter name mismatch for ${profile.name}`);
    assert.match(md, new RegExp(`^---[\\s\\S]*?version:\\s*${profile.version.replace(/\./g, '\\.')}\\b`), `frontmatter version mismatch for ${profile.name}`);

    assert.ok(Array.isArray(profile.srcEntries) && profile.srcEntries.length > 0);
    for (const srcEntry of profile.srcEntries) {
      assert.ok(srcEntry.startsWith(`../../skills/${profile.name}/scripts/`), srcEntry);
      assert.ok(srcEntry.endsWith('.ets'), srcEntry);
      assert.ok(existsSync(resolve(MAIN_DIR, srcEntry)), `missing srcEntry: ${srcEntry}`);
    }
  }
});
