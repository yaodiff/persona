// A list whose read failed (docs/design/DESIGN_SYSTEM.md 3.10): a red line saying what could not be read and why, and
// Retry under it, in place of the rows. The paged session lists and the sidebar's complete Agents list draw it, so a
// failed read never leaves a list blank or loading for good.
import { Button } from "./atoms";
import { t } from "../i18n";

export function ListError({ text, onRetry }: { text: string; onRetry: () => void }) {
  return <div className="px-2 py-2"><p role="alert" className="text-xs text-red">{text}</p><Button onClick={onRetry}>{t("sidebar.retry")}</Button></div>;
}
