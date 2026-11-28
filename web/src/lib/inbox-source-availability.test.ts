import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { inboxSourceVisibility, visibleInboxKinds } from "./inbox-source-availability";

interface PresenceCase {
  name: string;
  input: { offers: { lark: boolean; mail: boolean }; stored_sources?: { lark: boolean; mail: boolean } };
  expected: { lark: boolean; mail: boolean };
}

const fixture = JSON.parse(readFileSync(new URL("../../../spec/fixtures/inbox-sources/presence.json", import.meta.url), "utf8")) as { cases: PresenceCase[] };

describe("Inbox source visibility (#17399)", () => {
  for (const c of fixture.cases) {
    it(c.name, () => {
      expect(inboxSourceVisibility(c.input.offers, c.input.stored_sources)).toEqual(c.expected);
    });
  }

  it("treats an explicit merged null as unknown, not an empty archive", () => {
    expect(inboxSourceVisibility({ lark: false, mail: false }, null)).toEqual({ lark: true, mail: true });
  });
});

const KINDS = [
  { key: "mention", label: "@Me" },
  { key: "dm", label: "DM" },
  { key: "group", label: "Group" },
  { key: "bot", label: "Bot" },
  { key: "doc", label: "Doc" },
  { key: "mail", label: "Mail" },
  { key: "agent", label: "Agent" },
] as const;

describe("Mail kind chip visibility (#17814)", () => {
  // The four offered × stored combinations, plus unknown (missing and explicit null).
  const cases = [
    { name: "offered and stored", offers: { lark: false, mail: true }, stored: { lark: false, mail: true }, shown: true },
    { name: "offered without stored items", offers: { lark: false, mail: true }, stored: { lark: false, mail: false }, shown: true },
    { name: "stored without an offer", offers: { lark: false, mail: false }, stored: { lark: false, mail: true }, shown: true },
    { name: "neither offered nor stored", offers: { lark: false, mail: false }, stored: { lark: false, mail: false }, shown: false },
    { name: "missing stored_sources is unknown", offers: { lark: false, mail: false }, stored: undefined, shown: true },
    { name: "an explicit null is unknown", offers: { lark: false, mail: false }, stored: null, shown: true },
  ] as const;

  for (const c of cases) {
    it(c.name, () => {
      const visibility = inboxSourceVisibility(c.offers, c.stored);
      const keys = visibleInboxKinds(KINDS, visibility).map((k) => k.key);
      expect(keys.includes("mail")).toBe(c.shown);
      // Every non-Mail kind stays regardless of the mail decision.
      for (const key of ["mention", "dm", "group", "bot", "doc", "agent"] as const) {
        expect(keys).toContain(key);
      }
    });
  }

  it("keeps the closed visibility result as the only input", () => {
    const hidden = inboxSourceVisibility({ lark: false, mail: false }, { lark: false, mail: false });
    expect(visibleInboxKinds(KINDS, hidden).map((k) => k.key)).toEqual(["mention", "dm", "group", "bot", "doc", "agent"]);
  });
});
