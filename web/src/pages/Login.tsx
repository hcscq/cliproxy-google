import { useState, type FormEvent } from 'react';
import { useAuth, useTheme } from '../app-context';
import { ApiError } from '../api/client';
import { useI18n } from '../i18n';
import { Spinner } from '../components/ui';
import { IconMoon, IconSun } from '../components/icons';

export function LoginPage() {
  const { t, tr, lang, setLang } = useI18n();
  const { login, logoutReason } = useAuth();
  const [theme, setTheme] = useTheme();
  const [key, setKey] = useState('');
  const [remember, setRemember] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | undefined>(logoutReason);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!key.trim()) return;
    setBusy(true);
    setError(undefined);
    try {
      await login(key.trim(), remember);
    } catch (err) {
      if (err instanceof ApiError) {
        if (err.status === 401) setError(tr('管理密钥无效', 'Invalid management key'));
        else if (err.status === 404) setError(tr('Management API 未启用（未设置管理密钥）', 'Management API is disabled (no management key configured)'));
        else setError(err.message);
      } else setError(t('networkError'));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="login-wrap">
      <form className="card login-card stack" onSubmit={submit}>
        <div className="row">
          <div className="brand-logo">G</div>
          <div className="spacer">
            <div className="brand-name">{t('appName')}</div>
            <div className="brand-sub">{t('appSub')}</div>
          </div>
          <button type="button" className="btn btn-sm btn-ghost" onClick={() => setTheme(theme === 'dark' ? 'light' : 'dark')}>
            {theme === 'dark' ? <IconSun /> : <IconMoon />}
          </button>
          <button type="button" className="btn btn-sm btn-ghost" onClick={() => setLang(lang === 'zh' ? 'en' : 'zh')}>
            {lang === 'zh' ? 'EN' : '中文'}
          </button>
        </div>
        <h1>{t('loginTitle')}</h1>
        <div className="field">
          <label htmlFor="mkey">{t('managementKey')}</label>
          <input id="mkey" className="input" type="password" autoComplete="current-password" autoFocus value={key} onChange={(e) => setKey(e.target.value)} data-testid="login-key" />
        </div>
        <label className="check small muted">
          <input type="checkbox" checked={remember} onChange={(e) => setRemember(e.target.checked)} />
          {t('remember')}
        </label>
        {error && <div className="alert err small">{error}</div>}
        <button className="btn btn-primary" type="submit" disabled={busy || !key.trim()} data-testid="login-submit">
          {busy && <Spinner />}
          {t('login')}
        </button>
        <p className="hint" style={{ margin: 0 }}>
          {t('loginHint')}
        </p>
      </form>
    </div>
  );
}
