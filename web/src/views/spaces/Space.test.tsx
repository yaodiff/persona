// @vitest-environment jsdom
// A domain space's pages (#13500): its home lists its conversations and says how its information is used, an unknown
// space says so, the SPACE.md editor says plainly when the file changed meanwhile, and New space creates one at the
// address its name suggests.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const hub = vi.hoisted(() => ({
  calls: [] as { path: string; method: string; json?: unknown }[],
  mdWrite: "ok" as "ok" | "stale",
  kinds: [] as { slug: string; title: string; disabled?: string }[],
}));
vi.mock("../../lib/api", async (orig) => {
  const real = await orig<typeof import("../../lib/api")>();
  const track = (slug: string, title: string) => ({
    slug, kind: "space", title, icon: "wallet", description: "Budget and taxes", default_categories: ["finance"], template: "space", builtin: false, archived: false, revision: 2,
    config: { schema: 1, template: "space", context: "track", default_categories: ["finance"], tag_focus: [], rhythms: [], attention: { push_cap: "P0", deep_default: "quick", enter_mode: "none" }, supervision: "none", usage: [], assembly: "track", capture_target: true, channels: [], sidebar: { group: "spaces" } },
  });
  const content = "# Money\n\nWhat this space is for.\n\n## Rules\n\n- **Receipts** go here\n\n<!-- persona:managed:state:begin -->\nTaxes filed.\n<!-- persona:managed:state:end -->\n";
  return {
    ...real,
    api: (path: string, init?: { method?: string; json?: unknown }) => {
      const method = init?.method ?? "GET";
      hub.calls.push({ path, method, json: init?.json });
      if (path === "/assistant/tracks" && method === "GET") return Promise.resolve({ tracks: [track("money", "Money")] });
      if (path === "/assistant/tracks" && method === "POST") return Promise.resolve(track((init!.json as { slug: string }).slug, "Family"));
      if (path === "/assistant/track-kinds") return Promise.resolve({ kinds: hub.kinds });
      if (path.startsWith("/assistant?space=money")) return Promise.resolve({ machine_id: "mac", conversations: [{ machine_id: "mac", session_id: "c1", title: "Tax questions", space: "money", updated_at: "2026-11-20T08:00:00Z" }] });
      if (path === "/assistant/tracks/money/space-md" && method === "GET") return Promise.resolve({ content, digest: "d1", state: "Taxes filed." });
      if (path === "/assistant/tracks/money/space-md" && method === "PUT") {
        return hub.mdWrite === "stale" ? Promise.reject(new real.ApiError(409, "space-md changed", {})) : Promise.resolve({ content: (init!.json as { content: string }).content, digest: "d2", state: "Taxes filed." });
      }
      if (path.startsWith("/assistant/conversations?space=") && method === "POST") {
        return Promise.resolve({ session: { machine_id: "mac", session_id: "c-new", title: "New chat", space: "money" } });
      }
      return Promise.resolve({});
    },
  };
});
const composer = vi.hoisted(() => ({
  onSend: null as null | ((out: { text: string; images: unknown[] }) => Promise<boolean>),
}));
vi.mock("../chat/ChatView", () => ({ ChatView: () => null }));
vi.mock("../chat/ModelPill", () => ({ AssistantModelPill: () => <span data-testid="space-model-pill">model-pill</span> }));
vi.mock("../chat/Composer", () => ({
  Composer: ({ onSend, trailing }: { onSend: (out: { text: string; images: unknown[] }) => Promise<boolean>; trailing?: React.ReactNode }) => {
    composer.onSend = onSend;
    return <div data-testid="space-composer">{trailing}</div>;
  },
}));

import { NEW_CONVERSATION, pickEffort, pickLauncher, pickModel, pickServiceTier } from "../../lib/assistant-chat";
import { ASSISTANT_START_SERVICE_TIER_FEATURE } from "../../lib/assistant-models";
import { getState, reset } from "../../lib/store";
import { resetTracksForTest } from "../../lib/tracks";
import { NewSpace } from "./NewSpace";
import { Space } from "./Space";
import { SpaceMdEditor } from "./SpaceMdEditor";
import { t } from "../../i18n";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); });
};
const typeInto = (node: HTMLInputElement | HTMLTextAreaElement, value: string) => act(async () => {
  const proto = node instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
  Object.getOwnPropertyDescriptor(proto, "value")!.set!.call(node, value);
  node.dispatchEvent(new Event("input", { bubbles: true }));
});
const button = (text: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === text)!;

beforeEach(() => {
  hub.calls.length = 0;
  hub.mdWrite = "ok";
  hub.kinds = [];
  resetTracksForTest();
  history.replaceState(null, "", "/");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  document.body.innerHTML = "";
  reset();
  pickLauncher(NEW_CONVERSATION, undefined);
  pickEffort(NEW_CONVERSATION, undefined);
  pickServiceTier(undefined);
});

