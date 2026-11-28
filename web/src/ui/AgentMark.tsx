// The mark of the agent that runs in a row, a header, a pane or a launcher (#3391): drawn as the owner publishes it, like
// BrandMark (CLAUDE.md: brand marks are not redrawn), in a fixed box so a column of rows lines up, centred, at the share
// of the box its shape takes. Next to the state dot, never in its place. The one-color marks are templates drawn in
// the text color, so they read in light and dark and in a selected button; Claude keeps its coral (Anthropic's mark is
// coral on neutral). An agent with no mark of its own (a compatible CLI, a runtime, whatever else herdr reports) draws its first
// letter on a plain tile of the same box.
//
// Where each mark comes from (also in THIRD_PARTY_NOTICES.md). Simple Icons (SI) releases the files under CC0; the
// marks stay their owners' trademarks and are shown only to say which agent a row is, or which product a personal
// Agents account drives where the provider is that product's own agent (views/agents/account-groups.tsx, #16248: Claude
// Duet and Gemini Tasks); other products draw their own letter.
//   claude    SI 16.36.0 `claude.svg` (source claude.ai), coral #D97757 as in apple/…/Brands/brand-claude
//   codex     OpenAI Blossom, github.com/openai/openai-cookbook `chatgpt-blossom-black.svg`, as apple/…/Brands/brand-openai
//   opencode  SI `opencode.svg`; source github.com/anomalyco/opencode packages/identity/mark.svg (the project is MIT)
//   cursor    SI `cursor.svg`; source cursor.com/brand
//   gemini    SI `googlegemini.svg`; source gemini.google.com (no brand kit found)
//   pi        SI `pi.svg`; source pi.dev/favicon.svg (no licence text found)
//   trae      SI `trae.svg`; source trae.ai (TRAE artwork, used for TRAE and TRAE CLI)
//   grok, agy  Lobe Icons 1.104.0 Grok and Antigravity monochrome marks
//   dsh, kimi, copilot  SI 16.36.0 DeepSeek, Kimi and GitHub Copilot marks
// viewBoxes of opencode, cursor and trae are cropped to the glyph (CLAUDE.md: assets carry no padding); nothing else is changed.
import { t } from "../i18n";
import { agentLetter, agentName, isMarked, markId, sessionAgent, type AgentSurface, type MarkedAgent } from "../lib/agent-kind";
import { cx } from "./atoms";
import claudeMark from "./brands/claude.svg";
import codexMark from "./brands/codex.svg";
import cursorMark from "./brands/cursor.svg?no-inline";
import geminiMark from "./brands/gemini.svg";
import opencodeMark from "./brands/opencode.svg";
import piMark from "./brands/pi.svg";
import traeMark from "./brands/trae.svg";
import grokMark from "./brands/grok.svg?no-inline";
import agyMark from "./brands/agy.svg?no-inline";
import dshMark from "./brands/dsh.svg?no-inline";
import kimiMark from "./brands/kimi.svg?no-inline";
import copilotMark from "./brands/copilot.svg?no-inline";

// `fit`: the share of the box the mark is drawn at. Round and full-bleed shapes fill it; a square or a tall one is
// pulled in so it does not look bigger than its neighbours (iOS: `Theme.Brand.optical`).
const MARKS: Record<MarkedAgent, { src: string; template?: boolean; fit: number }> = {
  claude: { src: claudeMark, fit: 1 },
  codex: { src: codexMark, template: true, fit: 1 },
  opencode: { src: opencodeMark, template: true, fit: 0.88 },
  pi: { src: piMark, template: true, fit: 0.88 },
  gemini: { src: geminiMark, template: true, fit: 1 },
  cursor: { src: cursorMark, template: true, fit: 0.92 },
  trae: { src: traeMark, template: true, fit: 0.92 },
  grok: { src: grokMark, template: true, fit: 1 },
  agy: { src: agyMark, template: true, fit: 1 },
  dsh: { src: dshMark, template: true, fit: 1 },
  kimi: { src: kimiMark, template: true, fit: 0.88 },
  copilot: { src: copilotMark, template: true, fit: 1 },
};

/**
 * `agent` is what the node reports ("" is Claude Code); `label` the runtime's own name for an agent the app does not
 * know, whose mark is drawn when it names an agent that has one (a Paseo provider named "Cursor", markId); `surface`
 * the client it runs in (#6390): the command line's mark carries a </> badge on its lower right, the desktop app's
 * none. `size` is the box. The mark says what it is (role=img, "Claude Code, command line"); `decorative` for a place
 * that names the agent in text right beside it.
 */
