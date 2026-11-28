import { renderToStaticMarkup } from "react-dom/server";
import { expect, it } from "vitest";
import type { Session } from "../../lib/types";
import { AgentRow } from "./AgentRow";

const session = (o: Partial<Session> = {}): Session => ({ machine_id: "mac", session_id: "s1", title: "Fix the build", cwd: "/code", status: "idle", state: "unread", ...o });

it("shows an agent's latest words on one line without their Markdown (#10939)", () => {
  const html = renderToStaticMarkup(<AgentRow s={session({ last_text: "**Fixed** the `build` step.\n\n- tests pass" })} />);
  expect(html).toContain("Fixed the build step. tests pass");
  expect(html).not.toContain("**");
  expect(html).not.toContain("`");
});

it("draws no second line without latest words", () => {
  expect(renderToStaticMarkup(<AgentRow s={session()} />)).not.toContain("truncate text-[12px]");
});

it("links the attached to-do separately from the session", () => {
  const html = renderToStaticMarkup(<AgentRow s={session({ todo: { id: "task-a", title: "Check the build" } })} />);
  expect(html).toMatch(/href="\/s\/mac\/s1".*?<\/a>.*?href="\/todos\?id=task-a"/s);
  expect(html).toContain("Check the build");
  expect(renderToStaticMarkup(<AgentRow s={session()} />)).not.toContain('href="/todos');
});
