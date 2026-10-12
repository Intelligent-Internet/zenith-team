package daemon

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/golang-jwt/jwt/v5"
)

type commerceDevRegistrarStub struct {
	request  RegisterLocalCommerceSigningKeyRequest
	response RegisterLocalCommerceSigningKeyResponse
	err      error
	runToken string
}

type rejectedCommerceDevRegistrarStub struct {
	requests []RegisterLocalCommerceSigningKeyRequest
}

type blockingCommerceDevRegistrarStub struct {
	started chan RegisterLocalCommerceSigningKeyRequest
}

type manuallyBlockingCommerceDevRegistrarStub struct {
	started chan RegisterLocalCommerceSigningKeyRequest
	release chan struct{}
}

func testCommerceDevBootstrapAuthority() commerceDevBootstrapAuthority {
	return commerceDevBootstrapAuthority{
		scoped:       true,
		companyID:    testCompanyIDForCommerceDev,
		runID:        uuid.MustParse("00000000-0000-4000-8000-000000000001"),
		taskID:       "0198aa00-0000-7000-8000-0000000000c0",
		runAuthToken: testRunAuthTokenForCommerceDev,
	}
}

func (s *rejectedCommerceDevRegistrarStub) RegisterLocalCommerceSigningKey(
	_ context.Context,
	_ string,
	_ string,
	_ string,
	request RegisterLocalCommerceSigningKeyRequest,
) (RegisterLocalCommerceSigningKeyResponse, error) {
	s.requests = append(s.requests, request)
	if len(s.requests) == 1 {
		return RegisterLocalCommerceSigningKeyResponse{}, &requestError{
			Method: http.MethodPost, StatusCode: http.StatusConflict,
			Code: "commerce_site_verification_failed",
		}
	}
	return RegisterLocalCommerceSigningKeyResponse{
		SchemaVersion: 1,
		SiteID:        "0198aa00-0000-7000-8000-0000000000c1",
		Environment:   "sandbox",
		KeyID:         request.Fingerprint,
	}, nil
}

func (s *commerceDevRegistrarStub) RegisterLocalCommerceSigningKey(
	_ context.Context,
	runToken string,
	_ string,
	_ string,
	request RegisterLocalCommerceSigningKeyRequest,
) (RegisterLocalCommerceSigningKeyResponse, error) {
	s.runToken = runToken
	s.request = request
	if s.response.SiteID == "" {
		return RegisterLocalCommerceSigningKeyResponse{
			SchemaVersion: 1,
			SiteID:        "0198aa00-0000-7000-8000-0000000000c1",
			Environment:   "sandbox",
			KeyID:         request.Fingerprint,
		}, s.err
	}
	return s.response, s.err
}

func (s *blockingCommerceDevRegistrarStub) RegisterLocalCommerceSigningKey(
	ctx context.Context,
	_ string,
	_ string,
	_ string,
	request RegisterLocalCommerceSigningKeyRequest,
) (RegisterLocalCommerceSigningKeyResponse, error) {
	s.started <- request
	<-ctx.Done()
	return RegisterLocalCommerceSigningKeyResponse{}, ctx.Err()
}

func (s *manuallyBlockingCommerceDevRegistrarStub) RegisterLocalCommerceSigningKey(
	_ context.Context,
	_ string,
	_ string,
	_ string,
	request RegisterLocalCommerceSigningKeyRequest,
) (RegisterLocalCommerceSigningKeyResponse, error) {
	s.started <- request
	<-s.release
	return RegisterLocalCommerceSigningKeyResponse{}, context.Canceled
}

func TestCommerceDevServiceRejectsUnscopedLeaseIssuance(t *testing.T) {
	t.Parallel()

	registrar := &commerceDevRegistrarStub{}
	service := newCommerceDevService(registrar, "http://localhost:8080")
	service.processAlive = func(int) bool { return true }
	_, err := service.createLeaseForAuthority(
		t.Context(),
		testCompanyIDForCommerceDev,
		CommerceDevManifestSite{
			Alias: "o-ke", Origin: "http://localhost:3000", PortalReturnPath: "/payment",
		},
		4321,
		"http://127.0.0.1:19713",
		commerceDevBootstrapAuthority{},
	)
	if !errors.Is(err, errCommerceDevLeaseInvalid) {
		t.Fatalf("createLeaseForAuthority() error = %v, want invalid unscoped authority", err)
	}
	if registrar.request.SchemaVersion != 0 {
		t.Fatalf("unscoped authority reached registrar: %+v", registrar.request)
	}
}

