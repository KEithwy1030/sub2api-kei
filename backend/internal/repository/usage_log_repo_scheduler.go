package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (r *usageLogRepository) ListRecentOpenAIAccountRuntimeStatSeeds(
	ctx context.Context,
	since time.Time,
	perAccountLimit int,
) ([]service.OpenAIAccountRuntimeStatSeed, error) {
	if r == nil || r.sql == nil {
		return nil, nil
	}
	if perAccountLimit <= 0 {
		perAccountLimit = 8
	}
	rows, err := r.sql.QueryContext(ctx, `
WITH recent AS (
  SELECT
    ul.account_id,
    ul.first_token_ms,
    ul.created_at,
    ROW_NUMBER() OVER (PARTITION BY ul.account_id ORDER BY ul.created_at DESC) AS sample_rank
  FROM usage_logs ul
  INNER JOIN accounts a ON a.id = ul.account_id
  WHERE ul.created_at >= $1
    AND ul.first_token_ms IS NOT NULL
    AND ul.first_token_ms > 0
    AND a.deleted_at IS NULL
    AND a.platform IN ('openai', 'grok')
)
SELECT
  account_id,
  AVG(first_token_ms)::double precision AS average_ttft_ms,
  COUNT(*)::integer AS sample_count,
  MAX(created_at) AS last_observed_at
FROM recent
WHERE sample_rank <= $2
GROUP BY account_id
ORDER BY account_id`, since, perAccountLimit)
	if err != nil {
		return nil, fmt.Errorf("list recent OpenAI account runtime stat seeds: %w", err)
	}
	defer rows.Close()

	seeds := make([]service.OpenAIAccountRuntimeStatSeed, 0)
	for rows.Next() {
		var seed service.OpenAIAccountRuntimeStatSeed
		if err := rows.Scan(&seed.AccountID, &seed.AverageTTFTMs, &seed.SampleCount, &seed.LastObservedAt); err != nil {
			return nil, fmt.Errorf("scan OpenAI account runtime stat seed: %w", err)
		}
		seeds = append(seeds, seed)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate OpenAI account runtime stat seeds: %w", err)
	}
	return seeds, nil
}
