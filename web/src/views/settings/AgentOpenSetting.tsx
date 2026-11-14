// Settings › General › Agent preferences › "How Agents open" (#5424, lib/agent-open.ts): where an Agent or a task opened
// from a chat goes, the side panel beside the chat (the default) or a full page. Kept by this browser; a change made in
// another tab shows here too.
import { useEffect, useState } from "react";
import { t } from "../../i18n";
import { AGENT_OPEN_KEY, agentOpenMode, setAgentOpenMode, type AgentOpen } from "../../lib/agent-open";
import { Segmented } from "../assistant/parts";
import { Row } from "./SettingsParts";

export function AgentOpenRow() {
  const [mode, setMode] = useState(agentOpenMode);
  useEffect(() => {
    const other = (e: StorageEvent) => {
      if (e.key === AGENT_OPEN_KEY || e.key === null) setMode(agentOpenMode());
    };
    addEventListener("storage", other);
    return () => removeEventListener("storage", other);
  }, []);
  const choose = (next: AgentOpen) => {
    setAgentOpenMode(next);
    setMode(agentOpenMode());
  };
  const options: { v: AgentOpen; label: string }[] = [
    { v: "panel", label: t("settings-agentopen.panel") },
    { v: "page", label: t("settings-agentopen.page") },
  ];
  return (
    <Row label={t("settings-agentopen.label")} hint={t("settings-agentopen.hint")} wrapHint stackOnNarrow anchor="agent-open">
      <Segmented<AgentOpen> label={t("settings-agentopen.label")} value={mode} options={options} onChange={choose} />
    </Row>
  );
}
