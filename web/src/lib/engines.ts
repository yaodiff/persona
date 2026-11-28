// "Engine" on a machine's page (docs/integrations/engines.md, iOS EngineSelectionModel / EngineSelectionView): which terminal engine the
// computer's sessions come from. The hub keeps one per machine (herdr when never chosen) and takes a new one with
// PUT /api/v1/machines/{mid}/engine, also while the machine is offline (applied when it connects).
import { t } from "../i18n";
import { ENGINE_NAMES, engineName } from "./engine-names";
import type { DetectedEngine, Machine } from "./types";

export { engineName };

const DETAILS: [id: string, detail: string][] = [
  ["herdr", t("lib-engines.detail_herdr")],
  ["herdr-core", t("lib-engines.detail_herdr_core")],
  ["herdr-both", t("lib-engines.detail_herdr_both")],
  ["orca", t("lib-engines.detail_orca")],
  ["tern", t("lib-engines.detail_tern")],
  ["rex", t("lib-engines.detail_rex")],
  ["paseo", t("lib-engines.detail_paseo")],
  ["cindy", t("lib-engines.detail_cindy")],
  ["t3code", t("lib-engines.detail_t3code")],
];

/** The engines this client knows (the hub's proto.EngineNames), in the order they are listed when the node reports none. */
export const KNOWN_ENGINES: { id: string; name: string; detail: string }[] = DETAILS.map(([id, detail]) => ({ id, name: ENGINE_NAMES[id], detail }));

const known = (id: string) => KNOWN_ENGINES.find((e) => e.id === id);

type EngineMachine = Pick<Machine, "engine" | "engines" | "online">;

/** The machine's engine: herdr when it was never chosen (an engine is always picked; nothing is guessed from detection). */
export function currentEngine(m: Pick<Machine, "engine">): string {
  return m.engine || "herdr";
}

/** One line on what the engine is; none for an engine this client does not know. */
export function engineDetail(id: string): string | undefined {
  return known(id)?.detail;
}

/** The choices: what the node reports (names, notes and engines newer than this client), else — an older hub, an
 * offline machine — every known engine, which can still be configured. */
export function engineOptions(m: Pick<Machine, "engines">): DetectedEngine[] {
  if (m.engines?.length) return m.engines;
  return KNOWN_ENGINES.map((e) => ({ id: e.id, name: e.name, available: false }));
}

/** A known engine can always be chosen (offline too, or not detected yet: it applies once there); one this client does
 * not know only while the node reports it available. */
export function canSelectEngine(id: string, m: Pick<Machine, "engines">): boolean {
  return !!known(id) || !!m.engines?.some((e) => e.id === id && e.available);
}

/** What a node found of an engine (proto.EngineState, #17037): only Orca's row says it; "unknown" for a row that does
 * not, or a state this client does not know. */
export type EngineState = "running" | "stopped" | "incompatible" | "missing" | "unknown";

const ENGINE_STATES: readonly string[] = ["running", "stopped", "incompatible", "missing"];

export function engineState(e: Pick<DetectedEngine, "state"> | undefined): EngineState {
  return e?.state && ENGINE_STATES.includes(e.state) ? (e.state as EngineState) : "unknown";
}

/** Orca is in the machine's engine set and its node says Orca is installed and not running: what the Agents list's
 * "Orca isn't running" notice is shown for (an incompatible Orca, a missing one, an older node's row never are). */
export function orcaStopped(m: Pick<Machine, "online" | "engine" | "engine_hosts" | "engine_default" | "engines">): boolean {
  if (!m.online || !currentHosts(m).hosts.includes("orca")) return false;
  return engineState(m.engines?.find((e) => e.id === "orca")) === "stopped";
}

/** What the node found of the engine: "Available" (with its version), "Installed, not running", an incompatible version
 * with the node's note, "Not detected"; nothing to say while offline. */
export function detectionText(id: string, m: EngineMachine): string {
  if (!m.online) return t("lib-engines.detection_offline");
  const found = m.engines?.find((e) => e.id === id);
  if (!found) return t("lib-engines.detection_none");
  const state = engineState(found);
  if (!found.available && state === "stopped")
    return found.version ? t("lib-engines.detection_stopped_version", { version: found.version }) : t("lib-engines.detection_stopped");
  if (!found.available && state === "incompatible" && found.version)
    return t("lib-engines.detection_incompatible_version", { version: found.version });
  if (!found.available) return t("lib-engines.detection_not_found");
  return found.version ? t("lib-engines.detection_available_version", { version: found.version }) : t("lib-engines.detection_available");
}

