import { Fragment, useEffect, useMemo, useState, type ReactNode } from "react";
import { useAction, useApi, useAsync, useUi } from "../app-context";
import {
  coolingModels,
  credentialUsable,
  isSupportedProvider,
  type Credential,
  type CredentialGroupSummary,
  type CredentialQuery,
  type CredentialSortKey,
  type EgressMove,
  type StatsWindow,
  type EgressStatus,
  type CredentialSearchItem,
  type ModelCooldown,
  type ModelInfo,
  type QuotaItem,
  BULK_CHUNK,
} from "../api/client";
import {
  CredentialFilterBar,
  PageNav,
  SelectionBar,
  useSelection,
} from "../components/picker";
import { downloadBlob, fmtRelative, fmtTime } from "../format";
import { useI18n } from "../i18n";
import {
  Badge,
  Card,
  Drawer,
  Empty,
  ErrorBox,
  Loading,
  Modal,
  Spinner,
  Switch,
} from "../components/ui";
import { useFlexColumns, type FlexCol } from "../components/flexCols";
import { CallStatsCell, CallStatsPanel, StatsWindowSelect } from "../components/CallStats";
import { QuotaStatusBadge, QuotaWindows, fmtRecover, fmtResetShort } from "../components/quota";
import {
  IconDownload,
  IconPlus,
  IconRefresh,
  IconTrash,
} from "../components/icons";
import { AddCredentialModal } from "./AddCredential";
import { AllowPrimaryToggle, EgressSelect, useEgressSettings, egressIP, egressNode, egressRole, egressState, useEgressList } from "../components/EgressSelect";
import { GroupChecklist, useGroupLookup } from "./Groups";

export function healthBadges(c: Credential) {
  if (!c.health || c.health.length === 0) return null;
  return (
    <>
      {c.health.map((h) => (
        <span
          key={h.family}
          style={{ marginLeft: 4 }}
          title={`${h.family} · TTFB ${(h.ttfb_ms / 1000).toFixed(1)}s · fail ${(h.fail_ratio * 100).toFixed(0)}% · weight ${(h.weight * 100).toFixed(0)}%`}
        >
          <Badge kind={h.state === "降级" ? "err" : "warn"}>{`${h.label} ${h.state}`}</Badge>
        </span>
      ))}
    </>
  );
}

export function credentialStatus(
  c: Credential,
  t: ReturnType<typeof useI18n>["t"],
  tr: ReturnType<typeof useI18n>["tr"],
) {
  if (c.disabled && c.auto_disabled_reason)
    return (
      <span title={c.status_message}>
        <Badge kind="err" dot>
          {c.auto_disabled_reason === "account_verification_required"
            ? tr("已停用 · 需验证(403)", "Off · verify (403)")
            : tr("已停用 · 封禁(403)", "Off · suspended (403)")}
        </Badge>
      </span>
    );
  if (c.disabled && c.auto_paused_reason)
    return (
      <span title={c.status_message}>
        <Badge kind="err" dot>
          {c.auto_paused_reason === "egress_removed"
            ? tr("已暂停 · IP 已移除", "Paused · IP removed")
            : tr("已暂停 · 出口离线", "Paused · egress down")}
        </Badge>
      </span>
    );
  if (c.disabled) return <Badge>{t("disabled")}</Badge>;
  const buckets = bucketExhaustedMessages(c);
  if (buckets.length > 0 && !c.credential_scope_error)
    return (
      <span title={statusTooltip(c, tr)}>
        <Badge kind="warn" dot>
          {buckets.join(" / ")}
        </Badge>
      </span>
    );
  if (c.unavailable || c.credential_scope_error)
    return (
      <Badge kind="warn" dot>
        {c.next_retry_after
          ? tr(`冷却 · ${fmtRecover(c.next_retry_after)}恢复`, `Cooling · ${fmtRecover(c.next_retry_after)}`)
          : tr("不可用", "Unavailable")}
      </Badge>
    );
  const coolingList = coolingModels(c);
  const cooling = coolingList.length;
  if (credentialUsable(c) && cooling > 0) {
    const first = coolingList.map((m) => m.next_retry_after).filter(Boolean).sort()[0];
    const at = fmtRecover(first);
    return (
      <Badge kind="warn" dot>
        {at
          ? tr(`${cooling}模型冷却 · ${at}恢复`, `${cooling} cooling · ${at}`)
          : tr(`${cooling}模型冷却`, `${cooling} cooling`)}
      </Badge>
    );
  }
  if (credentialUsable(c) && (c.status === "active" || c.status === "error"))
    return (
      <Badge kind="ok" dot>
        {tr("可用", "Active")}
      </Badge>
    );
  return (
    <Badge kind="err" dot>
      {c.status || "unknown"}
    </Badge>
  );
}

function bucketBadgeText(msg?: string): string {
  const label = (msg || "").startsWith("Claude") ? "Claude/GPT" : "Gemini";
  const t = (msg || "").match(/(\d{2}-\d{2} \d{2}:\d{2})/);
  return t ? `${label} 耗尽 · ${t[1]}恢复` : `${label} 耗尽`;
}

/** Distinct "Gemini 额度耗尽，重置于 …" lines for buckets the quota probe shows at 0%. */
export function bucketExhaustedMessages(c: Credential): string[] {
  const out = new Set<string>();
  for (const m of coolingModels(c)) {
    if (m.bucket_exhausted || m.error_code === "quota_bucket_exhausted")
      out.add(bucketBadgeText(m.status_message));
  }
  return [...out];
}

function cooldownReason(m: { http_status?: number; quota_exceeded?: boolean; status_message?: string; bucket_exhausted?: boolean; error_code?: string }, tr: ReturnType<typeof useI18n>["tr"]) {
  if (m.bucket_exhausted || m.error_code === "quota_bucket_exhausted")
    return tr("额度 0%", "quota 0%");
  if (m.http_status === 429 || m.quota_exceeded) return tr("额度耗尽 429", "quota exhausted 429");
  if (m.http_status === 503) return tr("上游无容量 503", "no capacity 503");
  if (m.http_status) return `HTTP ${m.http_status}`;
  return m.status_message ? m.status_message.slice(0, 60) : tr("错误", "error");
}

function remaining(iso?: string) {
  if (!iso) return "";
  const s = Math.max(0, Math.round((new Date(iso).getTime() - Date.now()) / 1000));
  if (s >= 3600) return `${Math.floor(s / 3600)}h${Math.round((s % 3600) / 60)}m`;
  if (s >= 60) return `${Math.floor(s / 60)}m${s % 60}s`;
  return `${s}s`;
}

/** Hover text for the status cell: the real reason and remaining cooldown per model. */
export function statusTooltip(c: Credential, tr: ReturnType<typeof useI18n>["tr"]): string | undefined {
  const lines: string[] = [];
  if (c.credential_scope_error || c.unavailable) {
    lines.push(tr("整个凭据不可用", "Whole credential unavailable") + (c.next_retry_after ? ` · ${tr("剩余", "remaining")} ${remaining(c.next_retry_after)}` : ""));
    if (c.status_message) lines.push(c.status_message.replace(/\s+/g, " ").slice(0, 160));
  }
  const cooling = coolingModels(c);
  if (cooling.length > 0) {
    if (c.tried_models_all_cooling)
      lines.push(tr("已尝试过的模型都在冷却（按模型限额），未尝试的模型仍可用", "All models tried so far are cooling (per-model limits); untried models still work"));
    for (const m of cooling) {
      lines.push(`${m.model}: ${cooldownReason(m, tr)}${m.next_retry_after ? ` · ${tr("剩余", "remaining")} ${remaining(m.next_retry_after)}` : ""}`);
      const probe = probeLine(m, tr);
      if (probe) lines.push(`  ${probe}`);
    }
  }
  if (lines.length === 0) return c.status_message || undefined;
  return lines.join("\n");
}

