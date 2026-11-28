// Only a presence marker goes in localStorage. The base path also isolates hubs sharing an origin.
import { getToken } from "./api";
import { BASE } from "./base";
import { credentialEpoch } from "./credential-epoch";
import { feedCacheKey } from "./feed-cache";

export const imageDraftPrefix = `persona.imageDraft:${encodeURIComponent(BASE)}:`;
export const imageDraftMarker = (key: string) => imageDraftPrefix + key;

export function hasImageDraft(key: string): boolean {
  try { return localStorage.getItem(imageDraftMarker(key)) !== null; }
  catch { return false; }
}

export function imageDraftMarkers(): string[] {
  try { return Object.keys(localStorage).filter((key) => key.startsWith(imageDraftPrefix)); }
  catch { return []; }
}

/** The shared cache fingerprint survives a reload. The in-memory credential generation does not. */
export function imageDraftOwner(): string | null {
  try { return feedCacheKey(BASE, getToken(), "", credentialEpoch()); }
  catch { return null; }
}
