package daemon

import (
	"errors"

	"github.com/Intelligent-Internet/zenith-team/server/pkg/runfailure"
)

// runPreparationPhase is runfailure's vocabulary: the sentence a person
// reads for a failed phase lives beside the phase's name.
type runPreparationPhase = runfailure.PreparationPhase

const (
	runPreparationPhasePrepare              = runfailure.PreparationPhasePrepare
	runPreparationPhaseProviderAvailability = runfailure.PreparationPhaseProviderAvailability
	runPreparationPhaseSkillSnapshot        = runfailure.PreparationPhaseSkillSnapshot
	runPreparationPhaseConnector            = runfailure.PreparationPhaseConnector
	runPreparationPhaseWorkspace            = runfailure.PreparationPhaseWorkspace
	runPreparationPhaseExecution            = runfailure.PreparationPhaseExecution
	runPreparationPhaseStartAuthority       = runfailure.PreparationPhaseStartAuthority
	runPreparationPhaseProviderBackend      = runfailure.PreparationPhaseProviderBackend
	runPreparationPhaseProviderSpawn        = runfailure.PreparationPhaseProviderSpawn
)

type runPreparationError struct {
	phase runPreparationPhase
	err   error
}

func (e *runPreparationError) Error() string { return e.err.Error() }

func (e *runPreparationError) Unwrap() error { return e.err }

func wrapRunPreparationError(phase runPreparationPhase, err error) error {
	if err == nil {
		return nil
	}
	var classified *runPreparationError
	if errors.As(err, &classified) {
		return err
	}
	return &runPreparationError{phase: phase, err: err}
}

func runPreparationFailurePhase(err error) (runPreparationPhase, bool) {
	var classified *runPreparationError
	if !errors.As(err, &classified) || classified.phase == "" {
		return "", false
	}
	return classified.phase, true
}

func runPreparationDiagnosticEvent(err error) string {
	phase, _ := runPreparationFailurePhase(err)
	return runfailure.PreparationCode(phase)
}
