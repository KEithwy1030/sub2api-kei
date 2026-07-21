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
}
