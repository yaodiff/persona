import test from 'node:test';
import assert from 'node:assert/strict';
import {
  buildEdgeScrubMarks,
  edgeScrubIndexAt,
  edgeScrubYOf,
  fitEdgeScrubColumn
} from '../entry/src/main/ets/core/EdgeScrub.ts';

test('EdgeScrub extracts user prompt marks (skipping /loop) and maps vertical coordinates accurately', () => {
  const marks = buildEdgeScrubMarks([
    { id: 'm1', role: 'user', text: 'First prompt', hasImages: false },
    { id: 'm2', role: 'assistant', text: 'Answer 1', hasImages: false },
    { id: 'm3', role: 'user', text: '/loop check status', hasImages: false },
    { id: 'm4', role: 'user', text: '', hasImages: true },
    { id: 'm5', role: 'user', text: 'Final deploy check', hasImages: false }
  ]);
  assert.equal(marks.length, 3);
  assert.equal(marks[0].ordinal, 1);
  assert.equal(marks[0].lineIndex, 0);
  assert.equal(marks[1].text, 'Photo');
  assert.equal(marks[2].preview, '3. Final deploy check');

  const col = fitEdgeScrubColumn(marks.length, 600, 60, 80);
  assert.ok(col);
  assert.equal(col.count, 3);
  assert.equal(col.first, 0);
  assert.equal(edgeScrubIndexAt(col, edgeScrubYOf(col, 0)), 0);
  assert.equal(edgeScrubIndexAt(col, edgeScrubYOf(col, 2)), 2);
  assert.equal(edgeScrubIndexAt(col, 0), 0);
  assert.equal(edgeScrubIndexAt(col, 9999), 2);
});
