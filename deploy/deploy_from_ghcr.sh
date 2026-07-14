#!/usr/bin/env sh
set -eu

image="${SUB2API_IMAGE:-ghcr.io/keithwy1030/sub2api-kei:deploy-latest}"
deploy_dir="${SUB2API_DEPLOY_DIR:-/opt/sub2api}"
runtime_tag="sub2api:latest"
rollback_tag="sub2api:rollback-$(date +%Y%m%d-%H%M%S)"

cd "$deploy_dir"

current_image_id="$(docker inspect sub2api --format '{{.Image}}')"
docker image tag "$current_image_id" "$rollback_tag"
docker pull "$image"
docker image tag "$image" "$runtime_tag"
docker compose up -d --no-deps --force-recreate sub2api

healthy=0
attempt=0
while [ "$attempt" -lt 30 ]; do
  attempt=$((attempt + 1))
  state="$(docker inspect sub2api --format '{{.State.Status}}/{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' 2>/dev/null || true)"
  printf 'health check %s/30: %s\n' "$attempt" "$state"
  if [ "$state" = "running/healthy" ]; then
    healthy=1
    break
  fi
  case "$state" in
    *exited*|*dead*|*unhealthy*) break ;;
  esac
  sleep 3
done

if [ "$healthy" -ne 1 ]; then
  printf 'Deployment failed; restoring %s\n' "$rollback_tag" >&2
  docker image tag "$rollback_tag" "$runtime_tag"
  docker compose up -d --no-deps --force-recreate sub2api
  exit 1
fi

printf 'Deployment succeeded: %s\n' "$image"
printf 'Rollback image: %s\n' "$rollback_tag"
docker ps --filter name='^/sub2api$' --format '{{.Names}} {{.Image}} {{.Status}}'
