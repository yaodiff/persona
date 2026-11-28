// The personal list's Browse by account (#16248): the hub groups personal Agents by account id, which says nothing on its
// own. Its heading and the account picker name the account as `/agents` lists it (lib/agents.ts accountGroup), after its
// provider's mark: the agent mark of the product it drives where the web bundles one, else its label's letter tile.
import { useMemo, type ReactNode } from "react";
import { accountGroup, providerLabel, providerMarkAgent, useAgentList } from "../../lib/agents";
import { AgentMark } from "../../ui/AgentMark";
import { SESSION_LIST_METRICS } from "../../ui/list-metrics";

/** The mark of an account's provider, beside the account's name (so it says nothing of its own). */
export function AccountMark({ provider }: { provider: string }) {
  return <AgentMark agent={providerMarkAgent(provider)} size={SESSION_LIST_METRICS.accountMark} decorative />;
}

/** PagedSessionList's `label` and `accountMark` for a personal list, read off the agents store. */
export function useAccountGroups(): {
  label: (dimension: string, value: string) => string;
  accountMark: (account: string) => ReactNode;
  providerOptions: { key: string; label: string }[];
} {
  const { sources, sessions } = useAgentList();
  return useMemo(() => {
    const providers = new Map<string, string>();
    for (const session of sessions) {
      if (session.provider && !providers.has(session.provider)) providers.set(session.provider, providerLabel(session.provider));
    }
    return {
      label: (dimension: string, value: string) => (dimension === "group" ? accountGroup(value, sources, sessions).name : value),
      accountMark: (account: string) => {
        const { provider } = accountGroup(account, sources, sessions);
        return provider ? <AccountMark provider={provider} /> : undefined;
      },
      providerOptions: [...providers].map(([key, label]) => ({ key, label })).sort((a, b) => a.label.localeCompare(b.label)),
    };
  }, [sources, sessions]);
}
