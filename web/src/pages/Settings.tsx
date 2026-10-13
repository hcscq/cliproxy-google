import { useEffect, useMemo, useState } from 'react';
import { useAction, useApi, useAsync, useUi } from '../app-context';
import { useI18n } from '../i18n';
import { Badge, Card, ErrorBox, Loading, Spinner, Switch } from '../components/ui';
import { fmtBytes } from '../api/client';
import { fmtNum, fmtTime } from '../format';

type FieldType = 'bool' | 'number' | 'text' | 'select' | 'list';
interface FieldDef {
  path: string;
  type: FieldType;
  zh: string;
  en: string;
  hintZh?: string;
  hintEn?: string;
  options?: string[];
  danger?: boolean;
  /** Value shown when the key is absent from the config (server-side default). */
  def?: unknown;
}

const SECTIONS: { zh: string; en: string; fields: FieldDef[] }[] = [
  {
    zh: '路由',
    en: 'Routing',
    fields: [
      { path: 'routing.strategy', type: 'select', options: ['round-robin', 'weighted-round-robin', 'fill-first'], zh: '凭据选择策略', en: 'Credential strategy', hintZh: '加权轮询使用每个 Key 的权重；fill-first 优先用满一个凭据。', hintEn: 'Weighted uses per-key weights; fill-first drains one credential first.' },
      { path: 'routing.session-affinity', type: 'bool', zh: '会话粘性', en: 'Session affinity', hintZh: '同一会话尽量固定到同一凭据（状态保存在 PostgreSQL）。', hintEn: 'Pin a session to one credential (state in PostgreSQL).' },
      { path: 'routing.session-affinity-ttl', type: 'text', zh: '会话粘性时长', en: 'Session affinity TTL', hintZh: '例如 1h、30m', hintEn: 'e.g. 1h, 30m' },
      { path: 'routing.antigravity-catalog-refresh', type: 'text', zh: '模型目录刷新间隔', en: 'Model catalog refresh interval', hintZh: 'Home 主节点定时获取各 Antigravity 凭据上游模型目录的间隔（如 15m、30m、1h；最小 1m；填 off 关闭定时，仅保留手动与错误触发刷新）。留空 = 15m。每次刷新每个凭据 1 次上游请求，结果存数据库供所有副本使用。', hintEn: 'How often the Home leader re-fetches every Antigravity credential\'s upstream model catalog (e.g. 15m, 30m, 1h; min 1m; "off" disables the timer, manual and error-triggered refreshes still work). Empty = 15m. One upstream call per credential per refresh, stored in the database for all replicas.' },
      { path: 'routing.force-model-prefix', type: 'bool', zh: '强制模型前缀', en: 'Force model prefix', hintZh: '带前缀的凭据只响应 “前缀/模型”。', hintEn: 'Prefixed credentials only serve "prefix/model".' },
    ],
  },
  {
    zh: '重试与冷却',
    en: 'Retry & cooldown',
    fields: [
      { path: 'routing.retry.request-retry', type: 'number', zh: '额外重试轮数', en: 'Request retry rounds', hintZh: '403/408/429/5xx 时换凭据重试的额外轮数。', hintEn: 'Extra rounds on 403/408/429/5xx.' },
      { path: 'routing.retry.max-retry-credentials', type: 'number', zh: '每轮最多尝试凭据数', en: 'Max credentials per round', hintZh: '≤0 表示全部', hintEn: '≤0 means all' },
      { path: 'routing.retry.max-request-credentials', type: 'number', zh: '单请求最多切换凭据数', en: 'Max credentials per request', hintZh: '0=默认 5，<0 不限', hintEn: '0 = default 5, <0 unlimited' },
      { path: 'routing.retry.max-retry-duration', type: 'number', zh: '单请求切换时限(秒)', en: 'Max switching time (s)', hintZh: '0=默认 90，<0 不限', hintEn: '0 = default 90, <0 unlimited' },
      { path: 'routing.retry.max-retry-interval', type: 'number', zh: '最长等待冷却（秒）', en: 'Max cooldown wait (s)' },
      { path: 'routing.honor-max-tokens', type: 'bool', def: true, zh: '遵守客户端输出上限（max_tokens）', en: 'Honor client max_tokens', hintZh: '把 max_tokens / maxOutputTokens 传给 Antigravity/AI Studio 的 Gemini 模型。思考 token 也计入上限；固定思考预算 ≥ 上限时自动降为上限的一半。关闭则恢复旧行为（忽略上限）。', hintEn: 'Pass max_tokens / maxOutputTokens to Gemini models on Antigravity/AI Studio. Thinking tokens count toward the limit; a fixed thinking budget >= limit is lowered to half. Off = legacy (limit ignored).' },
      { path: 'routing.stream-thinking-emulation', type: 'bool', def: false, zh: '流式思考模拟（Gemini 3.x）', en: 'Stream thinking emulation (Gemini 3.x)', hintZh: '上游 Gemini 3.x 流式只在思考较长时才返回思考摘要，签名在正文之后到达。开启后，请求思考内容（includeThoughts / reasoning_effort / Claude thinking）的流式请求改走上游非流式接口，拿到完整结果后一次性按流式格式返回：思考文本和签名完整，但首字延迟等于整段生成时间。默认关闭。', hintEn: 'Upstream Gemini 3.x streams only include thought summaries for longer reasoning and send the signature after the text. When on, streaming requests that ask for thoughts use the non-stream upstream endpoint and are replayed as one SSE event: complete thinking and signature, but time-to-first-token equals full generation time. Default off.' },
      { path: 'routing.sticky-sessions.enabled', type: 'bool', def: true, zh: '粘性会话（凭据亲和）', en: 'Sticky sessions (credential affinity)', hintZh: '重复 / 续写的请求优先落到上次服务它的凭据，以命中该凭据上的上游隐式缓存。亲和键优先取客户端显式会话（X-Session-Id 头、OpenAI prompt_cache_key / user、Claude metadata.user_id、Gemini cachedContent），否则取“访问密钥 + 模型 + 稳定提示前缀”的哈希。绑定存于 PostgreSQL，所有 Home 副本共享；绑定凭据冷却 / 不可用 / 不在密钥分组内时按常规路由选择并改绑。', hintEn: 'Repeated / continued requests prefer the credential that served them before so upstream implicit caching on that credential hits. Key: explicit client session (X-Session-Id header, OpenAI prompt_cache_key / user, Claude metadata.user_id, Gemini cachedContent), else a hash of access key + model + stable prompt prefix. Bindings live in PostgreSQL (shared by all Home replicas); if the bound credential is cooling / unavailable / outside the key groups, normal routing picks another and re-pins.' },
      { path: 'routing.sticky-sessions.ttl', type: 'text', def: '45m', zh: '粘性绑定有效期', en: 'Sticky binding TTL', hintZh: '空闲多久后解除绑定（Go 时长，如 30m、1h；范围 1m–24h）', hintEn: 'Idle time before a binding expires (Go duration, 1m–24h)' },
      { path: 'routing.sticky-sessions.key-sources', type: 'list', zh: '亲和键来源（按优先级）', en: 'Affinity key sources (priority order)', hintZh: '可选：header, prompt-cache-key, openai-user, claude-metadata, gemini-cached-content, prefix；逗号分隔，留空 = 全部', hintEn: 'Any of: header, prompt-cache-key, openai-user, claude-metadata, gemini-cached-content, prefix; comma separated, empty = all' },
      { path: 'routing.sticky-sessions.prefix-messages', type: 'number', def: 1, zh: '前缀包含的消息数', en: 'Prefix messages', hintZh: '系统提示 + 前 N 条非系统消息参与哈希；默认 1，使续写对话保持同一个键', hintEn: 'System prompt + first N non-system messages are hashed; default 1 so a continued conversation keeps its key' },
      { path: 'routing.sticky-sessions.prefix-chars', type: 'number', def: 8000, zh: '前缀最大字符数', en: 'Prefix max chars', hintZh: '约 2k token；超出部分不参与哈希', hintEn: '≈2k tokens; the rest is ignored' },
      { path: 'routing.sticky-sessions.min-prefix-chars', type: 'number', def: 2000, zh: '前缀最小字符数', en: 'Prefix min chars', hintZh: '更短的提示不按前缀绑定，避免大量相同短提示全部压到同一凭据', hintEn: 'Shorter prompts get no prefix binding, so many identical short prompts are not all pinned to one credential' },
      { path: 'routing.retry.capacity-retry.enabled', type: 'bool', def: true, zh: '上游容量不足时重试', en: 'Retry on upstream capacity errors', hintZh: '503 UNAVAILABLE / MODEL_CAPACITY_EXHAUSTED 等容量错误：先换其他凭据，再退避后用同一模型重试。不会更换模型。', hintEn: '503 UNAVAILABLE / MODEL_CAPACITY_EXHAUSTED: try other credentials first, then back off and retry the same model. The model is never changed.' },
      { path: 'routing.retry.capacity-retry.max-attempts', type: 'number', def: 3, zh: '容量重试次数', en: 'Capacity retry attempts', hintZh: '首次失败后的最多重试轮数（默认 3，最大 10）', hintEn: 'Extra rounds after the first failure (default 3, max 10)' },
      { path: 'routing.retry.capacity-retry.base-delay', type: 'text', def: '1s', zh: '容量重试初始退避', en: 'Capacity retry base delay', hintZh: '每次翻倍并加 ±20% 抖动，例如 1s', hintEn: 'Doubles per attempt with ±20% jitter, e.g. 1s' },
      { path: 'routing.retry.capacity-retry.max-delay', type: 'text', def: '8s', zh: '容量重试最长退避', en: 'Capacity retry max delay', hintZh: '上游 RetryInfo 的 retryDelay 会被采纳，但不超过此值', hintEn: 'Upstream RetryInfo retryDelay is honoured up to this cap' },
      { path: 'routing.cooldown.disable-cooling', type: 'bool', zh: '全局禁用冷却', en: 'Disable cooldown globally' },
      { path: 'routing.cooldown.quota-short-cooldown-seconds', type: 'number', def: 60, zh: '无提示 429 短冷却（秒）', en: 'No-hint 429 short cooldown (s)', hintZh: '按模型 429 且上游未给出恢复时间时的固定冷却，不再指数增长；上游给出 Retry-After / retryDelay / quotaResetTimeStamp 时以上游为准。默认 60', hintEn: 'Fixed cooldown for per-model 429s without an upstream reset time (no exponential growth). Upstream Retry-After / retryDelay / quotaResetTimeStamp still win. Default 60' },
      { path: 'routing.cooldown.quota-probe-interval-seconds', type: 'number', def: 60, zh: '冷却探测间隔（秒）', en: 'Cooldown probe interval (s)', hintZh: 'Home 主节点按此间隔对每个冷却中的（凭据, 模型）发一个极小请求：200 立即解除冷却，429 重新开始短冷却。探测不计入密钥用量。默认 60', hintEn: 'The Home leader sends one tiny request per cooling (credential, model) at this interval: 200 clears the cooldown, 429 restarts the short cooldown. Probes are not counted as key usage. Default 60' },
      { path: 'routing.cooldown.quota-bucket-reprobe-seconds', type: 'number', def: 300, zh: '额度桶耗尽复查间隔（秒）', en: 'Exhausted quota bucket re-probe (s)', hintZh: '额度探测显示某个额度桶（如 Gemini 模型）剩余 0% 时，该凭据上这个桶的所有模型暂停调度（其他桶不受影响），并按此间隔重新探测额度；一旦探测到剩余额度 >0 立即恢复，最晚到上游给出的重置时间。默认 300，最小 30；填负数表示不按探测结果暂停', hintEn: 'When the quota probe shows a bucket (e.g. Gemini models) at 0%, all models of that bucket on that credential are skipped (other buckets unaffected) and its quota is re-probed at this interval; released as soon as a probe shows quota >0, at the latest at the upstream reset time. Default 300, min 30; negative disables' },
      { path: 'routing.cooldown.account-block-recheck-seconds', type: 'number', def: 21600, zh: '账号级 403 复查间隔（秒）', en: 'Account-level 403 recheck (s)', hintZh: '凭据返回账号级 403（如 “Verify your account” 需验证账号、账号被封）时自动停用并在面板显示原因；按此间隔发一个极小请求复查，成功即自动重新启用。也可完成验证后手动点「启用」。默认 21600（6 小时），最小 300；负数表示不自动恢复', hintEn: 'Credentials returning an account-level 403 (e.g. "Verify your account", suspended) are disabled with the reason shown; one tiny request rechecks at this interval and re-enables on success. Manual enable still works. Default 21600 (6h), min 300; negative = never auto re-enable' },
      { path: 'quota-exceeded.switch-project', type: 'bool', zh: '额度耗尽时切换项目', en: 'Switch project on quota exceeded' },
      { path: 'quota-exceeded.switch-preview-model', type: 'bool', zh: '额度耗尽时切换预览模型', en: 'Switch to preview model on quota exceeded' },
    ],
  },
  {
    zh: 'Antigravity',
    en: 'Antigravity',
    fields: [
      { path: 'oauth.providers.antigravity.antigravity-credits', type: 'bool', zh: '使用 Antigravity credits 作为 Claude 兜底', en: 'Use Antigravity credits as Claude fallback' },
      { path: 'oauth.providers.antigravity.signature-cache-enabled', type: 'bool', zh: '思考签名缓存校验', en: 'Thinking signature cache' },
      { path: 'oauth.providers.antigravity.connection-pool.enabled', type: 'bool', zh: '上游连接池', en: 'Upstream connection pool' },
    ],
  },
  {
    zh: '请求',
    en: 'Requests',
    fields: [
      { path: 'requests.proxy-url', type: 'text', zh: '全局出站代理', en: 'Global outbound proxy', hintZh: 'socks5:// 或 http://，留空为直连', hintEn: 'socks5:// or http://; empty = direct' },
      { path: 'requests.streaming.keepalive-seconds', type: 'number', zh: 'SSE 心跳间隔（秒）', en: 'SSE keepalive (s)' },
      { path: 'requests.streaming.bootstrap-retries', type: 'number', zh: '流式首字节前重试次数', en: 'Streaming bootstrap retries' },
      { path: 'requests.passthrough-headers', type: 'bool', zh: '透传上游响应头', en: 'Pass through upstream headers' },
    ],
  },
  {
    zh: '日志与管理',
    en: 'Logging & management',
    fields: [
      { path: 'observability.logs.request-log', type: 'bool', zh: '详细请求日志', en: 'Detailed request logs', hintZh: '会记录请求/响应内容，注意隐私与容量。', hintEn: 'Records request/response bodies; mind privacy and size.' },
      { path: 'observability.logs.error-body-max-bytes', type: 'number', def: 65536, zh: '错误响应体保存上限（字节）', en: 'Error body cap (bytes)', hintZh: '请求记录里保存的上游错误响应体最大字节数，超出部分截断并标注原始大小。默认 65536（64KB）。', hintEn: 'Max bytes of an upstream error body stored in the request log; larger bodies are truncated and marked. Default 65536 (64KB).' },
      { path: 'observability.logs.request-log-retention-days', type: 'number', def: 30, zh: '请求记录保留天数', en: 'Request log retention (days)', hintZh: '超过该天数的请求记录（含错误响应体）会被自动清理；PostgreSQL 下按天分区整块删除。0 = 默认 30 天，负数 = 永久保留。计费流水不受影响。', hintEn: 'Request-log rows older than this are removed automatically (whole daily partitions on PostgreSQL). 0 = default 30 days, negative = keep forever. Billing ledger is not affected.' },
      { path: 'observability.logs.debug', type: 'bool', zh: '调试日志', en: 'Debug logging' },
      { path: 'management.allow-remote', type: 'bool', danger: true, zh: '允许远程管理', en: 'Allow remote management', hintZh: '关闭后通过 Ingress 访问的本面板将立即失效（只能 kubectl port-forward 恢复）。', hintEn: 'Turning this off immediately locks this panel out via Ingress (recover with kubectl port-forward).' },
    ],
  },
];

