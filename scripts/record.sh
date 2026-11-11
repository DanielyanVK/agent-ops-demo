#!/usr/bin/env bash
set -euo pipefail

go run ./cmd/agentops run --scenario baseline  --model haiku  --n 3
go run ./cmd/agentops run --scenario baseline  --model sonnet --n 2
go run ./cmd/agentops run --scenario no-edit   --model haiku  --n 1
go run ./cmd/agentops run --scenario few-turns --model haiku  --n 1
go run ./cmd/agentops run --scenario one-turn  --model haiku  --n 1
go run ./cmd/agentops run --scenario bad-task  --model haiku  --n 2
go run ./cmd/agentops run --scenario skill     --model haiku  --n 2
go run ./cmd/agentops run --agent codex --scenario baseline --n 2
go run ./cmd/agentops run --agent codex --scenario no-edit  --n 1
go run ./cmd/agentops run --agent codex --scenario skill    --n 1
go run ./cmd/agentops run --agent codex --scenario bad-task --n 1

# The leak check runs even when a diagnosis failed; either failure fails the script.
rc=0
go run ./cmd/agentops analyze || rc=$?
scripts/leakcheck.sh || rc=$?
exit "$rc"
