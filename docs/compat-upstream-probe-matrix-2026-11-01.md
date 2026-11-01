# Antigravity upstream compat probe matrix (2026-11-01 Asia/Shanghai)

Probes: Home `POST /requests/api-call` (raw upstream, bypasses CPA schema cleaning) for Gemini; in-cluster CPA `POST /v1/messages` for Claude. Flash/cheap models; Claude sonnet-5.5-high sparsely. Argon api-call returned 404 NOT_FOUND on tested ids (CPA path still serves argon) — those rows excluded.

## Tool parameters

| case | gemini-2.5 | gemini-3.8 |
|---|---|---|
| `baseline_object` | accepted | accepted |
| `loaderExpression` | rejected | rejected |
| `empty_enum` | accepted | accepted |
| `const` | rejected | rejected |
| `const_string_typed` | rejected | rejected |
| `anyOf` | accepted | accepted |
| `oneOf` | accepted | accepted |
| `allOf` | accepted | accepted |
| `additionalProperties_false` | accepted | accepted |
| `additionalProperties_schema` | accepted | accepted |
| `format_email` | accepted | accepted |
| `format_uri` | accepted | accepted |
| `format_date_time` | accepted | accepted |
| `nullable_type_array` | rejected | rejected |
| `examples` | rejected | rejected |
| `default` | accepted | accepted |
| `title` | accepted | accepted |
| `pattern` | accepted | accepted |
| `minLength_maxLength` | accepted | accepted |
| `minimum_maximum` | accepted | accepted |
| `x_unknown_key` | rejected | rejected |
| `ref_defs` | rejected | rejected |

### Rejected — rewrite justified
- `loaderExpression`, `const`, `type: [string,null]`, `examples`, unknown `x-*`, `$defs`/`$ref`

### Accepted — do not strip solely for compat
- empty `enum`, `anyOf`/`oneOf`/`allOf`, `additionalProperties`, `format`, `default`, `title`, `pattern`, min/max length/number

## Response schema

| case | gemini-2.5 | gemini-3.8 |
|---|---|---|
| `resp_baseline` | accepted | accepted |
| `resp_loaderExpression` | rejected | rejected |
| `resp_empty_enum` | accepted | accepted |
| `resp_const` | rejected | rejected |
| `resp_anyOf` | accepted | accepted |
| `resp_additionalProperties_false` | accepted | accepted |
| `resp_x_unknown` | rejected | rejected |

## Function pairing (gemini-3.8 + thoughtSignature)

| case | verdict | implication |
|---|---|---|
| id-paired name mismatch | rejected | rewrite when id pairs |
| id-paired empty/`unknown_function` | rejected | rewrite |
| no-id real-name mismatch | accepted | do **not** position-rewrite arbitrary names |
| empty name (no id) | rejected | rewrite empty by position |
| reordered ids / missing response ids / no ids (matching names) | accepted | |

| `match_ids_names` | accepted |  |
| `mismatch_names_ids_ok` | rejected | Request contains an invalid argument. |
| `reordered_ids_match` | accepted |  |
| `missing_response_ids` | accepted |  |
| `no_call_ids` | accepted |  |
| `same_name_reordered` | accepted |  |
| `mismatch_without_ids` | accepted |  |

## Claude thinking

CPA already drops many locally-invalid signatures before upstream (garbage/missing → 200 in-probe). Production still hit `Invalid signature in thinking block` for well-formed foreign/bound signatures — keep strip-on-400 retry. Dropping thinking (keep text) suffices.

| case | status |
|---|---|
| `same_cred_replay_continue` | 200 |
| `modified_thinking_text` | 200 |
| `missing_signature` | 200 |
| `empty_signature` | 200 |
| `garbage_signature` | 200 |
| `drop_thinking_keep_text` | 200 |
| `cross_model_to_opus_low` | 200 |

## Defaults after this matrix
- repair-function-response-names: **on** (narrowed); modes off/observe/on
- schema-sanitize-mode: **on** (keep empty enum; still drop proven-reject keys); modes off/observe/on
- retry-strip-invalid-thinking-on-400: **on**; modes off/observe/on
- context-token-precheck: **on** with clear-over margin; modes off/observe/on
