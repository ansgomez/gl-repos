#!/bin/sh
# Stand-in for ssh in the demo recording (docs/demo/setup.sh): serves
# git@gitlab.example.com:<path> from the bare repos under $FAKE_REMOTES, so
# remotes keep their real-looking URLs. Never opens a connection.
for last; do :; done                      # git passes the command last
case "$last" in git-upload-pack\ *|git-receive-pack\ *|git-upload-archive\ *) ;; *) exit 255 ;; esac
cd "$FAKE_REMOTES" && eval "$last"
