import test from 'node:test';
import assert from 'node:assert/strict';
import {
  compactDisplayPath,
  extractTurnChangesFromRaw,
  extractWorkNowFromRaw,
  extractWorkStepsFromRaw,
  toolIconRole,
  toolVerb
} from '../entry/src/main/ets/core/WorkPresentation.ts';

test('toolVerb and toolIconRole mirror iOS ToolPresentation including MCP humanization', () => {
  assert.equal(toolVerb('Bash'), 'Run');
  assert.equal(toolIconRole('Bash'), 'terminal');
  assert.equal(toolVerb('Edit'), 'Edit');
  assert.equal(toolIconRole('Edit'), 'edit');
  assert.equal(toolVerb('Grep'), 'Search');
  assert.equal(toolIconRole('Grep'), 'search');
  assert.equal(toolVerb('mcp__github__create_pull_request'), 'Github · Create Pull Request');
  assert.equal(toolIconRole('mcp__github__create_pull_request'), 'skill');
  assert.equal(compactDisplayPath('/Users/example/Projects/persona/harmony/Index.ets'), 'harmony/Index.ets');
});

test('extractWorkStepsFromRaw, extractWorkNowFromRaw, and extractTurnChangesFromRaw parse structured tool turns', () => {
  const raw = JSON.stringify([
    { id: 't1', kind: 'thinking', text: '**Check build logs**\nInspecting ArkTS output.' },
    {
      id: 'u1',
      kind: 'tool_use',
      tool_use_id: 'call_1',
      tool_name: 'Read',
      tool_input: { file_path: '/demo/ReleaseDesk/CHANGELOG.md' }
    },
    { id: 'r1', kind: 'tool_result', tool_use_id: 'call_1', text: '# Release 1.2' },
    {
      id: 'u2',
      kind: 'tool_use',
      tool_use_id: 'call_2',
      tool_name: 'Edit',
      tool_input: {
        file_path: '/demo/ReleaseDesk/CHANGELOG.md',
        old_string: 'v1.1\n- old note',
        new_string: 'v1.2\n- harmony foldable split\n- live view kit'
      }
    },
    { id: 'r2', kind: 'tool_result', tool_use_id: 'call_2', text: 'Updated.' },
    {
      id: 'u3',
      kind: 'tool_use',
      tool_use_id: 'call_3',
      tool_name: 'Bash',
      tool_input: { description: 'Run release checklist', command: 'npm test' }
    }
  ]);

  const steps = extractWorkStepsFromRaw(raw, true);
  assert.equal(steps.length, 4);
  assert.equal(steps[0].kind, 'thought');
  assert.equal(steps[0].subject, 'Check build logs');
  assert.equal(steps[1].verb, 'Read');
  assert.equal(steps[1].subject, 'ReleaseDesk/CHANGELOG.md');
  assert.equal(steps[1].running, false);
  assert.equal(steps[2].verb, 'Edit');
  assert.equal(steps[2].additions, 3);
  assert.equal(steps[2].deletions, 2);
  assert.equal(steps[3].verb, 'Run');
  assert.equal(steps[3].subject, 'Run release checklist');
  assert.equal(steps[3].running, true);

  // WorkNow highlights the active running Bash description
  const now = extractWorkNowFromRaw(raw, true);
  assert.equal(now?.text, 'Run release checklist');
  assert.equal(now?.more, 0);
  assert.equal(extractWorkNowFromRaw(raw, false), undefined);

  // Parallel reads collapse into "Reading N files"
  const parallelReads = JSON.stringify([
    { id: 'a', kind: 'tool_use', tool_use_id: 'c1', tool_name: 'Read', tool_input: { file_path: '/a/one.ts' } },
    { id: 'b', kind: 'tool_use', tool_use_id: 'c2', tool_name: 'Read', tool_input: { file_path: '/a/two.ts' } }
  ]);
  assert.equal(extractWorkNowFromRaw(parallelReads, true)?.text, 'Reading 2 files');

  // TurnChangesCard extracts edited file with exact +3 / -2 counts
  const changes = extractTurnChangesFromRaw(raw);
  assert.ok(changes);
  assert.equal(changes.files.length, 1);
  assert.equal(changes.files[0].shown, 'ReleaseDesk/CHANGELOG.md');
  assert.equal(changes.totalAdditions, 3);
  assert.equal(changes.totalDeletions, 2);
  assert.equal(changes.exact, true);
});
