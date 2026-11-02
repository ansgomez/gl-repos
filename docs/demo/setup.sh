#!/bin/sh
# Builds the sandbox that docs/demo/demo.tape records: a fake HOME with
# ~/code full of made-up repositories in every state gl-repos shows, local
# bare "origins" served as gitlab.example.com by a fake ssh, a fake glab
# that lists the same made-up projects, and a fake Ollama that answers
# with a fixed plan. Nothing from this machine or any real server is used.
#
# Usage: docs/demo/setup.sh <dir> <gl-repos binary>
# Then:  . <dir>/env.sh   (sets HOME, PATH, git config for the recording)
set -eu
D=$1 BIN=$2
here=$(cd "$(dirname "$0")" && pwd)
# never wipe a directory this script did not make
if [ -e "$D" ] && [ ! -e "$D/.gl-repos-demo" ]; then
  echo "$D exists and is not a gl-repos demo sandbox; pick another directory" >&2; exit 1
fi
pkill -f "$D/bin/fake_ollama.py" 2>/dev/null || true
chmod -R u+w "$D" 2>/dev/null || true   # e.g. a read-only Go module cache
rm -rf "$D"; mkdir -p "$D/bin" "$D/home/code" "$D/remotes/acme" "$D/home/.config/gl-repos"
D=$(cd "$D" && pwd); touch "$D/.gl-repos-demo"
cp "$BIN" "$D/bin/gl-repos"
cp "$here/fake_glab.sh" "$D/bin/glab"
cp "$here/fake_ssh.sh" "$D/bin/ssh"
cp "$here/fake_ollama.py" "$D/bin/fake_ollama.py"
chmod 755 "$D/bin/glab" "$D/bin/ssh"
ln -s "$(command -v git)" "$D/bin/git"   # PATH in the recording: only these

cat > "$D/gitconfig" <<EOF
[user]
	name = Demo User
	email = demo@example.com
[init]
	defaultBranch = main
[advice]
	detachedHead = false
[core]
	sshCommand = $D/bin/ssh
EOF
export GIT_CONFIG_GLOBAL="$D/gitconfig" GIT_CONFIG_NOSYSTEM=1 HOME="$D/home" FAKE_REMOTES="$D/remotes"
now=$(date +%s)
# commit <dir> <hours ago> <message>: commit everything, dated in the past
commit() {
  d="$(( now - $2 * 3600 )) +0000"
  (cd "$1" && git add -A && GIT_AUTHOR_DATE=$d GIT_COMMITTER_DATE=$d git commit -qm "$3")
}
# project <path>: a bare origin plus a seed clone with a first commit
project() {
  git init -q --bare "$D/remotes/$1.git"
  git clone -q "git@gitlab.example.com:$1.git" "$D/seed/$1" 2>/dev/null
  printf '# %s\n' "${1##*/}" > "$D/seed/$1/README.md"
  printf 'module example.com/%s\n\ngo 1.26\n' "${1##*/}" > "$D/seed/$1/go.mod"
  commit "$D/seed/$1" 200 "Initial commit"
  (cd "$D/seed/$1" && git push -q origin main)
}
work() { git clone -q "git@gitlab.example.com:$1.git" "$D/home/code/$2" 2>/dev/null; }

for p in api-gateway billing-service web-dashboard terraform-modules docs-site mobile-app \
  data-pipeline platform/auth-service platform/notification-service tools/release-bot; do
  mkdir -p "$D/remotes/acme/$(dirname "$p")"
  project "acme/$p"
done

# api-gateway: teammates pushed since the last fetch -> behind after f
work acme/api-gateway api-gateway
s=$D/seed/acme/api-gateway
printf 'package gateway\n\nconst Timeout = 30\n' > "$s/timeout.go"; commit "$s" 5 "Raise upstream timeout to 30s"
printf 'package gateway\n\nconst MaxBody = 1 << 20\n' > "$s/limits.go"; commit "$s" 3 "Limit request bodies to 1 MiB"
printf 'package gateway\n\n// Rate limits per client.\nconst RPS = 50\n' > "$s/rate.go"; commit "$s" 1 "Add per-client rate limit"
(cd "$s" && git push -q origin main)

