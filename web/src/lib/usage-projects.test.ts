// Token usage by project (#16530): the groups keep the hub's order and add up; home, temp, assistant and unknown are
// named by the page (never a folder's name); shares read as whole percents above 10%, one decimal below. The tests
// run in Chinese.
import { describe, expect, it } from "vitest";
import { projectGroups, projectName, projectTotal, sharePercent, type UsageProject, type UsageProjectRange } from "./usage-projects";

const p = (key: string, kind: UsageProject["kind"], group: UsageProject["group"], tokens: number, name = ""): UsageProject => ({
  key,
  kind,
  group,
  name,
  machines: 1,
  share: 0,
  tokens: { cache_read: tokens },
});

describe("usage projects", () => {
  it("groups a range's projects in the hub's order, with each group's sum", () => {
    const range: UsageProjectRange = {
      days: 7,
      total: { cache_read: 1000 },
      not_split: { cache_read: 50 },
      projects: [
        p("git:github.com/acme/app", "repo", "development", 500, "acme/app"),
        p("assistant", "assistant", "other", 200),
        p("local:abc", "local", "development", 150, "scratch"),
        p("unknown", "unknown", "unknown", 100),
      ],
    };
    const g = projectGroups(range);
    expect(g.development.map((x) => x.key)).toEqual(["git:github.com/acme/app", "local:abc"]);
    expect(g.other.map((x) => x.key)).toEqual(["assistant"]);
    expect(g.unknown.map((x) => x.key)).toEqual(["unknown"]);
    expect(g.sums).toEqual({ development: 650, other: 200, unknown: 100 });
  });

  it("names what has no name of its own, never a folder", () => {
    expect(projectName({ kind: "repo", name: "acme/app" })).toBe("acme/app");
    expect(projectName({ kind: "folder", name: "Trip" })).toBe("Trip");
    expect(projectName({ kind: "home", name: "someone" })).toBe("主目录");
    expect(projectName({ kind: "temp", name: "" })).toBe("临时目录");
    expect(projectName({ kind: "assistant", name: "" })).toBe("助手");
    expect(projectName({ kind: "unknown", name: "" })).toBe("未知目录");
  });

  it("counts every kind and writes shares", () => {
    expect(projectTotal({ in: 1, out: 2, cache_write: 3, cache_read: 4 })).toBe(10);
    expect(projectTotal(undefined)).toBe(0);
    expect(sharePercent(0.361)).toBe("36%");
    expect(sharePercent(0.024)).toBe("2.4%");
    expect(sharePercent(0.0004)).toBe("<0.1%");
    expect(sharePercent(0)).toBe("0%");
  });
});
