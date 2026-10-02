package api

import (
	"context"
	"log"
	"time"
)

func (a *API) runSessionWorker(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	a.closeExpiredSessions(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.closeExpiredSessions(ctx)
		}
	}
}

func (a *API) closeExpiredSessions(ctx context.Context) {
	const q = `
UPDATE sessions
SET status = 'finished',
    ended_at = NOW(),
    actual_duration_minutes = GREATEST(
        0,
        FLOOR(EXTRACT(EPOCH FROM (NOW() - started_at)) / 60 - paused_minutes)::INTEGER
    ),
    updated_at = NOW()
WHERE status = 'active'
  AND started_at IS NOT NULL
  AND planned_end_at IS NOT NULL
  AND planned_end_at <= NOW()`

	if _, err := a.pool.Exec(ctx, q); err != nil {
		log.Printf("session worker: %v", err)
	}
}
