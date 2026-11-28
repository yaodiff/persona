import type { ModuleMailboxInboxSwitchProps, ModuleMailboxSettingsProps } from "../../core/module-host";
import { useEffect, useId, useRef, useState } from "react";
import { managerHeaderFor, sameManager, useChosenManager } from "../../lib/manager-choice";
import { clientIDError, EMPTY_MAIL_FORM, MAIL_KINDS, mailAccountLine, mailCodeGuide, mailErrors, mailProviderBadge, mailRequest, signsInOnPhone, type MailForm, type MicrosoftConfig } from "./account-form";
import { mailSummary, type MailAccount } from "../../lib/agent-settings";
import { t } from "../../i18n";
import { ExternalSourceSettingsEntry } from "../../features/external-sources/ExternalSourceSettingsEntry";
import { api, enc, errorText } from "../../lib/api";
import { listConnectors, setConnector, type HubConnector } from "../../lib/connectors";
import { Button, copyText, cx, Dialog, IconButton, Loading, Spinner, toast } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { Group, Pill, RowConfirm, Switch } from "../../views/settings/SettingsParts";

export function Mail({ hideInboxSwitch = false, manager, reloadKey, onHasAccounts }: ModuleMailboxSettingsProps = {}) {
  const [accounts, setAccounts] = useState<MailAccount[] | null>(null);
  const [error, setError] = useState("");
  const [adding, setAdding] = useState(false);
  const [confirm, setConfirm] = useState<string | null>(null);
  const [deleting, setDeleting] = useState<string | null>(null);

  const read = useRef(0);
  const load = async () => {
    const n = ++read.current;
    try {
      const r = await api<{ accounts: MailAccount[] }>("/mail/accounts");
      if (n === read.current) {
        setAccounts(r.accounts ?? []);
        setError("");
      }
    } catch (e) {
      if (n === read.current) setError(errorText(e));
    }
  };
  useEffect(() => void load(), [reloadKey]);
  useEffect(() => onHasAccounts?.(!!accounts?.length), [accounts, onHasAccounts]);

  const remove = async (a: MailAccount) => {
    setDeleting(a.id);
    try {
      await api(`/mail/accounts/${enc(a.id)}`, { method: "DELETE" });
      toast(t("settings-connectors.mail_deleted", { email: a.email }), "ok");
      setConfirm(null);
      await load();
    } catch (e) {
      toast(t("settings-connectors.delete_failed", { error: errorText(e) }), "error");
    } finally {
      setDeleting(null);
    }
  };

  return (
    <>
      <Group
        anchor="mail"
        title={
          <span className="flex items-center gap-2">
            {t("settings-connectors.mail")}
            {accounts && <span className="font-normal">{mailSummary(accounts)}</span>}
          </span>
        }
        right={
          <Button tone="quiet" icon="plus" onClick={() => setAdding(true)} className="-mb-1 h-7">
            {t("settings-connectors.add_mail")}
          </Button>
        }
        footer={t("settings-connectors.mail_footer")}
      >
        {!accounts && !error && (
          <div className="px-4 py-3">
            <Loading />
          </div>
        )}
        {error && <p className="px-4 py-3 text-[12.5px] text-red">{t("settings-connectors.mail_load_failed", { error })}</p>}
        {accounts?.length === 0 && <p className="px-4 py-3 text-[12.5px] text-ink-3">{t("settings-connectors.mail_empty")}</p>}
        {accounts?.map((a) => (
          <div key={a.id} className="flex items-center gap-3 px-4 py-2.5">
            <span className={cx("flex size-8 shrink-0 items-center justify-center rounded-[8px]", a.status === "error" ? "bg-red-tint text-red" : "bg-accent-tint text-accent-ink")}>
              <Icon name="mail" size={15} />
            </span>
            <div className="min-w-0 flex-1">
              <div className="flex min-w-0 items-center gap-2">
                <span className="truncate text-[13px] text-ink">{a.email}</span>
                {mailProviderBadge(a) && <Pill>{mailProviderBadge(a)}</Pill>}
                {a.status === "error" && <Pill tone="red">{t("settings-connectors.has_error")}</Pill>}
              </div>
              <div className={cx("mt-0.5 truncate text-[11.5px]", a.status === "error" ? "text-red" : "text-ink-3")} title={mailAccountLine(a)}>
                {mailAccountLine(a)}
              </div>
            </div>
            <RowConfirm
              open={confirm === a.id}
              confirm={t("settings-connectors.confirm_delete")}
              busy={deleting === a.id}
              onCancel={() => setConfirm(null)}
              onConfirm={() => void remove(a)}
              trigger={<IconButton icon="trash" label={t("settings-connectors.delete_account", { email: a.email })} onClick={() => setConfirm(a.id)} />}
            />
          </div>
        ))}
        {!hideInboxSwitch && accounts && accounts.length > 0 && <MailInboxSwitch manager={manager} />}
      </Group>
      {!!accounts?.length && <ExternalSourceSettingsEntry provider="mail" manager={manager}
        accounts={accounts.map((account) => ({ channel: account.id, name: account.email }))} />}
      <AddMail
        open={adding}
        onClose={() => setAdding(false)}
        onAdded={(email) => {
          setAdding(false);
          toast(t("settings-connectors.mail_added", { email }), "ok");
          void load();
        }}
      />
    </>
  );
}