func TestCommerceDevServiceKeepsPrivateKeysInMemoryAndReusesThemPerSite(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	registrar := &commerceDevRegistrarStub{}
	service := newCommerceDevService(registrar, "http://localhost:8080")
	service.processAlive = func(int) bool { return true }
	first, err := service.createLeaseForAuthority(
		t.Context(),
		testCompanyIDForCommerceDev,
		CommerceDevManifestSite{
			Alias: "o-ke", Origin: "http://localhost:3000", PortalReturnPath: "/payment",
		},
		4321,
		"http://127.0.0.1:19713",
		testCommerceDevBootstrapAuthority(),
	)
	if err != nil {
		t.Fatalf("first createLease() error = %v", err)
	}
	second, err := service.createLeaseForAuthority(
		t.Context(),
		testCompanyIDForCommerceDev,
		CommerceDevManifestSite{
			Alias: "o-ke", Origin: "http://localhost:3000", PortalReturnPath: "/payment",
		},
		4322,
		"http://127.0.0.1:19713",
		testCommerceDevBootstrapAuthority(),
	)
	if err != nil {
		t.Fatalf("second createLease() error = %v", err)
	}
	if first.KeyID != second.KeyID {
		t.Fatalf("same Site key IDs = %q and %q, want one daemon-memory key", first.KeyID, second.KeyID)
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatalf("read Boardly home: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("Boardly home contains persistent signing material: %v", entries)
	}
}

func TestCommerceDevServiceSeparatesInMemoryKeysBySiteBinding(t *testing.T) {
	t.Parallel()

	registrar := &commerceDevRegistrarStub{}
	service := newCommerceDevService(registrar, "http://localhost:8080")
	service.processAlive = func(int) bool { return true }
	first, err := service.createLeaseForAuthority(
		t.Context(), testCompanyIDForCommerceDev,
		CommerceDevManifestSite{
			Alias: "o-ke", Origin: "http://localhost:3000", PortalReturnPath: "/payment",
		},
		4321, "http://127.0.0.1:19713", testCommerceDevBootstrapAuthority(),
	)
	if err != nil {
		t.Fatalf("first createLease(): %v", err)
	}
	second, err := service.createLeaseForAuthority(
		t.Context(), testCompanyIDForCommerceDev,
		CommerceDevManifestSite{
			Alias: "o-ke-preview", Origin: "http://localhost:3001", PortalReturnPath: "/payment",
		},
		4322, "http://127.0.0.1:19713", testCommerceDevBootstrapAuthority(),
	)
	if err != nil {
		t.Fatalf("second createLease(): %v", err)
	}
	if first.KeyID == second.KeyID {
		t.Fatal("different Site bindings reused one private key")
	}
}

func TestCommerceDevServiceReplacesExplicitlyRejectedLocalKeyOnce(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	registrar := &rejectedCommerceDevRegistrarStub{}
	service := newCommerceDevService(registrar, "http://localhost:8080")
	service.processAlive = func(int) bool { return true }
	lease, err := service.createLeaseForAuthority(
		t.Context(),
		testCompanyIDForCommerceDev,
		CommerceDevManifestSite{
			Alias: "o-ke", Origin: "http://localhost:3000", PortalReturnPath: "/payment",
		},
		4321,
		"http://127.0.0.1:19713",
		testCommerceDevBootstrapAuthority(),
	)
	if err != nil {
		t.Fatalf("createLease() recovery error = %v", err)
	}
	if len(registrar.requests) != 2 ||
		registrar.requests[0].Fingerprint == registrar.requests[1].Fingerprint ||
		lease.KeyID != registrar.requests[1].Fingerprint {
		t.Fatalf("recovery requests = %#v, lease key = %q", registrar.requests, lease.KeyID)
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatalf("read Boardly home: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("rejected key replacement persisted private material: %v", entries)
	}
}

func TestCommerceDevServiceSignsOnlyLiveProcessBoundLeases(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.August, 29, 8, 0, 0, 123456789, time.UTC)
	registrar := &commerceDevRegistrarStub{}
	ownerAlive := true
	service := newCommerceDevService(registrar, "http://localhost:8080")
	service.now = func() time.Time { return now }
	service.processAlive = func(pid int) bool { return ownerAlive && pid == 4321 }

	lease, err := service.createLeaseForAuthority(t.Context(), testCompanyIDForCommerceDev, CommerceDevManifestSite{
		Alias: "o-ke", Origin: "http://localhost:3000", PortalReturnPath: "/payment",
	}, 4321, "http://127.0.0.1:19713", testCommerceDevBootstrapAuthority())
	if err != nil {
		t.Fatalf("createLease() error = %v", err)
	}
	if registrar.request.SchemaVersion != 1 || registrar.request.Origin != "http://localhost:3000" ||
		registrar.request.PublicJWK.Kty != "EC" || registrar.request.PublicJWK.Crv != "P-256" ||
		registrar.request.Fingerprint == "" || lease.KeyID != registrar.request.Fingerprint {
		t.Fatalf("registration request = %+v, lease = %+v", registrar.request, lease)
	}

	claims := CommerceDevSignRequest{
		SchemaVersion:         1,
		Subject:               "customer-1",
		SessionID:             "session-1",
		AuthenticatedAt:       now.Add(-time.Minute).Format(time.RFC3339),
		AuthenticationMethods: []string{"password"},
		Method:                http.MethodPost,
		Path:                  "/api/v1/site-commerce/checkout-sessions",
		OperationID:           "0198aa00-0000-7000-8000-0000000000f1",
		BodySHA256:            strings.Repeat("a", 64),
		Nonce:                 strings.Repeat("n", 43),
	}
	signed, err := service.sign(lease.SignerCapability, claims)
	if err != nil {
		t.Fatalf("sign() error = %v", err)
	}
	parsed, err := jwt.Parse(signed.Token, func(_ *jwt.Token) (any, error) {
		return commercePublicKeyForTest(t, registrar.request.PublicJWK), nil
	}, jwt.WithValidMethods([]string{"ES256"}), jwt.WithoutClaimsValidation())
	if err != nil || !parsed.Valid {
		t.Fatalf("parse signed JWT: token valid = %v, error = %v", parsed != nil && parsed.Valid, err)
	}
	if parsed.Header["typ"] != "boardly-site-request+jwt" || parsed.Header["kid"] != lease.KeyID {
		t.Fatalf("JWT header = %+v", parsed.Header)
	}
	metadata, err := service.metadata(lease.SignerCapability)
	if err != nil || metadata.KeyID != lease.KeyID || metadata.PublicJWK != registrar.request.PublicJWK {
		t.Fatalf("metadata = %+v, error = %v", metadata, err)
	}
	mapClaims, ok := parsed.Claims.(jwt.MapClaims)
	audience, audienceOK := mapClaims["aud"].([]any)
	if !ok || !audienceOK || len(audience) != 1 || audience[0] != "boardly-site-commerce" ||
		mapClaims["iss"] != lease.SiteID ||
		mapClaims["schemaVersion"] != float64(1) || mapClaims["action"] != "checkout" ||
		mapClaims["environment"] != "sandbox" || mapClaims["origin"] != "http://localhost:3000" ||
		mapClaims["path"] != claims.Path || mapClaims["bodySha256"] != claims.BodySHA256 {
		t.Fatalf("JWT claims = %+v", parsed.Claims)
	}
	if signed.ExpiresAt.Nanosecond() != 0 || mapClaims["exp"] != float64(signed.ExpiresAt.Unix()) {
		t.Fatalf("JWT exp = %v, response expiresAt = %s", mapClaims["exp"], signed.ExpiresAt)
	}
	issuedAt, issuedAtOK := mapClaims["iat"].(float64)
	if !issuedAtOK || signed.ExpiresAt.Unix()-int64(issuedAt) != int64(commerceDevJWTLifetime/time.Second) ||
		commerceDevJWTLifetime != 55*time.Second {
		t.Fatalf(
			"JWT lifetime = %v, want clock-skew-safe 55s",
			time.Duration(signed.ExpiresAt.Unix()-int64(issuedAt))*time.Second,
		)
	}

	now = lease.ExpiresAt
	if _, err := service.sign(lease.SignerCapability, claims); err == nil {
		t.Fatal("sign() accepted an expired lease")
	}
	now = lease.ExpiresAt.Add(-time.Second)
	ownerAlive = false
	if _, err := service.sign(lease.SignerCapability, claims); err == nil {
		t.Fatal("sign() accepted a lease after its owner process exited")
	}
}

func TestCommerceDevServiceRejectsCrossBoundaryAndMalformedClaims(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.August, 29, 8, 0, 0, 0, time.UTC)
	service := newCommerceDevService(&commerceDevRegistrarStub{}, "http://localhost:8080")
	service.now = func() time.Time { return now }
	service.processAlive = func(int) bool { return true }
	lease, err := service.createLeaseForAuthority(t.Context(), testCompanyIDForCommerceDev, CommerceDevManifestSite{
		Alias: "o-ke", Origin: "http://localhost:3000", PortalReturnPath: "/payment",
	}, 4321, "http://127.0.0.1:19713", testCommerceDevBootstrapAuthority())
	if err != nil {
		t.Fatalf("createLease() error = %v", err)
	}

	valid := CommerceDevSignRequest{
		SchemaVersion: 1, Subject: "customer-1", SessionID: "session-1",
		AuthenticatedAt: now.Format(time.RFC3339), AuthenticationMethods: []string{"password"},
		Method: http.MethodPost, Path: "/api/v1/site-commerce/entitlements/verify",
		OperationID: "0198aa00-0000-7000-8000-0000000000f2",
		BodySHA256:  strings.Repeat("b", 64),
	}
	tests := []struct {
		name       string
		capability string
		mutate     func(*CommerceDevSignRequest)
	}{
		{name: "wrong capability", capability: "not-the-lease"},
		{
			name:       "challenge is unsigned",
			capability: lease.SignerCapability,
			mutate: func(v *CommerceDevSignRequest) {
				v.Path = "/api/v1/site-commerce/challenges"
			},
		},
		{
			name:       "GET",
			capability: lease.SignerCapability,
			mutate:     func(v *CommerceDevSignRequest) { v.Method = http.MethodGet },
		},
		{
			name:       "uppercase digest",
			capability: lease.SignerCapability,
			mutate:     func(v *CommerceDevSignRequest) { v.BodySHA256 = strings.Repeat("A", 64) },
		},
		{
			name:       "checkout without nonce",
			capability: lease.SignerCapability,
			mutate: func(v *CommerceDevSignRequest) {
				v.Path = "/api/v1/site-commerce/checkout-sessions"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request := valid
			if test.mutate != nil {
				test.mutate(&request)
			}
			if _, err := service.sign(test.capability, request); err == nil {
				t.Fatal("sign() error = nil")
			}
		})
	}
}

