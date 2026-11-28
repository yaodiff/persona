import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { resolveMinutesWait } from '../entry/src/main/ets/core/MinutesWait.ts';

const fixture = JSON.parse(
  readFileSync(new URL('../../spec/fixtures/minutes-wait/cases.json', import.meta.url), 'utf8')
);

test('resolveMinutesWait matches every shared spec/fixtures/minutes-wait/cases.json case', () => {
  assert.ok(Array.isArray(fixture.cases) && fixture.cases.length >= 17);
  for (const c of fixture.cases) {
    const actual = resolveMinutesWait(c.transcription, c.minutes_state, c.has_minutes);
    assert.equal(actual.line, c.expected.line, `${c.name}: line mismatch`);
    if (Object.prototype.hasOwnProperty.call(c.expected, 'done_minutes')) {
      assert.equal(actual.doneMinutes, c.expected.done_minutes, `${c.name}: doneMinutes mismatch`);
    } else {
      assert.equal(actual.doneMinutes, undefined, `${c.name}: unexpected doneMinutes`);
    }
    if (Object.prototype.hasOwnProperty.call(c.expected, 'total_minutes')) {
      assert.equal(actual.totalMinutes, c.expected.total_minutes, `${c.name}: totalMinutes mismatch`);
    } else {
      assert.equal(actual.totalMinutes, undefined, `${c.name}: unexpected totalMinutes`);
    }
  }
});

test('KnowledgeWorkspacePage wires resolveMinutesWait and localized checking/writing strings exist in all locales', () => {
  const page = readFileSync(new URL('../entry/src/main/ets/components/KnowledgeWorkspacePage.ets', import.meta.url), 'utf8');
  assert.match(page, /resolveMinutesWait\(/);
  assert.match(page, /knowledge_minutes_checking_progress/);
  assert.match(page, /knowledge_minutes_checking/);
  assert.match(page, /knowledge_minutes_writing/);
  assert.match(page, /id\('knowledge\.minutes\.wait'\)/);

  const keys = ['knowledge_minutes_checking', 'knowledge_minutes_checking_progress', 'knowledge_minutes_writing'];
  for (const loc of ['base', 'en_US', 'zh_CN', 'ja_JP']) {
    const data = JSON.parse(readFileSync(new URL(`../entry/src/main/resources/${loc}/element/string.json`, import.meta.url), 'utf8'));
    const map = new Map(data.string.map((entry) => [entry.name, entry.value]));
    for (const key of keys) {
      const val = map.get(key);
      assert.ok(typeof val === 'string' && val.trim().length > 0, `${loc} missing ${key}`);
    }
    assert.ok(map.get('knowledge_minutes_checking_progress').includes('{done}'));
    assert.ok(map.get('knowledge_minutes_checking_progress').includes('{total}'));
  }
});