function probeResultText(r: string, tr: ReturnType<typeof useI18n>["tr"]): string {
  if (r === "ok") return tr("200 已恢复", "200 recovered");
  if (r === "quota") return tr("429 仍限额", "429 still limited");
  return tr("探测出错", "probe error");
}

export function probeLine(m: ModelCooldown, tr: ReturnType<typeof useI18n>["tr"]): string | undefined {
  if (!m.probe_managed) return undefined;
  const parts = [tr("上游未给出时间，由探测决定", "upstream gave no time; decided by probing")];
  if (m.probe) {
    parts.push(`${tr("上次探测", "last probe")} ${fmtTime(m.probe.at)} ${probeResultText(m.probe.result, tr)}${m.probe.detail ? ` (${m.probe.detail})` : ""}`);
    parts.push(`${tr("下次探测", "next probe")} ${fmtTime(m.probe.next_probe)}`);
  } else {
    parts.push(tr("尚未探测", "not probed yet"));
  }
  parts.push(`${tr("短冷却", "short cooldown")} ${m.quota_short_cooldown_seconds ?? "?"}s / ${tr("探测间隔", "probe interval")} ${m.quota_probe_interval_seconds ?? "?"}s`);
  return parts.join(" · ");
}

// checkbox, ID, account, groups, provider, status, quota, priority, created, last used, enabled
const CRED_COLUMNS: FlexCol[] = [
  { min: 36 },
  { min: 60, max: 72 },
  { min: 140, max: 280, weight: 2 },
  { min: 80, max: 140, weight: 0.6 },
  { min: 112, max: 130, weight: 0.3 },
  { min: 172, max: 210, weight: 0.8 },
  { min: 150, weight: 3 },
  { min: 96 },
  { min: 64 },
  { min: 92 },
  { min: 80 },
  { min: 64 },
];

type CredSort = { sort: CredentialSortKey; order: "asc" | "desc" };
const CRED_SORT_KEY = "cred-sort";
const SORT_KEYS: CredentialSortKey[] = ["seq", "created", "name", "status", "quota", "last_used", "egress", "success"];
const STATS_WINDOW_KEY = "cred-stats-window";
/** Default direction per key: newest / most recent first, otherwise ascending. */
export const defaultOrder = (k: CredentialSortKey): "asc" | "desc" =>
  k === "created" || k === "last_used" || k === "seq" ? "desc" : "asc";

/** URL (?sort=&order=) wins, then localStorage, then created/desc. */
export function loadCredSort(search: string, stored: string | null): CredSort {
  const parse = (sort?: string | null, order?: string | null): CredSort | null => {
    if (!sort || !SORT_KEYS.includes(sort as CredentialSortKey)) return null;
    const k = sort as CredentialSortKey;
    return { sort: k, order: order === "asc" || order === "desc" ? order : defaultOrder(k) };
  };
  const q = new URLSearchParams(search);
  const fromUrl = parse(q.get("sort"), q.get("order"));
  if (fromUrl) return fromUrl;
  try {
    const v = stored ? (JSON.parse(stored) as Partial<CredSort>) : null;
    const fromStore = parse(v?.sort, v?.order);
    if (fromStore) return fromStore;
  } catch {
    /* ignore */
  }
  return { sort: "created", order: "desc" };
}

/** Click-to-copy text (same affordance as the ID cell); never opens the row drawer. */
export function CopyText({
  value,
  done,
  title,
  className,
  testId,
  children,
}: {
  value: string;
  done: string;
  title?: string;
  className?: string;
  testId?: string;
  children: ReactNode;
}) {
  const { tr } = useI18n();
  const { toast } = useUi();
  return (
    <span
      className={`id-cell ${className ?? ""}`}
      title={`${title ?? value}\n${tr("点击复制", "Click to copy")}`}
      data-testid={testId}
      onClick={(e) => {
        e.stopPropagation();
        void navigator.clipboard?.writeText(value).then(() => toast(done));
      }}
    >
      {children}
    </span>
  );
}

function SortTh({
  k,
  cur,
  onSort,
  children,
  className,
}: {
  k: CredentialSortKey;
  cur: CredSort;
  onSort: (k: CredentialSortKey) => void;
  children: ReactNode;
  className?: string;
}) {
  const active = cur.sort === k;
  return (
    <span
      className={`sortable ${className ?? ""}`}
      style={{ cursor: "pointer", userSelect: "none", fontWeight: active ? 600 : undefined }}
      onClick={() => onSort(k)}
      data-testid={`sort-${k}`}
    >
      {children}
      {active ? (cur.order === "desc" ? " ↓" : " ↑") : ""}
    </span>
  );
}

