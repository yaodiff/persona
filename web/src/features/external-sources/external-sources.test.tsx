// @vitest-environment jsdom
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { initI18n } from "../../i18n";
import { type InboxItem } from "../../lib/inbox";
import { resetManagerChoice, setRosterDefault } from "../../lib/manager-choice";
import { parse } from "../../lib/router";
import { getState } from "../../lib/store";
import { TodosView } from "../../views/Todos";
import { InboxDetail } from "../../views/inbox/InboxDetail";
import { ChannelPage } from "../../views/settings/Channels";
import { IntegrationDetail } from "../../views/settings/IntegrationDetail";
import { SourcesUI } from "../../views/subscriptions/SourcesUI";
import type { BindingInput, ExternalBinding, ExternalPolicy, ExternalRef } from "./api";
import { ExternalSourceSettings } from "./ExternalSourceSettings";

const assistant = "assistant";
const direction = { id: "launch-direction", name: "Product launch" };
const todo = { id: "launch-todo", title: "Review onboarding", created_at: "2026-11-25T12:00:00Z", agents: [] };
const otherTodo = { ...todo, title: "Other assistant's to-do", manager: "other-assistant" };
const chat = { kind: "chat", id: "chat/Launch" };
const thread = { kind: "thread", id: "topic/42" };
const ref: ExternalRef = { provider: "lark", channel: "2", container: chat, item: thread };
const repositoryRef = { provider: "github", container: { kind: "repository", id: "example/web" }, item: { kind: "pull_request", id: "7" } };
const issueRef = { ...repositoryRef, item: { kind: "issue", id: "14" } };
const channel = { provider: "lark", channel: "2" };
const inherited = {
  policy: { mode: "digest", digest_interval_minutes: 60, action_role: "fyi", new_item_policy: "ask_owner", pre_filter: { keywords: ["release"] } },
  provenance: { mode: { source: "channel" }, digest_interval_minutes: { source: "channel" }, action_role: { source: "default" },
    new_item_policy: { source: "default" }, pre_filter: { source: "container" } },
};
const inboxItem: InboxItem = {
  id: "launch-item", manager: assistant, source: "lark", kind: "mention", account: "2", account_name: "Lark",
  title: "Launch topic", where: "Launch group", preview: "The launch checklist is ready.", count: 1,
  external_path: [ref, { ...channel, container: chat }, channel],
};
const initialBinding: ExternalBinding = {
  id: "connection-1", external_ref: { ...channel, container: { kind: "chat", id: "chat/Work" } },
  target_kind: "todo", target_id: todo.id, policy: {},
};
const directionBinding: ExternalBinding = { ...initialBinding, id: "connection-2", target_kind: "direction", target_id: direction.id };
const channelDetail = { id: "feishu", name: "Lark", kind: "im", source: "builtin", installed: true, enabled: true, state: "connected",
  bind: "none", capabilities: {}, waiting: { answers: 0, asks: 0 } };

const before = { ...getState() };
const globals = globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean };
globals.IS_REACT_ACT_ENVIRONMENT = true;

interface Request {
  path: string;
  query: URLSearchParams;
  method: string;
  manager: string | null;
  body: unknown;
}

let root: Root;
let host: HTMLDivElement;
let calls: Request[];
let bindings: ExternalBinding[];
let policy: ExternalPolicy;
let refuseNextSave: boolean;
const ok = (data: unknown, status = 200) => Response.json(data, { status });

