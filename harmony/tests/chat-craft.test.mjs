import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { registerHooks } from 'node:module';

registerHooks({
  resolve(specifier, context, next) {
    if (context.parentURL?.endsWith('/StreamMarkdown.ts') && specifier === './Markdown') {
      return { url: new URL('./Markdown.ts', context.parentURL).href, shortCircuit: true };
    }
    return next(specifier, context);
  }
});

const {
  countCodeLines,
  formatCodeLanguageBadge,
  formatCodeLineCount,
  trimCodeForCopy
} = await import('../entry/src/main/ets/core/StreamMarkdown.ts');
const {
  gitMarkBadgeLabel,
  summarizeUnifiedDiff
} = await import('../entry/src/main/ets/core/WorkPresentation.ts');

const read = (rel) => readFileSync(new URL('../entry/src/main/ets/' + rel, import.meta.url), 'utf8');

test('code block formatting helpers normalize language badges, trim trailing newlines, and count lines accurately', () => {
  assert.equal(formatCodeLanguageBadge('ts'), 'TYPESCRIPT');
  assert.equal(formatCodeLanguageBadge('TypeScript'), 'TYPESCRIPT');
  assert.equal(formatCodeLanguageBadge('sh'), 'BASH');
  assert.equal(formatCodeLanguageBadge('zsh'), 'BASH');
  assert.equal(formatCodeLanguageBadge('swift'), 'SWIFT');
  assert.equal(formatCodeLanguageBadge('ets'), 'ARKTS');
  assert.equal(formatCodeLanguageBadge('py'), 'PYTHON');
  assert.equal(formatCodeLanguageBadge('graphql'), 'GRAPHQL');
  assert.equal(formatCodeLanguageBadge('   '), 'CODE');

  assert.equal(trimCodeForCopy('const x = 1;\n\n'), 'const x = 1;');
  assert.equal(trimCodeForCopy('a\r\nb\r\n'), 'a\r\nb');
  assert.equal(trimCodeForCopy(''), '');

  assert.equal(countCodeLines(''), 0);
  assert.equal(countCodeLines('single line\n'), 1);
  assert.equal(countCodeLines('one\ntwo\nthree\n'), 3);
  assert.equal(formatCodeLineCount(0), '');
  assert.equal(formatCodeLineCount(1), '1 line');
  assert.equal(formatCodeLineCount(12), '12 lines');
});

test('git diff summary and status badge helpers accurately count hunks/lines and normalize status letters', () => {
  const patch = [
    'diff --git a/src/app.ets b/src/app.ets',
    '--- a/src/app.ets',
    '+++ b/src/app.ets',
    '@@ -10,4 +10,5 @@ struct App {',
    ' context',
    '-oldLine1',
    '-oldLine2',
    '+newLine1',
    '+newLine2',
    '+newLine3',
    '@@ -40,2 +41,2 @@',
    '-removed',
    '+added'
  ].join('\n');
  assert.deepEqual(summarizeUnifiedDiff(patch), { hunks: 2, additions: 4, deletions: 3 });
  assert.deepEqual(summarizeUnifiedDiff(''), { hunks: 0, additions: 0, deletions: 0 });

  assert.equal(gitMarkBadgeLabel('??'), '?');
  assert.equal(gitMarkBadgeLabel('U'), '?');
  assert.equal(gitMarkBadgeLabel('A'), 'A');
  assert.equal(gitMarkBadgeLabel('D'), 'D');
  assert.equal(gitMarkBadgeLabel('m'), 'M');
  assert.equal(gitMarkBadgeLabel(''), 'M');
});

