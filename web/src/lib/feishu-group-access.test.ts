import { beforeEach, describe, expect, it, vi } from "vitest";
import { api, ApiError } from "./api";
import {
  allowRequest, blockRequest, groupAccess, hasMember, hourlyChoices, isGroupAccessConflict, modeDetail, usableTools, withMember, withMode,
  withoutMember, withTool, updateGroupAccess, type GroupAccessPolicy, type GroupRequest, type GroupUser,
} from "./feishu-group-access";

vi.mock("./api", async (original) => ({ ...(await original<typeof import("./api")>()), api: vi.fn() }));

const apiMock = vi.mocked(api);

const policy = (over: Partial<GroupAccessPolicy> = {}): GroupAccessPolicy => ({
  mode: "allowlist",
  allowed_users: [{ open_id: "ou_a", name: "Teammate A" }],
  allowed_chats: [{ chat_id: "oc_1", name: "Project" }],
  blocked_users: [],
  blocked_chats: [],
  guest_tools: ["wiki_search"],
  per_chat_hourly: 20,
  ...over,
});

const request = (over: Partial<GroupRequest> = {}): GroupRequest => ({ open_id: "ou_b", name: "Teammate B", chat_id: "oc_2", at: 1795000000, ...over });

beforeEach(() => {
  apiMock.mockReset();
});

describe("groupAccess", () => {
  it("reads the policy from the dedicated route", async () => {
    apiMock.mockResolvedValue({ revision: 1 });
    await expect(groupAccess()).resolves.toEqual({ revision: 1 });
    expect(apiMock).toHaveBeenCalledWith("/bots/feishu/group-access");
  });

  it("answers null on 404, the hub before group access", async () => {
    apiMock.mockImplementation(async () => {
      throw new ApiError(404, "HTTP 404", {}, false);
    });
    expect(await groupAccess()).toBeNull();
  });

  it("lets other failures through", async () => {
    apiMock.mockImplementation(async () => {
      throw new ApiError(503, "unavailable", {}, true);
    });
    await expect(groupAccess()).rejects.toMatchObject({ status: 503 });
  });
});

describe("updateGroupAccess", () => {
  it("puts the whole policy with the revision it was read at", async () => {
    apiMock.mockResolvedValue({ revision: 4 });
    const p = policy({ mode: "owner_only" });
    await updateGroupAccess(3, p);
    expect(apiMock).toHaveBeenCalledWith("/bots/feishu/group-access", { method: "PUT", json: { revision: 3, policy: p } });
  });

  it("recognises a stale revision (409)", () => {
    expect(isGroupAccessConflict(new ApiError(409, "conflict", {}))).toBe(true);
    expect(isGroupAccessConflict(new ApiError(400, "bad", {}))).toBe(false);
    expect(isGroupAccessConflict(new Error("network"))).toBe(false);
  });
});

describe("member lists", () => {
  it("adds an entry, replacing one already named by the same id", () => {
    const list: GroupUser[] = [{ open_id: "ou_a", name: "Old" }];
    expect(withMember(list, { open_id: "ou_b" })).toEqual([{ open_id: "ou_a", name: "Old" }, { open_id: "ou_b" }]);
    expect(withMember(list, { open_id: "ou_a", name: "New" })).toEqual([{ open_id: "ou_a", name: "New" }]);
  });

  it("removes by id and tells when an id is listed", () => {
    const chats = [{ chat_id: "oc_1" }, { chat_id: "oc_2", name: "Two" }];
    expect(withoutMember(chats, "oc_1")).toEqual([{ chat_id: "oc_2", name: "Two" }]);
    expect(hasMember(chats, "oc_2")).toBe(true);
    expect(hasMember(chats, "oc_9")).toBe(false);
  });
});

describe("policy edits", () => {
  it("changes the mode and keeps the lists", () => {
    const p = policy();
    expect(withMode(p, "owner_only")).toEqual({ ...p, mode: "owner_only" });
  });

  it("switches a guest tool on once and off", () => {
    const p = policy();
    expect(withTool(p, "wiki_search", true).guest_tools).toEqual(["wiki_search"]);
    expect(withTool(p, "wiki_search", false).guest_tools).toEqual([]);
    expect(withTool(p, "calendar", true).guest_tools).toEqual(["wiki_search", "calendar"]);
  });

  it("drops tools the hub no longer offers before a save", () => {
    const p = policy({ guest_tools: ["wiki_search", "gone"] });
    expect(usableTools(p, [{ name: "wiki_search", connector: "Wiki" }]).guest_tools).toEqual(["wiki_search"]);
  });

  it("offers the standard hourly choices with the value in force", () => {
    expect(hourlyChoices(20)).toEqual([5, 10, 20, 40, 60, 120]);
    expect(hourlyChoices(7)).toEqual([5, 7, 10, 20, 40, 60, 120]);
  });

  it("describes each mode", () => {
    expect(modeDetail("owner_only")).toBeTruthy();
    expect(modeDetail("allowlist")).not.toBe(modeDetail("everyone_in_allowed_chats"));
  });
});

describe("recent requests", () => {
  it("allows the person and adds their group when it is not listed", () => {
    const next = allowRequest(policy({ allowed_chats: [] }), request());
    expect(next.allowed_users).toEqual([{ open_id: "ou_a", name: "Teammate A" }, { open_id: "ou_b", name: "Teammate B" }]);
    expect(next.allowed_chats).toEqual([{ chat_id: "oc_2" }]);
  });

  it("keeps a group that is already allowed with its label", () => {
    const next = allowRequest(policy(), request({ chat_id: "oc_1", chat_name: "Other" }));
    expect(next.allowed_chats).toEqual([{ chat_id: "oc_1", name: "Project" }]);
  });

  it("blocks the person whatever the mode", () => {
    const next = blockRequest(policy({ mode: "everyone_in_allowed_chats" }), request());
    expect(next.blocked_users).toEqual([{ open_id: "ou_b", name: "Teammate B" }]);
    expect(next.allowed_users).toEqual(policy().allowed_users);
  });
});
