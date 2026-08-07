package wailspkceflow

import (
	"context"
	"errors"
)

// ErrServiceStartupInProgress is returned when ServiceStartup is called while
// another startup attempt is still restoring or reporting its outcome.
var ErrServiceStartupInProgress = errors.New("wailspkceflow: service startup already in progress")

// RestoreErrorPolicy controls how ServiceStartup handles an operational token
// persistence failure. Missing or malformed persisted content is not an error.
type RestoreErrorPolicy uint8

const (
	// RestoreErrorContinue keeps the service running when persisted state cannot
	// be loaded. This compatibility-preserving behavior is the default.
	RestoreErrorContinue RestoreErrorPolicy = iota
	// RestoreErrorFailStartup returns a safely wrapped error from ServiceStartup
	// before installing lifecycle subscriptions or starting background work.
	RestoreErrorFailStartup
)

func (p RestoreErrorPolicy) valid() bool {
	return p == RestoreErrorContinue || p == RestoreErrorFailStartup
}

// RestoreStatus is the frontend-safe, latched outcome of the latest session
// restoration attempt. It never contains backend error text or token data.
type RestoreStatus string

const (
	// RestoreStatusPending means service startup has not completed its latest
	// restoration attempt.
	RestoreStatusPending RestoreStatus = "pending"
	// RestoreStatusRestored means a non-zero persisted or authoritative in-memory
	// token generation was retained or installed. Use AuthStatus for validity.
	RestoreStatusRestored RestoreStatus = "restored"
	// RestoreStatusNoSession means persistence returned no usable stored state.
	// This includes missing and malformed serialized content.
	RestoreStatusNoSession RestoreStatus = "no_session"
	// RestoreStatusPersistenceUnavailable means an operational persistence Load
	// failure prevented restoration.
	RestoreStatusPersistenceUnavailable RestoreStatus = "persistence_unavailable"
)

// EventRestorePersistenceUnavailable is emitted in continue mode when an
// operational persistence Load failure occurs. Its payload is
// RestoreStatusPersistenceUnavailable; the latched RestoreStatus query is
// authoritative because frontend listeners may not exist during startup.
const EventRestorePersistenceUnavailable = "wailspkceflow:restore-persistence-unavailable"

func (s *AuthService) setRestoreStatus(status RestoreStatus) {
	s.restoreMu.Lock()
	s.restoreStatus = status
	s.restoreMu.Unlock()
}

// RestoreStatus returns the latched outcome of the latest session restoration
// attempt. Before startup begins, it returns RestoreStatusPending.
func (s *AuthService) RestoreStatus() RestoreStatus {
	s.restoreMu.RLock()
	status := s.restoreStatus
	s.restoreMu.RUnlock()
	if status == "" {
		return RestoreStatusPending
	}
	return status
}

func (s *AuthService) reportRestoreError(err error, runCtx context.Context) {
	if s.restorePolicy == RestoreErrorContinue {
		if s.logger != nil {
			s.logger.Warn("wailspkceflow: persisted session is unavailable")
		}
		if runCtx != nil && s.bus != nil && s.isCurrentLifecycle(runCtx) {
			s.bus.Emit(
				EventRestorePersistenceUnavailable,
				RestoreStatusPersistenceUnavailable,
			)
		}
	}
	if s.onRestoreError != nil {
		s.onRestoreError(err)
	}
}
