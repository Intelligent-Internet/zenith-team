package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
)

const (
	testCommerceCompanyID      = "01900000-0000-7000-8000-000000000005"
	testCommerceTaskID         = "01900000-0000-7000-8000-000000000011"
	testCommerceRunID          = "00000000-0000-4000-8000-000000000007"
	testCommerceAgentID        = "01900000-0000-7000-8000-000000000008"
	testCommerceConversationID = "01900000-0000-7000-8000-000000000009"
	testCommerceManifestID     = "01900000-0000-7000-8000-000000000021"
	testCommerceApprovalID     = "01900000-0000-7000-8000-000000000023"
	testCommerceSiteID         = "01900000-0000-7000-8000-000000000022"
	testCommerceDigest         = "2b52a4c5bd127e96dbb7b283c5e3a154770582340d3f76b4a10b50c1226488b3"
)

type commerceAPIStub struct {
	fetchPath    string
	postPath     string
	requestKey   string
	postBody     any
	fetchPayload string
	postPayload  string
	err          error
}

func (s *commerceAPIStub) FetchRunJSON(_ context.Context, path string, destination any) error {
	s.fetchPath = path
	if s.err != nil {
		return s.err
	}
	return json.Unmarshal([]byte(s.fetchPayload), destination)
}

func (s *commerceAPIStub) PostRunJSON(
	_ context.Context,
	path string,
	requestKey string,
	source, destination any,
) error {
	s.postPath, s.postBody, s.requestKey = path, source, requestKey
	if s.err != nil {
		return s.err
	}
	return json.Unmarshal([]byte(s.postPayload), destination)
}

func newCommerceTestService(
	t *testing.T,
	api CommerceAPI,
	runContext RunContext,
) *DefaultCommerceToolService {
	t.Helper()
	service, err := NewCommerceToolService(api, runContext)
	if err != nil {
		t.Fatalf("NewCommerceToolService() error = %v", err)
	}
	return service
}

func TestSubmitCommerceManifestBindsRunIdentityOutsideToolInput(t *testing.T) {
	t.Parallel()
	api := &commerceAPIStub{postPayload: `{
		"manifestId":"` + testCommerceManifestID + `",
		"approvalId":"` + testCommerceApprovalID + `",
		"siteId":"` + testCommerceSiteID + `",
		"digest":"` + testCommerceDigest + `",
		"state":"draft",
		"verificationPath":"/.well-known/boardly-site-verification.json",
		"verificationDocument":{"schemaVersion":2,"siteAlias":"store","manifestDigest":"` + testCommerceDigest + `"},
		"createdAt":"2026-08-21T01:02:03Z"
	}`}
	service := newCommerceTestService(t, api, testCommerceRunContext())
	manifest := testCommerceManifest()

	result, err := service.SubmitCommerceManifest(t.Context(), SubmitCommerceManifestInput{
		Environment: "live",
		Manifest:    manifest,
	})
	if err != nil {
		t.Fatalf("SubmitCommerceManifest() error = %v", err)
	}
	if api.postPath != "/companies/"+testCommerceCompanyID+"/commerce/manifests" {
		t.Fatalf("PostJSON() path = %q", api.postPath)
	}
	encoded, err := json.Marshal(api.postBody)
	if err != nil {
		t.Fatal(err)
	}
	var request map[string]any
	if err := json.Unmarshal(encoded, &request); err != nil {
		t.Fatal(err)
	}
	wantKeys := []string{"environment", "manifest", "schemaVersion"}
	gotKeys := make([]string, 0, len(request))
	for key := range request {
		gotKeys = append(gotKeys, key)
	}
	slices.Sort(gotKeys)
	if !reflect.DeepEqual(gotKeys, wantKeys) || request["environment"] != "live" {
		t.Fatalf("PostJSON() body = %s", encoded)
	}
	if _, err := uuid.Parse(api.requestKey); err != nil || api.requestKey == "" {
		t.Fatalf("PostRunJSON() request key = %q", api.requestKey)
	}
	if result.ManifestID != testCommerceManifestID || result.ApprovalID != testCommerceApprovalID ||
		result.ApprovalStatus != "pending" || result.SiteID != testCommerceSiteID ||
		result.Digest != testCommerceDigest || result.State != "draft" ||
		result.VerificationDocument.SiteAlias != "store" ||
		result.VerificationDocument.ManifestDigest != testCommerceDigest {
		t.Fatalf("SubmitCommerceManifest() = %+v", result)
	}
	wantReviewPath := "/revenue?agentId=" + testCommerceAgentID +
		"&approvalId=" + testCommerceApprovalID +
		"&conversationId=" + testCommerceConversationID +
		"&conversationKind=task&manifestId=" + testCommerceManifestID +
		"&taskId=" + testCommerceTaskID
	if result.ReviewPath != wantReviewPath {
		t.Fatalf("SubmitCommerceManifest() review path = %q, want %q", result.ReviewPath, wantReviewPath)
	}
	if !strings.Contains(result.Instruction, "End this Run now") {
		t.Fatalf("SubmitCommerceManifest() instruction = %q, want live approval handoff", result.Instruction)
	}
	if strings.Contains(result.Instruction, "zen commerce dev") {
		t.Fatalf("SubmitCommerceManifest() instruction = %q, forbid local wrapper", result.Instruction)
	}
	encodedResult, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		`"manifestId"`, `"approvalId"`, `"approvalStatus"`, `"instruction"`,
		`"reviewPath"`, `"siteId"`, `"verificationPath"`,
		`"verificationDocument"`, `"createdAt"`,
	} {
		if !bytes.Contains(encodedResult, []byte(key)) {
			t.Fatalf("SubmitCommerceManifest() JSON = %s, missing %s", encodedResult, key)
		}
	}
}

