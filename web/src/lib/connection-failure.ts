// What a machine that is not online says about why (classifyConnection), for the machine card, its alert and Home.
// Not in node-health.ts: Home is on the first screen and needs only this, while node-health.ts builds its check and
// feature labels with t() as it loads, which would put that module and its words on every first screen.
import { t } from "../i18n";
import { supportsDeployment } from "./machine-connection-state";
import type { Machine } from "./types";

export type ConnectionFailureKind = "auth" | "unreachable" | "timeout" | "version";

export interface ConnectionFailure {
  kind: ConnectionFailureKind;
  title: string;
  /** One line: what to do next. */
  next: string;
}

const AUTH = /permission denied|publickey|authentication|unauthori[sz]ed|invalid[ _]token|forbidden|host key verification|access denied|\b40[13]\b|配对|认证/i;
const VERSION = /version mismatch|protocol version|unknown method|incompatible|不是同一版|版本/i;
const TIMEOUT = /timed out|timeout|deadline exceeded|no pong/i;
const UNREACHABLE = /refused|no route|unreachable|could not resolve|name or service|no such host|network is|connection (reset|closed|lost)|broken pipe|\beof\b|node exited|waiting for|lookup |dial /i;

/** What a machine that is not online says about why, read from the error text the hub keeps, with a next step; null
 * when the text does not say (nothing is guessed). A planned restart (a deployment) is not a failure: the caller
 * leaves those out. Matching order matters: "connection timed out" is a timeout, not "connection …". */
export function classifyConnection(m: Machine): ConnectionFailure | null {
  if (m.online) return null;
  const text = m.error ?? "";
  if (!text) return null;
  const link = m.kind === "link" || m.kind === "service";
  if (AUTH.test(text)) {
    return {
      kind: "auth",
      title: t("node-health.auth_title"),
      next: link ? t("node-health.auth_next_link") : t("node-health.auth_next_ssh"),
    };
  }
  if (VERSION.test(text) || m.node_mismatch) {
    return {
      kind: "version",
      title: t("node-health.version_title"),
      next: supportsDeployment(m) ? t("node-health.version_next_deploy") : t("node-health.version_next_local"),
    };
  }
  if (TIMEOUT.test(text)) {
    return { kind: "timeout", title: t("node-health.timeout_title"), next: t("node-health.timeout_next") };
  }
  if (UNREACHABLE.test(text)) {
    return {
      kind: "unreachable",
      title: t("node-health.unreachable_title"),
      next: link ? t("node-health.unreachable_next_link") : t("node-health.unreachable_next_ssh"),
    };
  }
  return null;
}
