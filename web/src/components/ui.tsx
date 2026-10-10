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

export function Modal({ title, onClose, children, footer, wide }: { title: ReactNode; onClose: () => void; children: ReactNode; footer?: ReactNode; wide?: boolean }) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === 'Escape' && onClose();
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onClose]);
  return createPortal(
    <div className="modal-backdrop" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className={`modal ${wide ? 'wide' : ''}`} role="dialog" aria-modal="true">
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

export function Drawer({ title, onClose, children }: { title: ReactNode; onClose: () => void; children: ReactNode }) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === 'Escape' && onClose();
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onClose]);
  return createPortal(
    <>
      <div className="drawer-backdrop" onClick={onClose} />
      <aside className="drawer" role="dialog">
        <div className="modal-head">
          <h2>{title}</h2>
          <button className="btn btn-ghost btn-icon" onClick={onClose} aria-label="close">
            <IconX />
          </button>
        </div>
        <div className="modal-body" style={{ flex: 1 }}>
          {children}
        </div>
      </aside>
    </>,
    document.body,
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
