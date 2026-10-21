package auth

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
)

func profileAvatarFixture(t *testing.T, format string, size int) string {
	t.Helper()
	var data bytes.Buffer
	value := image.NewNRGBA(image.Rect(0, 0, size, size))
	var err error
	if format == "jpeg" {
		err = jpeg.Encode(&data, value, nil)
	} else {
		err = png.Encode(&data, value)
	}
	if err != nil {
		t.Fatal(err)
	}
	return "data:image/" + format + ";base64," + base64.StdEncoding.EncodeToString(data.Bytes())
}

func TestProfileAvatarValidatesImageDataAndDimensions(t *testing.T) {
	t.Parallel()
	pngAvatar := profileAvatarFixture(t, "png", 256)
	_, pngEncoded, _ := strings.Cut(pngAvatar, ",")
	pngBytes, err := base64.StdEncoding.DecodeString(pngEncoded)
	if err != nil {
		t.Fatal(err)
	}
	truncated := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes[:len(pngBytes)/2])
	for _, test := range []struct {
		name      string
		value     string
		wantError bool
	}{
		{"PNG", pngAvatar, false},
		{"JPEG", profileAvatarFixture(t, "jpeg", 256), false},
		{"empty", "", true},
		{"remote URL", "https://example.com/avatar.png", true},
		{"SVG", "data:image/svg+xml;base64,PHN2Zy8+", true},
		{"invalid base64", "data:image/png;base64,?", true},
		{"not an image", "data:image/png;base64,aGVsbG8=", true},
		{"truncated image", truncated, true},
		{"wrong format", strings.Replace(pngAvatar, "image/png", "image/jpeg", 1), true},
		{"large dimensions", profileAvatarFixture(t, "png", 257), true},
		{"large bytes", "data:image/png;base64," + base64.StdEncoding.EncodeToString(make([]byte, 96<<10+1)), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := normalizeProfileInput(
				UpdateProfileInput{DisplayName: "Member", AvatarDataURL: &test.value},
			)
			if test.wantError {
				if !errors.Is(err, ErrInvalidInput) {
					t.Fatalf("avatar error = %v, want ErrInvalidInput", err)
				}
				return
			}
			if err != nil || got.avatarDataURL == nil || *got.avatarDataURL != test.value {
				t.Fatalf("valid avatar changed or rejected: %v", err)
			}
		})
	}
}
