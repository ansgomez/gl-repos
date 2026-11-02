#!/bin/sh
# Stand-in for gh used by test_create.py:
#   gh api --hostname H user | user/repos?... [--paginate] | repos/OWNER/NAME
#   gh repo create OWNER/NAME --<visibility>
# Created repos become bare repos under $FAKE_BARES/OWNER/ and are logged
# to $FAKE_LOG.
# gh auth status: logged in unless FAKE_GH_AUTH=none
if [ "$1" = auth ] && [ "$2" = status ]; then
  [ "$FAKE_GH_AUTH" = none ] && { echo "You are not logged into any GitHub hosts. To log in, run: gh auth login" >&2; exit 1; }
  printf 'github.com\n  ✓ Logged in to github.com account octo (keyring)\n  - Token scopes: '"'"'gist'"'"', '"'"'read:org'"'"', '"'"'repo'"'"'\n'; exit 0
fi
if [ "$1" = repo ] && [ "$2" = create ]; then
  echo "gh $* (cwd is repo: $(git rev-parse --is-inside-work-tree 2>/dev/null || echo no))" >> "$FAKE_LOG"
  git init -q --bare "$FAKE_BARES/$3.git" && echo "https://github.com/$3"
  exit
fi
repo() { printf '{"full_name":"%s","name":"%s","ssh_url":"git@github.com:%s.git","clone_url":"https://github.com/%s.git","description":null,"pushed_at":"2026-09-30T10:00:00Z"}' "$1" "${1#*/}" "$1" "$1"; }
case "$4" in
  user) echo '{"login":"octo"}' ;;
  user/repos*) printf '[%s,%s]\n' "$(repo octo/dotfiles)" "$(repo octo/weather-cli)" ;;
  *) echo '{"message":"Not Found"}' >&2; exit 1 ;;
esac
