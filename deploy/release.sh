#!/usr/bin/env bash
set -euo pipefail
cd /opt/masajka
# Server must already have .env and read access to the GHCR images.
owner="$1"
sha="$2"
[[ "$owner" =~ ^[a-z0-9-]+$ && "$sha" =~ ^[a-f0-9]{40}$ ]]
[ -f .env ]
[ -f telegram.env ]
cp compose.yaml compose.yaml.previous 2>/dev/null || true
cp images.env images.env.previous 2>/dev/null || true
mv compose.yaml.next compose.yaml
printf 'API_IMAGE=ghcr.io/%s/masajka-api:%s\nWEB_IMAGE=ghcr.io/%s/masajka-web:%s\n' "$owner" "$sha" "$owner" "$sha" > images.env
if docker compose -p masajka --env-file .env --env-file telegram.env --env-file images.env pull api web && docker compose -p masajka --env-file .env --env-file telegram.env --env-file images.env up -d --no-build --wait --wait-timeout 120 db api web; then
  echo "Release healthy: $sha"
else
  echo 'Deployment failed; attempting previous release.' >&2
  if [ -f images.env.previous ] && [ -f compose.yaml.previous ]; then
    cp images.env.previous images.env
    cp compose.yaml.previous compose.yaml
    docker compose -p masajka --env-file .env --env-file telegram.env --env-file images.env up -d --no-build --wait --wait-timeout 120 db api web
  fi
  exit 1
fi
