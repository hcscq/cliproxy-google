import { useEffect, useState } from 'react';
import { HashRouter, NavLink, Navigate, Route, Routes, useLocation } from 'react-router-dom';
import { AuthProvider, UiProvider, useAuth, useTheme } from './app-context';
import { I18nProvider, useI18n, type TKey } from './i18n';
import {
  IconChart, IconCube, IconGear, IconHome, IconKey, IconList, IconLogout, IconMenu, IconMoon, IconServer, IconShield, IconSun, IconTicket,
} from './components/icons';
import { LoginPage } from './pages/Login';
import { DashboardPage } from './pages/Dashboard';
import { CredentialsPage } from './pages/Credentials';
import { ProviderKeysPage } from './pages/ProviderKeys';
import { ModelsPage } from './pages/Models';
import { AccessKeysPage } from './pages/AccessKeys';
import { UsagePage } from './pages/Usage';
import { LogsPage } from './pages/Logs';
import { NodesPage } from './pages/Nodes';
import { SettingsPage } from './pages/Settings';

const NAV: { path: string; label: TKey; icon: typeof IconHome }[] = [
  { path: '/', label: 'dashboard', icon: IconHome },
  { path: '/credentials', label: 'credentials', icon: IconShield },
  { path: '/provider-keys', label: 'providerKeys', icon: IconKey },
  { path: '/models', label: 'models', icon: IconCube },
  { path: '/access-keys', label: 'accessKeys', icon: IconTicket },
  { path: '/usage', label: 'usage', icon: IconChart },
  { path: '/logs', label: 'logs', icon: IconList },
  { path: '/nodes', label: 'nodes', icon: IconServer },
  { path: '/settings', label: 'settings', icon: IconGear },
];

function Shell() {
  const { t, lang, setLang } = useI18n();
  const { logout } = useAuth();
  const [theme, setTheme] = useTheme();
  const [open, setOpen] = useState(false);
  const loc = useLocation();
  useEffect(() => setOpen(false), [loc.pathname]);
  const current = NAV.find((n) => (n.path === '/' ? loc.pathname === '/' : loc.pathname.startsWith(n.path)));
  useEffect(() => {
    document.title = `${current ? t(current.label) : ''} · ${t('appName')}`;
  }, [current, t]);

  return (
    <div className="shell">
      <aside className={`sidebar ${open ? 'open' : ''}`}>
        <div className="brand">
          <div className="brand-logo">G</div>
          <div>
            <div className="brand-name">{t('appName')}</div>
            <div className="brand-sub">{t('appSub')}</div>
          </div>
        </div>
        <nav className="nav">
          {NAV.map(({ path, label, icon: Icon }) => (
            <NavLink key={path} to={path} end={path === '/'} data-testid={`nav-${label}`}>
              <Icon />
              {t(label)}
            </NavLink>
          ))}
        </nav>
        <div className="sidebar-foot">
          <button className="btn btn-sm btn-ghost" onClick={() => setTheme(theme === 'dark' ? 'light' : 'dark')} title={t('theme')}>
            {theme === 'dark' ? <IconSun /> : <IconMoon />}
          </button>
          <button className="btn btn-sm btn-ghost" onClick={() => setLang(lang === 'zh' ? 'en' : 'zh')} title={t('language')}>
            {lang === 'zh' ? 'EN' : '中文'}
          </button>
          <span className="spacer" />
          <button className="btn btn-sm btn-ghost" onClick={() => logout()} data-testid="logout">
            <IconLogout />
            {t('logout')}
          </button>
        </div>
      </aside>
      <div className="main">
        <div className="topbar">
          <button className="btn btn-icon btn-ghost" onClick={() => setOpen(!open)} aria-label="menu">
            <IconMenu />
          </button>
          <strong>{current ? t(current.label) : t('appName')}</strong>
        </div>
        <main className="content">
          <Routes>
            <Route path="/" element={<DashboardPage />} />
            <Route path="/credentials" element={<CredentialsPage />} />
            <Route path="/provider-keys" element={<ProviderKeysPage />} />
            <Route path="/models" element={<ModelsPage />} />
            <Route path="/access-keys" element={<AccessKeysPage />} />
            <Route path="/usage" element={<UsagePage />} />
            <Route path="/logs" element={<LogsPage />} />
            <Route path="/nodes" element={<NodesPage />} />
            <Route path="/settings" element={<SettingsPage />} />
            <Route path="*" element={<Navigate to="/" replace />} />
          </Routes>
        </main>
      </div>
      {open && <div className="drawer-backdrop" style={{ zIndex: 25 }} onClick={() => setOpen(false)} />}
    </div>
  );
}

function Gate() {
  const { authed } = useAuth();
  useTheme();
  return authed ? <Shell /> : <LoginPage />;
}

export function App() {
  return (
    <I18nProvider>
      <UiProvider>
        <AuthProvider>
          <HashRouter>
            <Gate />
          </HashRouter>
        </AuthProvider>
      </UiProvider>
    </I18nProvider>
  );
}
