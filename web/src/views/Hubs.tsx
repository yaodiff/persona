// Going between hubs (#1014, lib/hubs.ts): the list in Settings › General, the other hubs in the account menu and on the sign-in
// page. Switching opens the other hub's own page; with no other hub in the list, the menu and the sign-in page look as
// before (only Settings shows the list, to add one from).
import { useEffect, useState } from "react";
import { addHub, here, hostOf, hubTitle, loadHubs, normalizeHubURL, onHubsChange, removeHub, renameHub, switchURL, type SavedHub } from "../lib/hubs";
import { Button, Dialog, IconButton, toast, type MenuItem } from "../ui/atoms";
import { Icon } from "../ui/icons";
import { Group, Pill, Row } from "./settings/SettingsParts";
import { t } from "../i18n";

export { here };

export function useHubs(): SavedHub[] {
  const [list, setList] = useState(loadHubs);
  useEffect(() => {
    const sync = () => setList(loadHubs());
    // another tab of this hub's page changed it
    const storage = (e: StorageEvent) => e.key === "persona.hubs" && sync();
    addEventListener("storage", storage);
    const off = onHubsChange(sync);
    return () => {
      off();
      removeEventListener("storage", storage);
    };
  }, []);
  return list;
}

/** Open another hub's page, handing it the list. */
export function goToHub(h: SavedHub) {
  location.href = switchURL(h.url, loadHubs());
}

/** The account menu's rows for the other hubs (none when there is no other one). */
export function hubMenuItems(list: SavedHub[]): MenuItem[] {
  return list
    .filter((h) => h.url !== here())
    .map((h, i) => ({ key: `hub:${h.url}`, label: hubTitle(h), hint: t("hubs.switch"), icon: "hub" as const, divider: i === 0, onSelect: () => goToHub(h) }));
}

// ── Settings › General ─────────────────────────────────────

export function HubList() {
  const list = useHubs();
  const [adding, setAdding] = useState(false);
  const [renaming, setRenaming] = useState<SavedHub | null>(null);
  const current = here();
  // the page's own hub first, the rest as the user added them
  const rows = [...list.filter((h) => h.url === current), ...list.filter((h) => h.url !== current)];
  return (
    <>
      <Group
        anchor="hubs"
        title={t("hubs.group_title")}
        right={
          <Button tone="quiet" icon="plus" onClick={() => setAdding(true)} className="-mb-1 h-7 px-2">
            {t("hubs.add_hub")}
          </Button>
        }
        footer={t("hubs.group_footer")}
      >
        {rows.length === 0 && <Row label={hostOf(current)} hint={t("hubs.this_hub")} />}
        {rows.map((h) => (
          <Row key={h.url} label={hubTitle(h)} hint={<span className="font-mono">{hostOf(h.url)}</span>}>
            {h.url === current ? (
              <Pill tone="green">{t("hubs.current")}</Pill>
            ) : (
              <Button tone="secondary" onClick={() => goToHub(h)} className="h-7 px-2.5">
                {t("hubs.switch")}
              </Button>
            )}
            <IconButton icon="pencil" label={t("hubs.rename_label", { name: hubTitle(h) })} onClick={() => setRenaming(h)} />
            {h.url !== current && (
              <IconButton
                icon="trash"
                label={t("hubs.remove_label", { name: hubTitle(h) })}
                onClick={() => {
                  removeHub(h.url);
                  toast(t("hubs.removed", { name: hubTitle(h) }));
                }}
              />
            )}
          </Row>
        ))}
      </Group>
      <AddHub open={adding} onClose={() => setAdding(false)} />
      <RenameHub hub={renaming} onClose={() => setRenaming(null)} />
    </>
  );
}

function Field({ label, value, onChange, placeholder, hint, error, mono, autoFocus }: { label: string; value: string; onChange: (v: string) => void; placeholder?: string; hint?: React.ReactNode; error?: string; mono?: boolean; autoFocus?: boolean }) {
  return (
    <label className="block">
      <span className="mb-1 block text-[12px] font-medium text-ink-2">{label}</span>
      <input
        autoFocus={autoFocus}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
        autoComplete="off"
        spellCheck={false}
        aria-invalid={error ? true : undefined}
        className={`h-9 w-full rounded-control bg-field px-3 text-[13px] text-ink shadow-inset-field outline-none placeholder:text-ink-3 focus:shadow-[inset_0_0_0_1px_var(--accent)] ${mono ? "font-mono" : ""}`}
      />
      {error ? <span className="mt-1 block text-[11px] text-red" role="alert">{error}</span> : hint && <span className="mt-1 block text-[11px] text-ink-3">{hint}</span>}
    </label>
  );
}

