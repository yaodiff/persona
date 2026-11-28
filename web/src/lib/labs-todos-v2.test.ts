import { afterEach, expect, it, vi } from "vitest";
import { api, ApiError } from "./api";
import { chooseManager, MANAGER_HEADER } from "./manager-choice";
import { parseDocument } from "./configuration/contract";
import {
  DEFAULT_TODOS_V2_ATTENTION_PREFS,
  getTodosV2State,
  isValidTodosV2QuietHours,
  parseTodosV2QuietHours,
  resetTodosV2StateForTest,
  resolveTodosV2AttentionPrefs,
  saveTodosV2AttentionPrefs,
  todosV2LabsSource,
} from "./labs-todos-v2";
import { t } from "../i18n";

vi.mock("./api", async (original) => ({ ...await original<typeof import("./api")>(), api: vi.fn() }));
vi.mock("./store", async (original) => ({
  ...await original<typeof import("./store")>(),
  patchAssistant: vi.fn(),
  refreshTodos: vi.fn(),
}));
import { patchAssistant, refreshTodos } from "./store";

afterEach(() => {
  vi.resetAllMocks();
  resetTodosV2StateForTest();
  chooseManager(null);
});

it("binds the Labs read, save and reset to the manager whose form was opened", async () => {
  const manager = "example";
  const source = todosV2LabsSource(manager);
  expect(source.key).toBe(`labs:assistant-todos-v2:${manager}`);
  const request = vi.mocked(api);
  request.mockResolvedValueOnce({ manager, todos_v2: false });
  const { doc } = parseDocument(await source.read());
  expect(doc.id).toBe("assistant-todos-v2");
  expect(doc.values.todos_v2).toBe(false);
  expect(doc.groups[0].fields[0].details?.map((d) => d.title)).toEqual([
    t("settings-labs.scope"),
    t("settings-labs.activation"),
    t("settings-labs.rollback"),
  ]);
  expect(request).toHaveBeenCalledWith("/assistant", expect.objectContaining({ headers: { [MANAGER_HEADER]: manager } }));
  request.mockResolvedValueOnce({ manager, todos_v2: true });
  const { doc: saved } = parseDocument(await source.update({ ...doc, changes: { todos_v2: true } }));
  expect(saved.values.todos_v2).toBe(true);
  expect(saved.notices?.todos_v2?.kind).toBe("info");
  expect(request).toHaveBeenLastCalledWith("/assistant", expect.objectContaining({
    headers: { [MANAGER_HEADER]: manager }, json: { todos_v2: true, expected_todos_v2: false },
  }));
  request.mockResolvedValueOnce({ manager, todos_v2: false });
  expect(parseDocument(await source.update({ ...saved, reset: ["todos_v2"] })).doc.values.todos_v2).toBe(false);
  expect(request).toHaveBeenLastCalledWith("/assistant", expect.objectContaining({
    json: { todos_v2: false, expected_todos_v2: true },
  }));
});

it("leaves older managers read-only, reports unconfirmed saves, and surfaces 409 conflicts", async () => {
  const source = todosV2LabsSource("example");
  vi.mocked(api).mockResolvedValueOnce({ manager: "example" });
  const { doc } = parseDocument(await source.read());
  expect(doc.groups[0].fields[0].read_only).toBe(true);
  expect(doc.groups[0].fields[0].disabled_reason).toBe(t("settings-labs.todos_v2_unsupported"));

  vi.mocked(api).mockResolvedValueOnce({ manager: "other", todos_v2: true });
  await expect(source.update({ schema_version: 1, id: doc.id, revision: "false", changes: { todos_v2: true } }))
    .rejects.toMatchObject({ code: "configuration_unavailable" });

  vi.mocked(api).mockRejectedValueOnce(new ApiError(409, "todos_v2 setting changed", { code: "configuration_conflict", error: "todos_v2 setting changed" }));
  await expect(source.update({ schema_version: 1, id: doc.id, revision: "false", changes: { todos_v2: true } }))
    .rejects.toMatchObject({ code: "configuration_conflict" });
});