function getPath(obj: unknown, path: string): unknown {
  return path.split('.').reduce<unknown>((o, k) => (o && typeof o === 'object' ? (o as Record<string, unknown>)[k] : undefined), obj);
}

function setPath(obj: Record<string, unknown>, path: string, value: unknown) {
  const keys = path.split('.');
  let cur = obj;
  keys.slice(0, -1).forEach((k) => {
    if (!cur[k] || typeof cur[k] !== 'object') cur[k] = {};
    cur = cur[k] as Record<string, unknown>;
  });
  cur[keys[keys.length - 1]] = value;
}

export function SettingsPage() {
  const { t, tr } = useI18n();
  const [tab, setTab] = useState<'form' | 'yaml'>('form');
  return (
    <div className="stack">
      <div className="page-head">
        <div>
          <h1>{t('settings')}</h1>
          <p>{tr('配置保存在 PostgreSQL，修改后所有 Home 副本和代理节点会自动重新加载。', 'Configuration lives in PostgreSQL; all Home replicas and proxies reload automatically.')}</p>
        </div>
      </div>
      <div className="tabs" style={{ marginBottom: 0 }}>
        <button className={tab === 'form' ? 'active' : ''} onClick={() => setTab('form')}>
          {tr('常用设置', 'Common settings')}
        </button>
        <button className={tab === 'yaml' ? 'active' : ''} onClick={() => setTab('yaml')}>
          {tr('完整配置 (YAML)', 'Full config (YAML)')}
        </button>
      </div>
      {tab === 'form' ? (
        <>
          <SettingsForm />
          <EgressPolicy />
          <RequestLogStorage />
        </>
      ) : (
        <YamlEditor />
      )}
    </div>
  );
}

