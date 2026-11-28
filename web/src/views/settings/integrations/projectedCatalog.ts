import { t } from "../../../i18n";
import type { IntegrationAvailability, IntegrationCatalogItem, IntegrationKind, IntegrationSourceAvailability } from "../../../lib/integrations-projection-wire";
import { subscriptionSourceState } from "../../../lib/subscriptions";
import type { IntegrationView } from "../integrations-route";
import type { IntegrationItem, IntegrationTone } from "./catalog";

export const capabilityLabel = {
  channels: "settings-integrations.cap_channels",
  data: "settings-integrations.cap_data",
  tools: "settings-integrations.cap_tools",
};

export function projectionStatus({ state, reason }: IntegrationAvailability): { text: string; tone: IntegrationTone } {
  switch (state) {
    case "available": return { text: t("settings-integrations.projection_available"), tone: "green" };
    case "needs_setup": return { text: t("settings-integrations.status_not_configured"), tone: "orange" };
    case "disabled": return { text: t("settings-integrations.status_off"), tone: "idle" };
    case "module_off": return { text: t("settings-integrations.projection_module_off"), tone: "idle" };
    case "read_only": return { text: t("settings-integrations.projection_read_only"), tone: "idle" };
  }
  switch (reason) {
    case "rate_limited": return { text: t("settings-integrations.sub_alert_rate_limited"), tone: "orange" };
    case "not_connected": return { text: t("settings-integrations.sub_alert_not_connected"), tone: "orange" };
    case "runtime_error": return { text: t("settings-integrations.status_error"), tone: "red" };
    default: return { text: t("settings-extensions.unavailable"), tone: "red" };
  }
}

const sourceLabels = {
  extensions: "settings-extensions.title",
  channels: "settings-channels.title",
  connectors: "settings-integrations.filter_data",
  subscriptions: "subscriptions.row",
  service: "settings-integrations.group_builtin",
};

export function projectionSourceErrors(sources: readonly IntegrationSourceAvailability[]): string[] {
  return sources.flatMap(({ source, ref, availability }) => {
    if (availability.reason !== "read_failed") return [];
    if (source === "service" && ref?.id === "mail") return [t("settings-connectors.mail")];
    if (source === "service" && ref?.id === "feishu-bot") return [t("settings-feishubot.title")];
    return [t(sourceLabels[source])];
  });
}

/** Keeps the existing row layout and translates the typed server resource to a Settings address. */
export function projectedCatalogItem(item: IntegrationCatalogItem): IntegrationItem {
  const { ref, origin, name, availability, capabilities, capability_availability } = item;
  const { id, manager } = ref;
  const kind = ref.kind;
  const view: IntegrationView = kind === "push_source" || kind === "subscription" ? { kind: "source", state: subscriptionSourceState(id) }
    : { kind: kind === "service" ? "builtin" : kind, id };
  const sourceState = view.kind === "source" ? view.state : undefined;
  const status = projectionStatus(availability);
  const subjectProblems = capability_availability?.filter((cap) => cap.availability.state !== "available") ?? [];
  const subtitle = subjectProblems.map(({ capability, availability }) => t("settings-integrations.projection_capability_status", {
    capability: t(capabilityLabel[capability]), status: projectionStatus(availability).text,
  })).join(" · ");
  const failedCapability = subjectProblems.some(({ availability }) => availability.state === "unavailable");

  return {
    ref, origin, sourceState, view, name, id, manager, capabilities,
    key: JSON.stringify([manager, ref.kind, id]),
    kind: ref.kind === "extension" ? "extension" : sourceState ? "source" : "builtin",
    builtinExtension: origin === "builtin_extension",
    statusText: status.text,
    tone: status.tone,
    subtitle,
    alertTone: failedCapability ? "red" : status.tone === "red" || status.tone === "orange" ? status.tone : undefined,
  };
}

/** Old built-in addresses included service, channel and connector ids in a single namespace. */
export function projectedResource(view: IntegrationView, items?: readonly IntegrationCatalogItem[]): { kind: IntegrationKind; id: string } | null {
  switch (view.kind) {
    case "extension":
    case "channel":
    case "connector": return { kind: view.kind, id: view.id };
    case "builtin":
      if (items?.some(({ ref }) => ref.kind === "service" && ref.id === view.id)) return { kind: "service", id: view.id };
      if (view.id === "lark" || view.id === "github" || view.id === "imessage" || view.id === "larkcli") {
        return { kind: "connector", id: view.id };
      }
      if (view.id === "apps" || view.id === "voice" || view.id === "feishu-mention" || view.id === "feishu") {
        return { kind: "channel", id: view.id };
      }
      return { kind: "service", id: view.id };
    case "source": {
      try {
        const state = JSON.parse(view.state) as { p?: string; id?: string } | null;
        if (state?.p !== "source" || typeof state.id !== "string" || !state.id) return null;
        const push = items?.some(({ ref }) => ref.kind === "push_source" && ref.id === state.id);
        return { kind: push ? "push_source" : "subscription", id: state.id };
      } catch {
        return null;
      }
    }
    default: return null;
  }
}
