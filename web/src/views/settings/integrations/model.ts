// Pure data model and helpers for the lazy-loaded unified Integration Detail view (integrations_v2, #14847).
import { currentLanguage, t } from "../../../i18n";
import { ApiError } from "../../../lib/api";
import {
  forgetChannels, MAX_THREAD_IDLE_HOURS, normalizeThreadMode, supportsThreadRouting, updateChannel,
  type ChannelDetail, type ChannelThreadMode, type ChannelUpdateBody,
} from "../../../lib/channels";
import {
  checkUpdate, CONFIGURATION_VERSION, ConfigurationError,
  type ConfigurationDocument, type ConfigurationSource,
} from "../../../lib/configuration/contract";
import { fieldPath, type ExtensionField } from "../../../lib/extensions";
import { sameManager } from "../../../lib/manager-choice";
import type { ResourceConfiguration } from "./resourceConfiguration";

export type BuiltinIntegrationId = "mail" | "feishu-bot" | "imessage" | "github" | "lark" | "larkcli" | (string & {});

export type IntegrationResourceKind = "service" | "extension" | "channel" | "connector" | "push_source" | "subscription";

export interface IntegrationResourceIdentity {
  manager?: string;
  kind: IntegrationResourceKind;
  id: string;
}

/** Stable resource identity key `(manager, kind, id)` for mounting detail containers across layout changes. */
export function integrationResourceKey(manager: string | undefined, kind: IntegrationResourceKind, id: string): string {
  return JSON.stringify([manager ?? "", kind, id]);
}

export type IntegrationTarget =
  | {
      kind: "builtin";
      id: BuiltinIntegrationId;
      name?: string;
      description?: string;
      channelId?: string;
      connectorId?: string | null;
      manager?: string;
    }
  | {
      kind: "extension";
      id: string;
      name?: string;
      channelId?: string;
      connectorId?: string;
      manager?: string;
    };

/** Derives canonical `(manager, kind, id)` resource identity for a target and optional server projection. */
export function resolveIntegrationResource(
  target: IntegrationTarget,
  manager: string | undefined,
  projectedRef?: { manager: string; kind: IntegrationResourceKind; id: string },
): IntegrationResourceIdentity {
  if (projectedRef) {
    return { manager: projectedRef.manager, kind: projectedRef.kind, id: projectedRef.id };
  }
  if (target.kind === "extension") {
    return { manager, kind: "extension", id: target.id };
  }
  if (target.id === "mail" || target.id === "feishu-bot" || target.id === "lark") {
    return { manager, kind: "service", id: target.id };
  }
  if (target.channelId && target.connectorId === null) {
    return { manager, kind: "channel", id: target.id };
  }
  if (target.connectorId !== null) {
    return { manager, kind: "connector", id: target.id };
  }
  return { manager, kind: "service", id: target.id };
}

export type IntegrationSectionId = "connection" | "channel" | "inbox" | "extension-settings" | "management";

export interface PartitionedExtensionFields {
  connection: ExtensionField[];
  channel: ExtensionField[];
  inbox: ExtensionField[];
  /** Fields without a `section` (legacy extensions) or with an unrecognized future section. */
  general: ExtensionField[];
  advanced: ExtensionField[];
}

/**
 * Splits extension configuration fields into the unified IntegrationDetail sections while preserving
 * manifest order and keeping legacy (unsectioned) or unknown-section fields safely in `general`.
 */
export function partitionExtensionFields(fields: readonly ExtensionField[] | undefined): PartitionedExtensionFields {
  const out: PartitionedExtensionFields = {
    connection: [],
    channel: [],
    inbox: [],
    general: [],
    advanced: [],
  };
  if (!fields?.length) return out;
  for (const field of fields) {
    switch (field.section) {
      case "connection":
        out.connection.push(field);
        break;
      case "channel":
        out.channel.push(field);
        break;
      case "inbox":
        out.inbox.push(field);
        break;
      case "advanced":
        out.advanced.push(field);
        break;
      default:
        out.general.push(field);
        break;
    }
  }
  return out;
}

