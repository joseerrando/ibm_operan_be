package service

import (
	"context"
	"log/slog"
	"time"
)

// RunWorker generates daily dose slots and raises missed-dose alerts every 5 minutes
// until ctx is cancelled. Tomorrow's slots are created early so midnight is covered.
func (s *Service) RunWorker(ctx context.Context, every time.Duration) {
	tick := func() {
		c, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		now := s.now()
		if err := s.GenerateAll(c, now); err != nil {
			slog.Error("worker: generate today", "err", err)
		}
		if now.In(s.Loc).Hour() >= 23 {
			if err := s.GenerateAll(c, now.Add(2*time.Hour)); err != nil {
				slog.Error("worker: generate tomorrow", "err", err)
			}
		}
		if n, err := s.CheckMissed(c, now); err != nil {
			slog.Error("worker: missed doses", "err", err)
		} else if n > 0 {
			slog.Info("worker: dosis terlewat ditandai", "count", n)
		}
	}
	tick()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tick()
		}
	}
}
