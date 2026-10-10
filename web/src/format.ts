export function fmtNum(n?: number | null, digits = 0): string {
  if (n === undefined || n === null || Number.isNaN(n)) return '—';
  return n.toLocaleString(undefined, { maximumFractionDigits: digits });
}

export function fmtCompact(n?: number | null): string {
  if (n === undefined || n === null || Number.isNaN(n)) return '—';
  const abs = Math.abs(n);
  if (abs >= 1e9) return `${(n / 1e9).toFixed(1)}B`;
  if (abs >= 1e6) return `${(n / 1e6).toFixed(1)}M`;
  if (abs >= 1e4) return `${(n / 1e3).toFixed(1)}K`;
  return fmtNum(n);
}

export function fmtPct(r?: number | null, digits = 1): string {
  if (r === undefined || r === null || Number.isNaN(r)) return '—';
  return `${(r * 100).toFixed(digits)}%`;
}

export function fmtMs(ms?: number | null): string {
  if (ms === undefined || ms === null || Number.isNaN(ms)) return '—';
  if (ms >= 1000) return `${(ms / 1000).toFixed(ms >= 10000 ? 0 : 1)} s`;
  return `${Math.round(ms)} ms`;
}

export function fmtTime(iso?: string | null): string {
  if (!iso) return '—';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime()) || d.getFullYear() < 2000) return '—';
  return d.toLocaleString(undefined, { hour12: false });
}

export function fmtRelative(iso: string | null | undefined, lang: 'zh' | 'en'): string {
  if (!iso) return '—';
  const d = new Date(iso).getTime();
  if (Number.isNaN(d)) return '—';
  const diff = (Date.now() - d) / 1000;
  const future = diff < 0;
  const s = Math.abs(diff);
  const units: [number, string, string][] = [
    [86400, '天', 'd'],
    [3600, '小时', 'h'],
    [60, '分钟', 'm'],
    [1, '秒', 's'],
  ];
  for (const [sec, zh, en] of units) {
    if (s >= sec || sec === 1) {
      const v = Math.floor(s / sec);
      if (lang === 'zh') return future ? `${v} ${zh}后` : `${v} ${zh}前`;
      return future ? `in ${v}${en}` : `${v}${en} ago`;
    }
  }
  return '—';
}

export function downloadBlob(blob: Blob, filename: string): void {
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}
