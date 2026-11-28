import { useCallback, useState } from "react";

export function pref(key: string): string | null {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}

export function setPref(key: string, value: string | null) {
  try {
    if (value === null) localStorage.removeItem(key);
    else localStorage.setItem(key, value);
  } catch {
    // The choice still lasts for the mounted view when browser storage is unavailable.
  }
}

export function useTodoPreference<T extends string | null>(key: string, read: (stored: string | null) => T) {
  const [saved, setSaved] = useState(() => ({ key, value: read(pref(key)) }));
  const value = saved.key === key ? saved.value : read(pref(key));
  const change = useCallback((next: T) => {
    setSaved({ key, value: next });
    setPref(key, next);
  }, [key]);
  return [value, change] as const;
}
