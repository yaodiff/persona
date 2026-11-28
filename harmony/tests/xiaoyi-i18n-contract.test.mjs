import test from 'node:test';
import assert from 'node:assert/strict';
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join, resolve } from 'node:path';

const MAIN_DIR = resolve(new URL('../entry/src/main', import.meta.url).pathname);
const LOCALES = ['base', 'en_US', 'zh_CN', 'ja_JP'];

function loadLocaleMap(locale) {
  const path = resolve(MAIN_DIR, 'resources', locale, 'element/string.json');
  const parsed = JSON.parse(readFileSync(path, 'utf8'));
  const map = new Map();
  for (const item of parsed.string) {
    assert.ok(item.name && typeof item.value === 'string', `${locale}: malformed entry`);
    assert.ok(!map.has(item.name), `${locale}: duplicate key ${item.name}`);
    map.set(item.name, item.value);
  }
  return map;
}

function collectFiles(dir) {
  const out = [];
  for (const entry of readdirSync(dir)) {
    const full = join(dir, entry);
    if (statSync(full).isDirectory()) out.push(...collectFiles(full));
    else if (full.endsWith('.ets') || full.endsWith('.ts')) out.push(full);
  }
  return out;
}

function extractPlaceholders(text) {
  const found = text.match(/\{[a-zA-Z0-9_]+\}/g) || [];
  return Array.from(new Set(found)).sort();
}

test('all XiaoYi string keys exist in base, en_US, zh_CN, and ja_JP with non-empty text and matching placeholders', () => {
  const maps = new Map(LOCALES.map((loc) => [loc, loadLocaleMap(loc)]));
  const baseMap = maps.get('base');
  const xiaoYiKeys = Array.from(baseMap.keys()).filter((k) => k.startsWith('xiaoyi_') || k.includes('phone_xiaoyi'));
  assert.ok(xiaoYiKeys.length >= 50, `expected >= 50 XiaoYi i18n keys, got ${xiaoYiKeys.length}`);

  for (const key of xiaoYiKeys) {
    const basePlaceholders = extractPlaceholders(baseMap.get(key));
    for (const loc of LOCALES) {
      const val = maps.get(loc).get(key);
      assert.ok(typeof val === 'string' && val.trim().length > 0, `${loc}: missing or empty key "${key}"`);
      assert.deepEqual(extractPlaceholders(val), basePlaceholders, `${loc}: placeholder mismatch for "${key}"`);
    }
  }
});

test('every tr("xiaoyi_*") key referenced in XiaoYi code exists in all four locales', () => {
  const maps = new Map(LOCALES.map((loc) => [loc, loadLocaleMap(loc)]));
  const targets = [
    ...collectFiles(resolve(MAIN_DIR, 'ets/xiaoyi')),
    resolve(MAIN_DIR, 'ets/pages/AgentCardPage.ets'),
    resolve(MAIN_DIR, 'ets/components/XiaoYiSettingsPage.ets')
  ];
  const referenced = new Set();
  for (const file of targets) {
    const src = readFileSync(file, 'utf8');
    for (const match of src.matchAll(/\btr\(\s*['"](xiaoyi_[a-z0-9_]+)['"]\s*\)/g)) {
      referenced.add(match[1]);
    }
  }
  assert.ok(referenced.size > 0);
  for (const key of referenced) {
    for (const loc of LOCALES) {
      assert.ok(maps.get(loc).has(key), `${loc}: missing referenced key "${key}"`);
    }
  }
});
