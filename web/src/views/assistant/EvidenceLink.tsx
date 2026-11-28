import { t } from "../../i18n";
import { stripBase } from "../../lib/base";
import { safeWebURL } from "../../lib/format";
import { navigate, parse, type Route } from "../../lib/router";
import { Icon, type IconName } from "../../ui/icons";

/** A validated, display-ready evidence link for a decision card (#14659). */
interface DecisionEvidenceLink {
  href: string;
  label: string;
  icon: IconName;
  external: boolean;
  route?: Route;
}

/** Validates and formats a decision ask's evidence URL: accepts only http(s) URLs, filters redundant self-links to
 *  the to-do's own /todos/<id> page, formats PR/MR/Issue references concisely, uses in-app navigation for same-origin
 *  Persona routes, and opens external URLs safely in a new tab. */
function evidenceLink(raw: string | undefined, todoId: string): DecisionEvidenceLink | undefined {
  const safe = safeWebURL(raw);
  if (!safe) return undefined;
  const parsed = new URL(safe);
  const cleanPath = parsed.pathname.replace(/\/+$/, "");
  const stripped = stripBase(cleanPath).replace(/\/+$/, "");
  const selfEncoded = `/todos/${encodeURIComponent(todoId)}`;
  const selfRaw = `/todos/${todoId}`;
  if (!parsed.search && !parsed.hash && (cleanPath === selfEncoded || cleanPath === selfRaw || stripped === selfEncoded || stripped === selfRaw)) {
    return undefined;
  }
  const sameOrigin = typeof location !== "undefined" && Boolean(location.origin) && location.origin !== "null" && parsed.origin === location.origin;
  const inApp = sameOrigin ? parse(stripped || "/", parsed.search) : undefined;
  const route = inApp && inApp.name !== "not-found" ? inApp : undefined;
  const external = !route;

  let label: string;
  let icon: IconName = "link";
  const pr = /\/pull\/(\d+)(?:\/|$)/.exec(parsed.pathname);
  const mr = /\/(?:-\/)?merge_requests\/(\d+)(?:\/|$)/.exec(parsed.pathname);
  const issue = /\/(?:-\/)?issues\/(\d+)(?:\/|$)/.exec(parsed.pathname);
  if (pr) {
    label = `PR #${pr[1]}`;
    icon = "pullRequest";
  } else if (mr) {
    label = `MR !${mr[1]}`;
    icon = "pullRequest";
  } else if (issue) {
    label = `Issue #${issue[1]}`;
  } else if (external) {
    const host = parsed.hostname.replace(/^www\./, "");
    label = host ? t("assistant-decisionrow.open_link_host", { host }) : t("assistant-decisionrow.open_link");
  } else {
    label = t("assistant-decisionrow.open_link");
  }
  return { href: parsed.href, label, icon, external, route };
}

export function EvidenceLink({ link, todoId, verification = false }: { link?: string; todoId: string; verification?: boolean }) {
  const evidence = evidenceLink(link, todoId);
  if (!evidence || (verification && evidence.route?.name === "session")) return null;
  return (
    <div className="flex min-w-0 items-center">
      <a
        data-decision-link={evidence.external ? "external" : "internal"}
        href={evidence.href}
        target={evidence.external ? "_blank" : undefined}
        rel={evidence.external ? "noopener noreferrer" : undefined}
        onClick={evidence.route ? (e) => {
          if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || e.button !== 0) return;
          e.preventDefault();
          navigate(evidence.route!);
        } : undefined}
        title={evidence.href}
        aria-label={t("assistant-decisionrow.open_link_aria", { label: evidence.label })}
        className="touch-hit inline-flex min-w-0 max-w-full items-center gap-1.5 rounded-control text-label font-medium text-accent-ink hover:underline"
      >
        <Icon name={evidence.icon} size={12} className="shrink-0" />
        <span className="truncate">{evidence.label}</span>
        {evidence.external && <Icon name="external" size={11} className="shrink-0" />}
      </a>
    </div>
  );
}
