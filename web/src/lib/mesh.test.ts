import { describe, expect, it } from "vitest";
import { hubCount, hubLabel, machineWithHub, routeText } from "./mesh";
import type { Machine } from "./types";

const m = (id: string, hub?: [string, string], route?: Machine["route"]): Machine => ({
  id,
  name: id.toUpperCase(),
  online: true,
  ...(hub ? { hub_id: hub[0], hub_name: hub[1] } : {}),
  route,
});

describe("mesh labels", () => {
  it("say nothing with one hub or an older hub without hub_id", () => {
    const old = [m("a"), m("b")];
    expect(hubCount(old)).toBe(1);
    expect(hubLabel(old[0], old)).toBe("");
    const one = [m("a", ["h1", "家里"]), m("b", ["h1", "家里"])];
    expect(hubLabel(one[0], one)).toBe("");
    expect(machineWithHub("a", one)).toBe("A");
    expect(routeText({ ...one[0], route: { via: "local", hops: 0, rtt_ms: 3 } }, one)).toBe("");
  });

  it("name the hub once the list spans two hubs", () => {
    const two = [m("a", ["h1", "家里"]), m("b", ["h2", "公司"], { via: "peer", hops: 1, rtt_ms: 24 })];
    expect(hubCount(two)).toBe(2);
    expect(hubLabel(two[1], two)).toBe("公司");
    expect(machineWithHub("b", two)).toBe("公司 · B");
    expect(routeText(two[1], two)).toBe("经互联 hub · 1 跳 · 24 ms");
  });

  it("fall back to the hub id when a hub has no name", () => {
    const two = [m("a", ["h1", ""]), m("b", ["h2", "公司"])];
    expect(hubLabel(two[0], two)).toBe("h1");
  });
});
