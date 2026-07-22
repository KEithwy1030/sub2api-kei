package repository

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAccountRepository_SetModelRateLimitIfInactiveUsesConditionalUpdate(t *testing.T) {
	exec := &recordingSQLExecutor{result: rowsAffectedResult(0)}
	repo := newAccountRepositoryWithSQL(nil, exec, nil)
	now := time.Now().UTC()

	applied, err := repo.SetModelRateLimitIfInactive(
		context.Background(),
		42,
		"grok-4.5",
		now.Add(15*time.Minute),
		"grok slow ttft quarantine: 25000ms (2 consecutive)",
		now,
	)

	require.NoError(t, err)
	require.False(t, applied)
	require.Len(t, exec.execQueries, 1)
	normalized := normalizeSQLWhitespace(exec.execQueries[0])
	require.Contains(t, normalized, "UPDATE accounts AS a")
	require.Contains(t, normalized, "rate_limit_reset_at')::timestamptz <= $4")
	require.NotContains(t, strings.Join(exec.execQueries, "\n"), "scheduler_outbox")
}

func TestAccountRepository_ClearModelRateLimitsExceptActiveReasonPrefixUsesAtomicFilter(t *testing.T) {
	exec := &recordingSQLExecutor{result: rowsAffectedResult(1)}
	repo := newAccountRepositoryWithSQL(nil, exec, nil)

	err := repo.ClearModelRateLimitsExceptActiveReasonPrefix(
		context.Background(),
		42,
		"grok slow ttft quarantine:",
		time.Now().UTC(),
	)

	require.NoError(t, err)
	require.GreaterOrEqual(t, len(exec.execQueries), 1)
	normalized := normalizeSQLWhitespace(exec.execQueries[0])
	require.Contains(t, normalized, "WITH locked AS MATERIALIZED")
	require.Contains(t, normalized, "FOR UPDATE")
	require.Contains(t, normalized, "preserved AS (")
	require.Contains(t, normalized, "jsonb_object_agg(entry.key, entry.value) FILTER")
	require.Contains(t, normalized, "rate_limit_reset_at')::timestamptz > $3")
}
