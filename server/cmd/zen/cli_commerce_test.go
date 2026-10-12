package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	daemonpkg "boardly.dev/boardly/server/internal/daemon"
)

type commerceDevLeaseStub struct {
	environment []string
	renewed     int
	closed      int
}

func (s *commerceDevLeaseStub) EnvironmentVariables() []string { return s.environment }

func (s *commerceDevLeaseStub) Renew(context.Context) error {
	s.renewed++
	return nil
}

func (s *commerceDevLeaseStub) Close(context.Context) error {
	s.closed++
	return nil
}

type cancelAwareCommerceDevLeaseStub struct {
	started chan struct{}
}

func (s *cancelAwareCommerceDevLeaseStub) EnvironmentVariables() []string { return nil }

func (s *cancelAwareCommerceDevLeaseStub) Renew(ctx context.Context) error {
	close(s.started)
	<-ctx.Done()
	return ctx.Err()
}

func (s *cancelAwareCommerceDevLeaseStub) Close(context.Context) error { return nil }

func TestCommerceDevLeaseRenewalStopsPromptlyOnCommandCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	lease := &cancelAwareCommerceDevLeaseStub{started: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		done <- renewCommerceDevLease(ctx, lease)
	}()
	select {
	case <-lease.started:
	case <-time.After(time.Second):
		t.Fatal("lease renewal did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("renewCommerceDevLease() error = %v, want context cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("lease renewal ignored command cancellation")
	}
}

func TestCommerceDevValidatesManifestInjectsOnlyLeaseAndSupervisesCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BOARDLY_HOME", home)
	t.Setenv("BOARDLY_COMPANY_ID", testCompanyID)
	manifestPath := filepath.Join(t.TempDir(), "boardly-commerce.json")
	manifest := `{
		"schemaVersion":2,
		"site":{"alias":"o-ke","origin":"http://localhost:3000","portalReturnPath":"/payment"},
		"offers":[{"alias":"host_pro_monthly","displayName":"Host Pro","type":"subscription","planGroup":"host_pro","entitlement":"host.pro","amount":99000,"currency":"vnd","interval":"month","automaticTax":false,"successPath":"/payment-processing","cancelPath":"/payment"}]
	}`
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	lease := &commerceDevLeaseStub{environment: []string{
		"BOARDLY_COMMERCE_API_URL=http://localhost:8080/api/v1",
		"BOARDLY_COMMERCE_SITE_ORIGIN=http://localhost:3000",
		"BOARDLY_COMMERCE_SIGNER_CAPABILITY=ephemeral",
	}}
	previousOpen := openCommerceDevLease
	previousRun := runCommerceDevChild
	t.Cleanup(func() {
		openCommerceDevLease = previousOpen
		runCommerceDevChild = previousRun
	})
	var gotCompanyID string
	var gotSite daemonpkg.CommerceDevManifestSite
	openCommerceDevLease = func(
		_ context.Context,
		companyID string,
		site daemonpkg.CommerceDevManifestSite,
		ownerPID int,
	) (commerceDevLease, error) {
		gotCompanyID = companyID
		gotSite = site
		if ownerPID != os.Getpid() {
			t.Fatalf("owner PID = %d, want %d", ownerPID, os.Getpid())
		}
		return lease, nil
	}
	var gotArgs, gotEnvironment []string
	var childBoardlyHome string
	runCommerceDevChild = func(
		_ context.Context,
		args []string,
		environment []string,
		_ commerceDevLease,
		_ commerceDevStreams,
	) error {
		gotArgs = slices.Clone(args)
		gotEnvironment = slices.Clone(environment)
		for _, entry := range environment {
			key, value, _ := strings.Cut(entry, "=")
			if key == "BOARDLY_HOME" {
				childBoardlyHome = value
			}
		}
		if childBoardlyHome == "" || childBoardlyHome == home {
			t.Fatalf("child BOARDLY_HOME = %q, want isolated from %q", childBoardlyHome, home)
		}
		entries, err := os.ReadDir(childBoardlyHome)
		if err != nil || len(entries) != 0 {
			t.Fatalf("child BOARDLY_HOME entries = %v, error = %v; want empty", entries, err)
		}
		return nil
	}

	if _, err := executeCLI(
		t,
		newCommerceCmd,
		"dev", "--manifest", manifestPath, "--", "npm", "run", "dev",
	); err != nil {
		t.Fatalf("commerce dev: %v", err)
	}
	if gotCompanyID != testCompanyID || gotSite.Alias != "o-ke" ||
		gotSite.Origin != "http://localhost:3000" || gotSite.PortalReturnPath != "/payment" {
		t.Fatalf("company = %q, site = %+v", gotCompanyID, gotSite)
	}
	if !slices.Equal(gotArgs, []string{"npm", "run", "dev"}) {
		t.Fatalf("child args = %v", gotArgs)
	}
	for _, want := range lease.environment {
		if !slices.Contains(gotEnvironment, want) {
			t.Fatalf("child environment is missing %q", want)
		}
	}
	if lease.closed != 1 {
		t.Fatalf("lease close count = %d, want 1", lease.closed)
	}
	if _, err := os.Stat(childBoardlyHome); !os.IsNotExist(err) {
		t.Fatalf("child BOARDLY_HOME remains after wrapper exit: %v", err)
	}
}

