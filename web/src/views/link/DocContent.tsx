// How a Feishu document reads in the panel: the Markdown export through the page's own Markdown look (prose-j), or
// the JSON tree drawn as the same HTML (FeishuDocRenderer: the blocks Feishu documents are made of; the rare ones
// say so and point at Feishu). Content colors are left out: the page only uses its own tokens.
import ReactMarkdown from "react-markdown";
import remarkGfm from "../../ui/remark-gfm-parse";
import { bytes, safeWebURL } from "../../lib/format";
import { clickLink, linkHref, prefetchLink } from "../../lib/links";
import { useLinkManager } from "../../lib/manager-choice";
import { linkify, markdownComponents, markdownUrl } from "../../ui/Markdown";
import { Icon } from "../../ui/icons";
import { formatDate, t } from "../../i18n";
import { attr, BR, mediaURL, num, orderedNumbers, plainText, type DocNode } from "./feishu-doc";

// ── Markdown ──

interface MdNode {
  type: string;
  value?: string;
  children?: MdNode[];
}

// The export's <br> (cells, mostly) arrive as BR; markdown here renders no raw HTML, so they become real breaks.
function remarkBreaks() {
  const walk = (node: MdNode) => {
    if (!node.children) return;
    const out: MdNode[] = [];
    for (const c of node.children) {
      if (c.type === "text" && c.value?.includes(BR)) {
        c.value.split(BR).forEach((part, i) => {
          if (i) out.push({ type: "break" });
          if (part) out.push({ type: "text", value: part });
        });
      } else {
        walk(c);
        out.push(c);
      }
    }
    node.children = out;
  };
  return (tree: MdNode) => walk(tree);
}

export function DocMarkdown({ markdown }: { markdown: string }) {
  return (
    <div className="prose-j">
      <ReactMarkdown remarkPlugins={[remarkGfm, remarkBreaks]} components={markdownComponents} urlTransform={markdownUrl}>
        {linkify(markdown)}
      </ReactMarkdown>
    </div>
  );
}

// ── JSON tree ──

export function DocTree({ doc }: { doc: DocNode }) {
  return (
    <div className="prose-j">
      <Blocks nodes={doc.content ?? []} />
    </div>
  );
}

function Blocks({ nodes }: { nodes: DocNode[] }) {
  const numbers = orderedNumbers(nodes);
  return (
    <>
      {nodes.map((n, i) => (
        <Block key={i} node={n} number={numbers[i]} />
      ))}
    </>
  );
}

const INLINE = new Set(["text", "mention", "hardBreak", "equation", "statusTag"]);

/** A container whose only child is a paragraph reads as that paragraph's text (list items, table cells). */
function Tight({ nodes }: { nodes: DocNode[] }) {
  if (nodes.length === 1 && nodes[0].type === "paragraph") return <Inline nodes={nodes[0].content ?? []} />;
  return <Blocks nodes={nodes} />;
}

function align(n: DocNode): React.CSSProperties | undefined {
  const a = attr(n, "align");
  return a === "center" || a === "right" ? { textAlign: a } : undefined;
}