func TestCommerceDevControlConfigIsAtomicPrivateAndLoadable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BOARDLY_HOME", home)
	capability, err := newCommerceDevCapability()
	if err != nil {
		t.Fatalf("newCommerceDevCapability() error = %v", err)
	}
	path, err := writeCommerceDevControlConfig(home, "http://127.0.0.1:19713", capability)
	if err != nil {
		t.Fatalf("writeCommerceDevControlConfig() error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat control config: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("control config permissions = %04o, want 0600", got)
	}
	config, err := LoadCommerceDevControlConfig()
	if err != nil {
		t.Fatalf("LoadCommerceDevControlConfig() error = %v", err)
	}
	if config.Endpoint != "http://127.0.0.1:19713" || config.Capability != capability {
		t.Fatalf("control config = %+v", config)
	}
}

func TestCommerceDevLeaseLifecycleEndsWithScopedBootstrap(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BOARDLY_HOME", home)
	service := newCommerceDevService(&commerceDevRegistrarStub{}, "http://localhost:8080")
	const longLivedForeignPID = 1
	service.processAlive = func(pid int) bool {
		return pid == longLivedForeignPID || pid == os.Getpid()
	}
	d := &Daemon{
		logger:      testLogger(),
		commerceDev: service,
	}
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()
	mux.Handle(commerceDevLeaseCreatePath, d.commerceDevLeaseCreateHandler(server.URL))
	mux.Handle(commerceDevLeaseRenewPath, d.commerceDevLeaseRenewHandler())
	mux.Handle(commerceDevLeaseRevokePath, d.commerceDevLeaseRevokeHandler())

	run := Run{
		ID:        uuid.MustParse("00000000-0000-4000-8000-000000001511"),
		CompanyID: testCompanyIDForCommerceDev,
		TaskID:    "0198aa00-0000-7000-8000-0000000000c2",
		AuthToken: testRunAuthTokenForCommerceDev,
	}
	bootstrap, revokeBootstrap, err := d.registerTaskCommerceBootstrap(run)
	if err != nil {
		t.Fatalf("registerTaskCommerceBootstrap() error = %v", err)
	}
	if _, err := writeCommerceDevControlConfig(home, server.URL, bootstrap); err != nil {
		t.Fatalf("write scoped control config: %v", err)
	}
	lease, err := OpenCommerceDevLease(
		t.Context(),
		run.CompanyID,
		CommerceDevManifestSite{
			Alias: "o-ke", Origin: "http://localhost:3000", PortalReturnPath: "/payment",
		},
		longLivedForeignPID,
	)
	if err != nil {
		t.Fatalf("OpenCommerceDevLease() error = %v", err)
	}
	if service.registrar.(*commerceDevRegistrarStub).runToken != run.AuthToken {
		t.Fatalf(
			"scoped registration token = %q, want exact run token",
			service.registrar.(*commerceDevRegistrarStub).runToken,
		)
	}
	if err := revokeBootstrap(); err != nil {
		t.Fatalf("revoke Task bootstrap: %v", err)
	}
	wrongLeaseCapability, err := newCommerceDevCapability()
	if err != nil {
		t.Fatalf("new wrong lease capability: %v", err)
	}
	wrongRenewalBody, err := json.Marshal(commerceDevLeaseLifecycleRequest{
		SchemaVersion: 1,
		LeaseID:       lease.LeaseID,
		OwnerPID:      longLivedForeignPID,
	})
	if err != nil {
		t.Fatalf("encode wrong-capability renewal: %v", err)
	}
	wrongRenewal, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		server.URL+commerceDevLeaseRenewPath,
		strings.NewReader(string(wrongRenewalBody)),
	)
	if err != nil {
		t.Fatalf("create wrong-capability renewal: %v", err)
	}
	wrongRenewal.Header.Set("Authorization", "BoardlyCommerceLease "+wrongLeaseCapability)
	wrongRenewal.Header.Set("Content-Type", "application/json")
	wrongResponse, err := http.DefaultClient.Do(wrongRenewal)
	if err != nil {
		t.Fatalf("wrong-capability renewal request: %v", err)
	}
	if err := wrongResponse.Body.Close(); err != nil {
		t.Fatalf("close wrong-capability renewal response: %v", err)
	}
	if wrongResponse.StatusCode != http.StatusUnauthorized {
		t.Fatalf(
			"wrong-capability renewal status = %d, want %d",
			wrongResponse.StatusCode,
			http.StatusUnauthorized,
		)
	}
	if err := lease.Renew(t.Context()); err == nil {
		t.Fatal("Renew() after bootstrap revocation error = nil")
	}
	if err := lease.Close(t.Context()); err != nil {
		t.Fatalf("Close() after bootstrap revocation error = %v, want idempotent close", err)
	}
	if _, err := service.metadata(lease.SignerCapability); !errors.Is(err, errCommerceDevLeaseInvalid) {
		t.Fatalf("closed signer capability error = %v, want invalid lease", err)
	}

	unregisteredCapability, err := newCommerceDevCapability()
	if err != nil {
		t.Fatalf("new unregistered capability: %v", err)
	}
	if _, err := writeCommerceDevControlConfig(home, server.URL, unregisteredCapability); err != nil {
		t.Fatalf("write unregistered control config: %v", err)
	}
	_, err = OpenCommerceDevLease(
		t.Context(),
		run.CompanyID,
		CommerceDevManifestSite{
			Alias: "o-ke", Origin: "http://localhost:3000", PortalReturnPath: "/payment",
		},
		os.Getpid(),
	)
	if err == nil {
		t.Fatal("unregistered global-style capability minted a Commerce lease")
	}
}

