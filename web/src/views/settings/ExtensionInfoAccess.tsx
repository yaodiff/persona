// What an extension may read of the user's personal information (#13500, docs/assistant/health.md "Using information
// by scene", GET / PUT /api/v1/assistant/extension-trust): "This is my own extension" lets it read everything (the user's
// own memory extension, say); otherwise it reads personal information only of the kinds turned on here. Shown on the
// extension's page; an assistant without these grants (an older one) shows nothing.
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { t } from "../../i18n";
import { ApiError, errorText, isAbort } from "../../lib/api";
import { unsupported, type ManagerList } from "../../lib/managers";
import { currentScopedAssistantRoster, ownedAssistant, verifyScopedAssistantRoster, type ScopedAssistantClient } from "../../lib/scoped-assistant-api";
import { extensionInfoAccessMode, listScopedExtensionTrust, setScopedExtensionTrust } from "../../lib/scoped-extension-trust";
import { INFO_CATEGORIES, trustOf, type ExtensionTrust } from "../../lib/tracks";
import { Loading, Switch, toast } from "../../ui/atoms";
import { categoryLabel } from "../spaces/labels";
import { Group, Row, RowFailed } from "./SettingsParts";
import { useScopedAssistant } from "./integrations/useScopedAssistant";

type Props = { client: ScopedAssistantClient; extension: string; editable?: boolean };

export function ExtensionInfoAccess({ client: owner, ...props }: Props) {
  // Trust has its own resource key so a grant change does not invalidate configuration reads.
  const { client, routing, roster } = useScopedAssistant(ownedAssistant(owner.managerId), "extension-trust", props.extension);
  const manager = roster?.managers.find((m) => m.id === client.managerId);
  const scopeKey = JSON.stringify([client.key, routing, manager?.since, extensionInfoAccessMode(roster, client.managerId)]);
  return <GrantScope key={scopeKey} {...props} client={client} roster={currentScopedAssistantRoster()} observed={roster} />;
}

function GrantScope({ roster, observed, ...props }: Props & { roster: ManagerList | null; observed: ManagerList | null }) {
  const [checked, setChecked] = useState<ManagerList | null>();
  useEffect(() => {
    if (roster) return;
    let active = true;
    void verifyScopedAssistantRoster().then((verified) => {
      if (active) setChecked(verified);
    }, () => {});
    return () => { active = false; };
  }, [roster, observed]);
  const known = roster ?? checked;
  const mode = known === undefined ? undefined : extensionInfoAccessMode(known, props.client.managerId);
  return mode && <GrantCard key={mode} {...props} scoped={mode === "scoped"} />;
}

type AccessState =
  | { kind: "loading" | "unsupported" | "unconfirmed" }
  | { kind: "failed"; error: unknown }
  | { kind: "confirmed" | "saving"; trust: ExtensionTrust };

function GrantCard({ extension, editable = true, client, scoped }: Props & { scoped: boolean }) {
  const [state, setState] = useState<AccessState>({ kind: "loading" });
  const latest = useRef(state);
  const sequence = useRef(0);
  const update = useCallback((next: AccessState) => {
    latest.current = next;
    setState(next);
  }, []);
  const load = useCallback(async (keepSaving = false) => {
    const request = ++sequence.current;
    if (!keepSaving) update({ kind: "loading" });
    try {
      const items = await listScopedExtensionTrust(client);
      if (request !== sequence.current) return;
      update(items ? { kind: "confirmed", trust: trustOf(items, extension) } : { kind: scoped ? "unconfirmed" : "unsupported" });
    } catch (error) {
      if (request !== sequence.current) return;
      if (!scoped && unsupported(error)) update({ kind: "unsupported" });
      else if (isAbort(error) || error instanceof ApiError && error.status === 403) update({ kind: "unconfirmed" });
      else update({ kind: "failed", error });
    }
  }, [client, extension, scoped, update]);

  useLayoutEffect(() => {
    const stop = client.subscribe(() => {
      if (latest.current.kind !== "saving") void load();
    });
    return () => {
      // Retire both read and write callbacks at commit when this resource is replaced.
      sequence.current++;
      stop();
    };
  }, [client, load]);
  useEffect(() => { void load(); }, [load]);

  const save = async (next: ExtensionTrust) => {
    const previous = latest.current;
    if (!editable || previous.kind !== "confirmed" || previous !== state) return;
    const request = ++sequence.current;
    update({ kind: "saving", trust: next });
    try {
      await setScopedExtensionTrust(client, extension, next);
    } catch (error) {
      if (request === sequence.current) {
        update({ kind: "saving", trust: previous.trust });
        if (!isAbort(error)) toast(t("settings-extensioninfoaccess.save_failed", { error: errorText(error) }), "error");
      }
    } finally {
      // A failed response can leave the write's outcome unknown. Recheck before enabling edits again.
      if (request === sequence.current) void load(true);
    }
  };

  if (state.kind === "unsupported") return null;
  const trust = state.kind === "confirmed" || state.kind === "saving" ? state.trust : null;
  const owner = trust?.trust === "owner";
  const disabled = state.kind === "saving" || !editable;
  return (
    <div data-extension-info-access>
      <Group title={t("settings-extensioninfoaccess.title")} footer={trust ? t(owner ? "settings-extensioninfoaccess.owner_footer" : "settings-extensioninfoaccess.footer") : undefined}>
        {state.kind === "loading" && <Row label={<Loading />} />}
        {(state.kind === "failed" || state.kind === "unconfirmed") && (
          <RowFailed
            text={state.kind === "failed" ? t("settings-extensioninfoaccess.load_failed", { error: errorText(state.error) }) : t("settings-extensioninfoaccess.unconfirmed")}
            retry={t("settings-extensions.retry")}
            onRetry={() => void load()}
          />
        )}
        {trust && <>
          <Row label={t("settings-extensioninfoaccess.owner")} hint={t("settings-extensioninfoaccess.owner_hint")} wrapHint>
            <Switch on={owner} disabled={disabled} label={t("settings-extensioninfoaccess.owner")} onChange={(on) => void save({ trust: on ? "owner" : "third_party", categories: trust.categories })} />
          </Row>
          {!owner && INFO_CATEGORIES.map((c) => {
            const on = trust.categories.includes(c);
            const label = categoryLabel(c);
            return (
              <Row key={c} label={label}>
                <Switch
                  on={on}
                  disabled={disabled}
                  label={t("settings-extensioninfoaccess.category", { name: label })}
                  onChange={(next) => void save({ trust: "third_party", categories: next ? [...trust.categories, c] : trust.categories.filter((x) => x !== c) })}
                />
              </Row>
            );
          })}
        </>}
      </Group>
    </div>
  );
}
