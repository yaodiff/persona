// The line about the hub over the Agents list (#2897): which one, in what order, what closing it remembers.
import { describe, expect, it } from "vitest";
import { dismissedHubNotices, dismissHubNotice, hubNotice, hubNoticeCandidates, hubNoticeKey, hubNoticeText, type HubUpdateStatus } from "./hub-update";

const hub = (o: Partial<HubUpdateStatus> = {}): HubUpdateStatus => ({
  version: "v1", build: 1, channel: "release", can_update: true, state: "idle", busy: [], auto: false, auto_notice: false, confirmed: true, ...o,
});
const newer = { version: "v2", build: 2, newer: true, bad: false };

describe("hub notice", () => {
  it("says nothing without a status or with nothing to say", () => {
    expect(hubNotice(null)).toBeNull();
    expect(hubNotice(hub())).toBeNull();
    expect(hubNotice(hub({ latest: { ...newer, newer: false } }))).toBeNull();
  });

  it("puts the most pressing first", () => {
    const s = hub({ state: "rolled_back", error: "新版本没起来，已回到 v1", auto: true, auto_notice: true, latest: { ...newer, held: "x" } });
    expect(hubNoticeCandidates(s).map((n) => n.kind)).toEqual(["rolled_back", "auto_on", "available"]);
    expect(hubNoticeCandidates(hub({ state: "downloading", latest: newer })).map((n) => n.kind)).toEqual(["updating", "available"]);
  });

  it("offers a newer release only when it would not install by itself", () => {
    expect(hubNotice(hub({ latest: newer }))?.kind).toBe("available");
    expect(hubNotice(hub({ auto: true, latest: newer }))).toBeNull();
    expect(hubNotice(hub({ auto: true, latest: { ...newer, held: "需要先确认" } }))?.kind).toBe("available");
    expect(hubNotice(hub({ latest: { ...newer, bad: true } }))).toBeNull();
    expect(hubNotice(hub({ can_update: false, latest: newer }))).toBeNull();
  });

  it("tells a Docker hub about a new image", () => {
    expect(hubNotice(hub({ channel: "docker", can_update: false, latest: { ...newer, version: "0.3.1", build: 0 } }))).toEqual({ kind: "new_image", version: "0.3.1" });
  });

  it("skips what the user closed, never the lines that cannot be closed", () => {
    const s = hub({ state: "rolled_back", error: "e", latest: newer });
    expect(hubNoticeKey(hubNotice(s)!)).toBe("rolledBack:2");
    expect(hubNotice(s, ["rolledBack:2"])?.kind).toBe("available");
    expect(hubNotice(s, ["rolledBack:2", "build:2"])).toBeNull();
    expect(hubNoticeKey({ kind: "auto_on" })).toBeNull();
    expect(hubNoticeKey({ kind: "updating", what: "x" })).toBeNull();
    expect(hubNoticeKey({ kind: "new_image", version: "0.3.1" })).toBe("image:0.3.1");
  });

  it("says each in words, the hub's own for a rollback", () => {
    expect(hubNoticeText(hubNotice(hub({ state: "restarting" }))!)).toBe("hub 正在更新：正在重启，连接将短暂中断");
    expect(hubNoticeText({ kind: "rolled_back", why: "新版本没起来", build: 2 })).toBe("新版本没起来");
    expect(hubNoticeText({ kind: "auto_on" })).toBe("hub 会在空闲时自动更新");
    expect(hubNoticeText({ kind: "available", version: "v2", build: 2 })).toBe("hub 有新版本 v2");
    expect(hubNoticeText({ kind: "new_image", version: "0.3.1" })).toBe("hub 有新镜像 0.3.1");
  });

  it("remembers the last eight it closed", () => {
    const kept = new Map<string, string>();
    const store = { getItem: (k: string) => kept.get(k) ?? null, setItem: (k: string, v: string) => void kept.set(k, v) };
    expect(dismissedHubNotices(store)).toEqual([]);
    for (let i = 0; i < 10; i++) dismissHubNotice(`build:${i}`, store);
    expect(dismissHubNotice("build:3", store)).toEqual(["build:2", "build:4", "build:5", "build:6", "build:7", "build:8", "build:9", "build:3"]);
    kept.set("persona.hubNotice.dismissed", "not json");
    expect(dismissedHubNotices(store)).toEqual([]);
  });
});
