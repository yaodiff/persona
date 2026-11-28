// A full page can return 404 while its installed extension is waiting for required settings.
// The native stream's failed open also falls back to this HTTP draw.
import { useEffect, useState, type ReactNode } from "react";
import { t } from "../../i18n";
import { credentialOwner } from "../../lib/api";
import { chosenManager, stampedHeaders } from "../../lib/manager-choice";
import { navigate } from "../../lib/router";
import { Button, Loading } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { settingsHref } from "./href";

type Setup = { name: string; missing: string[]; manager?: string };

export function ExtensionSetupNotice({ check, extension, manager, children }: {
  check: boolean; extension: string; manager?: string; children: ReactNode;
}) {
  const [setup, setSetup] = useState<Setup | null>();
  useEffect(() => {
    if (!check) return;
    const controller = new AbortController();
    const owner = credentialOwner();
    const current = () => !controller.signal.aborted && owner === credentialOwner();
    // Only the failure path loads the Settings API. Fetch one installation and retain its display fields alone.
    void (async () => {
      try {
        const { extensionDetail } = await import("../../lib/extensions");
        if (!current()) return;
        const detail = await extensionDetail(extension, {
          headers: stampedHeaders(manager), signal: controller.signal, isAuthContextCurrent: current,
        });
        if (!current()) return;
        if (detail.id !== extension || detail.status !== "needs_setup" || (detail.manager && manager && detail.manager !== manager)) {
          setSetup(null);
          return;
        }
        const ownedBy = manager || detail.manager;
        // An unqualified settings URL follows the picked manager, while a direct page follows the default.
        if (!ownedBy && chosenManager()) { setSetup(null); return; }
        const missing = Array.isArray(detail.needs_setup)
          ? detail.needs_setup.filter((item): item is string => typeof item === "string" && !!item.trim()) : [];
        setSetup({ name: typeof detail.name === "string" && detail.name.trim() ? detail.name : extension, missing, manager: ownedBy });
      } catch {
        if (current()) setSetup(null);
      }
    })();
    return () => controller.abort();
  }, [check, extension, manager]);
  if (!check || setup === null) return children;
  if (!setup) return <Loading />;
  const settings = settingsHref("extensions", setup.manager ? `${extension}@${setup.manager}` : extension, false);
  return (
    <section className="flex flex-col items-center gap-3 rounded-card bg-surface px-6 py-8 text-center shadow-card">
      <span className="flex size-9 items-center justify-center rounded-control bg-field text-ink-2">
        <Icon name="extension" size={16} />
      </span>
      <h2 className="text-meta font-semibold text-ink">{t("settings-extensionview.setup_title", { name: setup.name })}</h2>
      {setup.missing.length > 0 && (
        <ul aria-label={t("settings-extensionview.missing_settings")} className="max-w-full list-disc space-y-1 pl-5 text-left text-label text-ink-2">
          {setup.missing.map((item, index) => <li key={index} className="break-words">{item}</li>)}
        </ul>
      )}
      <Button tone="secondary" icon="gear" onClick={() => navigate(settings)}>{t("settings-extensionview.open_settings")}</Button>
    </section>
  );
}
