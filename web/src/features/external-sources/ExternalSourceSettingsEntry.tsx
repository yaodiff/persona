import { Suspense, useSyncExternalStore } from "react";
import { t } from "../../i18n";
import { credentialOwner, subscribeCredentialOwner } from "../../lib/api";
import { lazyPage } from "../../lib/lazy-page";
import { useShownManager } from "../../lib/manager-choice";
import { Loading } from "../../ui/atoms";
import { OverlayBoundary } from "../../views/LazyOverlay";
import type { ExternalSourceSettingsCopy, ExternalSourceSettingsProps } from "./ExternalSourceSettings";

const SettingsPanel = lazyPage(() => import("./ExternalSourceSettings").then((module) => module.ExternalSourceSettings), { listed: false });

/** These ingestion entry points share the provider spelling used on the hub's Inbox path. */
export function externalProviderForConnector(id: string) {
  if (id === "feishu" || id === "feishu-bot" || id === "larkcli") return "lark";
  if (id === "email") return "mail";
  return id;
}

export function ConnectorExternalSourceSettings({ ids, manager, copy }: { ids: readonly string[]; manager?: string; copy?: ExternalSourceSettingsCopy }) {
  const providers = [...new Set(ids.filter(Boolean).map(externalProviderForConnector))];
  return providers.map((provider) => <ExternalSourceSettingsEntry key={provider} provider={provider} manager={manager} copy={copy} />);
}

export function ExternalSourceSettingsEntry({ manager: named, ...props }: Omit<ExternalSourceSettingsProps, "manager"> & { manager?: string }) {
  const current = useShownManager();
  const owner = useSyncExternalStore(subscribeCredentialOwner, credentialOwner, credentialOwner);
  const manager = named ?? current;
  return (
    <OverlayBoundary open onClose={() => {}} fallback={<p role="alert" className="text-meta text-red">{t("pageboundary.chunk_body")}</p>}>
      <Suspense fallback={<Loading />}>
        <SettingsPanel key={JSON.stringify([owner, manager, props.provider, props.channel])} {...props} manager={manager} />
      </Suspense>
    </OverlayBoundary>
  );
}