/** Whether any field (or nested object field) in `fields` currently carries a validation error. */
export function fieldsHaveError(
  fields: readonly ExtensionField[],
  errors: Record<string, string>,
  problems: Record<string, string>,
  prefix: string[] = [],
): boolean {
  for (const f of fields) {
    const path = [...prefix, f.name];
    const key = fieldPath(path);
    if (errors[key] || problems[key]) return true;
    if (f.type === "object" && f.fields?.length && fieldsHaveError(f.fields, errors, problems, path)) {
      return true;
    }
  }
  return false;
}

/**
 * Resolves whether `integrations_v2` is active for `manager` (or the current assistant when omitted).
 * Always fails closed (false) on older hubs that omit `integrations_v2`.
 */
export function isIntegrationsV2Active(
  assistant: { manager?: string; integrations_v2?: boolean } | null | undefined,
  manager?: string,
  shownManagerName?: string | null,
): boolean {
  if (!assistant || assistant.integrations_v2 !== true) return false;
  const expected = manager ?? shownManagerName ?? undefined;
  if (!expected) return true;
  return sameManager(assistant.manager, expected);
}

/** Default channel id associated with a built-in integration when not explicitly overridden. */
export function resolveBuiltinChannelId(target: Extract<IntegrationTarget, { kind: "builtin" }>): string | null {
  if (target.channelId) return target.channelId;
  if (target.id === "feishu-bot") return "feishu";
  return null;
}

/** Default hub connector id associated with a built-in integration when not explicitly overridden. */
export function resolveBuiltinConnectorId(target: Extract<IntegrationTarget, { kind: "builtin" }>): string | null {
  if (target.connectorId === null) return null;
  if (target.connectorId) return target.connectorId;
  if (target.id === "mail" || target.id === "feishu-bot" || target.id === "lark") return null;
  return target.id;
}

/**
 * Builds a ConfigurationDocument for a channel's `thread_mode` and `thread_idle_hours` settings.
 * Gracefully degrades to read-only with an update hint on older hubs that omit `thread_mode`.
 */
export function channelRoutingDocument(d: ChannelDetail, saved = false): ConfigurationDocument {
  const supported = supportsThreadRouting(d);
  const editable = !d.read_only && d.source !== "market";
  const readOnly = !supported || !editable;
  const disabledReason = !supported ? t("settings-integrations.routing_hub_unsupported") : undefined;
  const mode: ChannelThreadMode = normalizeThreadMode(d.thread_mode);
  const idleHours = typeof d.thread_idle_hours === "number" && Number.isFinite(d.thread_idle_hours)
    ? Math.max(0, Math.trunc(d.thread_idle_hours))
    : 0;
  return {
    schema_version: CONFIGURATION_VERSION,
    id: `channel-routing-${d.id}`,
    revision: `${supported ? "v2" : "legacy"}:${mode}:${idleHours}:${editable ? "rw" : "ro"}:${currentLanguage()}`,
    title: t("settings-integrations.routing_title"),
    groups: [
      {
        id: "routing",
        title: t("settings-integrations.routing_group"),
        fields: [
          {
            id: "thread_mode",
            kind: "select",
            default: "channel_default",
            title: t("settings-integrations.thread_mode_title"),
            description: t("settings-integrations.thread_mode_description"),
            ...(readOnly && { read_only: true }),
            ...(disabledReason && { disabled_reason: disabledReason }),
            select: {
              presentation: "list",
              options: [
                {
                  value: "channel_default",
                  title: t("settings-integrations.thread_mode_default_title"),
                  description: t("settings-integrations.thread_mode_default_desc"),
                },
                {
                  value: "per_group_and_topic",
                  title: t("settings-integrations.thread_mode_group_topic_title"),
                  description: t("settings-integrations.thread_mode_group_topic_desc"),
                },
                {
                  value: "per_topic",
                  title: t("settings-integrations.thread_mode_topic_title"),
                  description: t("settings-integrations.thread_mode_topic_desc"),
                },
              ],
            },
          },
          {
            id: "thread_idle_hours",
            kind: "number",
            default: 0,
            title: t("settings-integrations.thread_idle_hours_title"),
            description: t("settings-integrations.thread_idle_hours_description"),
            ...(readOnly && { read_only: true }),
            ...(disabledReason && { disabled_reason: disabledReason }),
            number: {
              integer: true,
              minimum: 0,
              maximum: MAX_THREAD_IDLE_HOURS,
              step: 24,
            },
            details: [
              {
                title: t("settings-integrations.thread_idle_default_label"),
                description: t("settings-integrations.thread_idle_default_value"),
              },
              {
                title: t("settings-integrations.thread_idle_examples_label"),
                description: t("settings-integrations.thread_idle_examples_value"),
              },
            ],
          },
        ],
      },
    ],
    values: {
      thread_mode: mode,
      thread_idle_hours: idleHours,
    },
    ...(saved && {
      notices: {
        thread_mode: { kind: "info", message: t("settings-integrations.routing_saved") },
      },
    }),
  };
}

