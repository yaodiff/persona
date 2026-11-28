// Adapt the selected Assistant's integrations_v2 Labs setting to the Labs form (#14802, #14805).
import { t } from "../i18n";
import { api, ApiError } from "./api";
import { MANAGER_HEADER, sameManager, shownManager } from "./manager-choice";
import { patchAssistant } from "./store";
import { checkUpdate, CONFIGURATION_VERSION, ConfigurationError, type ConfigurationDocument, type ConfigurationSource } from "./configuration/contract";

const field = "integrations_v2";
type Settings = { manager?: string; integrations_v2?: boolean };

function document(manager: string, state: Settings, saved = false): ConfigurationDocument {
  if (state.manager !== manager) throw new ConfigurationError("configuration_unavailable");
  const supported = typeof state.integrations_v2 === "boolean";
  return {
    schema_version: CONFIGURATION_VERSION,
    id: "assistant-integrations-v2",
    revision: String(state.integrations_v2),
    title: t("settings-labs.work_area"),
    groups: [{ id: "integrations-v2", title: t("settings-labs.trial"), fields: [{
      id: field, kind: "toggle", default: true,
      title: t("settings-labs.integrations_v2_title"), description: t("settings-labs.integrations_v2_hint"),
      ...(!supported && { read_only: true, disabled_reason: t("settings-labs.integrations_v2_unsupported") }),
      details: [
        { title: t("settings-labs.scope"), description: t("settings-labs.integrations_v2_scope") },
        { title: t("settings-labs.activation"), description: t("settings-labs.integrations_v2_activation") },
        { title: t("settings-labs.rollback"), description: t("settings-labs.integrations_v2_rollback") },
      ],
    }] }],
    values: { [field]: state.integrations_v2 === true },
    ...(saved && { notices: { [field]: { kind: "info", message: t("settings-labs.integrations_v2_saved") } } }),
  };
}

export function integrationsV2LabsSource(manager: string): ConfigurationSource {
  const headers = { [MANAGER_HEADER]: manager };
  return {
    key: `labs:assistant-integrations-v2:${manager}`,
    read: async (signal) => document(manager, await api<Settings>("/assistant", { headers, signal })),
    update: async (update, signal) => {
      const expected = update.revision === "true";
      const refused = checkUpdate(document(manager, { manager, integrations_v2: expected }), update);
      if (refused) throw refused;
      const value = update.reset?.includes(field) ? true : update.changes?.[field];
      try {
        const state = await api<Settings>("/assistant", { method: "PUT", headers, signal,
          json: { integrations_v2: value, expected_integrations_v2: expected } });
        if (state.integrations_v2 !== value) throw new ConfigurationError("configuration_unavailable");
        if (sameManager(manager, shownManager())) patchAssistant({ integrations_v2: state.integrations_v2 === true });
        return document(manager, state, true);
      } catch (e) {
        if (e instanceof ApiError) throw ConfigurationError.fromBody(e.body, e.status);
        throw e;
      }
    },
  };
}
