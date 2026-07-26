-- Persist the latest scheduled-test observation on automated Grok accounts so
-- the hot scheduler can fail closed without joining the test-result tables.
WITH latest AS (
  SELECT DISTINCT ON (p.account_id)
    p.account_id,
    r.status,
    r.error_message,
    r.finished_at
  FROM scheduled_test_plans AS p
  JOIN scheduled_test_results AS r ON r.plan_id = p.id
  ORDER BY p.account_id, r.finished_at DESC, r.id DESC
), latest_success AS (
  SELECT p.account_id, MAX(r.finished_at) AS finished_at
  FROM scheduled_test_plans AS p
  JOIN scheduled_test_results AS r ON r.plan_id = p.id
  WHERE r.status = 'success'
  GROUP BY p.account_id
), updated AS (
UPDATE accounts AS a
SET extra = COALESCE(a.extra, '{}'::jsonb) || jsonb_build_object(
      'automation_health_state', CASE WHEN latest.status = 'success' THEN 'healthy' ELSE 'unhealthy' END,
      'automation_health_observed_at', to_char(latest.finished_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
      'automation_health_last_success_at', to_char(latest_success.finished_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
      'automation_health_last_failure_at', CASE WHEN latest.status <> 'success' THEN to_char(latest.finished_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"') END,
      'automation_health_failure_class', CASE
        WHEN latest.status = 'success' THEN NULL
        WHEN latest.error_message LIKE 'Grok Responses API returned 402:%' THEN 'quota_exhausted'
        WHEN latest.error_message LIKE 'Grok Responses API returned 429:%'
          AND LOWER(latest.error_message) ~ '"code"[[:space:]]*:[[:space:]]*"subscription:free-usage-exhausted"' THEN 'free_usage_exhausted'
        WHEN latest.error_message LIKE 'Grok Responses API returned 429:%' THEN 'rate_limited'
        WHEN latest.error_message LIKE 'Grok Responses API returned 403:%'
          AND LOWER(latest.error_message) ~ 'permission[-_]denied'
          AND LOWER(latest.error_message) LIKE '%access to the chat endpoint is denied%' THEN 'permission_denied'
        WHEN latest.error_message LIKE 'Grok Responses API returned 403:%' THEN 'forbidden_unknown'
        WHEN latest.error_message LIKE 'Grok Responses API returned 401:%'
          OR LOWER(latest.error_message) LIKE '%access token is expired%'
          OR LOWER(latest.error_message) LIKE '%refresh token%' THEN 'credential_refresh_pending'
        WHEN latest.error_message ~ '^Grok Responses API returned 5[0-9][0-9]:' THEN 'transient_upstream'
        WHEN LOWER(latest.error_message) ~ '(timeout|connection reset|connection refused|no such host|temporary network)' THEN 'transient_transport'
        ELSE 'unknown'
      END
    ),
    updated_at = NOW()
FROM latest
LEFT JOIN latest_success ON latest_success.account_id = latest.account_id
WHERE a.id = latest.account_id
  AND a.deleted_at IS NULL
  AND a.platform = 'grok'
  AND a.type = 'oauth'
  AND COALESCE(a.extra->>'automation_source', '') = 'mac-grok-register'
  AND COALESCE(a.extra->>'automation_cleanup_policy', '') = 'grok-free-permanent-v1'
RETURNING a.id
)
INSERT INTO scheduler_outbox (event_type, account_id)
SELECT 'account_changed', id FROM updated;
