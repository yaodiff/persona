import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import {
  FOLD_DISPLAY_MODE_FULL,
  FOLD_DISPLAY_MODE_GLOBAL_FULL,
  FOLD_DISPLAY_MODE_MAIN,
  FOLD_STATUS_EXPANDED,
  FOLD_STATUS_EXPANDED_WITH_SECOND_EXPANDED,
  FOLD_STATUS_EXPANDED_WITH_SECOND_HALF_FOLDED,
  FOLD_STATUS_FOLDED,
  FOLD_STATUS_FOLDED_WITH_SECOND_EXPANDED,
  FOLD_STATUS_FOLDED_WITH_SECOND_HALF_FOLDED,
  FOLD_STATUS_HALF_FOLDED,
  FOLD_STATUS_HALF_FOLDED_WITH_SECOND_EXPANDED,
  FOLD_STATUS_HALF_FOLDED_WITH_SECOND_HALF_FOLDED,
  FOLD_STATUS_UNKNOWN,
  classifyFoldPosture,
  compactTreeInset,
  deduplicateAgentPlace,
  isHalfFoldedStatus,
  isTriFoldAngledStatus,
  isTriFoldExpandedStatus,
  pickRelaySessionIndex,
  resolveFoldLayout,
  resolveFoldShortcut,
  resolveHoverSplitGeometry
} from '../entry/src/main/ets/core/FoldPosture.ts';

test('straight phone (Mate 90 Pro) stays compact stack with tight tree indentation', () => {
  const layout = resolveFoldLayout({
    widthClass: 'sm',
    windowWidthVp: 366,
    windowHeightVp: 809,
    isFoldable: false,
    foldStatus: FOLD_STATUS_UNKNOWN,
    foldDisplayMode: 0,
    nativeHoverAvailable: false
  });
  assert.equal(layout.posture, 'straight');
  assert.equal(layout.navigationMode, 'stack');
  assert.equal(layout.supportsInspector, false);
  assert.equal(layout.autoShowInspector, false);
  assert.equal(layout.cardColumns, 1);

  // Tight phone tree insets give agent rows ~36vp extra title space over legacy 58vp inset.
  assert.equal(compactTreeInset(0, 366, 1), 14);
  assert.equal(compactTreeInset(1, 366, 1), 18);
  assert.equal(compactTreeInset(2, 366, 1), 22);
  assert.ok(compactTreeInset(2, 366, 1) <= 24);
});

test('Mate X7 transitions seamlessly between cover stack, 707vp dual-pane split, and tabletop hover', () => {
  const cover = resolveFoldLayout({
    widthClass: 'sm',
    windowWidthVp: 346,
    windowHeightVp: 782,
    isFoldable: true,
    foldStatus: FOLD_STATUS_FOLDED,
    foldDisplayMode: FOLD_DISPLAY_MODE_MAIN,
    nativeHoverAvailable: false
  });
  assert.equal(cover.posture, 'cover');
  assert.equal(cover.navigationMode, 'stack');
  assert.equal(cover.supportsInspector, false);

  const unfolded = resolveFoldLayout({
    widthClass: 'md',
    windowWidthVp: 707,
    windowHeightVp: 773,
    isFoldable: true,
    foldStatus: FOLD_STATUS_EXPANDED,
    foldDisplayMode: FOLD_DISPLAY_MODE_FULL,
    nativeHoverAvailable: false
  });
  assert.equal(unfolded.posture, 'dual_unfolded');
  assert.equal(unfolded.navigationMode, 'split');
  assert.equal(unfolded.isFoldableSplit, true);
  assert.ok(unfolded.masterWidth >= 256 && unfolded.masterWidth <= 300);
  assert.ok(unfolded.masterWidth + unfolded.detailMin <= 707);
  assert.equal(unfolded.supportsInspector, true);
  assert.equal(unfolded.autoShowInspector, false);
  assert.equal(unfolded.collapseRailForInspector, true);
  assert.equal(unfolded.cardColumns, 2);

  const hover = resolveFoldLayout({
    widthClass: 'md',
    windowWidthVp: 707,
    windowHeightVp: 773,
    isFoldable: true,
    foldStatus: FOLD_STATUS_HALF_FOLDED,
    foldDisplayMode: FOLD_DISPLAY_MODE_FULL,
    nativeHoverAvailable: true
  });
  assert.equal(hover.posture, 'hover_tabletop');
  assert.equal(hover.hoverMode, true);
  assert.equal(isHalfFoldedStatus(FOLD_STATUS_HALF_FOLDED), true);
});