function Block({ node, number }: { node: DocNode; number?: number }) {
  const kids = node.content ?? [];
  const manager = useLinkManager();
  switch (node.type) {
    case "paragraph":
      return kids.length ? (
        <p style={align(node)} data-block={attr(node, "id")}>
          <Inline nodes={kids} />
        </p>
      ) : (
        <div aria-hidden className="h-2" />
      );
    case "heading": {
      const level = num(node, "level") ?? 1;
      const Tag = (level <= 1 ? "h1" : level === 2 ? "h2" : level === 3 ? "h3" : "h4") as "h1" | "h2" | "h3" | "h4";
      return (
        <Tag style={align(node)} data-block={attr(node, "id")}>
          <Inline nodes={kids} />
        </Tag>
      );
    }
    case "bulletList":
      return (
        <ul>
          {kids.map((item, i) => (
            <ListItem key={i} item={item} />
          ))}
        </ul>
      );
    case "orderedList":
      return (
        <ol start={number ?? 1}>
          {kids.map((item, i) => (
            <ListItem key={i} item={item} />
          ))}
        </ol>
      );
    case "taskList":
      return (
        <ul className="list-none! pl-0.5!">
          {kids.map((item, i) => (
            <ListItem key={i} item={item} task />
          ))}
        </ul>
      );
    case "listItem":
    case "mindnoteItem":
      return (
        <ul>
          <ListItem item={node} />
        </ul>
      );
    case "blockquote":
      return (
        <blockquote>
          <Blocks nodes={kids} />
        </blockquote>
      );
    case "callout":
      return (
        <div className="my-3 flex gap-2.5 rounded-card bg-inset px-3 py-2.5 shadow-hairline">
          {attr(node, "emoji") && <span className="shrink-0 text-[16px] leading-[1.6]">{attr(node, "emoji")}</span>}
          <div className="min-w-0 flex-1 [&>:first-child]:mt-0 [&>:last-child]:mb-0">
            <Blocks nodes={kids} />
          </div>
        </div>
      );
    case "columns":
      return (
        <div className="flex flex-col gap-1">
          {kids.map((col, i) => (
            <div key={i}>
              <Blocks nodes={col.content ?? []} />
            </div>
          ))}
        </div>
      );
    case "column":
    case "synced":
    case "agenda":
    case "agendaContent":
    case "doc":
    case "mindnote":
    case "slideNotes":
      return <Blocks nodes={kids} />;
    case "agendaItem":
      return (
        <div className="my-3 rounded-card bg-inset px-3 py-2 shadow-hairline [&>:first-child]:mt-0 [&>:last-child]:mb-0">
          <Blocks nodes={kids} />
        </div>
      );
    case "agendaTitle":
      return (
        <h4>
          <Inline nodes={kids} />
        </h4>
      );
    case "horizontalRule":
      return <hr />;
    case "codeBlock":
    case "mermaid":
    case "plantuml": {
      const lang = node.type === "mermaid" ? t("link-doccontent.mermaid") : node.type === "plantuml" ? t("link-doccontent.plantuml") : attr(node, "language");
      return (
        <div>
          {lang && lang.toLowerCase() !== "plaintext" && lang.toLowerCase() !== "text" && <div className="mt-3 -mb-1 text-[11.5px] text-ink-3">{lang}</div>}
          <pre>
            <code>{plainText(node)}</code>
          </pre>
        </div>
      );
    }
    case "table":
      return <Table node={node} />;
    case "image":
      return <Image node={node} />;
    case "missingImage":
      return <Placeholder text={attr(node, "caption") || t("link-doccontent.image_missing")} />;
    case "bookmark":
      return <LinkCard href={attr(node, "href")} title={attr(node, "title") || attr(node, "href") || t("link-doccontent.link")} subtitle={attr(node, "summary")} />;
    case "embedLink": {
      const kind = attr(node, "kind") ?? "";
      const label = ({ sheet: t("link-doccontent.sheet"), bitable: t("link-doccontent.bitable"), base: t("link-doccontent.bitable"), mindnote: t("link-doccontent.mindnote"), chat: t("link-doccontent.chat"), calendarEvent: t("link-doccontent.event") } as Record<string, string>)[kind] ?? t("link-doccontent.embed");
      const href = attr(node, "href");
      return <LinkCard href={safeWebURL(href)} title={attr(node, "title") || label} subtitle={label} />;
    }
    case "file": {
      const token = attr(node, "token");
      const size = num(node, "size");
      return (
        <LinkCard
          href={token ? mediaURL(`/api/v1/lark/raw/documents/drive/files/${encodeURIComponent(token)}/download`, manager) : attr(node, "sourceUrl")}
          title={attr(node, "name") || t("link-doccontent.file")}
          subtitle={size ? bytes(size) : t("link-doccontent.file")}
          icon="clip"
          download={Boolean(token)}
        />
      );
    }
    case "docTask": {
      const done = node.attrs?.checked === true || node.attrs?.checked === "true";
      return (
        <p className="flex items-start gap-2">
          <input type="checkbox" checked={done} readOnly className="mt-[0.45em]" />
          <span className={done ? "text-ink-3 line-through" : undefined}>{attr(node, "summary") || plainText(node)}</span>
        </p>
      );
    }
    case "mindnoteLabel":
      return (
        <p>
          <Inline nodes={kids} />
        </p>
      );
    case "slide":
      return (
        <div className="my-3 rounded-card bg-inset px-3.5 py-3 shadow-hairline [&>:last-child]:mb-0">
          <div className="text-[11.5px] font-medium text-ink-3">{t("link-doccontent.slide", { n: num(node, "index") ?? 0 })}</div>
          <Blocks nodes={kids} />
        </div>
      );
    case "toc":
    case "readReceipt":
    case "widgetEmpty":
      return null;
    case "unsupported":
      return <Placeholder text={attr(node, "kind") === "isv" ? t("link-doccontent.isv") : t("link-doccontent.block_unsupported")} href={attr(node, "sourceUrl")} />;
    default:
      if (!kids.length) return <Placeholder text={t("link-doccontent.block_unsupported")} href={attr(node, "sourceUrl") ?? attr(node, "href")} />;
      if (kids.every((k) => INLINE.has(k.type)))
        return (
          <p>
            <Inline nodes={kids} />
          </p>
        );
      return <Blocks nodes={kids} />;
  }
}

