# cliproxy-google

基于 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) 和 [CLIProxyAPIHome](https://github.com/router-for-me/CLIProxyAPIHome) 的精简版本。这里只保留 **Google 系代理能力**，并把代理节点改造成**无状态**部署，适合在 k3s 上横向扩缩。

- 保留：Antigravity（OAuth）、Gemini API Key、AI Studio、Vertex（服务账号 / API Key），以及 OpenAI 兼容上游。
- 入站协议不变：OpenAI Chat/Responses、Claude Messages、Gemini 格式的客户端都可以访问 Google 后端（格式转换器全部保留）。
- 不包含 Gemini CLI OAuth。上游当前版本已经移除了该功能，本项目也不恢复。

## 架构

```
            Traefik Ingress
                  │
          Service cliproxy:8317
        ┌─────────┼─────────┐
   cliproxy-xxxxx × N (Deployment)         ← proxy/ （无状态，只读根文件系统，可任意扩缩）
        └──── mTLS (RESP, Service cliproxy-home:8327) ─┐
                                      ▼
              cliproxy-home-xxxxx × 2 (Deployment)       ← home/ （控制面，PG 选主）
                                      │
                                 PostgreSQL       ← 唯一的持久化状态
```

- **proxy/**（CLIProxyAPI）：在 Home 模式下运行。启动时用共享的 bootstrap token 向 Home 自助注册，拿到一个临时的 mTLS 身份（只存在内存盘里，Pod 删除即消失），配置、凭据、调度、冷却、配额、Token 刷新全部由 Home 下发和协调。节点本地不写 config 或 auth 文件，日志只输出到 stdout。
- **home/**（CLIProxyAPIHome）：凭据管理、调度和分发中心。所有状态都存在 PostgreSQL 中，多副本之间通过数据库做选主和事件同步。Management API 和 OAuth 登录（会话也存在数据库里）都由它提供。
- 状态分布：
  - PostgreSQL：配置、凭据、刷新结果、冷却和配额、用量。
  - PostgreSQL 还保存：集群 CA、各 Home 副本的服务端证书、代理节点的临时身份、选主租约。
  - k8s Secret：数据库密码、管理密钥、bootstrap token（不再有每个节点的身份 Secret）。
  - 代理 Pod 和 Home Pod：只有内存状态，可以随时删除、扩缩。

## 相对上游的改动

**删除的部分**（两个目录都删了）：Claude、Codex、Devin、Kimi、Meta、xAI 的以下内容：
- 执行器
- OAuth 登录（CLI 参数和 Management 接口）
- auth 包
- Token 刷新
- 回放缓存
- 相关测试

**保留的部分**：
- 配置结构里这些 provider 的字段仍然存在，但对应的凭据在绑定执行器时会被忽略。
- 格式转换器 `proxy/internal/translator/{claude,codex}` 以及相关的 helper 也保留了，因为 Claude/OpenAI 入站请求转到 Gemini 时会用到它们。

**proxy 新增的功能**：
- 默认只把日志输出到 stdout，不再写本地请求日志或错误日志文件。设置 `CLIPROXY_FILE_LOGS=true` 可以恢复上游行为。
- 新增 `GET /readyz`：Home 模式下，只有 Home 客户端已连接且心跳正常时才返回 200，否则返回 503。`/healthz` 保持不变，用于存活检查。
- `HOME_JWT_FILE`：从文件读取 Home JWT（便于挂载 Secret）。
- `HOME_CERT_DIR`：指定 mTLS 证书目录，默认是 `~/.cli-proxy-api`。证书文件如果已经是私有权限，就不再 chmod，所以目录可以只读挂载。
- `-home-enroll`：只完成证书注册，然后退出（旧的 JWT 方式，保留兼容）。
- **自助注册（无状态）**：设置 `HOME_BOOTSTRAP_TOKEN` + `HOME_ADDRESS`（`host:port`）后，代理每次启动都会生成新的私钥，用 RESP 命令 `CERTIFICATE BOOTSTRAP <token> <节点名> <CSR>` 向 Home 申请证书，证书只写入 `HOME_CERT_DIR`（建议用内存 emptyDir）。`HOME_CA_FINGERPRINT` 用于固定 Home CA，`HOME_NODE_NAME` 为显示名（默认主机名）。
- **启动重试**：连接 Home、注册、拉取配置失败时按指数退避（1s→30s）重试，不再退出；`HOME_STARTUP_MAX_WAIT` 可以设置上限。

**home 的改动**：
- 删除上述 provider 的 OAuth 和刷新逻辑。
- 通过 `replace` 指令直接引用仓库内的 `proxy/` 模块。
- 修复了一处上游的 vet 告警。
- `HOME_BOOTSTRAP_TOKEN`：开启 bootstrap 自助注册。每次注册都会生成一个新身份，在证书表中标记为 `ephemeral`。
- 临时身份回收：主节点在生命周期清理时，吊销超过 `HOME_EPHEMERAL_NODE_TTL`（默认 10m）没有心跳的临时身份（删除证书即吊销）。同时清理已经失联的 Home 副本行，以及它们的服务端证书。
- `HOME_NODE_EXTERNAL_IP`：覆盖 `cluster.yaml` 里的 `node.external-ip`，在 Deployment 中设置为 Pod IP。
- `HOME_SERVER_CERT_EXTRA_SANS`：给 Home 服务端证书加上额外的 SAN（Service DNS 名），代理通过 Service 访问任意副本都能通过 TLS 校验。

许可证：两个上游项目都是 MIT 协议，原 LICENSE 保留在 `proxy/LICENSE` 和 `home/LICENSE` 中，来源和上游提交见 `NOTICE`。

## 构建

需要 Go 1.26 或更高版本。

```bash
(cd proxy && go build ./... && go test ./...)
(cd home  && go build ./... && go test ./...)

# 镜像（构建上下文是仓库根目录）
docker build -f deploy/docker/proxy.Dockerfile -t ghcr.io/hcscq/cliproxy-google-proxy:latest .
docker build -f deploy/docker/home.Dockerfile  -t ghcr.io/hcscq/cliproxy-google-home:latest  .
```

Home 镜像内置本项目的 Web 管理面板（`web/`，构建时由 Dockerfile 生成并嵌入），无需额外步骤。

## 镜像（GitHub Actions / GHCR）

工作流在 `.github/workflows/ci.yml`。push 到 main、推送 `v*` 标签、提交 PR，或者手动触发（workflow_dispatch）时，都会对 proxy 和 home 跑 `go vet` 和 `go test`。已知有两个上游测试不稳定，失败时会对失败的包自动重跑一次。测试通过后，默认只构建 `linux/amd64` 镜像。如果需要 `linux/arm64`，可以在 Actions 页面手动运行（Run workflow）时勾选 `arm64` 选项，届时会通过 QEMU 额外构建 arm64 镜像，速度较慢。PR 只构建，不推送。

| 镜像 | 标签 |
| --- | --- |
| `ghcr.io/hcscq/cliproxy-google-proxy` | `sha-<完整提交SHA>`；main 分支额外打 `latest`；`vX.Y.Z` 标签对应 `X.Y.Z` 和 `X.Y` |
| `ghcr.io/hcscq/cliproxy-google-home` | 同上 |

生产环境建议固定使用 `sha-...` 或版本号标签，不要用 `latest`。

GHCR 新建的 package **默认是私有的**，k3s 拉取镜像有两种办法：

1. **改成公开**：在 GitHub 打开 Your profile → Packages → `cliproxy-google-proxy` / `cliproxy-google-home` → Package settings → Change visibility → Public。仓库改回私有后，package 仍然可以单独保持公开。
2. **使用 imagePullSecret**（package 保持私有）：先创建一个只有 `read:packages` 权限的 PAT（classic），然后执行：
   ```bash
   kubectl -n cliproxy create secret docker-registry ghcr-pull \
     --docker-server=ghcr.io --docker-username=hcscq --docker-password=<PAT>
   kubectl -n cliproxy patch serviceaccount default \
     -p '{"imagePullSecrets":[{"name":"ghcr-pull"}]}'
   ```
   给 default ServiceAccount 打补丁后，命名空间内所有 Pod（包括 Job 和注册用的临时 Pod）都会自动使用这个凭据。也可以在各个 Pod spec 里单独添加 `imagePullSecrets`。

## k3s 部署

清单在 `deploy/k3s/` 下，镜像名需要改成你自己的仓库。

1. 创建命名空间和数据库：
   ```bash
   kubectl apply -f deploy/k3s/00-namespace.yaml
   cp deploy/k3s/20-secrets.example.yaml deploy/k3s/secrets.yaml   # 替换所有 CHANGE_ME，不要提交
   kubectl apply -f deploy/k3s/secrets.yaml
   kubectl apply -f deploy/k3s/10-postgres.yaml   # 使用外部 PG 时跳过，并修改 postgres_uri
   ```
2. 初始化 Home 数据库：Home 在数据库为空时会拒绝启动，所以要先导入一次。
   ```bash
   kubectl apply -f deploy/k3s/30-home-bootstrap.yaml
   kubectl -n cliproxy wait --for=condition=complete job/home-import --timeout=300s
   ```
3. 启动 Home（2 副本，Deployment）：
   ```bash
   kubectl apply -f deploy/k3s/40-home.yaml
   ```
   每个副本以 Pod IP 作为对外地址，服务端证书由 PG 中的集群 CA 签发，SAN 包含 Pod IP 和 `cliproxy-home` Service 的各个 DNS 名。
4. 启动代理节点和入口（不需要任何注册脚本）。`60-ingress.yaml` 只对公网暴露 Home 管理面板（`/`、`/index.html`、`/management.html`、`/assets/*`、`/v8/management/*`（包含 OAuth 回调）），**不暴露代理 API**：
   ```bash
   kubectl apply -f deploy/k3s/50-proxy.yaml -f deploy/k3s/60-ingress.yaml
   kubectl -n cliproxy scale deploy/cliproxy --replicas=5   # 随意扩缩
   kubectl apply -f deploy/k3s/55-proxy-hpa.yaml            # 可选：HPA
   ```
   可选：固定 Home CA。CA 首次启动时生成，可以从任意一个代理的证书目录读取后计算指纹，写进 `home-bootstrap` Secret 的 `ca-fingerprint`：
   ```bash
   kubectl -n cliproxy exec deploy/cliproxy -- cat /identity/home-ca-crt.pem \
     | openssl x509 -outform der | sha256sum
   ```
5. 添加账号和 Key：可以 `kubectl -n cliproxy port-forward svc/cliproxy-home 8327`，也可以走 Home 的 Ingress，然后调用 Home Management API：
   - Gemini / Vertex API Key 写入配置；
   - Antigravity 通过 `GET /v8/management/oauth/auth-url?provider=antigravity` 发起 OAuth 登录。

   回调会落到 Home，会话存在 PostgreSQL 中，所以由哪个副本处理都可以。

### 访问方式

- **代理 API（只在集群内部）**：`http://cliproxy.<命名空间>.svc.cluster.local:8317/v1`（本部署为 `http://cliproxy.cliproxy-google.svc.cluster.local:8317/v1`）。请求头 `Authorization: Bearer <API Key>`，API Key 在面板的 “Access keys” 页面，或者通过 `POST /v0/management/access/api-keys` 管理。
- **管理面板（公网）**：`https://<面板域名>/`，用管理密钥（Secret `home-management` 的 `password`，即 `MANAGEMENT_PASSWORD`）登录。
  - 所有 Management API 请求都必须带管理密钥；
  - 从集群外访问需要开启 `management.allow-remote: true`；
  - 同一个 IP 连续输错 5 次会被封禁 30 分钟；
  - Ingress 上还加了 Traefik `rateLimit`。
  - k3s 自带的 Traefik 不信任客户端自己带的 `X-Forwarded-For`，所以封禁按真实客户端 IP 生效。
- **管理面板**：`web/` 是本项目自己的面板（Vite + React + TypeScript，中文界面，可切换英文，支持深色模式和手机屏幕）。CI 会构建它，然后嵌入 Home 镜像，由 Home 在 `/` 提供。功能：
  - 总览：集群健康、主节点、凭据额度、24 小时趋势、最近失败请求
  - 凭据：Antigravity OAuth 登录、上传或粘贴 JSON、Vertex 服务账号导入；启用/禁用、刷新令牌、查询额度、清除冷却；编辑优先级、前缀、代理、重试和冷却；查看模型、下载、删除（删除需要二次确认）
  - Gemini / Vertex API Key 分组：可以用表单或 JSON 编辑，每个 Key 可设置权重
  - 模型：可用模型（包括 Antigravity 动态目录）、排除模型、别名/扩展模型、内置目录
  - 访问密钥：创建、重命名、删除
  - 用量与请求：趋势、排行、请求明细和 CSV 导出
  - 日志：实时刷新、过滤、清空
  - 节点：Home 副本和代理的临时身份
  - 设置：常用设置表单，以及完整 YAML 编辑
  - 本地开发：`cd web && npm ci && HOME_URL=http://127.0.0.1:8327 npm run dev`
  - 测试：`npm test`（API 客户端单元测试）、`npx playwright test`（e2e，需要一个运行中的 Home，见 CI 的 `web` job）

### 扩缩容和共享状态

- 代理和 Home 都是 Deployment。扩缩容直接用 `kubectl scale` 或 HPA，不需要预先注册。
- 被删除的代理 Pod 的身份，会在 TTL 到期后被 Home 自动吊销并清理。
- 共享运行时状态全部放在 PostgreSQL，由 Home 维护：
  - 凭据状态、冷却（cooldown）、配额信号：以 PG 中的 auth 行为准，多个 Home 通过原子更新同步；
  - 并发准入：PG 表；
  - 选主：PG 行锁和心跳。
- 代理节点不做本地调度：选哪个凭据由 Home 决定，所以副本之间不需要共享轮询状态。
- 每个 Home 副本内部的轮询游标（round-robin）保存在内存里。只有在两个 Home 同时服务时，分配顺序才可能略有不同，凭据的可用性不受影响。
- 当前没有引入 Redis：已有的 PG 存储已经覆盖上面的需求。
- 加副本不会增加上游配额（配额按账号计算）。
- `/readyz` 失败只会把节点移出 Service，不会重启 Pod。只有 `/healthz` 失败才会重启。

## Antigravity 动态模型列表

上游 Home 只使用静态的 `models.json` 目录，账号实际可用、但不在目录里的模型不会被暴露。本项目的 Home 会用每个 Antigravity 凭据调用上游的 `v1internal:fetchAvailableModels`（每次刷新每个凭据 1 次请求）：

- **定时**：只有 Home 主节点（leader）按 `routing.antigravity-catalog-refresh` 定时获取（默认 `15m`，最小 `1m`，`off` 关闭定时），失败后 5 分钟内不重试；结果连同原始目录写入数据库表 `antigravity_catalogs`，所有副本每 30 秒读取并应用，路由与 `/v1/models` 在各副本保持一致；
- **事件触发**：某凭据对某模型返回 404（含已下线模型）或 400 时，立即补刷该凭据的目录（每凭据每分钟最多一次，跨副本以数据库中的最近尝试时间去重）；
- **手动**：面板 模型 → 模型状态 → “上游模型目录” 可立即刷新全部或单个凭据，并显示每个凭据的最近成功/尝试时间、触发方式、执行副本和失败原因；
- 从目录消失的模型在 7 天内标记为“上游已移除”（新增的模型同样记录），并立即从该凭据的可用模型与路由中移除。

合并规则：

- 静态目录里、且账号可用的模型：保留原有的详细元数据；
- 只在上游出现的模型：根据上游返回的 displayName 和 token 上限自动生成条目；
- `HOME_ANTIGRAVITY_EXTRA_MODELS=a,b`（环境变量）或凭据元数据 `home_config_models`：按配置追加模型；
- `oauth-model-alias`（原有配置）：在以上结果之上添加别名。

设置 `HOME_ANTIGRAVITY_DYNAMIC_MODELS=false` 可以退回只用静态目录。

## 分组与权限（凭据分组 / 模型分组）

面板“分组与权限”页面管理两类分组，在“访问密钥”的编辑对话框里绑定到密钥：

- **凭据分组**：把上游凭据分组（一个凭据可属于多个分组）。绑定了凭据分组的密钥只会使用这些分组内的凭据，**没有回退**——分组内凭据全部冷却 / 额度耗尽 / 禁用时请求直接失败（503 / 429）。页面实时显示每个分组“当前可用 x/y”。未绑定分组的密钥可使用全部凭据。
- **模型分组**：限定密钥可调用的模型（所绑定分组中模型的并集）。`/v1/models`、Claude 模型列表和 Gemini `/v1beta/models` 只列出允许的模型；请求其他模型返回 403，错误码 `model_not_allowed`（按各协议的错误格式）。可为分组中的单个模型再限定只由某些凭据分组服务（与密钥自身绑定的凭据分组取交集）。未绑定模型分组的密钥可调用全部模型。
- 停用分组等同于该分组不授予任何东西；删除分组时面板会先把它从已绑定的密钥上解绑，避免密钥被意外锁死。
- 凭据所属分组也可在“凭据”页的凭据详情里直接勾选。

### 大规模分组 / 批量操作（数百凭据、上千密钥）

- **服务端搜索与分页**：凭据页、访问密钥页、分组成员抽屉均为服务端筛选 + 分页（提供方、状态、邮箱域名、标签、所在分组、模型可用性），附各筛选项计数；“选择全部筛选结果”可跨页选择。
- **批量操作**：凭据页多选 → 加入/移出分组、加/去标签、启用/禁用；密钥页多选 → 批量绑定/解绑凭据分组与模型分组。所有批量接口均在单个事务内完成。
- **凭据标签**：凭据详情可编辑标签（存于凭据元数据 `tags`），可用于筛选和规则。
- **规则成员（动态）**：凭据分组可配置规则（提供方 / 邮箱通配符 / 名称通配符 / 含任一或全部标签 / 优先级范围），新增凭据满足即自动加入；编辑时实时预览命中数量。规则成员与手动成员取并集，约 2 秒内在所有 Home 副本生效。
- **导入 / 导出**：分组页可导出 JSON / CSV，导入支持合并或替换、并提供“试运行”预览。
- **分组可用性**：分组列表的“当前可用 x/y”由服务端汇总计算。
- **调度缓存**：每个 Home 副本在内存中维护密钥→分组→凭据/模型的快照，每次请求不再查库；通过后台轮询 PostgreSQL 中相关表的指纹（约 2s）与本副本写操作后的即时失效保持一致，无需 Redis，副本保持无状态。

基准（本地合成数据：500 凭据、200 模型、1000 密钥、50 凭据分组、50 模型分组；`go run ./home/cmd/scopebench`）：

| 指标 | SQLite | PostgreSQL 17 |
| --- | --- | --- |
| 调度范围查询（缓存）p50 / p99 | 4.3µs / 34µs | 4.0µs / 31µs |
| 调度范围查询（直接查库）p50 / p99 | 0.33ms / 1.23ms | 0.40ms / 1.46ms |
| 快照重建（1000 密钥、含规则） | 32ms | 16ms |
| 后台变更检测（无变化）p50 | 0.67ms | 0.45ms |

面板接口（合成环境，p95）：凭据搜索 ≤18ms，分组汇总 21ms，成员分页 14ms，密钥搜索 ≤14ms，导出 3ms，均远低于 300ms 目标。

## 已知限制

- Home 里还保留了部分非 Google 的模型目录逻辑（例如 Devin 和其他 provider 的模型列表刷新），以及 API Key 配置项。这些不影响运行，以后的阶段再清理。
- 删除的测试只包括明确针对已删除 provider 的用例。

## 上游容量不足重试

Antigravity / Gemini 在模型容量不足时会返回 503 `UNAVAILABLE` / `MODEL_CAPACITY_EXHAUSTED`（或带容量原因的 429）。这类错误和账号额度无关，处理方式是：

1. 本轮先换其他可用凭据（容量错误不会让凭据进入冷却）；
2. 所有凭据都失败后，按指数退避 + ±20% 抖动等待（默认 1s → 2s → 4s，单次不超过 8s；上游 `RetryInfo.retryDelay` 会被采纳但同样不超过上限），再用**同一个模型**重试，默认最多 3 轮；
3. 流式请求只在还没有向客户端发送任何数据前重试；
4. **永远不会更换请求的模型**（没有模型降级/回退链）。

配置位置：`routing.retry.capacity-retry`（`enabled` / `max-attempts` / `base-delay` / `max-delay`），保存在 Home 配置中，所有代理共享；面板“设置 → 重试与冷却”中也可以修改。账号额度类 429（`QUOTA_EXHAUSTED` / `RATE_LIMIT_EXCEEDED`）仍走原有的冷却和凭据轮换。

## 请求记录中的错误响应体

Home 会把上游错误响应体完整保存到请求记录（`usage.fail_body`），上限由 `observability.logs.error-body-max-bytes` 控制（默认 65536 字节，面板“设置 → 日志与管理”可改）。超过上限时只保存前面部分，并记录 `fail_body_truncated=true` 和原始大小 `fail_body_size`，面板会显示“已截断（原始 N 字节）”。新列在 Home 启动时自动添加（PostgreSQL 和 SQLite 都支持，可重复执行）。

## 请求记录存储、保留期与写入（容量阶段 1）

- **按天分区**：PostgreSQL 下请求记录表 `usage` 按 `timestamp` 做 RANGE 分区，每天一个分区 `usage_pYYYYMMDD`，另有默认分区 `usage_p_default` 接收范围外的数据（例如导入的历史）。主键为 `(id, timestamp)`，`id` 序列不变。
- **保留期**：`observability.logs.request-log-retention-days`（默认 30 天，`0` = 默认，负数 = 永久保留），面板“设置 → 日志与管理”可改。Home 每小时执行一次维护（所有副本都会尝试，用 PG advisory lock 保证同一时刻只有一个副本执行）：提前创建未来 3 天的分区；把落在默认分区里、但已有对应日期分区的数据移过去；整块删除过期分区（不产生表膨胀）。SQLite 下改为分批 `DELETE`。计费流水 `billing_charge` 不受保留期影响。
- **面板**：“设置”页底部的“请求记录存储”卡片显示存储方式、保留期、行数和占用、最早记录、各分区大小，以及上次维护的时间、结果和错误。
- **迁移（schema v7）**：已有的普通表会在 Home 启动时的迁移事务里自动转换。步骤：锁表，把旧表改名为 `usage_legacy`，创建分区表，复制数据并核对行数。旧表 `usage_legacy` 作为备份保留，确认无误后可手动执行 `DROP TABLE usage_legacy;` 删除。迁移由全局迁移锁串行执行，其他副本会等待。本地用 53.7 万行（1.8 GB）演练过，耗时见 `/workspace/capacity-report.md`。
- **批量写入**：Home 收到的用量事件进入队列，单个写线程每批最多 500 条、最多等待 100ms，用一个事务写入用量行和计费记录。配额观测按凭据合并，只写每个凭据的最新一条。批量事务失败时会逐条重试，坏数据不会连累同批的其他记录。进程退出时会先把队列写完（最多 20 秒），滚动更新不会丢失请求记录。
- **专用心跳连接**：选主和实例心跳使用独立的小连接池（`HOME_PG_LEASE_CONNS`，默认 3），不和请求路径争连接。心跳单次失败只记录警告并重试；只有被 fence，或连续失败超过心跳超时时间，才会退出。主连接池大小可用 `HOME_PG_MAX_OPEN` / `HOME_PG_MAX_IDLE` 调整（默认 40）。

## 输出长度上限（max_tokens）

默认会把客户端设置的输出上限传给上游 Gemini 模型（Antigravity / AI Studio 路径）。支持的参数有：OpenAI 的 `max_tokens` / `max_completion_tokens`、Claude 的 `max_tokens`、Gemini 的 `generationConfig.maxOutputTokens`。旧版本（继承自上游 CLIProxyAPI）会直接删掉这个参数，原因是 Gemini 的思考 token 也计入 maxOutputTokens，上限设得小时，可能整段都花在思考上，最后没有正文输出。

现在的行为：
- 客户端没设上限，就不传上限。
- 客户端设了上限，原样传给上游；如果固定的 `thinkingBudget` ≥ 上限，会自动降到上限的一半（不低于模型允许的最小值；模型允许关闭思考时可降到 0）。
- `thinkingLevel` 和动态预算（-1）不做调整，思考 token 仍然计入上限。
- 被截断时，各协议返回 `finish_reason: "length"`（OpenAI）、`stop_reason: "max_tokens"`（Claude）、`finishReason: "MAX_TOKENS"`（Gemini）。

开关：`routing.honor-max-tokens`（默认 true，面板“设置”页可改）。设为 false 恢复旧行为。

## 模型状态与上游限制

管理面板“模型 → 模型状态”按模型汇总真实状态。数据来源：上游模型目录（弃用 / 即将下线 / 每账号额度）、各凭据的冷却与最近错误、手动“重新测试”的结果。重新测试会用每个已启用的 Antigravity 凭据各发一个极小请求，结果存在 PostgreSQL 的 `model_health_checks` 表，所有 Home 副本共享。

- **状态**：可用 / 部分冷却 / 冷却中 / 上游拒绝 / 已下线（上游）/ 未知，并附中文原因和最近错误。
- **不会自动隐藏任何模型**：“显示”开关写入 `oauth-excluded-models.antigravity`，可随时取消；已隐藏的模型也会列出。
- **已下线模型**：例如 gemini-3-flash-agent、gemini-3.5-flash-low、gemini-3.5-flash-extra-low。上游对它们仍返回 200，正文是 “… is no longer available. Please switch to …”。代理识别到这种回复后，改为按协议格式返回 404 错误（`MODEL_RETIRED` / `model_not_supported`），不再把这段文字当成正常回答，并且只冷却该（凭据, 模型）。
- **上游弃用**：上游目录的 `deprecatedModelIds` 标为弃用的模型（例如 gemini-3.1-pro-high → gemini-pro-agent）会显示为“上游拒绝”并给出建议替代。这类模型对任何请求都返回 400 INVALID_ARGUMENT。
- **Gemini 3.x 流式思考**：上游流式只在思考较长时才返回思考摘要，思考签名在正文之后才到；非流式总是完整。`routing.stream-thinking-emulation: true`（面板“设置”中可开，默认关闭）会让请求思考内容的流式请求改走上游非流式接口，再按流式格式一次性返回。
