// "Close…" (#7125): the dialog lib/close-agent.ts opens for every menu that closes an Agent: its row's, the chat's ⋯, and
// "Close pane" on its pane in the Agents tree, the Terminal page and the command palette. The dialog is its own chunk,
// loaded once the browser is idle after start, so it stays off the first screen.
import { Suspense, useEffect, useState } from "react";
import { onOpenCloseAgent } from "../lib/close-agent";
import { lazyPage } from "../lib/lazy-page";
import { machineWithHub } from "../lib/mesh";
import { useStore } from "../lib/store";
import type { Session } from "../lib/types";

const CloseAgentDialog = lazyPage(() => import("./chat/CloseAgentDialog").then((m) => m.CloseAgentDialog), { idle: true });

export function CloseAgentHost() {
  const [session, setSession] = useState<Session | null>(null);
  useEffect(() => onOpenCloseAgent(setSession), []);
  const machines = useStore((st) => st.machines);
  if (!session) return null;
  return (
    <Suspense fallback={null}>
      <CloseAgentDialog key={`${session.machine_id}/${session.session_id}`} open session={session} machineName={machineWithHub(session.machine_id, machines)} onClose={() => setSession(null)} />
    </Suspense>
  );
}