function SettingsForm() {
  const api = useApi();
  const { t, tr } = useI18n();
  const { toast, confirm } = useUi();
  const { busy, run } = useAction();
  const cfg = useAsync(() => api.config(), []);
  const [values, setValues] = useState<Record<string, unknown>>({});
  const initial = useMemo(() => {
    const out: Record<string, unknown> = {};
    for (const s of SECTIONS)
      for (const f of s.fields) {
        const raw = getPath(cfg.data, f.path) ?? f.def;
        // list fields are edited as comma-separated text
        out[f.path] = f.type === 'list' ? (Array.isArray(raw) ? raw.join(', ') : (raw ?? '')) : raw;
      }
    return out;
  }, [cfg.data]);
  useEffect(() => setValues(initial), [initial]);

  const changed = Object.keys(values).filter((k) => JSON.stringify(values[k] ?? null) !== JSON.stringify(initial[k] ?? null));

  const save = async () => {
    const patch: Record<string, unknown> = {};
    const listPaths = new Set(SECTIONS.flatMap((s) => s.fields.filter((f) => f.type === 'list').map((f) => f.path)));
    for (const k of changed)
      setPath(patch, k, listPaths.has(k) ? String(values[k] ?? '').split(',').map((x) => x.trim()).filter(Boolean) : values[k]);
    if (changed.includes('management.allow-remote') && values['management.allow-remote'] === false) {
      const ok = await confirm({ title: tr('关闭远程管理？', 'Disable remote management?'), body: tr('保存后本面板将无法通过 Ingress 访问。', 'This panel will be unreachable via Ingress after saving.'), danger: true, typeToConfirm: 'LOCK' });
      if (!ok) return;
    }
    await run('save', async () => {
      await api.configPatch(patch);
      toast(t('saved'));
      await cfg.reload();
    });
  };

  if (cfg.error) return <ErrorBox error={cfg.error} onRetry={cfg.reload} />;
  if (!cfg.data) return <Loading />;

  return (
    <div className="stack">
      {SECTIONS.map((s) => (
        <Card key={s.en} title={tr(s.zh, s.en)}>
          <div className="form-grid">
            {s.fields.map((f) => {
              const v = values[f.path];
              const label = tr(f.zh, f.en);
              const hint = f.hintZh ? tr(f.hintZh, f.hintEn ?? '') : undefined;
              return (
                <div className="field" key={f.path}>
                  {f.type === 'bool' ? (
                    <Switch checked={!!v} onChange={(nv) => setValues({ ...values, [f.path]: nv })} label={<span style={f.danger ? { color: 'var(--err)' } : undefined}>{label}</span>} />
                  ) : (
                    <>
                      <label>{label}</label>
                      {f.type === 'select' ? (
                        <select className="select" value={(v as string) ?? f.options![0]} onChange={(e) => setValues({ ...values, [f.path]: e.target.value })}>
                          {f.options!.map((o) => (
                            <option key={o}>{o}</option>
                          ))}
                        </select>
                      ) : (
                        <input
                          className="input"
                          type={f.type === 'number' ? 'number' : 'text'}
                          value={v === undefined || v === null ? '' : String(v)}
                          onChange={(e) => setValues({ ...values, [f.path]: f.type === 'number' ? (e.target.value === '' ? undefined : Number(e.target.value)) : e.target.value })}
                        />
                      )}
                    </>
                  )}
                  {hint && <span className="hint hint-clamp" title={hint} tabIndex={0} onClick={(e) => e.currentTarget.classList.toggle("open")}>{hint}</span>}
                  <span className="hint mono" style={{ opacity: 0.7 }}>
                    {f.path}
                  </span>
                </div>
              );
            })}
          </div>
        </Card>
      ))}
      <div className="row" style={{ position: 'sticky', bottom: 0, padding: '12px 0', background: 'var(--bg)' }}>
        <button className="btn btn-primary" disabled={!changed.length || busy === 'save'} onClick={save}>
          {busy === 'save' && <Spinner />}
          {t('save')} {changed.length > 0 && `(${changed.length})`}
        </button>
        <button className="btn" disabled={!changed.length} onClick={() => setValues(initial)}>
          {tr('放弃修改', 'Discard')}
        </button>
      </div>
    </div>
  );
}

