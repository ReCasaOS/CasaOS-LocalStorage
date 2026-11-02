package service

import (
	"errors"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"go.uber.org/zap"
)

// RunSnapRAIDSchedule is called every hour. At the hour that was set it syncs (the diff guard applies: a sync held back is a result to read, not a retry), then,
// on Sundays, scrubs a slice of the array. A sync is not started again within 20 hours of the last one, so a restart in that hour does not run it twice.
func RunSnapRAIDSchedule(now time.Time) {
	snapraidMu.Lock()
	f, ok := loadSnapRAID()
	snapraidMu.Unlock()
	if !ok || f.Config.Hour < 0 || now.Hour() != f.Config.Hour || now.Sub(f.Last["sync"].At) < 20*time.Hour {
		return
	}
	if result := scheduledSnapRAID("sync", now); !result.OK || now.Weekday() != time.Sunday || now.Sub(f.Last["scrub"].At) < 6*24*time.Hour {
		return
	}
	scheduledSnapRAID("scrub", now)
}

// scheduledSnapRAID runs the action; what keeps it from starting (a disk not mounted, the job of a person at work) is a result like any other, so that the
// dashboard shows why the night went by without a sync.
func scheduledSnapRAID(action string, now time.Time) SnapRAIDResult {
	result, err := runSnapRAIDNow(action, SnapRAIDOptions{})
	if err == nil {
		return result
	}
	if errors.Is(err, ErrSnapRAIDBusy) {
		return SnapRAIDResult{Message: err.Error()}
	}
	logger.Error("SnapRAID was not run as scheduled", zap.Error(err), zap.String("action", action))
	return finishSnapRAID(action, SnapRAIDResult{At: now, Message: err.Error()})
}
