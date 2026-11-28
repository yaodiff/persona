import { moduleFeature } from "../../core/module-flags";
import type { ModuleDescriptor } from "../../core/module-host";
import { byLanguage, t, type CatalogModule } from "../../i18n";

export const office: ModuleDescriptor = {
  id: "office",
  feature: moduleFeature("office"),
  title: () => t("settings-features.module_office"),
  description: () => t("settings-features.module_office_hint"),
  filePreviews: ["office", "workbook"],
  i18nNamespaces: ["office"],
  locales: byLanguage(import.meta.glob<CatalogModule>("../../i18n/locales/*/_meta.json", { query: "?catalog=module-office" })),
  load: () => import("./OfficePreview").then((m) => ({ FilePreview: m.OfficePreview })),
};
