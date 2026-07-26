package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGrokAutomationCleanupMigrationsStayGuarded(t *testing.T) {
	dropSQL, err := FS.ReadFile("185_drop_overbroad_openai_paid_plan_trigger.sql")
	require.NoError(t, err)
	require.Contains(t, string(dropSQL), "DROP TRIGGER IF EXISTS trg_prevent_openai_paid_plan_downgrade_on_429")

	policySQL, err := FS.ReadFile("186_enable_automated_grok_cleanup_policy.sql")
	require.NoError(t, err)
	normalized := strings.ToLower(string(policySQL))
	require.Contains(t, normalized, "automation_source")
	require.Contains(t, normalized, "mac-grok-register")
	require.Contains(t, normalized, "automation_cleanup_policy")
	require.Contains(t, normalized, "deleted_at is null")

	healthSQL, err := FS.ReadFile("187_backfill_automated_grok_health.sql")
	require.NoError(t, err)
	healthNormalized := strings.ToLower(string(healthSQL))
	require.Contains(t, healthNormalized, "distinct on (p.account_id)")
	require.Contains(t, healthNormalized, "automation_health_state")
	require.Contains(t, healthNormalized, "automation_health_last_success_at")
	require.Contains(t, healthNormalized, "automation_health_observed_at")
	require.Contains(t, healthNormalized, "subscription:free-usage-exhausted")
	require.Contains(t, healthNormalized, "free_usage_exhausted")
	require.Contains(t, healthNormalized, "permission[-_]denied")
	require.Contains(t, healthNormalized, "order by p.account_id, r.finished_at desc, r.id desc")
	require.NotContains(t, healthNormalized, "jsonb_strip_nulls")
	require.Contains(t, healthNormalized, "scheduled_test_results")
	require.Contains(t, healthNormalized, "mac-grok-register")
	require.Contains(t, healthNormalized, "insert into scheduler_outbox")
	require.Contains(t, healthNormalized, "'account_changed'")

	coldPoolSQL, err := FS.ReadFile("188_quarantine_automated_grok_cold_pool.sql")
	require.NoError(t, err)
	coldPoolNormalized := strings.ToLower(string(coldPoolSQL))
	require.Contains(t, coldPoolNormalized, "automation_quarantine_state")
	require.Contains(t, coldPoolNormalized, "cold_restore_state")
	require.Contains(t, coldPoolNormalized, "schedulable = false")
	require.Contains(t, coldPoolNormalized, "insert into scheduled_test_plans")
	require.Contains(t, coldPoolNormalized, "not exists")
	require.Contains(t, coldPoolNormalized, "grok-4.5")
	require.NotContains(t, coldPoolNormalized, "delete from accounts")
}
