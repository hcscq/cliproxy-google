# Claude upstream 500 Unknown Error probe matrix (2026-11-02 Asia/Shanghai)

## Incident shape (error_sample 14552–14558)

- Client: `POST /v1/messages` (Anthropic-Version), model `claude-opus-5-5` → upstream `claude-opus-5-5-high`
- ~146k approx input tokens, 53 messages, `has_tools=true`
- Client roles include **OpenAI-hybrid** `system` / **`tool`** / consecutive `assistant` (summarizer from full body; excerpts truncated ~8KB)
- Client content mostly **string** (`block_types.text=48`); no `tool_use`/`tool_result` blocks in excerpts
- Upstream body ~2.5MB after CPA Claude→Antigravity conversion; opaque Google `{code:500,message:Unknown Error.,status:UNKNOWN}`

Samples alone cannot full-replay (excerpt cap). Structure synthesized for ablation. No prod log widening.

## CPA conversion artifact (local `ConvertClaudeRequestToAntigravity`)

| client message | Gemini `contents.role` / parts |
|---|---|
| `role=system` string | becomes **user** reminder text (merged with adjacent user); not `systemInstruction` unless top-level `system` |
| `role=assistant` string | `model` + text |
| **`role=tool` string** | **`tool` + text** (INVALID for Antigravity/Gemini) |
| Anthropic `user`+`tool_result` after `assistant`+`tool_use` | `user` + `functionResponse` (valid) |
| consecutive `assistant` | consecutive `model` (accepted upstream) |

OpenAI→Antigravity path already maps `role=tool` → `user`+`functionResponse` when paired with `tool_calls`. Claude→Antigravity path does **not**.

## Upstream ablation (Home `POST /requests/api-call`, synthetic payloads, no user plaintext)

Auth: healthy Claude-capable credential; spaced calls. Prefer sonnet-high then opus-high.

### gemini-2.5-flash (shape check)

| case | status | note |
|---|---|---|
| baseline user | 200 | |
| **role=tool + text** | **400** INVALID_ARGUMENT | `Role 'tool' is not supported...` |
| proper functionCall/Response | 200 | |
| consecutive model | 200 | |
| tool rewritten as user text | 200 | |

### claude-sonnet-5-5-high

| case | status | note |
|---|---|---|
| baseline | 200 | |
| **role=tool + text** | **500 Unknown Error** | minimal trigger |
| role=tool empty text | **500 Unknown Error** | |
| role=tool without `tools` field | **500 Unknown Error** | |
| parallel consecutive role=tool | **500 Unknown Error** | |
| consecutive model (no tool role) | 200 | not the trigger |
| ~50k char context, no tool role | 200 | length alone not sufficient |
| ~40 alternating msgs, no tool role | 200 | |
| **tool→user text rewrite** | **200** | content preserved |
| functionCall+functionResponse **with id** | 200 | |
| functionCall without id | 400 | `tool_use.id: Field required` |

### claude-opus-5-5-high (confirm)

| case | status |
|---|---|
| baseline | 200 |
| **role=tool + text** | **500 Unknown Error** |
| **tool→user text rewrite** | **200** |

## Minimal trigger

**确定：** Claude→Antigravity 转换把客户端 OpenAI 风格的 `messages[].role="tool"` 原样写成 Gemini `contents[].role="tool"`；Antigravity Claude 上游对此返回 **500 Unknown Error**（Gemini 同形态为明确 400）。

非触发（本矩阵）：连续 `model`、中等加长上下文、多轮无 tool role、仅有 tools 定义。

## Lossless-leaning compat (proposal only — not deployed)

1. **Preferred when pairable:** If prior assistant/`model` turn has `tool_use` / `functionCall` (or OpenAI `tool_calls`), map each `role=tool` to `user` + `functionResponse` (same as OpenAI→AG translator; keep `tool_call_id`→id/name). Fully equivalent.
2. **Fallback when unpaired** (this incident: assistant strings only, no `tool_use`): rewrite `role=tool` → `role=user` keeping text/parts. **Content preserved**; role semantics become user-visible tool output (same as accepted probe `*_tool_as_user_text`). Prefer over dropping.
3. Do **not** silent-drop tool payloads unless a future probe shows rewrite still rejected.
4. Optional: accept OpenAI-hybrid on `/v1/messages` by normalizing to Anthropic `tool_result` before convert.

Verified: rewrite (2) → **200** on sonnet-high and opus-high.

## Out of scope this round

cache_control≤4, thinking budget/high mapping, stop_sequences, schema const/enum — not required once tool-role reproduced 500 at minimal size.


## Config (deployed)

- `antigravity.health.rewrite-tool-role-mode`: **on** (default) / `observe` / `off`
