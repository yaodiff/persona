import type { ReactNode } from "react";
import { t } from "../../i18n";
import { currentContextTokens } from "../../lib/context-usage";
import { togglePanel } from "../../lib/panel";
import { countdown, liveAccounts, resetLabel, useUsageWindows, windowKey, type UsageAccount } from "../../lib/usage-windows";
import type { Item, Session } from "../../lib/types";
import { useNow } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { ContextMeter, hasSessionTokenUsage } from "./ContextMeter";

function pathParts(path: string | undefined): string[] {
  return path?.replaceAll("\\", "/").split("/").filter(Boolean) ?? [];
}

export function workingLocation(session: Session) {
  const folder = session.workspace_cwd || session.cwd;
  const repo = session.repo_root || session.worktree;
  if (!repo) {
    const name = pathParts(folder).at(-1) ?? folder;
    return name ? { name, subfolder: "", branch: "", folder } : null;
  }
  const root = pathParts(session.worktree || repo);
  const cwd = pathParts(folder);
  return {
    name: pathParts(repo).at(-1) ?? repo,
    subfolder: root.every((part, index) => cwd[index] === part) ? cwd.slice(root.length).join("/") : "",
    branch: session.branch ?? "",
    folder,
  };
}

export function sessionQuota(accounts: UsageAccount[], session: Session, now: number) {
  // The feed identifies its provider, but not which account a launcher uses. Do not guess between accounts.
  const matches = liveAccounts(accounts, now).filter((account) => account.provider === session.agent && !account.stale);
  const account = matches.length === 1 ? matches[0] : undefined;
  const windows = account?.windows.filter((window) => !window.model) ?? [];
  return windows.length ? { account, windows } : null;
}

export function AgentStatusLine({ session, source, assistant, items, onCompact, compactBlocked }: {
  session: Session;
  source?: ReactNode;
  assistant?: boolean;
  items?: readonly Item[];
  onCompact?: () => boolean | void | Promise<boolean | void | unknown>;
  compactBlocked?: boolean;
}) {
  const isAssistant = Boolean(assistant || session.assistant);
  const place = isAssistant ? null : workingLocation(session);
  const hasContext = currentContextTokens(session) > 0 || hasSessionTokenUsage(items);
  const accounts = useUsageWindows();
  const now = useNow(30_000, !isAssistant && accounts.length > 0);
  const quota = isAssistant ? null : sessionQuota(accounts, session, now);
  const externalTrigger = Boolean(session.botmux_external_trigger);
  if (!place && !hasContext && !quota && !source && !externalTrigger) return null;

  const placeLabel = place && [place.folder || place.name, place.branch].filter(Boolean).join(" · ");
  const placeContent = place && (
    <>
      <Icon name={place.branch ? "git" : "folder"} size={12} className="shrink-0" />
      <span className="min-w-0 truncate">
        {place.name}
        {place.subfolder && <span className="hidden @min-[28rem]/agent-status:inline">/{place.subfolder}</span>}
      </span>
      {place.branch && <span className="min-w-0 truncate">{place.branch}</span>}
    </>
  );
  const placeClass = "flex min-w-0 items-center gap-1 whitespace-nowrap text-submeta text-ink-3";

  return (
    <div data-composer-usage className="@container/agent-status min-w-0 px-2 pt-0.5">
      <div className="flex min-w-0 items-center gap-3">
        {place && (session.repo_root || session.worktree ? (
          <button
            type="button"
            data-composer-source
            className={`touch-hit ${placeClass}`}
            title={placeLabel || undefined}
            aria-label={`${t("chat-chatview.agent_status_repository")}: ${placeLabel}`}
            onClick={() => togglePanel({ kind: "git", mid: session.machine_id, sid: session.session_id, cwd: session.cwd })}
          >
            {placeContent}
          </button>
        ) : (
          <span data-composer-source className={placeClass} title={placeLabel || undefined}>{placeContent}</span>
        ))}
        {isAssistant && source}
        {hasContext && <ContextMeter session={session} items={items} assistant={isAssistant} onCompact={onCompact} compactBlocked={compactBlocked} />}
        {!isAssistant && source}
        {quota && (
          <a
            href="/admin/usage"
            className="touch-hit hidden min-w-0 shrink-0 items-center gap-2 whitespace-nowrap text-submeta text-ink-3 @min-[36rem]/agent-status:flex"
            aria-label={t("usage.title")}
            title={quota.windows.map((window) => [window.label, `${Math.round(window.used_percent)}%`, resetLabel(window.resets_at, now)].filter(Boolean).join(" ")).join(" · ")}
          >
            {quota.windows.slice(0, 2).map((window) => (
              <span key={windowKey(window)} className="tabular-nums">
                {window.label} {Math.round(window.used_percent)}%
                {window.resets_at && ` ↻${countdown(window.resets_at, now)}`}
              </span>
            ))}
          </a>
        )}
      </div>
      {externalTrigger && (
        <div role="status" data-testid="botmux-external-trigger-note" className="truncate pt-0.5 text-submeta text-ink-3">
          {t(session.botmux_external_trigger_reason === "owner_unset" ? "chat-botmux.owner_unset_notice" : "chat-botmux.external_trigger_notice")}
        </div>
      )}
    </div>
  );
}
