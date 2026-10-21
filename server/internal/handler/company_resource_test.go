package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/company"
)

func TestProjectResourceRoutesCoverTypedCRUDWithoutLeakingLocalPath(t *testing.T) {
	userID, companyID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	resourceID := uuid.Must(uuid.NewV7())
	resourceDaemonID := "daemon-a"
	now := time.Date(2026, time.July, 12, 3, 4, 5, 0, time.UTC)
	resource := company.ResourceResponse{
		ID:           resourceID,
		CompanyID:    companyID,
		ResourceType: company.ResourceTypeLocalDirectory,
		ResourceRef: company.ResourceRefResponse{
			DaemonID: resourceDaemonID, DaemonName: "Owner laptop",
		},
		DaemonStatus:     company.DaemonStatusOnline,
		DaemonLastSeenAt: &now,
		Label:            "Repository",
		Position:         2,
		Metadata:         json.RawMessage(`{}`),
		CreatedBy: company.ActorRef{
			ID: userID, Kind: company.ActorTypeUser, DisplayName: "Owner",
		},
		CreatedAt: now,
		UpdatedAt: now,
		Revision:  1,
	}
	listCalls := 0
	service := &companyResourceServiceStub{
		list: func(
			_ context.Context,
			principal auth.UserPrincipal,
			gotCompanyID uuid.UUID,
			options company.ResourceListOptions,
		) ([]company.ResourceResponse, error) {
			listCalls++
			if principal.UserID != userID || gotCompanyID != companyID {
				t.Fatalf("ListResources() = %+v / %s / %+v", principal, gotCompanyID, options)
			}
			if listCalls == 1 && (options.DaemonID == nil || *options.DaemonID != resourceDaemonID) {
				t.Fatalf("filtered ListResources() options = %+v", options)
			}
			if listCalls == 2 && options.DaemonID != nil {
				t.Fatalf("unfiltered ListResources() options = %+v", options)
			}
			return []company.ResourceResponse{resource}, nil
		},
		get: func(_ context.Context, _ auth.UserPrincipal, gotCompanyID, gotResourceID uuid.UUID) (company.ResourceResponse, error) {
			if gotCompanyID != companyID || gotResourceID != resourceID {
				t.Fatalf("FetchResource() IDs = %s / %s", gotCompanyID, gotResourceID)
			}
			return resource, nil
		},
		update: func(_ context.Context, _ auth.UserPrincipal, gotCompanyID, gotResourceID uuid.UUID, input company.ResourceUpdateInput) (company.ResourceResponse, error) {
			if gotCompanyID != companyID || gotResourceID != resourceID || input.ExpectedRevision != 1 ||
				input.Label == nil {
				t.Fatalf("UpdateResource() = %s / %s / %+v", gotCompanyID, gotResourceID, input)
			}
			updated := resource
			updated.Label, updated.Revision = *input.Label, 2
			return updated, nil
		},
		delete: func(_ context.Context, _ auth.UserPrincipal, gotCompanyID, gotResourceID uuid.UUID, input company.RevisionInput) error {
			if gotCompanyID != companyID || gotResourceID != resourceID || input.ExpectedRevision != 2 {
				t.Fatalf("DeleteResource() = %s / %s / %+v", gotCompanyID, gotResourceID, input)
			}
			return nil
		},
	}
	handler := New(Handler{AuthService: authenticatedAuthStub(userID), CompanyResourceService: service})
	base := "/api/v1/companies/" + companyID.String() + "/resources"

	response := serveBearerRequest(handler, http.MethodGet, base+"?daemon_id="+resourceDaemonID, nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"items"`) ||
		strings.Contains(response.Body.String(), "nextCursor") {
		t.Fatalf("list response = %d / %s", response.Code, response.Body.String())
	}
	unfiltered := serveBearerRequest(handler, http.MethodGet, base, nil)
	if unfiltered.Code != http.StatusOK {
		t.Fatalf("unfiltered list response = %d / %s", unfiltered.Code, unfiltered.Body.String())
	}
	var collectionKeys map[string]json.RawMessage
	if err := json.Unmarshal(unfiltered.Body.Bytes(), &collectionKeys); err != nil || len(collectionKeys) != 1 {
		t.Fatalf("resource collection keys = %s, want items only", unfiltered.Body.String())
	}
	for _, query := range []string{
		"?limit=25",
		"?cursor=retired",
		"?unknown=true",
		"?daemonId=daemon-a",
		"?daemon_id=",
		"?daemon_id=daemon-a&daemon_id=daemon-b",
	} {
		got := serveBearerRequest(handler, http.MethodGet, base+query, nil)
		assertErrorResponse(t, got, http.StatusUnprocessableEntity, "validation_failed")
	}
	if listCalls != 2 {
		t.Fatalf("ListResources() calls = %d, want 2", listCalls)
	}
	response = serveBearerRequest(handler, http.MethodGet, base+"/"+resourceID.String(), nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), resourceDaemonID) {
		t.Fatalf("get response = %d / %s", response.Code, response.Body.String())
	}
	response = serveBearerJSON(
		handler,
		http.MethodPatch,
		base+"/"+resourceID.String(),
		[]byte(`{"expectedRevision":1,"label":"Renamed"}`),
	)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Renamed") {
		t.Fatalf("update response = %d / %s", response.Code, response.Body.String())
	}
	response = serveBearerJSON(
		handler,
		http.MethodDelete,
		base+"/"+resourceID.String(),
		[]byte(`{"expectedRevision":2}`),
	)
	if response.Code != http.StatusNoContent || response.Body.Len() != 0 {
		t.Fatalf("delete response = %d / %q", response.Code, response.Body.String())
	}
	if response := serveBearerRequest(
		handler,
		http.MethodGet,
		"/api/v1/projects/"+resourceID.String()+"/resources",
		nil,
	); response.Code != http.StatusNotFound {
		t.Fatalf("legacy unscoped Project-resource route status = %d, want 404", response.Code)
	}

	encoded, err := json.Marshal(resource)
	if err != nil || strings.Contains(string(encoded), "path") {
		t.Fatalf("local response leaks path = %s / %v", encoded, err)
	}
}

// The update body is decoded through an optional[T] per field, which is the only
// thing standing between a caller's typo and a silent partial write: an
// unrecognized key, a wrong type, or an explicit null must all be refused
// before the service is reached, because each one means the caller believes a
// change took effect that did not.
//
// The split matters as much as the refusal. A body the server cannot parse is
// 400; a body it parses but will not act on is 422. So these cases assert the
// status as well as the code, and the mixed case pins which one wins.
func TestProjectResourceUpdateRefusesBodiesBeforeReachingTheService(t *testing.T) {
	userID, companyID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	resourceID := uuid.Must(uuid.NewV7())
	service := &companyResourceServiceStub{
		list: func(
			context.Context,
			auth.UserPrincipal,
			uuid.UUID,
			company.ResourceListOptions,
		) ([]company.ResourceResponse, error) {
			return nil, nil
		},
		get: func(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID) (company.ResourceResponse, error) {
			return company.ResourceResponse{}, nil
		},
		update: func(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID, company.ResourceUpdateInput) (company.ResourceResponse, error) {
			t.Fatal("UpdateResource must not be reached for a body the handler refuses")
			return company.ResourceResponse{}, nil
		},
		delete: func(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID, company.RevisionInput) error {
			return nil
		},
	}
	handler := New(Handler{AuthService: authenticatedAuthStub(userID), CompanyResourceService: service})
	target := "/api/v1/companies/" + companyID.String() + "/resources/" + resourceID.String()
	for name, testCase := range map[string]struct {
		body   string
		status int
		code   string
	}{
		"null label":            {`{"expectedRevision":1,"label":null}`, http.StatusBadRequest, "invalid_request"},
		"null position":         {`{"expectedRevision":1,"position":null}`, http.StatusBadRequest, "invalid_request"},
		"null metadata":         {`{"expectedRevision":1,"metadata":null}`, http.StatusBadRequest, "invalid_request"},
		"null checkoutRef":      {`{"expectedRevision":1,"metadata":{"checkoutRef":null}}`, http.StatusBadRequest, "invalid_request"},
		"label is not a string": {`{"expectedRevision":1,"label":7}`, http.StatusBadRequest, "invalid_request"},
		"position is not whole": {`{"expectedRevision":1,"position":1.5}`, http.StatusBadRequest, "invalid_request"},
		"unknown field":         {`{"expectedRevision":1,"labell":"typo"}`, http.StatusBadRequest, "invalid_request"},
		// The nested object is decoded by optional's own unmarshaler, where the
		// outer decoder's strictness does not reach unless it is re-armed.
		"unknown nested field": {
			`{"expectedRevision":1,"metadata":{"checkoutRef":"a","nope":1}}`,
			http.StatusBadRequest,
			"invalid_request",
		},
		"nothing to change": {`{"expectedRevision":1}`, http.StatusUnprocessableEntity, "validation_failed"},
		// Both wrong at once: the body cannot be parsed, so the parse failure is
		// the only thing the server can honestly report.
		"unparseable and invalid": {
			`{"expectedRevision":0,"label":null}`,
			http.StatusBadRequest,
			"invalid_request",
		},
	} {
		t.Run(name, func(t *testing.T) {
			response := serveBearerJSON(handler, http.MethodPatch, target, []byte(testCase.body))
			if response.Code != testCase.status ||
				!strings.Contains(response.Body.String(), `"code":"`+testCase.code+`"`) {
				t.Fatalf(
					"response = %d / %s, want %d / %s",
					response.Code,
					response.Body.String(),
					testCase.status,
					testCase.code,
				)
			}
		})
	}
}

type companyResourceServiceStub struct {
	list func(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		company.ResourceListOptions,
	) ([]company.ResourceResponse, error)
	get    func(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID) (company.ResourceResponse, error)
	update func(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID, company.ResourceUpdateInput) (company.ResourceResponse, error)
	delete func(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID, company.RevisionInput) error
	create func(
		context.Context,
		uuid.UUID,
		uuid.UUID,
		company.CreateLocalDirectoryResourceInput,
	) (company.ResourceCreateResult, error)
}

func (s *companyResourceServiceStub) CreateLocalDirectoryResource(
	ctx context.Context,
	ownerUserID uuid.UUID,
	companyID uuid.UUID,
	input company.CreateLocalDirectoryResourceInput,
) (company.ResourceCreateResult, error) {
	if s.create == nil {
		return company.ResourceCreateResult{}, nil
	}
	return s.create(ctx, ownerUserID, companyID, input)
}

func (s *companyResourceServiceStub) ListResources(
	ctx context.Context,
	principal auth.UserPrincipal,
	companyID uuid.UUID,
	options company.ResourceListOptions,
) ([]company.ResourceResponse, error) {
	return s.list(ctx, principal, companyID, options)
}

func (s *companyResourceServiceStub) FetchResource(
	ctx context.Context,
	principal auth.UserPrincipal,
	companyID, resourceID uuid.UUID,
) (company.ResourceResponse, error) {
	return s.get(ctx, principal, companyID, resourceID)
}

func (s *companyResourceServiceStub) UpdateResource(
	ctx context.Context,
	principal auth.UserPrincipal,
	companyID, resourceID uuid.UUID,
	input company.ResourceUpdateInput,
) (company.ResourceResponse, error) {
	return s.update(ctx, principal, companyID, resourceID, input)
}

func (s *companyResourceServiceStub) DeleteResource(
	ctx context.Context,
	principal auth.UserPrincipal,
	companyID, resourceID uuid.UUID,
	input company.RevisionInput,
) error {
	return s.delete(ctx, principal, companyID, resourceID, input)
}

// Binding a directory is the switch between "the agent works in a sandbox" and
// "the agent edits the files on my laptop", so the transport is pinned: the
// database owns the row id, a fresh bind is 201, and a natural-key replay is 200.
func TestCreateCompanyResourceTransport(t *testing.T) {
	userID, companyID := uuid.New(), uuid.New()
	resourceID := uuid.New()
	now := time.Date(2026, time.July, 12, 3, 4, 5, 0, time.UTC)

	var seen company.CreateLocalDirectoryResourceInput
	created := true
	service := &companyResourceServiceStub{
		create: func(
			_ context.Context,
			gotOwnerID, gotCompanyID uuid.UUID,
			input company.CreateLocalDirectoryResourceInput,
		) (company.ResourceCreateResult, error) {
			if gotOwnerID != userID || gotCompanyID != companyID {
				t.Fatalf(
					"CreateLocalDirectoryResource() owner/company = %s / %s",
					gotOwnerID,
					gotCompanyID,
				)
			}
			seen = input
			daemonID := input.DaemonID
			return company.ResourceCreateResult{
				Resource: company.ResourceResponse{
					ID:           resourceID,
					CompanyID:    companyID,
					ResourceType: company.ResourceTypeLocalDirectory,
					ResourceRef: company.ResourceRefResponse{
						DaemonID: daemonID, DaemonName: "Owner laptop",
					},
					DaemonStatus: company.DaemonStatusOnline,
					Label:        input.Label,
					Metadata:     json.RawMessage(`{}`),
					CreatedBy: company.ActorRef{
						ID: userID, Kind: company.ActorTypeUser, DisplayName: "Owner",
					},
					CreatedAt: now,
					UpdatedAt: now,
					Revision:  1,
				},
				Created: created,
			}, nil
		},
	}
	handler := New(Handler{AuthService: authenticatedAuthStub(userID), CompanyResourceService: service})
	base := "/api/v1/companies/" + companyID.String() + "/resources"
	body := []byte(`{"daemonId":"daemon-a","localPath":"/Users/dana/code/acme","label":"Acme","position":0}`)

	response := serveBearerJSON(handler, http.MethodPost, base, body)
	if response.Code != http.StatusCreated {
		t.Fatalf("create response = %d / %s", response.Code, response.Body.String())
	}
	if seen.DaemonID != "daemon-a" || seen.Path != "/Users/dana/code/acme" {
		t.Fatalf("service input = %+v", seen)
	}
	if !strings.Contains(response.Body.String(), resourceID.String()) {
		t.Fatalf("create response does not carry the database id: %s", response.Body.String())
	}

	// The path identifies a folder on the caller's machine and is never
	// echoed: a company owner must not be able to read a colleague's disk
	// layout back out of the API.
	if strings.Contains(response.Body.String(), "/Users/dana") {
		t.Fatalf("create response leaks the local path: %s", response.Body.String())
	}

	created = false
	response = serveBearerJSON(handler, http.MethodPost, base, body)
	if response.Code != http.StatusOK {
		t.Fatalf("replayed create = %d, want 200 / %s", response.Code, response.Body.String())
	}

	clientID := uuid.New()
	withID := []byte(`{"id":"` + clientID.String() +
		`","daemonId":"daemon-b","localPath":"/tmp/work","label":"Work","position":0}`)
	rejected := serveBearerJSON(handler, http.MethodPost, base, withID)
	assertErrorResponse(t, rejected, http.StatusBadRequest, "invalid_request")
}