function YamlEditor() {
  const api = useApi();
  const { t, tr } = useI18n();
  const { toast, confirm } = useUi();
  const { busy, run } = useAction();
  const yaml = useAsync(() => api.configYAML(), []);
  const [text, setText] = useState('');
  useEffect(() => {
    if (yaml.data !== undefined) setText(yaml.data);
  }, [yaml.data]);
  if (yaml.error) return <ErrorBox error={yaml.error} onRetry={yaml.reload} />;
  if (yaml.data === undefined) return <Loading />;
  const dirty = text !== yaml.data;
  return (
    <Card
      title={<span className="faint small">{tr('整份替换：未出现的提供方分组会被清空。修改前建议先复制备份。', 'Full replacement: provider groups missing from the document are cleared. Copy a backup first.')}</span>}
      extra={
        <>
          <button className="btn btn-sm" disabled={!dirty} onClick={() => setText(yaml.data ?? '')}>
            {tr('还原', 'Revert')}
          </button>
          <button
            className="btn btn-sm btn-primary"
            disabled={!dirty || busy === 'save'}
            onClick={async () => {
              if (!(await confirm({ title: tr('替换完整配置？', 'Replace the full configuration?'), danger: true }))) return;
              await run('save', async () => {
                await api.putConfigYAML(text);
                toast(t('saved'));
                await yaml.reload();
              });
            }}
          >
            {busy === 'save' && <Spinner />}
            {t('save')}
          </button>
        </>
      }
    >
      <textarea className="textarea" style={{ minHeight: 'calc(100vh - 300px)' }} spellCheck={false} value={text} onChange={(e) => setText(e.target.value)} />
    </Card>
  );
}

