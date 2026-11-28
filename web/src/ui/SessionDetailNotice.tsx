import { t } from "../i18n";
import { Button } from "./atoms";

export function SessionDetailNotice({ error, retry }: { error: string; retry: () => void }) {
  return <div role="alert" className="flex shrink-0 items-center gap-2 border-b border-line bg-inset px-3 py-2 text-xs text-ink-2">
    <span className="min-w-0 flex-1 break-words">{error}</span>
    <Button tone="quiet" onClick={retry}>{t("sidebar.retry")}</Button>
  </div>;
}