func TestSubmitLoopbackManifestUsesTheSameSiteSignedContract(t *testing.T) {
	t.Parallel()
	api := &commerceAPIStub{postPayload: `{
		"manifestId":"` + testCommerceManifestID + `",
		"approvalId":"` + testCommerceApprovalID + `",
		"siteId":"` + testCommerceSiteID + `",
		"digest":"` + testCommerceDigest + `",
		"state":"draft",
		"verificationPath":"/.well-known/boardly-site-verification.json",
		"verificationDocument":{"schemaVersion":2,"siteAlias":"store","manifestDigest":"` + testCommerceDigest + `"},
		"createdAt":"2026-08-21T01:02:03Z"
	}`}
	service := newCommerceTestService(t, api, testCommerceRunContext())
	manifest := testCommerceManifest()
	manifest.Site.Origin = "http://localhost:5173"

	result, err := service.SubmitCommerceManifest(t.Context(), SubmitCommerceManifestInput{
		Environment: CommerceEnvironmentSandbox,
		Manifest:    manifest,
	})
	if err != nil {
		t.Fatalf("SubmitCommerceManifest() error = %v", err)
	}
	if result.VerificationPath != "/.well-known/boardly-site-verification.json" ||
		result.VerificationDocument.SiteAlias != "store" {
		t.Fatalf("SubmitCommerceManifest() = %+v", result)
	}
	encoded, err := json.Marshal(api.postBody)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"schemaVersion":2`)) {
		t.Fatalf("PostRunJSON() body = %s", encoded)
	}
	encodedResult, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encodedResult, []byte("verificationDocument")) ||
		bytes.Contains(encodedResult, []byte(`"authentication"`)) {
		t.Fatalf("SubmitCommerceManifest() JSON = %s", encodedResult)
	}
	for _, want := range []string{
		"cannot be published until the local signing key is registered",
		"zen commerce dev --manifest boardly-commerce.json -- <site command>",
		"Keep that wrapper in the foreground",
		"keep this Run active",
	} {
		if !strings.Contains(result.Instruction, want) {
			t.Fatalf("SubmitCommerceManifest() instruction missing %q: %s", want, result.Instruction)
		}
	}
	if strings.Contains(result.Instruction, "End this Run now") {
		t.Fatalf("SubmitCommerceManifest() instruction = %q, forbid premature Run exit", result.Instruction)
	}
}

func TestCommerceAccountStatusUsesInjectedCompanyAndValidatesReadiness(t *testing.T) {
	t.Parallel()
	api := &commerceAPIStub{fetchPayload: `{
		"termsAccepted":true,
		"accounts":[
			{
				"environment":"sandbox","configured":true,"status":"ready",
				"manifestReady":true,"chargesEnabled":true,"payoutsEnabled":true,
				"actionRequired":false,"country":"GB",
				"requirements":{"currentlyDue":0,"eventuallyDue":0,"pastDue":0,"currentDeadline":null},
				"syncStatus":"current","lastReconciledAt":"2026-08-24T01:02:03Z"
			},
			{
				"environment":"live","configured":false,"status":"provisioning",
				"manifestReady":false,"chargesEnabled":false,"payoutsEnabled":false,
				"actionRequired":false,"country":null,
				"requirements":{"currentlyDue":0,"eventuallyDue":0,"pastDue":0,"currentDeadline":null},
				"syncStatus":"stale","lastReconciledAt":null
			}
		]
	}`}
	service := newCommerceTestService(t, api, testCommerceRunContext())

	result, err := service.CommerceAccountStatus(t.Context(), CommerceAccountStatusInput{})
	if err != nil {
		t.Fatalf("CommerceAccountStatus() error = %v", err)
	}
	if api.fetchPath != "/companies/"+testCommerceCompanyID+"/commerce/account-status" {
		t.Fatalf("FetchRunJSON() path = %q", api.fetchPath)
	}
	if !result.TermsAccepted || len(result.Accounts) != 2 ||
		result.Accounts[0].Environment != CommerceEnvironmentSandbox ||
		!result.Accounts[0].ManifestReady || result.Accounts[0].Country == nil ||
		*result.Accounts[0].Country != "GB" ||
		result.Accounts[1].Environment != CommerceEnvironmentLive ||
		result.Accounts[1].Status != "provisioning" ||
		result.Accounts[1].SyncStatus != "stale" ||
		result.Accounts[1].ManifestReady {
		t.Fatalf("CommerceAccountStatus() = %+v", result)
	}
	encodedResult, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		`"termsAccepted"`, `"manifestReady"`, `"chargesEnabled"`,
		`"currentlyDue"`, `"syncStatus"`, `"lastReconciledAt"`,
	} {
		if !bytes.Contains(encodedResult, []byte(key)) {
			t.Fatalf("CommerceAccountStatus() JSON = %s, missing %s", encodedResult, key)
		}
	}

	for _, payload := range []string{
		`{"termsAccepted":true,"accounts":[]}`,
		`{"termsAccepted":false,"accounts":[` +
			`{"environment":"sandbox","configured":true,"status":"ready","manifestReady":true,` +
			`"chargesEnabled":true,"payoutsEnabled":true,"actionRequired":false,"country":"GB",` +
			`"requirements":{"currentlyDue":0,"eventuallyDue":0,"pastDue":0},` +
			`"syncStatus":"current"},` +
			`{"environment":"live","configured":false,"status":"not_started","manifestReady":false,` +
			`"chargesEnabled":false,"payoutsEnabled":false,"actionRequired":false,"country":null,` +
			`"requirements":{"currentlyDue":0,"eventuallyDue":0,"pastDue":0},` +
			`"syncStatus":"not_started"}]}`,
	} {
		api.fetchPayload = payload
		if _, err := service.CommerceAccountStatus(
			t.Context(),
			CommerceAccountStatusInput{},
		); err == nil {
			t.Fatalf("invalid account status response was accepted: %s", payload)
		}
	}
}

func TestSubmitCommerceManifestRejectsMismatchedVerificationDocument(t *testing.T) {
	t.Parallel()

	base := `{
		"manifestId":"` + testCommerceManifestID + `",
		"approvalId":"` + testCommerceApprovalID + `",
		"siteId":"` + testCommerceSiteID + `",
		"digest":"` + testCommerceDigest + `",
		"state":"draft",
		"verificationPath":"/.well-known/boardly-site-verification.json",
		"verificationDocument":%s,
		"createdAt":"2026-08-21T01:02:03Z"
	}`
	for _, document := range []string{
		`{"schemaVersion":2,"siteAlias":"other","manifestDigest":"` + testCommerceDigest + `"}`,
		`{"schemaVersion":2,"siteAlias":"store","manifestDigest":"` + strings.Repeat("a", 64) + `"}`,
		`{"schemaVersion":2,"siteAlias":"store","manifestDigest":"` + testCommerceDigest + `","siteId":"attacker"}`,
		`{"schemaVersion":2,"siteAlias":"store","manifestDigest":"` + testCommerceDigest + `","signingKeys":[]}`,
	} {
		api := &commerceAPIStub{postPayload: strings.Replace(base, "%s", document, 1)}
		service := newCommerceTestService(t, api, testCommerceRunContext())
		if _, err := service.SubmitCommerceManifest(t.Context(), SubmitCommerceManifestInput{
			Environment: CommerceEnvironmentLive,
			Manifest:    testCommerceManifest(),
		}); err == nil {
			t.Fatalf("verification document %s was accepted", document)
		}
	}
}

func TestCommerceManifestStatusIsReadableFromContinuationRunForTheSameTask(t *testing.T) {
	t.Parallel()
	api := &commerceAPIStub{fetchPayload: `{
		"manifestId":"` + testCommerceManifestID + `",
		"siteId":"` + testCommerceSiteID + `",
		"taskId":"` + testCommerceTaskID + `",
		"runId":"00000000-0000-4000-8000-000000000006",
		"digest":"` + testCommerceDigest + `",
		"state":"active",
		"approvedAt":"2026-08-21T01:03:00Z",
		"appliedAt":"2026-08-21T01:04:00Z",
		"createdAt":"2026-08-21T01:02:03Z",
		"updatedAt":"2026-08-21T01:04:00Z"
	}`}
	service := newCommerceTestService(t, api, testCommerceRunContext())

	result, err := service.CommerceManifestStatus(
		t.Context(),
		CommerceManifestStatusInput{ManifestID: testCommerceManifestID},
	)
	if err != nil {
		t.Fatalf("CommerceManifestStatus() error = %v", err)
	}
	wantPath := "/companies/" + testCommerceCompanyID + "/commerce/manifests/" +
		testCommerceManifestID
	if api.fetchPath != wantPath {
		t.Fatalf("FetchRunJSON() path = %q, want %q", api.fetchPath, wantPath)
	}
	if result.ManifestID != testCommerceManifestID || result.State != "active" ||
		result.ApprovedAt == nil || result.AppliedAt == nil {
		t.Fatalf("CommerceManifestStatus() = %+v", result)
	}
	encodedResult, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		`"manifestId"`, `"siteId"`, `"approvedAt"`, `"appliedAt"`,
		`"createdAt"`, `"updatedAt"`,
	} {
		if !bytes.Contains(encodedResult, []byte(key)) {
			t.Fatalf("CommerceManifestStatus() JSON = %s, missing %s", encodedResult, key)
		}
	}

	api.fetchPayload = strings.Replace(
		api.fetchPayload,
		`"taskId":"`+testCommerceTaskID+`"`,
		`"taskId":"01900000-0000-7000-8000-000000000099"`,
		1,
	)
	if _, err := service.CommerceManifestStatus(
		t.Context(),
		CommerceManifestStatusInput{ManifestID: testCommerceManifestID},
	); err == nil || !strings.Contains(err.Error(), "context") {
		t.Fatalf("cross-Task CommerceManifestStatus() error = %v", err)
	}
}

func TestCommerceToolsRejectInvalidInputBeforeCallingAPI(t *testing.T) {
	t.Parallel()
	api := &commerceAPIStub{err: errors.New("must not be called")}
	service := newCommerceTestService(t, api, testCommerceRunContext())
	if _, err := service.SubmitCommerceManifest(
		t.Context(),
		SubmitCommerceManifestInput{Environment: "production", Manifest: testCommerceManifest()},
	); err == nil || strings.Contains(err.Error(), "must not be called") {
		t.Fatalf("invalid environment error = %v", err)
	}
	if _, err := service.SubmitCommerceManifest(
		t.Context(),
		SubmitCommerceManifestInput{Environment: "live"},
	); err == nil || strings.Contains(err.Error(), "must not be called") {
		t.Fatalf("missing manifest error = %v", err)
	}
	if _, err := service.CommerceManifestStatus(
		t.Context(),
		CommerceManifestStatusInput{ManifestID: "not-a-uuid"},
	); err == nil || strings.Contains(err.Error(), "must not be called") {
		t.Fatalf("invalid manifest ID error = %v", err)
	}
}

func testCommerceManifest() CommerceManifest {
	return CommerceManifest{
		SchemaVersion: 2,
		Site: CommerceManifestSite{
			Alias:            "store",
			Origin:           "https://store.example.com",
			PortalReturnPath: "/settings/billing",
		},
		Offers: []CommerceManifestOffer{{
			Alias: "report", DisplayName: "Premium report", Type: CommerceOfferTypeOneTime,
			Entitlement: "report.premium", Amount: 2900, Currency: "usd",
			AutomaticTax: false, SuccessPath: "/reports/processing", CancelPath: "/reports",
		}},
	}
}

func testCommerceRunContext() RunContext {
	return RunContext{
		CompanyID:      testCommerceCompanyID,
		ConversationID: testCommerceConversationID,
		TaskID:         testCommerceTaskID,
		RunID:          testCommerceRunID,
		AgentID:        testCommerceAgentID,
	}
}
