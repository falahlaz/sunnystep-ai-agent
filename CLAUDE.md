# CLAUDE.md

Take-home prototype: footwear customer-support agent in Go. Read `docs/PRD.md` before doing anything; it is the source of truth for scope and milestones.

## Rules
- **Never weaken the invariants in PRD §2.** If a change would, stop and ask. Do not "fix" a failing test by loosening an invariant.
- Work one milestone (PRD §6) at a time. Stop at the end of each milestone and summarize what changed so the human can review before committing.
- Standard library first. Any new dependency needs a one-line justification in `docs/DECISIONS.md`.
- All unit tests use the fake LLM client. Only `cmd/eval` and manual chat call the real API.
- Inject time (`now func() time.Time`); never call `time.Now()` inside business logic.
- Never delete or weaken an eval case to make results pass. Record it as a known limitation instead.
- When you make a non-obvious choice, add an entry to `docs/DECISIONS.md` (decision, rejected alternatives, evidence).
- Do not edit the "My review (Falah)" sections of `AI_USAGE.md`; the human writes those.

## Commands
- `go test ./...` and `go test -race ./...`
- `go vet ./...` and `gofmt -l .` (must print nothing)
- `go run ./cmd/agent chat`
- `go run ./cmd/eval --provider fake` / `--provider anthropic --repeat 3`

## Layout
See `README.md`.
