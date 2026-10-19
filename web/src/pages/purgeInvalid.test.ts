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

import { compactCount, purgeStats, shortDate, shortReason } from "./purgeInvalid";
describe("purge row formatting", () => {
  it("short reason keeps the code only", () => {
    expect(shortReason("account_suspended: This service has been disabled in this account")).toBe("account_suspended");
    expect(shortReason("authentication_error credential unauthorized")).toBe("authentication_error");
    expect(shortReason("")).toBe("—");
  });
  it("compact counts", () => {
    expect(compactCount(20818, true)).toBe("2.1万");
    expect(compactCount(20000, true)).toBe("2万");
    expect(compactCount(950, true)).toBe("950");
    expect(compactCount(20818, false)).toBe("20.8k");
  });
  it("stats cell", () => {
    expect(purgeStats({ kind: "403", count_403_7d: 1, ok_7d: 20818 }, true)).toBe("7天1次 · 成功2.1万");
    expect(purgeStats({ kind: "401", count_403_7d: 0, ok_7d: 3 }, true)).toBe("7天成功3");
  });
  it("short date", () => {
    const d = new Date(2026, 9, 15, 19, 27, 26);
    expect(shortDate(d.toISOString())).toBe("10/15 19:27");
    expect(shortDate(undefined)).toBe("—");
  });
});