function ListItem({ item, task }: { item: DocNode; task?: boolean }) {
  const kids = item.content ?? [];
  const todo = task || item.attrs?.todo === true || item.attrs?.todo === "true";
  const done = item.attrs?.checked === true || item.attrs?.checked === "true";
  if (!todo) {
    return (
      <li>
        <Tight nodes={kids} />
      </li>
    );
  }
  return (
    <li className="flex list-none items-start gap-2">
      <input type="checkbox" checked={done} readOnly className="mt-[0.45em]" />
      <div className={done ? "min-w-0 flex-1 text-ink-3 line-through" : "min-w-0 flex-1"}>
        <Tight nodes={kids} />
      </div>
    </li>
  );
}

function Table({ node }: { node: DocNode }) {
  const rows = (node.content ?? []).filter((r) => r.type === "tableRow");
  return (
    <table>
      <tbody>
        {rows.map((r, i) => (
          <tr key={i}>
            {(r.content ?? [])
              .filter((c) => c.type === "tableCell" || c.type === "tableHeader")
              .map((c, j) => {
                const Cell = c.type === "tableHeader" ? "th" : "td";
                return (
                  <Cell key={j} colSpan={num(c, "colspan") ?? undefined} rowSpan={num(c, "rowspan") ?? undefined}>
                    <Tight nodes={c.content ?? []} />
                  </Cell>
                );
              })}
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function Image({ node }: { node: DocNode }) {
  const src = attr(node, "src");
  const manager = useLinkManager();
  const resolved = src ? mediaURL(src, manager) : "";
  if (!resolved) return <Placeholder text={t("link-doccontent.image_missing")} />;
  const w = num(node, "width") ?? num(node, "displayWidth");
  const h = num(node, "height");
  const caption = attr(node, "caption");
  return (
    <figure className="my-3">
      <a href={resolved} target="_blank" rel="noreferrer noopener">
        <img
          src={resolved}
          alt={attr(node, "alt") || caption || t("link-doccontent.image")}
          loading="lazy"
          width={w}
          height={h}
          className="h-auto bg-inset"
          style={w && h ? { aspectRatio: `${w} / ${h}` } : undefined}
        />
      </a>
      {caption && <figcaption className="mt-1 text-center text-[12px] text-ink-3">{caption}</figcaption>}
    </figure>
  );
}

function LinkCard({ href, title, subtitle, icon = "link", download }: { href?: string; title: string; subtitle?: string; icon?: "link" | "clip"; download?: boolean }) {
  const manager = useLinkManager();
  const body = (
    <>
      <span className="flex size-8 shrink-0 items-center justify-center rounded-[8px] bg-field text-ink-2">
        <Icon name={icon} size={14} />
      </span>
      <span className="min-w-0 flex-1">
        <span className="block truncate text-[13px] font-medium text-ink">{title}</span>
        {subtitle && <span className="block truncate text-[11.5px] text-ink-3">{subtitle}</span>}
      </span>
    </>
  );
  const cls = "my-3 flex items-center gap-2.5 rounded-card bg-surface px-3 py-2 no-underline! shadow-card";
  const safeHref = href && (download || !href.trim().startsWith("/")) ? linkHref(href) : undefined;
  if (!safeHref) return <div className={cls}>{body}</div>;
  return (
    <a
      href={safeHref}
      target="_blank"
      rel="noreferrer noopener"
      download={download || undefined}
      onClick={(e) => (download ? undefined : clickLink(e, href!, title, manager))}
      onPointerEnter={() => prefetchLink(href!, manager)}
      className={`${cls} transition-colors hover:bg-hover`}
    >
      {body}
    </a>
  );
}

function Placeholder({ text, href }: { text: string; href?: string }) {
  const safeHref = safeWebURL(href);
  return (
    <div className="my-3 flex items-center gap-2 rounded-card border border-dashed border-line-strong px-3 py-2 text-[12.5px] text-ink-3">
      <Icon name="info" size={13} />
      <span className="min-w-0 flex-1">{text}</span>
      {safeHref && (
        <a href={safeHref} target="_blank" rel="noreferrer noopener" className="shrink-0 whitespace-nowrap">
          {t("link-doccontent.open")}
        </a>
      )}
    </div>
  );
}

// ── inline runs ──

function Inline({ nodes }: { nodes: DocNode[] }) {
  return (
    <>
      {nodes.map((n, i) => (
        <Run key={i} node={n} />
      ))}
    </>
  );
}

function Run({ node }: { node: DocNode }) {
  const manager = useLinkManager();
  switch (node.type) {
    case "text":
      return <Marked node={node} />;
    case "hardBreak":
      return <br />;
    case "mention": {
      const kind = attr(node, "kind") ?? "";
      const label = attr(node, "label") ?? "";
      if (kind === "user") return <span className="text-accent-ink">@{label}</span>;
      if (kind === "reminder") {
        const at = Date.parse(label);
        return <span className="text-orange">⏰ {Number.isFinite(at) ? formatDate(at, { month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit" }) : label}</span>;
      }
      const href = attr(node, "href");
      const safeHref = href && !href.trim().startsWith("/") ? linkHref(href) : undefined;
      const text = `${kind === "doc" ? "📄 " : ""}${label}`;
      return safeHref ? (
        <a href={safeHref} target="_blank" rel="noreferrer noopener" onClick={(e) => clickLink(e, href!, label, manager)} onPointerEnter={() => prefetchLink(href!, manager)}>
          {text}
        </a>
      ) : (
        <span className="text-accent-ink">{text}</span>
      );
    }
    case "equation":
      return <span className="font-serif italic">{attr(node, "latex")}</span>;
    case "statusTag":
      return <span className="rounded-chip bg-field px-1.5 py-px text-[0.86em] font-medium text-ink-2">{attr(node, "text")}</span>;
    default:
      return <Inline nodes={node.content ?? []} />;
  }
}

function Marked({ node }: { node: DocNode }) {
  const manager = useLinkManager();
  let out: React.ReactNode = node.text ?? "";
  let href: string | undefined;
  for (const m of node.marks ?? []) {
    switch (m.type) {
      case "bold":
        out = <strong>{out}</strong>;
        break;
      case "italic":
        out = <em>{out}</em>;
        break;
      case "underline":
        out = <u>{out}</u>;
        break;
      case "strike":
        out = <s>{out}</s>;
        break;
      case "code":
        out = <code>{out}</code>;
        break;
      case "link":
        if (typeof m.attrs?.href === "string") href = m.attrs.href;
        break;
    }
  }
  const safeHref = href && !href.trim().startsWith("/") ? linkHref(href) : undefined;
  if (!safeHref) return <>{out}</>;
  const to = href!;
  return (
    <a href={safeHref} target="_blank" rel="noreferrer noopener" onClick={(e) => clickLink(e, to, node.text, manager)} onPointerEnter={() => prefetchLink(to, manager)}>
      {out}
    </a>
  );
}
