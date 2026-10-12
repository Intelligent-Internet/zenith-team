package conversation

import "errors"

var (
	ErrStudioEditorNotFound             = errors.New("studio editor notfound")
	ErrStudioEditorInvalid              = errors.New("studio editor invalid")
	ErrStudioEditorPermissionDenied     = errors.New("studio editor permissiondenied")
	ErrStudioEditorGenerationConflict   = errors.New("studio editor generationconflict")
	ErrStudioEditorBindingConflict      = errors.New("studio editor bindingconflict")
	ErrStudioEditorOriginConflict       = errors.New("studio editor originconflict")
	ErrStudioEditorAccountConflict      = errors.New("studio editor accountconflict")
	ErrStudioEditorRequestConflict      = errors.New("studio editor requestconflict")
	ErrStudioEditorReauthRequired       = errors.New("studio editor reauthrequired")
	ErrStudioEditorUpstreamDenied       = errors.New("studio editor upstreamdenied")
	ErrStudioEditorSourceUnavailable    = errors.New("studio editor sourceunavailable")
	ErrStudioEditorRuntimeUnavailable   = errors.New("studio editor runtimeunavailable")
	ErrStudioEditorAuthorityUnavailable = errors.New("studio editor authorityunavailable")
	ErrStudioEditorUnsupported          = errors.New("studio editor unsupported")
	ErrStudioEditorRateLimited          = errors.New("studio editor ratelimited")
	ErrStudioEditorOutcomeUnknown       = errors.New("studio editor outcomeunknown")
	ErrStudioEditorRecoveryRequired     = errors.New("studio editor recoveryrequired")
	ErrStudioEditorUpstreamInvalid      = errors.New("studio editor upstreaminvalid")
)
