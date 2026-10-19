import { describe, expect, it } from "vitest";
import type { ForbiddenCategory, ForbiddenCred } from "../api/client";
import { PURGE_DEFAULT_CATS, isAuthInvalidCategory, purgeSelection } from "./purgeInvalid";

const cred = (id: string, category: string, persistent: boolean, kind = "403"): ForbiddenCred => ({
  id, label: id, category, kind, message: "", source: "", count_403_7d: 0, ok_7d: 0, disabled: true, persistent,
});
const cat = (key: string, items: ForbiddenCred[]): ForbiddenCategory => ({ key, label: key, count: items.length, persistent: items.filter((i) => i.persistent).length, items });
const cats = [
  cat("tos_violation", [cred("t1", "tos_violation", true), cred("t2", "tos_violation", false)]),
  cat("verify_required", [cred("v1", "verify_required", true)]),
  cat("unauthorized_401", [cred("u1", "unauthorized_401", true, "401")]),
  cat("refresh_invalid", [cred("r1", "refresh_invalid", true, "401")]),
];

describe("purge invalid credentials selection", () => {
  it("defaults to 403 TOS only (previous behaviour)", () => {
    expect(purgeSelection(cats, PURGE_DEFAULT_CATS, true, {}).map((c) => c.id)).toEqual(["t1"]);
  });
  it("adds 401 kinds only when checked", () => {
    const ids = purgeSelection(cats, { ...PURGE_DEFAULT_CATS, unauthorized_401: true, refresh_invalid: true }, true, {}).map((c) => c.id);
    expect(ids).toEqual(["t1", "u1", "r1"]);
  });
  it("respects per-row exclusion and the persistent toggle", () => {
    expect(purgeSelection(cats, { tos_violation: true }, false, { t1: true }).map((c) => c.id)).toEqual(["t2"]);
  });
  it("classifies 401 categories", () => {
    expect(isAuthInvalidCategory("unauthorized_401")).toBe(true);
    expect(isAuthInvalidCategory("refresh_invalid")).toBe(true);
    expect(isAuthInvalidCategory("tos_violation")).toBe(false);
  });
});