/** Mail's Add to Inbox (#5307): the same switch as Settings › Inbox Sources' mail page; off, no account's mail comes into
 *  the Inbox (search, replies and the knowledge base go on). Only a hub that has the switch lists it (`inbox`). */
export function MailInboxSwitch({ manager, grouped }: ModuleMailboxInboxSwitchProps = {}) {
  const picked = useChosenManager();
  const [mail, setMail] = useState<HubConnector | null>(null);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    let alive = true;
    const owner = manager ?? managerHeaderFor("/connectors/mail") ?? picked ?? undefined;
    setMail(null);
    listConnectors(true).then(
      (list) => alive && setMail(list.find((c) => c.id === "mail" && sameManager(c.manager, owner)) ?? null),
      () => {}, // an older hub, or none to say: no switch
    );
    return () => void (alive = false);
  }, [manager, picked]);
  if (mail?.inbox == null) return null;
  const title = t("settings-hubconnector.to_inbox");
  const change = async (v: boolean) => {
    setBusy(true);
    try {
      const c = await setConnector("mail", { inbox: v }, mail.manager);
      setMail((current) => current === mail ? { ...mail, inbox: c.inbox ?? v } : current);
    } catch (e) {
      toast(t("settings-connectors.mail_inbox_failed", { error: errorText(e) }), "error");
    } finally {
      setBusy(false);
    }
  };
  const row = (
    <div className="flex items-center gap-3 px-4 py-2.5">
      <span className="min-w-0 flex-1 truncate text-[13px] text-ink">{title}</span>
      {busy && <Spinner />}
      <Switch on={mail.inbox} disabled={busy} label={title} onChange={(v) => void change(v)} />
    </div>
  );
  return grouped ? <Group>{row}</Group> : row;
}

const fieldClass = "h-9 w-full rounded-control bg-field px-3 text-[13px] text-ink outline-none placeholder:text-ink-3 focus:shadow-[inset_0_0_0_1px_var(--accent)] disabled:opacity-60";