export function AgentMark({ agent, label, surface, size = 14, decorative, className, remote }: { agent?: string; label?: string; surface?: AgentSurface; size?: number; decorative?: boolean; className?: string; /** It runs on another computer: a signal arc at the mark's upper right (RemoteSignal). */ remote?: boolean }) {
  const id = markId(agent, label);
  const agentNamed = agentName(agent, label);
  const name = markName(id, agent, label, surface);
  const a11y = decorative ? { "aria-hidden": true as const } : { role: "img" as const, "aria-label": name };
  const box = cx("relative inline-flex shrink-0 items-center justify-center", className ?? "text-ink-2");
  const badge = (
    <>
      {surface === "cli" && <CliBadge size={size} />}
      {remote && <RemoteSignal size={size} />}
    </>
  );
  if (!isMarked(id)) {
    return (
      <span {...a11y} data-agent-mark="letter" data-agent-surface={surface} className={box} style={{ width: size, height: size }}>
        <span
          aria-hidden
          className="flex items-center justify-center bg-inset font-semibold leading-none"
          style={{ width: Math.round(size * 0.92), height: Math.round(size * 0.92), borderRadius: Math.max(3, Math.round(size * 0.28)), fontSize: Math.max(8, Math.round(size * 0.6)) }}
        >
          {agentLetter(agentNamed)}
        </span>
        {badge}
      </span>
    );
  }
  const { src, template, fit } = MARKS[id];
  const px = Math.round(size * fit);
  const mask = `url("${src}") center / contain no-repeat`;
  return (
    <span {...a11y} data-agent-mark={id} data-agent-surface={surface} className={box} style={{ width: size, height: size }}>
      {template ? (
        <span aria-hidden className="block bg-current" style={{ width: px, height: px, mask, WebkitMask: mask }} />
      ) : (
        <img src={src} alt="" aria-hidden width={px} height={px} style={{ width: px, height: px }} />
      )}
      {badge}
    </span>
  );
}

/**
 * The command line's badge (#6390): </> on a small tile at the mark's lower right, the page's colour around it so it
 * reads on any row (selected, hovered). The tile is about 60% of the mark, the glyph drawn as strokes so it stays sharp
 * down to the 13 px launcher marks.
 */
function CliBadge({ size }: { size: number }) {
  const side = Math.max(8, Math.round(size * 0.62));
  return (
    <span
      aria-hidden
      data-agent-badge="cli"
      className="absolute flex items-center justify-center rounded-[3px] bg-ink text-page ring-1 ring-page"
      style={{ width: side, height: side, right: -Math.round(side * 0.35), bottom: -Math.round(side * 0.3) }}
    >
      <svg viewBox="0 0 12 12" width={side - 2} height={side - 2} fill="none" stroke="currentColor" strokeWidth={1.6} strokeLinecap="round" strokeLinejoin="round">
        <path d="M3.6 3.6 1.4 6l2.2 2.4M8.4 3.6 10.6 6 8.4 8.4M6.9 2.6 5.1 9.4" />
      </svg>
    </span>
  );
}

/**
 * "On another computer" (#5421, cindy's RemoteSignalOverlay, the same mark its header and sidebar draw for an Agent that
 * runs elsewhere): one arc and a dot off the mark's upper right corner, in the mark's text color. It overhangs the box
 * without taking room, so a mark with it lines up with one without. Drawn on a 16-unit canvas whose lower-left 12.5 units
 * are the mark's box.
 */
function RemoteSignal({ size }: { size: number }) {
  const unit = size / 12.5;
  const band = 3.5 * unit;
  return (
    <svg
      aria-hidden
      data-agent-remote
      viewBox="0 0 16 16"
      width={16 * unit}
      height={16 * unit}
      fill="none"
      stroke="currentColor"
      strokeWidth={1.4}
      strokeLinecap="round"
      className="pointer-events-none absolute"
      style={{ right: 0.1 * size - band, top: 0.1 * size - band }}
    >
      <path d="M12 1.4A2.6 2.6 0 0 1 14.6 4" />
      <circle cx="12.6" cy="3.4" r="0.95" fill="currentColor" stroke="none" />
    </svg>
  );
}

/** What a mark says it is: the agent's name, with the client it runs in ("Codex, command line"). */
function markName(id: string, agent?: string, label?: string, surface?: AgentSurface): string {
  const fallback = isMarked(id) ? t(`agent-mark.${id}`) : undefined;
  const named = agentName(agent, label?.trim() || fallback);
  return surface ? t("agent-mark.with_surface", { agent: named, surface: t(`agent-mark.surface_${surface}`) }) : named;
}

/** What a session's mark says it is (SessionMark's accessible name), for a place that names it along with more. */
export function sessionMarkName(s: Parameters<typeof sessionAgent>[0]): string {
  const { agent, label, surface } = sessionAgent(s);
  return markName(markId(agent, label), agent, label, surface);
}

/** The mark of a session's agent. */
export function SessionMark({ s, ...rest }: { s: Parameters<typeof sessionAgent>[0] } & Omit<Parameters<typeof AgentMark>[0], "agent" | "label" | "surface">) {
  return <AgentMark {...sessionAgent(s)} {...rest} />;
}