test('Mate XT / Mate XT 2 tri-fold unlocks 3-pane live workbench and angled stand mode across all 6 dual-axis FoldStatus values', () => {
  assert.equal(FOLD_DISPLAY_MODE_GLOBAL_FULL, 5);
  assert.equal(isTriFoldExpandedStatus(FOLD_STATUS_EXPANDED_WITH_SECOND_EXPANDED), true);
  assert.equal(isTriFoldAngledStatus(FOLD_STATUS_EXPANDED_WITH_SECOND_HALF_FOLDED), true);
  assert.equal(isTriFoldAngledStatus(FOLD_STATUS_HALF_FOLDED_WITH_SECOND_EXPANDED), true);
  assert.equal(isTriFoldAngledStatus(FOLD_STATUS_EXPANDED_WITH_SECOND_EXPANDED), false);

  // 11: FOLD_STATUS_EXPANDED_WITH_SECOND_EXPANDED -> full 3-column workbench (1061vp)
  const trifold = resolveFoldLayout({
    widthClass: 'lg',
    windowWidthVp: 1061,
    windowHeightVp: 744,
    isFoldable: true,
    foldStatus: FOLD_STATUS_EXPANDED_WITH_SECOND_EXPANDED,
    foldDisplayMode: FOLD_DISPLAY_MODE_GLOBAL_FULL,
    nativeHoverAvailable: true
  });
  assert.equal(trifold.posture, 'trifold_unfolded');
  assert.equal(trifold.navigationMode, 'split');
  assert.equal(trifold.supportsInspector, true);
  assert.equal(trifold.autoShowInspector, true);
  assert.equal(trifold.collapseRailForInspector, false);
  assert.equal(trifold.masterWidth, 288);
  assert.equal(trifold.inspectorWidth, 316);
  assert.equal(trifold.cardColumns, 3);
  assert.ok(trifold.masterWidth + trifold.detailMin + trifold.inspectorWidth <= 1061);

  // 11 at >=1160vp (Mate XT 2 landscape / MatePad XL): expanded master & code workbench columns
  const wideTrifold = resolveFoldLayout({
    widthClass: 'xl',
    windowWidthVp: 1280,
    windowHeightVp: 800,
    isFoldable: true,
    foldStatus: FOLD_STATUS_EXPANDED_WITH_SECOND_EXPANDED,
    foldDisplayMode: FOLD_DISPLAY_MODE_GLOBAL_FULL,
    nativeHoverAvailable: false
  });
  assert.equal(wideTrifold.posture, 'trifold_unfolded');
  assert.equal(wideTrifold.masterWidth, 312);
  assert.equal(wideTrifold.inspectorWidth, 356);
  assert.equal(wideTrifold.cardColumns, 3);

  // FOLD_DISPLAY_MODE_GLOBAL_FULL (5) triggers trifold_unfolded even before width reaches 920vp
  assert.equal(classifyFoldPosture({
    widthClass: 'md',
    windowWidthVp: 880,
    windowHeightVp: 744,
    isFoldable: true,
    foldStatus: FOLD_STATUS_EXPANDED,
    foldDisplayMode: FOLD_DISPLAY_MODE_GLOBAL_FULL,
    nativeHoverAvailable: false
  }), 'trifold_unfolded');

  // 12: FOLD_STATUS_FOLDED_WITH_SECOND_EXPANDED -> 2-screen intermediate split
  assert.equal(classifyFoldPosture({
    widthClass: 'md',
    windowWidthVp: 682,
    windowHeightVp: 744,
    isFoldable: true,
    foldStatus: FOLD_STATUS_FOLDED_WITH_SECOND_EXPANDED,
    foldDisplayMode: FOLD_DISPLAY_MODE_FULL,
    nativeHoverAvailable: false
  }), 'dual_unfolded');

  // 21 & 13: angled 3rd pane (Stand mode) when width >= 820vp
  const angled21 = resolveFoldLayout({
    widthClass: 'lg',
    windowWidthVp: 880,
    windowHeightVp: 744,
    isFoldable: true,
    foldStatus: FOLD_STATUS_EXPANDED_WITH_SECOND_HALF_FOLDED,
    foldDisplayMode: FOLD_DISPLAY_MODE_GLOBAL_FULL,
    nativeHoverAvailable: true
  });
  assert.equal(angled21.posture, 'trifold_angled');
  assert.equal(angled21.navigationMode, 'split');
  assert.equal(angled21.autoShowInspector, true);
  assert.equal(angled21.collapseRailForInspector, true);
  assert.equal(angled21.inspectorWidth, 304);
  assert.equal(angled21.hoverMode, false);
  assert.equal(angled21.cardColumns, 2);

  const angled13 = resolveFoldLayout({
    widthClass: 'lg',
    windowWidthVp: 980,
    windowHeightVp: 744,
    isFoldable: true,
    foldStatus: FOLD_STATUS_HALF_FOLDED_WITH_SECOND_EXPANDED,
    foldDisplayMode: FOLD_DISPLAY_MODE_FULL,
    nativeHoverAvailable: true
  });
  assert.equal(angled13.posture, 'trifold_angled');
  assert.equal(angled13.collapseRailForInspector, false);

  // 22 & 23: half-folded secondary with nativeHoverAvailable -> hover_tabletop
  assert.equal(classifyFoldPosture({
    widthClass: 'md',
    windowWidthVp: 707,
    windowHeightVp: 744,
    isFoldable: true,
    foldStatus: FOLD_STATUS_FOLDED_WITH_SECOND_HALF_FOLDED,
    foldDisplayMode: FOLD_DISPLAY_MODE_FULL,
    nativeHoverAvailable: true
  }), 'hover_tabletop');

  assert.equal(classifyFoldPosture({
    widthClass: 'md',
    windowWidthVp: 707,
    windowHeightVp: 744,
    isFoldable: true,
    foldStatus: FOLD_STATUS_HALF_FOLDED_WITH_SECOND_HALF_FOLDED,
    foldDisplayMode: FOLD_DISPLAY_MODE_FULL,
    nativeHoverAvailable: true
  }), 'hover_tabletop');
});

