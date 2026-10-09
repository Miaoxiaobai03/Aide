package coach

import (
	"context"
	"offerpilot/backend/internal/interview"
	"time"
)

// RecoverAfterRestart runs before serving requests, with exclusive process
// ownership of this installation's database. It never reissues a model call.
func (s *Service) RecoverAfterRestart(ctx context.Context) (int, error) {
	repo, ok := s.repo.(interface {
		PracticeDirectories(context.Context) ([]interview.PracticeInfo, error)
	})
	if !ok {
		return 0, nil
	}
	items, err := repo.PracticeDirectories(ctx)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, item := range items {
		for _, p := range item.Pages {
			if !p.Busy {
				continue
			}
			_, err = s.update(ctx, p.ID, func(c *Conversation) error {
				if c.Pending == nil {
					return nil
				}
				c.Pending.Until = time.Time{}
				return active(c)
			})
			if err != nil {
				return count, err
			}
			count++
		}
	}
	return count, nil
}
