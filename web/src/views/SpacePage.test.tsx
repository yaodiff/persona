// The space page (/space/<machine>/<space>) rendered statically: its agents, the todos it hangs on, and a space that is gone.
import { describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import type { Machine, Session } from "../lib/types";
import type { Todo } from "../lib/todos";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
  const kept = new Map<string, string>();
  g.localStorage = { getItem: (k: string) => kept.get(k) ?? null, setItem: (k: string, v: string) => void kept.set(k, v), removeItem: (k: string) => void kept.delete(k) };
});

vi.mock("react-dom", async (importOriginal) => ({
  ...(await importOriginal<typeof import("react-dom")>()),
  createPortal: (children: React.ReactNode) => children,
}));

const fake = vi.hoisted(() => ({ sessions: [] as Session[], machines: [] as Machine[], todos: [] as Todo[] }));

vi.mock("../lib/store", async (importOriginal) => {
  const real = await importOriginal<typeof import("../lib/store")>();
  return {
    ...real,
    useStore: <T,>(select: (s: ReturnType<typeof real.getState>) => T) =>
      select({ ...real.getState(), loaded: true, machines: fake.machines, sessions: { mac: fake.sessions }, todos: fake.todos, todosLoaded: true, todosSupported: true, spacesSupported: true }),
  };
});

const { SpacePage } = await import("./SpacePage");

const mac: Machine = { id: "mac", name: "Mac", online: true };
let n = 0;
const agent = (state: Session["state"], o: Partial<Session> = {}): Session => ({
  machine_id: "mac",
  session_id: `s${++n}`,
  title: `agent ${n}`,
  cwd: "/code/app",
  status: "idle",
  state,
  space_kind: "herdr",
  space_id: "w1",
  space_name: "app",
  slot_id: `w1:t${n}`,
  slot_index: n,
  ...o,
});
const render = (sessions: Session[], todos: Todo[] = [], space = "app") => {
  Object.assign(fake, { sessions, machines: [mac], todos });
  return renderToStaticMarkup(<SpacePage mid="mac" space={space} onToggleSidebar={() => {}} />);
};

describe("the space page", () => {
  it("lists the space's agents with links to their chats, and the machine it is on", () => {
    const a = agent("needs_you", { title: "fix login" });
    const b = agent("idle", { title: "write docs" });
    const html = render([a, b, agent("working", { space_id: "w2", space_name: "lib", title: "other space" })]);
    expect(html).toContain("fix login");
    expect(html).toContain("write docs");
    expect(html).not.toContain("other space");
    expect(html).toContain(`href="/s/mac/${a.session_id}"`);
    expect(html).toContain('href="/machines/mac"');
    expect(html).toContain("1 个需要你");
    expect(html).toContain("新建 Agent");
    expect(html).toContain('aria-label="空间操作"');
  });

  it("shows the todos the space and its agents hang on, each opening the todo, with a concern labelled", () => {
    const todos: Todo[] = [
      { id: "t1", title: "ship space page", kind: "concern", created_at: "", updated_at: "", spaces: [{ machine_id: "mac", space: "app", linked_at: "" }] },
      { id: "t2", title: "agent task", created_at: "", updated_at: "" },
    ];
    const html = render([agent("working", { todo: { id: "t2", title: "agent task" } })], todos);
    expect(html).toContain("ship space page");
    expect(html).toContain("agent task");
    expect(html).toContain('href="/todos?id=t1"');
    expect(html).toContain("空间关联");
    expect(html).toContain("改挂");
    expect(html).toContain('data-testid="todo-concern">关注点<');
    expect(html.match(/data-testid="todo-concern"/g)).toHaveLength(1);
  });

  it("offers to link a todo when the space has none", () => {
    const html = render([agent("idle")]);
    expect(html).toContain("这个空间还没有关联待办。");
    expect(html).toContain("关联到待办");
  });

  it("says so when the space is gone, with a way back to the machine", () => {
    const html = render([agent("idle")], [], "nope");
    expect(html).toContain("找不到这个空间");
    expect(html).toContain("回到机器");
  });
});
