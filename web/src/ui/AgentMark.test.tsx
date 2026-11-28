import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { MARKED_AGENTS } from "../lib/agent-kind";
import { AgentMark, SessionMark } from "./AgentMark";

const draw = (agent: string | undefined, props: Partial<Parameters<typeof AgentMark>[0]> = {}) => renderToStaticMarkup(<AgentMark agent={agent} size={14} {...props} />);
// the image's address: an <img src> or a mask's url() (Vite inlines files this small as data URIs, so it is the whole file)
const src = (html: string) => /(?:src="|url\(&quot;)(.+?)(?:"|&quot;)/.exec(html)?.[1];

describe("AgentMark", () => {
  it("draws each known agent's own image, no two alike", () => {
    const sources = MARKED_AGENTS.map((a) => src(draw(a)));
    expect(sources.every(Boolean)).toBe(true);
    expect(new Set(sources).size).toBe(MARKED_AGENTS.length);
    expect(draw("")).toBe(draw("claude")); // Claude Code reports nothing
    expect(draw("gemini-cli")).toBe(draw("gemini"));
  });

  it("draws the one-color marks as templates in the text color and Claude in its own color", () => {
    for (const a of MARKED_AGENTS.filter((x) => x !== "claude")) {
      const html = draw(a);
      expect(html).toContain("bg-current");
      expect(html).toContain("mask");
      expect(html).not.toContain("<img");
    }
    expect(draw("claude")).toContain("<img");
    expect(draw("codex")).toContain("text-ink-2"); // read in light and dark: the text color, not a fixed one
    expect(draw("codex", { className: "text-current" })).not.toContain("text-ink-2");
  });

  it("says which agent it is, in the interface language", () => {
    expect(draw("claude")).toContain('role="img"');
    expect(draw("claude")).toContain('aria-label="Claude Code"');
    expect(draw("opencode")).toContain('aria-label="opencode"');
    expect(draw("cursor-agent")).toContain('aria-label="Cursor"');
  });

  it("is only decoration where the text beside it names the agent", () => {
    const html = draw("codex", { decorative: true });
    expect(html).toContain('aria-hidden="true"');
    expect(html).not.toContain("aria-label");
    expect(html).not.toContain("role=");
  });

  it("draws a letter for an agent with no mark, in the same box", () => {
    for (const [agent, label, letter] of [["omp", "Oh My Pi", "O"], ["amp", undefined, "A"]] as const) {
      const html = draw(agent, { label });
      expect(html).toContain('data-agent-mark="letter"');
      expect(html).toContain(`>${letter}</span>`);
      expect(html).not.toContain("<img");
      expect(html).toContain("width:14px;height:14px");
    }
    expect(draw("omp")).toContain('aria-label="Oh My Pi"');
  });

  it("draws the TRAE mark for built-in traex with the TRAE CLI accessible label", () => {
    const html = draw("traex");
    expect(html).toContain('data-agent-mark="trae"');
    expect(html).toContain('aria-label="TRAE CLI"');
    expect(html).not.toContain('data-agent-mark="letter"');
  });

  it("keeps every mark in a box of the size asked, centred, whatever its shape", () => {
    for (const a of [...MARKED_AGENTS, "traex"]) {
      const html = draw(a, { size: 18 });
      expect(html).toMatch(/^<span [^>]*class="relative inline-flex shrink-0 items-center justify-center/);
      expect(html).toContain("width:18px;height:18px");
    }
  });

  it("pulls a square or tall mark in so it does not look bigger than a round one", () => {
    const inner = (a: string) => Number(/width:(\d+)px/.exec(draw(a, { size: 32 }).replace("width:32px;height:32px", ""))?.[1]);
    expect(MARKED_AGENTS.map((a) => inner(a === "claude" ? "codex" : a))).toEqual([32, 32, 28, 28, 32, 29, 29, 32, 32, 32, 28, 32]);
  });
});

describe("AgentMark's client (#6390)", () => {
  it("badges the command line's mark with </>, and says so", () => {
    for (const a of ["claude", "codex", "traex"]) {
      const html = draw(a, { surface: "cli" });
      expect(html).toContain('data-agent-badge="cli"');
      expect(html).toContain('data-agent-surface="cli"');
    }
    expect(draw("claude", { surface: "cli" })).toContain('aria-label="Claude Code（命令行版）"'); // tests run in zh-Hans
    expect(draw("codex", { surface: "desktop" })).toContain('aria-label="Codex（桌面版）"');
  });

  it("draws the desktop app's mark and an unknown client's as before, with no badge", () => {
    expect(draw("codex", { surface: "desktop" })).not.toContain("data-agent-badge");
    expect(draw("codex")).not.toContain("data-agent-badge");
    expect(draw("codex")).toContain('aria-label="Codex"');
  });

  it("keeps the badge inside a readable size on the smallest marks", () => {
    const side = (size: number) => Number(/data-agent-badge="cli"[^>]*width:(\d+)px/.exec(draw("codex", { surface: "cli", size }))?.[1]);
    expect(side(13)).toBe(8);
    expect(side(16)).toBe(10);
  });
});

describe("SessionMark", () => {
  it("badges a session whose agent runs on the command line", () => {
    expect(renderToStaticMarkup(<SessionMark s={{ agent: "codex", agent_surface: "cli" }} size={14} />)).toBe(draw("codex", { surface: "cli" }));
    expect(renderToStaticMarkup(<SessionMark s={{ agent: "codex", agent_surface: "web" }} size={14} />)).toBe(draw("codex"));
  });

  it("is the mark of the session's agent", () => {
    const html = renderToStaticMarkup(<SessionMark s={{ agent: "opencode" }} size={14} />);
    expect(html).toBe(draw("opencode"));
    expect(renderToStaticMarkup(<SessionMark s={{ agent: "claude", source_kind: "codex_history" }} size={14} />)).toBe(draw("codex"));
    expect(renderToStaticMarkup(<SessionMark s={{ agent: "dsh", agent_label: "DSH" }} size={14} />)).toContain('aria-label="DSH"');
  });

  it("draws one of Paseo's providers by its provider: the mark the app has for it, else its label's letter (#8142)", () => {
    const paseo = (launcher: string, agent_label: string) => renderToStaticMarkup(<SessionMark s={{ agent: "paseo", source_kind: "runtime", launcher, agent_label }} size={14} />);
    expect(src(paseo("cursor", "Cursor"))).toBe(src(draw("cursor")));
    expect(src(paseo("traecli", "TRAE CLI"))).toBe(src(draw("trae")));
    expect(src(paseo("opencode", "OpenCode"))).toBe(src(draw("opencode")));
    expect(src(paseo("pi", "Pi"))).toBe(src(draw("pi")));
    // a provider id of the user's own, with a label naming a known agent
    expect(src(paseo("my-gemini", "Gemini"))).toBe(src(draw("gemini")));
    const grok = paseo("grok", "Grok");
    expect(grok).toContain('data-agent-mark="grok"');
    expect(grok).toContain('aria-label="Grok"');
    expect(grok).not.toMatch(/paseo/i);
  });
});

describe("AgentMark's label (#8142)", () => {
  it("draws the mark its label names for an agent with none of its own, and keeps an agent's own mark", () => {
    expect(src(draw("paseo", { label: "Cursor" }))).toBe(src(draw("cursor")));
    expect(src(draw("paseo", { label: "Grok · Official" }))).toBe(src(draw("grok")));
    expect(src(draw("codex", { label: "Cursor" }))).toBe(src(draw("codex")));
  });
});
