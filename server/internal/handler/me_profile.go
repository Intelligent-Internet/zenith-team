package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/util"
)

type updateProfileRequest struct {
	ExpectedRevision int64            `json:"expectedRevision" binding:"revision"`
	DisplayName      json.RawMessage  `json:"displayName"`
	Biography        json.RawMessage  `json:"biography"`
	PhoneCountryISO2 json.RawMessage  `json:"phoneCountryIso2"`
	PhoneNumber      json.RawMessage  `json:"phoneNumber"`
	AvatarDataURL    optional[string] `json:"avatarDataUrl"`
}

func (h *Handler) updateProfile(c *gin.Context) {
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	request, err := bindRequest[updateProfileRequest](c, 144<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	input, err := request.domainInput()
	if err != nil {
		writeBoundaryError(c, boundaryErrorInvalidRequestBody)
		return
	}
	user, err := h.AuthService.UpdateProfile(c.Request.Context(), principal.UserID, input)
	if err != nil {
		var conflict *auth.ProfileRevisionConflictError
		if errors.As(err, &conflict) {
			requestID := util.RequestIDFromContext(c)
			c.JSON(http.StatusConflict, gin.H{
				"detail":      "profile changed elsewhere",
				"code":        "profile_revision_conflict",
				"requestId":   requestID,
				"currentUser": userToResponse(conflict.Current),
			})
			return
		}
		writeAuthError(c, err)
		return
	}
	c.JSON(http.StatusOK, userToResponse(user))
}

func (request updateProfileRequest) domainInput() (auth.UpdateProfileInput, error) {
	displayName, err := decodeRequiredProfileString("displayName", request.DisplayName)
	if err != nil {
		return auth.UpdateProfileInput{}, err
	}
	biography, err := decodeRequiredProfileString("biography", request.Biography)
	if err != nil {
		return auth.UpdateProfileInput{}, err
	}
	phoneCountryISO2, err := decodeRequiredNullableProfileString("phoneCountryIso2", request.PhoneCountryISO2)
	if err != nil {
		return auth.UpdateProfileInput{}, err
	}
	phoneNumber, err := decodeRequiredNullableProfileString("phoneNumber", request.PhoneNumber)
	if err != nil {
		return auth.UpdateProfileInput{}, err
	}
	var avatarDataURL *string
	if request.AvatarDataURL.set {
		avatarDataURL = &request.AvatarDataURL.value
	}
	return auth.UpdateProfileInput{
		ExpectedRevision: request.ExpectedRevision,
		DisplayName:      displayName,
		Biography:        biography,
		PhoneCountryISO2: phoneCountryISO2,
		PhoneNumber:      phoneNumber,
		AvatarDataURL:    avatarDataURL,
	}, nil
}

// decodeRequiredProfileString reads a profile field that must be present and
// a JSON string. The error names the field the way the validator's own
// messages do (`name is required`), so a caller can serve it as the detail.
// A JSON null is refused here: encoding/json would otherwise leave the string
// empty without complaint, and null is the wrong type, not an empty value.
func decodeRequiredProfileString(field string, raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", errors.New(field + " is required")
	}
	var value *string
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return "", errors.New(field + " must be a string")
	}
	return *value, nil
}

// decodeRequiredNullableProfileString reads a profile field that must be
// present and either a JSON string or null.
func decodeRequiredNullableProfileString(field string, raw json.RawMessage) (*string, error) {
	if len(raw) == 0 {
		return nil, errors.New(field + " is required")
	}
	var value *string
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, errors.New(field + " must be a string or null")
	}
	return value, nil
}
