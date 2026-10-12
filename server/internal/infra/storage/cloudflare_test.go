package storage

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCloudflareRoutesWritesWorkerContractAndDeletesIdempotently(t *testing.T) {
	t.Parallel()
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		if r.Header.Get("Authorization") != "Bearer test-token" ||
			r.URL.Path != "/accounts/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/storage/kv/namespaces/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb/values/my-app.apps.example.com" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.Method == http.MethodPut {
			var body map[string]string
			if err := json.NewDecoder(r.Body).
				Decode(&body); err != nil || body["originUrl"] != "https://app.run.app" ||
				len(body) != 1 {
				t.Errorf("route body = %+v, %v", body, err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true}`))
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	routes, err := NewCloudflareRoutes(CloudflareRoutesConfig{
		APIToken: "test-token", AccountID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		NamespaceID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", BaseDomain: "apps.example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	routes.endpoint = server.URL
	routes.client = server.Client()
	if err := routes.Put(t.Context(), "my-app.apps.example.com", "https://app.run.app"); err != nil {
		t.Fatal(err)
	}
	if err := routes.Delete(t.Context(), "my-app.apps.example.com"); err != nil {
		t.Fatal(err)
	}
	if len(methods) != 2 || methods[0] != "PUT" || methods[1] != "DELETE" {
		t.Fatalf("methods = %v", methods)
	}
}

func TestCloudflareRoutesRejectsFailedWritesWithoutExposingProviderBodies(t *testing.T) {
	t.Parallel()
	for _, status := range []int{http.StatusOK, http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				_, _ = w.Write(
					[]byte(`{"success":false,"errors":[{"message":"sensitive-provider-body"}]}`),
				)
			}))
			defer server.Close()
			routes := &CloudflareRoutes{
				endpoint: server.URL,
				client:   server.Client(),
				configuration: CloudflareRoutesConfig{
					AccountID:   "account",
					NamespaceID: "namespace",
					APIToken:    "test-token",
				},
			}
			err := routes.Put(t.Context(), "my-app.apps.example.com", "https://app.run.app")
			if err == nil || strings.Contains(err.Error(), "sensitive-provider-body") ||
				strings.Contains(err.Error(), "test-token") {
				t.Fatalf("Put() error = %v", err)
			}
		})
	}
}

func TestCloudflareRoutesBindingPinsNamespaceButAllowsTokenRotation(t *testing.T) {
	t.Parallel()
	configuration := CloudflareRoutesConfig{
		APIToken:    "first-token",
		AccountID:   "account",
		NamespaceID: "namespace",
		BaseDomain:  "apps.example.com",
	}
	first, err := NewCloudflareRoutes(configuration)
	if err != nil {
		t.Fatal(err)
	}
	configuration.APIToken = "replacement-token"
	rotated, err := NewCloudflareRoutes(configuration)
	if err != nil {
		t.Fatal(err)
	}
	if first.BindingID() != rotated.BindingID() {
		t.Fatal("token rotation changed the routing binding")
	}
	configuration.NamespaceID = "different-namespace"
	moved, err := NewCloudflareRoutes(configuration)
	if err != nil {
		t.Fatal(err)
	}
	if first.BindingID() == moved.BindingID() {
		t.Fatal("namespace move retained the routing binding")
	}
}
