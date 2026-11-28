// @vitest-environment jsdom
// Group access on the Lark bot (#16848): hidden without the route or a pairing; every change PUTs the whole policy at the
// revision on screen; a stale revision reloads and says so.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "../../../lib/api";
import type { FeishuBotStatus } from "../../../lib/feishu-bot";
import type { GroupAccess, GroupAccessPolicy } from "../../../lib/feishu-group-access";

const api = vi.hoisted(() => ({ get: vi.fn(), put: vi.fn() }));
vi.mock("../../../lib/feishu-group-access", async (original) => ({
  ...(await original<typeof import("../../../lib/feishu-group-access")>()),
  groupAccess: () => api.get(),
  updateGroupAccess: (revision: number, policy: GroupAccessPolicy) => api.put(revision, policy),
}));
import { t } from "../../../i18n";
import { FeishuGroupAccess } from "./FeishuGroupAccess";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

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

const access = (over: Partial<GroupAccess> = {}): GroupAccess => ({
  revision: 3,
  paired: true,
  policy: policy(),
  available_tools: [{ name: "wiki_search", connector: "Wiki" }],
  recent_requests: [{ open_id: "ou_b", name: "Teammate B", chat_id: "oc_1", chat_name: "Project", at: 1795000000 }],
  limits: { list_max: 200, guest_tools_max: 8, per_chat_hourly_min: 1, per_chat_hourly_max: 120, per_chat_hourly_default: 20 },
  ...over,
});

const paired = (over: Partial<FeishuBotStatus> = {}): FeishuBotStatus => ({
  enabled: true,
  app_id: "cli_saved",
  domain: "feishu",
  has_secret: true,
  from_config: false,
  features: { main: true, cards: true, all_cards: false, pushes: false, sync: false, agents: false },
  connected: true,
  owner: { name: "Owner", paired_at: 1795000000 },
  pairing: null,
  ...over,
});

let host: HTMLDivElement;
let root: Root;

const flush = () => act(async () => {});
const render = async (status: FeishuBotStatus = paired()) => {
  act(() => root.render(<FeishuGroupAccess status={status} saving={false} />));
  await flush();
};
const button = (text: string) => [...document.body.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === text)!;
const input = (label: string) => document.body.querySelector<HTMLInputElement>(`input[aria-label="${label}"]`)!;
const select = (label: string) => document.body.querySelector<HTMLSelectElement>(`select[aria-label="${label}"]`)!;
const switchNamed = (label: string) => document.body.querySelector<HTMLButtonElement>(`[role="switch"][aria-label="${label}"]`)!;
const typeInto = (el: HTMLInputElement, value: string) =>
  act(() => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
const choose = (el: HTMLSelectElement, value: string) =>
  act(() => {
    Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("change", { bubbles: true }));
  });
const click = (el: HTMLElement) => act(() => el.click());