beforeEach(async () => {
  await initI18n("en");
  resetManagerChoice();
  setRosterDefault(assistant);
  vi.stubGlobal("matchMedia", (query: string) => ({
    matches: false, media: query, addListener() {}, removeListener() {}, addEventListener() {}, removeEventListener() {},
  }));
  Object.assign(getState(), {
    todos: [todo, otherTodo], todosLoaded: true, todosSupported: true, features: [], sessions: {}, machines: [],
    assistant: { manager: assistant, todos_v2: false },
  });
  calls = [];
  bindings = [initialBinding, directionBinding];
  policy = { mode: "realtime" };
  refuseNextSave = false;
  vi.stubGlobal("fetch", vi.fn(async (address: string, options: RequestInit) => {
    const url = new URL(address, "https://app.example.test");
    const path = url.pathname.replace(/^\/api\/v1/, "");
    const method = options.method ?? "GET";
    const body = typeof options.body === "string" ? JSON.parse(options.body) : null;
    const manager = new Headers(options.headers).get("X-Persona-Manager");
    const query = url.searchParams;
    calls.push({ path, query, method, body, manager });
    const externalRef = JSON.parse(query.get("external_ref") ?? "null") as ExternalRef | null;
    switch (path) {
      case "/assistant/leaders": return ok({ leaders: [direction] });
      case "/inbox": return ok({ items: [inboxItem] });
      case "/channels/feishu": return ok(channelDetail);
      case "/assistant/external-source-bindings":
        if (method === "POST") {
          if (refuseNextSave) {
            refuseNextSave = false;
            return ok({ code: "external_target_not_found" }, 404);
          }
          const next = { ...body as BindingInput, id: "new-connection" };
          bindings.push(next);
          return ok(next, 201);
        }
        return ok({ bindings: bindings.filter((b) => b.target_kind === query.get("target_kind") && b.target_id === query.get("target_id")) });
      case "/assistant/external-source-bindings/lookup":
        return ok({ path: [externalRef], matches: [] });
      case "/assistant/external-source-bindings/summary":
        return ok({ bindings: [
          { ...directionBinding, source: "explicit", binding_id: directionBinding.id },
          ...(!query.has("provider") ? [
            { external_ref: repositoryRef, target_kind: "todo", target_id: todo.id, source: "repository_link", repository_role: "delivery" },
          ] : []),
        ] });
      case "/assistant/external-source-policies/effective":
        return ok({ external_ref: externalRef, ...inherited });
      case "/assistant/external-source-policies":
        if (method === "PUT") policy = body;
        return ok({ external_ref: externalRef, policy });
      case "/todos/launch-todo/repositories":
        return ok({ schema_version: 1, manager: assistant, todo_id: todo.id, revision: 1, repositories: [
          { repo_id: "web", requested_ref: { kind: "default" }, catalog_state: "current",
            source_snapshot: {
              identity: { provider: "github", instance_url: "https://github.com", path: "example/web", canonical_url: "https://github.com/example/web" },
              clone_url: "https://github.com/example/web", catalog_revision: 1, transport_origin: "canonical_default",
            },
            links: [
              { ...repositoryRef.item, number: "7", title: "Improve the sign-in flow", canonical_url: "https://github.com/example/web/pull/7" },
              { ...issueRef.item, number: "14", title: "Review release checklist", canonical_url: "https://github.com/example/web/issues/14", role: "reference" },
            ],
          },
        ] });
      default:
        if (path.startsWith("/assistant/external-source-bindings/") && method === "DELETE") {
          bindings = bindings.filter((b) => b.id !== path.split("/").at(-1));
          return new Response(null, { status: 204 });
        }
        if (path.startsWith("/assistant/external-source-bindings/") && method === "PUT") {
          const id = path.slice(path.lastIndexOf("/") + 1);
          const next = { ...body as BindingInput, id };
          bindings = bindings.map((binding) => binding.id === id ? next : binding);
          return ok(next);
        }
        return ok({});
    }
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  Object.assign(getState(), before);
  vi.unstubAllGlobals();
  resetManagerChoice();
  await initI18n("zh-Hans");
});

const render = (node: ReactNode) => act(async () => root.render(node));
const wait = (check: () => void) => vi.waitFor(async () => { await act(async () => {}); check(); });
const dialog = () => document.querySelector<HTMLElement>('[role="dialog"]')!;
const button = (label: string, scope: ParentNode = document) => [...scope.querySelectorAll<HTMLButtonElement>("button")].find(
  (b) => b.getAttribute("aria-label") === label || b.textContent?.trim() === label,
);
const click = async (label: string, scope: ParentNode = document) => {
  await wait(() => expect(button(label, scope)).toBeTruthy());
  await act(async () => button(label, scope)!.click());
};
function field<T extends HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement>(label: string): T {
  return [...dialog().querySelectorAll("label")].find((l) => l.textContent === label)!.control as T;
}
const fill = async (label: string, value: string) => act(async () => {
  const control = field(label);
  Object.getOwnPropertyDescriptor(Object.getPrototypeOf(control), "value")!.set!.call(control, value);
  control.dispatchEvent(new Event(control instanceof HTMLSelectElement ? "change" : "input", { bubbles: true }));
});
const options = (label: string) => [...field<HTMLSelectElement>(label).options].map((option) => option.value);
const description = (label: string) => document.getElementById(field(label).getAttribute("aria-describedby")!)?.textContent;
const policyOptions = () => ["Forwarding", "Responsibility", "New work", "Filter updates"].map(options);
const request = (end: string, method: string) => calls.find((c) => c.path.endsWith(end) && c.method === method);

describe("external sources in work and Inbox", () => {
  it.each([false, true])("shows one work section with explicit bindings and the saved PR and issue for todos_v2=%s", async (todosV2) => {
    Object.assign(getState(), { assistant: { manager: assistant, todos_v2: todosV2 } });
    await render(<TodosView selectedId={todo.id} />);
    const section = await vi.waitFor(() => {
      const section = host.querySelector<HTMLElement>('section[aria-label="External sources"]');
      expect(section).toBeTruthy();
      return section!;
    });
    await wait(() => {
      expect(section.textContent).toContain("chat/Work");
      const links = [...section.querySelectorAll<HTMLAnchorElement>("a")];
      expect(links.filter((link) => link.href.endsWith("/pull/7"))).toHaveLength(1);
      expect(links.filter((link) => link.href.endsWith("/issues/14"))).toHaveLength(1);
    });
    expect(request("/external-source-bindings", "GET")?.query.get("target_kind")).toBe("todo");
    await click("Edit", section);
    await wait(() => expect(dialog().textContent).toContain("Channel default"));
    await fill("Responsibility", "owner");
    await click("Save", dialog());
    expect(request("/connection-1", "PUT")?.body).toEqual({ ...initialBinding, id: undefined, policy: { action_role: "owner" } });
  });

  it("opens a direction detail, confirms removal, and re-reads the binding list", async () => {
    await render(<TodosView directionId={direction.id} manager={assistant} />);
    await wait(() => expect(host.querySelector("h1")?.textContent).toBe(direction.name));
    await click("Disconnect");
    expect(dialog().textContent).toContain("Future updates will stop using this connection.");
    await click("Cancel", dialog());
    expect(request("/connection-2", "DELETE")).toBeUndefined();
    await click("Disconnect");
    await click("Disconnect", dialog());
    await wait(() => expect(host.textContent).toContain("No source connections yet"));
    expect(request("/connection-2", "DELETE")?.manager).toBe(assistant);
  });

  it("offers the same policy schema in Inbox, keeps the draft on refusal, and saves the selected canonical scope", async () => {
    await render(<TodosView directionId={direction.id} />);
    await click("Connect external source");
    await wait(() => expect(dialog().textContent).toContain("Forwarding"));
    const workOptions = policyOptions();
    expect(workOptions).toEqual([
      ["", "off", "realtime", "digest", "on_demand"], ["", "owner", "fyi"], ["", "ask_owner", "auto_todo"], ["inherit", "replace"],
    ]);
    const workScope = options("Scope");
    const workTargets = options("Destination type");
    await click("Cancel", dialog());

    await render(<InboxDetail loaded item={inboxItem} />);
    await click("Connect source to direction or to-do");
    await wait(() => expect(dialog().textContent).toContain("Channel default"));
    expect(policyOptions()).toEqual(workOptions);
    expect(options("Scope")).toEqual(workScope);
    expect(options("Destination type")).toEqual(workTargets);
    expect(field("Scope").value).toBe("item");
    expect(description("Forwarding")).toBe("Send a digest · Channel default");
    expect(field<HTMLInputElement>("Digest interval (minutes)").placeholder).toBe("60");
    expect(dialog().textContent).toContain("Source setting");
    expect(dialog().textContent).toContain("release");
    expect(description("New work")).toBe("Ask me first · Persona default");

    await fill("Scope", "container");
    await fill("Destination type", "todo");
    expect(options("To-do")).toEqual(["", todo.id]);
    await fill("To-do", todo.id);
    await fill("Forwarding", "digest");
    await fill("Digest interval (minutes)", "45");
    await fill("Responsibility", "owner");
    await fill("New work", "auto_todo");
    await fill("Filter updates", "replace");
    await fill("Keywords", "release\nreview");
    await fill("Senders", "launch-team");
    await fill("Mentions", "project-owner");
    refuseNextSave = true;
    await click("Save", dialog());
    await wait(() => expect(dialog().textContent).toContain("That destination is no longer available."));
    expect(field("Keywords").value).toBe("release\nreview");
    await click("Save", dialog());
    await wait(() => expect(dialog()).toBeNull());
    const posts = calls.filter((c) => c.method === "POST" && c.path.endsWith("/external-source-bindings"));
    expect(posts).toHaveLength(2);
    expect(posts[1]).toMatchObject({
      manager: assistant,
      body: { external_ref: { ...channel, container: chat }, target_kind: "todo", target_id: todo.id,
        policy: { mode: "digest", digest_interval_minutes: 45, action_role: "owner", new_item_policy: "auto_todo",
          pre_filter: { keywords: ["release", "review"], senders: ["launch-team"], mentions: ["project-owner"] } },
      },
    });
  });
});

describe("external sources in Settings", () => {
  it.each([
    ["legacy Channels", () => <ChannelPage id="feishu" onBack={() => {}} onSettings={() => {}} />],
    ["unified Integration detail", () => <IntegrationDetail integrationsV2 target={{ kind: "builtin", id: "chat-fixture", channelId: "feishu", connectorId: null }} />],
    ["legacy Sources", () => <SourcesUI title="Sources" />],
    ["unified Data Flow", () => <SourcesUI title="Data Flow" initialView="graph" />],
  ] as const)("edits only channel defaults and links to work in %s", async (_name, view) => {
    await render(view());
    await wait(() => expect(host.textContent).toContain("Product launch"));
    const panel = host.querySelector("[data-external-source-settings]")!;
    const link = [...panel.querySelectorAll("a")].find((el) => el.textContent?.includes("Product launch"))!;
    expect(parse("/todos", new URL(link.href).search)).toEqual({ name: "todos", direction: direction.id, manager: assistant });
    expect(panel.querySelector('a[href*="launch-todo"]')?.textContent ?? "").toEqual(_name.includes("Sources") || _name.includes("Flow")
      ? expect.stringContaining("Repository link") : "");
    expect(button("Connect external source", panel)).toBeUndefined();
    expect(button("Disconnect", panel)).toBeUndefined();
    await click("Edit defaults", panel);
    await wait(() => expect(dialog().textContent).toContain("Forwarding"));
    expect(dialog().textContent).not.toContain("Destination type");
    expect(dialog().textContent).not.toContain("Scope");
    expect(field("Forwarding").value).toBe("realtime");
    await click("Restore defaults", dialog());
    expect(field("Forwarding").value).toBe("");
    await click("Save", dialog());
    expect(request("/external-source-policies", "PUT")?.body).toEqual({});
    expect(JSON.parse(request("/external-source-policies", "PUT")!.query.get("external_ref")!)).toEqual({ provider: "lark" });
    expect(calls.filter((c) => c.path.includes("external-source-bindings") && c.method !== "GET")).toEqual([]);
  });

  it("uses context copy on an extension page and hides an empty connected-work block", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => ok({ bindings: [] })));
    await render(<ExternalSourceSettings
      manager={assistant}
      provider="lark"
      copy={{
        channelDefaultsTitle: "How its updates are handled",
        channelHint: "Default handling for this connector.",
        flowsTitle: "Where its updates go",
        flowsHint: "To-dos and directions that receive updates.",
        hideEmptyFlows: true,
      }}
    />);
    await wait(() => expect(host.textContent).toContain("How its updates are handled"));
    expect(host.textContent).toContain("Default handling for this connector.");
    expect(host.textContent).not.toContain("Where its updates go");
    expect(host.textContent).not.toContain("Connected work");
  });
});
