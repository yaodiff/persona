// Replays every sort projection fixture through the real Rust core's sort command, proving that the ArkTS host can
// reach the same pure-function boundary the Rust tests exercise. Each case's input goes in as JSON; the output must
// match the fixture's expected JSON exactly.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, readdirSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { HostCore, helperPath } from './host.mjs';

const fixtureDir = fileURLToPath(new URL('../../../spec/fixtures/sort/', import.meta.url));
const documents = readdirSync(fixtureDir).filter((f) => f.endsWith('.json')).sort().map((f) => ({
  file: f,
  ...JSON.parse(readFileSync(fixtureDir + f, 'utf8')),
}));

// Only agents_list operation fixtures are sort projections; session_state and hub_state are state decoders.
const projectionDocs = documents.filter((d) => d.operation === 'agents_list');
const projectionNames = new Set();
for (const doc of projectionDocs) {
  for (const c of doc.cases) {
    assert.ok(!projectionNames.has(c.name), `duplicate projection case: ${c.name}`);
    projectionNames.add(c.name);
  }
}

test('sort projection replays every agents_list fixture through the host', async () => {
  const host = new HostCore(helperPath);
  try {
    const replayed = new Set();
    for (const doc of projectionDocs) {
      for (const c of doc.cases) {
        const actual = await host.sort(c.input);
        assert.deepEqual(actual, c.expected, `${doc.file}/${c.name}`);
        replayed.add(c.name);
      }
    }
    assert.deepEqual([...replayed].sort(), [...projectionNames].sort(), 'exact projection case set');
    console.log(`  sort host replay: ${replayed.size} cases`);
  } finally {
    await host.end();
  }
});

test('sort host rejects invalid JSON input', async () => {
  const host = new HostCore(helperPath);
  try {
    await assert.rejects(() => host.sort({ invalid: true }), /sort/);
  } finally {
    await host.end();
  }
});
