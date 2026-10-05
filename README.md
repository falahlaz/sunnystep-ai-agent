# Footwear Support Agent (prototype)

Customer-support agent for a fictional footwear store ("Stridewell"). All
products, policies, customers and orders are invented.

> **Status:** prototype complete. The committed eval reports are from the
> offline provider; real-model runs are pending (see "Known limitations").

## Setup

Requires Go 1.24+. No other services.

```sh
git clone <repo> && cd <repo>
go test ./...                      # all unit tests use a fake model, no key needed
LLM_PROVIDER=fake go run ./cmd/agent chat   # works offline (keyword rules, not a model)
```

For a real model, copy `.env.example` values into your shell: either
`ANTHROPIC_API_KEY` (default model `claude-sonnet-5-5`, override with `LLM_MODEL`)
or `LLM_PROVIDER=openai` with `OPENAI_API_KEY` and `LLM_MODEL` (required).

## Run

```sh

export ANTHROPIC_API_KEY=...       # real model (default claude-sonnet-5-5, override with LLM_MODEL)
go run ./cmd/agent chat            # terminal chat; turn logs go to var/chat.log
go run ./cmd/agent serve --addr :8080

LLM_PROVIDER=fake go run ./cmd/agent chat   # offline keyword rules, no API key
LLM_PROVIDER=openai LLM_MODEL=<model> go run ./cmd/agent chat   # OpenAI (D25)
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

## Eval

```sh
go run ./cmd/eval --provider fake                     # offline rules, no key
go run ./cmd/eval --provider anthropic --repeat 3     # real model, flakiness over 3 runs
go run ./cmd/eval --provider anthropic --filter faq-  # subset by case ID
LLM_MODEL=<model> go run ./cmd/eval --provider openai --repeat 3
```

40 cases in `eval/cases/*.json` (categories and minimum counts from PRD §4.10).
Each run writes `eval/results/<timestamp>-<provider>/report.md` and
`results.json`: pass rate per category, passes per case out of N repeats
(flaky cases are marked), failing turns with what the agent actually did,
token totals and p50/p95 turn latency. Failing cases are kept and reported.
`go test ./...` runs the `"ci": true` subset through the offline provider.

### Defect found by the eval and the fix (M5)

The first eval run showed two FAQ questions the KB answers ("How long does a
refund take?", "What does the warranty cover?") escalating as
`no_kb_grounding`: their BM25 scores (2.83, 2.54) were below the 3.0
threshold and inside the range of off-topic questions (2.40–2.71), so
lowering the threshold would have let irrelevant chunks through. The fix
was a slightly better stemmer ("-ing", "-ed", double consonants), which
raised the grounded questions to ≥3.29 while off-topic ones stayed ≤2.71.
`kb.TestThresholdSeparatesEvalQuestions` guards both sides. Offline
before/after and the caveats are in `docs/DECISIONS.md` D24; real-model
numbers are pending.

## Layout

```
cmd/agent            chat REPL and HTTP server
cmd/eval             eval runner
eval/cases           eval conversations and expectations
eval/results         committed eval reports
data/kb/*.md         knowledge base; each "## " heading is one citable chunk
data/orders.json     fake orders (SS-10007 carries a prompt-injection payload)
internal/agent       one customer turn end to end (PRD §3)
internal/classify    model intent label + hints, validated, one retry
internal/llm         Client interface, Claude and OpenAI clients, scripted fake
internal/offline     keyword-rule stand-in for the model (LLM_PROVIDER=fake)
internal/kb          BM25 retrieval over KB sections
internal/orders      verified order lookup, model-safe View, date math
internal/policy      intent -> route + per-turn tool allowlist (fails closed)
internal/guard       citation + action-claim checks, PII redaction
internal/respond     fixed texts for clarify/refuse/escalate
internal/handoff     escalation tickets (JSONL)
internal/session     per-conversation state, 30-min TTL
internal/obs         one structured log event per turn, expvar counters
internal/eval        case loading, checks, runner, report
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

## Testing evidence

| What | Where |
|---|---|
| Unit tests (fake model only), incl. `-race` | `go test -race ./...` |
| Policy invariants enumerated (intent × verification × score × failures) | `internal/policy` `TestToolInvariants`, `TestDecideOutcomesListed` |
| End-to-end invariants with the real KB/orders and a scripted model | `internal/agent` (gift-message injection never reaches the model, card data never reaches it even via history, attempt limit cannot be bypassed, non-answer routes never draft) |
| Fuzzing `Agent.Turn` (random customer text × random model output) | `go test -run '^$' -fuzz=FuzzTurn -fuzztime=30s ./internal/agent` (≈166k execs, no panics) |
| Mutation checks (tests have teeth) | listed per decision in `docs/DECISIONS.md` and per session in `AI_USAGE.md` |
| Eval, 40 conversations, failures kept | `eval/results/*/report.md` |
| One defect found by the eval → fix → regression test | README "Defect found" above, D24 |

## Assumptions

- **Identity = order ID + checkout email.** Good enough for order status in a prototype; real deployments would add OTP or a logged-in session (D6).
- **Single process.** Sessions are in memory (30-min idle TTL) and lost on restart; tickets go to a local JSONL file. Both sit behind interfaces (`session.Store`, `handoff.Sink`).
- **English only**, one store, SGD, Singapore/Malaysia shipping, as in the KB.
- **All write actions go to a human** (refund, return, cancel, exchange, address change, replacement), even when the policy looks mechanical (D3). The agent explains policy and hands over pre-computed facts.
- **The KB is the only source of truth** for FAQ answers; a question it does not cover escalates rather than being answered from model knowledge.
- **"Now" is the server clock** for eligibility facts; tests and eval pin it to 2026-10-04 12:00 SGT.

## Known limitations

- **Real-model eval not yet run.** The committed reports use the offline provider, which says nothing about model quality. Run `go run ./cmd/eval --provider anthropic --repeat 3` to produce the real numbers (D24 lists the before/after commits).
- **Retrieval is keyword BM25 with a crude stemmer.** Synonyms are missed ("my shoes are too small" does not reach sizing content and escalates). The threshold separates the eval questions with a small margin (3.29 vs 2.71, D24); new phrasings may fall on the wrong side. Failure is safe (escalation), not wrong answers.
- **Attempt limit is per session.** Rotating session IDs gives 3 more tries each; a per-order or per-client limit is out of scope (D16).
- **`ClaimsAction` is regex-based** and will miss paraphrased promises. It is a backstop; the agent has no write tools at all (D19).
- **No temperature control** on the default model (it rejects non-default sampling); variance is measured with `--repeat` instead (D10).
- **Mixed-intent messages lose the answerable half**: "where is my order and I want a refund" escalates as a whole (D15).
- **No auth, rate limiting or TLS** on the HTTP server; it is a local demo interface.

## Technical judgment questions

> Draft prepared with Claude from the decision log and test evidence.
> Falah: review and rewrite in your own words before submitting.

### 1. What did you decide was unsafe to automate, and why?

- **Every write action**: refund, return approval, cancellation, exchange, address change, replacement for a lost parcel. They move money or goods, are hard to reverse, and depend on facts the system cannot see (shoe condition, whether the one-time comfort guarantee was already used, which lives in an internal note). The agent explains the policy, collects the order and reason, and hands a ticket with pre-computed facts to a human (D3). This holds even for a verified customer and even when the rule looks mechanical ("within 30 days").
- **Choosing the route.** The model only labels intent; plain Go (`policy.Decide`) decides answer/clarify/refuse/escalate and which data the drafter may see (D2, D14). A prompt saying "don't refund" is a request; an allowlist is a control.
- **Showing order data without a second factor.** Order IDs are sequential, so lookup needs ID + checkout email, errors are identical for "no such order" and "wrong email", and lookups stop after 3 failures in a session (D6, D16).
- **Medical claims.** "Will these help my plantar fasciitis?" is refused with a fixed message even when the KB matches strongly (D9).
- **Customer-facing text on non-answer routes.** Refusals, clarifications and handoffs are templates, so a jailbroken model cannot change what they say (I8).
- **Date arithmetic and eligibility.** Days since delivery and the final-sale flag are computed in code and given to the model as facts (D8).

### 2. What would most likely fail first in production, and how would you detect and contain it?

**Retrieval.** BM25 with a crude stemmer misses paraphrases; the eval already found two KB-answerable questions below the threshold (D23–D24), and "my shoes are too small" still misses. The margin between the lowest grounded question (3.29) and the highest off-topic one (2.71) is small, so real traffic will land on both wrong sides.

- *Containment:* a miss escalates (`no_kb_grounding`), it does not produce a wrong answer. A false hit still has to pass the citation check (the drafter may only cite chunks retrieved this turn) and the action-claim check, or it escalates.
- *Detection:* `reason_total{no_kb_grounding}` share and `validation_failures_total{no_citation,unknown_citation}` (README "What to alert on"); every turn log carries `kb_top_score` and `kb_top_chunk`, so a weekly review of escalated questions with their scores shows which phrasings miss.

**Second most likely: the model provider** (timeouts, 429s, a model update that changes output). Contained by one retry then `llm_unavailable` escalation and schema validation with fail-closed parsing; detected by `llm_errors_total` and `reason_total{classifier_failure}`.

### 3. What important architecture or product choices did you make, what alternatives did you reject, and what evidence informed those decisions?

| Choice | Rejected | Evidence |
|---|---|---|
| Model labels intent; deterministic policy picks route and data (D2) | Model chooses tools freely under a "don't refund" prompt | `TestToolInvariants` enumerates every intent × verification × score × failure count |
| "Tools" are context injected by Go from the allowlist (D14) | Native tool-use loop | `TestVerifiedSessionFAQGetsNoOrderContext`, `TestVerifiedOrderAThenOrderB`; a mutation that ignored the allowlist was caught |
| Model sees a projection of the order, never the raw record (D7) | Pass the order and instruct the model to ignore notes | SS-10007's gift message carries an injection payload; `TestInjectedGiftMessageNeverReachesModel` |
| BM25 + citation check, threshold only a coarse filter (D4, D5) | Vector DB; trusting a score threshold | Score table in D5: off-topic queries scored 2.4–2.7 on keyword overlap |
| Better stemmer instead of a lower threshold (D24) | Lower threshold | Misses (2.54, 2.83) sat inside the off-topic band; regression test checks both sides |
| Validation failure escalates, no retry (D18) | Retry the draft | A retry hides the failure rate; escalation is counted |
| Payment-like messages never reach the model or history (D17) | Redact and continue | History is replayed to the model later; test checks the next turn |
| Attempt limit blocks lookups, not just answers (D16) | Escalate but keep looking up | Found a bypass while building it: a correct guess after the limit verified the session |
| Own retry, no temperature on the default model (D10) | SDK default retries; temperature 0 as the PRD said | Model rejects non-default sampling; request-shape test |

### 4. What did an AI tool suggest or generate that you rejected, corrected or improved? How did you identify the problem?

Most code was generated with Claude (Claude Code); `AI_USAGE.md` logs each session.

**How I directed it.** I fixed the scope and nine invariants in `docs/PRD.md` (§2) and gave the AI working rules in `CLAUDE.md`: never loosen an invariant to make a test pass, never delete an eval case, inject time, all unit tests on a fake model, stop after each milestone for review, record non-obvious choices with evidence. These rules are what turned the AI's first drafts into the corrections below; several of them were caught because a rule forced a check (mutation tests, measuring before fixing).

**Problems in the AI's first drafts, caught by the checks those rules required:**

- **Verification limit bypass.** The first orchestrator kept calling `Store.Lookup` after 3 failures, so a later correct guess still verified. Found while checking the flow against the anti-enumeration goal; fixed and covered by a test that tries the right email on turn 4 (D16).
- **Payment data leaking through history.** The pre-check kept card numbers from the model on that turn, but history is replayed on the next. Found by tracing where session history goes; a test now inspects the next turn's request (D17).
- **Invented support email** in the "ticket failed" message, not present in the KB. Removed.
- **Action-claim regex** first flagged KB policy sentences ("a refund is issued once…") and missed curly apostrophes. Found by a test of policy sentences that must pass and by running the guard over every KB chunk (D19).
- **PRD said temperature 0**; the default model rejects it. Found by checking current model docs before writing the client (D10).
- **Tests checked for teeth:** mutations (removing redaction, the session lock, the lookup block, the allowlist) each made a test fail. Two mutations first failed to compile and proved nothing; they were redone.
- **The M5 re-run moved a different case** (`safety-prompt-injection-user` started failing offline). Investigated instead of tuning the fake: it had passed only by accident before (D24).

**What I rejected or changed myself after review:**

_Falah: fill in from your "My review" notes in `AI_USAGE.md` (2–4 concrete items: what the AI produced, what was wrong, how you noticed, what you did)._

### 5. What evidence makes you trust the system today, what remains unproven, and what would you improve first with one additional day?

**Evidence today**
- The control boundaries are enforced in code and tested: tool allowlist enumerated, order data only after ID + email, attempt limit with concurrency test under `-race`, gift-message injection never reaches the model, card data never reaches it (even via history), templates for every non-answer outcome (coverage test).
- `Agent.Turn` fuzzed for 30s (≈166k executions) with random customer text and random model output: no panics, never an answer without a citation.
- Mutation checks on the key guards; eval guards that stop the case set from being weakened.
- Fresh clone → `go test ./...` → offline chat works.

**Unproven**
- **Real-model behaviour.** The committed eval reports use the offline keyword provider, which tests the control layer, not model quality. Classifier accuracy on boundary cases, drafter faithfulness, flakiness across repeats, latency and cost are not measured yet.
- Retrieval on real customer phrasing (only 13 FAQ phrasings measured).
- `ClaimsAction` against paraphrased promises.

**First with one more day**
1. Run the real-model eval with `--repeat 3`, read every failure, and fix or record each one.
2. Measure query rewriting for retrieval (the classifier returns search terms in the same call) against the current stemmer, keeping the two-sided threshold test.
3. Move the attempt limit from per-session to per-order so rotating session IDs does not reset it.
