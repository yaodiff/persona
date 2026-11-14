import { readFileSync } from "node:fs";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { initI18n } from "../i18n";
import { fromLine, matches, type InboxFilter, type InboxItem } from "./inbox";
import { nowText, rowMeta } from "./inbox-now";

const LANGS = ["en", "zh-Hans", "ja"] as const;
type Lang = (typeof LANGS)[number];
type Follow = { who?: string; next?: string; check_at?: string };
type NowCase = {
  name: string;
  input: { item: InboxItem; follow_by_lang?: Partial<Record<Lang, Follow>> };
  expected: { text: Record<Lang, string>; time?: string };
};
type RowCase = { name: string; input: { item: InboxItem }; expected: { meta: Record<Lang, string> | null } };

const fixture = (name: string) => join(__dirname, "../../../spec/fixtures/inbox", name);
const nowCases = (JSON.parse(readFileSync(fixture("now.json"), "utf8")) as { cases: NowCase[] }).cases;
const rowCases = (JSON.parse(readFileSync(fixture("row.json"), "utf8")) as { cases: RowCase[] }).cases;
const filterCase = (JSON.parse(readFileSync(fixture("filters.json"), "utf8")) as {
  cases: {
    name: string;
    input: { items: InboxItem[]; settings: { source: string; kind: string; query: string } };
    expected: { groups: { rows: { id: string }[] }[] };
  }[];
}).cases.find((c) => c.name === "search_four_fields_only")!;

const item = (patch: Partial<InboxItem> = {}): InboxItem => ({
  id: "n-time", source: "chat", kind: "group", account: "chat", account_name: "Team chat", title: "A matter",
  preview: "Latest line", count: 1, tier: "action", ...patch,
});
const tomorrow: Record<Lang, string> = { en: "tomorrow", "zh-Hans": "明天", ja: "明日" };
const waitingAt = (lang: Lang, time: string) => ({
  en: `Waiting for Alex · look again ${time}`,
  "zh-Hans": `等Alex · ${time} 再看`,
  ja: `Alexを待っています · ${time}に再確認`,
})[lang];
const clock = (d: Date) => `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;

// The text depends on the machine's region and zone (formatLocale keeps the system's region when it is in the interface
// language, so an en_AU machine wrote "3 Jan" where the fixtures say "Jan 3", #7061): pin both for every case.
const realZone = process.env.TZ;
beforeEach(() => {
  vi.stubGlobal("navigator", { language: "en-US", languages: ["en-US"] });
  process.env.TZ = "UTC";
});

afterEach(async () => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  if (realZone === undefined) delete process.env.TZ;
  else process.env.TZ = realZone;
  await initI18n("zh-Hans");
}, 30_000);

for (const lang of LANGS) {
  describe(`nowText (${lang})`, () => {
    for (const c of nowCases) {
      it(c.name, async () => {
        await initI18n(lang);
        const input = { ...c.input.item };
        const translated = c.input.follow_by_lang?.[lang];
        if (translated) input.follow = { ...input.follow, ...translated };
        let expected = c.expected.text[lang];
        if (c.expected.time) {
          const at = new Date(c.expected.time);
          // Make the fixture's instant tomorrow in every test time zone, then check its local wall clock.
          vi.useFakeTimers();
          vi.setSystemTime(new Date(at.getFullYear(), at.getMonth(), at.getDate() - 1, 12));
          expected = expected.replace("{time}", `${tomorrow[lang]} ${clock(at)}`);
        }
        expect(nowText(input)).toBe(expected);
      });
    }

    it("uses the local clock today, tomorrow, and a date farther ahead", async () => {
      await initI18n(lang);
      vi.useFakeTimers();
      vi.setSystemTime(new Date(2026, 0, 1, 12));
      const at = (day: number) => new Date(2026, 0, day, 9, 5).toISOString();
      const waiting = (day: number) => nowText(item({ now: "waiting", follow: { who: "Alex", check_at: at(day) } }));
      expect(waiting(1)).toBe(waitingAt(lang, "09:05"));
      expect(waiting(2)).toBe(waitingAt(lang, `${tomorrow[lang]} 09:05`));
      expect(waiting(3)).toBe(waitingAt(lang, `${{ en: "Jan 3", "zh-Hans": "1月3日", ja: "1月3日" }[lang]} 09:05`));
    });
  });

  describe(`rowMeta (${lang})`, () => {
    for (const c of rowCases) {
      it(c.name, async () => {
        await initI18n(lang);
        expect(rowMeta(c.input.item)).toBe(c.expected.meta?.[lang] ?? fromLine(c.input.item));
      });
    }
  });
}

describe("older and newer hubs", () => {
  it("leaves the app's own words in place without now or for an unknown key", () => {
    expect(nowText(item())).toBeUndefined();
    for (const now of ["a_future_state", "tomorrow", "next", "look_again", "waiting_anyone", "failed_plain"]) {
      expect(nowText(item({ now })), now).toBeUndefined();
    }
  });
});

describe("search", () => {
  it("adds where without changing the four-fields fixture's other boundaries", () => {
    const settings: InboxFilter = filterCase.input.settings;
    const matched = filterCase.input.items.filter((it) => matches(it, settings)).map((it) => it.id);
    expect(matched).toEqual(filterCase.expected.groups.flatMap((g) => g.rows.map((r) => r.id)));
    const base = filterCase.input.items[0];
    expect(matches({ ...base, where: "Launch group" }, { ...settings, query: "launch" })).toBe(true);
    expect(matches(base, { ...settings, query: "launch" })).toBe(false);
  });
});