func TestScopedCommerceBootstrapRevocationCancelsInflightLeaseCreation(t *testing.T) {
	registrar := &blockingCommerceDevRegistrarStub{
		started: make(chan RegisterLocalCommerceSigningKeyRequest, 1),
	}
	service := newCommerceDevService(registrar, "http://localhost:8080")
	service.processAlive = func(int) bool { return true }
	d := &Daemon{
		logger:      testLogger(),
		commerceDev: service,
	}
	run := Run{
		ID:        uuid.MustParse("00000000-0000-4000-8000-000000001512"),
		CompanyID: testCompanyIDForCommerceDev,
		TaskID:    "0198aa00-0000-7000-8000-0000000000c3",
		AuthToken: testRunAuthTokenForCommerceDev,
	}
	bootstrap, revokeBootstrap, err := d.registerTaskCommerceBootstrap(run)
	if err != nil {
		t.Fatalf("registerTaskCommerceBootstrap() error = %v", err)
	}
	body, err := json.Marshal(commerceDevLeaseRequest{
		SchemaVersion: 1,
		CompanyID:     run.CompanyID,
		Site: CommerceDevManifestSite{
			Alias: "o-ke", Origin: "http://localhost:3000", PortalReturnPath: "/payment",
		},
		OwnerPID: 4242,
	})
	if err != nil {
		t.Fatalf("encode lease request: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, commerceDevLeaseCreatePath, strings.NewReader(string(body)))
	request.Header.Set("Authorization", "BoardlyCommerceTask "+bootstrap)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handlerDone := make(chan struct{})
	go func() {
		d.commerceDevLeaseCreateHandler("http://127.0.0.1:19713").ServeHTTP(response, request)
		close(handlerDone)
	}()

	select {
	case <-registrar.started:
	case <-time.After(time.Second):
		t.Fatal("lease creation did not reach registrar")
	}
	if err := revokeBootstrap(); err != nil {
		t.Fatalf("revoke Task bootstrap: %v", err)
	}
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("in-flight lease creation did not stop after bootstrap revocation")
	}
	if response.Code == http.StatusCreated {
		t.Fatalf("in-flight lease creation status = %d, want failure", response.Code)
	}
	service.mu.Lock()
	leaseCount := len(service.leasesByID)
	service.mu.Unlock()
	if leaseCount != 0 {
		t.Fatalf("leases after bootstrap revocation = %d, want 0", leaseCount)
	}
}

func TestHealthServerDoesNotExposeAnHTTPShutdownCapability(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	daemon := &Daemon{logger: testLogger()}
	stopHealthServer := daemon.startHealthServer(listener, time.Now())

	request, err := http.NewRequestWithContext(
		t.Context(), http.MethodPost, "http://"+listener.Addr().String()+"/shutdown", nil,
	)
	if err != nil {
		t.Fatalf("create shutdown probe: %v", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("shutdown probe: %v", err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatalf("close shutdown probe: %v", err)
	}
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("POST /shutdown status = %d, want 404", response.StatusCode)
	}
	done := make(chan error, 1)
	go func() { done <- stopHealthServer() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("stop health server: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("health server did not join after it was stopped")
	}
}

func TestStoppingTheHealthServerJoinsActiveHandlers(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	registrar := &manuallyBlockingCommerceDevRegistrarStub{
		started: make(chan RegisterLocalCommerceSigningKeyRequest, 1),
		release: make(chan struct{}),
	}
	service := newCommerceDevService(registrar, "http://localhost:8080")
	service.processAlive = func(int) bool { return true }
	daemon := &Daemon{logger: testLogger(), commerceDev: service}
	run := Run{
		ID:        uuid.MustParse("00000000-0000-4000-8000-000000001513"),
		CompanyID: testCompanyIDForCommerceDev,
		TaskID:    "0198aa00-0000-7000-8000-0000000000c4",
		AuthToken: testRunAuthTokenForCommerceDev,
	}
	bootstrap, revokeBootstrap, err := daemon.registerTaskCommerceBootstrap(run)
	if err != nil {
		t.Fatalf("register Task bootstrap: %v", err)
	}
	stopHealthServer := daemon.startHealthServer(listener, time.Now())

	body := `{"schemaVersion":1,"companyId":"` + run.CompanyID +
		`","site":{"alias":"o-ke","origin":"http://localhost:3000","portalReturnPath":"/payment"},` +
		`"ownerPid":4321}`
	request, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		"http://"+listener.Addr().String()+commerceDevLeaseCreatePath,
		strings.NewReader(body),
	)
	if err != nil {
		t.Fatalf("create blocked lease request: %v", err)
	}
	request.Header.Set("Authorization", "BoardlyCommerceTask "+bootstrap)
	request.Header.Set("Content-Type", "application/json")
	requestDone := make(chan struct{})
	go func() {
		response, _ := http.DefaultClient.Do(request)
		if response != nil {
			_ = response.Body.Close()
		}
		close(requestDone)
	}()
	select {
	case <-registrar.started:
	case <-time.After(time.Second):
		t.Fatal("lease handler did not reach its blocking registration")
	}
	serverDone := make(chan error, 1)
	go func() { serverDone <- stopHealthServer() }()
	select {
	case err := <-serverDone:
		t.Fatalf("the health server stopped while a handler still used Commerce state: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(registrar.release)
	select {
	case <-requestDone:
	case <-time.After(time.Second):
		t.Fatal("blocked signing request did not finish")
	}
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatalf("stop health server: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("the health server did not stop after its active handler finished")
	}
	if err := revokeBootstrap(); err != nil {
		t.Fatalf("revoke Task bootstrap: %v", err)
	}
}

func TestStoppingTheHealthServerBoundsAHandlerThatIgnoresCancellation(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	registrar := &manuallyBlockingCommerceDevRegistrarStub{
		started: make(chan RegisterLocalCommerceSigningKeyRequest, 1),
		release: make(chan struct{}),
	}
	service := newCommerceDevService(registrar, "http://localhost:8080")
	service.processAlive = func(int) bool { return true }
	daemon := &Daemon{
		logger:                          testLogger(),
		commerceDev:                     service,
		healthShutdownTimeoutOverride:   20 * time.Millisecond,
		healthCloseDrainTimeoutOverride: 20 * time.Millisecond,
	}
	run := Run{
		ID:        uuid.MustParse("00000000-0000-4000-8000-000000001514"),
		CompanyID: testCompanyIDForCommerceDev,
		TaskID:    "0198aa00-0000-7000-8000-0000000000c5",
		AuthToken: testRunAuthTokenForCommerceDev,
	}
	bootstrap, revokeBootstrap, err := daemon.registerTaskCommerceBootstrap(run)
	if err != nil {
		t.Fatalf("register Task bootstrap: %v", err)
	}
	stopHealthServer := daemon.startHealthServer(listener, time.Now())

	body := `{"schemaVersion":1,"companyId":"` + run.CompanyID +
		`","site":{"alias":"o-ke","origin":"http://localhost:3000","portalReturnPath":"/payment"},` +
		`"ownerPid":4321}`
	request, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		"http://"+listener.Addr().String()+commerceDevLeaseCreatePath,
		strings.NewReader(body),
	)
	if err != nil {
		t.Fatalf("create blocked lease request: %v", err)
	}
	request.Header.Set("Authorization", "BoardlyCommerceTask "+bootstrap)
	request.Header.Set("Content-Type", "application/json")
	requestDone := make(chan struct{})
	go func() {
		response, _ := http.DefaultClient.Do(request)
		if response != nil {
			_ = response.Body.Close()
		}
		close(requestDone)
	}()
	select {
	case <-registrar.started:
	case <-time.After(time.Second):
		t.Fatal("lease handler did not reach its blocking registration")
	}
	serverDone := make(chan error, 1)
	go func() { serverDone <- stopHealthServer() }()
	select {
	case err := <-serverDone:
		if !errors.Is(err, errHealthHandlersNotDrained) {
			t.Fatalf("stop health server error = %v, want undrained handler", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("serveHealth() remained blocked after both shutdown deadlines")
	}
	close(registrar.release)
	select {
	case <-requestDone:
	case <-time.After(time.Second):
		t.Fatal("forced-closed request did not finish")
	}
	if err := revokeBootstrap(); err != nil {
		t.Fatalf("revoke Task bootstrap: %v", err)
	}
}

func TestPrepareCommerceDevDoesNotWriteGlobalAuthority(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BOARDLY_HOME", home)
	legacyKeyDirectory := filepath.Join(home, "commerce", "dev-signing")
	if err := os.MkdirAll(legacyKeyDirectory, 0o700); err != nil {
		t.Fatalf("seed legacy key directory: %v", err)
	}
	for path, data := range map[string]string{
		filepath.Join(home, "commerce-dev.json"):         "legacy master capability",
		filepath.Join(legacyKeyDirectory, "legacy.json"): "legacy private key",
		filepath.Join(home, "keep.txt"):                  "unrelated",
	} {
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatalf("seed %s: %v", path, err)
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	daemon := &Daemon{
		cfg:    Config{ServerBaseURL: "http://localhost:8080"},
		client: NewClient("http://localhost:8080"),
		logger: testLogger(),
	}
	cleanup, err := daemon.prepareCommerceDev(listener)
	if err != nil {
		t.Fatalf("prepareCommerceDev() error = %v", err)
	}
	defer cleanup()
	for _, path := range []string{filepath.Join(home, "commerce-dev.json"), legacyKeyDirectory} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("legacy authority %q remains: %v", path, err)
		}
	}
	if got, err := os.ReadFile(filepath.Join(home, "keep.txt")); err != nil || string(got) != "unrelated" {
		t.Fatalf("unrelated Boardly state = %q, %v", got, err)
	}
	if daemon.commerceDevEndpoint != "http://"+listener.Addr().String() || daemon.commerceDev == nil {
		t.Fatalf(
			"in-memory Commerce state = endpoint %q, service %v",
			daemon.commerceDevEndpoint,
			daemon.commerceDev != nil,
		)
	}
}

func TestCommerceDevCapabilityUsesOneCanonical256BitValue(t *testing.T) {
	t.Parallel()

	capability, err := newCommerceDevCapability()
	if err != nil {
		t.Fatalf("newCommerceDevCapability() error = %v", err)
	}
	if !validCommerceDevCapability(capability) {
		t.Fatalf("generated capability %q is invalid", capability)
	}
	for _, invalid := range []string{
		"", strings.Repeat("a", 42), strings.Repeat("a", 44),
		strings.Repeat("+", 43), capability + "=",
	} {
		if validCommerceDevCapability(invalid) {
			t.Fatalf("validCommerceDevCapability(%q) = true", invalid)
		}
	}
}

func TestClientRegistersLocalCommerceKeyWithDaemonAndRunIdentity(t *testing.T) {
	t.Parallel()

	var authorization, runProof, path string
	var gotRequest RegisterLocalCommerceSigningKeyRequest
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		runProof = r.Header.Get("X-Boardly-Run-Token")
		path = r.URL.Path
		var raw json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Errorf("decode registration request: %v", err)
		}
		if err := json.Unmarshal(raw, &gotRequest); err != nil {
			t.Errorf("decode typed registration request: %v", err)
		}
		if err := json.Unmarshal(raw, &gotBody); err != nil {
			t.Errorf("decode registration request keys: %v", err)
		}
		_ = json.NewEncoder(w).Encode(RegisterLocalCommerceSigningKeyResponse{
			SchemaVersion: 1,
			SiteID:        "0198aa00-0000-7000-8000-0000000000c1",
			Environment:   "sandbox",
			KeyID:         "fingerprint",
		})
	}))
	defer server.Close()

	client := NewClient(server.URL)
	client.SetToken("daemon-session-token")
	response, err := client.RegisterLocalCommerceSigningKey(
		t.Context(),
		testRunAuthTokenForCommerceDev,
		testCompanyIDForCommerceDev,
		"o-ke",
		RegisterLocalCommerceSigningKeyRequest{
			SchemaVersion: 1, Origin: "http://localhost:3000",
			PublicJWK:   CommercePublicJWK{Kty: "EC", Crv: "P-256", X: "x", Y: "y"},
			Fingerprint: "fingerprint",
		},
	)
	if err != nil {
		t.Fatalf("RegisterLocalCommerceSigningKey() error = %v", err)
	}
	if path != "/api/v1/daemon/companies/0198aa00-0000-7000-8000-0000000000c0/commerce/sites/o-ke/local-signing-keys" ||
		authorization != "Bearer daemon-session-token" ||
		runProof != testRunAuthTokenForCommerceDev {
		t.Fatalf("path = %q, authorization = %q, run proof = %q", path, authorization, runProof)
	}
	if response.KeyID != "fingerprint" {
		t.Fatalf("response = %+v", response)
	}
	if _, callerAuthored := gotBody["taskId"]; callerAuthored {
		t.Fatalf("registration request trusts caller-authored taskId: %+v", gotBody)
	}
}