function AddHub({ open, onClose }: { open: boolean; onClose: () => void }) {
  const [url, setUrl] = useState("");
  const [label, setLabel] = useState("");
  const [error, setError] = useState("");
  useEffect(() => {
    if (!open) return;
    setUrl("");
    setLabel("");
    setError("");
  }, [open]);
  const parsed = url.trim() ? normalizeHubURL(url) : null;
  const save = () => {
    const r = addHub(url, label);
    if ("error" in r) return setError(r.error);
    toast(t("hubs.added", { name: hubTitle(r.list[r.list.length - 1]) }));
    onClose();
  };
  return (
    <Dialog
      open={open}
      onClose={onClose}
      width={440}
      title={t("hubs.add_hub")}
      footer={
        <>
          <Button tone="quiet" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button tone="primary" disabled={!parsed || "error" in parsed} onClick={save}>
            {t("hubs.add")}
          </Button>
        </>
      }
    >
      <form
        className="flex flex-col gap-3"
        onSubmit={(e) => {
          e.preventDefault();
          save();
        }}
      >
        <Field
          label={t("hubs.address")}
          autoFocus
          mono
          value={url}
          onChange={(v) => {
            setUrl(v);
            setError("");
          }}
          placeholder={t("hubs.address_placeholder")}
          error={error || (parsed && "error" in parsed ? parsed.error : "")}
          hint={parsed && "url" in parsed ? t("hubs.will_open", { url: parsed.url }) : t("hubs.address_hint")}
        />
        <Field label={t("hubs.name_optional")} value={label} onChange={setLabel} placeholder={t("hubs.name_placeholder")} />
        <button type="submit" hidden />
      </form>
    </Dialog>
  );
}

function RenameHub({ hub, onClose }: { hub: SavedHub | null; onClose: () => void }) {
  const [label, setLabel] = useState("");
  useEffect(() => setLabel(hub?.label ?? ""), [hub]);
  const save = () => {
    if (hub) renameHub(hub.url, label);
    onClose();
  };
  return (
    <Dialog
      open={hub !== null}
      onClose={onClose}
      width={400}
      title={t("hubs.rename")}
      footer={
        <>
          <Button tone="quiet" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button tone="primary" onClick={save}>
            {t("common.save")}
          </Button>
        </>
      }
    >
      <form
        onSubmit={(e) => {
          e.preventDefault();
          save();
        }}
      >
        <Field label={t("hubs.name")} autoFocus value={label} onChange={setLabel} placeholder={hub?.name || (hub ? hostOf(hub.url) : "")} hint={t("hubs.rename_hint")} />
        <button type="submit" hidden />
      </form>
    </Dialog>
  );
}

// ── Sign-in page ───────────────────────────────────────────

/** On the sign-in page: the other hubs, so a hub not signed in on yet is not a dead end. */
export function OtherHubs() {
  const others = useHubs().filter((h) => h.url !== here());
  if (others.length === 0) return null;
  return (
    <div className="mt-6">
      <div className="mb-1.5 px-1 text-[11.5px] font-medium text-ink-3">{t("hubs.other_hubs")}</div>
      <div className="divide-y divide-line overflow-hidden rounded-card bg-surface shadow-card">
        {others.map((h) => (
          <button key={h.url} type="button" onClick={() => goToHub(h)} className="flex w-full items-center gap-2.5 whitespace-nowrap px-3 py-2.5 text-left transition-colors hover:bg-hover">
            <Icon name="hub" size={14} className="shrink-0 text-ink-3" />
            <span className="min-w-0 flex-1 truncate text-[13px] text-ink">{hubTitle(h)}</span>
            <span className="shrink-0 truncate font-mono text-[11px] text-ink-3">{hostOf(h.url)}</span>
            <Icon name="chevronRight" size={12} stroke={2} className="shrink-0 text-ink-3" />
          </button>
        ))}
      </div>
    </div>
  );
}
