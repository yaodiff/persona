// Settings › Knowledge › Knowledge points (docs/assistant/knowledge.md "Knowledge points"): whether the background curation
// pulls durable knowledge out of timelines and chats into topics (`kb_topics`, on unless turned off), and the way to the
// knowledge map that shows them.
import { useEffect, useState } from "react";
import { t } from "../../i18n";
import { api, errorText } from "../../lib/api";
import { MANAGER_HEADER } from "../../lib/manager-choice";
import { navigate } from "../../lib/router";
import { toast } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { Group, Row, Switch } from "./SettingsParts";

/** Shown above the selection in Settings › Knowledge, inside its manager picker (KnowledgeSelection), keyed by manager. */
export function KnowledgeTopicsSwitch({ manager }: { manager: string }) {
  const [on, setOn] = useState<boolean | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const headers = { [MANAGER_HEADER]: manager };
  useEffect(() => {
    const ac = new AbortController();
    api<{ kb_topics?: boolean }>("/assistant", { headers, signal: ac.signal }).then(
      (r) => setOn(r.kb_topics ?? true),
      (e) => !ac.signal.aborted && setError(errorText(e)),
    );
    return () => ac.abort();
    // The wrapper remounts this for another manager.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  const change = async (next: boolean) => {
    setBusy(true);
    setOn(next);
    try {
      const r = await api<{ kb_topics?: boolean }>("/assistant", { method: "PUT", json: { kb_topics: next }, headers });
      setOn(r.kb_topics ?? next);
    } catch (e) {
      setOn(!next);
      toast(t("settings-knowledgetopics.save_failed", { error: errorText(e) }), "error");
    } finally {
      setBusy(false);
    }
  };
  return (
    <Group title={t("settings-knowledge.topics_title")} anchor="knowledge-topics" footer={t("settings-knowledgetopics.footer")}>
      <Row label={t("settings-knowledge.topics_switch")} anchor="knowledge-topics-switch" hint={error ? t("settings-knowledgetopics.load_failed", { error }) : undefined}>
        <Switch on={on ?? true} onChange={(v) => void change(v)} disabled={on === null || busy} label={t("settings-knowledge.topics_switch")} />
      </Row>
      <button type="button" onClick={() => navigate({ name: "kb", kind: "map" })} className="flex w-full items-center justify-between px-4 py-3 text-left text-[13px] text-ink hover:bg-hover">
        <span>{t("settings-knowledgetopics.open_map")}</span>
        <Icon name="chevronRight" size={12} className="text-ink-3" />
      </button>
    </Group>
  );
}
