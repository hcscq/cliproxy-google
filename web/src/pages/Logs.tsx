import { useEffect, useRef, useState } from 'react';
import { useAction, useApi, useAsync, useUi } from '../app-context';
import { useI18n } from '../i18n';
import { Card, ErrorBox, Loading, Spinner, Switch } from '../components/ui';
import { IconRefresh, IconTrash } from '../components/icons';
import { ErrorSamplesTab } from './ErrorSamples';

const LEVELS = ['', 'debug', 'info', 'warn', 'error'];

export function LogsPage() {
  const { tr } = useI18n();
  const [tab, setTab] = useState<'app' | 'samples'>('app');
  return (
    <div className="stack">
      <div className="tabs" style={{ marginBottom: 0 }}>
        <button className={tab === 'app' ? 'active' : ''} onClick={() => setTab('app')} data-testid="logs-tab-app">
          {tr('应用日志', 'App logs')}
        </button>
        <button className={tab === 'samples' ? 'active' : ''} onClick={() => setTab('samples')} data-testid="logs-tab-samples">
          {tr('错误样本', 'Error samples')}
        </button>
      </div>
      {tab === 'app' ? <AppLogs /> : <ErrorSamplesTab />}
    </div>
  );
}

function AppLogs() {
  const api = useApi();
  const { t, tr } = useI18n();
  const { toast, confirm } = useUi();
  const { busy, run } = useAction();
  const [level, setLevel] = useState('');
  const [requestId, setRequestId] = useState('');
  const [text, setText] = useState('');
  const [hideProbes, setHideProbes] = useState(true);
  const [follow, setFollow] = useState(true);
  const [limit, setLimit] = useState(300);
  const logs = useAsync(() => api.logs({ level, request_id: requestId.trim(), limit }), [level, requestId, limit], follow ? 5000 : undefined);
  const view = useRef<HTMLDivElement>(null);

  const lines = (logs.data?.logs ?? [])
    .slice()
    .reverse()
    .filter((l) => (!hideProbes || !/"\/(readyz|healthz)"/.test(l.line)) && (!text || l.line.toLowerCase().includes(text.toLowerCase())));

  useEffect(() => {
    if (follow && view.current) view.current.scrollTop = view.current.scrollHeight;
  }, [lines.length, follow]);

  return (
    <div className="stack">
      <div className="page-head">
        <div>
          <h1>{t('logs')}</h1>
          <p>{tr('Home 与所有代理节点写入 PostgreSQL 的应用日志（最新在下）。', 'Application logs written to PostgreSQL by Home and every proxy node (newest at the bottom).')}</p>
        </div>
        <div className="toolbar">
          <button className="btn" onClick={logs.reload}>
            <IconRefresh />
            {t('refresh')}
          </button>
          <button
            className="btn btn-danger"
            disabled={busy === 'clear'}
            onClick={async () => {
              if (!(await confirm({ title: tr('清空日志', 'Clear logs'), body: tr('将删除数据库中所有节点的日志记录（不影响 Pod 标准输出）。', 'Deletes all log rows from the database for every node (stdout is unaffected).'), danger: true }))) return;
              await run('clear', async () => {
                const r = await api.clearLogs();
                toast(tr(`已删除 ${r?.removed ?? 0} 条`, `Removed ${r?.removed ?? 0} rows`));
                await logs.reload();
              });
            }}
          >
            {busy === 'clear' ? <Spinner /> : <IconTrash />}
            {tr('清空', 'Clear')}
          </button>
        </div>
      </div>
      <Card
        title={
          <div className="row">
            <select className="select input-sm" style={{ width: 110 }} value={level} onChange={(e) => setLevel(e.target.value)}>
              {LEVELS.map((l) => (
                <option key={l} value={l}>
                  {l || tr('全部级别', 'All levels')}
                </option>
              ))}
            </select>
            <input className="input input-sm" style={{ width: 170 }} placeholder={tr('请求 ID', 'Request ID')} value={requestId} onChange={(e) => setRequestId(e.target.value)} />
            <input className="input input-sm" style={{ width: 200 }} placeholder={tr('过滤文本', 'Filter text')} value={text} onChange={(e) => setText(e.target.value)} />
            <select className="select input-sm" style={{ width: 100 }} value={limit} onChange={(e) => setLimit(Number(e.target.value))}>
              {[100, 300, 1000].map((n) => (
                <option key={n} value={n}>
                  {n}
                </option>
              ))}
            </select>
          </div>
        }
        extra={
          <>
            <Switch checked={hideProbes} onChange={setHideProbes} label={<span className="small">{tr('隐藏健康检查', 'Hide probes')}</span>} />
            <Switch checked={follow} onChange={setFollow} label={<span className="small">{tr('自动刷新', 'Live')}</span>} />
          </>
        }
      >
        {logs.error ? (
          <ErrorBox error={logs.error} onRetry={logs.reload} />
        ) : !logs.data ? (
          <Loading />
        ) : (
          <div className="log-view" ref={view} data-testid="log-view">
            {lines.length === 0 && <div className="log-line">{tr('（没有日志）', '(no logs)')}</div>}
            {lines.map((l) => (
              <div key={l.id} className={`log-line ${l.level}`}>
                {l.line}
              </div>
            ))}
          </div>
        )}
        <div className="faint small" style={{ marginTop: 8 }}>
          {tr(`显示 ${lines.length} 条，数据库共 ${logs.data?.total ?? 0} 条`, `Showing ${lines.length} of ${logs.data?.total ?? 0} rows`)}
        </div>
      </Card>
    </div>
  );
}
