import { t } from "../i18n";
import type { Machine } from "./types";
import { disconnectExpected, supportsDeployment } from "./machine-connection-state";
export { disconnectExpected, linkSwitchRunning } from "./machine-connection-state";

const PHASES = {
  waiting_idle: { label: t("lib-machine-deploy.waiting_idle"), message: t("lib-machine-deploy.waiting_idle_message") },
  transferring: { label: t("lib-machine-deploy.transferring"), message: t("lib-machine-deploy.transferring_message") },
  draining: { label: t("lib-machine-deploy.draining"), message: t("lib-machine-deploy.draining_message") },
  restarting: { label: t("lib-machine-deploy.restarting"), message: t("lib-machine-deploy.restarting_message") },
  confirming: { label: t("lib-machine-deploy.confirming"), message: t("lib-machine-deploy.confirming_message") },
} satisfies Record<NonNullable<Machine["deploy_phase"]>, { label: string; message: string }>;

/** Older hubs can deploy SSH nodes, but never advertised an update path for linked nodes. */
export function machineDeployment(m: Machine) {
  const linked = m.kind === "link" || m.kind === "service";
  const supported = supportsDeployment(m);
  const phase = m.deploy_phase && PHASES[m.deploy_phase];
  const busy = !!(m.deploying || m.deploy_phase);
  const canDeploy = supported && (!linked || m.online);
  const reason = !supported
    ? m.deploy_reason || (m.kind === "local"
      ? t("lib-machine-deploy.reason_local")
      : linked
        ? t("lib-machine-deploy.reason_linked")
        : t("lib-machine-deploy.reason_unsupported"))
    : !canDeploy ? t("lib-machine-deploy.reason_offline") : "";
  return {
    visible: !!(m.node_mismatch || busy || m.deploy_error),
    busy,
    expectedDisconnect: disconnectExpected(m),
    supported,
    canDeploy: canDeploy && !busy,
    label: busy ? phase?.label || t("lib-machine-deploy.deploying") : t("lib-machine-deploy.redeploy"),
    message: busy ? phase?.message || t("lib-machine-deploy.deploying_message") : m.node_mismatch ? t("lib-machine-deploy.mismatch") : "",
    error: m.deploy_error ? t("lib-machine-deploy.last_failed", { error: m.deploy_error }) : "",
    reason,
  };
}
