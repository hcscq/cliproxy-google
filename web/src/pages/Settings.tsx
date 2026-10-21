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
  /** Compact sections only: shown under a collapsed "Advanced" toggle. */
  adv?: boolean;
}

const SECTIONS: { zh: string; en: string; compact?: boolean; fields: FieldDef[] }[] = [
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
      { path: 'routing.retry.first-byte-deadline', type: 'number', def: 120, zh: '流式首字节总时限(秒)', en: 'Stream first-byte deadline (s)', hintZh: '请求在此时间内未向客户端输出任何数据（含调度、Home/Redis 调用和全部重试）即中止，返回可重试的 503，让 new-api 切换渠道。已开始的流式不受影响。0=默认 120，<0 关闭。', hintEn: 'If a request has sent nothing to the client within this time (dispatch, Home/Redis calls and all retries), it is cancelled with a retryable 503 so new-api fails over. Started streams are never cut. 0 = default 120, <0 off.' },
      { path: 'routing.retry.first-byte-deadline-nonstream', type: 'number', def: 240, zh: '非流式总时限(秒)', en: 'Non-stream deadline (s)', hintZh: '非流式请求要等整段生成完才有首字节，单独设置，应大于非流式单次超时（默认 180）。超时返回可重试的 503。0=默认 240，<0 关闭。', hintEn: 'Non-stream requests only send a first byte after full generation, so they get their own bound; keep it above the non-stream attempt timeout (default 180). Retryable 503 on expiry. 0 = default 240, <0 off.' },
      { path: 'routing.retry.home-kv-timeout-ms', type: 'number', adv: true, def: 500, zh: '非关键 KV 调用超时(毫秒)', en: 'Best-effort KV timeout (ms)', hintZh: '请求路径上的非关键 Home/Redis 调用（思考回放读写等）超过此时间直接跳过继续处理。0=默认 500。', hintEn: 'Non-critical Home/Redis calls on the request path (reasoning replay read/write) are skipped after this. 0 = default 500.' },
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
      { path: 'routing.cooldown.quota-probe-backoff', type: 'text', def: '1m,2m,5m,15m,30m', zh: '冷却探测退避', en: 'Cooldown probe backoff', hintZh: '冷却中的（凭据, 模型）按此序列逐次拉长探测间隔，最后一项重复（逗号分隔的 Go 时长）。上游给出长重置时间也照常探测，以便捕捉 Google 提前/额外重置；额度桶剩余比例上升时立即探测。探测记入用量（event_type=probe）。默认 1m,2m,5m,15m,30m', hintEn: 'Per (credential, model) re-probe schedule; the last step repeats (comma-separated Go durations). Long upstream resets are probed too to catch early/bonus resets; a rising bucket ratio triggers an immediate probe. Probes are logged to usage (event_type=probe). Default 1m,2m,5m,15m,30m' },
      { path: 'routing.cooldown.quota-canary', type: 'bool', def: true, zh: '探测成功后金丝雀放行', en: 'Canary after probe success', hintZh: '在已知重置时间之前，1 token 探测成功不直接解锁：全集群只放行 1 个真实请求，成功才解锁；仍额度耗尽则重新冷却并拉长退避。过了重置时间照常解锁。默认开启。', hintEn: 'Before the known reset, a passing 1-token probe does not unlock: exactly one real request is let through cluster-wide and the pair unlocks only if it succeeds; QUOTA_EXHAUSTED re-blocks and advances backoff. After the reset time, normal unlock. Default on.' },
      { path: 'routing.cooldown.quota-bucket-reprobe-seconds', type: 'number', def: 300, zh: '额度桶耗尽复查间隔（秒）', en: 'Exhausted quota bucket re-probe (s)', hintZh: '额度探测显示某个额度桶（如 Gemini 模型）剩余 0% 时，该凭据上这个桶的所有模型暂停调度（其他桶不受影响），并按此间隔重新探测额度；一旦探测到剩余额度 >0 立即恢复，最晚到上游给出的重置时间。默认 300，最小 30；填负数表示不按探测结果暂停', hintEn: 'When the quota probe shows a bucket (e.g. Gemini models) at 0%, all models of that bucket on that credential are skipped (other buckets unaffected) and its quota is re-probed at this interval; released as soon as a probe shows quota >0, at the latest at the upstream reset time. Default 300, min 30; negative disables' },
      { path: 'routing.cooldown.account-block-recheck-seconds', type: 'number', def: 21600, zh: '账号级 403 复查间隔（秒）', en: 'Account-level 403 recheck (s)', hintZh: '凭据返回账号级 403（如 “Verify your account” 需验证账号、账号被封）时自动停用并在面板显示原因；按此间隔发一个极小请求复查，成功即自动重新启用。也可完成验证后手动点「启用」。默认 21600（6 小时），最小 300；负数表示不自动恢复', hintEn: 'Credentials returning an account-level 403 (e.g. "Verify your account", suspended) are disabled with the reason shown; one tiny request rechecks at this interval and re-enables on success. Manual enable still works. Default 21600 (6h), min 300; negative = never auto re-enable' },
      { path: 'quota-exceeded.switch-project', type: 'bool', zh: '额度耗尽时切换项目', en: 'Switch project on quota exceeded' },
      { path: 'quota-exceeded.switch-preview-model', type: 'bool', zh: '额度耗尽时切换预览模型', en: 'Switch to preview model on quota exceeded' },
    ],
  },
  {
    zh: '导入体检',
    en: 'Import health check',
    fields: [
      { path: 'credentials.import-check.on-import', type: 'bool', def: true, zh: '导入后自动体检', en: 'Check after import', hintZh: '新导入的凭据自动检查：订阅等级、额度是否全新、模型目录、经固定出口发 1 个最小真实请求（检测需验证 / 403 / 429）。结果与上游原文可导出给供应商。', hintEn: 'New credentials are checked: tier, fresh quota, catalog, one minimal real request via pinned egress (verify / 403 / 429). Results with raw upstream text are exportable.' },
      { path: 'credentials.import-check.expected-tier', type: 'text', def: 'pro', zh: '期望订阅', en: 'Expected tier', hintZh: 'ultra / pro / free；any = 不检查。默认 pro', hintEn: 'ultra / pro / free; any = skip. Default pro' },
      { path: 'credentials.import-check.quota-min-percent', type: 'number', def: 95, zh: '额度全新阈值（%）', en: 'Fresh quota threshold (%)', hintZh: '每个额度窗口剩余都需 ≥ 此值，否则判为额度非新。默认 95', hintEn: 'Every quota window must have at least this much left. Default 95' },
      { path: 'credentials.import-check.model', type: 'text', def: 'gemini-2.5-flash-lite', zh: '测试模型', en: 'Test model', hintZh: '最小真实请求（maxOutputTokens 1）使用的模型', hintEn: 'Model of the minimal request (maxOutputTokens 1)' },
      { path: 'credentials.import-check.concurrency', type: 'number', def: 3, zh: '体检并发', en: 'Check concurrency', hintZh: '每个 Home 同时体检的凭据数（1–16），避免突发。默认 3', hintEn: 'Concurrent checks per Home (1–16). Default 3' },
      { path: 'credentials.import-check.promote', type: 'select', options: ['pass', 'not-failed', 'any'], def: 'pass', zh: '储备启用条件', en: 'Reserve promotion requires', hintZh: 'pass = 仅体检通过；not-failed = 通过或未体检；any = 不看体检。默认 pass', hintEn: 'pass = passed only; not-failed = passed or unchecked; any = ignore. Default pass' },
      { path: 'credentials.import-check.region-fail', type: 'bool', def: true, zh: '地区限制判为失败', en: 'Region restriction fails', hintZh: '订阅查询返回 UNSUPPORTED_LOCATION（“not available in your location”）时判为体检失败（地区限制），原文作为证据；关闭则判为通过并标记「通过·地区提示」。默认开启', hintEn: 'UNSUPPORTED_LOCATION from the tier lookup fails the check (region restricted), raw text kept as evidence; off = pass tagged as region notice. Default on' },
      { path: 'credentials.import-check.recheck-backoff', type: 'text', def: '10m,30m,2h', zh: '失败自动复检间隔', en: 'Failed re-check backoff', hintZh: '体检失败的凭据按此间隔自动复检（最后一档重复）；off = 关闭。默认 10m,30m,2h', hintEn: 'Failed credentials are re-checked after these delays (last repeats); off disables. Default 10m,30m,2h' },
      { path: 'credentials.import-check.recover-successes', type: 'number', def: 20, zh: '成功即复检阈值', en: 'Re-check after successes', hintZh: '失败后实际调用成功达到此次数时立即复检，避免旧失败结果滞留；0 = 关闭。默认 20', hintEn: 'This many real successes after a failed check trigger an immediate re-check; 0 disables. Default 20' },
      { path: 'credentials.import-check.block-failed', type: 'bool', def: false, zh: '体检失败不参与调度', en: 'Do not schedule failed', hintZh: '开启后体检失败的凭据不接请求（仍保活），重新体检通过后恢复。默认关闭', hintEn: 'Failed credentials get no requests (kept alive) until a re-check passes. Default off' },
    ],
  },
  {
    zh: '凭据储备',
    en: 'Credential reserve',
    fields: [
      { path: 'routing.reserve.min-active', type: 'number', def: 0, zh: '最低可用凭据数', en: 'Minimum active credentials', hintZh: '可用凭据（见下方口径）低于此数时自动启用储备凭据。0 = 关闭', hintEn: 'Active = enabled and not reserved (quota-cooled still counts; banned ones get disabled). Below this, reserves are promoted. 0 = off' },
      { path: 'routing.reserve.count-cooled', type: 'bool', def: true, zh: '额度冷却计入可用', en: 'Count quota-cooled as available', hintZh: '可用 = 已启用、非储备、非错误/暂停、有令牌、体检未失败。开启时整体额度冷却中的也算可用（避免反复启用）；关闭则不算。默认开启', hintEn: 'Available = enabled, not reserved, not error/paused, has token, health check not failed. On: quota-cooled count (avoids churn); off: they do not. Default on' },
      { path: 'routing.reserve.ban-probe-interval', type: 'text', def: '12h', zh: '储备封号探测间隔', en: 'Reserve ban probe interval', hintZh: '每个储备凭据按此间隔经其固定出口发 1 个极小请求，发现 403 封号/违反条款即停用，不会被启用。0 = 关闭。默认 12h', hintEn: 'Each reserved credential sends one minimal request via its pinned egress at this interval; 403 bans get disabled and never promoted. 0 = off. Default 12h' },
      { path: 'routing.reserve.pre-promote-check', type: 'bool', def: true, zh: '启用前探测', en: 'Probe before promotion', hintZh: '自动启用前先探测候选（15 分钟内已探测则复用），已封号的停用并顺延下一个。默认开启', hintEn: 'Probe candidates before promotion (reuse a result <15m old); banned ones are disabled and the next is used. Default on' },
      { path: 'routing.reserve.activation-delay', type: 'text', def: '5m', zh: '启用延迟', en: 'Activation delay', hintZh: '不足状态持续多久后才启用储备（Go 时长）。默认 5m', hintEn: 'How long a shortfall must persist before promoting (Go duration). Default 5m' },
      { path: 'routing.reserve.max-promotions', type: 'number', def: 3, zh: '窗口内最多启用数', en: 'Max promotions per window', hintZh: '限速：每个窗口最多自动启用的储备数。默认 3', hintEn: 'Rate limit per window. Default 3' },
      { path: 'routing.reserve.promotion-window', type: 'text', def: '1h', zh: '限速窗口', en: 'Promotion window', hintZh: 'Go 时长，默认 1h', hintEn: 'Go duration, default 1h' },
      { path: 'routing.reserve.tier-filter', type: 'text', def: '', zh: '仅启用这些订阅', en: 'Tier filter', hintZh: '逗号分隔：ultra,pro,free,unknown；留空 = 不限。顺序：健康优先，其次最早导入', hintEn: 'Comma list: ultra,pro,free,unknown; empty = any. Order: healthy first, then oldest import' },
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
    zh: '订阅检测',
    en: 'Subscription tier',
    compact: true,
    fields: [
      { path: 'oauth.providers.antigravity.health.subscription-interval-minutes', type: 'number', def: 60, zh: '订阅等级检测间隔（分钟）', en: 'Tier check interval (min)', hintZh: '每个启用凭据按此间隔（±10% 抖动）经其绑定出口查询订阅等级。重新授权、启用、订阅额度 429、模型目录变化时会立即检测。默认 60。', hintEn: 'Each enabled credential re-reads its subscription tier at this interval (±10% jitter) via its pinned egress. Re-auth, enable, subscription-quota 429 and catalog changes trigger an immediate check. Default 60.' },
      { path: 'oauth.providers.antigravity.health.subscription-per-minute', type: 'number', def: 300, zh: '订阅检测每分钟上限', en: 'Tier checks per minute', hintZh: '全集群每分钟最多检测次数。1 万凭据每小时一次约需 170/分钟。默认 300。', hintEn: 'Cluster-wide cap. 10k credentials hourly need ~170/min. Default 300.' },
    ],
  },
  {
    zh: '凭据请求上限',
    en: 'Credential request caps',
    compact: true,
    fields: [
      { path: 'oauth.providers.antigravity.health.request-cap-hourly-free', type: 'number', def: 0, zh: 'Free 每小时', en: 'Free / hour' },
      { path: 'oauth.providers.antigravity.health.request-cap-daily-free', type: 'number', def: 0, zh: 'Free 每天', en: 'Free / day', hintZh: '0 = 不限制。', hintEn: '0 = unlimited.' },
      { path: 'oauth.providers.antigravity.health.request-cap-hourly-pro', type: 'number', def: 0, zh: 'Pro 每小时', en: 'Pro / hour' },
      { path: 'oauth.providers.antigravity.health.request-cap-daily-pro', type: 'number', def: 0, zh: 'Pro 每天', en: 'Pro / day', hintZh: '0 = 不限制。', hintEn: '0 = unlimited.' },
      { path: 'oauth.providers.antigravity.health.request-cap-hourly-ultra', type: 'number', def: 0, zh: 'Ultra 每小时', en: 'Ultra / hour' },
      { path: 'oauth.providers.antigravity.health.request-cap-daily-ultra', type: 'number', def: 0, zh: 'Ultra 每天', en: 'Ultra / day', hintZh: '0 = 不限制。', hintEn: '0 = unlimited.' },
      { path: 'oauth.providers.antigravity.health.request-cap-hourly-unknown', type: 'number', def: 0, zh: '未知 每小时', en: '未知 / hour' },
      { path: 'oauth.providers.antigravity.health.request-cap-daily-unknown', type: 'number', def: 0, zh: '未知 每天', en: '未知 / day', hintZh: '0 = 不限制。', hintEn: '0 = unlimited.' },
    ],
  },
  {
    zh: '慢凭据与首字超时',
    en: 'Slow credentials & TTFB',
    compact: true,
    fields: [
      { path: 'oauth.providers.antigravity.health.retry-empty-response', type: 'bool', def: true, zh: '空响应换凭据重试', en: 'Retry empty replies', hintZh: '上游成功但无可见输出（STOP 且只有空/思考内容，或无候选且无拦截原因）时，换一个凭据重试；流式与非流式均适用。MAX_TOKENS、SAFETY 等结束原因与拦截照常返回。默认开启。', hintEn: 'Retry on another credential when a successful upstream reply has no visible output (STOP with only empty/thought parts, or no candidates and no block reason). Stream and non-stream. MAX_TOKENS, SAFETY etc. pass through. Default on.' },
      { path: 'oauth.providers.antigravity.health.trailing-model-turn-text', type: 'text', def: 'Continue.', zh: '末尾模型轮补全文本', en: 'Trailing model-turn text', hintZh: '请求以模型（assistant 预填）轮结尾时，上游返回 400 "Requests ending with a model turn"；空文本用户轮会被上游丢弃仍报错。此处为追加的用户轮文本。填 off 则不追加。默认 Continue.', hintEn: 'Requests ending with a model (assistant prefill) turn get 400 "Requests ending with a model turn"; an empty-text user turn is dropped upstream and still fails. This text is appended as the user turn. "off" disables. Default "Continue."' },
      { path: 'oauth.providers.antigravity.health.drop-empty-parts', type: 'bool', def: true, zh: '移除空 part', en: 'Drop empty parts', hintZh: '移除不含任何数据字段的 part（如 {}、仅 thought:true、仅 thoughtSignature），以及因此变空的轮次，避免 400 "required oneof field data" / "Unsupported input part type"。默认开启。', hintEn: 'Remove parts with no data field ({}, thought:true only, lone thoughtSignature) and turns left empty, avoiding 400 "required oneof field data" / "Unsupported input part type". Default on.' },
      { path: 'oauth.providers.antigravity.health.repair-function-response-names', type: 'bool', def: true, zh: '修复工具结果名称', en: 'Repair tool-result names', hintZh: '客户端回传的 functionResponse.name 为空或为占位名（如 unknown_function）时，按 id（否则按顺序）改成对应 functionCall 的名称，避免 400 "functionResponse.name does not match functionCall.name"。正确的名称不改。默认开启。', hintEn: 'When functionResponse.name is empty or a placeholder (e.g. unknown_function), set it from the matching functionCall (by id, else by position), avoiding 400 "functionResponse.name does not match". Valid names are untouched. Default on.' },
      { path: 'oauth.providers.antigravity.health.builtin-tools-with-functions', type: 'select', options: ['emulate', 'keep', 'drop-builtin'], def: 'emulate', zh: '内置工具与函数混用', en: 'Built-in tools with functions', hintZh: '请求同时带 googleSearch/urlContext 与函数声明时，上游返回 400 "Please enable tool_config.include_server_side_tool_invocations"。emulate = 两段式：同一模型先只带搜索做一次非流式检索（仅在最后一轮是用户文本时），再只带函数请求并注入搜索结果与来源，附回 groundingMetadata，两次用量合计；检索失败则不带结果继续。keep = 原样透传（上游 400）；drop-builtin = 去掉内置工具（丢失联网搜索）。Anthropic 格式的 web_search 与函数同用时同样按此处理（emulate 下以 server_tool_use + web_search_tool_result 返回检索结果；drop-builtin 下去掉 web_search）。codeExecution 混用总是透传。默认 emulate。', hintEn: 'Requests mixing googleSearch/urlContext with function declarations get 400 "Please enable tool_config.include_server_side_tool_invocations". emulate = two passes on the same model: a non-stream search-only pass (only when the last turn is user text), then the functions-only request with findings and sources injected; groundingMetadata attached, usage of both passes summed; if search fails it continues without it. keep = pass through (upstream 400s); drop-builtin = drop built-in tools (loses web search). Anthropic web_search sent with function tools follows the same setting (emulate returns the search as server_tool_use + web_search_tool_result; drop-builtin drops web_search). codeExecution mixes always pass through. Default emulate.' },
      { path: 'oauth.providers.antigravity.health.builtin-presearch-timeout-seconds', type: 'number', adv: true, def: 20, zh: '两段式检索超时（秒）', en: 'Pre-search timeout (s)', hintZh: 'emulate 检索阶段的上限，同时不超过本次尝试首字/非流式超时的一半；超时则不带搜索结果继续。默认 20。', hintEn: 'Limit for the emulate search pass, also capped at half the attempt first-byte / non-stream limit; on timeout continue without search. Default 20.' },
      { path: 'oauth.providers.antigravity.health.clamp-thinking-level', type: 'bool', def: true, zh: '思考等级就近取值', en: 'Clamp thinking level', hintZh: 'Gemini 目标模型不支持请求的思考等级时（如 minimal），改用最接近的支持等级（minimal→low）而不是返回 400 "level not supported"。只改思考等级，不改模型。默认开启。', hintEn: 'If a Gemini target does not support the requested thinking level (e.g. minimal), use the nearest supported one (minimal→low) instead of 400 "level not supported". Only the level changes, never the model. Default on.' },
      { path: 'oauth.providers.antigravity.health.ttfb-timeout-seconds', type: 'number', def: 75, zh: '首字超时（秒）', en: 'TTFB timeout (s)', hintZh: '流式尝试无首字节超过此时间即中止并换凭据（客户端尚未收到任何数据）。默认 75。', hintEn: 'Streaming attempt with no first byte after this is aborted and retried on another credential (client has received nothing). Default 75.' },
      { path: 'oauth.providers.antigravity.health.ttfb-timeout-by-family', type: 'text', def: '3.8=15', zh: '按模型族首字超时', en: 'TTFB by family', hintZh: '如 3.8=15,2.5=60（3.8 含 tiered/high/medium/low，默认 15）。仅能比上一项更短；请求预算内最后一次尝试仍用上一项的长超时。off = 关闭。', hintEn: 'e.g. 3.8=15,2.5=60 (3.8 covers tiered/high/medium/low; default 15). Only shortens the timeout above; the last attempt allowed by the retry budget keeps the long one. off = disabled.' },
      { path: 'oauth.providers.antigravity.health.ttfb-extra-seconds-per-10k-tokens', type: 'number', def: 5, zh: '首字超时按输入放宽（秒/万token）', en: 'TTFB extra per 10k tokens (s)', hintZh: '按模型族首字超时每多 1 万输入 token（按请求体字节/4 估算）多给这么多秒，避免大上下文请求被误判卡顿。0 = 关闭。默认 5。', hintEn: 'Per-family first-byte limit grows by this many seconds per 10k input tokens (body bytes / 4). 0 = off. Default 5.' },
      { path: 'oauth.providers.antigravity.health.ttfb-scaled-max-seconds', type: 'number', def: 45, zh: '放宽后首字超时上限（秒）', en: 'Scaled TTFB max (s)', hintZh: '按输入放宽后的上限，且不超过首字超时。默认 45。', hintEn: 'Cap for the input-scaled limit, never above the TTFB timeout. Default 45.' },
      { path: 'oauth.providers.antigravity.health.ttfb-timeout-max-retries', type: 'number', def: 2, zh: '首字超时最多尝试次数', en: 'Max TTFB timeouts per request', hintZh: '同一请求累计首字超时达到此次数即停止换凭据，直接返回 504（其它错误照常重试）。0 = 不限。默认 2。', hintEn: 'Stop switching credentials after this many first-byte timeouts on one request and return 504 (other errors retry as before). 0 = unlimited. Default 2.' },
      { path: 'oauth.providers.antigravity.health.nonstream-timeout-by-family', type: 'text', def: '3.8=120', zh: '按模型族非流式超时', en: 'Non-stream timeout by family', hintZh: '非流式没有首字，按整次尝试计时，如 3.8=120。仅能比非流式总超时（默认 180）更短；超时换凭据，请求预算内最后一次尝试仍用长超时。off = 关闭。', hintEn: 'Non-stream has no first byte, so this bounds the whole attempt, e.g. 3.8=120. Only shortens the non-stream timeout (default 180); on timeout switch credential; the last budgeted attempt keeps the long one. off = disabled.' },
      { path: 'oauth.providers.antigravity.health.cluster.demote-ttfb-p95-seconds', type: 'number', def: 30, zh: '降权：首字 p95 >（秒）', en: 'Demote: TTFB p95 > (s)', hintZh: '(凭据, 模型族) 近期首字 p95 超过即降权到 25% 流量（不低于探测下限）。负数关闭。', hintEn: 'A (credential, family) pair whose recent TTFB p95 exceeds this gets 25% of traffic (>= probe floor). Negative = off.' },
      { path: 'oauth.providers.antigravity.health.cluster.demote-fail-percent', type: 'number', def: 40, zh: '降权：失败率 >（%）', en: 'Demote: fail rate > (%)', hintZh: '近期失败率（5xx/超时，不含 429）超过即降权。负数关闭。', hintEn: 'Recent failure rate (5xx/timeouts, not 429) above this demotes. Negative = off.' },
      { path: 'oauth.providers.antigravity.health.cluster.max-demoted-percent', type: 'number', def: 30, zh: '最多降权比例（%）', en: 'Max demoted share (%)', hintZh: '每个模型族最多降权这么多比例的凭据（最差的优先）。', hintEn: 'At most this share of a family\'s credentials is demoted (worst first).' },
      { path: 'oauth.providers.antigravity.health.cluster.probe-floor-percent', type: 'number', def: 15, zh: '探测下限（%）', en: 'Probe floor (%)', hintZh: '被降权凭据仍保留的最低流量，用于自动恢复。', hintEn: 'Minimum traffic a demoted pair keeps so it can recover.' },
      { path: 'oauth.providers.antigravity.health.cluster.upstream-fail-percent', type: 'number', adv: true, def: 50, zh: '整体故障判定：失败率 ≥（%）', en: 'Upstream-wide: fail ≥ (%)', hintZh: '模型族中位失败率达到此值视为上游整体问题，不降权任何凭据。', hintEn: 'Family median failure at or above this = upstream-wide; nobody is demoted.' },
      { path: 'oauth.providers.antigravity.health.cluster.upstream-ttfb-seconds', type: 'number', adv: true, def: 45, zh: '整体故障判定：首字 ≥（秒）', en: 'Upstream-wide: TTFB ≥ (s)', hintZh: '模型族中位首字达到此值视为上游整体变慢，不降权。', hintEn: 'Family median TTFB at or above this = upstream-wide; nobody is demoted.' },
      { path: 'oauth.providers.antigravity.health.cluster.stall-cooldown-seconds', type: 'number', def: 45, zh: '首字超时后冷却（秒）', en: 'Stall cooldown (s)', hintZh: '某凭据在某模型上首字超时一次后，该模型对该凭据冷却这么久（其它模型不受影响），重试换到别的凭据。建议 30-60，默认 45。', hintEn: 'After one first-byte timeout the credential is skipped for that model (other models unaffected) for this long. 30-60 sensible; default 45.' },
      { path: 'oauth.providers.antigravity.health.cluster.latency-aware-disabled', type: 'bool', def: false, zh: '关闭延迟感知调度', en: 'Disable latency-aware scheduling', hintZh: '默认开启：某凭据在某模型族上近期首字远高于健康基线（同族 p25）时只分配少量流量，流量优先给快凭据；没有快凭据可用时仍会用慢凭据。', hintEn: 'On by default: a credential whose recent TTFB for a model family is far above the healthy baseline (family p25) gets only a trickle; fast credentials get the rest. Slow ones are still used when no fast one is ready.' },
      { path: 'oauth.providers.antigravity.health.cluster.latency-slow-factor', type: 'number', def: 3, zh: '延迟感知：慢于基线倍数', en: 'Latency-aware: slow factor', hintZh: '首字 > 基线 × 倍数视为慢（最小 1.5）。', hintEn: 'TTFB > baseline × factor counts as slow (min 1.5).' },
      { path: 'oauth.providers.antigravity.health.cluster.latency-slow-seconds', type: 'number', def: 8, zh: '延迟感知：首字绝对阈值（秒）', en: 'Latency-aware: absolute TTFB (s)', hintZh: '基线较快时，首字超过此值也视为慢。负数关闭。', hintEn: 'While the baseline is fast, TTFB above this also counts as slow. Negative = off.' },
      { path: 'oauth.providers.antigravity.health.cluster.latency-trickle-percent', type: 'number', def: 10, zh: '延迟感知：慢凭据保留流量（%）', en: 'Latency-aware: slow trickle (%)', hintZh: '慢凭据保留的探测流量，用于恢复（2-100）。', hintEn: 'Probe share a slow credential keeps so it can recover (2-100).' },
      { path: 'oauth.providers.antigravity.health.cluster.latency-max-slow-percent', type: 'number', adv: true, def: 80, zh: '延迟感知：最多判慢比例（%）', en: 'Latency-aware: max slow share (%)', hintZh: '每个模型族最多这么多比例的凭据被判慢（最慢的优先）。', hintEn: 'At most this share of a family is treated as slow (slowest first).' },
      { path: 'oauth.providers.antigravity.health.cluster.slow-events', type: 'number', adv: true, def: 3, zh: '隔离：慢事件次数', en: 'Quarantine: slow events', hintZh: '窗口内首字超时/久等后客户端断开达到次数即隔离。', hintEn: 'TTFB timeouts / long-wait disconnects within the window before quarantine.' },
      { path: 'oauth.providers.antigravity.health.cluster.window-seconds', type: 'number', adv: true, def: 600, zh: '隔离：窗口（秒）', en: 'Quarantine: window (s)' },
      { path: 'oauth.providers.antigravity.health.cluster.quarantine-seconds', type: 'number', adv: true, def: 300, zh: '隔离：时长（秒）', en: 'Quarantine (s)', hintZh: '重复则翻倍，直到上限。', hintEn: 'Doubles on repeat up to the max.' },
      { path: 'oauth.providers.antigravity.health.cluster.quarantine-max-seconds', type: 'number', adv: true, def: 900, zh: '隔离：上限（秒）', en: 'Quarantine max (s)' },
      { path: 'oauth.providers.antigravity.health.cluster.slow-factor', type: 'number', adv: true, def: 2, zh: '相对慢：倍数', en: 'Relative slow: factor', hintZh: '首字超过同族中位数 × 倍数（且多出下项秒数）按比例降权。', hintEn: 'TTFB above family median × factor (and by the extra below) is weighted down proportionally.' },
      { path: 'oauth.providers.antigravity.health.cluster.slow-min-extra-seconds', type: 'number', adv: true, def: 5, zh: '相对慢：至少多（秒）', en: 'Relative slow: min extra (s)' },
      { path: 'oauth.providers.antigravity.health.cluster.fail-margin-percent', type: 'number', adv: true, def: 25, zh: '相对失败：高出中位（%）', en: 'Relative fail: margin (%)' },
      { path: 'oauth.providers.antigravity.health.cluster.min-peers', type: 'number', adv: true, def: 4, zh: '最少同族凭据数', en: 'Min peers', hintZh: '同族有样本的凭据少于此数时不做任何降权。', hintEn: 'Fewer sampled credentials in the family → no demotion at all.' },
    ],
  },
  {
    zh: '请求',
    en: 'Requests',
    fields: [
      { path: 'requests.proxy-url', type: 'text', zh: '全局出站代理', en: 'Global outbound proxy', hintZh: 'socks5:// 或 http://，留空为直连', hintEn: 'socks5:// or http://; empty = direct' },
      { path: 'requests.streaming.keepalive-seconds', type: 'number', zh: 'SSE 心跳间隔（秒）', en: 'SSE keepalive (s)' },
      { path: 'requests.streaming.bootstrap-retries', type: 'number', zh: '流式首字节前重试次数', en: 'Streaming bootstrap retries' },
      { path: 'requests.streaming.early-keepalive', type: 'bool', zh: '提前心跳（会阻止 new-api 切换渠道）', en: 'Early keepalive (blocks new-api channel failover)' },
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
      {SECTIONS.map((s) => {
        const renderField = (f: FieldDef) => {
          const v = values[f.path];
          const label = tr(f.zh, f.en);
          const hint = f.hintZh ? tr(f.hintZh, f.hintEn ?? '') : undefined;
          const input =
            f.type === 'select' ? (
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
            );
          if (s.compact) {
            return (
              <div className="field" key={f.path}>
                <label title={[hint, f.path].filter(Boolean).join('\n')}>
                  {label}
                  {hint && <span style={{ opacity: 0.5 }}> ⓘ</span>}
                </label>
                {input}
              </div>
            );
          }
          return (
            <div className="field" key={f.path}>
              {f.type === 'bool' ? (
                <Switch checked={!!v} onChange={(nv) => setValues({ ...values, [f.path]: nv })} label={<span style={f.danger ? { color: 'var(--err)' } : undefined}>{label}</span>} />
              ) : (
                <>
                  <label>{label}</label>
                  {input}
                </>
              )}
              {hint && <span className="hint hint-clamp" title={hint} tabIndex={0} onClick={(e) => e.currentTarget.classList.toggle("open")}>{hint}</span>}
              <span className="hint mono" style={{ opacity: 0.7 }}>
                {f.path}
              </span>
            </div>
          );
        };
        const main = s.fields.filter((f) => !f.adv);
        const adv = s.fields.filter((f) => f.adv);
        return (
          <Card key={s.en} title={tr(s.zh, s.en)}>
            <div className={s.compact ? 'form-grid form-grid-compact' : 'form-grid'}>{main.map(renderField)}</div>
            {adv.length > 0 && (
              <details style={{ marginTop: 10 }}>
                <summary className="hint" style={{ cursor: 'pointer' }}>
                  {tr('高级（隔离 / 相对判定 / 上游整体保护）', 'Advanced (quarantine / relative / upstream-wide guard)')}
                </summary>
                <div className="form-grid form-grid-compact" style={{ marginTop: 10 }}>
                  {adv.map(renderField)}
                </div>
              </details>
            )}
          </Card>
        );
      })}
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
      <label
        style={{ display: 'flex', gap: 6, alignItems: 'center', marginTop: 12 }}
        title={tr('停用的出口（id 如 node/ip，或 IP，逗号分隔）不会被任何新分配使用（导入、添加、自动均衡、手动指定）。不影响健康检查；已绑定的凭据不会被暂停或移动。', 'Disabled egresses (id like node/ip, or IP; comma-separated) are never used for new assignments (import, add, auto-balance, explicit). Health checks are unaffected; credentials already pinned are neither paused nor moved.')}
      >
        {tr('停用出口', 'Disabled egresses')}
        <input
          type="text"
          style={{ flex: 1, minWidth: 320 }}
          defaultValue={(st.data.disabled ?? []).join(', ')}
          disabled={saving}
          data-testid="settings-disabled-egresses"
          onBlur={async (e) => {
            const v = e.target.value.split(/[\s,]+/).map((x) => x.trim()).filter(Boolean);
            if (v.join(',') === (st.data?.disabled ?? []).join(',')) return;
            setSaving(true);
            try {
              await api.saveEgressSettings({ allow_primary: st.data!.allow_primary, max_per_egress: st.data?.max_per_egress ?? 0, disabled: v });
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
