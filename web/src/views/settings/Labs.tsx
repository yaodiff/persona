// Settings › Labs (#9877, docs/web/web.md "Labs") admits a temporary trial or a stable/new implementation choice.
// Keep the stable default and a lossless rollback. Link a tracking issue with a plan to graduate or remove the trial.
// Put permanent module switches in General and other lasting preferences in their owning category. Keep irreversible
// changes, internal debug controls and placeholder demos out of Labs. ConfigurationForm draws each trial's source.
import { useLayoutEffect, useState } from "react";
import { t } from "../../i18n";
import type { ConfigurationSource } from "../../lib/configuration/contract";
import type { ConfigurationGuidance } from "../../lib/configuration/guidance";
import { ASSISTANT_LABS, TERMINAL_LABS_ID, type LabId } from "../../lib/labs-catalog";
import { useLabsState } from "../../lib/labs-seen";
import { markLabsSeen } from "../../lib/labs-seen-write";
import { integrationsV2LabsSource } from "../../lib/labs-integrations-v2";
import { todosV2LabsSource } from "../../lib/labs-todos-v2";
import { workGuideLabsSource } from "../../lib/labs-work-guide";
import { useChosenManager, useKnownDefault } from "../../lib/manager-choice";
import { TERMINAL_LABS_FIELD, terminalLabsSource } from "../../lib/labs-terminal";
import { Dialog, Loading } from "../../ui/atoms";
import { ConfigurationForm } from "./configuration/ConfigurationForm";
import { Group, Pill, SettingsHint } from "./SettingsParts";
import { TodosV2AttentionPrefs } from "./TodosV2AttentionPrefs";

export interface LabsModule {
  source: ConfigurationSource;
  guidance?: ConfigurationGuidance;
}

const terminal = terminalLabsSource();
const assistantSources = {
  work_guide: workGuideLabsSource,
  todos_v2: todosV2LabsSource,
  integrations_v2: integrationsV2LabsSource,
};

function LabCards({ manager, newIds = [] }: { manager: string; newIds?: readonly LabId[] }) {
  const card = (id: LabId, source: ConfigurationSource) => (
    <div key={source.key} className="mb-6">
      <ConfigurationForm
        source={source}
        fieldBadge={newIds.includes(id) ? {
          id: id === TERMINAL_LABS_ID ? TERMINAL_LABS_FIELD : id,
          content: <Pill tone="accent">{t("settings-labs.new_badge")}</Pill>,
        } : undefined}
        afterFields={id === "todos_v2" ? <TodosV2AttentionPrefs manager={manager} /> : undefined}
      />
    </div>
  );
  return (
    <>
      {manager ? ASSISTANT_LABS.map((id) => card(id, assistantSources[id](manager))) : <Loading />}
      {card(TERMINAL_LABS_ID, terminal)}
    </>
  );
}

function LabsVisit({ manager }: { manager: string }) {
  const { ready, unseen, visible, seen, waiting, synced } = useLabsState();
  const [entry, setEntry] = useState<{ unseen: readonly LabId[]; remaining: LabId[]; synced: boolean } | null>(null);
  if (entry === null && manager && ready) {
    setEntry({ unseen, remaining: visible.filter((id) => !seen.includes(id) && !waiting.includes(id)), synced });
  }
  useLayoutEffect(() => {
    if (entry) void markLabsSeen(manager, entry.remaining, entry.synced);
  }, [entry, manager]);
  return <LabCards manager={manager} newIds={entry?.unseen} />;
}

export function LabsSettings({ modules, trackVisit = true }: { modules?: readonly LabsModule[]; trackVisit?: boolean } = {}) {
  const chosen = useChosenManager();
  const knownDefault = useKnownDefault();
  const manager = chosen ?? knownDefault;
  return (
    <>
      <Group anchor="labs" footer={t("settings-labs.scope_note")}>
        <SettingsHint>{t("settings-labs.intro")}</SettingsHint>
      </Group>
      {modules ? modules.map(({ source, guidance }) => (
        <div key={source.key} className="mb-6">
          <ConfigurationForm source={source} guidance={guidance} />
        </div>
      )) : trackVisit ? <LabsVisit key={manager} manager={manager} /> : <LabCards manager={manager} />}
    </>
  );
}

/** Labs over the current page (a sheet on a phone), opened from a feature's own menu (Terminal settings › Renderer): the
 *  same page as Settings › Labs, without leaving, so nothing open behind it (a chat's draft) is unmounted. */
export function LabsDialog({ open, onClose, modules }: { open: boolean; onClose: () => void; modules?: readonly LabsModule[] }) {
  return (
    <Dialog open={open} onClose={onClose} title={t("settings-nav.tab_labs")} width={600}>
      <div data-labs-dialog className="-mb-6">
        <LabsSettings modules={modules} trackVisit={false} />
      </div>
    </Dialog>
  );
}