# billing-service: uncommitted work for the AI to commit
work acme/billing-service billing-service
b=$D/home/code/billing-service
mkdir -p "$b/charge"
printf 'package charge\n\nfunc Charge(id string) error { return send(id) }\n' > "$b/charge/charge.go"
commit "$b" 30 "Add charge endpoint"; (cd "$b" && git push -q origin main)
cat > "$b/charge/charge.go" <<'EOF'
package charge

import "time"

// Charge sends a charge, retrying transient failures with backoff.
func Charge(id string) error {
	var err error
	for i := 0; i < 3; i++ {
		if err = send(id); err == nil {
			return nil
		}
		time.Sleep(time.Duration(1<<i) * 100 * time.Millisecond)
	}
	return err
}
EOF
cat > "$b/charge/charge_test.go" <<'EOF'
package charge

import "testing"

func TestChargeRetries(t *testing.T) {
	if err := Charge("ch_123"); err != nil {
		t.Fatal(err)
	}
}
EOF

# web-dashboard: two local commits not pushed yet -> ahead
work acme/web-dashboard web-dashboard
w=$D/home/code/web-dashboard
printf 'export const theme = "dark";\n' > "$w/theme.ts"; commit "$w" 6 "Add dark theme"
printf 'export const chart = "line";\n' > "$w/chart.ts"; commit "$w" 2 "Show revenue as a line chart"

# terraform-modules: on a feature branch
work acme/terraform-modules terraform-modules
t=$D/home/code/terraform-modules
(cd "$t" && git switch -qc feature/vpc-peering)
printf 'module "peering" {}\n' > "$t/peering.tf"; commit "$t" 20 "Add VPC peering module"
(cd "$t" && git push -q -u origin feature/vpc-peering)

# clean ones, with different ages
work acme/docs-site docs-site
printf 'Install with brew.\n' > "$D/home/code/docs-site/install.md"; commit "$D/home/code/docs-site" 50 "Document brew install"
(cd "$D/home/code/docs-site" && git push -q origin main)
work acme/mobile-app mobile-app
work acme/data-pipeline data-pipeline
(cd "$D/home/code/data-pipeline" && git switch -qc spike/duckdb)
printf 'SELECT 1;\n' > "$D/home/code/data-pipeline/q.sql"; commit "$D/home/code/data-pipeline" 8 "Try DuckDB for the nightly job"

# a local experiment without any remote
mkdir -p "$D/home/code/scratch"; (cd "$D/home/code/scratch" && git init -q)
printf 'notes\n' > "$D/home/code/scratch/notes.md"; commit "$D/home/code/scratch" 70 "Notes"

rm -rf "$D/seed"

cat > "$D/home/.config/gl-repos/config.toml" <<'EOF'
tool = "true"

[[profile]]
name = "work"
forge = "gitlab"
host = "gitlab.example.com"
color = "4"

[ai]
url = "http://127.0.0.1:11999"
model = "gemma4:e2b"
EOF
chmod 700 "$D/home/.config" "$D/home/.config/gl-repos"; chmod 600 "$D/home/.config/gl-repos/config.toml"

# env.sh: what the recording shell sources (hidden in the tape)
cat > "$D/env.sh" <<EOF
export HOME="$D/home" PATH="$D/bin:/usr/bin:/bin" GIT_CONFIG_GLOBAL="$D/gitconfig" GIT_CONFIG_NOSYSTEM=1
export GL_REPOS_CACHE_DIR="$D/cache" FAKE_REMOTES="$D/remotes" TERM=xterm-256color
unset GL_REPOS_CONFIG GITGAZE_CONFIG GITGAZE_CACHE_DIR GITLAB_TOKEN GITLAB_HOST GH_TOKEN GITHUB_TOKEN GLAB_CONFIG_DIR GH_CONFIG_DIR
pgrep -f "$D/bin/fake_ollama.py" >/dev/null || ("$(command -v python3)" "$D/bin/fake_ollama.py" 11999 >/dev/null 2>&1 &)
cd "\$HOME/code"
PS1='\$ '
clear
EOF
echo "sandbox ready: . $D/env.sh"
