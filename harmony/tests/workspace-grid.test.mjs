import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { registerHooks, stripTypeScriptTypes } from 'node:module';
import {
  WORKSPACE_A11Y_SINGLE_COL_FONT_SCALE,
  WORKSPACE_ULTRA_WIDE_BREAKPOINT_VP,
  WORKSPACE_WIDE_BREAKPOINT_VP,
  workspaceGridColumns,
  workspaceGridGutter,
  workspaceHorizontalPadding
} from '../entry/src/main/ets/core/WorkspaceGrid.ts';

registerHooks({
  resolve(specifier, context, next) {
    if (specifier.endsWith('/Wire') && context.parentURL?.endsWith('/MachineDashboard.ets')) {
      return { url: new URL(specifier + '.ets', context.parentURL).href, shortCircuit: true };
    }
    return next(specifier, context);
  },
  load(url, context, next) {
    if (!url.endsWith('/MachineDashboard.ets') && !url.endsWith('/Wire.ets')) return next(url, context);
    const raw = readFileSync(new URL(url), 'utf8').replace(/@ObservedV2\s*/g, '').replace(/@Trace\s+/g, '');
    return { format: 'module', source: stripTypeScriptTypes(raw), shortCircuit: true };
  }
});
const { formatMachineLoad } = await import('../entry/src/main/ets/models/MachineDashboard.ets');

test('workspaceGridColumns adapts from straight phone to Mate X7 dual-fold and Mate XT tri-fold', () => {
  assert.equal(WORKSPACE_WIDE_BREAKPOINT_VP, 560);
  assert.equal(WORKSPACE_ULTRA_WIDE_BREAKPOINT_VP, 960);
  assert.equal(WORKSPACE_A11Y_SINGLE_COL_FONT_SCALE, 1.6);

  // Straight phones & cover screens (< 560vp)
  for (const width of [-10, 0, Number.NaN, 320, 346, 366, 402, 559.9]) {
    assert.equal(workspaceGridColumns(width, 1.0), 1, 'narrow width ' + width);
  }

  // Mate X7 unfolded & 2-screen Mate XT (560–959vp)
  for (const width of [560, 640, 680, 707, 840, 959.9]) {
    assert.equal(workspaceGridColumns(width, 1.0), 2, 'dual-fold width ' + width);
    assert.equal(workspaceGridColumns(width, 1.45), 2, 'medium text on dual-fold ' + width);
  }

  // Mate XT tri-fold & MatePad Pro 13 (>= 960vp)
  for (const width of [960, 1060, 1280, 1440]) {
    assert.equal(workspaceGridColumns(width, 1.0), 3, 'tri-fold width ' + width);
    assert.equal(workspaceGridColumns(width, 1.0, 2), 2, 'maxCols=2 cap at ' + width);
    assert.equal(workspaceGridColumns(width, 1.0, 1), 1, 'maxCols=1 cap at ' + width);
  }
});

test('workspaceGridColumns collapses to 1 column under accessibility fontScale >= 1.6', () => {
  assert.equal(workspaceGridColumns(707, 1.59), 2);
  assert.equal(workspaceGridColumns(1060, 1.59), 3);
  for (const scale of [1.6, 1.75, 2.0, 3.2]) {
    assert.equal(workspaceGridColumns(707, scale), 1, 'Mate X7 at scale ' + scale);
    assert.equal(workspaceGridColumns(1060, scale), 1, 'Mate XT at scale ' + scale);
  }
});

test('workspaceHorizontalPadding and workspaceGridGutter scale smoothly across display classes', () => {
  for (const width of [0, Number.NaN, 346, 366, 559]) {
    assert.equal(workspaceHorizontalPadding(width), 16, 'phone padding ' + width);
  }
  for (const width of [560, 707, 959]) {
    assert.equal(workspaceHorizontalPadding(width), 20, 'foldable padding ' + width);
  }
  for (const width of [960, 1060, 1280]) {
    assert.equal(workspaceHorizontalPadding(width), 24, 'wide padding ' + width);
  }

  assert.equal(workspaceGridGutter(0), 10);
  assert.equal(workspaceGridGutter(1), 10);
  assert.equal(workspaceGridGutter(2), 12);
  assert.equal(workspaceGridGutter(3), 14);
});

