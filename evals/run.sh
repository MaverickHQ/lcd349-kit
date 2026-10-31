#!/bin/sh
# KN6 agent evaluation (task 10.3): fresh Claude Code sessions with only the
# lcd349 plugin, each given evals/briefs/three-screen.md, then checked.
#   evals/run.sh [-runs 5] [-model claude-opus-5-5] [-budget 15]
# It spends API money: each session is capped by -budget (US dollars).
set -eu
cd "$(dirname "$0")/.."
exec go run ./tools/eval "$@"
