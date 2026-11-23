#!/usr/bin/env bash
# Installed by the server administrator, used as an authorized_keys forced command.
# No interactive shell, arbitrary command, forwarding, or unrelated project access.
set -euo pipefail
umask 077
cd /opt/masajka
case "${SSH_ORIGINAL_COMMAND:-}" in
  token)
    IFS= read -r token
    [[ "$token" =~ ^[0-9]+:[A-Za-z0-9_-]+$ ]] || exit 1
    printf 'TELEGRAM_BOT_TOKEN=%s\n' "$token" > telegram.env.next
    mv telegram.env.next telegram.env
    ;;
  admin-password)
    IFS= read -r password
    # 12-72 bytes (bcrypt limit); no quote or control characters, so the
    # single-quoted env-file value stays literal and is never interpolated.
    length=$(LC_ALL=C; printf '%s' "${#password}")
    [ "$length" -ge 12 ] && [ "$length" -le 72 ] || exit 1
    [[ "$password" != *"'"* && ! "$password" =~ [[:cntrl:]] ]] || exit 1
    printf "ADMIN_PASSWORD='%s'\n" "$password" > admin.env.next
    mv admin.env.next admin.env
    ;;
  compose)
    temp=$(mktemp /opt/masajka/compose-upload.XXXXXX.yaml)
    trap 'rm -f "$temp"' EXIT
    head -c 65537 > "$temp"
    [ "$(wc -c < "$temp")" -le 65536 ]
    docker compose -p masajka --env-file .env --env-file telegram.env --env-file admin.env -f "$temp" config --quiet
    mv "$temp" compose.yaml.next
    ;;
  deploy\ *)
    sha=${SSH_ORIGINAL_COMMAND#deploy }
    [[ "$sha" =~ ^[a-f0-9]{40}$ ]] || exit 1
    # A short-lived GitHub Actions token arrives over stdin, never in argv.
    registry_config=$(mktemp -d /opt/masajka/registry.XXXXXX)
    trap 'rm -rf "$registry_config"' EXIT
    export DOCKER_CONFIG="$registry_config"
    docker login ghcr.io --username elvn5 --password-stdin >/dev/null 2>&1
    /opt/masajka/release.sh elvn5 "$sha"
    ;;
  *) echo 'Only token, admin-password, compose and deploy <commit SHA> commands are allowed.' >&2; exit 1 ;;
esac
