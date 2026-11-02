#!/bin/sh
# Stand-in for ssh used by test_doctor.py (never reaches a server):
#   ssh -T -o BatchMode=yes -o ConnectTimeout=5 git@HOST
# FAKE_SSH=timeout or denied simulates the usual failures.
for a in "$@"; do host="${a#git@}"; done
case "$FAKE_SSH" in
  timeout) echo "ssh: connect to host $host port 22: Operation timed out" >&2; exit 255 ;;
  denied) echo "git@$host: Permission denied (publickey)." >&2; exit 255 ;;
esac
echo "Welcome to GitLab, @tester!"
