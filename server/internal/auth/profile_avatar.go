package auth

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"strings"
)

// Profile avatars are bounded thumbnails, so the existing User projection can
// carry them across Companies without granting access to a Company's uploads.
func validateProfileAvatarDataURL(value string) error {
	if len(value) > (128<<10)+32 {
		return fmt.Errorf("%w: avatar must be at most 96 KiB", ErrInvalidInput)
	}
	header, encoded, ok := strings.Cut(value, ",")
	if !ok || (header != "data:image/png;base64" && header != "data:image/jpeg;base64") {
		return fmt.Errorf("%w: avatar must be a PNG or JPEG data URL", ErrInvalidInput)
	}
	data, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(data) == 0 || len(data) > 96<<10 {
		return fmt.Errorf("%w: avatar must contain valid image data up to 96 KiB", ErrInvalidInput)
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || header != "data:image/"+format+";base64" ||
		config.Width < 1 || config.Height < 1 || config.Width > 256 || config.Height > 256 {
		return fmt.Errorf("%w: avatar dimensions must be at most 256 by 256 pixels", ErrInvalidInput)
	}
	if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
		return fmt.Errorf("%w: avatar image is incomplete", ErrInvalidInput)
	}
	return nil
}
