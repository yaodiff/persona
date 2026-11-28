import test from 'node:test';
import assert from 'node:assert/strict';
import { readdirSync, readFileSync } from 'node:fs';
import { stripTypeScriptTypes } from 'node:module';

const etsRoot = new URL('../entry/src/main/ets/', import.meta.url);
const resRoot = new URL('../entry/src/main/resources/', import.meta.url);

const readEts = (rel) => readFileSync(new URL(rel, etsRoot), 'utf8');
const listEtsFiles = (dir = etsRoot, prefix = '') => {
  const out = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    if (entry.isDirectory()) {
      out.push(...listEtsFiles(new URL(entry.name + '/', dir), `${prefix}${entry.name}/`));
    } else if (entry.name.endsWith('.ets') || entry.name.endsWith('.ts')) {
      out.push(`${prefix}${entry.name}`);
    }
  }
  return out;
};
const readColorMap = (mode, onlyFile = '') => {
  const dir = new URL(`${mode}/element/`, resRoot);
  const files = onlyFile ? [onlyFile] : readdirSync(dir).filter((name) => name.endsWith('.json'));
  const map = new Map();
  for (const file of files) {
    const parsed = JSON.parse(readFileSync(new URL(file, dir), 'utf8'));
    if (!Array.isArray(parsed.color)) continue;
    for (const entry of parsed.color) {
      assert.equal(map.has(entry.name), false, `${mode}/element/${file} has duplicate key: ${entry.name}`);
      map.set(entry.name, entry.value);
    }
  }
  return map;
};

