// A computer's alert sheet (#8037, docs/web/web.md "Machines"): opened from a chip on its card, on that alert. One
// Dialog (a centred card on a desktop, a sheet from the bottom on a phone) with the computer's alerts as a row of
// tabs at the top and, for the one shown, three parts in a fixed order: what is wrong, why, what you can do (the
// sections in Alert*.tsx). "Hand work to another computer" is a second step inside it. Rendered once by the page,
// outside every card, so a click inside never reaches a card's own "open the computer".
import { useEffect, useState } from "react";
import { t } from "../../i18n";
import { alertTitle, offlineReason, type AlertKind, type LauncherHealthRecord, type MachineAlert } from "../../lib/machine-alerts";
import { machineDeployment } from "../../lib/machine-deploy";
import type { Machine, MachineMetrics, Session } from "../../lib/types";
import { Button, Dialog } from "../../ui/atoms";
import { AlertChip } from "./MachineAlertChips";
import { DeploySection } from "./AlertDeploy";
import { HandoffStep } from "./AlertHandoff";
import { LauncherSection } from "./AlertLauncher";
import { OfflineSection } from "./AlertOffline";
import { PressureSection } from "./AlertPressure";

export interface AlertSheetProps {
  machine: Machine | undefined;
  kind: Exclude<AlertKind, "disk">;
  alerts: readonly MachineAlert[];
  metrics?: MachineMetrics;
  /** The computer's live Agents (not the assistant's conversations). */
  agents: readonly Session[];
  agentsKnown: "yes" | "loading" | "failed";
  machines: readonly Machine[];
  allMetrics: readonly MachineMetrics[] | null;
  records: readonly LauncherHealthRecord[];
  launcherIssues: (mid: string) => number;
  onKind: (kind: AlertKind) => void;
  onClose: () => void;
  onReconnect: (id: string) => Promise<void> | void;
  onRecords: (records: LauncherHealthRecord[]) => void;
  /** Reads every launcher health record again (a check that answered no record for its launcher, #14534). */
  onReloadRecords?: () => void;
}

export function MachineAlertSheet(p: AlertSheetProps) {
  const { machine: m, kind } = p;
  const [handoff, setHandoff] = useState(false);
  // another computer or another alert: back to the alert itself
  useEffect(() => setHandoff(false), [m?.id, kind]);
  if (!m) return <Dialog open={false} onClose={p.onClose}>{null}</Dialog>;
  // the alert shown stays a tab after it clears (a redeploy that worked, a reconnect), so its outcome stays in view
  const tabs: MachineAlert[] = p.alerts.some((a) => a.kind === kind) ? [...p.alerts] : [...p.alerts, { kind, severity: "info" }];
  const deployment = machineDeployment(m);
  const redeploy = deployment.supported ? (
    <Button size="sm" tone="secondary" icon="retry" disabled={!deployment.canDeploy} onClick={() => p.onKind("mismatch")}>
      {t("lib-machine-deploy.redeploy")}
    </Button>
  ) : undefined;
  return (
    <Dialog open onClose={p.onClose} width={600} title={<span data-testid="alert-sheet-title">{t("machines-alerts.sheet_title", { machine: m.name })}</span>}>
      <div data-testid="alert-sheet" data-kind={kind} data-machine={m.id}>
        {!handoff && tabs.length > 1 && (
          <div className="-mx-1 mb-2 flex flex-wrap gap-1.5 px-1" role="group" aria-label={t("machines-alerts.tabs_label")}>
            {tabs.map((a) => (
              <AlertChip key={a.kind} alert={a} selected={a.kind === kind} onOpen={p.onKind} />
            ))}
          </div>
        )}
        {!handoff && tabs.length <= 1 && <p className="mb-1 text-meta font-medium text-ink">{alertTitle({ kind })}</p>}
        {handoff ? (
          <HandoffStep source={m} agents={p.agents} machines={p.machines} metrics={p.allMetrics} launcherIssues={p.launcherIssues} onBack={() => setHandoff(false)} onDone={p.onClose} />
        ) : kind === "memory" || kind === "cpu" || kind === "load" ? (
          <PressureSection kind={kind} machine={m} metrics={p.metrics} agents={p.agents} agentsKnown={p.agentsKnown} redeploy={redeploy} onHandoff={() => setHandoff(true)} />
        ) : kind === "mismatch" || kind === "deploy_failed" ? (
          <DeploySection machine={m} agents={p.agents} />
        ) : kind === "offline" ? (
          <OfflineSection machine={m} reason={offlineReason(m)} onReconnect={p.onReconnect} onRedeploy={() => p.onKind("mismatch")} />
        ) : (
          <LauncherSection machine={m} records={p.records} onRecords={p.onRecords} onReload={p.onReloadRecords} />
        )}
      </div>
    </Dialog>
  );
}
