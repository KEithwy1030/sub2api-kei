-- Existing accounts created by the Mac Grok registration automation predate
-- the explicit cleanup opt-in. Enable the guarded policy once without moving
-- the grace-period timestamp on subsequent starts.
UPDATE accounts
SET extra = COALESCE(extra, '{}'::jsonb) || jsonb_build_object(
      'automation_cleanup_policy', 'grok-free-permanent-v1',
      'automation_cleanup_enabled_at', to_char(NOW() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
    ),
    updated_at = NOW()
WHERE deleted_at IS NULL
  AND COALESCE(extra->>'automation_source', '') = 'mac-grok-register'
  AND COALESCE(extra->>'automation_cleanup_policy', '') <> 'grok-free-permanent-v1';
