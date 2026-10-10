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

上游 Home 只使用静态的 `models.json` 目录，账号实际可用、但不在目录里的模型不会被暴露。本项目的 Home 会用每个 Antigravity 凭据调用上游的 `v1internal:fetchAvailableModels`，每 3 小时刷新一次，失败时 5 分钟后重试。合并规则：

- 静态目录里、且账号可用的模型：保留原有的详细元数据；
- 只在上游出现的模型：根据上游返回的 displayName 和 token 上限自动生成条目；
- `HOME_ANTIGRAVITY_EXTRA_MODELS=a,b`（环境变量）或凭据元数据 `home_config_models`：按配置追加模型；
- `oauth-model-alias`（原有配置）：在以上结果之上添加别名。

设置 `HOME_ANTIGRAVITY_DYNAMIC_MODELS=false` 可以退回只用静态目录。

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
