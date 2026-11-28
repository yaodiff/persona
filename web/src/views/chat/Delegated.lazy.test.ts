/// <reference types="node" />
// The card of an answer the assistant gave for the user ("Change answer", #544) shows only after such an answer, so
// neither its code nor the words only it reads are on the first screen.
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import startup from "../../i18n/startup-namespaces.json";
import { startupMatcher } from "../../i18n/startup-keys";

const read = (path: string) => readFileSync(new URL(path, import.meta.url), "utf8");
const keys = (source: string) => [...new Set(source.match(/chat-delegate\.[a-z_]+/g) ?? [])];

describe("the delegated-answer card", () => {
  it("loads from its own chunk on first render", () => {
    const interactions = read("./Interactions.tsx");
    expect(interactions).not.toMatch(/^import[^;]*from\s+["']\.\/Delegated["']/m);
    expect(interactions).toMatch(/lazyPage\(\(\) => import\("\.\/Delegated"\)\.then\(\(m\) => m\.DelegatedCard\)\)/);
    // Every one is drawn inside DelegatedCardFrame, which keeps a failed load in its slot (Interactions.lazy-delegated.test.tsx).
    expect(interactions).toContain("delegated.map((d) => <DelegatedCardFrame key={d.ix.id} d={d} />)");
    expect(interactions.match(/<DelegatedCard\b/g)).toHaveLength(1);
  });

  it("keeps in the startup catalog the chat-delegate words first-screen code reads, and only those", () => {
    const inStartup = startupMatcher(startup);
    expect(startup).not.toContain("chat-delegate");
    const firstScreen = ["./Interactions.tsx", "../../lib/delegate.ts", "../../lib/card-answer.ts", "../../ui/RecommendedTag.tsx"].flatMap((f) => keys(read(f)));
    expect(firstScreen.length).toBeGreaterThan(10);
    for (const k of firstScreen) expect(inStartup(k), k).toBe(true);
    const own = keys(read("./Delegated.tsx")).filter((k) => !firstScreen.includes(k));
    expect(own.length).toBeGreaterThan(5);
    for (const k of own) expect(inStartup(k), k).toBe(false);
  });
});
