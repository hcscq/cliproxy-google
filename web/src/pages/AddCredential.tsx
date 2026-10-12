import { useEffect, useRef, useState } from 'react';
import { useApi, useUi } from '../app-context';
import { stateFromRedirect, type OAuthResult } from '../api/client';
import { useI18n } from '../i18n';
import { CopyButton, Modal, Spinner } from '../components/ui';
import { IconExternal, IconUpload } from '../components/icons';

type Tab = 'oauth' | 'upload' | 'paste' | 'vertex';

export function AddCredentialModal({ onClose, onAdded }: { onClose: () => void; onAdded: () => void }) {
  const { t } = useI18n();
  const [tab, setTab] = useState<Tab>('oauth');
  const tabs: [Tab, string][] = [
    ['oauth', t('antigravityLogin')],
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
      {tab === 'oauth' && <OAuthFlow onDone={onAdded} />}
      {tab === 'upload' && <UploadFiles onDone={onAdded} />}
      {tab === 'paste' && <PasteJson onDone={onAdded} />}
      {tab === 'vertex' && <VertexImport onDone={onAdded} />}
    </Modal>
  );
}

function OAuthFlow({ onDone }: { onDone: () => void }) {
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
      toast(tr(`账号 ${res.email ?? ''} 已存在，已更新其令牌（未新增重复凭据）`, `Account ${res.email ?? ''} already exists; its tokens were updated (no duplicate added)`));
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
      const res = await api.oauthStart('antigravity');
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

function UploadFiles({ onDone }: { onDone: () => void }) {
  const api = useApi();
  const { toast, toastError } = useUi();
  const { tr } = useI18n();
  const [files, setFiles] = useState<File[]>([]);
  const [busy, setBusy] = useState(false);
  const [drag, setDrag] = useState(false);
  const input = useRef<HTMLInputElement>(null);
  const add = (list: FileList | null) => {
    if (!list) return;
    setFiles((prev) => [...prev, ...Array.from(list).filter((f) => f.name.toLowerCase().endsWith('.json'))]);
  };
  const upload = async () => {
    setBusy(true);
    try {
      const res = await api.uploadCredentialFiles(files);
      toast(tr(`已上传 ${res.uploaded ?? files.length} 个凭据`, `Uploaded ${res.uploaded ?? files.length} credential(s)`));
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
        {tr('上传 CLIProxyAPI 格式的凭据 JSON（如 antigravity-xxx.json），可多选。', 'Upload CLIProxyAPI-format credential JSON files (e.g. antigravity-xxx.json). Multiple files allowed.')}
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
    </div>
  );
}

function PasteJson({ onDone }: { onDone: () => void }) {
  const api = useApi();
  const { toast, toastError } = useUi();
  const { tr } = useI18n();
  const [text, setText] = useState('');
  const [busy, setBusy] = useState(false);
  let parseError: string | undefined;
  let provider: string | undefined;
  if (text.trim()) {
    try {
      const v = JSON.parse(text);
      if (!v || typeof v !== 'object' || Array.isArray(v)) parseError = tr('必须是 JSON 对象', 'Must be a JSON object');
      else provider = typeof v.type === 'string' ? v.type : typeof v.provider === 'string' ? v.provider : undefined;
    } catch (e) {
      parseError = (e as Error).message;
    }
  }
  const submit = async () => {
    setBusy(true);
    try {
      const res = await api.uploadCredentialJSON(text);
      toast(tr(`已导入 ${res.name ?? ''}`, `Imported ${res.name ?? ''}`));
      setText('');
      onDone();
    } catch (e) {
      toastError(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="stack">
      <textarea className="textarea" style={{ minHeight: 220 }} placeholder='{"type": "antigravity", "access_token": "...", "refresh_token": "...", ...}' value={text} onChange={(e) => setText(e.target.value)} spellCheck={false} />
      {parseError && <div className="alert err small">JSON: {parseError}</div>}
      {!parseError && provider && (
        <div className="faint small">
          {tr('检测到类型', 'Detected type')}: <b>{provider}</b>
        </div>
      )}
      <div>
        <button className="btn btn-primary" disabled={!text.trim() || !!parseError || busy} onClick={submit}>
          {busy && <Spinner />}
          {tr('导入', 'Import')}
        </button>
      </div>
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
