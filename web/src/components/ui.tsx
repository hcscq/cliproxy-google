import { useEffect, useRef, useState, type ReactNode } from 'react';
import { createPortal } from 'react-dom';
import { useI18n } from '../i18n';
import { IconCopy, IconX } from './icons';

export function Spinner() {
  return <span className="spinner" role="status" aria-label="loading" />;
}

export function Loading() {
  return (
    <div className="center">
      <Spinner />
    </div>
  );
}

export function Empty({ children }: { children?: ReactNode }) {
  const { t } = useI18n();
  return <div className="empty">{children ?? t('empty')}</div>;
}

export function ErrorBox({ error, onRetry }: { error: unknown; onRetry?: () => void }) {
  const { tr } = useI18n();
  const msg = error instanceof Error ? error.message : String(error);
  return (
    <div className="alert err row">
      <span className="spacer">{msg}</span>
      {onRetry && (
        <button className="btn btn-sm" onClick={onRetry}>
          {tr('重试', 'Retry')}
        </button>
      )}
    </div>
  );
}

export function Card({ title, extra, children, bodyClass, className }: { title?: ReactNode; extra?: ReactNode; children: ReactNode; bodyClass?: string; className?: string }) {
  return (
    <section className={`card ${className ?? ''}`}>
      {(title || extra) && (
        <div className="card-head">
          {typeof title === 'string' ? <h2>{title}</h2> : title}
          {extra && <div className="toolbar">{extra}</div>}
        </div>
      )}
      <div className={bodyClass ?? 'card-body'}>{children}</div>
    </section>
  );
}

export function Stat({ label, value, sub, icon }: { label: ReactNode; value: ReactNode; sub?: ReactNode; icon?: ReactNode }) {
  return (
    <div className="card stat">
      <div className="stat-label">
        {icon}
        {label}
      </div>
      <div className="stat-value">{value}</div>
      {sub !== undefined && <div className="stat-sub">{sub}</div>}
    </div>
  );
}

export function Badge({ kind, children, dot }: { kind?: 'ok' | 'warn' | 'err' | 'primary'; children: ReactNode; dot?: boolean }) {
  return (
    <span className={`badge ${kind ?? ''}`}>
      {dot && <span className="dot" />}
      {children}
    </span>
  );
}

export function Switch({ checked, onChange, disabled, label }: { checked: boolean; onChange: (v: boolean) => void; disabled?: boolean; label?: ReactNode }) {
  return (
    <label className="check">
      <span className="switch">
        <input type="checkbox" checked={checked} disabled={disabled} onChange={(e) => onChange(e.target.checked)} />
        <span />
      </span>
      {label}
    </label>
  );
}

export function Modal({ title, onClose, children, footer, wide, full }: { title: ReactNode; onClose: () => void; children: ReactNode; footer?: ReactNode; wide?: boolean; full?: boolean }) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return;
      e.stopImmediatePropagation(); // close only the top-most layer
      onClose();
    };
    window.addEventListener('keydown', onKey, true);
    return () => window.removeEventListener('keydown', onKey, true);
  }, [onClose]);
  return createPortal(
    <div className="modal-backdrop" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className={`modal ${wide ? 'wide' : ''} ${full ? 'full' : ''}`} role="dialog" aria-modal="true">
        <div className="modal-head">
          <h2>{title}</h2>
          <button className="btn btn-ghost btn-icon" onClick={onClose} aria-label="close">
            <IconX />
          </button>
        </div>
        <div className="modal-body">{children}</div>
        {footer && <div className="modal-foot">{footer}</div>}
      </div>
    </div>,
    document.body,
  );
}

