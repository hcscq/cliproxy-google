import { useEffect, useRef, useState } from 'react';
import { useApi, useUi } from '../app-context';
import { stateFromRedirect, type OAuthResult, type RTImportResult } from '../api/client';
import { useI18n } from '../i18n';
import { CopyButton, Modal, Spinner } from '../components/ui';
import { IconExternal, IconUpload } from '../components/icons';
import { AllowPrimaryToggle, EgressSelect } from '../components/EgressSelect';
import { healthCategoryLabel, healthSummaryText, importVerdictText } from '../components/HealthCheck';
import { downloadBlob } from '../format';
import type { HealthItem, HealthSummary, ImportJob } from '../api/client';

type Tab = 'oauth' | 'tokens' | 'upload' | 'paste' | 'vertex';

export function AddCredentialModal({ onClose, onAdded }: { onClose: () => void; onAdded: () => void }) {
  const { t } = useI18n();
  const [tab, setTab] = useState<Tab>('oauth');
  const [rtPrefill, setRtPrefill] = useState('');
  const [egress, setEgress] = useState('auto');
  const [allowPrimary, setAllowPrimary] = useState<boolean | undefined>(undefined);
  const [reserve, setReserve] = useState(false);
  const { tr } = useI18n();
  const tabs: [Tab, string][] = [
    ['oauth', t('antigravityLogin')],
    ['tokens', 'Refresh Token 批量导入'],
    ['upload', t('uploadJson')],
    ['paste', t('pasteJson')],
    ['vertex', t('vertexSa')],
  ];
  return (
    <Modal title={t('addCredential')} onClose={onClose} wide>
      <div className="tabs">
        {tabs.map(([k, label]) => (
          <button key={k} className={tab === k ? 'active' : ''} onClick={() => setTab(k)} data-testid={`add-tab-${k}`}>
            {label}
          </button>
        ))}
      </div>
      {tab !== 'vertex' && (
        <div className="field" style={{ maxWidth: 420, marginBottom: 12 }}>
          <label>{tr('出口', 'Egress')}</label>
          <EgressSelect value={egress} onChange={setEgress} testId="add-egress" />
          {egress === 'auto' && <AllowPrimaryToggle value={allowPrimary} onChange={setAllowPrimary} testId="add-allow-primary" />}
        </div>
      )}
      {tab !== 'vertex' && (
        <label className="row gap-sm small" style={{ marginBottom: 8 }} title={tr('新建的凭据进入储备：不参与调度，仍保活刷新；可用数不足时自动启用', 'New credentials go to reserve: not scheduled, kept alive, auto-promoted on shortfall')}>
          <input type="checkbox" checked={reserve} onChange={(e) => setReserve(e.target.checked)} data-testid="add-reserve" />
          {tr('设为储备', 'Import as reserve')}
        </label>
      )}
      {tab === 'oauth' && <OAuthFlow allowPrimary={allowPrimary} reserve={reserve} onDone={onAdded} egress={egress} />}
      {tab === 'tokens' && <RefreshTokenImport allowPrimary={allowPrimary} reserve={reserve} key={rtPrefill} initialText={rtPrefill} onDone={onAdded} egress={egress} />}
      {tab === 'upload' && <UploadFiles allowPrimary={allowPrimary} reserve={reserve} onDone={onAdded} egress={egress} />}
      {tab === 'paste' && <PasteJson allowPrimary={allowPrimary} reserve={reserve} onDone={onAdded} egress={egress} onUseTokens={(t) => { setRtPrefill(t); setTab('tokens'); }} />}
      {tab === 'vertex' && <VertexImport onDone={onAdded} />}
    </Modal>
  );
}