test('chat components remove raw Image(Symbols.more) and apply flagship HarmonyOS NEXT craft across Markdown, Welcome, Tray, Decisions, and SessionTools', () => {
  const markdown = read('components/MarkdownText.ets');
  const conversation = read('components/Conversation.ets');
  const cards = read('components/AssistantCards.ets');
  const welcome = read('components/AssistantWelcome.ets');
  const decisions = read('components/InteractionDecisionForm.ets');
  const tools = read('components/SessionTools.ets');

  // Raw 4-dot Image(Symbols.more) is completely removed from owned chat components.
  for (const [name, src] of [
    ['Conversation.ets', conversation],
    ['AssistantCards.ets', cards],
    ['InteractionDecisionForm.ets', decisions]
  ]) {
    assert.doesNotMatch(src, /Image\(Symbols\.more\)/, `${name} must not render raw Image(Symbols.more)`);
  }

  // MarkdownText clean code cards, accent-bar blockquotes, alternating tables, and incremental streaming without wireframe borders.
  assert.ok(markdown.includes('formatCodeLanguageBadge'));
  assert.ok(markdown.includes('formatCodeLineCount'));
  assert.ok(markdown.includes('Colors.codeHunk'));
  assert.ok(markdown.includes('Colors.codeCanvas'));
  assert.match(markdown, /left:\s*3\.5/);
  assert.ok(markdown.includes('Colors.recapTint'));
  assert.ok(markdown.includes('rowIndex % 2 === 0'));
  assert.ok(markdown.includes('IncrementalMarkdownCache'));
  assert.ok(markdown.includes('StreamTailCaret'));
  assert.ok(markdown.includes('PhoneStyle.cardBorder'));
  assert.doesNotMatch(markdown, /#2EFFFFFF/);

  // Conversation MessageSurface aligns with iOS ChatBox.persona (#F0F0F3 / #2A2A2E) and borderless/shadowless Glass bubbles.
  const surface = conversation.slice(conversation.indexOf('struct MessageSurface'), conversation.indexOf('struct ChatSendStatus'));
  assert.doesNotMatch(surface, /Colors\.control/);
  assert.ok(surface.includes('#2A2A2E'));
  assert.ok(surface.includes('#F0F0F3'));
  assert.ok(surface.includes('PhoneStyle.cardBorder'));
  assert.ok(surface.includes('PhoneStyle.cardElevation'));

  // Floating composer and glass controls use PhoneStyle floating elevation/border and COMPONENT_THICK blur; demo leading control uses +.
  const composerBlock = conversation.slice(conversation.indexOf('private composerSurface('), conversation.indexOf('@Builder taskDock('));
  assert.ok(composerBlock.includes('PhoneStyle.floatingElevation'));
  assert.ok(composerBlock.includes('PhoneStyle.floatingBorder'));
  assert.ok(composerBlock.includes('BlurStyle.COMPONENT_THICK'));
  assert.match(composerBlock, /this\.showContextMenu && this\.model\.demo[\s\S]{0,200}Symbols\.plus/);

  // AssistantCards tray chips are borderless/shadowless in Glass with colored status dots, press scale, and haptics; trayList floats with Liquid Glass.
  const dock = cards.slice(cards.indexOf('@Builder decisionEntry()'), cards.indexOf('private receiptTodo('));
  assert.ok(dock.includes('trayDotColor'));
  assert.ok(dock.includes('PhoneStyle.cardBorder'));
  assert.ok(dock.includes('PhoneStyle.cardElevation'));
  assert.ok(dock.includes('MotionTokens.pressScale'));
  assert.ok(dock.includes('Haptics.tap()'));
  const trayList = cards.slice(cards.indexOf('@Builder trayList()'), cards.indexOf('private metric('));
  assert.ok(trayList.includes('BlurStyle.COMPONENT_THICK'));
  assert.ok(trayList.includes('PhoneStyle.floatingElevation'));
  assert.ok(trayList.includes('PhoneStyle.floatingBorder'));

  // AssistantWelcome has mascot emblem, semantic icon wells, press scale, and borderless/shadowless Glass suggestion cards.
  assert.ok(welcome.includes('welcome_app_icon'));
  assert.ok(welcome.includes('questionWell'));
  assert.ok(welcome.includes('MotionTokens.pressScale'));
  assert.ok(welcome.includes('PhoneStyle.cardBorder'));
  assert.ok(welcome.includes('PhoneStyle.cardElevation'));

  // InteractionDecisionForm uses clean inline header tint (no pill box), Liquid Glass panel radius 22, floating elevation/border, and clean secondary capsules.
  assert.doesNotMatch(decisions, /headerPillFill|headerPillBorder/);
  assert.ok(decisions.includes('PhoneStyle.floatingElevation'));
  assert.ok(decisions.includes('PhoneStyle.floatingBorder'));
  assert.ok(decisions.includes('BlurStyle.COMPONENT_THICK'));
  assert.ok(decisions.includes('borderRadius(22)'));
  assert.doesNotMatch(decisions, /#48484A/);
  assert.match(decisions, /this\.request\.kind === 'plan' && this\.taskPlanSource\(\)[\s\S]{0,200}MarkdownText/);

  // SessionTools elevates files, git status badges, diff summary cards, clean terminal keycaps, and narrow-pane / compactHeader adaptation.
  assert.ok(tools.includes('summarizeUnifiedDiff'));
  assert.ok(tools.includes('gitMarkBadgeLabel'));
  assert.ok(tools.includes('Colors.codeAddedFill'));
  assert.ok(tools.includes('Colors.codeRemovedFill'));
  assert.ok(tools.includes('terminalKeyButton'));
  assert.ok(tools.includes('compactHeader'));
  assert.ok(tools.includes('paneWidth < 400'));
  assert.doesNotMatch(tools, /#48484A/);
});
