import test from 'node:test';
import assert from 'node:assert/strict';
import { bubbleFoldSpec, evaluateBubbleFold } from '../entry/src/main/ets/core/BubbleFold.ts';

test('short messages never fold while multi-paragraph prompts fold past iOS thresholds', () => {
  assert.deepEqual(bubbleFoldSpec(1), { limit: 14, shown: 10 });
  assert.deepEqual(bubbleFoldSpec(1.5), { limit: 9, shown: 5 });

  const shortMsg = evaluateBubbleFold('Quick follow-up question.', 280, 1);
  assert.equal(shortMsg.foldable, false);

  const longLines = Array.from({ length: 18 }, (_, i) => `Step ${i + 1}: verify layout`).join('\n');
  const foldedRegular = evaluateBubbleFold(longLines, 280, 1);
  assert.equal(foldedRegular.foldable, true);
  assert.ok(foldedRegular.estimatedLines >= 18);
  assert.ok(foldedRegular.foldedText.endsWith('…'));
  assert.ok(foldedRegular.foldedText.split('\n').length <= 10);

  // Accessibility large font folds an 11-line message down to 5 shown lines
  const mediumLines = Array.from({ length: 11 }, (_, i) => `第${i + 1}条：鸿蒙折叠屏适配检查`).join('\n');
  assert.equal(evaluateBubbleFold(mediumLines, 280, 1).foldable, false);
  const foldedLarge = evaluateBubbleFold(mediumLines, 280, 1.5);
  assert.equal(foldedLarge.foldable, true);
  assert.ok(foldedLarge.foldedText.split('\n').length <= 5);
});
