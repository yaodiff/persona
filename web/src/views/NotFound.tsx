// An address that is no page of the app (lib/router.ts "not-found"): a mistyped one, or one left from an older version.
// Said plainly, with the way to Overview, as a space that is gone says it (SpacePage), instead of Overview drawn under
// the wrong address (web usability audit item 42). A lazy page: only such an address loads it, and its words (the
// not-found namespace) stay off the first screen.
import { t } from "../i18n";
import { navigate } from "../lib/router";
import { Button, IconButton } from "../ui/atoms";
import { Empty } from "./session/files/parts";

export function NotFound({ onToggleSidebar }: { onToggleSidebar: () => void }) {
  return (
    <div className="flex h-full flex-col">
      <header className="flex h-14 shrink-0 items-center px-3 sm:px-4">
        <IconButton icon="sidebar" label={t("app.sidebar")} onClick={onToggleSidebar} className="lg:hidden" />
      </header>
      <div className="flex min-h-0 flex-1 flex-col pb-[10vh]" role="status">
        <Empty
          icon="search"
          title={t("not-found.page_title")}
          detail={t("not-found.page_detail")}
          action={<Button tone="secondary" icon="home" onClick={() => navigate({ name: "home" })}>{t("not-found.page_back")}</Button>}
        />
      </div>
    </div>
  );
}
