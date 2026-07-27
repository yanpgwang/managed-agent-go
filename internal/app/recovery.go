package app

import "context"

func (s *SessionService) Recover(ctx context.Context) error {
	sessionIDs, err := s.runs.Recover(ctx)
	if err != nil {
		return err
	}
	for _, sessionID := range sessionIDs {
		s.kick(sessionID)
	}
	return nil
}
