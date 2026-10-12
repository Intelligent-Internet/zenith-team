package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// RunProofPolicy decides by the route a request matched, before any handler:
// a Run proof reaches only routes that say they accept one, and a request
// without the proof is never touched.
func TestRunProofPolicyRefusesARunProofUnlessTheRouteAcceptsOne(t *testing.T) {
	t.Parallel()

	router := gin.New()
	router.Use(RunProofPolicy(map[string]RouteActorType{
		"GET /person":      RouteActorTypePerson,
		"GET /run/:runId":  RouteActorTypeRun,
		"GET /any":         RouteActorTypeAny,
		"POST /run/:runId": RouteActorTypePerson,
	}, nil))
	reached := func(c *gin.Context) { c.Status(http.StatusNoContent) }
	for _, path := range []string{"/person", "/run/:runId", "/any", "/unlisted"} {
		router.GET(path, reached)
	}
	router.POST("/run/:runId", reached)
	router.NoRoute(func(c *gin.Context) { c.Status(http.StatusNotFound) })

	for _, test := range []struct {
		name, method, path string
		proof              []string
		want               int
	}{
		{"person route without a proof", http.MethodGet, "/person", nil, http.StatusNoContent},
		{"person route with a proof", http.MethodGet, "/person", []string{"brt_proof"}, http.StatusForbidden},
		{"unlisted route counts as person", http.MethodGet, "/unlisted", []string{"brt_proof"}, http.StatusForbidden},
		{"run route with a proof", http.MethodGet, "/run/42", []string{"brt_proof"}, http.StatusNoContent},
		{"any route with a proof", http.MethodGet, "/any", []string{"brt_proof"}, http.StatusNoContent},
		{"the method is part of the key", http.MethodPost, "/run/42", []string{"brt_proof"}, http.StatusForbidden},
		{"an empty proof still counts", http.MethodGet, "/person", []string{""}, http.StatusForbidden},
		{"a repeated proof still counts", http.MethodGet, "/person", []string{"", "brt_proof"}, http.StatusForbidden},
		{"no route matched", http.MethodGet, "/missing", []string{"brt_proof"}, http.StatusNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request := httptest.NewRequest(test.method, test.path, nil)
			for _, value := range test.proof {
				request.Header.Add(HeaderRunToken, value)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf(
					"status = %d, want %d; body = %s",
					response.Code,
					test.want,
					response.Body.String(),
				)
			}
			if test.want != http.StatusForbidden {
				return
			}
			var body map[string]string
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode refusal: %v", err)
			}
			if body["code"] != "human_actor_required" {
				t.Fatalf("code = %q, want human_actor_required", body["code"])
			}
		})
	}
}
