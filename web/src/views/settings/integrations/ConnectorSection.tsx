// Connector connection and inbox/sync sections shared by built-in connectors and extensions in IntegrationDetail (#14847).
import { t } from "../../../i18n";
import {
  connectorState, connectorTone, githubOptions, isEnabled, isSignedIn, larkcliOptions,
  signInFooter, signsInHere, undescribed,
  type ConnectorTone, type HubConnector,
} from "../../../lib/connectors";
import { ago } from "../../../lib/format";
import { Button, cx } from "../../../ui/atoms";
import {
  DataGroup, FeedsGroup, GitHubInbox, GitHubSections, IMessageSections,
  LarkCLIDocuments, LarkCLITriage, SignInSteps, SwitchGroup, useActions,
} from "../HubConnector";
import { Group, Row, SettingsHint } from "../SettingsParts";

const DOT: Record<ConnectorTone, string> = {
  green: "bg-green",
  orange: "bg-orange",
  red: "bg-red",
  idle: "bg-line-strong",
};

/** Whether to show the hub-side `manager.json` credential hint for a connector block. */
export function showHubSideNote(c: HubConnector, authManagedByHost = false): boolean {
  return !authManagedByHost && c.id !== "imessage" && !signsInHere(c) && !undescribed(c);
}

/** Connection status, sign-in, fix guide, and sign-out/delete-data for one hub connector. */
export function ConnectorConnectionBlock({
  connector: c,
  reload,
  loading = false,
  showStatusCard = true,
  authManagedByHost = false,
  readOnly = false,
}: {
  connector: HubConnector;
  reload: () => Promise<void>;
  loading?: boolean;
  showStatusCard?: boolean;
  readOnly?: boolean;
  /**
   * True when the host integration already renders interactive top-level authentication UI
   * (such as an extension's `AuthGroup`) in the same connection section, so the hub-side
   * `manager.json` credential hint is suppressed.
   */
  authManagedByHost?: boolean;
}) {
  const a = useActions(c.id, reload);
  const rateLimited = c.status === "rate_limited";

  return (
    <>
      {showStatusCard && (
        <Group title={t("settings-integrations.connector_status_title")}>
          <Row label={c.account || c.name}>
            <span className="flex items-center gap-1.5 text-label text-ink-2">
              <span aria-hidden className={cx("size-1.5 shrink-0 rounded-full", DOT[connectorTone(c)])} />
              <span>{connectorState(c)}</span>
            </span>
          </Row>
          {c.via && <Row label={t("settings-hubconnector.signed_in_via")}>{c.via}</Row>}
          {c.last_sync && <Row label={t("settings-hubconnector.last_sync")}>{ago(c.last_sync)}</Row>}
          {c.error && <SettingsHint alert={!rateLimited}>{c.error}</SettingsHint>}
        </Group>
      )}

      {!readOnly && showHubSideNote(c, authManagedByHost) && (
        <p className="-mt-3 mb-5 px-1 text-submeta leading-relaxed text-ink-3">
          {t("settings-hubconnector.hub_side_note")}
        </p>
      )}

      {!readOnly && c.id === "imessage" && <IMessageSections />}

      {!readOnly && c.id === "github" && signsInHere(c) && <GitHubSections c={c} reload={reload} hideFeeds />}

      {!readOnly && c.id !== "github" && (signsInHere(c) || undescribed(c)) && (
        <>
          <SwitchGroup c={c} a={a} />
          {isEnabled(c) && !isSignedIn(c) && (signsInHere(c) ? (
            <SignInSteps c={c} reload={reload} footer={signInFooter(c)} />
          ) : (
            <Group>
              <p className="px-4 py-3 text-meta text-ink-3">{t("settings-hubconnector.starting", { name: c.name })}</p>
            </Group>
          ))}
          <DataGroup c={c} a={a} logout />
        </>
      )}

      {c.status !== "ok" && !!c.guide?.length && (
        <Group title={t("settings-hubconnector.fix_title")}>
          {c.guide.map((step, i) => (
            <div key={i} className="flex items-baseline gap-3 px-4 py-2.5">
              <span className="w-4 shrink-0 text-label font-semibold text-accent-ink tabular-nums">{i + 1}</span>
              <span className="min-w-0 text-meta leading-relaxed text-ink select-text">{step}</span>
            </div>
          ))}
          <div className="px-4 py-2.5">
            <Button tone="secondary" icon="retry" onClick={() => void reload()} busy={loading}>
              {t("settings-hubconnector.recheck")}
            </Button>
          </div>
        </Group>
      )}
    </>
  );
}

/** Whether `c` contributes any inbox or knowledge-base sync controls. */
export function connectorHasInbox(c: HubConnector): boolean {
  if (c.archive !== undefined || c.inbox !== undefined) return true;
  if (c.id === "github" && githubOptions(c.options)) return true;
  if (c.id === "larkcli" && larkcliOptions(c.options)) return true;
  return false;
}

/** Inbox & Sync controls (archive/inbox switches, GitHub inbox options, lark-cli triage & documents). */
export function ConnectorInboxBlock({
  connector: c,
  reload,
  onSaved,
}: {
  connector: HubConnector;
  reload: () => Promise<void>;
  onSaved: (next: HubConnector) => void;
}) {
  const a = useActions(c.id, reload);
  const gh = c.id === "github" ? githubOptions(c.options) : null;
  const lc = c.id === "larkcli" ? larkcliOptions(c.options) : null;

  return (
    <>
      <FeedsGroup c={c} a={a} />
      {gh && <GitHubInbox key={JSON.stringify(c.options)} options={gh} onSaved={onSaved} />}
      {lc && <LarkCLITriage key={`t:${JSON.stringify(c.options)}`} options={lc} onSaved={onSaved} />}
      {lc && <LarkCLIDocuments key={JSON.stringify(c.options)} options={lc} onSaved={onSaved} />}
    </>
  );
}
