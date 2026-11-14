import { t } from "../i18n";

export interface MainExecution {
  session_id?: string;
  /** Routing node, never the execution location. */
  machine_id?: string;
  proxy?: string;
  state: "unknown" | "starting" | "running" | "stopped" | "offline";
  source?: "native" | "runtime";
  harness?: string;
  harness_label?: string;
  location?: { host?: string; container?: string };
}

function harnessOf(report: MainExecution): string | undefined {
  switch (report.harness) {
    case "claude": return "Claude Code";
    case "agentsdk": return "Claude Agent SDK";
    case "codex": return "Codex";
    default: return report.harness_label || report.harness;
  }
}

/** The main chat's line on the Assistant screen: its name and a plain state ("Main chat · Running", "· Offline", "· Stopped",
 *  "· Starting…"), or "" when nothing about it is known (an older hub, a failed read, a report for another conversation,
 *  the `unknown` state): the line is left out then, rather than telling an everyday user what was not reported. Where and
 *  with what it runs (a host, a container, a proxy; Claude Code, Codex) is engineering detail the everyday screen leaves
 *  to Settings › Assistant's Runs on group (`mainRuntimeDetails`; super rules 0 and 4, web usability audit item 80). */
export function mainRuntimeSummary(main: string | undefined, report?: MainExecution, starting = false): string {
  if (!main) return t(starting ? "main-runtime.starting" : "main-runtime.not_started");
  if (report?.session_id !== main) return "";
  switch (report.state) {
    case "starting": return t("main-runtime.starting");
    case "running": return `${t("main-runtime.main")} · ${t("main-runtime.running")}`;
    case "offline": return t("main-runtime.offline");
    case "stopped": return t("main-runtime.stopped");
    default: return "";
  }
}

/** Where and with what the main chat runs, for Settings › Assistant's Runs on group; each part only when the report says
 *  it. `place`: the execution side's container or host (the last one seen while it is stopped or offline), never the
 *  routing node; a proxy without a location is named as the way in. `runtime`: Claude Code, Claude Agent SDK, Codex or
 *  what the runtime calls itself. */
export function mainRuntimeDetails(main: string | undefined, report?: MainExecution): { place?: string; runtime?: string } {
  if (!main || report?.session_id !== main) return {};
  const location = report.source && (report.location?.container
    ? t("main-runtime.container", { name: report.location.container })
    : report.location?.host);
  const gone = report.state === "offline" || report.state === "stopped";
  const place = location
    ? (gone ? t("main-runtime.last_location", { location }) : location)
    : report.source === "runtime" && report.proxy ? t("main-runtime.proxy", { name: report.proxy }) : undefined;
  return { place: place || undefined, runtime: harnessOf(report) || undefined };
}