beforeEach(() => {
  api.get.mockReset();
  api.put.mockReset();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

describe("FeishuGroupAccess", () => {
  it("stays hidden when the hub has no group access route (404)", async () => {
    api.get.mockRejectedValue(new ApiError(404, "HTTP 404", {}, false));
    await render();
    expect(document.body.textContent).not.toContain(t("settings-feishubot.group_access_title"));
  });

  it("stays hidden, and asks nothing, while the bot has no owner", async () => {
    await render(paired({ owner: null }));
    expect(api.get).not.toHaveBeenCalled();
    expect(document.body.textContent).not.toContain(t("settings-feishubot.group_access_title"));
  });

  it("hides the lists the mode does not use", async () => {
    api.get.mockResolvedValue(access({ policy: policy({ mode: "owner_only" }) }));
    await render();
    expect(select(t("settings-feishubot.group_access_mode")).value).toBe("owner_only");
    const headings = [...document.body.querySelectorAll("h2")].map((h) => h.textContent);
    expect(headings).not.toContain(t("settings-feishubot.group_access_groups"));
    expect(headings).not.toContain(t("settings-feishubot.group_access_people"));
    expect(headings).not.toContain(t("settings-feishubot.group_access_tools"));
    expect(headings).toContain(t("settings-feishubot.group_access_blocked"));
  });

  it("changes the mode by putting the whole policy at the revision on screen", async () => {
    api.get.mockResolvedValue(access());
    api.put.mockResolvedValue(access({ revision: 4, policy: policy({ mode: "everyone_in_allowed_chats" }) }));
    await render();
    choose(select(t("settings-feishubot.group_access_mode")), "everyone_in_allowed_chats");
    await flush();
    expect(api.put).toHaveBeenCalledWith(3, policy({ mode: "everyone_in_allowed_chats" }));
    expect(select(t("settings-feishubot.group_access_mode")).value).toBe("everyone_in_allowed_chats");
  });

  it("adds an allowed group from the form and removes an allowed person", async () => {
    const twoChats = [{ chat_id: "oc_1", name: "Project" }, { chat_id: "oc_2", name: "Design" }];
    api.get.mockResolvedValue(access());
    api.put.mockImplementation(async (revision: number, p: GroupAccessPolicy) => access({ revision: revision + 1, policy: p }));
    await render();

    click(button(t("settings-feishubot.group_access_add_group")));
    typeInto(input(t("settings-feishubot.group_access_group_id")), " oc_2 ");
    typeInto(input(t("settings-feishubot.group_access_name")), "Design");
    click(button(t("settings-feishubot.group_access_add")));
    await flush();
    expect(api.put).toHaveBeenLastCalledWith(3, policy({ allowed_chats: twoChats }));

    click(button(t("settings-feishubot.group_access_add_person")));
    typeInto(input(t("settings-feishubot.group_access_person_id")), "ou with space");
    expect(button(t("settings-feishubot.group_access_add")).disabled).toBe(true);
    typeInto(input(t("settings-feishubot.group_access_person_id")), "ou_c");
    click(button(t("settings-feishubot.group_access_add")));
    await flush();
    expect(api.put).toHaveBeenLastCalledWith(4, policy({ allowed_chats: twoChats, allowed_users: [{ open_id: "ou_a", name: "Teammate A" }, { open_id: "ou_c" }] }));

    click(document.body.querySelector<HTMLButtonElement>(`button[aria-label="${t("settings-feishubot.group_access_remove", { name: "Teammate A" })}"]`)!);
    await flush();
    expect(api.put).toHaveBeenLastCalledWith(5, policy({ allowed_chats: twoChats, allowed_users: [{ open_id: "ou_c" }] }));
  });

  it("allows a recent request from its menu, adding the person and their group", async () => {
    api.get.mockResolvedValue(access({ policy: policy({ allowed_chats: [] }) }));
    api.put.mockImplementation(async (revision: number, p: GroupAccessPolicy) => access({ revision: revision + 1, policy: p }));
    await render();

    click(document.body.querySelector<HTMLButtonElement>(`button[aria-label="${t("settings-feishubot.group_access_request_actions", { name: "Teammate B" })}"]`)!);
    const allow = [...document.body.querySelectorAll<HTMLElement>('[role="menuitem"]')].find((el) => el.textContent?.includes(t("settings-feishubot.group_access_allow")))!;
    click(allow);
    await flush();
    expect(api.put).toHaveBeenCalledWith(
      3,
      policy({
        allowed_users: [{ open_id: "ou_a", name: "Teammate A" }, { open_id: "ou_b", name: "Teammate B" }],
        allowed_chats: [{ chat_id: "oc_1", name: "Project" }],
      }),
    );
  });

  it("switches a guest tool and keeps the rest of the policy", async () => {
    api.get.mockResolvedValue(access({ available_tools: [{ name: "wiki_search", connector: "Wiki" }, { name: "cal_list", connector: "Calendar" }] }));
    api.put.mockImplementation(async (revision: number, p: GroupAccessPolicy) => access({ revision: revision + 1, policy: p, available_tools: [{ name: "wiki_search", connector: "Wiki" }, { name: "cal_list", connector: "Calendar" }] }));
    await render();
    const label = t("settings-feishubot.group_access_tool_label", { connector: "Calendar", tool: "cal_list" });
    click(switchNamed(label));
    await flush();
    expect(api.put).toHaveBeenCalledWith(3, policy({ guest_tools: ["wiki_search", "cal_list"] }));
  });

  it("reloads on a stale revision and says the policy changed elsewhere", async () => {
    api.get.mockResolvedValueOnce(access()).mockResolvedValueOnce(access({ revision: 4, policy: policy({ per_chat_hourly: 40 }) }));
    api.put.mockRejectedValue(new ApiError(409, "conflict", {}));
    await render();
    choose(select(t("settings-feishubot.group_access_per_hour")), "5");
    await flush();
    expect(api.get).toHaveBeenCalledTimes(2);
    expect(document.body.textContent).toContain(t("settings-feishubot.group_access_conflict"));
    expect(select(t("settings-feishubot.group_access_per_hour")).value).toBe("40");
  });
});
