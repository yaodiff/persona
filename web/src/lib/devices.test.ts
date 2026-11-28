import { describe, expect, it } from "vitest";
import { groupDevices, splitDevices } from "./devices";

describe("splitDevices", () => {
  const tokens = [
    { id: "old", last_seen: "2026-11-01T10:00:00Z" },
    { id: "gone-early", last_seen: "2026-11-04T09:00:00Z", revoked_at: "2026-11-02T08:00:00Z" },
    { id: "me", last_seen: "2026-11-03T10:00:00Z" },
    { id: "recent", last_seen: "2026-11-04T10:00:00Z" },
    { id: "gone-late", revoked_at: "2026-11-04T08:00:00Z" },
  ];

  it("keeps revoked credentials out of the devices in use", () => {
    const { active, revoked } = splitDevices(tokens, "me");
    expect(active.map((t) => t.id)).toEqual(["me", "recent", "old"]); // this browser first, then by last use
    expect(revoked.map((t) => t.id)).toEqual(["gone-late", "gone-early"]); // the latest revoked first
  });

  it("works without a current browser and without revoked ones", () => {
    const { active, revoked } = splitDevices(tokens.filter((t) => !t.revoked_at));
    expect(active.map((t) => t.id)).toEqual(["recent", "me", "old"]);
    expect(revoked).toEqual([]);
  });
});

describe("groupDevices", () => {
  it("groups active tokens by type, prioritizes current then online then recent, and omits empty groups", () => {
    const tokens = [
      { id: "phone-old", kind: "phone", role: "full", last_seen: "2026-11-01T10:00:00Z" },
      { id: "phone-live", kind: "phone", role: "full", last_seen: "2026-10-01T10:00:00Z" },
      { id: "watch-1", kind: "watch", role: "answer", created_at: "2026-11-02T10:00:00Z" },
      { id: "web-other", kind: "web", role: "full", last_seen: "2026-11-05T10:00:00Z" },
      { id: "web-me", kind: "web", role: "full", last_seen: "2026-11-01T10:00:00Z" },
      { id: "link-off", kind: "link", role: "link", last_seen: "2026-11-06T10:00:00Z" },
      { id: "link-on", kind: "link", role: "link", last_seen: "2026-11-01T10:00:00Z" },
      { id: "scoped-web", kind: "web", role: "scoped", last_seen: "2026-11-06T10:00:00Z" },
      { id: "gone", kind: "phone", role: "full", revoked_at: "2026-11-04T00:00:00Z" },
    ];
    const { groups, activeCount, revoked } = groupDevices(tokens, "web-me", ["phone-live", "link-on"]);
    expect(activeCount).toBe(8);
    expect(groups.map((g) => g.key)).toEqual(["devices", "browsers", "links", "integrations"]);
    expect(groups[0].items.map((t) => t.id)).toEqual(["phone-live", "watch-1", "phone-old"]);
    expect(groups[1].items.map((t) => t.id)).toEqual(["web-me", "web-other"]);
    expect(groups[2].items.map((t) => t.id)).toEqual(["link-on", "link-off"]);
    expect(groups[3].items.map((t) => t.id)).toEqual(["scoped-web"]);
    expect(revoked.map((t) => t.id)).toEqual(["gone"]);
  });

  it("omits empty groups and handles older hubs without online IDs", () => {
    const { groups, activeCount } = groupDevices([{ id: "web-1", kind: "web", role: "full" }], "web-1");
    expect(activeCount).toBe(1);
    expect(groups.map((g) => g.key)).toEqual(["browsers"]);
  });
});
