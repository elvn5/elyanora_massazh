#!/bin/sh
# Runs in the one-shot deploy-tools container on every release (compose.yaml).
# Installs release.sh and ssh-gateway.sh from the verified API image into the
# server directory, so the server scripts always match the deployed commit.
set -eu
target=/target
# Only a server prepared per README already has the gateway; a local checkout is left untouched.
[ -f "$target/ssh-gateway.sh" ] || exit 0
owner=$(stat -c %u:%g "$target")
for f in release.sh ssh-gateway.sh; do
  tmp="$target/.$f.$$"
  cp "/deploy-tools/$f" "$tmp"
  chown "$owner" "$tmp"
  chmod 0700 "$tmp"
  # rename() keeps the currently running copy intact (it holds the old inode).
  mv -f "$tmp" "$target/$f"
done
echo 'Server deploy scripts updated.'
