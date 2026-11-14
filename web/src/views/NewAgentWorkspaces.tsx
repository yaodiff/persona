// New Agent's workspaces (#7259): the chosen computer's Orca and Paseo workspaces by project (lib/workspace-start.ts), one
// click to start there. The folder below stays for typing or browsing one.
import type { HostWorkspace } from "../lib/workspace-catalog";
import { startBranch, startKind, startTitle, type StartGroup, type StartKind } from "../lib/workspace-start";
import { shortPath } from "../lib/format";
import { Icon } from "../ui/icons";
import { t } from "../i18n";

const KIND_LABEL: Record<StartKind, string> = { main: "newagent-workspace.kind_main", worktree: "newagent-workspace.kind_worktree", folder: "newagent-workspace.kind_folder" };

/** "Worktree · fix-login": the kind word, with the branch when the name does not already say it. */
export function kindLine(w: HostWorkspace): string {
  const kind = t(KIND_LABEL[startKind(w)]);
  const branch = startBranch(w);
  return branch ? `${kind} · ${branch}` : kind;
}

export function WorkspaceChoices({ groups, onPick }: { groups: StartGroup[]; onPick: (w: HostWorkspace) => void }) {
  return (
    <div data-testid="new-agent-workspaces" className="max-h-52 overflow-y-auto rounded-control bg-surface p-1 shadow-hairline">
      {groups.map((g) => {
        const heading = g.title || t("newagent-workspace.other");
        return (
          <div key={g.key} role="group" aria-label={heading}>
            <div className="truncate px-2 pt-1.5 pb-0.5 text-label font-medium text-ink-3">{heading}</div>
            {g.workspaces.map((w) => (
              <button
                key={`${w.source}:${w.source_id}`}
                type="button"
                data-workspace-choice
                onClick={() => onPick(w)}
                title={w.cwd}
                className="flex min-h-8 w-full items-center gap-2 rounded-[6px] px-2 py-1 text-left transition-colors hover:bg-hover pointer-coarse:min-h-11"
              >
                <Icon name={startKind(w) === "folder" ? "folder" : "git"} size={13} className="shrink-0 text-ink-3" />
                <span className="min-w-0 truncate text-meta text-ink">{startTitle(w)}</span>
                <span className="shrink-0 text-label text-ink-3">{kindLine(w)}</span>
                <span className="ml-auto min-w-0 flex-1 truncate text-right font-mono text-label text-ink-3 max-sm:hidden">{shortPath(w.cwd)}</span>
              </button>
            ))}
          </div>
        );
      })}
    </div>
  );
}
