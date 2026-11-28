import { moduleFeature } from "../../core/module-flags";
import type { ModuleDescriptor } from "../../core/module-host";
import { byLanguage, t, type CatalogModule } from "../../i18n";

export const mail: ModuleDescriptor = {
  id: "mail",
  feature: moduleFeature("mail"),
  title: () => t("settings-features.module_mail"),
  description: () => t("settings-features.module_mail_hint"),
  mailbox: true,
  i18nNamespaces: ["mail"],
  locales: byLanguage(import.meta.glob<CatalogModule>("../../i18n/locales/*/_meta.json", { query: "?catalog=module-mail" })),
  load: () => import("./mail-code").then((m) => m.code),
};
