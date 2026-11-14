import { readFileSync } from "node:fs";
import { join } from "node:path";
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { initI18n } from "../i18n";
import type { InboxItem } from "./inbox";
import { nowText } from "./inbox-now";

const LANGS = ["en", "zh-Hans", "ja"] as const;
type Lang = (typeof LANGS)[number];
type TimeCase = { name: string; input: { now: string; at: string }; expected: Record<Lang, string> };
const fixture = JSON.parse(readFileSync(join(__dirname, "../../../spec/fixtures/inbox/time.json"), "utf8")) as {
  operation: string;
  time_zone: string;
  cases: TimeCase[];
};

const item: InboxItem = {
  id: "follow-time", source: "chat", kind: "group", account: "chat", account_name: "Team chat", title: "A matter",
  preview: "Latest line", count: 1, tier: "action", now: "waiting",
};
const waitingAt = (lang: Lang, time: string) => ({
  en: `Waiting for a reply · look again ${time}`,
  "zh-Hans": `等对方回复 · ${time} 再看`,
  ja: `相手の返信待ち · ${time}に再確認`,
})[lang];

const savedTZ = process.env.TZ;
beforeAll(() => {
  process.env.TZ = fixture.time_zone;
});
afterEach(async () => {
  vi.useRealTimers();
  await initI18n("zh-Hans");
});
afterAll(() => {
  if (savedTZ === undefined) delete process.env.TZ;
  else process.env.TZ = savedTZ;
});

describe("follow-up time (shared time.json)", () => {
  it("is the UTC inbox_follow_time contract", () => {
    expect(fixture.operation).toBe("inbox_follow_time");
    expect(fixture.time_zone).toBe("UTC");
  });

  for (const lang of LANGS) {
    describe(lang, () => {
      for (const c of fixture.cases) {
        it(c.name, async () => {
          await initI18n(lang);
          vi.useFakeTimers();
          vi.setSystemTime(new Date(c.input.now));
          const actual = nowText({ ...item, follow: { check_at: c.input.at } });
          expect(actual).toBe(waitingAt(lang, c.expected[lang]));
        });
      }
    });
  }
});