it("resolves attention preferences with older-hub defaults and preserves explicit zero/false values", () => {
  expect(resolveTodosV2AttentionPrefs(undefined)).toEqual(DEFAULT_TODOS_V2_ATTENTION_PREFS);
  expect(resolveTodosV2AttentionPrefs({})).toEqual({
    todos_v2_auto_seen: "all_autonomous",
    todos_v2_pending_review_sla_hours: 24,
    todos_v2_can_wait_sla_hours: 48,
    todos_v2_fold_children: true,
    todos_v2_quiet_hours: "",
    todos_v2_digest_window_minutes: 0,
  });
  expect(resolveTodosV2AttentionPrefs({
    todos_v2_auto_seen: "off",
    todos_v2_pending_review_sla_hours: 0,
    todos_v2_can_wait_sla_hours: 0,
    todos_v2_fold_children: false,
    todos_v2_quiet_hours: "22:00-08:00",
    todos_v2_digest_window_minutes: 60,
  })).toEqual({
    todos_v2_auto_seen: "off",
    todos_v2_pending_review_sla_hours: 0,
    todos_v2_can_wait_sla_hours: 0,
    todos_v2_fold_children: false,
    todos_v2_quiet_hours: "22:00-08:00",
    todos_v2_digest_window_minutes: 60,
  });
  // Invalid quiet hours or unsupported digest window fall back to defaults.
  expect(resolveTodosV2AttentionPrefs({
    todos_v2_quiet_hours: "22:00-22:00",
    todos_v2_digest_window_minutes: 45 as unknown as 30,
  })).toMatchObject({
    todos_v2_quiet_hours: "",
    todos_v2_digest_window_minutes: 0,
  });
});

it("validates and parses strict 24-hour quiet hours ranges", () => {
  expect(isValidTodosV2QuietHours("")).toBe(true);
  expect(isValidTodosV2QuietHours("22:00-08:00")).toBe(true);
  expect(isValidTodosV2QuietHours("00:00-23:59")).toBe(true);
  expect(isValidTodosV2QuietHours("22:00-22:00")).toBe(false);
  expect(isValidTodosV2QuietHours("24:00-08:00")).toBe(false);
  expect(isValidTodosV2QuietHours("9:00-17:00")).toBe(false);
  expect(isValidTodosV2QuietHours("invalid")).toBe(false);
  expect(parseTodosV2QuietHours("22:00-08:00")).toEqual({ start: "22:00", end: "08:00" });
  expect(parseTodosV2QuietHours("")).toBeNull();
  expect(parseTodosV2QuietHours("22:00-22:00")).toBeNull();
});

it("saves attention preferences without expected_todos_v2, updates store, and refreshes to-dos", async () => {
  const manager = "example";
  chooseManager(manager);
  const request = vi.mocked(api);
  request.mockResolvedValueOnce({
    manager,
    todos_v2: true,
    todos_v2_auto_seen: "subtasks",
    todos_v2_pending_review_sla_hours: 12,
    todos_v2_can_wait_sla_hours: 72,
    todos_v2_fold_children: false,
  });
  const saved = await saveTodosV2AttentionPrefs(manager, { todos_v2_auto_seen: "subtasks" });
  expect(request).toHaveBeenCalledWith("/assistant", {
    method: "PUT",
    headers: { [MANAGER_HEADER]: manager },
    json: { todos_v2_auto_seen: "subtasks" },
  });
  expect(saved).toEqual({
    todos_v2_auto_seen: "subtasks",
    todos_v2_pending_review_sla_hours: 12,
    todos_v2_can_wait_sla_hours: 72,
    todos_v2_fold_children: false,
    todos_v2_quiet_hours: "",
    todos_v2_digest_window_minutes: 0,
  });
  expect(getTodosV2State(manager)).toEqual({ enabled: true, prefs: saved });
  expect(patchAssistant).toHaveBeenCalledWith(saved);
  expect(refreshTodos).toHaveBeenCalledTimes(1);
});

it("sends explicit MANAGER_HEADER for secondary assistants without mutating the shown assistant or accepting mismatched manager responses", async () => {
  chooseManager("primary");
  const request = vi.mocked(api);
  request.mockResolvedValueOnce({
    manager: "secondary",
    todos_v2: true,
    todos_v2_auto_seen: "off",
    todos_v2_pending_review_sla_hours: 48,
    todos_v2_can_wait_sla_hours: 24,
    todos_v2_fold_children: false,
  });

  const saved = await saveTodosV2AttentionPrefs("secondary", { todos_v2_auto_seen: "off" });
  expect(request).toHaveBeenCalledWith("/assistant", {
    method: "PUT",
    headers: { [MANAGER_HEADER]: "secondary" },
    json: { todos_v2_auto_seen: "off" },
  });
  expect(saved.todos_v2_auto_seen).toBe("off");
  expect(patchAssistant).not.toHaveBeenCalled();
  expect(refreshTodos).not.toHaveBeenCalled();

  request.mockResolvedValueOnce({
    manager: "primary",
    todos_v2: true,
  });
  await expect(saveTodosV2AttentionPrefs("secondary", { todos_v2_fold_children: true }))
    .rejects.toMatchObject({ code: "configuration_unavailable" });
});
