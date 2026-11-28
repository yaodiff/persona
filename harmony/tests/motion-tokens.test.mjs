import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import {
  MotionTokens,
  clampDrawerVelocity,
  resolveMotionDuration,
  shouldAnimateLoop
} from '../entry/src/main/ets/core/MotionTokens.ts';

test('motion token durations align to iOS Theme.Motion and respect Reduce Motion', () => {
  assert.equal(MotionTokens.stateMs, 250);
  assert.equal(MotionTokens.layoutMs, 300);
  assert.equal(MotionTokens.expandMs, 400);
  assert.equal(MotionTokens.revealMs, 280);
  assert.equal(MotionTokens.concealMs, 200);
  assert.equal(MotionTokens.drawerMs, 320);
  assert.equal(MotionTokens.breathDurationMs, 1000);
  assert.equal(MotionTokens.workingArcTurnMs, 1000);

  assert.equal(resolveMotionDuration(250, false), 250);
  assert.equal(resolveMotionDuration(250, true), 0);
  assert.equal(resolveMotionDuration(-10, false), 0);
  assert.equal(resolveMotionDuration(Number.NaN, false), 0);
});

test('continuous loop motion obeys P9: runs only when visible, foregrounded, and not reduced', () => {
  assert.equal(shouldAnimateLoop(true, true, false), true);
  assert.equal(shouldAnimateLoop(false, true, false), false);
  assert.equal(shouldAnimateLoop(true, false, false), false);
  assert.equal(shouldAnimateLoop(true, true, true), false);
  assert.equal(shouldAnimateLoop(false, false, true), false);
});

test('drawer spring velocity clamps extreme flings and rejects invalid spans', () => {
  assert.equal(clampDrawerVelocity(320, 320), 1);
  assert.equal(clampDrawerVelocity(-640, 320), -2);
  assert.equal(clampDrawerVelocity(50000, 320), 12);
  assert.equal(clampDrawerVelocity(-50000, 320), -12);
  assert.equal(clampDrawerVelocity(400, 0), 0);
  assert.equal(clampDrawerVelocity(Number.NaN, 320), 0);
});

test('status motion marks guard every repeating animation with visibility, foreground, and reduceMotion', () => {
  const source = readFileSync(new URL('../entry/src/main/ets/components/StatusMotionMarks.ets', import.meta.url), 'utf8');
  for (const name of ['NeedsYouPulseDot', 'WorkingArcSpinner', 'StreamTailCaret']) {
    assert.ok(source.includes(`export struct ${name}`), name);
  }
  assert.equal((source.match(/shouldAnimateLoop\(this\.visible, this\.env\.foreground, this\.env\.reduceMotion\)/g) || []).length, 3);
  assert.equal((source.match(/onVisibleAreaChange\(/g) || []).length, 3);
  assert.ok(source.includes('HitTestMode.None'));
});
