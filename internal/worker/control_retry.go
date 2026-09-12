package worker

import (
	"context"
	"time"

	"go.uber.org/zap"
)

func waitScheduledControlRetry(ctx context.Context, store *journalStore,
	journal *runJournal, logger *zap.Logger, err error,
) bool {
	if journal == nil {
		abandonRunJournal(store, journal)
		return false
	}
	wait, attempts, stop := journal.scheduleControlRetry(time.Now())
	if store != nil {
		_ = store.save(journal)
	}
	if stop {
		if logger != nil {
			logger.Warn("补报达到次数或时长上限，停止补报",
				zap.Int("attempts", attempts), zap.Error(err))
		}
		abandonRunJournal(store, journal)
		return false
	}
	return waitContext(ctx, wait)
}
