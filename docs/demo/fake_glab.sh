#!/bin/sh
# Stand-in for glab in the demo recording (docs/demo/setup.sh): one made-up
# GitLab, gitlab.example.com, whose projects are the bare repos under
# $FAKE_REMOTES. Read-only; never talks to a network.
if [ "$1" = auth ] && [ "$2" = status ]; then
  printf 'gitlab.example.com\n  ✓ Logged in to gitlab.example.com as demo (/demo/config.yml)\n'; exit 0
fi
[ "$1" = api ] || { echo "demo glab: $1 is not part of the demo" >&2; exit 1; }
ep=$4
[ "$ep" = user ] && { echo '{"username":"demo"}'; exit; }
desc() {
  case "$1" in
    acme/api-gateway) echo "Edge proxy: auth, rate limits, routing" ;;
    acme/billing-service) echo "Charges, invoices and refunds" ;;
    acme/web-dashboard) echo "Customer dashboard (TypeScript)" ;;
    acme/terraform-modules) echo "Shared infrastructure modules" ;;
    acme/docs-site) echo "Public documentation" ;;
    acme/mobile-app) echo "iOS and Android app" ;;
    acme/data-pipeline) echo "Nightly ETL jobs" ;;
    acme/platform/auth-service) echo "OAuth2 and SSO for every app" ;;
    acme/platform/notification-service) echo "Email, SMS and push notifications" ;;
    acme/tools/release-bot) echo "Tags releases and writes changelogs" ;;
  esac
}
projects() {
  sep=
  printf '['
  for g in $(cd "$FAKE_REMOTES" && find . -name '*.git' -type d | sed 's|^\./||; s|\.git$||' | sort); do
    printf '%s{"path_with_namespace":"%s","path":"%s","ssh_url_to_repo":"git@gitlab.example.com:%s.git","http_url_to_repo":"https://gitlab.example.com/%s.git","description":"%s","last_activity_at":"2026-10-01T10:00:00Z","archived":false}' \
      "$sep" "$g" "${g##*/}" "$g" "$g" "$(desc "$g")"
    sep=,
  done
  printf ']\n'
}
case "$ep" in
  projects/*) echo '{"message":"404 Project Not Found"}' >&2; exit 1 ;;
  groups\?*|groups/*) echo '[]' ;;
  projects\?*) projects ;;
  *) echo '[]' ;;
esac