describe("a space's home", () => {
  it("shows the space, how its information is used, its conversations and its categories", async () => {
    act(() => root.render(<Space slug="money" fresh onToggleSidebar={() => {}} />));
    await flush();
    expect(host.querySelector("h1")?.textContent).toBe("Money");
    expect(host.textContent).toContain("Budget and taxes");
    expect(host.querySelector('[data-testid="space-privacy"]')?.textContent).toBe(t("spaces.privacy_line"));
    expect(host.textContent).toContain("Tax questions");
    expect(host.textContent).toContain(t("spaces.category_finance"));
    expect(host.textContent).toContain("What this space is for.");
    expect(host.textContent).not.toContain("persona:managed");
  });

  it("draws SPACE.md as formatted text, without Markdown marks or Persona's kept blocks", async () => {
    act(() => root.render(<Space slug="money" fresh onToggleSidebar={() => {}} />));
    await flush();
    const about = host.querySelector('[data-testid="space-md"]')!;
    expect(about.querySelector("h1")).toBeNull(); // the title only repeats the space's name above it
    expect(about.querySelector("h2")?.textContent).toBe("Rules");
    expect(about.querySelector("li strong")?.textContent).toBe("Receipts");
    expect(about.textContent).not.toContain("#");
    expect(about.textContent).not.toContain("**");
    expect(about.textContent).not.toContain("persona:managed");
    expect(about.textContent).not.toContain("Taxes filed.");
    // where things stand is said once, below, in words
    expect(host.textContent).toContain(`${t("spaces.md_state")} Taxes filed.`);
  });

  it("says when the space is not there", async () => {
    act(() => root.render(<Space slug="nowhere" fresh onToggleSidebar={() => {}} />));
    await flush();
    expect(host.textContent).toContain(t("spaces.gone_title"));
  });

  it("shows the model selector pill and forwards picked launcher, model, effort and service tier on start", async () => {
    Object.assign(getState(), {
      assistant: { machine_id: "mac", launcher: "claude", conversations: [] },
      features: [ASSISTANT_START_SERVICE_TIER_FEATURE],
    });
    pickLauncher(NEW_CONVERSATION, "codex");
    pickModel(NEW_CONVERSATION, "gpt-5.4");
    pickEffort(NEW_CONVERSATION, "high");
    pickServiceTier("fast");

    act(() => root.render(<Space slug="money" fresh onToggleSidebar={() => {}} />));
    await flush();
    expect(host.querySelector('[data-testid="space-model-pill"]')).not.toBeNull();

    await act(async () => {
      await composer.onSend?.({ text: "Check Q4 budget", images: [] });
    });
    await flush();
    const post = hub.calls.find((c) => c.method === "POST" && c.path === "/assistant/conversations?space=money");
    expect(post?.json).toEqual({
      prompt: "Check Q4 budget",
      images: undefined,
      launcher: "codex",
      model: "gpt-5.4",
      effort: "high",
      service_tier: "fast",
      space: "money",
    });
  });
});

describe("the SPACE.md editor", () => {
  it("edits only the user's part and keeps Persona's block", async () => {
    act(() => root.render(<SpaceMdEditor open slug="money" onClose={() => {}} />));
    await flush();
    const fields = document.querySelectorAll("textarea");
    expect(fields).toHaveLength(1);
    expect(document.body.textContent).toContain("Taxes filed.");
    await typeInto(fields[0]!, "# Money\n\nBudget, taxes and savings.\n\n");
    await act(async () => button(t("spaces.save")).click());
    await flush();
    const put = hub.calls.find((c) => c.method === "PUT");
    expect(put?.json).toEqual({
      content: "# Money\n\nBudget, taxes and savings.\n\n<!-- persona:managed:state:begin -->\nTaxes filed.\n<!-- persona:managed:state:end -->\n",
      base_digest: "d1",
    });
  });

  it("says plainly when the file changed meanwhile, and offers the latest", async () => {
    hub.mdWrite = "stale";
    act(() => root.render(<SpaceMdEditor open slug="money" onClose={() => {}} />));
    await flush();
    await typeInto(document.querySelector("textarea")!, "changed");
    await act(async () => button(t("spaces.save")).click());
    await flush();
    expect(document.body.textContent).toContain(t("spaces.md_stale"));
    expect(button(t("spaces.md_reload"))).toBeTruthy();
  });
});

describe("New space", () => {
  it("creates a space of kind space at the address its name suggests, and opens it", async () => {
    act(() => root.render(<NewSpace onToggleSidebar={() => {}} />));
    await flush();
    await typeInto(host.querySelector("input")!, "Family");
    await act(async () => button(t("spaces.create")).click());
    await flush();
    const post = hub.calls.find((c) => c.method === "POST" && c.path === "/assistant/tracks");
    expect(post?.json).toMatchObject({ slug: "family", kind: "space", title: "Family", icon: "layers", default_categories: [] });
    expect(location.pathname).toBe("/tracks/family/new");
  });

  it("starts on the first kind the assistant offers when the default kind is not among them", async () => {
    hub.kinds = [{ slug: "space", title: "Space", disabled: "schema" }, { slug: "orchestration", title: "Main chat" }, { slug: "project", title: "Project" }];
    act(() => root.render(<NewSpace onToggleSidebar={() => {}} />));
    await flush();
    await typeInto(host.querySelector("input")!, "Family");
    await act(async () => button(t("spaces.create")).click());
    await flush();
    const post = hub.calls.find((c) => c.method === "POST" && c.path === "/assistant/tracks");
    expect(post?.json).toMatchObject({ slug: "family", kind: "project" });
  });
});
