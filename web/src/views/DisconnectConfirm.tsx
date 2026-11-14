// "Disconnect this browser?" (Settings › General and the account menu, DisconnectBrowser.tsx):
// asked before the hub revokes this browser's token, since getting back in takes pairing again. Its own chunk and
// namespace, so the first screen, which carries the account menu, carries neither.
import { t } from "../i18n";
import { Button, Dialog } from "../ui/atoms";

export function DisconnectConfirm({ open, busy, onCancel, onConfirm }: { open: boolean; busy: boolean; onCancel: () => void; onConfirm: () => void }) {
  return (
    <Dialog
      open={open}
      onClose={() => !busy && onCancel()}
      title={t("disconnect-confirm.title")}
      width={420}
      footer={
        <>
          <Button tone="quiet" onClick={onCancel} disabled={busy}>
            {t("common.cancel")}
          </Button>
          <Button tone="danger" busy={busy} onClick={onConfirm}>
            {t("disconnect-confirm.confirm")}
          </Button>
        </>
      }
    >
      <p className="text-meta leading-relaxed text-ink-2">{t("disconnect-confirm.body")}</p>
    </Dialog>
  );
}