test('Colors.ets semantic app.color.* tokens exist in both base and dark color.json with expected pairs', () => {
  const allLight = readColorMap('base');
  const allDark = readColorMap('dark');
  const light = readColorMap('base', 'color.json');
  const dark = readColorMap('dark', 'color.json');
  const colorsSrc = readEts('design/Colors.ets');

  const refs = [...colorsSrc.matchAll(/\$r\('app\.color\.([a-z0-9_]+)'\)/g)].map((m) => m[1]);
  assert.ok(refs.length >= 30, 'expected semantic app.color.* references in Colors.ets');
  for (const name of refs) {
    assert.equal(allLight.has(name), true, `base/element/*.json missing ${name}`);
    assert.equal(allDark.has(name), true, `dark/element/*.json missing ${name}`);
  }

  for (const file of listEtsFiles()) {
    const src = readEts(file);
    for (const match of src.matchAll(/\$r\('app\.color\.([a-z0-9_]+)'\)/g)) {
      const name = match[1];
      assert.equal(allLight.has(name), true, `${file}: base/element/*.json missing ${name}`);
      assert.equal(allDark.has(name), true, `${file}: dark/element/*.json missing ${name}`);
      assert.match(allLight.get(name), /^#[0-9A-Fa-f]{6,8}$/, `base invalid hex for ${name}`);
      assert.match(allDark.get(name), /^#[0-9A-Fa-f]{6,8}$/, `dark invalid hex for ${name}`);
    }
  }

  const expectedNormalized = [
    ['needsWash', 'persona_needs_wash', '#1A0064C8', '#29409CFF'],
    ['workingWash', 'persona_working_wash', '#1A9B5500', '#28FFB224'],
    ['unreadWash', 'persona_unread_wash', '#161D7A4C', '#244CC38A'],
    ['dangerWash', 'persona_danger_wash', '#16BA1A1A', '#28FF453A'],
    ['aiWash', 'persona_ai_wash', '#165B4FD6', '#26B0A8FF'],
    ['tealWash', 'persona_teal_wash', '#16087F8C', '#2663D3DF'],
    ['pressOverlay', 'persona_press_overlay', '#0E000000', '#1FFFFFFF'],
    ['elevatedGlass', 'persona_elevated_glass', '#E6FFFFFF', '#CC1C1C1E'],
    ['hairlineGlass', 'persona_hairline_glass', '#14000000', '#28FFFFFF'],
    ['toastSurface', 'persona_toast_surface', '#F5FFFFFF', '#EB1C1C1E'],
    ['codeSurface', 'persona_code_surface', '#F4F5F7', '#1C1C1E'],
    ['subtleScrim', 'persona_subtle_scrim', '#33000000', '#73000000']
  ];

  for (const [prop, resource, lightHex, darkHex] of expectedNormalized) {
    assert.equal(light.get(resource), lightHex, `light ${resource}`);
    assert.equal(dark.get(resource), darkHex, `dark ${resource}`);
    assert.match(colorsSrc, new RegExp(`readonly\\s+${prop}\\s*:\\s*ResourceColor\\s*=\\s*\\$r\\('app\\.color\\.${resource}'\\)`));
  }
});

test('normalized surfaces and PhoneStyle contain zero hardcoded hex color literals', () => {
  const guardedFiles = [
    'components/QuickCommandSheet.ets',
    'components/FoldInspectorPanel.ets',
    'components/ComposerTopStrip.ets',
    'components/FloatingToastOverlay.ets',
    'components/WorkStepsCard.ets',
    'components/AssistantWelcome.ets',
    'components/LiveViewSettingsCard.ets',
    'components/XiaoYiSettingsPage.ets',
    'components/MarkdownText.ets',
    'design/PhoneStyle.ets',
    'design/NormalizedPrimitives.ets',
    'components/SessionTools.ets',
    'components/TodoWorkspacePage.ets',
    'components/StarredMessagesSheet.ets',
    'components/ChatExportSheet.ets',
    'components/ArchivedConversationsSheet.ets',
    'components/StandingAuthSheet.ets',
    'components/AlwaysGrantsSection.ets',
    'components/AssistantSearchSheet.ets',
    'components/AutoConversationsSheet.ets',
    'components/AssistantCards.ets',
    'components/KnowledgeWorkspacePage.ets',
    'components/InboxWorkspacePage.ets',
    'components/HealthWorkspacePage.ets',
    'components/HealthToday.ets',
    'components/HealthConversationHistory.ets',
    'components/TokenUsageSheet.ets',
    'components/PersonalAgentsSheet.ets',
    'components/MachineSubscriptionCards.ets',
    'components/AgentListPage.ets',
    'components/ProjectsPage.ets',
    'components/SettingsDirectory.ets',
    'components/SettingsPage.ets',
    'components/TodoStageBar.ets',
    'components/TodoDecisionsSection.ets',
    'components/RuleSuggestionBar.ets'
  ];

  for (const file of guardedFiles) {
    const src = readEts(file);
    const matches = src.match(/#[0-9A-Fa-f]{6,8}\b/g) || [];
    assert.deepEqual(matches, [], `${file} still has hardcoded hex literals: ${matches.join(', ')}`);
  }
});

test('Tokens typography, 5-tier radii, 4vp spacing, and control sizes align with iOS Theme', () => {
  const tokensSrc = readEts('design/Tokens.ets');
  const Tokens = new Function(
    'return ' + stripTypeScriptTypes(tokensSrc.slice(tokensSrc.indexOf('export class Tokens')).replace('export class ', 'class '))
  )();

  const expected = {
    typeDisplay: 28,
    typeTitle: 22,
    typeHeading: 17,
    typeBody: 17,
    typeSecondary: 15,
    typeCaption: 13,
    typeSectionHeader: 13,
    typeLabel: 12,
    typeCode: 13,
    lineHeightDisplay: 34,
    lineHeightTitle: 28,
    lineHeightHeading: 22,
    lineHeightBody: 24,
    lineHeightSecondary: 21,
    lineHeightCaption: 18,
    lineHeightLabel: 16,
    radiusBadge: 6,
    radiusControl: 10,
    radiusCard: 14,
    radiusSheetCard: 20,
    radiusBubble: 20,
    radiusPanel: 22,
    radiusCapsule: 999,
    spaceXxs: 2,
    spaceXs: 4,
    spaceSm: 6,
    spaceS: 8,
    spaceM: 12,
    spaceL: 16,
    spaceXl: 24,
    controlCompact: 36,
    controlBar: 44,
    controlTouch: 48,
    // Legacy invariants preserved
    card: 20,
    body: 17,
    meta: 14,
    title: 20,
    touch: 48
  };

  for (const [key, value] of Object.entries(expected)) {
    assert.equal(Tokens[key], value, `Tokens.${key}`);
  }
});

test('NormalizedPrimitives defines PersonaSheetHeader, PersonaStatusPill, and PersonaSectionLabel with >=44vp touch targets', () => {
  const src = readEts('design/NormalizedPrimitives.ets');
  assert.match(src, /export struct PersonaSheetHeader/);
  assert.match(src, /export struct PersonaStatusPill/);
  assert.match(src, /export struct PersonaSectionLabel/);
  assert.match(src, /controlDiameter:\s*Tokens\.controlBar/);
  assert.match(src, /minWidth:\s*Tokens\.controlBar,\s*minHeight:\s*Tokens\.controlBar/);
  assert.match(src, /ButtonStyles\.touchRegion\(Tokens\.controlBar\)/);
  assert.match(src, /Tokens\.typeHeading/);
  assert.match(src, /Tokens\.typeSectionHeader/);
  assert.match(src, /Tokens\.typeLabel/);
  assert.match(src, /Colors\.hairlineGlass/);

  const sheet = readEts('components/QuickCommandSheet.ets');
  assert.match(sheet, /PersonaSheetHeader\(/);
  assert.match(sheet, /PersonaStatusPill\(/);
  assert.match(sheet, /PersonaSectionLabel\(/);
  assert.match(sheet, /closeIdentity:\s*'quick\.palette\.close'/);
  assert.match(sheet, /id\('quick\.palette\.search'\)/);
  assert.match(sheet, /id\('quick\.palette\.sheet'\)/);
});

test('InteractionDecisionForm prevents Plan header and Auto-Accept Edits dock truncation and uses design tokens', () => {
  const form = readEts('components/InteractionDecisionForm.ets');
  const header = form.slice(form.indexOf('@Builder taskHeader()'), form.indexOf('private approveTaskPlan'));
  assert.doesNotMatch(header, /interaction\.task\.skip_unavailable/);

  const secondary = form.slice(form.indexOf('@Builder dockSecondary('), form.indexOf('@Builder beautifulPlanChoice('));
  assert.match(secondary, /constraintSize\(\{\s*minHeight:\s*Math\.max\(Tokens\.controlBar,\s*this\.dockActionSize\(false\)\)/);
  assert.match(secondary, /fontSize\(Tokens\.typeSecondary\)/);
  assert.match(secondary, /labelStyle\(\{\s*maxLines:\s*2,\s*minFontSize:\s*12,\s*maxFontSize:\s*Tokens\.typeSecondary,\s*overflow:\s*TextOverflow\.Ellipsis\s*\}\)/);
  assert.match(form, /Colors\.pressOverlay/);
  assert.match(form, /Colors\.hairlineGlass/);
});

test('Wave 5 visual polish fixes: ellipsis dots, Starred MarkdownText, wrapping Todo status row, and native Status icon', () => {
  const strip = readEts('components/ComposerTopStrip.ets');
  assert.doesNotMatch(strip, /\$r\('sys\.symbol\.more'\)/);
  assert.match(strip, /id\('composer\.queue\.more\.' \+ item\.id\)/);

  const starred = readEts('components/StarredMessagesSheet.ets');
  assert.match(starred, /MarkdownText\(\{/);
  assert.match(starred, /id\('starred\.snapshot\.text\.' \+ star\.id\)/);

  const todo = readEts('components/TodoWorkspacePage.ets');
  const statusWords = todo.slice(todo.indexOf('@Builder phoneStatusWords('), todo.indexOf('@Builder seenChip('));
  assert.match(statusWords, /Flex\(\{\s*wrap:\s*FlexWrap\.Wrap/);
  const listControls = todo.slice(todo.indexOf('@Builder phoneListControls('), todo.indexOf('@Builder listContent('));
  assert.match(listControls, /SymbolGlyph\(\$r\('sys\.symbol\.list_checkmark'\)\)/);
  assert.match(listControls, /borderRadius\(Tokens\.radiusCapsule\)/);

  const index = readEts('pages/Index.ets');
  const slimHeader = index.slice(index.indexOf('@Builder slimSessionHeader('), index.indexOf('@Computed get floatingDraftHeader'));
  assert.match(slimHeader, /Colors\.elevatedGlass/);
  assert.match(slimHeader, /Colors\.hairlineGlass/);
  assert.match(slimHeader, /ButtonStyles\.touchRegion/);
});
