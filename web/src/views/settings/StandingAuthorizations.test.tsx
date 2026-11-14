// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { initI18n } from "../../i18n";
import { setToken } from "../../lib/api";
import { MANAGER_HEADER } from "../../lib/manager-choice";
import { sourceLabel, StandingAuthorizations } from "./StandingAuthorizations";

const path = "/assistant/standing-authorizations";
const grant = {
  id: "sa_example/one", machine_id: "example-computer", directory: "/code/example",
  actions: ["merge", "deploy-test"], conditions: ["ci_green", "no_conflicts", "Only the example repository"],
  quote: "Merge changes after the checks pass without asking again.", source: "Main conversation",
  granted_at: "2026-11-11T07:36:00Z", reversible: true,
};
type Call = { path: string; init: RequestInit; answer: (body: unknown, status?: number) => void };
let calls: Call[], host: HTMLDivElement, root: Root;
const text = () => document.body.textContent ?? "";
const buttons = (label: string) => [...document.body.querySelectorAll("button")].filter(b => b.textContent?.trim() === label);
const click = (label: string) => act(async () => { expect(buttons(label)[0], label).toBeTruthy(); buttons(label)[0].click(); });
const answer = (call: Call, body: unknown, status = 200) => act(async () => call.answer(body, status));
const writes = () => calls.filter(call => call.init.method === "DELETE");
async function render(manager = "example-manager", key = manager) {
  await act(async () => root.render(<StandingAuthorizations key={key} manager={manager} />));
}
async function loaded(authorizations: unknown[] = [grant]) {
  await render(); await answer(calls[0], { revision: 1, authorizations });
}

beforeEach(async () => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  await initI18n("en"); setToken("synthetic-token"); calls = [];
  vi.stubGlobal("matchMedia", () => ({ matches: false, addEventListener() {}, removeEventListener() {} }));
  vi.stubGlobal("fetch", vi.fn((url: string, init: RequestInit) => new Promise<Response>(resolve => {
    calls.push({ path: url.split("/api/v1")[1], init, answer: (body, status = 200) => resolve(new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } })) });
  })));
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
});
afterEach(async () => {
  act(() => root.unmount()); host.remove(); document.body.innerHTML = "";
  vi.unstubAllGlobals(); await initI18n("zh-Hans");
});

it("renders every registered scope and audit field, including both action reversibility values", async () => {
  await loaded([grant, { ...grant, id: "sa_two", directory: "/code/second", reversible: false, conditions: [],
    actions: ["push", "deploy-production", "send", "delete", "spend", "future-action"] }]);
  expect(calls[0].path).toBe(path);
  expect(new Headers(calls[0].init.headers).get(MANAGER_HEADER)).toBe("example-manager");
  expect(host.querySelectorAll("article")).toHaveLength(2);
  for (const value of [grant.machine_id, grant.directory, grant.quote, "Main chat",
    "Merge changes", "Deploy to a test environment", "All checks pass", "No merge conflicts", grant.conditions[2],
    "Push changes", "Deploy to production", "Send messages", "Delete data", "Spend money", "future-action", "No conditions recorded"]) expect(text()).toContain(value);
  expect(host.querySelector("time")?.dateTime).toBe(grant.granted_at);
  expect([...host.querySelectorAll("dt")].map(el => el.textContent)).toContain("Can the authorized actions be undone?");
  expect([...host.querySelectorAll("dd")].map(el => el.textContent)).toEqual(expect.arrayContaining(["Yes", "No"]));
});

it("shows loading then a real empty state without granting anything", async () => {
  await render(); expect(host.querySelector('[role="status"]')).toBeTruthy();
  expect(text()).not.toContain("No standing authorizations.");
  await answer(calls[0], { revision: 2, authorizations: [] });
  expect(text()).toContain("No standing authorizations."); expect(buttons("Revoke…")).toHaveLength(0);
});

it("treats a successful response without the registry contract as a failed read", async () => {
  await render(); await answer(calls[0], {});
  expect(host.querySelector('[role="alert"]')).toBeTruthy();
  expect(text()).not.toContain("No standing authorizations.");
});

it.each([403, 404, 503])("shows a failed read (%s) rather than an empty registry, and can retry", async status => {
  await render(); await answer(calls[0], { error: "Synthetic failure" }, status);
  expect(host.querySelector('[role="alert"]')).toBeTruthy(); expect(text()).not.toContain("No standing authorizations.");
  expect(text()).toContain(status === 403 ? "Full access" : "Couldn't load standing authorizations");
  await click("Refresh"); await answer(calls[1], { revision: 1, authorizations: [grant] });
  expect(text()).toContain(grant.quote);
});