func TestClientNeverFallsBackToGlobalSessionForLocalCommerceKey(t *testing.T) {
	t.Parallel()

	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requestCount++
	}))
	defer server.Close()
	client := NewClient(server.URL)
	client.SetToken("global-user-session-must-not-be-used")
	_, err := client.RegisterLocalCommerceSigningKey(
		t.Context(),
		"",
		testCompanyIDForCommerceDev,
		"o-ke",
		RegisterLocalCommerceSigningKeyRequest{},
	)
	if err == nil {
		t.Fatal("RegisterLocalCommerceSigningKey() accepted an absent run token")
	}
	if requestCount != 0 {
		t.Fatalf("missing run proof made %d registration requests", requestCount)
	}
}

func TestClientNeverSendsRunProofWithoutDaemonSession(t *testing.T) {
	t.Parallel()

	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requestCount++
	}))
	defer server.Close()
	client := NewClient(server.URL)
	_, err := client.RegisterLocalCommerceSigningKey(
		t.Context(),
		testRunAuthTokenForCommerceDev,
		testCompanyIDForCommerceDev,
		"o-ke",
		RegisterLocalCommerceSigningKeyRequest{},
	)
	if err == nil {
		t.Fatal("RegisterLocalCommerceSigningKey() accepted an absent daemon session")
	}
	if requestCount != 0 {
		t.Fatalf("run proof without daemon session made %d registration requests", requestCount)
	}
}

