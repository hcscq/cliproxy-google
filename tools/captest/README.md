# 本地容量测试工具（不用于生产）

- `mock/`：模拟 Gemini 上游（可配延迟、分块、429/503 比例）。
- `load/`：开环压测器（固定 RPS，流式/非流式，统计 TTFB/总延迟/状态码）。
- `net.sh up N`：创建 netns cap1..capN（10.201.0.11+）和网桥 brcap（宿主 10.201.0.1），proxy 在 netns 里运行。
- `start.sh HOMES PROXIES [mock 参数]`：启动 mock、Home、proxy；每个进程都设置了 GOMEMLIMIT。
- `step.py RPS DUR LABEL`：执行一轮压测并采集 CPU、内存和 PG 指标。
- `watchdog.sh`：可用内存低于 2GB 时立即停止所有测试进程（包括本地 PG）。

种子数据：在 `run/import` 下用 `home -import` 导入 2000 个指向 mock 的凭据和 10000 个 key。