func TestCommerceDevRevokesLeaseBeforeRemovingHomeAndJoinsCleanupErrors(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BOARDLY_HOME", home)
	t.Setenv("BOARDLY_COMPANY_ID", testCompanyID)
	manifestPath := filepath.Join(t.TempDir(), "boardly-commerce.json")
	manifest := `{
		"schemaVersion":2,
		"site":{"alias":"o-ke","origin":"http://localhost:3000","portalReturnPath":"/payment"},
		"offers":[{"alias":"pro","displayName":"Pro","type":"one_time","entitlement":"pro.access","amount":1,"currency":"vnd","automaticTax":false,"successPath":"/ok","cancelPath":"/payment"}]
	}`
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	closeErr := errors.New("close failed")
	removeErr := errors.New("remove failed")
	lease := &commerceDevLeaseStub{environment: []string{
		"BOARDLY_COMMERCE_API_URL=http://localhost:8080/api/v1",
	}}
	previousOpen := openCommerceDevLease
	previousRun := runCommerceDevChild
	previousRemove := removeCommerceDevChildHome
	t.Cleanup(func() {
		openCommerceDevLease = previousOpen
		runCommerceDevChild = previousRun
		removeCommerceDevChildHome = previousRemove
	})
	var cleanupOrder []string
	openCommerceDevLease = func(
		context.Context,
		string,
		daemonpkg.CommerceDevManifestSite,
		int,
	) (commerceDevLease, error) {
		return &orderedCleanupCommerceDevLease{
			commerceDevLeaseStub: lease,
			close: func() error {
				cleanupOrder = append(cleanupOrder, "close")
				return closeErr
			},
		}, nil
	}
	runCommerceDevChild = func(
		context.Context,
		[]string,
		[]string,
		commerceDevLease,
		commerceDevStreams,
	) error {
		return nil
	}
	removeCommerceDevChildHome = func(path string) error {
		cleanupOrder = append(cleanupOrder, "remove")
		t.Cleanup(func() { _ = os.RemoveAll(path) })
		return removeErr
	}

	_, err := executeCLI(
		t,
		newCommerceCmd,
		"dev", "--manifest", manifestPath, "--", "npm", "run", "dev",
	)
	if !errors.Is(err, closeErr) || !errors.Is(err, removeErr) {
		t.Fatalf("commerce dev cleanup error = %v, want both cleanup causes", err)
	}
	if !slices.Equal(cleanupOrder, []string{"close", "remove"}) {
		t.Fatalf("cleanup order = %v, want lease close before home removal", cleanupOrder)
	}
}

type orderedCleanupCommerceDevLease struct {
	*commerceDevLeaseStub
	close func() error
}

func (s *orderedCleanupCommerceDevLease) Close(context.Context) error {
	s.closed++
	return s.close()
}