function OAuthFlow({ onDone, egress, allowPrimary, reserve }: { onDone: () => void; egress: string; allowPrimary?: boolean; reserve?: boolean }) {
  const api = useApi();
  const { toast, toastError } = useUi();
  const { tr } = useI18n();
  const [session, setSession] = useState<{ url: string; state: string } | null>(null);
  const [starting, setStarting] = useState(false);
  const [redirect, setRedirect] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [phase, setPhase] = useState<'idle' | 'waiting' | 'exchanging' | 'done' | 'error'>('idle');
  const [error, setError] = useState<string>();
  const pollRef = useRef<number>();
  const doneRef = useRef(false);

  const stopPoll = () => window.clearInterval(pollRef.current);
  useEffect(() => {
    doneRef.current = false;
    return () => stopPoll();
  }, []);

  const finish = (res: OAuthResult) => {
    if (doneRef.current) return;
    doneRef.current = true;
    stopPoll();
    setPhase('done');
    if (res.updated_existing) {
      const n = res.seq ? ` #${res.seq}` : '';
      const re = res.reenabled ? tr('，已解除停用', '; re-enabled') : '';
      toast(tr(`已更新现有凭据${n}（${res.email ?? ''}）的令牌${re}，未新增重复凭据`, `Updated existing credential${n} (${res.email ?? ''}) tokens${re}; no duplicate added`));
    } else {
      toast(tr('Antigravity 账号已添加', 'Antigravity account added'));
    }
    onDone();
  };

  const startPoll = (state: string) => {
    stopPoll();
    pollRef.current = window.setInterval(async () => {
      try {
        const st = await api.oauthStatus(state);
        if (st.status === 'ok' && !doneRef.current) {
          finish(st);
        } else if (st.status === 'error') {
          stopPoll();
          setPhase('error');
          setError(st.error || 'error');
        }
      } catch {
        /* keep polling; transient */
      }
    }, 2000);
  };

  const start = async () => {
    setStarting(true);
    setError(undefined);
    try {
      const res = await api.oauthStart('antigravity', egress, allowPrimary, reserve);
      setSession({ url: res.url, state: res.state });
      setPhase('waiting');
      startPoll(res.state);
    } catch (e) {
      toastError(e);
    } finally {
      setStarting(false);
    }
  };

  const submit = async () => {
    if (!session) return;
    const st = stateFromRedirect(redirect);
    if (st && st !== session.state) {
      setError(tr('回调地址中的 state 与当前登录会话不一致，请确认粘贴的是本次登录的地址。', 'The state in the URL does not match this login session.'));
      return;
    }
    setSubmitting(true);
    setError(undefined);
    try {
      const res = await api.oauthSubmitCallback(redirect.trim(), session.state, 'antigravity');
      if (res.already_completed) finish(res);
      else setPhase('exchanging');
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setSubmitting(false);
    }
  };

  const cancel = async () => {
    stopPoll();
    if (session && phase !== 'done') await api.oauthCancel(session.state).catch(() => undefined);
    setSession(null);
    setRedirect('');
    setPhase('idle');
    setError(undefined);
  };

  if (!session) {
    return (
      <div className="stack">
        <p className="muted" style={{ margin: 0 }}>
          {tr(
            '使用 Google 账号登录 Antigravity。登录完成后，浏览器会跳转到一个 localhost 地址（页面打不开是正常的），把地址栏里的完整 URL 复制回来粘贴即可。',
            'Sign in to Antigravity with a Google account. After consent the browser is redirected to a localhost URL (it will fail to load — that is expected); copy the full URL from the address bar and paste it back here.',
          )}
        </p>
        <div>
          <button className="btn btn-primary" onClick={start} disabled={starting} data-testid="oauth-start">
            {starting && <Spinner />}
            {tr('生成登录链接', 'Generate login link')}
          </button>
        </div>
      </div>
    );
  }

  return (
    <div className="stack">
      <div className="field">
        <label>{tr('① 在新标签页打开登录链接并完成授权', '① Open the login link in a new tab and approve')}</label>
        <div className="row">
          <input className="input mono input-sm spacer" readOnly value={session.url} data-testid="oauth-url" style={{ minWidth: 0, flex: 1 }} />
          <a className="btn btn-primary btn-sm" href={session.url} target="_blank" rel="noreferrer noopener">
            <IconExternal />
            {tr('打开', 'Open')}
          </a>
          <CopyButton text={session.url} small />
        </div>
      </div>
      <div className="field">
        <label>{tr('② 粘贴跳转后的完整地址（http://localhost:51121/oauth-callback?code=…&state=…）', '② Paste the full redirected URL (http://localhost:51121/oauth-callback?code=…&state=…)')}</label>
        <textarea className="textarea" style={{ minHeight: 70 }} value={redirect} onChange={(e) => setRedirect(e.target.value)} placeholder="http://localhost:51121/oauth-callback?state=...&code=..." />
      </div>
      {error && <div className="alert err small">{error}</div>}
      {phase === 'exchanging' && (
        <div className="alert small row">
          <Spinner /> {tr('已提交，正在交换令牌并保存凭据…', 'Submitted, exchanging the code and saving the credential…')}
        </div>
      )}
      {phase === 'done' && <div className="alert small">{tr('✅ 登录成功，凭据已保存。', '✅ Signed in, credential saved.')}</div>}
      <div className="row">
        <button className="btn btn-primary" disabled={!redirect.trim() || submitting || phase === 'done'} onClick={submit}>
          {submitting && <Spinner />}
          {tr('提交回调地址', 'Submit redirect URL')}
        </button>
        <button className="btn" onClick={cancel}>
          {phase === 'done' ? tr('再添加一个', 'Add another') : tr('取消本次登录', 'Cancel this login')}
        </button>
        <span className="spacer" />
        {phase === 'waiting' && (
          <span className="faint small row">
            <Spinner /> {tr('等待授权…', 'Waiting for authorization…')}
          </span>
        )}
      </div>
    </div>
  );
}

