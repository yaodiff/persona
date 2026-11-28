// Pure profile ID generation and validation for creating a new ModelProfile
// (mirrors server/internal/assistant/model_profiles.go and model_profiles_edit.go).
import { t } from "../../i18n";
import {
  MAX_PROFILE_DESCRIPTION,
  MAX_PROFILE_NAME,
  PROFILE_ID_RE,
} from "../../lib/model-profiles";

const MAX_PROFILE_ID = 32;
const RESERVED_PROFILE_IDS = new Set(["rollback"]);
const AUTO_AVOID_PROFILE_IDS = new Set(["rollback", "deactivate"]);

function runeLength(value: string): number {
  return [...value].length;
}

/** Converts a profile display name into a unique, server-valid slug ID (`^[a-z0-9][a-z0-9_-]{0,31}$`). */
export function suggestProfileId(name: string, existingIds: readonly string[]): string {
  const taken = new Set([...existingIds, ...AUTO_AVOID_PROFILE_IDS]);
  const ascii = name
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9_-]+/g, "-")
    .replace(/^[^a-z0-9]+/, "")
    .replace(/-+/g, "-")
    .replace(/[-_]+$/, "");

  const base = ascii.slice(0, MAX_PROFILE_ID);
  if (base && PROFILE_ID_RE.test(base) && !taken.has(base)) {
    return base;
  }

  const stem = base || "profile";
  for (let i = base ? 2 : 1; i < 1000; i++) {
    const suffix = `-${i}`;
    const candidate = `${stem.slice(0, MAX_PROFILE_ID - suffix.length)}${suffix}`;
    if (PROFILE_ID_RE.test(candidate) && !taken.has(candidate)) {
      return candidate;
    }
  }
  return `profile-${Date.now().toString(36).slice(-6)}`;
}

export type NewProfileValidation =
  | { ok: true; id: string; name: string; description: string }
  | { ok: false; nameError?: string; idError?: string; descriptionError?: string };

/** Validates inputs for creating a new model profile against server rules and existing IDs. */
export function validateNewProfile(
  rawName: string,
  rawId: string,
  rawDescription: string,
  existingIds: readonly string[],
): NewProfileValidation {
  const name = rawName.trim();
  const id = rawId.trim();
  const description = rawDescription.trim();

  let nameError: string | undefined;
  let idError: string | undefined;
  let descriptionError: string | undefined;

  if (!name || runeLength(name) > MAX_PROFILE_NAME || /[\r\n\0]/.test(name)) {
    nameError = t("settings-modelprofiles.err_name", { max: MAX_PROFILE_NAME });
  }
  if (!PROFILE_ID_RE.test(id) || RESERVED_PROFILE_IDS.has(id)) {
    idError = t("settings-modelprofiles.err_id");
  } else if (existingIds.includes(id)) {
    idError = t("settings-modelprofiles.err_id_duplicate", { id });
  }
  if (runeLength(description) > MAX_PROFILE_DESCRIPTION || description.includes("\0")) {
    descriptionError = t("settings-modelprofiles.err_description", { max: MAX_PROFILE_DESCRIPTION });
  }

  if (nameError || idError || descriptionError) {
    return { ok: false, nameError, idError, descriptionError };
  }
  return { ok: true, id, name, description };
}
