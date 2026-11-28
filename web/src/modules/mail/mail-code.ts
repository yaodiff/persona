import type { ModuleCode } from "../../core/module-host";
import { lazyPage } from "../../lib/lazy-page";
import { accounts } from "./mail";

export const code: ModuleCode = {
  mailbox: {
    accounts,
    Body: lazyPage(() => import("./FullMailBody").then((m) => m.FullMailBody), { listed: false }),
    Compose: lazyPage(() => import("./ComposeDialog").then((m) => m.NewMail), { listed: false }),
    Settings: lazyPage(() => import("./MailSettings").then((m) => m.Mail), { listed: false }),
    InboxSwitch: lazyPage(() => import("./MailSettings").then((m) => m.MailInboxSwitch), { listed: false }),
  },
};