/** Request-log storage: partitions, size and retention (read-only status). */
function EgressPolicy() {
  const api = useApi();
  const { tr } = useI18n();
  const { toast, toastError } = useUi();
  const st = useAsync(() => api.egressSettings(), []);
  const [saving, setSaving] = useState(false);
  if (st.error) return <ErrorBox error={st.error} onRetry={st.reload} />;
  if (!st.data) return <Loading />;
  return (
    <Card title={tr('出口分配', 'Egress assignment')}>
      <label
        style={{ display: 'inline-flex', gap: 6, alignItems: 'center' }}
        title={tr('自动分配（新凭据、自动均衡）的默认值。关闭时只用副 IP，没有健康副 IP 时失败，不会改用主 IP。已有凭据不受影响。', 'Default for automatic assignment. Off: secondary IPs only; fails instead of using a primary IP. Existing credentials are not moved.')}
      >
        <input
          type="checkbox"
          checked={st.data.allow_primary}
          disabled={saving}
          data-testid="settings-allow-primary"
          onChange={async (e) => {
            setSaving(true);
            try {
              await api.saveEgressSettings({ allow_primary: e.target.checked, max_per_egress: st.data?.max_per_egress ?? 0 });
              toast(tr('已保存', 'Saved'));
              await st.reload();
            } catch (err) {
              toastError(err);
            } finally {
              setSaving(false);
            }
          }}
        />
        {tr('默认允许分配到主 IP', 'Allow primary IP by default')}
      </label>
      <label
        style={{ display: 'inline-flex', gap: 6, alignItems: 'center', marginLeft: 24 }}
        title={tr('新分配（导入/添加/自动均衡）时每个出口 IP 最多绑定的凭据数；满了导入会被拒绝。0 = 不限。已有凭据不会被移动。', 'Max credentials per egress IP for new assignments; imports are refused when all are full. 0 = unlimited. Existing pins are not moved.')}
      >
        {tr('每出口上限', 'Max per egress')}
        <input
          type="number"
          min={0}
          style={{ width: 80 }}
          defaultValue={st.data.max_per_egress ?? 0}
          disabled={saving}
          data-testid="settings-max-per-egress"
          onBlur={async (e) => {
            const v = Math.max(0, Math.floor(Number(e.target.value) || 0));
            if (v === (st.data?.max_per_egress ?? 0)) return;
            setSaving(true);
            try {
              await api.saveEgressSettings({ allow_primary: st.data!.allow_primary, max_per_egress: v });
              toast(tr('已保存', 'Saved'));
              await st.reload();
            } catch (err) {
              toastError(err);
            } finally {
              setSaving(false);
            }
          }}
        />
      </label>
    </Card>
  );
}

