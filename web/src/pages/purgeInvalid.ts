import type { ForbiddenCategory, ForbiddenCred } from "../api/client";

/** Categories pre-checked when the dialog opens (403 TOS bans only, as before). */
export const PURGE_DEFAULT_CATS: Record<string, boolean> = { tos_violation: true };

/** 401-type categories: Home already disabled them for a terminal auth failure. */
export const isAuthInvalidCategory = (key: string) => key === "unauthorized_401" || key === "refresh_invalid";

/** Credentials the purge will remove for the current dialog state. */
export function purgeSelection(
  categories: ForbiddenCategory[],
  cats: Record<string, boolean>,
  onlyPersistent: boolean,
  excluded: Record<string, boolean>,
): ForbiddenCred[] {
  const eligible = (it: ForbiddenCred) => !onlyPersistent || it.persistent;
  return categories.filter((c) => cats[c.key]).flatMap((c) => c.items.filter((it) => eligible(it) && !excluded[it.id]));
}