export function Drawer({ title, onClose, children, extra }: { title: ReactNode; onClose: () => void; children: ReactNode; extra?: ReactNode }) {
  const [width, setWidth] = useState(() => Number(localStorage.getItem('panel.drawerWidth')) || 760);
  const [dragging, setDragging] = useState(false);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === 'Escape' && onClose();
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onClose]);
  useEffect(() => {
    if (!dragging) return;
    const move = (e: MouseEvent) => setWidth(Math.max(420, Math.min(window.innerWidth - 40, window.innerWidth - e.clientX)));
    const up = () => setDragging(false);
    window.addEventListener('mousemove', move);
    window.addEventListener('mouseup', up);
    return () => {
      window.removeEventListener('mousemove', move);
      window.removeEventListener('mouseup', up);
    };
  }, [dragging]);
  useEffect(() => {
    if (!dragging) localStorage.setItem('panel.drawerWidth', String(width));
  }, [dragging, width]);
  return createPortal(
    <>
      <div className="drawer-backdrop" onClick={onClose} />
      <aside className="drawer" role="dialog" style={{ ['--drawer-w' as string]: `${width}px`, userSelect: dragging ? 'none' : undefined }} data-testid="drawer">
        <div className={`drawer-resize ${dragging ? 'active' : ''}`} onMouseDown={() => setDragging(true)} title="drag to resize" />
        <div className="modal-head">
          <h2 className="ellipsis">{title}</h2>
          <div className="row">
            {extra}
            <button className="btn btn-ghost btn-icon" onClick={onClose} aria-label="close">
              <IconX />
            </button>
          </div>
        </div>
        <div className="modal-body" data-testid="drawer-body">
          {children}
        </div>
      </aside>
    </>,
    document.body,
  );
}

/** Pretty-prints JSON when possible. */
export function prettyText(text: string): string {
  const t = text.trim();
  if (t.startsWith('{') || t.startsWith('[')) {
    try {
      return JSON.stringify(JSON.parse(t), null, 2);
    } catch {
      /* not (complete) JSON */
    }
  }
  return text;
}

/** Scrollable code/JSON block with copy and full-screen view. */
export function CodeBlock({ text, title, maxHeight = 420, testId }: { text: string; title?: string; maxHeight?: number; testId?: string }) {
  const { tr } = useI18n();
  const [full, setFull] = useState(false);
  const pretty = prettyText(text);
  return (
    <div className="code-wrap">
      <div className="code-tools">
        <CopyButton text={pretty} small />
        <button className="btn btn-sm" onClick={() => setFull(true)} data-testid={testId ? `${testId}-expand` : undefined}>
          ⤢ {tr('全屏查看', 'Full view')}
        </button>
      </div>
      <pre className="code-block" style={{ maxHeight }} data-testid={testId}>
        {pretty}
      </pre>
      {full && (
        <Modal title={title ?? tr('完整内容', 'Full content')} onClose={() => setFull(false)} full footer={<CopyButton text={pretty} />}>
          <pre className="code-block" data-testid={testId ? `${testId}-full` : undefined}>
            {pretty}
          </pre>
        </Modal>
      )}
    </div>
  );
}

export function CopyButton({ text, small }: { text: string; small?: boolean }) {
  const { t } = useI18n();
  const [done, setDone] = useState(false);
  const timer = useRef<number>();
  useEffect(() => () => window.clearTimeout(timer.current), []);
  return (
    <button
      type="button"
      className={`btn ${small ? 'btn-sm' : ''}`}
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(text);
        } catch {
          const ta = document.createElement('textarea');
          ta.value = text;
          document.body.appendChild(ta);
          ta.select();
          document.execCommand('copy');
          ta.remove();
        }
        setDone(true);
        timer.current = window.setTimeout(() => setDone(false), 1500);
      }}
    >
      <IconCopy />
      {done ? t('copied') : t('copy')}
    </button>
  );
}

export function Progress({ ratio }: { ratio: number | undefined | null }) {
  const r = Math.max(0, Math.min(1, ratio ?? 0));
  const cls = r < 0.1 ? 'err' : r < 0.3 ? 'warn' : '';
  return (
    <div className={`bar ${cls}`}>
      <div style={{ width: `${(r * 100).toFixed(1)}%` }} />
    </div>
  );
}

export function Pager({ total, limit, offset, onChange }: { total: number; limit: number; offset: number; onChange: (offset: number) => void }) {
  const { tr } = useI18n();
  const page = Math.floor(offset / limit) + 1;
  const pages = Math.max(1, Math.ceil(total / limit));
  return (
    <div className="pager">
      <span className="faint small">{tr(`共 ${total} 条 · 第 ${page}/${pages} 页`, `${total} total · page ${page}/${pages}`)}</span>
      <button className="btn btn-sm" disabled={page <= 1} onClick={() => onChange(Math.max(0, offset - limit))}>
        {tr('上一页', 'Prev')}
      </button>
      <button className="btn btn-sm" disabled={page >= pages} onClick={() => onChange(offset + limit)}>
        {tr('下一页', 'Next')}
      </button>
    </div>
  );
}