function RequestLogStorage() {
  const api = useApi();
  const { tr } = useI18n();
  const st = useAsync(() => api.usageRetention(), []);
  const [showAll, setShowAll] = useState(false);
  if (st.error) return <ErrorBox error={st.error} onRetry={st.reload} />;
  if (!st.data) return <Loading />;
  const s = st.data.status;
  const parts = s.partitions ?? [];
  const visible = showAll ? parts : parts.slice(0, 8);
  const retention = s.retention_days > 0 ? tr(`${s.retention_days} 天`, `${s.retention_days} days`) : tr('永久保留', 'Keep forever');
  return (
    <Card
      title={tr('请求记录存储', 'Request log storage')}
      extra={
        <button className="btn ghost sm" onClick={st.reload} data-testid="retention-reload">
          {tr('刷新', 'Refresh')}
        </button>
      }
    >
      <div className="kv-grid" data-testid="retention-status">
        <div>
          <span className="muted">{tr('存储方式', 'Storage')}</span>
          <div>
            {s.partitioned ? <Badge kind="ok">{tr('按天分区', 'Daily partitions')}</Badge> : <Badge>{tr('单表（批量删除）', 'Single table (batched delete)')}</Badge>}{' '}
            <span className="muted">{s.backend}</span>
          </div>
        </div>
        <div>
          <span className="muted">{tr('保留期', 'Retention')}</span>
          <div>{retention}</div>
        </div>
        <div>
          <span className="muted">{tr('记录数 / 占用', 'Rows / size')}</span>
          <div>
            {fmtNum(s.total_rows)} / {fmtBytes(s.total_bytes)}
          </div>
        </div>
        <div>
          <span className="muted">{tr('最早记录', 'Oldest row')}</span>
          <div>{s.oldest_timestamp ? fmtTime(s.oldest_timestamp) : '-'}</div>
        </div>
        <div>
          <span className="muted">{tr('上次清理', 'Last maintenance')}</span>
          <div>
            {s.last_maintenance ? fmtTime(s.last_maintenance) : tr('本副本尚未执行', 'Not yet on this replica')}
            {s.last_error ? (
              <>
                {' '}
                <Badge kind="err">{tr('失败', 'failed')}</Badge>
              </>
            ) : null}
          </div>
          {(s.last_dropped?.length ?? 0) > 0 || (s.last_deleted ?? 0) > 0 ? (
            <div className="muted sm">
              {tr('删除分区', 'Dropped')}: {s.last_dropped?.join(', ') || '-'} · {tr('删除行', 'Deleted rows')}: {fmtNum(s.last_deleted ?? 0)}
            </div>
          ) : null}
          {s.last_error ? <div className="muted sm wrap">{s.last_error}</div> : null}
        </div>
      </div>
      {s.legacy_table ? (
        <p className="muted sm" data-testid="retention-legacy">
          {tr(
            `迁移前的旧表 usage_legacy（${fmtNum(s.legacy_rows ?? 0)} 行）已保留作为备份，确认无误后可手动删除：DROP TABLE usage_legacy;`,
            `The pre-migration table usage_legacy (${fmtNum(s.legacy_rows ?? 0)} rows) is kept as a backup; drop it manually once verified: DROP TABLE usage_legacy;`,
          )}
        </p>
      ) : null}
      {parts.length > 0 ? (
        <div className="table-wrap">
          <table className="table compact" data-testid="retention-partitions">
            <thead>
              <tr>
                <th>{tr('分区', 'Partition')}</th>
                <th>{tr('日期 (UTC)', 'Day (UTC)')}</th>
                <th className="num">{tr('行数', 'Rows')}</th>
                <th className="num">{tr('大小', 'Size')}</th>
              </tr>
            </thead>
            <tbody>
              {visible.map((p) => (
                <tr key={p.name}>
                  <td className="mono">{p.name}</td>
                  <td>{p.is_default ? tr('默认分区（范围外数据）', 'Default (out-of-range rows)') : p.from ? p.from.slice(0, 10) : '-'}</td>
                  <td className="num">{fmtNum(p.rows)}</td>
                  <td className="num">{fmtBytes(p.bytes)}</td>
                </tr>
              ))}
            </tbody>
          </table>
          {parts.length > 8 ? (
            <button className="btn ghost sm" onClick={() => setShowAll(!showAll)}>
              {showAll ? tr('收起', 'Show less') : tr(`查看全部 ${parts.length} 个分区`, `Show all ${parts.length} partitions`)}
            </button>
          ) : null}
        </div>
      ) : null}
    </Card>
  );
}
