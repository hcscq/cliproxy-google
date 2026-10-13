import { useApi, useAsync } from '../app-context';
import type { CallStats, StatsWindow } from '../api/client';
import { useI18n } from '../i18n';

type Tr = (zh: string, en: string) => string;

/** Error classes in display order (keys match the backend). */
export const CALL_CLASSES: [string, string, string][] = [
  ['429', '429 额度/限流', '429 quota/rate limit'],
  ['503', '503 无容量', '503 no capacity'],
  ['403', '403 禁止', '403 forbidden'],
  ['404', '404 模型下线', '404 model gone'],
  ['400', '400 请求无效/超长', '400 invalid/too long'],
  ['5xx', '5xx 上游', '5xx upstream'],
  ['net', '网络/出口', 'network/egress'],
  ['cancel', '客户端取消', 'client cancelled'],
  ['other', '其他', 'other'],
];

export const STATS_WINDOWS: StatsWindow[] = ['1h', '24h', '7d'];

export function fmtCompact(n: number): string {
  if (n >= 1e6) return `${(n / 1e6).toFixed(n >= 1e7 ? 0 : 1)}M`;
  if (n >= 1e3) return `${(n / 1e3).toFixed(n >= 1e4 ? 0 : 1)}k`;
  return String(n);
}

export function successPct(s?: CallStats): number | null {
  return s && s.calls > 0 ? (s.ok / s.calls) * 100 : null;
}

const pctText = (p: number) => (p >= 99.95 || p === 0 ? p.toFixed(0) : p.toFixed(p >= 99 ? 1 : 0)) + '%';

export function callStatsTooltip(s: CallStats | undefined, window: StatsWindow, tr: Tr): string {
  if (!s || s.calls === 0) return tr(`${window} 内无调用`, `no calls in ${window}`);
  const lines = [tr(`${window}：调用 ${s.calls} · 成功 ${s.ok}`, `${window}: ${s.calls} calls · ${s.ok} ok`)];
  for (const [k, zh, en] of CALL_CLASSES) {
    const n = s.by_class?.[k];
    if (n) lines.push(`${tr(zh, en)}: ${n} (${((n / s.calls) * 100).toFixed(1)}%)`);
  }
  return lines.join('\n');
}

/** Compact list cell: "92% · 1.2k". */
export function CallStatsCell({ s, window }: { s?: CallStats; window: StatsWindow }) {
  const { tr } = useI18n();
  const p = successPct(s);
  if (p === null) return <span className="faint" title={callStatsTooltip(s, window, tr)}>—</span>;
  const color = p < 70 ? 'var(--err, #c53030)' : p < 90 ? 'var(--warn, #b7791f)' : undefined;
  return (
    <span className="small nowrap" title={callStatsTooltip(s, window, tr)} data-testid="call-stats-cell">
      <span style={{ color }}>{pctText(p)}</span>
      <span className="faint"> · {fmtCompact(s!.calls)}</span>
    </span>
  );
}

export function StatsWindowSelect({ value, onChange }: { value: StatsWindow; onChange: (w: StatsWindow) => void }) {
  const { tr } = useI18n();
  return (
    <select
      className="select"
      style={{ padding: '0 2px', height: 20, fontSize: 11, width: 'auto', marginLeft: 4 }}
      value={value}
      title={tr('统计时间窗', 'Stats window')}
      onClick={(e) => e.stopPropagation()}
      onChange={(e) => onChange(e.target.value as StatsWindow)}
      data-testid="stats-window"
    >
      {STATS_WINDOWS.map((w) => (
        <option key={w} value={w}>{w}</option>
      ))}
    </select>
  );
}

function Row({ label, s }: { label: string; s: CallStats }) {
  const p = successPct(s);
  return (
    <tr>
      <td className="mono small">{label}</td>
      <td className="num">{s.calls}</td>
      <td className="num">{p === null ? '—' : pctText(p)}</td>
      {CALL_CLASSES.map(([k]) => (
        <td key={k} className="num faint">{s.by_class?.[k] || ''}</td>
      ))}
    </tr>
  );
}

/** Drawer section: totals + per-model breakdown. */
export function CallStatsPanel({ credentialId, window, onWindow }: { credentialId: string; window: StatsWindow; onWindow: (w: StatsWindow) => void }) {
  const api = useApi();
  const { tr } = useI18n();
  const st = useAsync(() => api.credentialCallStats(credentialId, window), [credentialId, window], 30000);
  const used = CALL_CLASSES;
  return (
    <div data-testid="call-stats-panel">
      <div style={{ display: 'flex', alignItems: 'center', gap: 6, marginBottom: 6 }}>
        <strong className="small">{tr('调用统计', 'Call stats')}</strong>
        <StatsWindowSelect value={window} onChange={onWindow} />
      </div>
      {!st.data ? (
        <span className="faint small">…</span>
      ) : st.data.total.calls === 0 ? (
        <span className="faint small">{tr(`${window} 内无调用`, `No calls in ${window}`)}</span>
      ) : (
        <div style={{ overflowX: 'auto' }}>
          <table className="table small">
            <thead>
              <tr>
                <th>{tr('模型', 'Model')}</th>
                <th className="num">{tr('调用', 'Calls')}</th>
                <th className="num">{tr('成功率', 'OK %')}</th>
                {used.map(([k, zh, en]) => (
                  <th key={k} className="num" title={tr(zh, en)}>{k === 'cancel' ? '499' : k === 'other' ? tr('其他', 'other') : k === 'net' ? tr('网络', 'net') : k}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              <Row label={tr('合计', 'Total')} s={st.data.total} />
              {[...st.data.models].sort((a, b) => b.calls - a.calls).map((m) => (
                <Row key={m.model} label={m.model || '—'} s={m} />
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
