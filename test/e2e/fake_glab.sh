#!/bin/sh
# Stand-in for glab used by test_clone.py:
#   glab auth status
#   glab api --hostname H <endpoint> [--paginate]
#   glab repo create NAME [--group G] --<visibility> --skipGitInit
#   glab api --hostname H groups -X POST -f name=N -f path=N -f parent_id=I -f visibility=V
# Projects point at the local bare repo in $FAKE_ORIGIN. Created projects
# become bare repos under $FAKE_BARES/<namespace>/ and are logged to
# $FAKE_LOG.
#   glab api --hostname H personal_access_tokens/self   (scopes: $FAKE_GLAB_SCOPES, default api,read_api)
# FAKE_GLAB_AUTH=none acts as if glab were not logged in; FAKE_GLAB_NET=down
# as if the server could not be reached; FAKE_GLAB_STORE=plaintext as if
# the token were in glab's config file (like glab 1.123, else the keyring). Like glab 1.106, every call warns
# on stderr while the deprecated NO_PROMPT is set.
PATH="$PATH:/usr/bin:/bin" # the tests run us with a PATH of fakes only (tdrive.tooldir)
[ -n "$NO_PROMPT" ] && printf 'DEPRECATION WARNING: The NO_PROMPT environment variable is deprecated\n  and will be removed. Use GLAB_NO_PROMPT instead.\n\n' >&2
if [ "$1" = auth ] && [ "$2" = status ]; then
  [ "$FAKE_GLAB_AUTH" = none ] && { printf 'git.example.test\n  x git.example.test: no token found\n'; exit 1; }
  printf 'git.example.test\n  ✓ Logged in to git.example.test as tester (/fake/config.yml)\n'
  if [ "$FAKE_GLAB_STORE" = plaintext ]; then
    printf '  ✓ Token found in configuration file (plaintext): glpat-e2e-secret\n'
  else
    printf '  ✓ Token found in operating system keyring: **************************\n'
  fi
  exit 0
fi
if [ "$1" = api ] && [ "$FAKE_GLAB_NET" = down ]; then
  echo "ERROR: Get \"https://$3/api/v4/$4\": dial tcp: lookup $3: no such host" >&2; exit 1
fi
if [ "$1" = api ] && [ "$FAKE_GLAB_AUTH" = none ]; then
  echo '{"message":"401 Unauthorized"}' >&2; exit 1
fi
if [ "$1" = repo ] && [ "$2" = create ]; then
  echo "GITLAB_HOST=$GITLAB_HOST glab $* (cwd is repo: $(git rev-parse --is-inside-work-tree 2>/dev/null || echo no))" >> "$FAKE_LOG"
  name="$3"; group=tester; shift 3
  while [ $# -gt 0 ]; do [ "$1" = --group ] && { group="$2"; shift; }; shift; done
  [ "$name" = taken ] && { echo "glab: name has already been taken (HTTP 400)" >&2; exit 1; }
  [ "$name" = noscope ] && { printf 'POST https://%s/api/v4/projects\n403 Forbidden {"error":"insufficient_scope"}\n' "$GITLAB_HOST" >&2; exit 1; }
  git init -q --bare "$FAKE_BARES/$group/$name.git" && echo "✓ Created repository $group/$name on GitLab: https://$GITLAB_HOST/$group/$name"
  exit
fi
[ "$4" = user ] && { echo '{"username":"tester"}'; exit; }
if [ "$4" = personal_access_tokens/self ]; then
  printf '{"name":"fake","scopes":["%s"]}\n' "$(printf '%s' "${FAKE_GLAB_SCOPES:-api,read_api}" | sed 's/,/","/g')"; exit
fi
# Subgroups created by gl-repos: logged and remembered (path per line) so
# later lookups find them. Parent ids are the line numbers of
# $FAKE_BARES/groups (1 = team, 2 = team/tools).
groups="${FAKE_BARES:-$(dirname "$FAKE_ORIGIN")}/groups"
[ -f "$groups" ] || printf 'team\nteam/tools\n' > "$groups"
if [ "$4" = groups ] && [ "$5" = -X ]; then
  echo "glab $*" >> "$FAKE_LOG"
  name=; parent=
  for a in "$@"; do case "$a" in name=*) name="${a#name=}" ;; parent_id=*) parent="${a#parent_id=}" ;; esac; done
  ppath=$(sed -n "${parent}p" "$groups")
  [ -n "$ppath" ] || { echo '{"message":"404 Parent Not Found"}' >&2; exit 1; }
  echo "$ppath/$name" >> "$groups"
  printf '{"id":%s,"full_path":"%s/%s","path":"%s"}\n' "$(wc -l < "$groups" | tr -d ' ')" "$ppath" "$name" "$name"; exit
fi
ep="$4"
proj() { printf '{"path_with_namespace":"%s","path":"%s","ssh_url_to_repo":"%s","description":"%s","last_activity_at":"2026-10-01T10:00:00Z","archived":%s}' "$1" "$2" "${3:-$FAKE_ORIGIN}" "$4" "${5:-false}"; }
grp() { printf '{"id":%s,"full_path":"%s","path":"%s","description":"%s"}' "$(grep -nxF "$1" "$groups" | cut -d: -f1 | grep . || echo 0)" "$1" "${1##*/}" "$2"; }
case "$ep" in
  groups\?*search=tool*) printf '[%s]\n' "$(grp team/tools 'Tooling')" ;;
  groups\?*search=*) echo '[]' ;;
  groups\?*) printf '[%s,%s]\n' "$(grp team)" "$(grp team/tools 'Tooling')" ;;
  groups/team%2Ftools/projects*)   # subgroups included; one archived, one only shared in
    printf '[%s,%s]\n' "$(proj team/tools/cli/deploy deploy)" "$(proj team/tools/lib lib)"
    printf '[%s,%s]\n' "$(proj team/tools/old old "" "" true)" "$(proj other/shared shared)" ;;
  groups/team%2Ftools) grp team/tools Tooling ;;
  groups/*)
    g=$(printf '%s' "${ep#groups/}" | sed 's/%2F/\//g')
    grep -qxF "$g" "$groups" && { grp "$g"; exit; }
    echo '{"message":"404 Group Not Found"}' >&2; exit 1 ;;
  projects/team%2Fcalc) proj team/calc calc "" Calculator ;;
  projects/team%2Flegal%2Fsecret-calc) proj team/legal/secret-calc secret-calc ;;
  projects/*) echo '{"message":"404 Project Not Found"}' >&2; exit 1 ;;
  *order_by=last_activity_at\&per_page=100*)   # full member list, two pages (--paginate)
    printf '[%s,%s]\n' "$(proj team/calc calc "" Calculator)" "$(proj team/docs/calc-docs calc-docs "" 'Calculator docs')"
    printf '[%s,%s]\n' "$(proj team/svc/payments/billing-v3 billing-v3)" "$(proj team/broken broken /nonexistent/broken.git)"
    printf '[%s,%s]\n' "$(proj team/legal/secret-calc secret-calc)" "$(proj team/finance/payroll-calc payroll-calc)" ;;
  *search=hidden*) printf '[%s]' "$(proj team/legal/hidden-calc hidden-calc)" ;;
  *search=slow*) sleep 30; echo '[]' ;;
  *search=remoteonly*) printf '[%s]' "$(proj team/new/remoteonly remoteonly)" ;;
  *search=multi*) printf '[%s,%s]' "$(proj team/multi-a multi-a "" 'First')" "$(proj team/multi-b multi-b "" 'Second')" ;;
  *) echo '[]' ;;
esac
