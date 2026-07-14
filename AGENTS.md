# Sub2API Development And VPS Deployment

## Canonical Branch

- Use `deploy/vps-responses-lite-fix-20260714` for the customized VPS build.
- Preserve the Responses Lite WebSocket guards for hosted `image_generation` and `web_search` tools.

## UI Work

- The frontend lives under `frontend/`.
- Run the existing frontend typecheck/build before pushing UI changes.
- Keep API contracts and backend behavior unchanged unless the task explicitly requires both.

## Image Build

- Do not compile production images on the 2 GB VPS.
- Push build-affecting changes to the canonical branch. `.github/workflows/vps-image.yml` builds and publishes `ghcr.io/keithwy1030/sub2api-kei:deploy-latest` for `linux/amd64`.
- Confirm the `Build VPS Image` workflow succeeds before deployment.

## VPS Deployment

- Follow the installed `interserver-vps-ssh` skill and verify the pinned host key first.
- Deploy with `/opt/sub2api/deploy-from-ghcr.sh`.
- The script preserves the current image as a timestamped rollback tag, recreates only the `sub2api` container, waits for health, and automatically rolls back on failure.
- Do not replace PostgreSQL, Redis, Xray, Cloudflare Tunnel, `/opt/sub2api/data`, or the production compose environment for a frontend-only change.

## Verification

- Require a healthy `sub2api` container and HTTP 200 from `https://api.kaimoweb.cloud/health`.
- For provider or transport changes, run a real Windows `codex exec` request and confirm the matching VPS request log.
