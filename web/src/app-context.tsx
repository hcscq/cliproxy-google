import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { ApiClient, ApiError } from './api/client';
import { useI18n } from './i18n';
import { Modal } from './components/ui';

const KEY_STORAGE = 'panel.managementKey';

// ---- Auth ---------------------------------------------------------------------

interface AuthCtx {
  api: ApiClient;
  authed: boolean;
  login: (key: string, remember: boolean) => Promise<void>;
  logout: (reason?: string) => void;
  logoutReason?: string;
}

const AuthContext = createContext<AuthCtx | null>(null);

function storedKey(): string {
  return sessionStorage.getItem(KEY_STORAGE) || localStorage.getItem(KEY_STORAGE) || '';
}

export function AuthProvider({ children }: { children: ReactNode }) {
  const { t } = useI18n();
  const [key, setKey] = useState(storedKey);
  const [reason, setReason] = useState<string>();
  const api = useMemo(() => new ApiClient(key), []); // eslint-disable-line react-hooks/exhaustive-deps

  const logout = useCallback((why?: string) => {
    sessionStorage.removeItem(KEY_STORAGE);
    localStorage.removeItem(KEY_STORAGE);
    api.setKey('');
    setReason(why);
    setKey('');
  }, [api]);

  useEffect(() => {
    api.onUnauthorized = (err: ApiError) => {
      if (api && key) logout(err.status === 401 ? t('sessionExpired') : err.message);
    };
  }, [api, key, logout, t]);

  const login = useCallback(
    async (k: string, remember: boolean) => {
      const probe = new ApiClient(k);
      await probe.verify();
      sessionStorage.setItem(KEY_STORAGE, k);
      if (remember) localStorage.setItem(KEY_STORAGE, k);
      else localStorage.removeItem(KEY_STORAGE);
      api.setKey(k);
      setReason(undefined);
      setKey(k);
    },
    [api],
  );

  const value = useMemo(() => ({ api, authed: !!key, login, logout, logoutReason: reason }), [api, key, login, logout, reason]);
  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthCtx {
  const v = useContext(AuthContext);
  if (!v) throw new Error('AuthProvider missing');
  return v;
}

export function useApi(): ApiClient {
  return useAuth().api;
}

// ---- Theme --------------------------------------------------------------------

export type Theme = 'light' | 'dark';

export function useTheme(): [Theme, (t: Theme) => void] {
  const [theme, setTheme] = useState<Theme>(() => {
    const s = localStorage.getItem('panel.theme');
    if (s === 'dark' || s === 'light') return s;
    return window.matchMedia?.('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
  });
  useEffect(() => {
    document.documentElement.dataset.theme = theme;
    localStorage.setItem('panel.theme', theme);
  }, [theme]);
  return [theme, setTheme];
}

// ---- Toasts & confirm ---------------------------------------------------------

type ToastKind = 'ok' | 'err' | 'info';
interface Toast {
  id: number;
  kind: ToastKind;
  text: string;
}

interface ConfirmOpts {
  title: string;
  body?: ReactNode;
  danger?: boolean;
  confirmText?: string;
  /** Require typing this exact text before confirming. */
  typeToConfirm?: string;
}

interface UiCtx {
  toast: (text: string, kind?: ToastKind) => void;
  toastError: (e: unknown) => void;
  confirm: (opts: ConfirmOpts) => Promise<boolean>;
}

const UiContext = createContext<UiCtx | null>(null);

export function UiProvider({ children }: { children: ReactNode }) {
  const { t } = useI18n();
  const [toasts, setToasts] = useState<Toast[]>([]);
  const [pending, setPending] = useState<(ConfirmOpts & { resolve: (v: boolean) => void }) | null>(null);
  const [typed, setTyped] = useState('');
  const seq = useRef(0);

  const toast = useCallback((text: string, kind: ToastKind = 'ok') => {
    const id = ++seq.current;
    setToasts((ts) => [...ts, { id, kind, text }]);
    setTimeout(() => setToasts((ts) => ts.filter((x) => x.id !== id)), kind === 'err' ? 7000 : 3500);
  }, []);
  const toastError = useCallback(
    (e: unknown) => {
      const msg = e instanceof ApiError ? `${e.message}${e.status ? ` (HTTP ${e.status})` : ''}` : e instanceof Error ? e.message : String(e);
      toast(msg, 'err');
    },
    [toast],
  );
  const confirm = useCallback((opts: ConfirmOpts) => {
    setTyped('');
    return new Promise<boolean>((resolve) => setPending({ ...opts, resolve }));
  }, []);
  const close = (v: boolean) => {
    pending?.resolve(v);
    setPending(null);
  };
  const value = useMemo(() => ({ toast, toastError, confirm }), [toast, toastError, confirm]);
  const blocked = !!pending?.typeToConfirm && typed !== pending.typeToConfirm;

  return (
    <UiContext.Provider value={value}>
      {children}
      <div className="toasts" aria-live="polite">
        {toasts.map((x) => (
          <div key={x.id} className={`toast ${x.kind}`}>
            {x.text}
          </div>
        ))}
      </div>
      {pending && (
        <Modal
          title={pending.title}
          onClose={() => close(false)}
          footer={
            <>
              <button className="btn" onClick={() => close(false)}>
                {t('cancel')}
              </button>
              <button className={`btn ${pending.danger ? 'btn-danger solid' : 'btn-primary'}`} disabled={blocked} onClick={() => close(true)} data-testid="confirm-ok">
                {pending.confirmText ?? t('confirm')}
              </button>
            </>
          }
        >
          <div className="stack">
            <div>{pending.body ?? t('areYouSure')}</div>
            {pending.typeToConfirm && (
              <div className="field">
                <label>{t('typeToConfirm', pending.typeToConfirm)}</label>
                <input className="input mono" value={typed} onChange={(e) => setTyped(e.target.value)} autoFocus />
              </div>
            )}
          </div>
        </Modal>
      )}
    </UiContext.Provider>
  );
}

export function useUi(): UiCtx {
  const v = useContext(UiContext);
  if (!v) throw new Error('UiProvider missing');
  return v;
}

// ---- Data loading -------------------------------------------------------------

export interface AsyncState<T> {
  data: T | undefined;
  error: unknown;
  loading: boolean;
  reload: () => Promise<void>;
  setData: (d: T) => void;
}

/** Loads data on mount / when deps change; optional polling interval in ms. */
export function useAsync<T>(fn: () => Promise<T>, deps: unknown[] = [], pollMs?: number): AsyncState<T> {
  const [data, setData] = useState<T>();
  const [error, setError] = useState<unknown>();
  const [loading, setLoading] = useState(true);
  const fnRef = useRef(fn);
  fnRef.current = fn;
  const alive = useRef(true);
  const reload = useCallback(async () => {
    setLoading(true);
    try {
      const d = await fnRef.current();
      if (alive.current) {
        setData(d);
        setError(undefined);
      }
    } catch (e) {
      if (alive.current) setError(e);
    } finally {
      if (alive.current) setLoading(false);
    }
  }, []);
  useEffect(() => {
    alive.current = true;
    void reload();
    let timer: number | undefined;
    if (pollMs) {
      timer = window.setInterval(() => {
        if (document.visibilityState === 'visible') {
          fnRef.current().then(
            (d) => alive.current && (setData(d), setError(undefined)),
            () => undefined,
          );
        }
      }, pollMs);
    }
    return () => {
      alive.current = false;
      if (timer) window.clearInterval(timer);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps);
  return { data, error, loading, reload, setData };
}

/** Wraps an async action with busy state + error toast. */
export function useAction() {
  const { toastError } = useUi();
  const [busy, setBusy] = useState<string | null>(null);
  const run = useCallback(
    async <T,>(id: string, fn: () => Promise<T>): Promise<T | undefined> => {
      setBusy(id);
      try {
        return await fn();
      } catch (e) {
        toastError(e);
        return undefined;
      } finally {
        setBusy(null);
      }
    },
    [toastError],
  );
  return { busy, run };
}
