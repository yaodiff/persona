import type { NewAgentPreset } from "./new-agent";
import type { BoardCard } from "./workspace-board";

export function boardAgentPreset(card: Pick<BoardCard, "machine_id" | "cwd" | "source" | "source_id">): NewAgentPreset {
  const launcher = card.source === "codex_app" ? "codex-app" : card.source === "claude_desktop" ? "claude-desktop" : undefined;
  return { machineId: card.machine_id, cwd: card.cwd,
    ...(launcher ? { launcher, ...(card.source_id ? { desktopProjectId: card.source_id } : {}) } : {}) };
}
