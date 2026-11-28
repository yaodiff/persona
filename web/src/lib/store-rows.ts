import { useRef } from "react";
import { useStore, type State } from "./store";

function sameRow(a: object, b: object): boolean {
  const keys = Object.keys(a);
  return keys.length === Object.keys(b).length && keys.every((key) =>
    Object.is((a as Record<string, unknown>)[key], (b as Record<string, unknown>)[key]));
}

/** Keep the previous list if a selector rebuilt rows with the same fields. */
export function useStoreRows<T extends object>(select: (state: State) => T[]): T[] {
  const last = useRef<T[] | null>(null);
  return useStore((state) => {
    const rows = select(state);
    const previous = last.current;
    if (previous && previous.length === rows.length && rows.every((row, at) => sameRow(row, previous[at]))) return previous;
    last.current = rows;
    return rows;
  });
}
