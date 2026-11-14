// A waiting card taller than the room left for it in the box it scrolls in (the dock's cards over the composer, a tray's
// list) keeps its answer row in view at that box's bottom edge (Card's sticky footer, #3112), while what the row answers
// (the title, the command) is out of view above it. Then the row says what it answers, and the stack says how many cards
// it holds (docs/design/DESIGN_SYSTEM.md 4.8): never an Allow next to nothing.
import { useEffect, useState, type RefObject } from "react";

/** The nearest ancestor that scrolls vertically: where a card's sticky answer row sticks. */
export function scrollBox(el: Element | null): HTMLElement | null {
  for (let p = el?.parentElement; p; p = p.parentElement) {
    const y = getComputedStyle(p).overflowY;
    if (y === "auto" || y === "scroll") return p;
  }
  return null;
}

/** Whether the end of a card's body (`end`, a zero-height mark under it) is out of view below `boxBottom`, the box's
 *  bottom edge less the answer row stuck there: the row then stands away from what it answers. Above is not "below". */
export function cutBelow(visible: boolean, end: { top: number }, boxBottom: number): boolean {
  return !visible && end.top >= boxBottom - 1;
}

/** What a card's observer says of it: `cut`, the end of its body is out of view below its answer row (under the row
 *  stuck at the box's bottom edge, or below the box); `titleShown`, its title is wholly in view above that row. */
export interface CardCut {
  cut: boolean;
  titleShown: boolean;
}

const IN_VIEW: CardCut = { cut: false, titleShown: true };

/** CardCut for a card's end mark `end`, its title `title` and its answer row `row`, kept current as the box scrolls and
 *  its cards come and go (one IntersectionObserver for both, no scroll listener). The row's height, read once, is taken
 *  off the box's bottom: what is under the stuck row is out of view as well. Everything in view where there is no
 *  observer (tests, older browsers). */
export function useCardCut(end: RefObject<HTMLElement | null>, title: RefObject<HTMLElement | null>, row: RefObject<HTMLElement | null>): CardCut {
  const [state, setState] = useState<CardCut>(IN_VIEW);
  useEffect(() => {
    const el = end.current;
    if (!el || typeof IntersectionObserver === "undefined") return;
    const head = title.current;
    const covered = Math.max(0, Math.round(row.current?.offsetHeight ?? 0));
    const io = new IntersectionObserver((entries) => setState((had) => {
      let next = had;
      for (const e of entries) {
        if (e.target === el) {
          const cut = cutBelow(e.isIntersecting, e.boundingClientRect, e.rootBounds?.bottom ?? innerHeight - covered);
          if (cut !== next.cut) next = { ...next, cut };
        } else if (e.target === head) {
          const titleShown = e.isIntersecting && e.intersectionRatio >= 0.99;
          if (titleShown !== next.titleShown) next = { ...next, titleShown };
        }
      }
      return next;
    }), { root: scrollBox(el), rootMargin: `0px 0px -${covered}px 0px`, threshold: [0, 1] });
    io.observe(el);
    if (head) io.observe(head);
    return () => io.disconnect();
  }, [end, title, row]);
  return state;
}
