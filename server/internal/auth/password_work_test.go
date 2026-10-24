package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Fill the budget with real Login calls, then prove that every public entry
// point which performs password work refuses excess work without queueing it.
func TestPasswordWorkBudgetIsSharedByLoginSignupAndReset(t *testing.T) {
	t.Parallel()

	entered := make(chan struct{}, 8)
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	passwords := &passwordWorkStub{verify: func() {
		entered <- struct{}{}
		<-release
	}}
	service := storelessService(t, ServiceOptions{Passwords: passwords})
	service.passwordReset = &passwordResetManager{}
	completed := make(chan error, 4)
	for range 4 {
		go func() {
			_, err := service.Login(
				t.Context(),
				LoginInput{Email: "invalid", Password: "password", Client: ClientWeb},
			)
			completed <- err
		}()
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("login did not start password verification")
		}
	}

	for name, call := range passwordWorkCalls(service) {
		t.Run(name, func(t *testing.T) {
			result := make(chan error, 1)
			go func() { result <- call(t.Context()) }()
			select {
			case err := <-result:
				if !errors.Is(err, ErrPasswordWorkBusy) {
					t.Fatalf("error = %v, want ErrPasswordWorkBusy", err)
				}
			case <-time.After(time.Second):
				t.Error("password work queued instead of rejecting overload")
			}
		})
	}
	if got := passwords.hashCalls.Load(); got != 1 {
		t.Errorf("Hash calls = %d, want only the startup dummy hash", got)
	}
	if got := passwords.verifyCalls.Load(); got != 4 {
		t.Errorf("Verify calls = %d, want four admitted requests", got)
	}

	releaseOnce.Do(func() { close(release) })
	for range 4 {
		if err := <-completed; !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("admitted login error = %v, want ErrInvalidCredentials", err)
		}
	}
	// All slots must be returned after the admitted requests finish.
	_, err := service.Login(t.Context(), LoginInput{Email: "invalid", Password: "password", Client: ClientWeb})
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("login after overload error = %v, want ErrInvalidCredentials", err)
	}
}

func TestCancelledRequestsDoNotStartPasswordWork(t *testing.T) {
	t.Parallel()

	passwords := &passwordWorkStub{}
	service := storelessService(t, ServiceOptions{Passwords: passwords})
	service.passwordReset = &passwordResetManager{}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for name, call := range passwordWorkCalls(service) {
		t.Run(name, func(t *testing.T) {
			if err := call(ctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v, want context.Canceled", err)
			}
		})
	}
	if passwords.hashCalls.Load() != 1 || passwords.verifyCalls.Load() != 0 {
		t.Fatal("cancelled request started password work")
	}
}

func TestFailedPasswordHashReleasesWorkBudget(t *testing.T) {
	t.Parallel()

	passwords := &passwordWorkStub{}
	service := storelessService(t, ServiceOptions{Passwords: passwords})
	service.passwordReset = &passwordResetManager{}
	for range 8 {
		for name, call := range passwordWorkCalls(service) {
			want := errPasswordWorkTestHash
			if name == "login" {
				want = ErrInvalidCredentials
			}
			if err := call(t.Context()); !errors.Is(err, want) {
				t.Fatalf("%s error = %v, want %v", name, err, want)
			}
		}
	}
}

func passwordWorkCalls(service *DefaultAuthService) map[string]func(context.Context) error {
	return map[string]func(context.Context) error{
		"login": func(ctx context.Context) error {
			_, err := service.Login(
				ctx,
				LoginInput{Email: "invalid", Password: "password", Client: ClientWeb},
			)
			return err
		},
		"signup": func(ctx context.Context) error {
			_, err := service.Signup(ctx, SignupInput{
				Email:       "person@example.com",
				Password:    "password",
				DisplayName: "Person",
				Client:      ClientWeb,
			})
			return err
		},
		"reset": func(ctx context.Context) error {
			return service.ResetPassword(ctx, PasswordResetInput{
				Token: base64.RawURLEncoding.EncodeToString(make([]byte, 32)), NewPassword: "password",
			})
		},
	}
}

var errPasswordWorkTestHash = errors.New("test password hashing failure")

type passwordWorkStub struct {
	hashCalls   atomic.Int32
	verifyCalls atomic.Int32
	verify      func()
}

func (s *passwordWorkStub) Hash(string) (string, error) {
	if s.hashCalls.Add(1) == 1 {
		return "startup dummy hash", nil
	}
	return "", errPasswordWorkTestHash
}

func (s *passwordWorkStub) Verify(string, string) bool {
	s.verifyCalls.Add(1)
	if s.verify != nil {
		s.verify()
	}
	return false
}
