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

/** Short reason code for a row; the full message goes in the tooltip. */
export function shortReason(msg: string): string {
  const m = (msg || "").trim();
  if (!m) return "—";
  const head = m.split(/[:：]/)[0].trim().split(/\s+/)[0];
  return head.length > 28 ? head.slice(0, 27) + "…" : head;
}

/** "10/15 19:27" in local time. */
export function shortDate(iso?: string): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime()) || d.getFullYear() < 2000) return "—";
  const p = (n: number) => String(n).padStart(2, "0");
  return `${p(d.getMonth() + 1)}/${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}

/** 20818 -> "2.1万" (zh) / "20.8k" (en). */
export function compactCount(n: number, zh: boolean): string {
  if (zh) return n >= 1e4 ? `${(n / 1e4).toFixed(1).replace(/\.0$/, "")}万` : String(n);
  return n >= 1e3 ? `${(n / 1e3).toFixed(1).replace(/\.0$/, "")}k` : String(n);
}

/** One nowrap stats cell: "7天1次 · 成功2.1万" (403) or "成功2.1万" (401). */
export function purgeStats(it: Pick<ForbiddenCred, "kind" | "count_403_7d" | "ok_7d">, zh: boolean): string {
  const ok = compactCount(it.ok_7d, zh);
  if (it.kind === "401") return zh ? `7天成功${ok}` : `7d ok ${ok}`;
  return zh ? `7天${it.count_403_7d}次 · 成功${ok}` : `7d ${it.count_403_7d}× · ok ${ok}`;
}
