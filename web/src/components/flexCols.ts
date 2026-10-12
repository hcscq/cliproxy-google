import { useCallback, useEffect, useState } from 'react';

export interface FlexCol {
  min: number;
  max?: number;
  weight?: number; // 0 = fixed at min
}

/** Distribute `total` px across columns: start at min, hand extra out by weight, capped at max. */
export function distributeColumns(cols: FlexCol[], total: number): number[] {
  const w = cols.map((c) => c.min);
  let extra = total - w.reduce((a, b) => a + b, 0);
  for (let guard = 0; extra > 0.5 && guard < 10; guard++) {
    const open = cols.map((c, i) => ((c.weight ?? 0) > 0 && (c.max === undefined || w[i] < c.max) ? i : -1)).filter((i) => i >= 0);
    const sum = open.reduce((a, i) => a + (cols[i].weight ?? 0), 0);
    if (!sum) break;
    let used = 0;
    for (const i of open) {
      const want = (extra * (cols[i].weight ?? 0)) / sum;
      const room = cols[i].max === undefined ? want : Math.min(want, cols[i].max! - w[i]);
      w[i] += room;
      used += room;
    }
    extra -= used;
    if (used < 0.5) break;
  }
  return w.map((x) => Math.floor(x));
}

/** Measure a container and return proportional column widths for it. */
export function useFlexColumns<T extends HTMLElement>(cols: FlexCol[]) {
  const [el, setEl] = useState<T | null>(null);
  const [width, setWidth] = useState(0);
  useEffect(() => {
    if (!el) return;
    const ro = new ResizeObserver(() => setWidth(el.clientWidth));
    ro.observe(el);
    setWidth(el.clientWidth);
    return () => ro.disconnect();
  }, [el]);
  const ref = useCallback((node: T | null) => setEl(node), []);
  const minTotal = cols.reduce((a, c) => a + c.min, 0);
  return { ref, widths: distributeColumns(cols, Math.max(width, minTotal)), minTotal };
}