// "Add mail account" (iOS AddMailAccountView): Lark or any IMAP server with a password / app password; the hub logs in to IMAP and SMTP
// before it saves, so a wrong password comes back as an error here and nothing is stored.
function AddMail({ open, onClose, onAdded }: { open: boolean; onClose: () => void; onAdded: (email: string) => void }) {
  const [f, setF] = useState<MailForm>(EMPTY_MAIL_FORM);
  const [tried, setTried] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const errorId = useId(); // field k's error line is `${errorId}-${k}`
  // false: this hub has no Google OAuth client, so Gmail goes in with an app password. An older hub (no such route)
  // or a failed read: as before, Gmail on the phone.
  const [googleSignIn, setGoogleSignIn] = useState(true);
  useEffect(() => {
    if (open) {
      setF(EMPTY_MAIL_FORM);
      setTried(false);
      setError("");
      api<{ configured: boolean }>("/mail/oauth/google/config")
        .then((r) => setGoogleSignIn(r.configured !== false))
        .catch(() => setGoogleSignIn(true));
    }
  }, [open]);

  const gmailAppPassword = f.kind === "gmail" && !googleSignIn;
  const errors = mailErrors(f, gmailAppPassword);
  const req = mailRequest(f, gmailAppPassword);
  const guide = mailCodeGuide(f);
  const shown = tried ? errors : {};
  const set = (k: keyof MailForm) => (e: React.ChangeEvent<HTMLInputElement>) => setF((x) => ({ ...x, [k]: e.target.value }));

  const save = async () => {
    setTried(true);
    if (!req || Object.keys(errors).length) return;
    setBusy(true);
    setError("");
    try {
      await api("/mail/accounts", { method: "POST", json: req });
      onAdded(req.email);
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  };

  const field = (k: keyof MailForm, label: string, opts: { placeholder?: string; type?: string; mono?: boolean; auto?: string } = {}) => (
    <label className="block min-w-0">
      <span className="mb-1 block text-[12px] font-medium text-ink-2">{label}</span>
      <input
        value={f[k]}
        onChange={set(k)}
        type={opts.type ?? "text"}
        placeholder={opts.placeholder}
        autoComplete={opts.auto ?? "off"}
        spellCheck={false}
        disabled={busy}
        aria-invalid={!!shown[k]}
        aria-describedby={shown[k] ? `${errorId}-${k}` : undefined}
        className={cx(fieldClass, opts.mono && "font-mono", shown[k] ? "shadow-[inset_0_0_0_1px_var(--red)]" : "shadow-inset-field")}
      />
      {shown[k] && <span id={`${errorId}-${k}`} className="mt-1 block text-[11px] text-red">{shown[k]}</span>}
    </label>
  );

  return (
    <Dialog
      open={open}
      onClose={() => !busy && onClose()}
      width={460}
      title={t("settings-connectors.add_mail_title")}
      footer={
        <>
          <Button tone="quiet" onClick={onClose} disabled={busy}>
            {t("common.cancel")}
          </Button>
          {(!signsInOnPhone(f.kind) || gmailAppPassword) && (
            <Button tone="primary" busy={busy} onClick={() => void save()}>
              {busy ? t("settings-connectors.connecting") : t("settings-connectors.connect")}
            </Button>
          )}
        </>
      }
    >
      <div className="flex flex-col gap-3">
        <div className="flex gap-0.5 rounded-control bg-inset p-0.5 shadow-hairline" role="radiogroup" aria-label={t("settings-connectors.mail_kind")}>
          {MAIL_KINDS.map((k) => (
            <button
              key={k.k}
              type="button"
              role="radio"
              aria-checked={f.kind === k.k}
              disabled={busy}
              onClick={() => (setF((x) => ({ ...x, kind: k.k })), setTried(false), setError(""))}
              className={cx(
                "flex h-7 flex-1 items-center justify-center whitespace-nowrap rounded-[7px] px-2.5 text-[12.5px] transition-[background-color,color] duration-150",
                f.kind === k.k ? "bg-surface font-medium text-ink shadow-btn" : "text-ink-3 hover:text-ink-2",
              )}
            >
              {k.label}
            </button>
          ))}
        </div>

        {f.kind === "gmail" && !gmailAppPassword ? (
          <Note title={t("settings-connectors.gmail_title")} body={t("settings-connectors.gmail_body")} />
        ) : f.kind === "outlook" ? (
          <>
            <Note title={t("settings-connectors.outlook_title")} body={t("settings-connectors.outlook_body")} />
            <OutlookClient />
          </>
        ) : (
          <form
            className="flex flex-col gap-3"
            onSubmit={(e) => {
              e.preventDefault();
              void save();
            }}
          >
            {gmailAppPassword && <Note title={t("settings-connectors.gmail_not_configured_title")} body={t("settings-connectors.gmail_not_configured_body")} />}
            {field("email", gmailAppPassword ? t("settings-connectors.gmail_address") : t("settings-connectors.email"), { placeholder: "me@example.com", type: "email", auto: "email" })}
            {field("password", t(f.kind === "feishu" ? "settings-connectors.password_lark" : gmailAppPassword ? "settings-connectors.gmail_app_password" : "settings-connectors.password"), { type: "password", auto: "new-password" })}
            {guide ? (
              <div className="flex flex-col items-start gap-1.5 text-[11.5px] leading-relaxed text-ink-3">
                <p>{t(guide.provider === "qq" ? "settings-connectors.qq_code_steps" : "settings-connectors.netease_code_steps")}</p>
                <a href={guide.webMail} target="_blank" rel="noopener noreferrer" className="whitespace-nowrap font-medium text-accent-ink hover:underline">
                  {t(guide.provider === "qq" ? "settings-connectors.qq_open_web" : "settings-connectors.netease_open_web")}
                </a>
              </div>
            ) : (
              <p className="text-[11.5px] leading-relaxed text-ink-3">
                {t(f.kind === "feishu" ? "settings-connectors.lark_hint" : gmailAppPassword ? "settings-connectors.gmail_app_password_hint" : "settings-connectors.imap_hint")}
              </p>
            )}
            {f.kind === "imap" && (
              <fieldset className="flex flex-col gap-3 rounded-[10px] bg-inset p-3 shadow-hairline">
                <legend className="sr-only">{t("settings-connectors.servers")}</legend>
                <p className="text-[12px] font-medium text-ink-2">{t("settings-connectors.servers_optional")}</p>
                <div className="grid grid-cols-[1fr_88px] gap-2">
                  {field("imapHost", t("settings-connectors.imap_server"), { placeholder: "imap.example.com", mono: true })}
                  {field("imapPort", t("settings-connectors.port"), { placeholder: "993", mono: true })}
                  {field("smtpHost", t("settings-connectors.smtp_server"), { placeholder: "smtp.example.com", mono: true })}
                  {field("smtpPort", t("settings-connectors.port"), { placeholder: "465", mono: true })}
                </div>
                <p className="text-[11px] text-ink-3">{t("settings-connectors.smtp_tls")}</p>
              </fieldset>
            )}
            {error && <p className="rounded-[8px] bg-red-tint px-2.5 py-1.5 text-[12px] break-words text-red">{t("settings-connectors.add_failed", { error })}</p>}
            <button type="submit" hidden />
          </form>
        )}
      </div>
    </Dialog>
  );
}

/** Gmail's and Outlook's note in "Add mail account": they sign in on the provider's page, which returns to the phone app. */
function Note({ title, body }: { title: string; body: string }) {
  return (
    <div className="flex gap-3 rounded-[10px] bg-inset px-3.5 py-3 shadow-hairline">
      <Icon name="info" size={15} className="mt-0.5 shrink-0 text-ink-3" />
      <div className="text-[12.5px] leading-relaxed text-ink-2">
        <p className="font-medium text-ink">{title}</p>
        <p className="mt-1">{body}</p>
      </div>
    </div>
  );
}

const OUTLOOK_STEPS = [1, 2, 3, 4, 5, 6].map((n) => `settings-connectors.outlook_step${n}`);

/**
 * Outlook's client ID (iOS OutlookSignInSection's registration): the app registered in Microsoft Entra that this hub
 * signs in with. Once the hub has one this says so (and where it came from) with "Change"; else the steps, the callback to
 * register and the field. The sign-in itself stays on the phone.
 */
export function OutlookClient() {
  const [config, setConfig] = useState<MicrosoftConfig | null>(null);
  const [failed, setFailed] = useState("");
  const [editing, setEditing] = useState(false);
  const [id, setId] = useState("");
  const [tried, setTried] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const problemId = useId();
  useEffect(() => {
    api<MicrosoftConfig>("/mail/oauth/microsoft/config").then(setConfig, (e) => setFailed(errorText(e)));
  }, []);

  const problem = tried ? clientIDError(id) : "";
  const save = async () => {
    setTried(true);
    if (clientIDError(id)) return;
    setBusy(true);
    setError("");
    try {
      const c = await api<MicrosoftConfig>("/mail/oauth/microsoft/config", { method: "PUT", json: { client_id: id.trim() } });
      setConfig(c);
      setEditing(false);
      toast(t("settings-connectors.client_id_saved"), "ok");
    } catch (e) {
      setError(t("settings-connectors.client_id_save_failed", { error: errorText(e) }));
    } finally {
      setBusy(false);
    }
  };

  if (failed) return <p className="rounded-[8px] bg-red-tint px-2.5 py-1.5 text-[12px] break-words text-red">{t("settings-connectors.outlook_config_failed", { error: failed })}</p>;
  if (!config) return <div className="flex justify-center py-2"><Spinner /></div>;
  if (config.configured && !editing) {
    const source = config.source === "config" ? t("settings-connectors.outlook_source_config") : config.source === "app" ? t("settings-connectors.outlook_source_app") : "";
    return (
      <div className="flex items-center gap-3 rounded-[10px] bg-inset px-3.5 py-2.5 shadow-hairline">
        <Icon name="check" size={14} stroke={2.4} className="shrink-0 text-green" />
        <div className="min-w-0 flex-1">
          <p className="text-[12.5px] leading-snug text-ink">{t("settings-connectors.outlook_ready")}</p>
          <p className="mt-0.5 truncate font-mono text-[11px] text-ink-3" title={config.client_id}>
            {config.client_id}
          </p>
          {source && <p className="mt-0.5 truncate text-[11px] text-ink-3">{source}</p>}
        </div>
        <Button tone="quiet" onClick={() => (setId(config.client_id ?? ""), setTried(false), setError(""), setEditing(true))}>
          {t("settings-connectors.outlook_change")}
        </Button>
      </div>
    );
  }
  return (
    <form
      className="flex flex-col gap-3 rounded-[10px] bg-inset p-3 shadow-hairline"
      onSubmit={(e) => {
        e.preventDefault();
        void save();
      }}
    >
      <div>
        <p className="text-[12px] font-medium text-ink-2">{t("settings-connectors.outlook_register_title")}</p>
        <p className="mt-1 text-[11.5px] leading-relaxed text-ink-3">{t("settings-connectors.outlook_register_intro")}</p>
        <ol className="mt-1 list-decimal pl-5 text-[11.5px] leading-relaxed text-ink-3">
          {OUTLOOK_STEPS.map((k) => <li key={k}>{t(k)}</li>)}
        </ol>
      </div>
      <div className="min-w-0">
        <span className="mb-1 block text-[12px] font-medium text-ink-2">{t("settings-connectors.redirect_uri")}</span>
        <div className="flex items-center gap-1">
          <code className="h-9 min-w-0 flex-1 truncate rounded-control bg-field px-3 font-mono text-[12.5px] leading-9 text-ink shadow-inset-field select-all">{config.redirect_uri}</code>
          <IconButton icon="copy" label={t("settings-connectors.copy_redirect")} onClick={() => void copyText(config.redirect_uri)} />
        </div>
      </div>
      <label className="block min-w-0">
        <span className="mb-1 block text-[12px] font-medium text-ink-2">{t("settings-connectors.client_id")}</span>
        <input
          value={id}
          onChange={(e) => setId(e.target.value)}
          placeholder="xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx"
          autoComplete="off"
          spellCheck={false}
          disabled={busy}
          aria-invalid={!!problem}
          aria-describedby={problem ? problemId : undefined}
          className={cx(fieldClass, "font-mono", problem ? "shadow-[inset_0_0_0_1px_var(--red)]" : "shadow-inset-field")}
        />
        {problem && <span id={problemId} className="mt-1 block text-[11px] text-red">{problem}</span>}
      </label>
      {error && <p className="rounded-[8px] bg-red-tint px-2.5 py-1.5 text-[12px] break-words text-red">{error}</p>}
      <div className="flex justify-end gap-2">
        {config.configured && (
          <Button tone="quiet" disabled={busy} onClick={() => setEditing(false)}>
            {t("settings-connectors.outlook_change_cancel")}
          </Button>
        )}
        <Button tone="primary" type="submit" busy={busy}>
          {t("settings-connectors.save_client_id")}
        </Button>
      </div>
    </form>
  );
}