function UploadFiles({ onDone, egress, allowPrimary, reserve }: { onDone: () => void; egress: string; allowPrimary?: boolean; reserve?: boolean }) {
  const api = useApi();
  const { toast, toastError } = useUi();
  const { tr } = useI18n();
  const [files, setFiles] = useState<File[]>([]);
  const [busy, setBusy] = useState(false);
  const [drag, setDrag] = useState(false);
  const [results, setResults] = useState<RTImportResult[] | null>(null);
  const [healthBatch, setHealthBatch] = useState<string | null>(null);
  const input = useRef<HTMLInputElement>(null);
  const add = (list: FileList | null) => {
    if (!list) return;
    setFiles((prev) => [...prev, ...Array.from(list).filter((f) => f.name.toLowerCase().endsWith('.json'))]);
  };
  const upload = async () => {
    setBusy(true);
    setResults(null);
    setHealthBatch(null);
    try {
      const res = await api.uploadCredentialFiles(files, egress, allowPrimary, { reserve, health_check: true, batch_id: newBatchId('up') });
      const rs = res.results ?? [];
      setResults(rs);
      setHealthBatch(res.health_batch_id ?? null);
      toast(importSummaryText(rs, tr), rs.some((r) => r.status === 'failed') ? 'err' : 'ok');
      setFiles([]);
      onDone();
    } catch (e) {
      toastError(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="stack">
      <p className="muted" style={{ margin: 0 }}>
        {tr('上传凭据 JSON，可多选；文件可为单个对象或对象数组（完整凭据或 {email, refresh_token}）。同邮箱/同 token 只更新令牌。', 'Upload CLIProxyAPI-format credential JSON files (e.g. antigravity-xxx.json). Multiple files allowed.')}
      </p>
      <div
        className="card center"
        style={{ borderStyle: 'dashed', cursor: 'pointer', background: drag ? 'var(--primary-soft)' : undefined, padding: 28 }}
        onClick={() => input.current?.click()}
        onDragOver={(e) => {
          e.preventDefault();
          setDrag(true);
        }}
        onDragLeave={() => setDrag(false)}
        onDrop={(e) => {
          e.preventDefault();
          setDrag(false);
          add(e.dataTransfer.files);
        }}
      >
        <div className="stack" style={{ alignItems: 'center', gap: 6 }}>
          <IconUpload width={22} height={22} />
          <span>{tr('点击选择或拖拽 .json 文件到这里', 'Click or drop .json files here')}</span>
        </div>
        <input ref={input} type="file" accept=".json,application/json" multiple hidden onChange={(e) => add(e.target.files)} data-testid="upload-input" />
      </div>
      {files.length > 0 && (
        <div>
          {files.map((f, i) => (
            <div className="list-row" key={`${f.name}-${i}`}>
              <span className="mono">{f.name}</span>
              <button className="btn btn-sm btn-ghost" onClick={() => setFiles(files.filter((_, j) => j !== i))}>
                ✕
              </button>
            </div>
          ))}
        </div>
      )}
      <div>
        <button className="btn btn-primary" disabled={!files.length || busy} onClick={upload}>
          {busy && <Spinner />}
          {tr(`上传 ${files.length || ''} 个文件`, `Upload ${files.length || ''} file(s)`)}
        </button>
      </div>
      {healthBatch && <HealthBatchPanel batchId={healthBatch} />}
      {results && <ImportResultsTable results={results} />}
    </div>
  );
}

/** True when pasted non-JSON text looks like `email----refresh_token` lines or bare Google refresh tokens. */
export function looksLikeRefreshTokenText(text: string): boolean {
  const lines = text.replace(/[\u200b-\u200d\u2060\ufeff]/g, '').split(/\r\n?|\n/).map((l) => l.trim()).filter((l) => l && !l.startsWith('#'));
  if (!lines.length || lines[0].startsWith('{') || lines[0].startsWith('[')) return false;
  return lines.every((l) => /^(\S+@\S+?\s*[-\u2010-\u2015\u2212\uff0d]{3,}\s*)?1\/\/\S+/.test(l));
}

/** Parse pasted credential JSON: one object, an array of objects, or newline-delimited objects. */
export function parseCredentialJSON(text: string): { items: Record<string, unknown>[]; error?: string } {
  const t = text.replace(/[\u200b-\u200d\u2060\ufeff]/g, '').trim();
  if (!t) return { items: [] };
  const check = (v: unknown): Record<string, unknown>[] | string => {
    const arr = Array.isArray(v) ? v : [v];
    if (!arr.length) return '数组为空';
    for (let i = 0; i < arr.length; i++) {
      const x = arr[i];
      if (!x || typeof x !== 'object' || Array.isArray(x)) return Array.isArray(v) ? `第 ${i + 1} 项不是对象` : '必须是 JSON 对象或对象数组';
    }
    return arr as Record<string, unknown>[];
  };
  try {
    const r = check(JSON.parse(t));
    return typeof r === 'string' ? { items: [], error: r } : { items: r };
  } catch (e) {
    const lines = t.split(/\r\n?|\n/).map((l) => l.trim().replace(/,$/, '')).filter(Boolean);
    if (lines.length > 1 && lines.every((l) => l.startsWith('{'))) {
      const out: Record<string, unknown>[] = [];
      for (let i = 0; i < lines.length; i++) {
        try {
          const r = check(JSON.parse(lines[i]));
          if (typeof r === 'string') return { items: [], error: `第 ${i + 1} 行：${r}` };
          out.push(...r);
        } catch (e2) {
          return { items: [], error: `第 ${i + 1} 行：${(e2 as Error).message}` };
        }
      }
      return { items: out };
    }
    return { items: [], error: (e as Error).message };
  }
}

function newBatchId(prefix: string) {
  return `${prefix}-${new Date().toISOString().slice(0, 16).replace(/[-:T]/g, '')}-${Math.random().toString(36).slice(2, 8)}`;
}

function importSummaryText(rs: RTImportResult[], tr: (zh: string, en: string) => string) {
  const n = (s: string) => rs.filter((r) => r.status === s).length;
  return tr(`导入完成：新建 ${n('created')}，已存在更新 ${n('updated')}，失败 ${n('failed')}`, `Imported: ${n('created')} new, ${n('updated')} updated, ${n('failed')} failed`);
}

const PASTE_CHUNK = 200;

function PasteJson({ onDone, onUseTokens, egress, allowPrimary, reserve }: { onDone: () => void; onUseTokens?: (text: string) => void; egress: string; allowPrimary?: boolean; reserve?: boolean }) {
  const api = useApi();
  const { toast, toastError } = useUi();
  const { tr } = useI18n();
  const [text, setText] = useState('');
  const [busy, setBusy] = useState(false);
  const [progress, setProgress] = useState('');
  const [results, setResults] = useState<RTImportResult[] | null>(null);
  const [healthBatch, setHealthBatch] = useState<string | null>(null);
  const parsed = parseCredentialJSON(text);
  const parseError = text.trim() ? parsed.error : undefined;
  const items = parsed.items;
  const first = items[0];
  const ij = useImportJob();
  const notified = useRef<string | null>(null);
  useEffect(() => {
    if (ij.job && ij.job.status !== 'running' && notified.current !== ij.job.id) {
      notified.current = ij.job.id;
      if (ij.job.created || ij.job.updated) onDone();
    }
  }, [ij.job, onDone]);
  const asJob = items.length > 1 && items.every((x) => typeof x.refresh_token === 'string' && (x.type === undefined || x.type === 'antigravity'));
  const provider = items.length === 1 && first ? (typeof first.type === 'string' ? first.type : typeof first.provider === 'string' ? first.provider : undefined) : undefined;
  const submit = async () => {
    setBusy(true);
    setResults(null);
    setHealthBatch(null);
    if (asJob) {
      try {
        const res = await api.startImportJob({ items, egress_id: egress, allow_primary: allowPrimary, reserve: reserve || undefined, health_check: true, source: 'paste' });
        ij.setJobId(res.job.id);
        setText('');
        toast(tr(`已开始导入 ${res.job.total} 个，切换页签或关闭窗口不影响进度`, `Import of ${res.job.total} started`));
      } catch (e) {
        toastError(e);
      } finally {
        setBusy(false);
      }
      return;
    }
    const batchId = newBatchId('paste');
    const all: RTImportResult[] = [];
    let checking = false;
    try {
      for (let i = 0; i < items.length; i += PASTE_CHUNK) {
        if (items.length > PASTE_CHUNK) setProgress(`${i / PASTE_CHUNK + 1} / ${Math.ceil(items.length / PASTE_CHUNK)}`);
        const chunk = items.slice(i, i + PASTE_CHUNK);
        const body = JSON.stringify(items.length === 1 ? chunk[0] : chunk);
        const res = await api.uploadCredentialJSON(body, egress, allowPrimary, { reserve, health_check: true, batch_id: batchId });
        if (res.health_batch_id) checking = true;
        const rs = res.results ?? (res.result ? [res.result] : [{ index: 0, status: 'created', token_hint: '', reason: res.name } as RTImportResult]);
        rs.forEach((r, k) => all.push({ ...r, index: i + k }));
        setResults([...all]);
      }
      toast(importSummaryText(all, tr), all.some((r) => r.status === 'failed') ? 'err' : 'ok');
      setHealthBatch(checking ? batchId : null);
      if (all.some((r) => r.status === 'created' || r.status === 'updated')) {
        setText('');
        onDone();
      }
    } catch (e) {
      if (all.length) setResults([...all]);
      toastError(e);
    } finally {
      setBusy(false);
      setProgress('');
    }
  };
  return (
    <div className="stack">
      <textarea className="textarea" style={{ minHeight: 220 }} placeholder={'{"type": "antigravity", "refresh_token": "...", ...}\n或数组 [{"email":"...","refresh_token":"1//..."}, ...]，或每行一个对象'} value={text} onChange={(e) => setText(e.target.value)} spellCheck={false} data-testid="paste-json-text" />
      {parseError && looksLikeRefreshTokenText(text) ? (
        <div className="alert warn small" data-testid="paste-json-rt-hint">
          {tr('这看起来是 Refresh Token（email----token），请使用「Refresh Token 批量导入」页签。', 'This looks like refresh tokens (email----token); use the "Refresh Token batch import" tab.')}{' '}
          {onUseTokens && (
            <button className="btn btn-sm btn-primary" onClick={() => { const t = text; setText(''); onUseTokens(t); }} data-testid="paste-json-to-rt">
              {tr('转到批量导入', 'Open batch import')}
            </button>
          )}
        </div>
      ) : (
        parseError && <div className="alert err small" data-testid="paste-json-error">JSON: {parseError}</div>
      )}
      {!parseError && items.length > 0 && (
        <div className="faint small" data-testid="paste-json-count">
          {tr(`识别到 ${items.length} 个凭据`, `${items.length} credential(s)`)}
          {provider && <> · {tr('类型', 'type')}: <b>{provider}</b></>}
        </div>
      )}
      <div className="row">
        <button className="btn btn-primary" disabled={!items.length || !!parseError || busy || (asJob && ij.running)} onClick={submit} data-testid="paste-json-import">
          {busy && <Spinner />}
          {tr('导入', 'Import')}
        </button>
        {progress && <span className="faint small">{tr('批次', 'Batch')} {progress}</span>}
      </div>
      {healthBatch && <HealthBatchPanel batchId={healthBatch} />}
      {results && <ImportResultsTable results={results} />}
      {!results && <ImportJobPanel job={ij.job} results={ij.results} />}
      <RecentImports current={ij.jobId} onOpen={(id) => { setResults(null); ij.setJobId(id); }} />
    </div>
  );
}

function VertexImport({ onDone }: { onDone: () => void }) {
  const api = useApi();
  const { toast, toastError } = useUi();
  const { tr } = useI18n();
  const [file, setFile] = useState<File | null>(null);
  const [location, setLocation] = useState('us-central1');
  const [busy, setBusy] = useState(false);
  const submit = async () => {
    if (!file) return;
    setBusy(true);
    try {
      const res = await api.importVertex(file, location.trim() || undefined);
      toast(tr(`已导入 Vertex 服务账号 ${res.email ?? ''}（${res.project_id ?? ''}）`, `Imported Vertex service account ${res.email ?? ''} (${res.project_id ?? ''})`));
      setFile(null);
      onDone();
    } catch (e) {
      toastError(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="stack">
      <p className="muted" style={{ margin: 0 }}>
        {tr('上传 Google Cloud 服务账号密钥 JSON，Home 会把它保存为 Vertex 凭据。Vertex API Key 请在“Gemini / Vertex 密钥”页面配置。', 'Upload a Google Cloud service-account key JSON; Home stores it as a Vertex credential. Vertex API keys are configured on the "Gemini / Vertex keys" page.')}
      </p>
      <div className="form-grid">
        <div className="field">
          <label>{tr('服务账号 JSON', 'Service account JSON')}</label>
          <input className="input" type="file" accept=".json,application/json" onChange={(e) => setFile(e.target.files?.[0] ?? null)} style={{ paddingTop: 5 }} />
        </div>
        <div className="field">
          <label>{tr('区域 (location)', 'Location')}</label>
          <input className="input" value={location} onChange={(e) => setLocation(e.target.value)} />
        </div>
      </div>
      <div>
        <button className="btn btn-primary" disabled={!file || busy} onClick={submit}>
          {busy && <Spinner />}
          {tr('导入', 'Import')}
        </button>
      </div>
    </div>
  );
}

/** Split pasted text into chunks the server accepts (<=50 entries per request). */
export function chunkTokenInput(text: string): { text?: string; items?: unknown[] }[] {
  // Strip BOM / zero-width chars and normalize newlines / dash variants from copy-paste.
  const t = text
    .replace(/[\u200b-\u200d\u2060\ufeff]/g, '')
    .replace(/[\u00a0\u3000]/g, ' ')
    .replace(/\r\n?/g, '\n')
    .trim();
  if (!t) return [];
  if (t.startsWith('[')) {
    try {
      const arr = JSON.parse(t) as unknown[];
      if (Array.isArray(arr)) {
        const out = [];
        for (let i = 0; i < arr.length; i += 50) out.push({ items: arr.slice(i, i + 50) });
        return out;
      }
    } catch {
      /* fall through to line mode */
    }
  }
  const lines = t.split('\n').map((l) => l.trim()).filter((l) => l && !l.startsWith('#'));
  const out = [];
  for (let i = 0; i < lines.length; i += 50) out.push({ text: lines.slice(i, i + 50).join('\n') });
  return out;
}

const JOB_KEY = 'cpa.importJob';

/** Attach to the current server-side import job (survives tab switch, dialog close and reload). */
function useImportJob() {
  const api = useApi();
  const [jobId, setJobIdState] = useState<string | null>(() => localStorage.getItem(JOB_KEY));
  const [job, setJob] = useState<ImportJob | null>(null);
  const [results, setResults] = useState<RTImportResult[] | null>(null);
  const setJobId = (id: string | null) => {
    if (id) localStorage.setItem(JOB_KEY, id);
    else localStorage.removeItem(JOB_KEY);
    setJob(null);
    setResults(null);
    setJobIdState(id);
  };
  useEffect(() => {
    if (!jobId) return;
    let stop = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const tick = async () => {
      try {
        const r = await api.importJob(jobId);
        if (stop) return;
        setJob(r.job);
        setResults(r.results);
        if (r.job.status !== 'running') return;
      } catch {
        /* keep polling */
      }
      if (!stop) timer = setTimeout(() => void tick(), 2000);
    };
    void tick();
    return () => {
      stop = true;
      if (timer) clearTimeout(timer);
    };
  }, [api, jobId]);
  return { jobId, job, results, setJobId, running: job?.status === 'running' || (!!jobId && !job) };
}

const JOB_STATUS: Record<string, string> = { running: '进行中', done: '完成', failed: '失败', interrupted: '中断' };

function ImportJobPanel({ job, results }: { job: ImportJob | null; results: RTImportResult[] | null }) {
  if (!job) return null;
  return (
    <div className="stack" data-testid="import-job">
      <div className="alert small row" style={{ gap: 8, flexWrap: 'wrap' }}>
        {job.status === 'running' && <Spinner />}
        <b>导入任务 {JOB_STATUS[job.status] ?? job.status}</b>
        <span>
          {job.done} / {job.total} · 新建 {job.created} · 已存在更新 {job.updated} · 失败 {job.failed}
          {job.reserve ? ' · 储备' : ''}
        </span>
        {job.error && <span className="err">{job.error}</span>}
        {job.status === 'interrupted' && <span className="faint">（服务重启导致中断，已处理部分已保存，可重新导入剩余项，重复项只会更新）</span>}
        <span className="spacer" />
        <span className="faint mono">{job.id}</span>
      </div>
      {job.health_batch_id && <HealthBatchPanel batchId={job.health_batch_id} />}
      {results && results.length > 0 && <ImportResultsTable results={results} />}
    </div>
  );
}

function RecentImports({ onOpen, current }: { onOpen: (id: string) => void; current?: string | null }) {
  const api = useApi();
  const [jobs, setJobs] = useState<ImportJob[] | null>(null);
  const [open, setOpen] = useState(false);
  useEffect(() => {
    if (!open) return;
    api.importJobs().then((r) => setJobs(r.jobs)).catch(() => setJobs([]));
  }, [api, open, current]);
  return (
    <details className="small" open={open} onToggle={(e) => setOpen((e.target as HTMLDetailsElement).open)} data-testid="recent-imports">
      <summary className="faint" style={{ cursor: 'pointer' }}>最近导入</summary>
      {jobs && !jobs.length && <div className="faint">暂无</div>}
      {jobs?.map((j) => (
        <div className="list-row" key={j.id}>
          <span className="faint">{new Date(j.started_at).toLocaleString()}</span>
          <span className={`badge ${j.status === 'done' ? 'ok' : j.status === 'running' ? 'primary' : 'warn'}`}>{JOB_STATUS[j.status] ?? j.status}</span>
          <span>
            {j.done}/{j.total} · 新建 {j.created} · 更新 {j.updated} · 失败 {j.failed}
            {j.reserve ? ' · 储备' : ''}
          </span>
          <span className="spacer" />
          <button className="btn btn-sm" disabled={j.id === current} onClick={() => onOpen(j.id)}>
            查看
          </button>
        </div>
      ))}
    </details>
  );
}

const RT_STATUS: Record<string, [string, 'ok' | 'warn' | 'err' | 'primary']> = {
  created: ['新建', 'ok'],
  updated: ['已更新', 'primary'],
  would_create: ['将新建', 'ok'],
  would_update: ['将更新', 'primary'],
  failed: ['失败', 'err'],
  skipped: ['跳过', 'warn'],
};

function RefreshTokenImport({ onDone, initialText = '', egress, allowPrimary, reserve }: { onDone: () => void; initialText?: string; egress: string; allowPrimary?: boolean; reserve?: boolean }) {
  const api = useApi();
  const { toast, toastError } = useUi();
  const [text, setText] = useState(initialText);
  const [busy, setBusy] = useState(false);
  const [progress, setProgress] = useState('');
  const [results, setResults] = useState<RTImportResult[] | null>(null);
  const [lastDry, setLastDry] = useState(true);
  const [healthCheck, setHealthCheck] = useState(true);
  const [healthBatch, setHealthBatch] = useState<string | null>(null);
  const ij = useImportJob();
  const chunks = chunkTokenInput(text);
  const count = chunks.reduce((a, c) => a + (c.items?.length ?? c.text?.split('\n').length ?? 0), 0);
  const fileRef = useRef<HTMLInputElement>(null);
  const notified = useRef<string | null>(null);
  useEffect(() => {
    if (ij.job && ij.job.status !== 'running' && notified.current !== ij.job.id) {
      notified.current = ij.job.id;
      if (ij.job.created || ij.job.updated) onDone();
    }
  }, [ij.job, onDone]);
  const run = async (dry: boolean) => {
    if (!dry) {
      setBusy(true);
      setResults(null);
      setHealthBatch(null);
      try {
        const body = text.trim().startsWith('[') && chunks.every((c) => c.items) ? { items: chunks.flatMap((c) => c.items ?? []) } : { text: chunks.map((c) => c.text ?? '').join('\n') };
        const res = await api.startImportJob({ ...body, egress_id: egress, allow_primary: allowPrimary, reserve: reserve || undefined, health_check: healthCheck });
        ij.setJobId(res.job.id);
        setText('');
        toast(`已开始导入 ${res.job.total} 条，切换页签或关闭窗口不影响进度`);
      } catch (e) {
        toastError(e);
      } finally {
        setBusy(false);
      }
      return;
    }
    setBusy(true);
    setResults(null);
    setLastDry(dry);
    const batchId = `rt-${new Date().toISOString().slice(0, 16).replace(/[-:T]/g, '')}-${Math.random().toString(36).slice(2, 8)}`;
    let checking = false;
    const all: RTImportResult[] = [];
    let offset = 0;
    try {
      for (let i = 0; i < chunks.length; i++) {
        setProgress(`${i + 1} / ${chunks.length}`);
        const res = await api.importRefreshTokens({ ...chunks[i], dry_run: dry, egress_id: egress, allow_primary: allowPrimary, reserve: reserve || undefined, health_check: healthCheck, batch_id: dry ? undefined : batchId });
        if (res.health_batch_id) checking = true;
        res.results.forEach((r) => all.push({ ...r, index: r.index + offset }));
        offset += res.results.length;
        setResults([...all]);
      }
      const n = (s: string) => all.filter((r) => r.status === s).length;
      toast(
        dry
          ? `预检完成：将新建 ${n('would_create')}，将更新 ${n('would_update')}，失败 ${n('failed')}`
          : `导入完成：新建 ${n('created')}，更新 ${n('updated')}，失败 ${n('failed')}`,
        n('failed') ? 'err' : 'ok',
      );
      if (!dry && (n('created') || n('updated'))) onDone();
      if (!dry) setHealthBatch(checking ? batchId : null);
    } catch (e) {
      toastError(e);
    } finally {
      setBusy(false);
      setProgress('');
    }
  };
  return (
    <div className="stack">
      <p className="muted" style={{ margin: 0 }}>
        每行一个：<code>email----refresh_token</code> 或仅 <code>refresh_token</code>；也可粘贴 JSON 数组（字符串或含 refresh_token 的对象）。
        导入时会刷新令牌、获取邮箱与 project_id；同邮箱或同 token 的已有凭据只更新令牌，保留分组与备注。新凭据自动加入 default 分组并立即探测额度。
      </p>
      <textarea
        className="textarea mono"
        style={{ minHeight: 200 }}
        placeholder={'alice@gmail.com----1//0g...\nbob@gmail.com----1//0e...\n\n或 [{"refresh_token":"1//..."}, ...]'}
        value={text}
        onChange={(e) => setText(e.target.value)}
        spellCheck={false}
        data-testid="rt-import-text"
      />
      <div className="row" style={{ gap: 8 }}>
        <button className="btn" disabled={busy} onClick={() => fileRef.current?.click()}>
          从文件读取（.txt / .json）
        </button>
        <input
          ref={fileRef}
          type="file"
          accept=".txt,.json,text/plain,application/json"
          hidden
          onChange={async (e) => {
            const f = e.target.files?.[0];
            if (f) setText(await f.text());
            e.target.value = '';
          }}
        />
        <span className="faint small">识别到 {count} 条</span>
        <label className="row gap-sm small" title="新建的凭据导入后自动体检：订阅等级、额度是否全新、模型目录、经固定出口发 1 个最小真实请求（检测需验证/403/429）。结果可导出给供应商售后。">
          <input type="checkbox" checked={healthCheck} onChange={(e) => setHealthCheck(e.target.checked)} data-testid="rt-import-health" />
          导入后体检
        </label>
        <span className="spacer" />
        {progress && <span className="faint small">批次 {progress}</span>}
        <button className="btn" disabled={!count || busy} onClick={() => void run(true)} data-testid="rt-import-dry">
          {busy && lastDry && <Spinner />}
          预检（不保存）
        </button>
        <button className="btn btn-primary" disabled={!count || busy || ij.running} title={ij.running ? '已有导入任务进行中' : undefined} onClick={() => void run(false)} data-testid="rt-import-run">
          {busy && !lastDry && <Spinner />}
          导入
        </button>
      </div>
      {healthBatch && <HealthBatchPanel batchId={healthBatch} />}
      {results && <ImportResultsTable results={results} />}
      {!results && <ImportJobPanel job={ij.job} results={ij.results} />}
      <RecentImports current={ij.jobId} onOpen={(id) => { setResults(null); ij.setJobId(id); }} />
    </div>
  );
}

function ImportResultsTable({ results }: { results: RTImportResult[] }) {
  return (
        <div className="table-wrap scroll-list" style={{ maxHeight: 280 }}>
          <table className="table compact">
            <thead>
              <tr>
                <th>#</th>
                <th>结果</th>
                <th>邮箱</th>
                <th>Token</th>
                <th>说明</th>
              </tr>
            </thead>
            <tbody>
              {results.map((r) => {
                const [label, kind] = RT_STATUS[r.status] ?? [r.status, 'warn'];
                return (
                  <tr key={r.index}>
                    <td className="faint">{r.index + 1}</td>
                    <td>
                      <span className={`badge ${kind}`}>{label}</span>
                    </td>
                    <td className="ellipsis" style={{ maxWidth: 220 }} title={r.email}>
                      {r.email || '—'}
                    </td>
                    <td className="mono faint">{r.token_hint}</td>
                    <td className="small ellipsis" style={{ maxWidth: 320 }} title={[r.reason, r.warning, r.matched_by && `匹配：${r.matched_by}`].filter(Boolean).join('\n')}>
                      {r.reason || r.warning || (r.matched_by ? `匹配已有凭据（${r.matched_by === 'email' ? '邮箱' : 'token'}）` : r.project_id ? `project ${r.project_id}` : '')}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
  );
}

/** Import batch health check progress + failures, with supplier-claim export. */
function HealthBatchPanel({ batchId, showAll = true }: { batchId: string; showAll?: boolean }) {
  const api = useApi();
  const { toast, toastError } = useUi();
  const { tr } = useI18n();
  const [data, setData] = useState<{ imported: number; summary: HealthSummary; items: HealthItem[] } | null>(null);
  useEffect(() => {
    let stop = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const started = Date.now();
    const tick = async () => {
      try {
        const r = await api.healthBatch(batchId, showAll);
        if (stop) return;
        setData(r);
        const done = r.summary.total >= r.imported && r.summary.running === 0 && r.summary.total > 0;
        if (done || Date.now() - started > 15 * 60_000) return;
      } catch {
        /* keep polling */
      }
      if (!stop) timer = setTimeout(() => void tick(), 4000);
    };
    void tick();
    return () => {
      stop = true;
      if (timer) clearTimeout(timer);
    };
  }, [api, batchId, showAll]);
  const s = data?.summary;
  const pending = data ? Math.max(0, data.imported - (s?.total ?? 0)) + (s?.running ?? 0) : 0;
  return (
    <div className="alert small stack" data-testid="health-batch">
      <div className="row" style={{ gap: 8, flexWrap: 'wrap' }}>
        <b>{tr('导入体检', 'Import check')}</b>
        <span>{s ? healthSummaryText(s, tr) : tr('体检排队中…', 'Queued…')}</span>
        {pending > 0 && (
          <span className="faint">
            <Spinner /> {tr(`剩余 ${pending}`, `${pending} left`)}
          </span>
        )}
        <span className="spacer" />
        <span className="faint mono">{batchId}</span>
        <button
          className="btn btn-sm"
          disabled={!s?.fail}
          onClick={async () => {
            try {
              downloadBlob((await api.exportHealth({ batch_id: batchId, status: 'fail', format: 'csv' })) as Blob, `health-failed-${batchId}.csv`);
            } catch (e) {
              toastError(e);
            }
          }}
          data-testid="health-batch-csv"
        >
          {tr('导出失败 CSV', 'Export CSV')}
        </button>
        <button
          className="btn btn-sm"
          disabled={!s?.fail}
          onClick={async () => {
            try {
              const text = (await api.exportHealth({ batch_id: batchId, status: 'fail', format: 'text' })) as string;
              await navigator.clipboard.writeText(text);
              toast(tr('已复制（邮箱 / 原因 / 时间 / 上游原文）', 'Copied'));
            } catch (e) {
              toastError(e);
            }
          }}
          data-testid="health-batch-copy"
        >
          {tr('复制失败列表', 'Copy failed')}
        </button>
      </div>
      {!!data?.items.length && (
        <div className="table-wrap scroll-list" style={{ maxHeight: 200 }}>
          <table className="table compact">
            <tbody>
              {data.items.map((it) => (
                <tr key={it.auth_id}>
                  <td className="faint">{it.seq ? `#${it.seq}` : ''}</td>
                  <td className="ellipsis" style={{ maxWidth: 220 }}>{it.email}</td>
                  <td>{it.tier ? <span className="badge">{it.tier}</span> : <span className="faint">—</span>}</td>
                  <td title="gemini-4-argon">{it.argon === undefined ? <span className="faint">argon ?</span> : it.argon ? <span className="badge ok">argon ✓</span> : <span className="badge warn">无 argon</span>}</td>
                  <td title={it.import ? importVerdictText(it.import, tr) : undefined}>{it.import ? (it.import.status === 'pass' ? <span className="faint">{tr('导入:通过', 'Import: pass')}</span> : <span className="badge warn">{tr('导入', 'Import')}: {it.import.categories.map((c) => healthCategoryLabel(c, tr, it.import?.tier)).join('/')}</span>) : null}</td>
                  <td>{it.status === 'running' ? tr('体检中', 'Checking') : it.status === 'pass' ? <span className="badge ok">{tr('正常', 'OK')}</span> : (it.categories ?? []).map((c) => healthCategoryLabel(c, tr, it.tier)).join(' / ')}</td>
                  <td className="small ellipsis" style={{ maxWidth: 320 }} title={[...(it.reasons ?? []), it.evidence ?? ''].join('\n')}>
                    {(it.reasons ?? []).join('；')}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