export function CredentialsPage() {
  const api = useApi();
  const { t, tr, lang } = useI18n();
  const { toast } = useUi();
  const { busy, run } = useAction();
  const [filter, setFilter] = useState<CredentialQuery>({});
  const [page, setPage] = useState(1);
  const [size, setSize] = useState(50);
  const [sort, setSort] = useState<CredSort>(() =>
    loadCredSort(window.location.search, localStorage.getItem(CRED_SORT_KEY)),
  );
  const onSort = (k: CredentialSortKey) =>
    setSort((cur) => ({
      sort: k,
      order: cur.sort === k ? (cur.order === "desc" ? "asc" : "desc") : defaultOrder(k),
    }));
  useEffect(() => {
    localStorage.setItem(CRED_SORT_KEY, JSON.stringify(sort));
    const url = new URL(window.location.href);
    url.searchParams.set("sort", sort.sort);
    url.searchParams.set("order", sort.order);
    window.history.replaceState(window.history.state, "", url);
  }, [sort]);
  useEffect(() => setPage(1), [filter, sort]);
  const [statsWindow, setStatsWindowState] = useState<StatsWindow>(() => {
    const v = localStorage.getItem(STATS_WINDOW_KEY);
    return v === "1h" || v === "7d" ? v : "24h";
  });
  const setStatsWindow = (w: StatsWindow) => {
    localStorage.setItem(STATS_WINDOW_KEY, w);
    setStatsWindowState(w);
  };
  const creds = useAsync(
    () => api.searchCredentials({ ...filter, ...sort, stats_window: statsWindow, page, page_size: size }),
    [filter, sort, page, size, statsWindow],
    20000,
  );
  const groups = useGroupLookup();
  const credCols = useFlexColumns<HTMLDivElement>(CRED_COLUMNS);
  const quota = useAsync(() => api.quotaCredentialsAll(), [], 60000);
  const egress = useAsync(() => api.egressStatus(), [], 30000);
  const sel = useSelection<string>();
  const [adding, setAdding] = useState(false);
  const [bulkEgress, setBulkEgress] = useState(false);
  const [selected, setSelected] = useState<CredentialSearchItem | null>(null);
  const [bulk, setBulk] = useState<
    null | "add_to_group" | "remove_from_group" | "add_tags" | "remove_tags"
  >(null);

  const items = useMemo(
    () =>
      (creds.data?.items ?? []).filter((c) => isSupportedProvider(c.provider)),
    [creds.data],
  );
  const quotaById = useMemo(
    () =>
      new Map<string, QuotaItem>(
        (quota.data?.items ?? []).map((q) => [q.credential_id, q]),
      ),
    [quota.data],
  );
  const current = selected
    ? (items.find((c) => c.id === selected.id) ?? selected)
    : undefined;
  const reloadAll = async () => {
    await creds.reload();
    await groups.reload();
  };

  const toggle = (c: Credential, enabled: boolean) =>
    run(`toggle-${c.id}`, async () => {
      await api.setCredentialDisabled(c.id, !enabled);
      toast(enabled ? tr("已启用", "Enabled") : tr("已禁用", "Disabled"));
      await creds.reload();
    });
  const bulkToggle = (action: "enable" | "disable") =>
    run(`bulk-${action}`, async () => {
      const ids = sel.list();
      const r = await api.bulkCredentials({ ids, action }, ids.length > BULK_CHUNK ? (d, t) => toast(tr(`处理中 ${d}/${t}`, `Working ${d}/${t}`)) : undefined);
      toast(
        tr(
          `已${action === "enable" ? "启用" : "禁用"} ${r.updated ?? 0} 个${r.unchanged ? `（${r.unchanged} 个无需变更）` : ""}`,
          `${action}d ${r.updated ?? 0}${r.unchanged ? ` (${r.unchanged} unchanged)` : ""}`,
        ),
        r.failed?.length ? "err" : "ok",
      );
      await reloadAll();
    });

  return (
    <div className="stack">
      <div className="page-head">
        <div>
          <h1>{t("credentials")}</h1>
          <p>
            {tr(
              "Antigravity OAuth、AI Studio 与 Vertex 服务账号凭据。支持服务端筛选、跨页多选与批量操作；点击一行查看详情。",
              "Antigravity OAuth, AI Studio and Vertex credentials. Server-side filters, cross-page multi-select and bulk actions; click a row for details.",
            )}
          </p>
        </div>
        <div className="toolbar">
          <button
            className="btn"
            disabled={busy === "refresh-all" || !creds.data?.all}
            onClick={() =>
              run("refresh-all", async () => {
                const res = await api.refreshAllCredentials();
                const failed = res.results?.filter((r) => !r.success) ?? [];
                toast(
                  failed.length
                    ? tr(
                        `${failed.length} 个刷新失败`,
                        `${failed.length} refresh(es) failed`,
                      )
                    : tr("全部刷新完成", "All refreshed"),
                  failed.length ? "err" : "ok",
                );
                await creds.reload();
              })
            }
          >
            {busy === "refresh-all" ? <Spinner /> : <IconRefresh />}
            {tr("全部刷新令牌", "Refresh all tokens")}
          </button>
          <button
            className="btn"
            data-testid="quota-refresh"
            disabled={busy === "quota-refresh"}
            title={tr(
              "立即重新探测全部凭据额度（Home 后台约每 30 分钟自动探测）",
              "Re-probe quota for all credentials now (Home also probes ~every 30 min)",
            )}
            onClick={() =>
              run("quota-refresh", async () => {
                const r = await api.collectQuota();
                toast(
                  tr(
                    `已触发 ${r.accepted} 个凭据的额度探测`,
                    `Quota probe started for ${r.accepted} credential(s)`,
                  ),
                );
                setTimeout(() => void quota.reload(), 4000);
              })
            }
          >
            {busy === "quota-refresh" ? <Spinner /> : <IconRefresh />}
            {tr("刷新额度", "Refresh quota")}
          </button>
          <button
            className="btn btn-primary"
            onClick={() => setAdding(true)}
            data-testid="add-credential"
          >
            <IconPlus />
            {t("addCredential")}
          </button>
        </div>
      </div>

      <Card
        bodyClass="table-wrap"
        title={
          <CredentialFilterBar
            value={filter}
            onChange={setFilter}
            facets={creds.data?.facets}
            groups={groups.data?.credential_groups}
          />
        }
        extra={
          <span className="faint small nowrap">
            {tr(
              `${creds.data?.total ?? 0} / ${creds.data?.all ?? 0} 个`,
              `${creds.data?.total ?? 0} / ${creds.data?.all ?? 0}`,
            )}
          </span>
        }
      >
        {(egress.data?.items?.length ?? 0) > 0 && (
          <EgressStrip
            items={egress.data!.items}
            onChanged={async () => { await egress.reload(); await reloadAll(); }}
            active={filter.egress}
            onFilter={(id) => { setFilter({ ...filter, egress: id }); setPage(1); }}
          />
        )}
        <SelectionBar
          selected={sel.size}
          total={creds.data?.total ?? 0}
          pageIds={items.map((c) => c.id)}
          sel={sel}
          loadAllIds={async () => (await api.credentialIds(filter)).ids}
        >
          {sel.size > 0 && (
            <div
              className="row bulk-actions"
              style={{ gap: 6 }}
              data-testid="bulk-actions"
            >
              <button
                className="btn btn-sm"
                disabled={!!busy}
                onClick={() => setBulk("add_to_group")}
                data-testid="bulk-add-group"
              >
                {tr("加入分组", "Add to group")}
              </button>
              <button
                className="btn btn-sm"
                disabled={!!busy}
                onClick={() => setBulk("remove_from_group")}
              >
                {tr("移出分组", "Remove from group")}
              </button>
              <button
                className="btn btn-sm"
                disabled={!!busy}
                onClick={() => setBulk("add_tags")}
                data-testid="bulk-add-tags"
              >
                {tr("加标签", "Add tags")}
              </button>
              <button
                className="btn btn-sm"
                disabled={!!busy}
                onClick={() => setBulk("remove_tags")}
              >
                {tr("去标签", "Remove tags")}
              </button>
              <button
                className="btn btn-sm"
                disabled={!!busy}
                onClick={() => setBulkEgress(true)}
                data-testid="bulk-egress"
              >
                {tr("设置出口", "Set egress")}
              </button>
              <button
                className="btn btn-sm"
                disabled={!!busy}
                onClick={() => void bulkToggle("enable")}
              >
                {busy === "bulk-enable" && <Spinner />}
                {tr("启用", "Enable")}
              </button>
              <button
                className="btn btn-sm btn-danger"
                disabled={!!busy}
                onClick={() => void bulkToggle("disable")}
              >
                {busy === "bulk-disable" && <Spinner />}
                {tr("禁用", "Disable")}
              </button>
            </div>
          )}
        </SelectionBar>
        {creds.error ? (
          <div className="card-body">
            <ErrorBox error={creds.error} onRetry={creds.reload} />
          </div>
        ) : !creds.data ? (
          <Loading />
        ) : items.length === 0 ? (
          <Empty>
            {creds.data.all
              ? tr("没有匹配的凭据", "No matching credentials")
              : tr(
                  "还没有凭据，点击右上角“添加凭据”。",
                  'No credentials yet — click "Add credential".',
                )}
          </Empty>
        ) : (
          <div ref={credCols.ref}>
            <table
              className="table cred-table"
              data-testid="credentials-table"
              style={{ minWidth: credCols.minTotal }}
            >
              <colgroup>
                {credCols.widths.map((w, i) => (
                  <col
                    key={i}
                    className={i >= 6 && i <= 10 ? "hide-sm" : undefined}
                    style={{ width: w }}
                  />
                ))}
              </colgroup>
              <thead>
                <tr>
                  <th style={{ width: 28 }} />
                  <th>
                    <SortTh k="seq" cur={sort} onSort={onSort}>ID</SortTh>
                  </th>
                  <th>
                    <SortTh k="name" cur={sort} onSort={onSort}>{tr("账号", "Account")}</SortTh>
                  </th>
                  <th>{tr("分组", "Groups")}</th>
                  <th>{t("provider")}</th>
                  <th>
                    <SortTh k="status" cur={sort} onSort={onSort}>{t("status")}</SortTh>
                    <span className="faint"> / </span>
                    <SortTh k="egress" cur={sort} onSort={onSort}>{tr("出口", "Egress")}</SortTh>
                  </th>
                  <th className="hide-sm">
                    <SortTh k="quota" cur={sort} onSort={onSort}>{t("quota")}</SortTh>
                  </th>
                  <th className="hide-sm nowrap">
                    <SortTh k="success" cur={sort} onSort={onSort}>{tr("成功率", "OK %")}</SortTh>
                    <StatsWindowSelect value={statsWindow} onChange={setStatsWindow} />
                  </th>
                  <th className="hide-sm">{t("priority")}</th>
                  <th className="hide-sm">
                    <SortTh k="created" cur={sort} onSort={onSort}>{tr("创建时间", "Created")}</SortTh>
                  </th>
                  <th className="hide-sm">
                    <SortTh k="last_used" cur={sort} onSort={onSort}>{tr("最近使用", "Last used")}</SortTh>
                  </th>
                  <th style={{ textAlign: "right" }}>{t("enabled")}</th>
                </tr>
              </thead>
              <tbody>
                {items.map((c) => (
                  <tr
                    key={c.id}
                    className={`clickable ${sel.has(c.id) ? "row-selected" : ""}`}
                    onClick={() => setSelected(c)}
                  >
                    <td onClick={(e) => e.stopPropagation()}>
                      <input
                        type="checkbox"
                        checked={sel.has(c.id)}
                        onChange={() => sel.toggle(c.id)}
                        aria-label="select"
                      />
                    </td>
                    <td onClick={(e) => e.stopPropagation()}>
                      <CopyText value={c.id} title={c.id} done={tr("已复制 ID", "ID copied")} className="mono">
                        {c.seq ? `#${c.seq}` : c.id.slice(0, 8)}
                      </CopyText>
                    </td>
                    <td>
                      <div
                        className="cell-title ellipsis"
                        title={[
                          c.label || c.email || c.name,
                          c.note && `${tr("备注", "Note")}: ${c.note}`,
                        ]
                          .filter(Boolean)
                          .join("\n")}
                      >
                        <CopyText
                          value={c.email || c.label || c.name}
                          done={tr("已复制账号", "Account copied")}
                          title={[c.email || c.label || c.name, c.note && `${tr("备注", "Note")}: ${c.note}`].filter(Boolean).join("\n")}
                          testId="copy-account"
                        >
                          {c.label || c.email || c.name}
                        </CopyText>
                        {c.note && <span className="faint small"> · 📝</span>}
                      </div>
                    </td>
                    <td>
                      {(() => {
                        const gs = (c.group_ids ?? []).map(
                          (id) => groups.cgNames.get(id) ?? `#${id}`,
                        );
                        const tags = (c.tags ?? []).map((tg) => `#${tg}`);
                        if (!gs.length && !tags.length)
                          return <span className="faint small">—</span>;
                        const hover = [
                          gs.length
                            ? `${tr("分组", "Groups")}: ${gs.join(", ")}`
                            : "",
                          tags.length
                            ? `${tr("标签", "Tags")}: ${tags.join(", ")}`
                            : "",
                        ]
                          .filter(Boolean)
                          .join("\n");
                        const extra = Math.max(gs.length - 1, 0) + tags.length;
                        return (
                          <span
                            className="pill-row nowrap-row"
                            style={{ marginTop: 0 }}
                            title={hover}
                          >
                            {gs.length ? (
                              <span className="pill pill-group">{gs[0]}</span>
                            ) : (
                              <span className="pill pill-tag">{tags[0]}</span>
                            )}
                            {(gs.length ? extra : extra - 1) > 0 && (
                              <span className="pill pill-tag">
                                +{gs.length ? extra : extra - 1}
                              </span>
                            )}
                          </span>
                        );
                      })()}
                    </td>
                    <td title={c.provider}>
                      <Badge kind="primary">{c.provider}</Badge>
                    </td>
                    <td>
                      <span
                        className="cell-status"
                        title={statusTooltip(c, tr)}
                      >
                        {credentialStatus(c, t, tr)}{healthBadges(c)}
                      </span>
                      {c.egress_ip && (() => {
                        const eg = egress.data?.items?.find((e) => e.name === c.egress_id);
                        const shown = eg ? egressIP(eg) : c.egress_ip;
                        return (
                          <div
                            className="faint small mono nowrap"
                            title={
                              tr(`固定出口 ${c.egress_id ?? ""}`, `Pinned egress ${c.egress_id ?? ""}`) +
                              (eg?.public_changed ? tr(` · 公网 IP 由 ${eg.expected_public_ip} 变为 ${eg.public_ip}`, ` · public IP ${eg.expected_public_ip} → ${eg.public_ip}`) : "")
                            }
                          >
                            {shown}
                            {eg && <span className="faint"> {egressRole(eg)}</span>}
                            {eg?.public_changed && <span style={{ color: "var(--warn, #b7791f)" }}> ⚠</span>}
                          </div>
                        );
                      })()}
                    </td>
                    <td className="hide-sm wrap-ok">
                      <QuotaWindows
                        item={quotaById.get(c.id)}
                        compact
                        error={quota.error}
                        loading={!quota.data && !quota.error}
                      />
                    </td>
                    <td className="hide-sm">
                      <CallStatsCell s={c.call_stats} window={statsWindow} />
                    </td>
                    <td className="hide-sm">
                      {c.priority ?? <span className="faint">0</span>}
                    </td>
                    <td
                      className="hide-sm small nowrap"
                      title={c.created_at ? new Date(c.created_at).toLocaleString() : ""}
                    >
                      {c.created_at ? fmtResetShort(c.created_at) : <span className="faint">—</span>}
                    </td>
                    <td
                      className="hide-sm faint small nowrap"
                      title={
                        (c.last_used_at ? new Date(c.last_used_at).toLocaleString() : tr("3 天内未使用", "unused in 3 days")) +
                        (c.updated_at ? ` · ${tr("更新", "updated")} ${new Date(c.updated_at).toLocaleString()}` : "")
                      }
                    >
                      {c.last_used_at ? fmtRelative(c.last_used_at, lang) : "—"}
                    </td>
                    <td
                      className="actions"
                      onClick={(e) => e.stopPropagation()}
                    >
                      <Switch
                        checked={!c.disabled}
                        disabled={busy === `toggle-${c.id}`}
                        onChange={(v) => toggle(c, v)}
                      />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        <PageNav
          page={page}
          pageSize={size}
          total={creds.data?.total ?? 0}
          onPage={setPage}
          onPageSize={(n) => (setSize(n), setPage(1))}
        />
      </Card>

      {bulk && (
        <BulkCredentialModal
          action={bulk}
          ids={sel.list()}
          groups={groups.data?.credential_groups ?? []}
          tagFacets={Object.keys(creds.data?.facets.tag ?? {})}
          onClose={() => setBulk(null)}
          onDone={async () => {
            setBulk(null);
            await reloadAll();
          }}
        />
      )}
      {bulkEgress && (
        <BulkEgressModal
          ids={sel.list()}
          onClose={() => setBulkEgress(false)}
          onDone={async () => {
            setBulkEgress(false);
            await reloadAll();
          }}
        />
      )}
      {adding && (
        <AddCredentialModal
          onClose={() => setAdding(false)}
          onAdded={() => {
            void creds.reload();
            void quota.reload();
          }}
        />
      )}
      {current && (
        <CredentialDrawer
          cred={current}
          statsWindow={statsWindow}
          onStatsWindow={setStatsWindow}
          quota={quotaById.get(current.id)}
          onClose={() => setSelected(null)}
          onChanged={async () => {
            await reloadAll();
            await quota.reload();
          }}
          onDeleted={async () => {
            setSelected(null);
            await reloadAll();
          }}
        />
      )}
    </div>
  );
}

function BulkCredentialModal({
  action,
  ids,
  groups,
  tagFacets,
  onClose,
  onDone,
}: {
  action: "add_to_group" | "remove_from_group" | "add_tags" | "remove_tags";
  ids: string[];
  groups: CredentialGroupSummary[];
  tagFacets: string[];
  onClose: () => void;
  onDone: () => Promise<void>;
}) {
  const api = useApi();
  const { tr } = useI18n();
  const { toast } = useUi();
  const { busy, run } = useAction();
  const [groupId, setGroupId] = useState(0);
  const [tags, setTags] = useState("");
  const isGroup = action.endsWith("group");
  const titles: Record<typeof action, [string, string]> = {
    add_to_group: [
      `将 ${ids.length} 个凭据加入分组`,
      `Add ${ids.length} credentials to a group`,
    ],
    remove_from_group: [
      `将 ${ids.length} 个凭据移出分组`,
      `Remove ${ids.length} credentials from a group`,
    ],
    add_tags: [
      `为 ${ids.length} 个凭据添加标签`,
      `Tag ${ids.length} credentials`,
    ],
    remove_tags: [
      `从 ${ids.length} 个凭据移除标签`,
      `Untag ${ids.length} credentials`,
    ],
  };
  const tagList = tags
    .split(/[,，\s]+/)
    .map((x) => x.trim().toLowerCase())
    .filter(Boolean);
  const valid = isGroup ? groupId > 0 : tagList.length > 0;
  const apply = () =>
    run("apply", async () => {
      const r = await api.bulkCredentials(
        isGroup
          ? { ids, action, group_id: groupId }
          : { ids, action, tags: tagList },
      );
      if (r.result) {
        toast(
          action === "add_to_group"
            ? tr(
                `已加入 ${r.result.added} 个${r.result.unchanged ? `（${r.result.unchanged} 个原本就在组内）` : ""}`,
                `Added ${r.result.added}${r.result.unchanged ? ` (${r.result.unchanged} already in)` : ""}`,
              )
            : tr(
                `已移出 ${r.result.removed} 个（规则匹配的成员需修改规则）`,
                `Removed ${r.result.removed} (rule members follow the rule)`,
              ),
        );
      } else {
        toast(
          tr(`已更新 ${r.updated ?? 0} 个`, `Updated ${r.updated ?? 0}`),
          r.failed?.length ? "err" : "ok",
        );
      }
      await onDone();
    });
  const target = groups.find((g) => g.id === groupId);
  return (
    <Modal
      title={tr(...titles[action])}
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            {tr("取消", "Cancel")}
          </button>
          <button
            className="btn btn-primary"
            disabled={!valid || !!busy}
            onClick={() => void apply()}
            data-testid="bulk-apply"
          >
            {busy && <Spinner />}
            {tr("应用", "Apply")}
          </button>
        </>
      }
    >
      {isGroup ? (
        <div className="stack">
          <select
            className="select"
            value={groupId}
            onChange={(e) => setGroupId(Number(e.target.value))}
            data-testid="bulk-group"
          >
            <option value={0}>
              {tr("选择凭据分组…", "Choose a credential group…")}
            </option>
            {groups.map((g) => (
              <option key={g.id} value={g.id}>
                {g.channel_name} ·{" "}
                {tr(`${g.total} 个成员`, `${g.total} members`)}
              </option>
            ))}
          </select>
          {target && target.keys > 0 && (
            <div className="alert warn small">
              {tr(
                `有 ${target.keys} 个访问密钥绑定此分组，变更会立即影响它们可使用的凭据（无回退）。`,
                `${target.keys} keys are bound to this group; the change affects them immediately (no fallback).`,
              )}
            </div>
          )}
          <p className="hint">
            {tr(
              "在一个事务中完成；已在组内的凭据会被跳过。",
              "Runs in one transaction; existing members are skipped.",
            )}
          </p>
        </div>
      ) : (
        <div className="field">
          <label>
            {tr("标签（逗号分隔，小写）", "Tags (comma separated)")}
          </label>
          <input
            className="input"
            list="bulk-tags"
            value={tags}
            autoFocus
            onChange={(e) => setTags(e.target.value)}
            placeholder="vip, eu"
            data-testid="bulk-tags"
          />
          <datalist id="bulk-tags">
            {tagFacets.map((t) => (
              <option key={t} value={t} />
            ))}
          </datalist>
          <span className="hint">
            {tr(
              "标签可用于筛选，以及凭据分组的自动加入规则。",
              "Tags drive filters and credential-group auto-join rules.",
            )}
          </span>
        </div>
      )}
    </Modal>
  );
}

function CredentialDrawer({
  cred,
  quota,
  onClose,
  onChanged,
  onDeleted,
  statsWindow,
  onStatsWindow,
}: {
  cred: CredentialSearchItem;
  statsWindow: StatsWindow;
  onStatsWindow: (w: StatsWindow) => void;
  quota?: QuotaItem;
  onClose: () => void;
  onChanged: () => Promise<void>;
  onDeleted: () => Promise<void>;
}) {
  const api = useApi();
  const { t, tr } = useI18n();
  const { toast, confirm } = useUi();
  const { busy, run } = useAction();
  const egressList = useEgressList();
  const [form, setForm] = useState({
    priority: cred.priority?.toString() ?? "",
    prefix: cred.prefix ?? "",
    proxy_url: cred.proxy_url ?? "",
    egress: cred.egress_id || (cred.proxy_url ? "custom" : "auto"),
    note: cred.note ?? "",
    requestRetry: cred["request-retry"]?.toString() ?? "",
    cooling:
      cred["disable-cooling"] === undefined
        ? "inherit"
        : cred["disable-cooling"]
          ? "off"
          : "on",
  });
  const models = useAsync<ModelInfo[]>(
    () => api.credentialModels(cred.id).then((r) => r.models ?? []),
    [cred.id],
  );

  const save = () =>
    run("save", async () => {
      const fields: Record<string, unknown> = {
        prefix: form.prefix.trim(),
        note: form.note.trim(),
        "request-retry":
          form.requestRetry.trim() === "" ? null : Number(form.requestRetry),
        "disable-cooling":
          form.cooling === "inherit" ? null : form.cooling === "off",
      };
      if (form.priority.trim() !== "") fields.priority = Number(form.priority);
      const initialEgress = cred.egress_id || (cred.proxy_url ? "custom" : "auto");
      if (form.egress === "custom") {
        if (form.proxy_url.trim() !== (cred.proxy_url ?? "")) fields.proxy_url = form.proxy_url.trim();
      } else if (form.egress !== initialEgress) {
        fields.egress_id = form.egress;
      }
      const ipChanges = form.egress !== initialEgress || fields.proxy_url !== undefined;
      if (ipChanges) {
        const target =
          form.egress === "auto"
            ? tr("自动均衡", "auto-balance")
            : form.egress === "custom"
              ? form.proxy_url.trim() || tr("直连", "direct")
              : (egressList.data?.find((e) => e.name === form.egress)?.ip ?? form.egress);
        const ok = await confirm({
          title: tr("更换出口", "Change egress"),
          body: tr(
            `出口 IP 将由 ${cred.egress_ip || cred.proxy_url || "—"} 改为 ${target}，该凭据所有流量随之切换。`,
            `Egress changes from ${cred.egress_ip || cred.proxy_url || "—"} to ${target} for all of this credential's traffic.`,
          ),
        });
        if (!ok) return;
      }
      await api.updateCredentialFields(cred.id, fields);
      toast(t("saved"));
      await onChanged();
    });

  const actions: {
    id: string;
    label: string;
    fn: () => Promise<unknown>;
    done: string;
  }[] = [
    {
      id: "refresh",
      label: t("refreshToken"),
      fn: () => api.refreshCredential(cred.id),
      done: tr("令牌已刷新", "Token refreshed"),
    },
    {
      id: "quota",
      label: t("fetchQuota"),
      fn: () => api.fetchQuota(cred.id),
      done: tr("已开始查询额度，稍后自动更新", "Quota collection queued"),
    },
    {
      id: "cooldown",
      label: t("resetCooldown"),
      fn: () => api.resetCooldown(cred.id),
      done: tr("冷却已清除", "Cooldown cleared"),
    },
  ];

  return (
    <Drawer title={cred.label || cred.email || cred.name} onClose={onClose}>
      <div className="stack">
        <div className="row">
          <Badge kind="primary">{cred.provider}</Badge>
          {credentialStatus(cred, t, tr)}{healthBadges(cred)}
          <QuotaStatusBadge item={quota} />
        </div>
        {cred.credential_scope_error && cred.status_message && (
          <div className="alert warn small">{cred.status_message}</div>
        )}
        <ModelCooldownList cred={cred} onChanged={onChanged} />

        <dl className="kv small">
          <dt>ID</dt>
          <dd className="mono">{cred.seq ? `#${cred.seq} · ` : ""}{cred.id}</dd>
          <dt>{tr("文件名", "File")}</dt>
          <dd className="mono">{cred.name}</dd>
          <dt>{tr("来源", "Source")}</dt>
          <dd>{cred.source ?? "—"}</dd>
          <dt>{t("created")}</dt>
          <dd>{fmtTime(cred.created_at)}</dd>
          <dt>{t("updated")}</dt>
          <dd>{fmtTime(cred.updated_at)}</dd>
          {quota?.observed_at && (
            <>
              <dt>{tr("额度采集", "Quota observed")}</dt>
              <dd>
                {fmtTime(quota.observed_at)}{" "}
                {quota.freshness === "stale" && (
                  <Badge kind="warn">{tr("已过期", "stale")}</Badge>
                )}
              </dd>
            </>
          )}
        </dl>

        <div className="row">
          {actions.map((a) => (
            <button
              key={a.id}
              className="btn btn-sm"
              disabled={!!busy}
              onClick={() =>
                run(a.id, async () => {
                  await a.fn();
                  toast(a.done);
                  await onChanged();
                })
              }
            >
              {busy === a.id && <Spinner />}
              {a.label}
            </button>
          ))}
          <button
            className="btn btn-sm"
            onClick={() =>
              run("download", async () => {
                const blob = await api.downloadCredential(cred.id);
                downloadBlob(
                  blob,
                  cred.name.endsWith(".json") ? cred.name : `${cred.name}.json`,
                );
              })
            }
          >
            <IconDownload />
            {t("download")}
          </button>
        </div>

        <div>
          <h3 style={{ marginBottom: 8 }}>{t("quota")}</h3>
          <QuotaWindows item={quota} />
        </div>

        <CallStatsPanel credentialId={cred.id} window={statsWindow} onWindow={onStatsWindow} />

        <CredentialGroupsEditor cred={cred} onChanged={onChanged} />

        <div>
          <h3 style={{ marginBottom: 8 }}>
            {tr("路由设置", "Routing settings")}
          </h3>
          <div className="form-grid">
            <div className="field">
              <label>{t("priority")}</label>
              <input
                className="input"
                type="number"
                value={form.priority}
                onChange={(e) => setForm({ ...form, priority: e.target.value })}
                placeholder="0"
              />
              <span className="hint">
                {tr("数值越大越优先被选择", "Higher values are picked first")}
              </span>
            </div>
            <div className="field">
              <label>{t("prefix")}</label>
              <input
                className="input"
                value={form.prefix}
                onChange={(e) => setForm({ ...form, prefix: e.target.value })}
                placeholder="team-a"
              />
              <span className="hint">
                {tr(
                  "设置后通过 “前缀/模型” 访问",
                  'Models become "prefix/model"',
                )}
              </span>
            </div>
            <div className="field">
              <label>{tr("额外重试轮数", "Request retry")}</label>
              <input
                className="input"
                type="number"
                min={0}
                value={form.requestRetry}
                onChange={(e) =>
                  setForm({ ...form, requestRetry: e.target.value })
                }
                placeholder={tr("继承全局", "inherit")}
              />
            </div>
            <div className="field">
              <label>{tr("冷却", "Cooldown")}</label>
              <select
                className="select"
                value={form.cooling}
                onChange={(e) => setForm({ ...form, cooling: e.target.value })}
              >
                <option value="inherit">
                  {tr("继承全局设置", "Inherit global")}
                </option>
                <option value="on">
                  {tr("出错时冷却", "Cool down on errors")}
                </option>
                <option value="off">{tr("不冷却", "Never cool down")}</option>
              </select>
            </div>
            <div className="field full">
              <label>{tr("出口", "Egress")}</label>
              <EgressSelect
                value={form.egress}
                onChange={(v) => setForm({ ...form, egress: v })}
                allowCustom
                testId="cred-egress"
              />
              {form.egress === "custom" && (
                <input
                  className="input"
                  style={{ marginTop: 6 }}
                  value={form.proxy_url}
                  onChange={(e) =>
                    setForm({ ...form, proxy_url: e.target.value })
                  }
                  placeholder="socks5://user:pass@host:1080"
                />
              )}
            </div>
            <div className="field full">
              <label>{t("note")}</label>
              <input
                className="input"
                value={form.note}
                onChange={(e) => setForm({ ...form, note: e.target.value })}
              />
            </div>
          </div>
          <div className="row" style={{ marginTop: 12 }}>
            <button
              className="btn btn-primary"
              disabled={busy === "save"}
              onClick={save}
            >
              {busy === "save" && <Spinner />}
              {t("save")}
            </button>
          </div>
        </div>

        <div>
          <h3 style={{ marginBottom: 8 }}>
            {tr("可用模型", "Models")}{" "}
            {models.data && (
              <span className="faint">({models.data.length})</span>
            )}
          </h3>
          {models.error ? (
            <ErrorBox error={models.error} />
          ) : !models.data ? (
            <Loading />
          ) : models.data.length ? (
            models.data.map((m) => (
              <span key={m.id} className="tag" title={m.display_name}>
                {m.id}
              </span>
            ))
          ) : (
            <span className="faint">—</span>
          )}
        </div>

        <div className="card" style={{ borderColor: "var(--err)" }}>
          <div className="card-body row">
            <div className="spacer">
              <b>{t("dangerZone")}</b>
              <div className="faint small">
                {tr(
                  "删除后无法恢复（可先下载备份）。",
                  "Deletion is permanent (download a backup first).",
                )}
              </div>
            </div>
            <button
              className="btn btn-danger"
              data-testid="delete-credential"
              onClick={async () => {
                const ok = await confirm({
                  title: tr("删除凭据", "Delete credential"),
                  body: tr(
                    `将永久删除 ${cred.label || cred.name}。`,
                    `This permanently deletes ${cred.label || cred.name}.`,
                  ),
                  danger: true,
                  confirmText: t("delete"),
                  typeToConfirm: "DELETE",
                });
                if (!ok) return;
                await run("delete", async () => {
                  await api.deleteCredential(cred.id);
                  toast(tr("已删除", "Deleted"));
                  await onDeleted();
                });
              }}
            >
              <IconTrash />
              {t("delete")}
            </button>
          </div>
        </div>
      </div>
    </Drawer>
  );
}

function ModelCooldownList({ cred, onChanged }: { cred: Credential; onChanged?: () => Promise<void> }) {
  const { tr, lang } = useI18n();
  const api = useApi();
  const { toast } = useUi();
  const { busy, run } = useAction();
  const items = cred.model_cooldowns ?? [];
  if (!items.length) return null;
  return (
    <div className="stack" style={{ gap: 6 }} data-testid="model-cooldowns">
      <div className="small strong">
        {tr(
          "按模型的冷却 / 最近错误（仅影响对应模型）",
          "Per-model cooldown / recent errors (only that model is affected)",
        )}
      </div>
      <table className="table small">
        <thead>
          <tr>
            <th>{tr("模型", "Model")}</th>
            <th>{tr("状态", "State")}</th>
            <th>{tr("恢复时间", "Retry at")}</th>
            <th />
          </tr>
        </thead>
        <tbody>
          {items.map((m) => (
            <tr key={m.model}>
              <td className="mono">{m.model}</td>
              <td>
                <Badge kind={m.cooling ? "warn" : undefined} dot>
                  {m.cooling
                    ? m.quota_exceeded
                      ? tr("额度耗尽冷却", "Quota cooldown")
                      : tr("冷却中", "Cooling")
                    : tr("最近失败", "Recent error")}
                  {m.http_status ? ` · ${m.http_status}` : ""}
                </Badge>
                {m.status_message && (
                  <div
                    className="faint small ellipsis"
                    style={{ maxWidth: 280 }}
                    title={m.status_message}
                  >
                    {m.status_message}
                  </div>
                )}
              </td>
              <td className="small">
                {m.next_retry_after
                  ? `${fmtTime(m.next_retry_after)} (${fmtRelative(m.next_retry_after, lang)})`
                  : "—"}
                {probeLine(m, tr) && <div className="faint small">{probeLine(m, tr)}</div>}
              </td>
              <td>
                {m.cooling && (
                  <button
                    className="btn small"
                    disabled={busy === `clear-${m.model}`}
                    onClick={() =>
                      run(`clear-${m.model}`, async () => {
                        await api.resetCooldown(cred.id, m.model);
                        toast(tr("冷却已清除", "Cooldown cleared"));
                        await onChanged?.();
                      })
                    }
                  >
                    {tr("清除冷却", "Clear cooldown")}
                  </button>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function CredentialGroupsEditor({
  cred,
  onChanged,
}: {
  cred: CredentialSearchItem;
  onChanged: () => Promise<void>;
}) {
  const api = useApi();
  const { tr } = useI18n();
  const { toast } = useUi();
  const { busy, run } = useAction();
  const groups = useGroupLookup();
  const d = groups.data;
  const [value, setValue] = useState<number[]>(cred.group_ids ?? []);
  useEffect(() => setValue(cred.group_ids ?? []), [cred.group_ids]);
  if (!d) return null;
  const change = (next: number[]) =>
    run("groups", async () => {
      let ruleKept = 0;
      for (const id of next.filter((x) => !value.includes(x)))
        await api.bulkCredentialGroupMembers(id, { add: [cred.id] });
      for (const id of value.filter((x) => !next.includes(x))) {
        const r = await api.bulkCredentialGroupMembers(id, {
          remove: [cred.id],
        });
        if (r.result.removed === 0) ruleKept++;
      }
      if (ruleKept)
        toast(
          tr(
            "该凭据由分组规则自动加入，需修改规则才能移出。",
            "This credential joined via a group rule; change the rule to remove it.",
          ),
          "err",
        );
      setValue(next);
      await groups.reload();
      await onChanged();
    });
  return (
    <div data-testid="cred-groups-editor">
      <h3 style={{ marginBottom: 8 }}>
        {tr("凭据分组", "Credential groups")} {busy === "groups" && <Spinner />}
      </h3>
      <GroupChecklist
        groups={d.credential_groups.map((g) => ({
          id: g.id,
          name: g.channel_name,
          enabled: g.enabled,
          extra: g.has_rule ? tr("含规则", "has rule") : undefined,
        }))}
        value={value}
        onChange={(v) => void change(v)}
      />
      <p className="hint" style={{ marginTop: 6 }}>
        {tr(
          "绑定了凭据分组的访问密钥只会使用分组内的凭据。凭据可同时属于多个分组；不属于任何分组的凭据只服务未绑定分组的密钥。",
          "Keys bound to credential groups only use members. A credential may be in several groups; ungrouped credentials only serve keys without groups.",
        )}
      </p>
      <TagsEditor cred={cred} onChanged={onChanged} />
    </div>
  );
}

function TagsEditor({
  cred,
  onChanged,
}: {
  cred: CredentialSearchItem;
  onChanged: () => Promise<void>;
}) {
  const api = useApi();
  const { t, tr } = useI18n();
  const { toast } = useUi();
  const { busy, run } = useAction();
  const [text, setText] = useState((cred.tags ?? []).join(", "));
  useEffect(() => setText((cred.tags ?? []).join(", ")), [cred.tags]);
  const tags = Array.from(
    new Set(
      text
        .split(/[,，\s]+/)
        .map((x) => x.trim().toLowerCase())
        .filter(Boolean),
    ),
  ).sort();
  const dirty = tags.join(",") !== (cred.tags ?? []).join(",");
  return (
    <div
      className="field"
      style={{ marginTop: 12 }}
      data-testid="cred-tags-editor"
    >
      <label>{tr("标签（逗号分隔）", "Tags (comma separated)")}</label>
      <div className="row" style={{ gap: 6, flexWrap: "nowrap" }}>
        <input
          className="input"
          value={text}
          onChange={(e) => setText(e.target.value)}
          placeholder="vip, eu"
          data-testid="cred-tags-input"
        />
        <button
          className="btn"
          disabled={!dirty || !!busy}
          data-testid="cred-tags-save"
          onClick={() =>
            void run("tags", async () => {
              await api.bulkCredentials({
                ids: [cred.id],
                action: "set_tags",
                tags,
              });
              toast(t("saved"));
              await onChanged();
            })
          }
        >
          {busy === "tags" && <Spinner />}
          {t("save")}
        </button>
      </div>
      <span className="hint">
        {tr(
          "用于筛选与分组自动加入规则。",
          "Used by filters and group auto-join rules.",
        )}
      </span>
    </div>
  );
}

function BulkEgressModal({
  ids,
  onClose,
  onDone,
}: {
  ids: string[];
  onClose: () => void;
  onDone: () => Promise<void>;
}) {
  const api = useApi();
  const { tr } = useI18n();
  const { toast, toastError } = useUi();
  const [egress, setEgress] = useState("auto");
  const [allowPrimary, setAllowPrimary] = useState<boolean | undefined>(undefined);
  const [busy, setBusy] = useState(false);
  const apply = async () => {
    setBusy(true);
    try {
      const r = await api.egressAssign(ids, egress, egress === "auto" ? allowPrimary : undefined);
      if (r.failed.length > 0) {
        toastError(new Error(tr(`已更新 ${r.applied} 个，失败 ${r.failed.length} 个：${r.failed[0].error}`, `Set ${r.applied}, failed ${r.failed.length}: ${r.failed[0].error}`)));
      } else {
        toast(tr(`已更新 ${r.applied} 个凭据出口`, `Egress set for ${r.applied}`));
      }
      await onDone();
    } catch (e) {
      toastError(e);
      await onDone();
    } finally {
      setBusy(false);
    }
  };
  return (
    <Modal
      title={tr(`设置出口（${ids.length} 个）`, `Set egress (${ids.length})`)}
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            {tr("取消", "Cancel")}
          </button>
          <button className="btn btn-primary" disabled={busy} onClick={() => void apply()} data-testid="bulk-egress-apply">
            {busy && <Spinner />}
            {tr("确认切换", "Apply")}
          </button>
        </>
      }
    >
      <div className="field">
        <EgressSelect value={egress} onChange={setEgress} testId="bulk-egress-select" />
        {egress === "auto" && <AllowPrimaryToggle value={allowPrimary} onChange={setAllowPrimary} testId="bulk-allow-primary" />}
        <span className="hint">
          {tr("所选凭据的出口 IP 将切换，所有流量随之改变。", "Selected credentials switch exit IP for all traffic.")}
        </span>
      </div>
    </Modal>
  );
}

const EGRESS_OPEN_KEY = "cred.egress.expanded";

function EgressStrip({
  items,
  onChanged,
  active,
  onFilter,
}: {
  items: EgressStatus[];
  onChanged: () => Promise<void>;
  active?: string;
  onFilter: (id: string | undefined) => void;
}) {
  const api = useApi();
  const { tr } = useI18n();
  const { toast, toastError, confirm } = useUi();
  const [plan, setPlan] = useState<EgressMove[] | null>(null);
  const [allowPrimary, setAllowPrimary] = useState<boolean | undefined>(undefined);
  const [busy, setBusy] = useState(false);
  const [open, setOpen] = useState(() => {
    try {
      return localStorage.getItem(EGRESS_OPEN_KEY) === "1";
    } catch {
      return false;
    }
  });
  const toggle = () => {
    const v = !open;
    setOpen(v);
    try {
      localStorage.setItem(EGRESS_OPEN_KEY, v ? "1" : "0");
    } catch {
      /* ignore */
    }
  };
  const preview = async (ap: boolean | undefined) => {
    setBusy(true);
    try {
      setPlan((await api.egressRebalance(true, ap)).moves);
    } catch (e) {
      toastError(e);
    } finally {
      setBusy(false);
    }
  };
  const act = async (fn: () => Promise<unknown>, done: string) => {
    try {
      await fn();
      toast(done);
      await onChanged();
    } catch (e) {
      toastError(e);
    }
  };
  const egSettings = useEgressSettings();
  const cap = egSettings.data?.max_per_egress ?? 0;
  const live = items.filter((e) => !e.removed);
  const full = cap > 0 ? live.filter((e) => e.healthy && e.credentials >= cap).length : 0;
  const primaries = live.filter((e) => e.primary).length;
  const loads = live.map((e) => e.credentials);
  const lo = loads.length ? Math.min(...loads) : 0;
  const hi = loads.length ? Math.max(...loads) : 0;
  const down = live.filter((e) => !e.healthy).length;
  const removed = items.length - live.length;
  const changed = live.filter((e) => e.healthy && e.public_changed).length;
  const nodes = new Map<string, EgressStatus[]>();
  for (const e of items) {
    const n = egressNode(e);
    if (!nodes.has(n)) nodes.set(n, []);
    nodes.get(n)!.push(e);
  }
  const ipKey = (e: EgressStatus) => egressIP(e).split(".").map((x) => x.padStart(3, "0")).join(".");
  const grouped = [...nodes.entries()]
    .sort((a, b) => a[0].localeCompare(b[0], undefined, { numeric: true }))
    .map(([n, list]) => [n, list.sort((a, b) => Number(!!b.primary) - Number(!!a.primary) || ipKey(a).localeCompare(ipKey(b)))] as const);
  const activeEg = active ? items.find((e) => e.name === active) : undefined;
  return (
    <div className="card-body small" data-testid="egress-list">
      <div style={{ display: "flex", flexWrap: "wrap", gap: 8, alignItems: "center" }}>
        <button className="btn btn-xs" onClick={toggle} data-testid="egress-toggle">
          {open ? tr("收起", "Collapse") : tr("展开", "Expand")}
        </button>
        <span data-testid="egress-summary">
          {tr("出口", "Egress")} {live.length}
          <span className="faint"> · {tr("主", "P")} {primaries} / {tr("副", "S")} {live.length - primaries} · {tr("负载", "load")} {lo === hi ? lo : `${lo}–${hi}`}{cap > 0 ? ` / ${tr("上限", "cap")} ${cap}` : ""}</span>
        </span>
        {full > 0 && <Badge kind="warn" dot>{tr(`已满 ${full}`, `${full} full`)}</Badge>}
        {down > 0 && <Badge kind="err" dot>{tr(`不可用 ${down}`, `${down} down`)}</Badge>}
        {removed > 0 && <Badge kind="err" dot>{tr(`IP 已移除 ${removed}`, `${removed} removed`)}</Badge>}
        {changed > 0 && <Badge kind="warn" dot>{tr(`⚠ IP 变更 ${changed}`, `⚠ ${changed} changed`)}</Badge>}
        {active && (
          <button className="btn btn-xs" onClick={() => onFilter(undefined)} title={tr("清除出口筛选", "Clear egress filter")}>
            {tr("筛选", "Filter")}: {activeEg ? egressIP(activeEg) : active} ✕
          </button>
        )}
      <button
        className="btn btn-sm"
        data-testid="egress-rebalance"
        disabled={busy}
        onClick={() => void preview(allowPrimary)}
      >
        {tr("均衡到新出口", "Rebalance")}
      </button>
      </div>
      {open && (
        <div data-testid="egress-nodes" style={{ marginTop: 6, maxHeight: 140, overflowY: "auto", display: "grid", gridTemplateColumns: "max-content 1fr", columnGap: 10, rowGap: 4, alignItems: "start" }}>
          {grouped.map(([node, list]) => (
            <Fragment key={node}>
              <span className="faint mono nowrap" style={{ lineHeight: "20px" }}>{node}</span>
              <div style={{ display: "flex", flexWrap: "wrap", gap: 4 }}>
                {list.map((e) => (
                  <span key={e.name} style={{ display: "inline-flex", gap: 2, alignItems: "center" }}>
                    <span
                      role="button"
                      style={{ cursor: "pointer", outline: active === e.name ? "1px solid currentColor" : undefined, borderRadius: 999 }}
                      onClick={() => onFilter(active === e.name ? undefined : e.name)}
                      title={[
                        e.name,
                        e.ip !== egressIP(e) ? tr(`本地 ${e.ip}`, `local ${e.ip}`) : "",
                        e.removed || !e.healthy || e.public_changed ? egressState(e, tr) : "",
                        e.paused > 0 ? tr(`暂停 ${e.paused}`, `${e.paused} paused`) : "",
                        e.public_changed ? `${e.expected_public_ip} → ${e.public_ip}` : "",
                        e.last_error ?? "",
                        tr("点击筛选凭据", "Click to filter"),
                      ].filter(Boolean).join(" · ")}
                    >
                      <Badge kind={e.removed || !e.healthy ? "err" : e.public_changed ? "warn" : "ok"} dot>
                        <span className="mono">{egressIP(e)}</span> {egressRole(e)} · {e.credentials}
                      </Badge>
                    </span>
                    {e.public_changed && (
                      <button className="btn btn-xs" onClick={() => void act(() => api.egressAck(e.name), tr("已确认新 IP", "Acknowledged"))}>
                        {tr("确认", "Ack")}
                      </button>
                    )}
                    {e.removed && e.credentials === 0 && (
                      <button className="btn btn-xs" onClick={() => void act(() => api.egressForget(e.name), tr("已移除", "Removed"))}>
                        {tr("移除", "Forget")}
                      </button>
                    )}
                  </span>
                ))}
              </div>
            </Fragment>
          ))}
        </div>
      )}
      {plan && (
        <Modal
          title={tr(`均衡预览（${plan.length} 个凭据换 IP）`, `Rebalance preview (${plan.length} moves)`)}
          onClose={() => setPlan(null)}
          footer={
            <>
              <button className="btn" onClick={() => setPlan(null)}>{tr("取消", "Cancel")}</button>
              <button
                className="btn btn-primary"
                disabled={busy || plan.length === 0}
                data-testid="egress-rebalance-apply"
                onClick={async () => {
                  const ok = await confirm({
                    title: tr("确认均衡", "Apply rebalance"),
                    body: tr(`${plan.length} 个凭据的出口 IP 将改变。`, `${plan.length} credentials will change exit IP.`),
                  });
                  if (!ok) return;
                  setBusy(true);
                  await act(async () => {
                    const r = await api.egressRebalance(false, allowPrimary);
                    setPlan(null);
                    return r;
                  }, tr("已均衡", "Rebalanced"));
                  setBusy(false);
                }}
              >
                {tr("执行", "Apply")}
              </button>
            </>
          }
        >
          <AllowPrimaryToggle
            value={allowPrimary}
            onChange={(v) => {
              setAllowPrimary(v);
              void preview(v);
            }}
            testId="rebalance-allow-primary"
          />
          {plan.length === 0 ? (
            <Empty>{tr("已均衡，无需移动", "Already balanced")}</Empty>
          ) : (
            <table className="table">
              <thead>
                <tr>
                  <th>{tr("凭据", "Credential")}</th>
                  <th>{tr("原出口", "From")}</th>
                  <th>{tr("新出口", "To")}</th>
                </tr>
              </thead>
              <tbody>
                {plan.map((m) => (
                  <tr key={m.credential_id}>
                    <td>{m.label || m.credential_id.slice(0, 8)}</td>
                    <td className="mono small">{m.from_ip}</td>
                    <td className="mono small">{m.to_ip}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </Modal>
      )}
    </div>
  );
}