it("requires confirmation, cancels without a write, then revokes exactly the selected grant once", async () => {
  await loaded([grant, { ...grant, id: "sa_two", directory: "/code/second", quote: "Another grant" }]);
  await click("Revoke…"); expect(writes()).toHaveLength(0);
  expect(document.querySelector('[role="dialog"]')?.textContent).toContain(grant.quote);
  expect(document.querySelector('[role="dialog"]')?.textContent).toContain(grant.machine_id);
  await click("Cancel"); expect(writes()).toHaveLength(0);
  await click("Revoke…");
  await act(async () => { buttons("Revoke authorization")[0].click(); buttons("Revoke authorization")[0].click(); });
  expect(writes()).toHaveLength(1); expect(writes()[0].path).toBe(`${path}/${encodeURIComponent(grant.id)}`);
  expect(new Headers(writes()[0].init.headers).get(MANAGER_HEADER)).toBe("example-manager");
  expect(host.querySelectorAll("article")).toHaveLength(2);
  await answer(writes()[0], { ok: true, revision: 2 });
  expect(host.querySelectorAll("article")).toHaveLength(1); expect(host.textContent).not.toContain(grant.quote);
  expect(host.textContent).toContain("Another grant");
});

it.each([403, 409, 503, 500])("retains the grant after DELETE %s and supports safe retry", async status => {
  await loaded(); await click("Revoke…"); await click("Revoke authorization");
  await answer(writes()[0], { error: "Synthetic failure" }, status);
  expect(host.querySelectorAll("article")).toHaveLength(1);
  expect(document.querySelector('[role="alert"]')?.textContent).toContain("Couldn't revoke this authorization");
  expect(buttons("Revoke authorization")[0].disabled).toBe(false);
  await click("Revoke authorization"); await answer(writes()[1], { ok: true, revision: 2 });
  expect(host.querySelectorAll("article")).toHaveLength(0); expect(host.textContent).toContain("No standing authorizations.");
});

it("does not treat an unacknowledged DELETE response as success", async () => {
  await loaded(); await click("Revoke…"); await click("Revoke authorization");
  await answer(writes()[0], { ok: false, revision: 1 });
  expect(host.querySelectorAll("article")).toHaveLength(1); expect(document.querySelector('[role="alert"]')).toBeTruthy();
});

it("retires late reads and writes when the manager or credential-keyed parent changes", async () => {
  await render(); const oldRead = calls[0]; await render("other-manager");
  await answer(calls[1], { revision: 4, authorizations: [{ ...grant, id: "other" }] });
  await answer(oldRead, { revision: 1, authorizations: [] }); expect(host.querySelectorAll("article")).toHaveLength(1);
  await click("Revoke…"); await click("Revoke authorization"); const oldWrite = writes()[0];
  await render("other-manager", "new-credential");
  await answer(calls.at(-1)!, { revision: 4, authorizations: [{ ...grant, id: "other" }] });
  await answer(oldWrite, { ok: true, revision: 5 }); expect(host.querySelectorAll("article")).toHaveLength(1);
});

it("words a known source in the page's language, whatever language it was written in, and keeps any other as written", async () => {
  const suggested = "Suggested rule: the same answer 3 times in 14 days, confirmed with Always.";
  for (const raw of [suggested, "建议的规则：14 天内 3 次相同的回答，点「总是」确认。", // i18n-ignore: the hub's sentence
    "提案されたルール：14 日間に同じ回答が 3 回あり、「常に」で確認されました。"]) expect(sourceLabel(raw), raw).toBe(suggested); // i18n-ignore: the hub's sentence
  for (const raw of ["Main conversation", " main chat. ", "主对话", "メインチャット"]) expect(sourceLabel(raw), raw).toBe("Main chat"); // i18n-ignore: sources as written
  expect(sourceLabel("Settings")).toBe("Settings");
  expect(sourceLabel("conversation")).toBe("A chat");
  expect(sourceLabel("Weekly review with Sam")).toBe("Weekly review with Sam");
  await initI18n("zh-Hans");
  expect(sourceLabel("Main conversation")).toBe("主对话"); // i18n-ignore: expected words
  expect(sourceLabel(suggested)).toBe("建议的规则：14 天内 3 次相同的回答，点「总是」确认。"); // i18n-ignore: expected words
});