test('hardware keyboard shortcuts resolve Ctrl/Meta+K/N/I/1/2/3/[/] and Escape across fold postures', () => {
  assert.equal(resolveFoldShortcut('k', true, true), 'search');
  assert.equal(resolveFoldShortcut('K', true, false), 'search');
  assert.equal(resolveFoldShortcut('n', true, true), 'new_chat');
  assert.equal(resolveFoldShortcut('N', true, false), 'new_chat');
  assert.equal(resolveFoldShortcut('i', true, true), 'toggle_inspector');
  assert.equal(resolveFoldShortcut('I', true, false), 'none');
  assert.equal(resolveFoldShortcut('1', true, true), 'workbench_pulse');
  assert.equal(resolveFoldShortcut('2', true, true), 'workbench_code');
  assert.equal(resolveFoldShortcut('3', true, true), 'workbench_machines');
  assert.equal(resolveFoldShortcut('1', true, false), 'none');
  assert.equal(resolveFoldShortcut('2', true, false), 'none');
  assert.equal(resolveFoldShortcut('3', true, false), 'none');
  assert.equal(resolveFoldShortcut(']', true, false), 'next_unread');
  assert.equal(resolveFoldShortcut('[', true, false), 'prev_unread');
  assert.equal(resolveFoldShortcut('escape', false, false), 'dismiss');
  assert.equal(resolveFoldShortcut('Esc', true, true), 'dismiss');
  // Bare letters/brackets/digits without Ctrl/Meta never steal text input.
  assert.equal(resolveFoldShortcut('k', false, true), 'none');
  assert.equal(resolveFoldShortcut('n', false, true), 'none');
  assert.equal(resolveFoldShortcut('i', false, true), 'none');
  assert.equal(resolveFoldShortcut('1', false, true), 'none');
  assert.equal(resolveFoldShortcut('2', false, true), 'none');
  assert.equal(resolveFoldShortcut('3', false, true), 'none');
  assert.equal(resolveFoldShortcut(']', false, true), 'none');
  assert.equal(resolveFoldShortcut('[', false, true), 'none');
  assert.equal(resolveFoldShortcut('', true, true), 'none');
  assert.equal(resolveFoldShortcut('x', true, true), 'none');
});

