#!/bin/bash
# Builds e2e fixture repos covering every status gl-repos shows.
# Usage: fixtures.sh <dir>   (repos land in <dir>/work)
set -e
R=$1; rm -rf "$R"; mkdir -p "$R"; cd "$R"
export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t
git init -q --bare -b main origin.git
git clone -q origin.git seed 2>/dev/null
(cd seed && echo "hello" > README.md && printf 'def add(a, b):\n    return a + b\n' > calc.py && git add . && git commit -qm "Initial commit" && git push -q origin main)
mkdir -p work/group/deep work/node_modules
cl() { git clone -q origin.git "work/$1"; }
cl clean
cl dirty;   (cd work/dirty && printf 'def add(a, b):\n    """Return the sum."""\n    return a + b\n\ndef sub(a, b):\n    return a - b\n' > calc.py && echo "notes" > TODO.txt)
cl ahead;   (cd work/ahead && echo "more" >> README.md && git commit -qam "Expand README" \
             && for i in $(seq 1 30); do echo $i >> log.txt; git add log.txt; git commit -qm "c$i"; done \
             && git switch -qc side HEAD~5 && echo s > s && git add s && git commit -qm side \
             && git switch -q main && git merge -q --no-edit side)
cl behind
cl feature; (cd work/feature && git switch -qc feat && echo x > feat.txt && git add . && git commit -qm "Add feat")
cl detached; (cd work/detached && git checkout -q --detach)
cl group/deep/nested
cl node_modules/skipme
(cd seed && echo "v2" >> README.md && git commit -qam "Bump README" && git push -q origin main)
# repo without origin
git init -q -b main work/noorigin && (cd work/noorigin && echo a > a && git add . && git commit -qm init)
# origin without origin/HEAD and default branch master -> fallback detection
git init -q --bare -b master legacy.git
git clone -q legacy.git lseed 2>/dev/null; (cd lseed && echo a > a && git add . && git commit -qm init && git push -q origin master)
git init -q -b master work/legacy && (cd work/legacy && git remote add origin ../../legacy.git && git fetch -q origin && git reset -q --hard origin/master && git branch -q -u origin/master && git switch -qc topic && echo b > b && git add . && git commit -qm topic)
echo done