/** The line under the list: what the choice decides, and when it applies. */
export function engineFooter(selected: string, online: boolean): string {
  if (selected === "herdr-both")
    return online ? t("lib-engines.footer_both") : t("lib-engines.footer_both_offline");
  return online ? t("lib-engines.footer_single") : t("lib-engines.footer_one_offline");
}

/** Whether "Save" is offered: a change the hub can take. */
export function canSaveEngine(selected: string, m: Pick<Machine, "engine" | "engines">): boolean {
  return selected !== currentEngine(m) && canSelectEngine(selected, m);
}

export function engineSaveError(message: string): string {
  return t("lib-engines.save_failed", { message });
}

// ---- Host sets (#2576, docs/integrations/engines.md "Host sets"): several engines enabled side by side plus a default one. ----

/** The hosts a set may hold, in the hub's canonical order (proto.EngineHostIDs); herdr-both is a set, not a member. */
export const ENGINE_HOSTS = ["herdr", "herdr-core", "orca", "tern", "rex", "paseo", "cindy", "t3code"];

export interface HostChoice {
  hosts: string[];
  default: string;
}

/** The set a single engine stands for (proto.EngineHostsOf). */
export function hostsOf(engine: string | undefined): HostChoice {
  if (engine === "herdr-both") return { hosts: ["herdr", "herdr-core"], default: "herdr" };
  if (engine && ENGINE_HOSTS.includes(engine)) return { hosts: [engine], default: engine };
  return { hosts: ["herdr"], default: "herdr" };
}

/** The hub takes sets: it reports engine_hosts (older hubs keep the single-engine picker). */
export function supportsHostSets(m: Pick<Machine, "engine_hosts">): boolean {
  return Array.isArray(m.engine_hosts);
}

/** The machine's set as the hub says, else the one its single engine stands for. */
export function currentHosts(m: Pick<Machine, "engine" | "engine_hosts" | "engine_default">): HostChoice {
  if (Array.isArray(m.engine_hosts)) return { hosts: m.engine_hosts, default: m.engine_default ?? "" };
  return hostsOf(m.engine);
}

/** A set in canonical order; the default stays when still enabled, else the first host ("" for none). */
export function normalizeHosts(hosts: string[], def: string): HostChoice {
  const out = ENGINE_HOSTS.filter((h) => hosts.includes(h));
  return { hosts: out, default: out.includes(def) ? def : (out[0] ?? "") };
}

/** Turns host id on or off in the set. */
export function toggleHost(c: HostChoice, id: string): HostChoice {
  const on = c.hosts.includes(id);
  return normalizeHosts(on ? c.hosts.filter((h) => h !== id) : [...c.hosts, id], c.default);
}

export function sameHosts(a: HostChoice, b: HostChoice): boolean {
  return a.default === b.default && a.hosts.length === b.hosts.length && a.hosts.every((h, i) => h === b.hosts[i]);
}

/** The host options: what the node reports of the set's members, else every one of them (offline, older nodes). */
export function hostOptions(m: Pick<Machine, "engines">): DetectedEngine[] {
  const found = (m.engines ?? []).filter((e) => ENGINE_HOSTS.includes(e.id));
  if (found.length) return ENGINE_HOSTS.map((id) => found.find((e) => e.id === id)).filter((e): e is DetectedEngine => !!e);
  return ENGINE_HOSTS.map((id) => ({ id, name: known(id)?.name ?? id, available: false }));
}

/** The enabled hosts a new session may be opened in (every host can start one, the capability table in
 * docs/integrations/engines.md): offered as a choice only when there is more than one. */
export function startHosts(m: Pick<Machine, "engine" | "engine_hosts" | "engine_default">): string[] {
  return currentHosts(m).hosts;
}

/** The line under the set picker. */
/** A set by its engines' names (herdr + Orca); "no terminal engine" for none. */
export function hostsName(c: HostChoice, m?: Pick<Machine, "engines">): string {
  return c.hosts.length ? c.hosts.map((id) => engineName(id, m)).join(" + ") : t("lib-engines.none");
}

export function hostsFooter(c: HostChoice, online: boolean): string {
  const key = c.hosts.length === 0 ? "footer_set_empty" : c.hosts.length === 1 ? "footer_set_single" : "footer_set_several";
  const text = t(`lib-engines.${key}`, { engine: engineName(c.default) });
  return online ? text : t("lib-engines.footer_set_offline", { text });
}

/** "Also open in X": the other hosts a session is live in, by name; "" when none. */
export function alsoOpenText(s: { also_open_in?: string[] }, m?: Pick<Machine, "engines">): string {
  const names = (s.also_open_in ?? []).map((id) => engineName(id, m));
  return names.length ? t("lib-engines.also_open", { engines: names.join(t("lib-engines.list_separator")) }) : "";
}