test('pickRelaySessionIndex cycles needs_you and unread first, falls back to working, or -1 if clear', () => {
  const mixed = ['idle', 'needs_you', 'working', 'unread', 'idle'];
  assert.equal(pickRelaySessionIndex(mixed, -1, 1), 1);
  assert.equal(pickRelaySessionIndex(mixed, 1, 1), 3);
  assert.equal(pickRelaySessionIndex(mixed, 3, 1), 1);
  assert.equal(pickRelaySessionIndex(mixed, 2, 1), 3);
  assert.equal(pickRelaySessionIndex(mixed, -1, -1), 3);
  assert.equal(pickRelaySessionIndex(mixed, 3, -1), 1);
  assert.equal(pickRelaySessionIndex(mixed, 1, -1), 3);

  const workingOnly = ['idle', 'working', 'idle', 'working'];
  assert.equal(pickRelaySessionIndex(workingOnly, -1, 1), 1);
  assert.equal(pickRelaySessionIndex(workingOnly, 1, 1), 3);
  assert.equal(pickRelaySessionIndex(workingOnly, 3, 1), 1);
  assert.equal(pickRelaySessionIndex(workingOnly, 1, -1), 3);

  assert.equal(pickRelaySessionIndex(['idle', 'idle'], 0, 1), -1);
  assert.equal(pickRelaySessionIndex([], -1, 1), -1);
});

test('deduplicateAgentPlace suppresses redundant title echoes while keeping distinct place metadata', () => {
  assert.equal(deduplicateAgentPlace('Review release checks', 'Review release checks'), '');
  assert.equal(deduplicateAgentPlace('Review release checks', 'review release checks'), '');
  assert.equal(deduplicateAgentPlace('Review release checks', '  Review release checks  '), '');
  assert.equal(deduplicateAgentPlace('Review release checks', 'Review release checks · Mac Studio'), 'Mac Studio');
  assert.equal(deduplicateAgentPlace('Review release checks', 'Review release checks › Claude 1'), 'Claude 1');
  assert.equal(deduplicateAgentPlace('Fix login flow', 'main'), 'main');
  assert.equal(deduplicateAgentPlace('Fix login flow', 'Claude 1'), 'Claude 1');
  assert.equal(deduplicateAgentPlace('', 'main'), 'main');
  assert.equal(deduplicateAgentPlace('Fix login flow', ''), '');
});

