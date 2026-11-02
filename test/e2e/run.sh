#!/bin/bash
# End-to-end tests: drive the real binary in a pseudo-terminal (pyte) against
# throwaway fixture repos. Needs uv, git, and lazygit (tools suite).
#
#   test/e2e/run.sh          list + tools suites
#   test/e2e/run.sh --ai     also the AI suite (real Ollama, gemma4:e2b and gemma3:1b;
#                            E2E_AI_MODEL=<model> picks another)
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT

(cd "$root" && go build -o "$tmp/gl-repos" .)
: > "$tmp/empty.toml" # a config with only defaults (-config must name an existing file)
export GL_REPOS_BIN="$tmp/gl-repos" E2E_WORK="$tmp/fx/work" E2E_EMPTY_CFG="$tmp/empty.toml"
# hermetic: ignore the user's git config (signing, hooks, aliases) and caches
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 GL_REPOS_CACHE_DIR="$tmp/cache"

suites=(test_list test_tools test_clone test_create test_doctor)
[[ "${1:-}" == "--ai" ]] && { suites+=(test_ai); export E2E_AI=1; }

fail=0
for t in "${suites[@]}"; do
  "$here/fixtures.sh" "$tmp/fx" >/dev/null
  if [[ $t == test_ai ]]; then
    (cd "$E2E_WORK/clean" && echo temp > scratch.log && echo draft > notes.md)
  fi
  out=$(uv run -q --with pyte==0.8.2 python "$here/$t.py" 2>&1) || fail=1
  printf '%-11s %3d passed, %d failed\n' "$t" "$(grep -c '^PASS' <<<"$out")" "$(grep -c '^FAIL ' <<<"$out")"
  grep -E '^FAIL |Traceback|Error' -A3 <<<"$out" || true
done
exit $fail
