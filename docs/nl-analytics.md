# Natural-language analytics ("Ask your mail logs")

MX Sentinel extends its local-LLM layer from incident explanation (`cmd/aid` + `internal/ai`)
to **conversational queries over telemetry**. An operator asks a question in plain English —
*"why did mail to Yahoo drop 40% Tuesday?"* — and the system answers using **only aggregate
metadata**.

- **Package:** `internal/nlquery`
- **API:** `POST /v1/ask`, `GET /v1/ask/history` (both `ScopeRead`)
- **UI:** `web/app/ask/page.tsx` (nav → Reporting → "Ask Logs")
- **Audit:** `nl_query_log` (Postgres, migration `00023_nl_analytics.sql`)

## Privacy design (the whole point)

The critical constraint is enforced **in code**, not just documented:

1. **No message bodies or subject lines — ever.** The subsystem has no code path that reads,
   stores, logs, or sends message content. This holds because MX Sentinel's privacy boundary
   already drops bodies at the telemetry parser (`internal/telemetry`); the NL layer only
   touches already-aggregated tables.

2. **Whitelist-only, tool/function-calling pattern — no free-form SQL.** The model may **not**
   write SQL. Instead it plans over a fixed registry of pre-defined, parameterized *aggregate*
   queries (`internal/nlquery/registry.go`, `Registry`). Each tool has a name, a description,
   and a strict parameter spec. The model's job in step 1 is only to choose which whitelisted
   tool(s) to call and with which validated arguments.

3. **Argument validation is a pure, tested security boundary.** `ValidateArgs` rejects:
   unknown argument keys (so a field like `body`/`subject` can never slip through — the
   whitelist simply never declares one), missing required args, enum values outside the
   allowed set, and unparseable ints/timestamps. `forbiddenParamNames` is a belt-and-braces
   deny-list (`body`, `subject`, `content`, `headers`, `html`, …). A unit test asserts no tool
   exposes such a param and no tool returns such a column.

4. **The model only sees aggregate rows.** After we execute the chosen whitelisted query
   against ClickHouse/Postgres, we feed the **aggregate result rows** (counts and rates) back
   to the model, which composes the natural-language answer. It never receives per-message
   rows.

## Flow

```
question ──▶ [plan]   LLM picks whitelisted tool(s)+args  ── ONLY tool names + params, no data
         ──▶ [validate] ValidateArgs (pure) rejects off-whitelist / forbidden / malformed
         ──▶ [execute] run query vs ClickHouse/Postgres (tenant-scoped)   ── aggregate rows only
         ──▶ [compose] LLM writes answer grounded in the aggregate rows   ── counts/rates only
         ──▶ {answer, used_queries, data}
```

Two LLM calls total (plan, then compose), both against the same OpenAI-compatible endpoint the
rest of the AI layer uses.

## Whitelisted queries

| Tool | Answers | Key params |
|---|---|---|
| `deliverability_by_provider` | delivery/deferral/bounce/reject counts + rate per receiving provider | `window` \| `since`/`until` |
| `rejection_reasons` | rejected/bounced messages grouped into normalized reasons (reputation, blocklist, auth, spam_content, rate_limit, greylist, policy, user_unknown) | `window`, `limit` |
| `top_senders` | top sending IP / SMTP user / sender-domain by volume, spam, or rejected | `dimension`*, `metric`*, `window`, `limit` |
| `dmarc_alignment` | total / DKIM-aligned / SPF-aligned counts + pass rates from DMARC aggregate reports | `domain`, `window` |

(* required.) Time is expressed as a relative `window` enum (`1h`/`24h`/`7d`/`30d`/`90d`) or an
explicit RFC3339 `since`/`until` range. Every query is scoped to the caller's `tenant_id`.

Adding a capability = adding a `Tool` to `Registry`. There is no other way to widen what the
model can reach.

## API

### `POST /v1/ask`  (ScopeRead)

Request:

```json
{ "question": "why did mail to Yahoo drop this week?" }
```

Response:

```json
{
  "answer": "Deliverability to Yahoo fell to 60% (60/100) this week versus 95% for Google...",
  "used_queries": ["deliverability_by_provider"],
  "data": [
    {
      "tool": "deliverability_by_provider",
      "label": "Deliverability by provider",
      "columns": ["provider","delivered","deferred","bounced","rejected","total","delivered_rate"],
      "rows": [ { "provider": "yahoo", "delivered": 60, "total": 100, "delivered_rate": 0.6, ... } ]
    }
  ]
}
```

Errors: `400 bad_request` (empty/oversize question), `502 ai_unavailable` (LLM unreachable or
the planner produced an off-whitelist / invalid plan).

### `GET /v1/ask/history?limit=N`  (ScopeRead)

Returns the tenant's recent questions, the whitelisted tools that ran, and the answers.

## Data model

`nl_query_log` (migration `migrations/postgres/00023_nl_analytics.sql`):

| column | notes |
|---|---|
| `id` | UUID PK |
| `tenant_id` | FK → `tenants(id)` ON DELETE CASCADE |
| `question` | operator's plain-English question |
| `chosen_tools` | JSONB: the whitelisted queries + args that ran |
| `answer` | composed natural-language answer |
| `created_at` | timestamptz |

The log stores **only** the question, the aggregate-query names/args, and the answer. It never
stores raw mail content (none is available to this subsystem).

## Configuration

Reuses the existing AI endpoint — configure the LLM in one place:

| Env | Default | Meaning |
|---|---|---|
| `MXS_AI_ENDPOINT` | `http://localhost:11434/v1` | OpenAI-compatible base URL (Ollama/vLLM) |
| `MXS_AI_MODEL` | `llama3` | model name |
| `MXS_AI_APIKEY` | — | optional bearer token |
| `MXS_AI_TIMEOUTSECS` | `60` | per-call timeout |
| `MXS_NLQUERY_MAX_TOOLS` | `3` | max whitelisted queries executed per question |

Loaded at request time via `nlquery.LoadConfig()`.

## Testing

`go test ./internal/nlquery/...` — pure, no live services:

- the registry is asserted **aggregate-only** (no body/subject param or column on any tool);
- `ValidateArgs` is table-tested: rejects unknown args, forbidden `body`/`subject` args,
  missing-required, bad enums, malformed times; normalizes/clamps valid ones;
- the full `Answer` flow runs against a **mock LLM** (interface) + a fake aggregate executor,
  including rejection of an off-whitelist tool and of a forbidden argument.
