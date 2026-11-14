// Settings › General › Agent (#2475): the hub-wide switches about agents. "Workflow board" is off unless turned on here; the switch
// is the hub's, so every browser shows the same (lib/workspace-board-setting.ts).
import { t } from "../../i18n";
import { ApiError, errorText } from "../../lib/api";
import { setWorkspaceBoardEnabled, useWorkspaceBoardSetting } from "../../lib/workspace-board-setting";
import { Spinner, toast } from "../../ui/atoms";
import { Group, Row, RowFailed, Switch } from "./SettingsParts";

export function AgentGroup() {
  const board = useWorkspaceBoardSetting();
  const toggle = (on: boolean) => void setWorkspaceBoardEnabled(on).catch((e) => toast(errorText(e), "error"));
  // a hub from before the switch (404) has none to show: what to do about it, not a switch that can't move
  const missing = board.error instanceof ApiError && board.error.status === 404;
  // read-only only when the hub said so: after a read that failed, a push says on or off but not who may change it
  // (the switch is read again; a write the hub refuses is put back and says why)
  const readonly = board.enabled !== null && !board.canWrite && !board.error;
  return (
    <Group title={t("workspace-board.setting_group")} anchor="agent-group">
      {board.enabled === null && board.error ? (
        <RowFailed text={t("workspaceboard.setting_failed", { error: errorText(board.error) })} retry={t("workspaceboard.retry")} onRetry={() => void board.retry()} />
      ) : (
        <Row anchor="workflow-board" label={t("workspace-board.setting_label")} hint={missing ? t("workspaceboard.setting_unsupported") : readonly ? t("workspace-board.setting_readonly") : t("workspace-board.setting_hint")} wrapHint={missing}>
          {board.enabled === null ? <Spinner /> : missing ? undefined : <Switch on={board.enabled} onChange={toggle} disabled={readonly} label={t("workspace-board.setting_label")} />}
        </Row>
      )}
    </Group>
  );
}
