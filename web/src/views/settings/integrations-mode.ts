// A bookmarked assistant can have a different Labs setting from the sidebar's assistant.
import { useEffect, useState } from "react";
import { api } from "../../lib/api";
import { MANAGER_HEADER } from "../../lib/manager-choice";
import { onHubEvent } from "../../lib/store";

type Mode = boolean | "loading" | "unavailable";

export function useOtherIntegrationsMode(manager: string | undefined): Mode {
  const [answer, setAnswer] = useState<{ manager: string | undefined; mode: Mode }>({ manager, mode: "loading" });
  if (answer.manager !== manager) setAnswer({ manager, mode: "loading" });
  useEffect(() => {
    if (!manager) return;
    let request: AbortController | undefined;
    const load = () => {
      request?.abort();
      const controller = new AbortController();
      request = controller;
      const publish = (mode: Mode) => {
        if (!controller.signal.aborted) setAnswer({ manager, mode });
      };
      void api<{ manager?: string; integrations_v2?: boolean }>("/assistant", {
        headers: { [MANAGER_HEADER]: manager },
        signal: controller.signal,
      }).then(
        (profile) => publish(profile.manager === manager ? profile.integrations_v2 === true : "unavailable"),
        () => publish("unavailable"),
      );
    };
    load();
    const stop = onHubEvent("hello", load);
    return () => {
      request?.abort();
      stop();
    };
  }, [manager]);
  return answer.manager === manager ? answer.mode : "loading";
}
