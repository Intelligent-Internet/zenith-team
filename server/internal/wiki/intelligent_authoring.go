package wiki

import "errors"

// ErrUIAuthoringDisabled leaves existing documents intact during a controlled
// rollout or rollback. It does not prohibit reading or exporting saved evidence.
var ErrUIAuthoringDisabled = errors.New("Intelligent UI authoring is temporarily disabled")

// UIAuthoringPolicy is immutable deployment policy, supplied by the composition
// root. Its zero value preserves existing behavior. Disabled freezes schema-2
// authored documents and prevents introducing schema-2 content into schema 1.
type UIAuthoringPolicy struct {
	Disabled bool
}

func (p UIAuthoringPolicy) AllowsSchema(schema int) bool {
	return !p.Disabled || schema < 2
}

func (p UIAuthoringPolicy) Check(savedSchema int, next BlockNode) error {
	if p.Disabled && (savedSchema >= 2 || UIDocumentSchema(next) >= 2) {
		return ErrUIAuthoringDisabled
	}
	return nil
}