test('all five secondary workspaces wire flagship elevation, haptics, and foldable responsiveness', () => {
  const read = (file) => readFileSync(new URL('../entry/src/main/ets/components/' + file, import.meta.url), 'utf8');
  for (const file of [
    'InboxWorkspacePage.ets',
    'TodoWorkspacePage.ets',
    'KnowledgeWorkspacePage.ets',
    'HealthWorkspacePage.ets',
    'MachineDirectory.ets',
    'ResourcePage.ets'
  ]) {
    const source = read(file);
    assert.match(source, /WorkspaceGrid/, file + ' imports WorkspaceGrid');
    assert.match(source, /PhoneStyle\.cardElevation\(/, file + ' applies PhoneStyle.cardElevation');
    assert.match(source, /Haptics\./, file + ' wires Haptics');
  }
  assert.match(read('InboxWorkspacePage.ets'), /NeedsYouPulseDot/);
  assert.match(read('TodoWorkspacePage.ets'), /WorkingArcSpinner/);
  assert.match(read('TodoWorkspacePage.ets'), /NeedsYouPulseDot/);
  assert.match(read('KnowledgeRecordingView.ets'), /NeedsYouPulseDot/);
  assert.match(read('HealthToday.ets'), /PhoneStyle\.cardElevation\(/);
  assert.match(read('MachineSubscriptionCards.ets'), /PhoneStyle\.cardElevation\(/);
  assert.match(read('PhoneTodoDetailForm.ets'), /PhoneStyle\.cardElevation\(/);
});

test('all secondary workspace cards use borderless Liquid Glass PhoneStyle.cardBorder and support Mate XT 2 3-column grids', () => {
  const read = (file) => readFileSync(new URL('../entry/src/main/ets/components/' + file, import.meta.url), 'utf8');
  for (const file of [
    'InboxWorkspacePage.ets',
    'TodoWorkspacePage.ets',
    'PhoneTodoDetailForm.ets',
    'KnowledgeWorkspacePage.ets',
    'KnowledgeRecordingView.ets',
    'HealthWorkspacePage.ets',
    'HealthToday.ets',
    'MachineDirectory.ets',
    'MachineSubscriptionCards.ets',
    'ResourcePage.ets'
  ]) {
    const source = read(file);
    assert.match(source, /PhoneStyle\.cardBorder\(/, file + ' uses PhoneStyle.cardBorder');
    assert.doesNotMatch(source, /Colors\.styleEdge\s*\|\|\s*Colors\.divider/, file + ' removes hardcoded divider fallback border');
  }
  assert.match(read('MachineDirectory.ets'), /workspaceGridColumns\([^)]+,\s*3\)/);
  assert.match(read('MachineSubscriptionCards.ets'), /workspaceGridColumns\([^)]+,\s*3\)/);
  assert.match(read('KnowledgeWorkspacePage.ets'), /workspaceGridColumns\([^)]+,\s*3\)/);
  assert.match(read('MachineDirectory.ets'), /formatMachineLoad\(/);
  assert.match(read('MachineDirectory.ets'), /tr\('machine_load_per_core'\)/);
  const healthToday = read('HealthToday.ets');
  assert.match(healthToday, /demoWaterMl:\s*number\s*=\s*1500/);
  assert.match(healthToday, /demoProteinGrams:\s*number\s*=\s*48/);
  assert.match(healthToday, /if\s*\(this\.model\.demo\)\s*\{\s*this\.totals\(\)/);
});

test('formatMachineLoad formats raw and per-core load clearly and falls back when cores are unavailable', () => {
  assert.equal(formatMachineLoad(2.4, 12, '{per}/core'), '2.4 · 0.2/core');
  assert.equal(formatMachineLoad(25.4, 18, '{per}/core'), '25.4 · 1.4/core');
  assert.equal(formatMachineLoad(2.4, 12, '{per}/核'), '2.4 · 0.2/核');
  assert.equal(formatMachineLoad(2.4, 12, '{per}/コア'), '2.4 · 0.2/コア');
  assert.equal(formatMachineLoad(2.45, 0, '{per}/core'), '2.45');
  assert.equal(formatMachineLoad(12.0, -1, '{per}/core'), '12');
  assert.equal(formatMachineLoad(-1, 8, '{per}/core'), '—');
  assert.equal(formatMachineLoad(Number.NaN, 8, '{per}/core'), '—');
});
