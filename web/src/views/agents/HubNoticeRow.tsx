// Keep the status subscription live while Settings is visible. Load the row and its actions only for a notice.
import { lazy, Suspense, useEffect, useState } from "react";
import {
  dismissedHubNotices, hubNotice, hubUpdate,
  type HubUpdateStatus,
} from "../../lib/hub-update";
import { isResumed } from "../../lib/reconnect";
import { onHubEvent } from "../../lib/store";

const HubNoticeView = lazy(() => import("./HubNoticeView"));

export function HubNoticeLine({ onNavigate }: { onNavigate?: () => void }) {
  const [status, setStatus] = useState<HubUpdateStatus | null>(null);
  const [dismissed, setDismissed] = useState(() => dismissedHubNotices());

  useEffect(() => {
    let dead = false;
    // Only the newest read may land, and a hub_update push outdates every read still on its way: a GET that left
    // before the push would otherwise put the older status back (an "Update" button in the middle of an update).
    let seq = 0;
    // an older hub without /hub/update, or one that can't be read now: no line
    const read = () => {
      const mine = ++seq;
      void hubUpdate().then((s) => !dead && mine === seq && setStatus(s), () => {});
    };
    read();
    const offUpdate = onHubEvent("hub_update", (m) => {
      if (!m.update) return;
      seq++;
      setStatus(m.update as HubUpdateStatus);
    });
    // A resumed hello does not read: the hub replays the hub_update pushes it missed, which carry the update state. The
    // auto-update settings behind the auto_on and available lines are not pushed, so a change another device made while
    // the socket was down shows with the next push or fresh hello (Settings › hub works the same way).
    const offHello = onHubEvent("hello", (m) => !isResumed(m) && read());
    return () => {
      dead = true;
      offUpdate();
      offHello();
    };
  }, []);

  const notice = hubNotice(status, dismissed);
  if (!notice) return null;
  return (
    <Suspense fallback={null}>
      <HubNoticeView notice={notice} onNavigate={onNavigate} onStatus={setStatus} onDismiss={setDismissed} />
    </Suspense>
  );
}
