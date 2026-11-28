// The four dimensions under a form that picks a launcher, a model and an effort (New Agent, a preset's candidate):
// the combination is checked on the computer (POST /execution/validate) — nothing shown when it is allowed, the hub's
// words when it runs with a notice (an experimental bridge marked), its reason in red and the form's confirm disabled
// when it is blocked, each alternative a one-tap replacement. "Engine and channel" folds out the route: the engines and
// channels narrow one another, and the pick goes back as the launcher alias the hub resolved (old clients keep working).
import { useEffect, useState } from "react";
import { t } from "../../i18n";
import {
  aliasFor, aliasSpec, channelLabel, cleanSpec, pickOfSpec, reportBlocked, runtimeLabel, verdictBlocks,
  type ExecutionCatalog, type ExecutionSpec, type SpecPick,
} from "../../lib/execution";
import { Button } from "../../ui/atoms";
import { useExecutionCatalog, useVerdict } from "./hooks";
import { RouteFields } from "./RouteFields";
import { VerdictNote } from "./VerdictNote";

export interface ExecutionCheckProps {
  machine: string;
  launcher: string;
  model?: string;
  effort?: string;
  /** The form's own runtime and channel, when it keeps them (a preset's candidate). */
  runtime?: string;
  channel?: string;
  /** The form whose confirm a blocked combination disables (lib/execution.ts reportBlocked), and this part's key in it. */
  scope?: string;
  part?: string;
  /** The verdict's level changed: blocked or not. */
  onBlocked?: (blocked: boolean) => void;
  /** An alternative or a route was taken: the form applies the pick (launcher alias, model, effort, runtime, channel). */
  onPick: (pick: SpecPick) => void;
  disabled?: boolean;
  /** Controlled catalog (tests, a parent that read it). */
  catalog?: ExecutionCatalog | null;
}

export function ExecutionCheck({ machine, launcher, model, effort, runtime, channel, scope, part, onBlocked, onPick, disabled, catalog: controlled }: ExecutionCheckProps) {
  const { catalog } = useExecutionCatalog(machine || undefined, true, controlled);
  const alias = aliasSpec(catalog, launcher);
  // what the form runs: its own runtime and channel, else its launcher's; nothing to check for a launcher the hub
  // does not know as an alias (an older hub, or no launcher chosen and no route either)
  const known = Boolean(catalog && (runtime || channel || alias));
  const spec: ExecutionSpec | null = known
    ? cleanSpec({ runtime: runtime || alias?.runtime, channel: channel || alias?.channel, model: model || alias?.model, effort: effort || alias?.effort })
    : null;
  const { verdict } = useVerdict(spec, machine);
  const blocked = verdictBlocks(verdict);
  const [open, setOpen] = useState(false);
  const [route, setRoute] = useState<ExecutionSpec>({});
  const { verdict: routeVerdict } = useVerdict(open && (route.runtime || route.channel) ? cleanSpec({ ...route, model, effort }) : null, machine);

  useEffect(() => {
    onBlocked?.(blocked);
    if (scope && part) reportBlocked(scope, part, blocked);
    // eslint-disable-next-line react-hooks/exhaustive-deps -- report each change of the level
  }, [blocked, scope, part]);
  useEffect(() => () => {
    if (scope && part) reportBlocked(scope, part, false);
  }, [scope, part]);

  if (!catalog || !known && !open) {
    return catalog && catalog.runtimes.length > 0 ? <RouteToggle open={open} onToggle={() => setOpen((o) => !o)} disabled={disabled} /> : null;
  }
  const take = (alt: ExecutionSpec, launcherOf?: string) => onPick(pickOfSpec(alt, launcherOf || aliasFor(catalog, alt), launcher));
  const current = spec ?? {};
  const routeText = [runtimeLabel(current.runtime), channelLabel(catalog, current.channel)].filter(Boolean).join(" · ");
  return (
    <div data-execution-check="" className="flex flex-col gap-2">
      <VerdictNote verdict={verdict} catalog={catalog} busy={disabled} onReplace={(alt) => take({ ...alt, model: alt.model ?? model, effort: alt.effort ?? effort })} />
      <div className="flex flex-wrap items-center gap-2">
        <RouteToggle open={open} onToggle={() => (setRoute(cleanSpec({ runtime: current.runtime, channel: current.channel })), setOpen((o) => !o))} disabled={disabled} />
        {!open && routeText && <span className="min-w-0 truncate text-submeta text-ink-3">{routeText}</span>}
      </div>
      {open && (
        <div data-execution-route="" className="flex flex-col gap-2 rounded-control border border-line p-2.5">
          <RouteFields catalog={catalog} machine={machine} spec={route} onChange={setRoute} dimensions={["runtime", "channel"]} disabled={disabled} />
          <VerdictNote verdict={routeVerdict} catalog={catalog} busy={disabled} onReplace={(alt) => setRoute(cleanSpec({ runtime: alt.runtime, channel: alt.channel }))} />
          <Button
            size="sm"
            tone="secondary"
            className="self-start"
            testId="execution-route-use"
            disabled={disabled || !routeVerdict || verdictBlocks(routeVerdict)}
            onClick={() => {
              if (!routeVerdict) return;
              take({ ...routeVerdict.spec, model: model || undefined, effort: effort || routeVerdict.spec.effort }, routeVerdict.launcher);
              setOpen(false);
            }}
          >
            {t("execution.route_use")}
          </Button>
        </div>
      )}
    </div>
  );
}

function RouteToggle({ open, onToggle, disabled }: { open: boolean; onToggle: () => void; disabled?: boolean }) {
  return (
    <button type="button" data-testid="execution-route-toggle" aria-expanded={open} disabled={disabled} onClick={onToggle} className="touch-hit self-start text-label font-medium text-accent-ink hover:underline disabled:opacity-60">
      {open ? t("execution.route_hide") : t("execution.route_show")}
    </button>
  );
}
