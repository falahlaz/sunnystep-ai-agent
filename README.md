# Footwear Support Agent (prototype)

Customer-support agent for a fictional footwear store ("Stridewell"). All
products, policies, customers and orders are invented.

> **Status:** control layer, orchestrator, CLI/HTTP interface and
> observability done and tested with a fake model. Eval harness is next.

## Run

Requires Go 1.24+.

```sh
go test ./...                      # all unit tests use a fake model

export ANTHROPIC_API_KEY=...       # real model (default claude-sonnet-5-5, override with LLM_MODEL)
go run ./cmd/agent chat            # terminal chat; turn logs go to var/chat.log
go run ./cmd/agent serve --addr :8080

LLM_PROVIDER=fake go run ./cmd/agent chat   # offline keyword rules, no API key
```

HTTP:

```sh
curl -s -XPOST localhost:8080/chat -d '{"session_id":"s1","message":"is shipping free?"}'
curl -s localhost:8080/healthz
curl -s localhost:8080/debug/vars  # counters
```

Flags: `--kb data/kb`, `--orders data/orders.json`, `--tickets var/tickets.jsonl`,
`--log var/chat.log` (chat only; `serve` logs to stdout). Startup fails if the
KB or orders file does not validate.

## Layout

```
cmd/agent            chat REPL and HTTP server
data/kb/*.md         knowledge base; each "## " heading is one citable chunk
data/orders.json     fake orders (SS-10007 carries a prompt-injection payload)
internal/agent       one customer turn end to end (PRD §3)
internal/classify    model intent label + hints, validated, one retry
internal/llm         Client interface, Claude API client, scripted fake
internal/offline     keyword-rule stand-in for the model (LLM_PROVIDER=fake)
internal/kb          BM25 retrieval over KB sections
internal/orders      verified order lookup, model-safe View, date math
internal/policy      intent -> route + per-turn tool allowlist (fails closed)
internal/guard       citation + action-claim checks, PII redaction
internal/respond     fixed texts for clarify/refuse/escalate
internal/handoff     escalation tickets (JSONL)
internal/session     per-conversation state, 30-min TTL
internal/obs         one structured log event per turn, expvar counters
docs/PRD.md          requirements and milestones
docs/DECISIONS.md    decision log with evidence
AI_USAGE.md          running log of AI assistance and corrections
```

## Architecture

```
customer msg
   │
   ├─ >2,000 chars ──────────────► clarify (not sent to model)
   ├─ looks like card data ──────► refuse  (not sent to model, not kept in history)
   ▼
[LLM: classify] ─► ParseIntent / NormalizeOrderID ─invalid twice─► escalate
   │
   ▼
[deterministic facts] Store.Lookup(id, email) · kb.Search · attempt counter
   │
   ▼
[policy.Decide] ─► route + allowed tools
   │
   ├─ answer   ─► context built ONLY from allowed tools ─► LLM draft
   │              ─► ClaimsAction · ValidateCitations · length ─fail─► escalate
   ├─ clarify  ─► template
   ├─ refuse   ─► template
   └─ escalate ─► handoff ticket (reason, verified facts, redacted summary) ─► template
   │
   ▼
one "turn" log event + counters
```

The model is called at most twice per turn (classify, draft), plus at most
one retry each. It never chooses an action and never receives a callable
tool; see `docs/DECISIONS.md` D2, D14.

## Observability

Every turn emits exactly one JSON log event (`"msg":"turn"`) with trace and
session IDs, intent, route, reason, KB top score/chunk, verification state,
tools offered, citations, ticket ID, validation failure kind, LLM calls,
tokens and latency. Customer text is logged only after PII redaction.

Counters at `GET /debug/vars`: `turns_total`, `route_total{route}`,
`reason_total{reason}`, `llm_errors_total`, `validation_failures_total{kind}`,
`handoff_failures_total`.

### What to alert on

| Signal | Why it matters | Starting threshold |
|---|---|---|
| `handoff_failures_total` increasing | Customers are told to contact support because tickets are not being saved. Escalations are silently lost. | any increase (page) |
| `llm_errors_total` rate | Provider outage, bad key or quota. Every affected turn escalates. | > 5% of turns over 5 min |
| `reason_total{classifier_failure}` rate | Model output stopped matching the schema (model change, prompt regression). | > 2% of turns over 15 min |
| `reason_total{action_claim}` | The drafter tried to promise a refund/cancel. Blocked, but a rise means a prompt or model regression or an injection campaign. | any spike vs 7-day baseline |
| `validation_failures_total{unknown_citation,no_citation}` / answer attempts | Drafter answering from memory instead of the KB. | > 10% of answer attempts |
| escalate share of `route_total` | Overall containment. A jump with flat traffic usually means a dependency failure or a KB change that broke retrieval. | +50% vs 7-day baseline |
| `reason_total{verification_attempts_exceeded}` | Order-ID enumeration attempts. | spike vs baseline; check session IDs in logs |
| `reason_total{no_kb_grounding}` share | Questions the KB cannot answer, or retrieval threshold too strict (see D5). | review weekly, not paged |

The five technical judgment questions will be answered here once the eval
results exist.