test('64vp icon rail, 3-dot header, 3-tab Live Workbench, relay wiring, and borderless grouped cards maintain structural invariants', () => {
  const index = readFileSync(new URL('../entry/src/main/ets/pages/Index.ets', import.meta.url), 'utf8');
  const agents = readFileSync(new URL('../entry/src/main/ets/components/AgentListPage.ets', import.meta.url), 'utf8');
  const rowContent = readFileSync(new URL('../entry/src/main/ets/components/AgentDirectoryRowContent.ets', import.meta.url), 'utf8');
  const projects = readFileSync(new URL('../entry/src/main/ets/components/ProjectsPage.ets', import.meta.url), 'utf8');
  const inspector = readFileSync(new URL('../entry/src/main/ets/components/FoldInspectorPanel.ets', import.meta.url), 'utf8');

  // Rebuilt 64vp rail in Index.ets: no truncated toolbarConfiguration expand text; clean railShell + expand circle.
  assert.match(index, /\.toolbarConfiguration\(\[\]\)/);
  assert.doesNotMatch(index, /ohos_ic_public_arrow_right/);
  assert.match(index, /@Builder railShell\(\)[\s\S]*id\('shell\.rail\.persona'\)[\s\S]*id\('shell\.rail\.agents'\)[\s\S]*id\('shell\.rail\.expand'\)/);
  // Clean 3-dot menu button replaces ohos_ic_public_more in destinationMenus.
  const destMenus = index.slice(index.indexOf('  @Builder destinationMenus('), index.indexOf('  private openSettingsRoot('));
  assert.match(destMenus, /Row\(\{ space: 3 \}\)[\s\S]*Circle\(\)\.width\(3\.5\)\.height\(3\.5\)/);
  // Hardware keyboard shortcuts wired into root Stack, including Ctrl/Cmd+1/2/3 and [/] + trifold_angled label.
  assert.match(index, /\.onKeyEvent\(\(event: KeyEvent\) => this\.handleFoldKeyEvent\(event\)\)/);
  assert.match(index, /resolveFoldShortcut\(/);
  assert.match(index, /KEYCODE_1[\s\S]*KEYCODE_2[\s\S]*KEYCODE_3[\s\S]*KEYCODE_LEFT_BRACKET[\s\S]*KEYCODE_RIGHT_BRACKET/);
  assert.match(index, /fold_posture_trifold_angled/);
  assert.match(index, /pickRelaySessionIndex\(/);
  // Inspector onOpenToolsTab and onStepRelay wired live.
  assert.match(index, /onOpenToolsTab:\s*\(tab: number\) =>/);
  assert.match(index, /onStepRelay:\s*\(direction: 1 \| -1\) =>/);

  // Rebuilt AgentListPage rail mode: 40x40 avatars with status marks & bindTips, zero repo outline in rail mode.
  assert.match(agents, /@Builder railAvatar\(binding: BoundAgentRow\)[\s\S]*\.width\(40\)[\s\S]*\.height\(40\)[\s\S]*\.bindTips\(/);
  const resultList = agents.slice(agents.indexOf('  @Builder resultList()'), agents.indexOf('  build()'));
  assert.match(resultList, /if \(this\.rail\) \{[\s\S]*this\.railAvatar\(rowBinding\)[\s\S]*id\('agents\.rail\.list'\)[\s\S]*\} else \{[\s\S]*AgentRepositoryRows\(/);
  // Wide split agentRow uses live motion marks & PhoneStyle.cardBorder.
  const wideRow = agents.slice(agents.indexOf('  @Builder agentRow('), agents.indexOf('  @Builder rowContext('));
  assert.match(wideRow, /NeedsYouPulseDot\(/);
  assert.match(wideRow, /WorkingArcSpinner\(/);
  assert.match(agents, /PhoneStyle\.cardBorder\(/);

  // Duplicate place suppression in AgentDirectoryRowContent & ProjectsPage + worktree dividers & borderless card styling.
  assert.match(rowContent, /private displayPlace\(\): string \{\s*return deduplicateAgentPlace\(this\.displayTitle\(\), this\.place\);/);
  assert.match(projects, /deduplicateAgentPlace\(this\.displayAgentTitle\(agent\), paneName \|\| agent\.tab\)/);
  assert.match(projects, /outlineItemNeedsDivider\(entry\)[\s\S]*Divider\(\)\.strokeWidth\(0\.5\)/);
  assert.match(projects, /PhoneStyle\.cardElevation\(nativeEnvironment\.dark\)/);
  assert.match(projects, /PhoneStyle\.cardBorder\(nativeEnvironment\.dark\)/);

  // Mate XT 2 Tri-Fold 3-Tab Live Workbench in FoldInspectorPanel.
  assert.match(inspector, /id\('fold\.inspector\.posture'\)/);
  assert.match(inspector, /fold_workbench_pulse[\s\S]*fold_workbench_code[\s\S]*fold_workbench_machines/);
  assert.match(inspector, /fold_relay_queue[\s\S]*fold_relay_clear[\s\S]*fold_relay_prev[\s\S]*fold_relay_next/);
  assert.match(inspector, /SessionTools\(\{/);
  assert.match(inspector, /fold_workbench_expand_tools/);
  assert.match(inspector, /fold_workbench_pick_session/);
  assert.match(inspector, /\/api\/v1\/metrics/);
  assert.match(inspector, /\/api\/v1\/usage\/windows/);
  assert.match(inspector, /machine_load_per_core/);
  assert.match(inspector, /this\.toolChip\('files', 0, 'fold\.inspector\.tools\.files'/);
  assert.match(inspector, /this\.toolChip\('git', 1, 'fold\.inspector\.tools\.git'/);
  assert.match(inspector, /this\.toolChip\('terminal', 2, 'fold\.inspector\.tools\.terminal'\)/);
  assert.match(inspector, /onOpenToolsTab\(tab\)/);
  assert.match(inspector, /id\('fold\.inspector\.count\.needs'\)|'fold\.inspector\.count\.needs'/);
});

test('Mate XT / Mate XT 2 portrait 3-screen, floating/split windows, outward-fold MAIN, angled 23, and 820-960vp fit safely', () => {
  // 3-screen portrait (744x1061 vp) with hardware GLOBAL_FULL still uses 2-pane dual_unfolded so center chat is never crushed.
  const portraitTriFold = resolveFoldLayout({
    widthClass: 'md',
    windowWidthVp: 744,
    windowHeightVp: 1061,
    isFoldable: true,
    foldStatus: FOLD_STATUS_EXPANDED_WITH_SECOND_EXPANDED,
    foldDisplayMode: FOLD_DISPLAY_MODE_GLOBAL_FULL,
    nativeHoverAvailable: true
  });
  assert.equal(portraitTriFold.posture, 'dual_unfolded');
  assert.equal(portraitTriFold.navigationMode, 'split');
  assert.equal(portraitTriFold.autoShowInspector, false);
  assert.equal(portraitTriFold.collapseRailForInspector, true);
  assert.equal(portraitTriFold.cardColumns, 2);

  // Floating window / narrow split-screen (< 560vp) on tri-fold stays compact cover stack.
  for (const widthVp of [420, 520]) {
    const narrow = resolveFoldLayout({
      widthClass: 'sm',
      windowWidthVp: widthVp,
      windowHeightVp: 800,
      isFoldable: true,
      foldStatus: FOLD_STATUS_EXPANDED_WITH_SECOND_EXPANDED,
      foldDisplayMode: FOLD_DISPLAY_MODE_GLOBAL_FULL,
      nativeHoverAvailable: true
    });
    assert.equal(narrow.posture, 'cover', `width ${widthVp}vp`);
    assert.equal(narrow.navigationMode, 'stack');
    assert.equal(narrow.supportsInspector, false);
    assert.equal(narrow.cardColumns, 1);
  }

  // 880vp GLOBAL_FULL tri-fold fits 3 panes cleanly by collapsing the 64vp rail when inspector is open.
  const midTriFold = resolveFoldLayout({
    widthClass: 'lg',
    windowWidthVp: 880,
    windowHeightVp: 744,
    isFoldable: true,
    foldStatus: FOLD_STATUS_EXPANDED_WITH_SECOND_EXPANDED,
    foldDisplayMode: FOLD_DISPLAY_MODE_GLOBAL_FULL,
    nativeHoverAvailable: false
  });
  assert.equal(midTriFold.posture, 'trifold_unfolded');
  assert.equal(midTriFold.collapseRailForInspector, true);
  assert.equal(midTriFold.masterWidth, 256);
  assert.equal(midTriFold.inspectorWidth, 288);
  assert.ok(64 + midTriFold.detailMin + midTriFold.inspectorWidth <= 880);
  assert.equal(midTriFold.cardColumns, 2);

  // Z-stand half-folded (23) resolves to trifold_angled at wide widths (>= 820vp), keeping 707vp as hover_tabletop.
  assert.equal(isTriFoldAngledStatus(FOLD_STATUS_HALF_FOLDED_WITH_SECOND_HALF_FOLDED), true);
  assert.equal(classifyFoldPosture({
    widthClass: 'lg',
    windowWidthVp: 880,
    windowHeightVp: 744,
    isFoldable: true,
    foldStatus: FOLD_STATUS_HALF_FOLDED_WITH_SECOND_HALF_FOLDED,
    foldDisplayMode: FOLD_DISPLAY_MODE_GLOBAL_FULL,
    nativeHoverAvailable: true
  }), 'trifold_angled');

  // Outward-fold / Mate XT 2-screen reporting FOLD_DISPLAY_MODE_MAIN at 682vp uses dual_unfolded, while 346vp stays cover.
  assert.equal(classifyFoldPosture({
    widthClass: 'md',
    windowWidthVp: 682,
    windowHeightVp: 744,
    isFoldable: true,
    foldStatus: FOLD_STATUS_FOLDED_WITH_SECOND_EXPANDED,
    foldDisplayMode: FOLD_DISPLAY_MODE_MAIN,
    nativeHoverAvailable: false
  }), 'dual_unfolded');
  assert.equal(classifyFoldPosture({
    widthClass: 'sm',
    windowWidthVp: 346,
    windowHeightVp: 782,
    isFoldable: true,
    foldStatus: FOLD_STATUS_FOLDED,
    foldDisplayMode: FOLD_DISPLAY_MODE_MAIN,
    nativeHoverAvailable: false
  }), 'cover');
});

test('wide XiaoYi settings, SettingsDirectory tint, module.json5 window modes, and Index posture transitions stay aligned', () => {
  const index = readFileSync(new URL('../entry/src/main/ets/pages/Index.ets', import.meta.url), 'utf8');
  const settingsDir = readFileSync(new URL('../entry/src/main/ets/components/SettingsDirectory.ets', import.meta.url), 'utf8');
  const xiaoyiPage = readFileSync(new URL('../entry/src/main/ets/components/XiaoYiSettingsPage.ets', import.meta.url), 'utf8');
  const moduleJson = JSON.parse(readFileSync(new URL('../entry/src/main/module.json5', import.meta.url), 'utf8'));

  assert.match(index, /@Monitor\('environment\.foldStatus',\s*'environment\.foldDisplayMode',\s*'environment\.windowWidthPx',\s*'environment\.windowHeightPx'\)/);
  assert.match(index, /wasTriFold\s*!==\s*isNowTriFold/);
  assert.match(index, /constraintSize\(\{\s*maxWidth:\s*440\s*\}\)/);
  assert.match(index, /if \(!this\.wide\(\) \|\| !this\.model\.profile\.id\)[\s\S]*phone_xiaoyi/);
  assert.match(index, /bindSheet\(!this\.model\.profile\.id && this\.phoneSettingsShown/);

  const tintBlock = settingsDir.slice(settingsDir.indexOf('function settingTint('), settingsDir.indexOf('function settingArtwork('));
  assert.match(tintBlock, /case 'phone_xiaoyi':[\s\S]*return Colors\.ai;/);

  assert.match(xiaoyiPage, /PhoneStyle\.cardBorder\(this\.environment\.dark\)/);
  assert.match(xiaoyiPage, /PhoneStyle\.cardElevation\(this\.environment\.dark\)/);
  assert.match(xiaoyiPage, /constraintSize\(\{\s*maxWidth:\s*740\s*\}\)/);
  assert.match(xiaoyiPage, /workspaceHorizontalPadding\(/);
  assert.match(xiaoyiPage, /Haptics\./);

  const entryAbility = moduleJson.module.abilities.find((a) => a.name === 'EntryAbility');
  assert.deepEqual(entryAbility.supportWindowMode, ['fullscreen', 'split', 'floating']);
});

test('resolveHoverSplitGeometry computes crease-safe top/bottom panes and rejects insufficient bounds', () => {
  const valid = resolveHoverSplitGeometry(773, 372, 28);
  assert.equal(valid.active, true);
  assert.equal(valid.topPaneHeightVp, 372);
  assert.equal(valid.creaseBandVp, 28);
  assert.equal(valid.bottomPaneHeightVp, 373);

  // Thin crease enforces minimum 12vp band
  const thin = resolveHoverSplitGeometry(760, 360, 4);
  assert.equal(thin.active, true);
  assert.equal(thin.creaseBandVp, 12);
  assert.equal(thin.bottomPaneHeightVp, 388);

  // Insufficient top (< 180) or bottom (< 140) stays inactive
  assert.equal(resolveHoverSplitGeometry(773, 150, 24).active, false);
  assert.equal(resolveHoverSplitGeometry(500, 360, 24).active, false);
  assert.equal(resolveHoverSplitGeometry(773, 360, 0).active, false);
  assert.equal(resolveHoverSplitGeometry(0, 360, 24).active, false);
});

test('Ctrl/Meta+0 opens XiaoYi, and Hover Tabletop + Workbench quick controls stay wired cleanly', () => {
  assert.equal(resolveFoldShortcut('0', true, false), 'open_xiaoyi');
  assert.equal(resolveFoldShortcut('0', true, true), 'open_xiaoyi');
  assert.equal(resolveFoldShortcut('0', false, true), 'none');

  const index = readFileSync(new URL('../entry/src/main/ets/pages/Index.ets', import.meta.url), 'utf8');
  const inspector = readFileSync(new URL('../entry/src/main/ets/components/FoldInspectorPanel.ets', import.meta.url), 'utf8');

  assert.match(index, /KEYCODE_0/);
  assert.match(index, /open_xiaoyi/);
  assert.match(index, /resolveHoverSplitGeometry\(/);
  assert.match(index, /id\('fold\.hover\.crease'\)/);
  assert.match(index, /id\('fold\.hover\.deck'\)/);
  assert.match(index, /id\('fold\.hover\.tabletop'\)/);
  assert.match(index, /onInterruptSession:/);
  assert.match(index, /onOpenXiaoYi:/);

  assert.match(inspector, /id\('fold\.inspector\.urgent'\)/);
  assert.match(inspector, /id\('fold\.inspector\.tools\.xiaoyi'\)/);
  assert.match(inspector, /fold\.inspector\.interrupt\./);
});
