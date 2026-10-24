package auth

import "context"

// acquirePasswordWork bounds expensive password operations without creating an
// unbounded queue of waiting requests. Context cancellation cannot interrupt
// Argon2 once it has started; the caller keeps its slot until hashing finishes.
func (s *DefaultAuthService) acquirePasswordWork(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case s.passwordWork <- struct{}{}:
		return nil
	default:
		return ErrPasswordWorkBusy
	}
}

func (s *DefaultAuthService) hashPassword(ctx context.Context, password string) (string, error) {
	if err := s.acquirePasswordWork(ctx); err != nil {
		return "", err
	}
	defer func() { <-s.passwordWork }()
	return s.passwords.Hash(password)
}

func (s *DefaultAuthService) verifyPassword(ctx context.Context, encodedHash, password string) (bool, error) {
	if err := s.acquirePasswordWork(ctx); err != nil {
		return false, err
	}
	defer func() { <-s.passwordWork }()
	return s.passwords.Verify(encodedHash, password), nil
}
