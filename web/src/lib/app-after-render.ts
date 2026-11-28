import { t } from "../i18n";
import { toast } from "../ui/atoms";
import { startLanguageSync } from "./language-sync";
import { startNewVersion } from "./new-version";

// An open page switches to a new build or another tab's language when idle, or offers to reload.
startNewVersion((reloadNow) => toast(t("app.new_version"), "info", { label: t("app.new_version_reload"), run: reloadNow, sticky: true }));
startLanguageSync();
