import { Suspense } from "react";
import { t } from "../../i18n";
import { lazyPage } from "../../lib/lazy-page";
import { Group, Row, Switch } from "./SettingsParts";
import type { RenderForwarding } from "./integrations/InboxForwardController";

// Sources can open directly. Load the scoped settings code only when the forwarding control is mounted.
const InboxForwardController = lazyPage(() => import("./integrations/InboxForwardController").then((m) => m.InboxForwardController));

export function InboxForwardSetting(props: { manager?: string }) {
  return <Suspense fallback={null}><InboxForwardController {...props} render={forwardingSwitch} /></Suspense>;
}

const forwardingSwitch: RenderForwarding = (enabled, busy, toggle) => (
  <div className="mt-4">
    <Group anchor="inbox-todo-forward">
      <Row label={t("settings-assistantsettings.inbox_todo_forward")} hint={t("settings-assistantsettings.inbox_todo_forward_hint")} wrapHint>
        <Switch on={enabled} onChange={toggle} disabled={busy} label={t("settings-assistantsettings.inbox_todo_forward")} />
      </Row>
    </Group>
  </div>
);