func TestCommerceDevRejectsLegacyAndOpenManifestFields(t *testing.T) {
	tests := []struct {
		name     string
		manifest string
	}{
		{
			name: "schema v1",
			manifest: `{"schemaVersion":1,"site":{"alias":"o-ke","origin":"http://localhost:3000","portalReturnPath":"/payment"},` +
				`"offers":[{"alias":"pro","displayName":"Pro","type":"one_time","entitlement":"pro.access","amount":1,"currency":"vnd","automaticTax":false,"successPath":"/ok","cancelPath":"/payment"}]}`,
		},
		{
			name: "removed authMode",
			manifest: `{"schemaVersion":2,"site":{"alias":"o-ke","origin":"http://localhost:3000","authMode":"boardly_sandbox","portalReturnPath":"/payment"},` +
				`"offers":[{"alias":"pro","displayName":"Pro","type":"one_time","entitlement":"pro.access","amount":1,"currency":"vnd","automaticTax":false,"successPath":"/ok","cancelPath":"/payment"}]}`,
		},
		{
			name: "removed oidc",
			manifest: `{"schemaVersion":2,"site":{"alias":"o-ke","origin":"http://localhost:3000","oidc":{"issuer":"https://id.example"},"portalReturnPath":"/payment"},` +
				`"offers":[{"alias":"pro","displayName":"Pro","type":"one_time","entitlement":"pro.access","amount":1,"currency":"vnd","automaticTax":false,"successPath":"/ok","cancelPath":"/payment"}]}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := daemonpkg.ParseCommerceDevManifest([]byte(test.manifest)); err == nil {
				t.Fatal("ParseCommerceDevManifest() error = nil")
			}
		})
	}
}

func TestCommerceDevRejectsInvalidSubscriptionGroups(t *testing.T) {
	tests := []struct {
		name   string
		offers string
	}{
		{
			name: "same plan group changes identity",
			offers: `[` +
				`{"alias":"pro_monthly","displayName":"Pro","type":"subscription","planGroup":"pro","entitlement":"plan.pro","amount":100,"currency":"usd","interval":"month","automaticTax":false,"successPath":"/ok","cancelPath":"/payment"},` +
				`{"alias":"pro_yearly","displayName":"Pro Plus","type":"subscription","planGroup":"pro","entitlement":"plan.pro","amount":1000,"currency":"usd","interval":"year","automaticTax":false,"successPath":"/ok","cancelPath":"/payment"}]`,
		},
		{
			name: "same plan group repeats interval",
			offers: `[` +
				`{"alias":"pro_monthly","displayName":"Pro","type":"subscription","planGroup":"pro","entitlement":"plan.pro","amount":100,"currency":"usd","interval":"month","automaticTax":false,"successPath":"/ok","cancelPath":"/payment"},` +
				`{"alias":"pro_monthly_2","displayName":"Pro","type":"subscription","planGroup":"pro","entitlement":"plan.pro","amount":200,"currency":"usd","interval":"month","automaticTax":false,"successPath":"/ok","cancelPath":"/payment"}]`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := `{"schemaVersion":2,"site":{"alias":"o-ke","origin":"http://localhost:3000","portalReturnPath":"/payment"},"offers":` +
				test.offers + `}`
			if _, err := daemonpkg.ParseCommerceDevManifest([]byte(manifest)); err == nil {
				t.Fatal("ParseCommerceDevManifest() error = nil")
			}
		})
	}
}

func TestMergeCommerceDevEnvironmentReplacesStaleCommerceValues(t *testing.T) {
	got := mergeCommerceDevEnvironment(
		[]string{
			"PATH=/bin",
			"BOARDLY_HOME=/private/bootstrap",
			"BOARDLY_RUN_TOKEN=brt_secret",
			"BOARDLY_COMPANY_ID=company-secret",
			"BOARDLY_COMMERCE_API_URL=stale",
			"BOARDLY_COMMERCE_SIGNER_CAPABILITY=stale",
		},
		[]string{
			"BOARDLY_HOME=/isolated-child",
			"BOARDLY_COMMERCE_API_URL=fresh",
			"BOARDLY_COMMERCE_SIGNER_CAPABILITY=ephemeral",
		},
	)
	joined := strings.Join(got, "\n")
	if strings.Contains(joined, "stale") || !strings.Contains(joined, "BOARDLY_COMMERCE_API_URL=fresh") ||
		!strings.Contains(joined, "BOARDLY_COMMERCE_SIGNER_CAPABILITY=ephemeral") ||
		!strings.Contains(joined, "PATH=/bin") || !strings.Contains(joined, "BOARDLY_HOME=/isolated-child") ||
		strings.Contains(joined, "/private/bootstrap") ||
		strings.Contains(joined, "BOARDLY_RUN_TOKEN") || strings.Contains(joined, "BOARDLY_COMPANY_ID") {
		t.Fatalf("merged environment = %v", got)
	}
}
