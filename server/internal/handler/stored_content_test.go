package handler

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRejectInlineUIInStoredText(t *testing.T) {
	t.Parallel()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	if rejectInlineUI(c, storedText{"Email body", "Hi team,\n\nUse a <button> element for actions."}) {
		t.Fatal("prose that mentions an HTML tag was rejected")
	}
	if !rejectInlineUI(c, storedText{"Email body", "<box><text>Quarterly total</text></box>"}) {
		t.Fatal("component source in an email body was accepted")
	}
	if recorder.Code < 400 || !strings.Contains(recorder.Body.String(), "Email body is stored without an Intelligent UI renderer") {
		t.Fatalf("unexpected rejection: %d %s", recorder.Code, recorder.Body.String())
	}
}
