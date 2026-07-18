package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestUsageLogRepositoryListRecentOpenAIAccountRuntimeStatSeeds(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}
	since := time.Date(2026, 7, 18, 8, 0, 0, 0, time.UTC)
	lastObserved := since.Add(20 * time.Minute)

	mock.ExpectQuery("WITH recent AS").
		WithArgs(since, 8).
		WillReturnRows(sqlmock.NewRows([]string{"account_id", "average_ttft_ms", "sample_count", "last_observed_at"}).
			AddRow(int64(301), float64(24500), 4, lastObserved).
			AddRow(int64(302), float64(3200), 2, lastObserved))

	seeds, err := repo.ListRecentOpenAIAccountRuntimeStatSeeds(context.Background(), since, 8)
	require.NoError(t, err)
	require.Len(t, seeds, 2)
	require.Equal(t, int64(301), seeds[0].AccountID)
	require.InDelta(t, 24500, seeds[0].AverageTTFTMs, 0.001)
	require.Equal(t, 4, seeds[0].SampleCount)
	require.Equal(t, lastObserved, seeds[0].LastObservedAt)
	require.NoError(t, mock.ExpectationsWereMet())
}
