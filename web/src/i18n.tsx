import { createContext, useCallback, useContext, useMemo, useState, type ReactNode } from 'react';

export type Lang = 'zh' | 'en';

// Chinese is the primary UI language; English strings are provided for every key.
const dict = {
  appName: ['CLIProxy Google', 'CLIProxy Google'],
  appSub: ['管理面板', 'Control Panel'],
  login: ['登录', 'Sign in'],
  loginTitle: ['使用管理密钥登录', 'Sign in with the management key'],
  loginHint: ['密钥只保存在当前浏览器，并且只以请求头形式发送给 Home。连续输错 5 次会被封禁 30 分钟。', 'The key stays in this browser and is only sent to Home as a request header. 5 wrong attempts ban your IP for 30 minutes.'],
  managementKey: ['管理密钥', 'Management key'],
  remember: ['在此设备上记住（localStorage）', 'Remember on this device (localStorage)'],
  logout: ['退出登录', 'Sign out'],
  dashboard: ['总览', 'Overview'],
  credentials: ['凭据', 'Credentials'],
  providerKeys: ['Gemini / Vertex 密钥', 'Gemini / Vertex keys'],
  models: ['模型', 'Models'],
  accessKeys: ['访问密钥', 'Access keys'],
  groups: ['分组与权限', 'Groups'],
  usage: ['用量与请求', 'Usage & requests'],
  logs: ['日志', 'Logs'],
  nodes: ['节点', 'Nodes'],
  settings: ['设置', 'Settings'],
  refresh: ['刷新', 'Refresh'],
  save: ['保存', 'Save'],
  saved: ['已保存', 'Saved'],
  cancel: ['取消', 'Cancel'],
  confirm: ['确认', 'Confirm'],
  delete: ['删除', 'Delete'],
  edit: ['编辑', 'Edit'],
  add: ['添加', 'Add'],
  create: ['创建', 'Create'],
  close: ['关闭', 'Close'],
  copy: ['复制', 'Copy'],
  copied: ['已复制', 'Copied'],
  search: ['搜索', 'Search'],
  loading: ['加载中…', 'Loading…'],
  empty: ['暂无数据', 'Nothing here yet'],
  actions: ['操作', 'Actions'],
  status: ['状态', 'Status'],
  enabled: ['启用', 'Enabled'],
  disabled: ['已禁用', 'Disabled'],
  enable: ['启用', 'Enable'],
  disable: ['禁用', 'Disable'],
  healthy: ['健康', 'Healthy'],
  unhealthy: ['异常', 'Unhealthy'],
  provider: ['提供方', 'Provider'],
  name: ['名称', 'Name'],
  priority: ['优先级', 'Priority'],
  weight: ['权重', 'Weight'],
  prefix: ['模型前缀', 'Model prefix'],
  proxyUrl: ['代理地址', 'Proxy URL'],
  note: ['备注', 'Note'],
  updated: ['更新时间', 'Updated'],
  created: ['创建时间', 'Created'],
  details: ['详情', 'Details'],
  dangerZone: ['危险操作', 'Danger zone'],
  areYouSure: ['确定要继续吗？', 'Are you sure?'],
  typeToConfirm: ['请输入 {0} 以确认', 'Type {0} to confirm'],
  leader: ['主节点', 'Leader'],
  follower: ['从节点', 'Follower'],
  homeReplicas: ['Home 副本', 'Home replicas'],
  proxyNodes: ['代理节点', 'Proxy nodes'],
  requests24h: ['24 小时请求', 'Requests (24h)'],
  successRate: ['成功率', 'Success rate'],
  tokens: ['Tokens', 'Tokens'],
  latency: ['延迟', 'Latency'],
  quota: ['额度', 'Quota'],
  remaining: ['剩余', 'Remaining'],
  resetsAt: ['重置于', 'Resets'],
  lastSeen: ['最后心跳', 'Last seen'],
  since: ['启动于', 'Started'],
  addCredential: ['添加凭据', 'Add credential'],
  antigravityLogin: ['Antigravity 登录', 'Antigravity login'],
  uploadJson: ['上传 JSON', 'Upload JSON'],
  pasteJson: ['粘贴 JSON', 'Paste JSON'],
  vertexSa: ['Vertex 服务账号', 'Vertex service account'],
  refreshToken: ['刷新令牌', 'Refresh token'],
  fetchQuota: ['查询额度', 'Fetch quota'],
  resetCooldown: ['清除冷却', 'Reset cooldown'],
  viewModels: ['查看模型', 'View models'],
  download: ['下载', 'Download'],
  apply: ['应用', 'Apply'],
  theme: ['主题', 'Theme'],
  language: ['语言', 'Language'],
  networkError: ['无法连接到 Home', 'Cannot reach Home'],
  sessionExpired: ['登录已失效，请重新登录', 'Session expired, please sign in again'],
} satisfies Record<string, [string, string]>;

export type TKey = keyof typeof dict;

interface I18nCtx {
  lang: Lang;
  setLang: (l: Lang) => void;
  t: (k: TKey, ...args: (string | number)[]) => string;
  /** Inline bilingual string for one-off texts: tr('中文', 'English'). */
  tr: (zh: string, en: string) => string;
}

const Ctx = createContext<I18nCtx | null>(null);

export function I18nProvider({ children }: { children: ReactNode }) {
  const [lang, setLangState] = useState<Lang>(() => (localStorage.getItem('panel.lang') === 'en' ? 'en' : 'zh'));
  const setLang = useCallback((l: Lang) => {
    localStorage.setItem('panel.lang', l);
    document.documentElement.lang = l === 'zh' ? 'zh-CN' : 'en';
    setLangState(l);
  }, []);
  const value = useMemo<I18nCtx>(() => {
    const idx = lang === 'zh' ? 0 : 1;
    return {
      lang,
      setLang,
      t: (k, ...args) => args.reduce<string>((s, a, i) => s.replace(`{${i}}`, String(a)), dict[k][idx]),
      tr: (zh, en) => (idx === 0 ? zh : en),
    };
  }, [lang, setLang]);
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

export function useI18n(): I18nCtx {
  const v = useContext(Ctx);
  if (!v) throw new Error('I18nProvider missing');
  return v;
}