export function saveChannelFields(
  resource: ResourceConfiguration<ChannelDetail>, label: string, body: ChannelUpdateBody,
  check?: (remote: ChannelDetail) => void,
) {
  return resource.save({
    label, key: JSON.stringify(body),
    prepare: (remote) => {
      check?.(remote);
      return {
        send: () => updateChannel(resource.id, body, resource.client),
        matches: (d) =>
          (body.enabled === undefined || d.enabled === body.enabled) &&
          (!body.target || (d.target?.kind === body.target.kind && d.target.machine_id === body.target.machine_id && d.target.session_id === body.target.session_id)) &&
          Object.entries(body.features ?? {}).every(([id, on]) => d.features?.some((f) => f.id === id && f.on === on)) &&
          (body.thread_mode === undefined || d.thread_mode === body.thread_mode) &&
          (body.thread_idle_hours === undefined || (supportsThreadRouting(d) && (d.thread_idle_hours ?? 0) === body.thread_idle_hours)),
      };
    },
  });
}

/** The routing form edits one field at a time using the channel's partial PUT. */
export function channelRoutingSource(resource: ResourceConfiguration<ChannelDetail>): ConfigurationSource {
  const read = () => {
    const remote = resource.snapshot().remote;
    if (!remote) throw new ConfigurationError("configuration_unavailable");
    return channelRoutingDocument(remote);
  };
  return {
    key: resource.client.key,
    subscribe: resource.subscribeRemote,
    read: async () => read(),
    update: async (update) => {
      const doc = read();
      const refused = checkUpdate(doc, update);
      if (refused) throw refused;
      const body: { thread_mode?: ChannelThreadMode; thread_idle_hours?: number } = {};
      if (update.reset?.includes("thread_mode")) {
        body.thread_mode = "channel_default";
      } else if (typeof update.changes?.thread_mode === "string") {
        body.thread_mode = normalizeThreadMode(update.changes.thread_mode);
      }
      if (update.reset?.includes("thread_idle_hours")) {
        body.thread_idle_hours = 0;
      } else if (typeof update.changes?.thread_idle_hours === "number") {
        body.thread_idle_hours = Math.trunc(update.changes.thread_idle_hours);
      }
      try {
        const next = await saveChannelFields(resource, "routing", body, (remote) => {
          const latest = channelRoutingDocument(remote);
          for (const [id, value] of Object.entries(body)) {
            if (latest.values[id] !== doc.values[id] && latest.values[id] !== value) throw new ConfigurationError("configuration_conflict");
          }
          const refused = checkUpdate(latest, { ...update, revision: latest.revision });
          if (refused) throw refused;
        });
        if (!next) throw new ConfigurationError("configuration_unavailable");
        forgetChannels();
        return channelRoutingDocument(next, true);
      } catch (e) {
        if (e instanceof ApiError) throw ConfigurationError.fromBody(e.body, e.status);
        throw e;
      }
    },
  };
}