func TestClientDoesNotForwardRunProofAcrossRegistrationRedirect(t *testing.T) {
	t.Parallel()

	redirectedRequests := 0
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirectedRequests++
	}))
	defer redirectTarget.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, redirectTarget.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	client := NewClient(server.URL)
	client.SetToken("daemon-session-token")
	_, err := client.RegisterLocalCommerceSigningKey(
		t.Context(),
		testRunAuthTokenForCommerceDev,
		testCompanyIDForCommerceDev,
		"o-ke",
		RegisterLocalCommerceSigningKeyRequest{},
	)
	if err == nil {
		t.Fatal("RegisterLocalCommerceSigningKey() followed a redirect")
	}
	if redirectedRequests != 0 {
		t.Fatalf("run proof was forwarded across %d redirected requests", redirectedRequests)
	}
}

const (
	testCompanyIDForCommerceDev    = "0198aa00-0000-7000-8000-0000000000c0"
	testRunAuthTokenForCommerceDev = "brt_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

func decodeJWKCoordinateForTest(t *testing.T, value string) []byte {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		t.Fatalf("decode JWK coordinate: %v", err)
	}
	return raw
}

func commercePublicKeyForTest(t *testing.T, jwk CommercePublicJWK) *ecdsa.PublicKey {
	t.Helper()
	x := decodeJWKCoordinateForTest(t, jwk.X)
	y := decodeJWKCoordinateForTest(t, jwk.Y)
	uncompressed := append([]byte{4}, x...)
	uncompressed = append(uncompressed, y...)
	key, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), uncompressed)
	if err != nil {
		t.Fatalf("parse JWK public key: %v", err)
	}
	return key
}
