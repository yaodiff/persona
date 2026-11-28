// The assistant's cards draw an extension view's sections (ui/AssistantCard.tsx), and the chat draws those cards, so a
// static import of ViewBody.tsx put the whole renderer (UI version 6's items, the chart frame, the icon map) into every
// cold first screen, though it only draws for a reply with such a card (#3614). This small first-screen module keeps
// the renderer a chunk of its own: fetched once the browser is idle after start, or by the first card drawn before then,
// which shows its sections' boxes meanwhile so the card's frame doesn't collapse. Types only come from ViewBody here.
import { Suspense } from "react";
import type { UIAction, UIItem, UIView } from "../../lib/extension-ui";
import { lazyPage } from "../../lib/lazy-page";
import { cx } from "../../ui/atoms";
import type { ViewHandlers } from "./ViewBody";

/** Nothing acts (an assistant's card): what a press would do is never asked for. */
export const INERT: ViewHandlers = { values: {}, setValue: () => {}, busy: "", onPress: () => {}, onChoose: () => {} };

// Only chart items draw it: its own chunk, fetched by the idle preload or the first chart. Registered here, on the first
// screen: an idle page registered inside a lazy chunk (ViewBody's) would miss the idle preload (lib/lazy-page.ts).
export const ExtensionChart = lazyPage(() => import("./ExtensionChart").then((m) => m.ExtensionChart), { idle: true });

// Canvas registrations live here too: their chunks load only on intent, while ViewBody's idle chunk stays small.
type CanvasProps = {
  item: UIItem;
  compact?: boolean;
  busy: string;
  onPress: (a: UIAction, destructive?: boolean) => void;
  canPress?: (a: UIAction) => boolean;
};
export const GraphCanvas = lazyPage<CanvasProps>(() => import("./ViewBody10").then((m) => m.GraphCanvas));
export const SceneCanvas = lazyPage<CanvasProps>(() => import("./ViewBody11").then((m) => m.SceneCanvas));

type SectionsProps = ViewHandlers & { view: UIView; embedded?: boolean; compact?: boolean };

const Sections = lazyPage<SectionsProps>(() => import("./ViewBody").then((m) => m.ViewSections), { idle: true });

/** A view's sections (ViewBody.tsx ViewSections), drawn once the renderer's chunk is in. */
export function ViewSections(props: SectionsProps) {
  return (
    <Suspense fallback={<SectionBoxes {...props} />}>
      <Sections {...props} />
    </Suspense>
  );
}

/** While the renderer loads: each section's title and an empty box where its items go, as Section and Run draw them. */
function SectionBoxes({ view, embedded, compact }: SectionsProps) {
  return (
    <>
      {view.sections.map((s, i) => (
        <section key={s.key ?? i} aria-busy="true">
          {s.title && <h3 className={cx("font-medium text-ink-3", compact ? "px-4 pt-1 text-[11.5px]" : embedded ? "mb-1.5 px-1 text-[11.5px]" : "mb-1.5 px-1 text-[12px]")}>{s.title}</h3>}
          <div className={cx("min-h-12", compact ? "" : embedded ? "rounded-control bg-inset shadow-hairline" : "rounded-card bg-surface shadow-card")} />
        </section>
      ))}
    </>
  );
}
