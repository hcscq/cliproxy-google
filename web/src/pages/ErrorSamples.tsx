import { useState } from 'react';
import { useAction, useApi, useAsync, useUi } from '../app-context';
import type { ErrorSample } from '../api/client';
import { fmtTime } from '../format';
import { useI18n } from '../i18n';
import { Badge, Card, CopyButton, Empty, ErrorBox, Loading, Spinner } from '../components/ui';
import { IconRefresh, IconTrash } from '../components/icons';

function fmtBytes(n: number): string {
  if (n >= 1 << 20) return `${(n / (1 << 20)).toFixed(1)} MB`;
  if (n >= 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${n} B`;
}

// pretty renders JSON text indented; other text is returned unchanged.
function pretty(v: unknown): string {
  if (v == null) return '';
  if (typeof v !== 'string') return JSON.stringify(v, null, 2);
  const s = v.trim();
  if (s.startsWith('{') || s.startsWith('[')) {
    try {
      return JSON.stringify(JSON.parse(s), null, 2);
    } catch {
      /* not plain JSON */
    }
  }
  return v;
}

const FIELDS: [string, string, string][] = [
  ['errors', '错误信息', 'Errors'],
  ['request_headers', '客户端请求头（已脱敏）', 'Client headers (redacted)'],
  ['request_body', '客户端请求体', 'Client request body'],
  ['upstream_request', '转换后的上游请求', 'Translated upstream request'],
  ['upstream_response', '上游响应', 'Upstream response'],
  ['response', '返回给客户端的响应', 'Response to client'],
];

const preStyle = { maxHeight: 420, overflow: 'auto', whiteSpace: 'pre-wrap' as const, wordBreak: 'break-all' as const, fontSize: 12, margin: 0, padding: 8, background: 'var(--bg-subtle, rgba(127,127,127,.08))', borderRadius: 6 };

function SampleView({ s }: { s: ErrorSample }) {
  const { tr } = useI18n();
  const sample = (s.sample ?? {}) as Record<string, unknown>;
  const all = JSON.stringify(sample, null, 2);
  const meta: [string, unknown][] = [
    [tr('时间', 'Time'), fmtTime(s.created_at)],
    [tr('状态码', 'Status'), s.status],
    [tr('请求模型', 'Model'), s.requested_model],
    ['request id', s.request_id],
    [tr('客户端 Key 前缀', 'Client key prefix'), sample.client_key_prefix],
    [tr('耗时', 'Latency'), sample.latency_ms != null ? `${sample.latency_ms} ms` : ''],
    [tr('代理节点', 'Proxy'), sample.proxy_node ?? s.proxy_ip],
    ['Home', s.home_ip],
    [tr('原始大小', 'Raw size'), fmtBytes(s.size)],
    ['URL', `${sample.method ?? ''} ${sample.url ?? ''}`],
  ];
  return (
    <Card>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
        <div style={{ display: 'flex', gap: 14, flexWrap: 'wrap', fontSize: 13 }}>
          {meta.filter(([, v]) => v != null && v !== '' && v !== ' ').map(([k, v]) => (
            <span key={k}>
              <span className="muted">{k}: </span>
              {String(v)}
            </span>
          ))}
          {sample.truncated ? <Badge kind="warn">{tr('已截断', 'Truncated')}</Badge> : null}
        </div>
        <span style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
          <span className="muted" style={{ fontSize: 12 }}>{tr('复制全部', 'Copy all')}</span>
          <CopyButton text={all} small />
        </span>
      </div>
      {FIELDS.filter(([k]) => sample[k] != null && sample[k] !== '').map(([k, zh, en]) => {
        const text = pretty(sample[k]);
        return (
          <details key={k} open={k === 'errors' || k === 'request_body' || k === 'upstream_response'} style={{ marginTop: 10 }}>
            <summary style={{ cursor: 'pointer', display: 'flex', gap: 8, alignItems: 'center' }}>
              <b>{tr(zh, en)}</b>
              <span className="muted" style={{ fontSize: 12 }}>{fmtBytes(text.length)}</span>
              <CopyButton text={text} small />
            </summary>
            <pre style={preStyle}>{text}</pre>
          </details>
        );
      })}
    </Card>
  );
}

export function ErrorSamplesTab() {
  const api = useApi();
  const { tr } = useI18n();
  const { toast, confirm } = useUi();
  const { busy, run } = useAction();
  const types = useAsync(() => api.errorSampleTypes(), []);
  const [sel, setSel] = useState('');
  const samples = useAsync(() => (sel ? api.errorSamples(sel) : Promise.resolve(null)), [sel]);
  const st = types.data?.settings;
  return (
    <div className="stack">
      <Card>
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
          <div className="muted" style={{ fontSize: 13 }}>
            {st
              ? tr(
                  `失败请求按错误类型保留最近 ${st.per_type} 条完整上下文（敏感头与令牌已脱敏），最多 ${st.max_types} 类，单条上限 ${fmtBytes(st.max_bytes)}${st.enabled ? '' : '（当前已关闭）'}。当前占用 ${fmtBytes(types.data?.stored_bytes ?? 0)}（压缩后）。在“设置 → 日志”中调整。`,
                  `Keeps the latest ${st.per_type} full contexts per error type (secrets redacted), up to ${st.max_types} types, ${fmtBytes(st.max_bytes)} per sample${st.enabled ? '' : ' (currently disabled)'}. Stored: ${fmtBytes(types.data?.stored_bytes ?? 0)} compressed. Configure in Settings → Logs.`,
                )
              : ''}
          </div>
          <div className="toolbar">
            <button className="btn" onClick={() => { types.reload(); if (sel) samples.reload(); }}>
              <IconRefresh />
              {tr('刷新', 'Refresh')}
            </button>
            <button
              className="btn btn-danger"
              disabled={busy === 'clear-samples'}
              onClick={async () => {
                if (!(await confirm({ title: tr('清空错误样本', 'Clear error samples'), body: sel ? tr(`删除类型 ${sel} 的所有样本？`, `Delete all samples of ${sel}?`) : tr('删除所有错误样本？', 'Delete all error samples?'), danger: true }))) return;
                await run('clear-samples', async () => {
                  const r = await api.clearErrorSamples(sel || undefined);
                  toast(tr(`已删除 ${r?.deleted ?? 0} 条`, `Deleted ${r?.deleted ?? 0}`));
                  setSel('');
                  await types.reload();
                });
              }}
            >
              {busy === 'clear-samples' ? <Spinner /> : <IconTrash />}
              {sel ? tr('清空此类型', 'Clear this type') : tr('全部清空', 'Clear all')}
            </button>
          </div>
        </div>
      </Card>
      {types.error ? (
        <ErrorBox error={types.error} onRetry={types.reload} />
      ) : !types.data ? (
        <Loading />
      ) : types.data.types.length === 0 ? (
        <Empty>{tr('暂无错误样本', 'No error samples yet')}</Empty>
      ) : (
        <Card>
          <table className="table">
            <thead>
              <tr>
                <th>{tr('错误类型', 'Error type')}</th>
                <th>{tr('样本数', 'Samples')}</th>
                <th>{tr('最近出现', 'Last seen')}</th>
                <th>{tr('占用', 'Size')}</th>
              </tr>
            </thead>
            <tbody>
              {types.data.types.map((t) => (
                <tr key={t.type} onClick={() => setSel(sel === t.type ? '' : t.type)} style={{ cursor: 'pointer', background: sel === t.type ? 'var(--primary-soft, rgba(80,120,255,.1))' : undefined }} data-testid="error-sample-type">
                  <td>
                    <Badge kind={t.status >= 500 ? 'err' : 'warn'}>{t.type}</Badge>
                  </td>
                  <td>{t.count}</td>
                  <td>{fmtTime(t.last_seen)}</td>
                  <td>{fmtBytes(t.bytes)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </Card>
      )}
      {sel && (samples.error ? <ErrorBox error={samples.error} onRetry={samples.reload} /> : !samples.data ? <Loading /> : samples.data.samples.map((s) => <SampleView key={s.id} s={s} />))}
    </div>
  );
}
