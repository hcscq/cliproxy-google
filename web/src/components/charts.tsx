import { useMemo, useState } from 'react';
import type { UsageBucket } from '../api/client';
import { fmtCompact } from '../format';
import { useI18n } from '../i18n';

/** Stacked success/failure request bars for usage trend buckets. */
export function TrendChart({ buckets, height = 180 }: { buckets: UsageBucket[]; height?: number }) {
  const { tr } = useI18n();
  const [hover, setHover] = useState<number | null>(null);
  const width = 800;
  const padL = 36;
  const padB = 20;
  const max = useMemo(() => Math.max(1, ...buckets.map((b) => b.request_count || 0)), [buckets]);
  const n = Math.max(1, buckets.length);
  const bw = (width - padL) / n;
  const h = height - padB;
  const ticks = [0, 0.5, 1].map((r) => Math.round(max * r));
  const labelEvery = Math.max(1, Math.ceil(n / 8));
  const fmtLabel = (iso: string) => {
    const d = new Date(iso);
    const spanH = buckets.length > 1 ? (new Date(buckets[buckets.length - 1].bucket_start).getTime() - new Date(buckets[0].bucket_start).getTime()) / 3.6e6 : 0;
    return spanH > 48 ? `${d.getMonth() + 1}/${d.getDate()}` : `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`;
  };
  const hb = hover !== null ? buckets[hover] : null;
  return (
    <div style={{ position: 'relative' }}>
      <svg className="chart" viewBox={`0 0 ${width} ${height}`} preserveAspectRatio="none" style={{ height }} onMouseLeave={() => setHover(null)}>
        {ticks.map((tv, i) => {
          const y = h - (tv / max) * (h - 8);
          return (
            <g key={i}>
              <line className="axis" x1={padL} x2={width} y1={y} y2={y} strokeDasharray={i ? '3 3' : undefined} />
              <text x={padL - 6} y={y + 3} textAnchor="end">
                {fmtCompact(tv)}
              </text>
            </g>
          );
        })}
        {buckets.map((b, i) => {
          const x = padL + i * bw + bw * 0.15;
          const w = Math.max(1, bw * 0.7);
          const okH = ((b.success_count || 0) / max) * (h - 8);
          const errH = ((b.failed_count || 0) / max) * (h - 8);
          return (
            <g key={b.bucket_start} onMouseEnter={() => setHover(i)}>
              <rect x={padL + i * bw} y={0} width={bw} height={h} fill="transparent" />
              <rect className="bar-ok" x={x} y={h - okH} width={w} height={okH} rx={1.5} opacity={hover === i ? 1 : undefined} />
              <rect className="bar-err" x={x} y={h - okH - errH} width={w} height={errH} rx={1.5} />
              {i % labelEvery === 0 && (
                <text x={padL + i * bw + bw / 2} y={height - 5} textAnchor="middle">
                  {fmtLabel(b.bucket_start)}
                </text>
              )}
            </g>
          );
        })}
      </svg>
      {hb && (
        <div className="card small" style={{ position: 'absolute', top: 4, right: 4, padding: '6px 10px', pointerEvents: 'none' }}>
          <div className="faint">{new Date(hb.bucket_start).toLocaleString(undefined, { hour12: false })}</div>
          <div>
            {tr('请求', 'Requests')}: <b>{hb.request_count}</b> · {tr('失败', 'Failed')}: <b>{hb.failed_count}</b> · Tokens: <b>{fmtCompact(hb.total_tokens)}</b>
          </div>
        </div>
      )}
      <div className="legend" style={{ marginTop: 6 }}>
        <span>
          <i style={{ background: 'var(--primary)' }} />
          {tr('成功', 'Success')}
        </span>
        <span>
          <i style={{ background: 'var(--err)' }} />
          {tr('失败', 'Failed')}
        </span>
      </div>
    </div>
  );
}

/** Horizontal ranking bars. */
export function RankList({ items, valueLabel }: { items: { label: string; value: number; sub?: string }[]; valueLabel?: (v: number) => string }) {
  const max = Math.max(1, ...items.map((i) => i.value));
  return (
    <div className="stack" style={{ gap: 10 }}>
      {items.map((it) => (
        <div key={it.label}>
          <div className="row small" style={{ marginBottom: 4 }}>
            <span className="ellipsis mono spacer" title={it.label}>
              {it.label}
            </span>
            <span className="muted">{valueLabel ? valueLabel(it.value) : fmtCompact(it.value)}</span>
            {it.sub && <span className="faint">{it.sub}</span>}
          </div>
          <div className="bar">
            <div style={{ width: `${(it.value / max) * 100}%`, background: 'var(--primary)' }} />
          </div>
        </div>
      ))}
    </div>
  );
}
