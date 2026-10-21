package config

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The settings families the server reads. A literal that is exactly one of
// these keys in this package is a setting the self-host template must list.
var serverSettingKey = regexp.MustCompile(
	`"((?:APP|ATTACHMENT|AUTH|CLOUD_RUN|CLOUDFLARE|GCP|GOOGLE|HUDDLE|HUDDLES|LIVEKIT|MAILBOX|SOCIAL|STORAGE|ZENITH_STUDIO|ZENITH_TEAM)_[A-Z0-9_]*[A-Z0-9])"`,
)

var templateKey = regexp.MustCompile(`(?m)^#?([A-Z][A-Z0-9_]*)=`)

// composeOnlySettings are read by deploy/docker-compose.production.yml, not by
// the server.
var composeOnlySettings = []string{
	"APP_BIND_ADDRESS", "APP_HTTP_PORT", "APP_POSTGRES_PASSWORD",
	"APP_SERVER_IMAGE", "APP_WEB_IMAGE", "APP_WEB_PORT",
}

// builtSettings are keys the code assembles at runtime (social.go joins
// SOCIAL_, the platform, and the credential name).
var builtSettings = regexp.MustCompile(`^SOCIAL_[A-Z]+_CLIENT_(ID|SECRET)$`)

func TestSelfHostTemplateListsEverySetting(t *testing.T) {
	t.Parallel()
	template := templateKeys(t, "../../../deploy/production.env.example")
	for _, key := range settingKeys(t, ".", serverSettingKey) {
		if !slices.Contains(template, key) {
			t.Errorf("deploy/production.env.example does not list %s", key)
		}
	}
	compose := readFile(t, "../../../deploy/docker-compose.production.yml")
	for _, key := range composeOnlySettings {
		if !strings.Contains(compose, "${"+key) {
			t.Errorf("compose-only setting %s is not used by the compose file", key)
		}
	}
}

func TestEnvTemplatesListOnlyRealSettings(t *testing.T) {
	t.Parallel()
	used := settingKeys(t, "../..", regexp.MustCompile(`"([A-Z][A-Z0-9]*(?:_[A-Z0-9]+)+)"`))
	for _, path := range []string{"../../../deploy/production.env.example", "../../.env.example"} {
		for _, key := range templateKeys(t, path) {
			if !slices.Contains(used, key) && !slices.Contains(composeOnlySettings, key) &&
				!builtSettings.MatchString(key) {
				t.Errorf("%s lists %s, which nothing reads", filepath.Base(path), key)
			}
		}
	}
}

func templateKeys(t *testing.T, path string) []string {
	t.Helper()
	var keys []string
	for _, match := range templateKey.FindAllStringSubmatch(readFile(t, path), -1) {
		keys = append(keys, match[1])
	}
	return keys
}

// settingKeys collects the quoted keys in the non-test Go files under root.
func settingKeys(t *testing.T, root string, pattern *regexp.Regexp) []string {
	t.Helper()
	var keys []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") ||
			strings.HasSuffix(path, "_test.go") {
			return err
		}
		for _, match := range pattern.FindAllStringSubmatch(readFile(t, path), -1) {
			if !slices.Contains(keys, match[1]) {
				keys = append(keys, match[1])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
