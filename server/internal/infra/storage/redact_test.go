package storage

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"boardly.dev/boardly/server/internal/config"
)

func TestStorageConfigurationTypesRedactPhysicalBackendMaterial(t *testing.T) {
	t.Parallel()

	const secret = "sentinel-private-storage-material"
	values := []any{
		CloudflareRoutes{configuration: CloudflareRoutesConfig{APIToken: secret}},
		CloudflareRoutesConfig{APIToken: secret},
		config.CloudflareConfig{APIToken: secret},
		S3{bucket: secret},
		S3Config{
			Bucket: secret, Region: secret, Endpoint: "https://" + secret + ".example",
			AccessKeyID: secret, SecretAccessKey: secret, SessionToken: secret,
		},
		config.StorageConfig{
			Bucket: secret, Region: secret, Endpoint: "https://" + secret + ".example",
			AccessKeyID: secret, SecretAccessKey: secret, SessionToken: secret,
		},
	}
	for _, value := range values {
		goStringer, ok := value.(fmt.GoStringer)
		if !ok {
			t.Errorf("value %T does not implement fmt.GoStringer", value)
		} else if output := goStringer.GoString(); strings.Contains(output, secret) {
			t.Errorf("%T GoString exposed storage material: %s", value, output)
		}
		for _, format := range []string{"%v", "%+v", "%#v", "%q"} {
			if output := fmt.Sprintf(
				format,
				value,
			); strings.Contains(output, secret) {
				t.Errorf("%T %s exposed storage material: %s", value, format, output)
			}
		}
		var output bytes.Buffer
		slog.New(slog.NewJSONHandler(&output, nil)).Info("storage", slog.Any("value", value))
		if strings.Contains(output.String(), secret) {
			t.Errorf("%T slog exposed storage material: %s", value, output.String())
		}
	}
}
