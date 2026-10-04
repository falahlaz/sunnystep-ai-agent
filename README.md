# Footwear Support Agent (prototype)

Customer-support agent for a fictional footwear store ("Stridewell"). All
products, policies, customers and orders are invented.

> **Status:** deterministic control layer done and tested. LLM integration,
> orchestrator, CLI/HTTP interface and eval harness are next.

## Run the tests

Requires Go 1.22+.

```sh
go test ./...
```

## Layout

```
data/kb/*.md         knowledge base; each "## " heading is one citable chunk
data/orders.json     fake orders (SS-10007 carries a prompt-injection payload)
internal/kb          BM25 retrieval over KB sections
internal/orders      verified order lookup, model-safe View, date math
internal/policy      intent -> route + per-turn tool allowlist (fails closed)
internal/guard       citation validation, PII redaction for logs
docs/PRD.md          requirements and milestones for the remaining work
docs/DECISIONS.md    decision log with evidence
AI_USAGE.md          running log of AI assistance and corrections
```

## Architecture (planned)

```
customer msg
   │
   ▼
[guard: payment-data check]──► block + warn
   │
   ▼
[LLM: classify intent] ──► ParseIntent (enum) ──invalid──► escalate
   │
   ▼
[deterministic facts: KB score, verification state]
   │
   ▼
[policy.Decide] ──► route + allowed tools
   │
   ├─ answer   ─► LLM drafts with allowed tools only ─► ValidateCitations ─fail─► escalate
   ├─ clarify  ─► templated question
   ├─ escalate ─► handoff ticket (reason code, redacted summary)
   └─ refuse   ─► templated message
```

The five technical judgment questions will be answered here once the
end-to-end flow and the eval results exist.
