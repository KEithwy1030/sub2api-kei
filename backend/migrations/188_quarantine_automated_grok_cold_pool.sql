-- Preserve isolated automated Grok accounts in a low-frequency cold pool.
WITH updated AS (
  UPDATE accounts AS a
  SET status = 'error',
      schedulable = FALSE,
      extra = COALESCE(a.extra, '{}'::jsonb) || jsonb_build_object(
        'automation_source', 'mac-grok-register',
        'automation_cleanup_policy', 'grok-free-permanent-v1',
        'automation_quarantine_state', 'cold',
        'automation_quarantine_reason', COALESCE(NULLIF(a.extra->>'automation_quarantine_reason', ''), 'restored_from_backup'),
        'automation_quarantined_at', COALESCE(NULLIF(a.extra->>'automation_quarantined_at', ''), to_char(NOW() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')),
        'cold_restore_state', NULL
      ),
      updated_at = NOW()
  WHERE a.deleted_at IS NULL
    AND a.platform = 'grok'
    AND a.type = 'oauth'
    AND a.status <> 'disabled'
    AND (
      COALESCE(a.extra->>'cold_restore_state', '') = 'isolated'
      OR (
        COALESCE(a.extra->>'automation_source', '') = 'mac-grok-register'
        AND COALESCE(a.extra->>'automation_cleanup_policy', '') = 'grok-free-permanent-v1'
        AND COALESCE(a.extra->>'automation_quarantine_state', '') = 'cold'
      )
    )
  RETURNING a.id
)
INSERT INTO scheduler_outbox (event_type, account_id)
SELECT 'account_changed', id FROM updated;

UPDATE scheduled_test_plans AS p
SET cron_expression = format('%s %s * * *', mod(p.account_id * 37, 60), mod(p.account_id * 17, 24)),
    enabled = TRUE,
    auto_recover = TRUE,
    updated_at = NOW()
FROM accounts AS a
WHERE a.id = p.account_id
  AND COALESCE(a.extra->>'automation_quarantine_state', '') = 'cold';

INSERT INTO scheduled_test_plans (
  account_id, model_id, cron_expression, enabled, max_results,
  auto_recover, next_run_at, created_at, updated_at
)
SELECT
  a.id,
  'grok-4.5',
  format('%s %s * * *', mod(a.id * 37, 60), mod(a.id * 17, 24)),
  TRUE,
  20,
  TRUE,
  NOW() + make_interval(secs => (mod(a.id * 7919, 86400) + 60)::int),
  NOW(),
  NOW()
FROM accounts AS a
WHERE a.deleted_at IS NULL
  AND COALESCE(a.extra->>'automation_quarantine_state', '') = 'cold'
  AND NOT EXISTS (
    SELECT 1 FROM scheduled_test_plans AS p WHERE p.account_id = a.id
  );
