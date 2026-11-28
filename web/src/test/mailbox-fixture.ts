import { registerModule, moduleById } from "../core/module-host";
import { api } from "../lib/api";
import type { MailAccount } from "../lib/agent-settings";

/** Isolate panel behavior that needs a writable mailbox or a list of accounts. */
export function registerTestMailbox() {
  if (moduleById("test-mailbox")) return;
  const empty = () => null;
  registerModule({
    id: "test-mailbox", title: () => "Mail", description: () => "", mailbox: true,
    load: async () => ({ mailbox: {
      Body: empty, Compose: empty, Settings: empty, InboxSwitch: empty,
      accounts: async () => (await api<{ accounts?: MailAccount[] }>("/mail/accounts")).accounts ?? [],
    } }),
  });
}
