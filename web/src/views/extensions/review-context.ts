// What a page of diffs shares (DiffReview.tsx's ReviewProvider): quiet actions, viewed marks, whitespace, the lines of
// files that came. Its own file so the file tree (CodeViews.tsx) reads viewed marks without importing the diff.
import { createContext, useContext } from "react";
import { LineCache } from "../../lib/code-review";
import type { UIAction, UIActionResult } from "../../lib/extension-ui";

/** What a page of diffs shares: quiet actions, viewed marks, whitespace, the lines of files that came. */
export interface ReviewApi {
  /** An action that does not hold the page busy (lines, a viewed mark, a diff loaded): its answer, or null when it failed. */
  fetch: (a: UIAction, values?: Record<string, string>) => Promise<UIActionResult | null>;
  viewed: (path: string) => boolean;
  setViewed: (path: string, viewed: boolean) => void;
  whitespace: boolean;
  setWhitespace: (on: boolean) => void;
  /** The lines of a file's new side that came (for hidden lines, suggestions). */
  lines: (path: string) => LineCache;
  /** Drafts of the pending review, as the view says. */
  drafts: number;
}

const caches = new Map<string, LineCache>();

const fallback: ReviewApi = {
  fetch: async () => null,
  viewed: () => false,
  setViewed: () => {},
  whitespace: false,
  setWhitespace: () => {},
  lines: (path) => {
    if (!caches.has(path)) caches.set(path, new LineCache());
    return caches.get(path)!;
  },
  drafts: 0,
};

export const ReviewContext = createContext<ReviewApi>(fallback);
export const useReview = () => useContext(ReviewContext);
