package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/execution"
)

func TestSocialRunProofRejectsWrongIdentityAndConversation(t *testing.T) {
	t.Parallel()
	user, company, conversation, agent, run := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	p := auth.UserPrincipal{UserID: user, CompanyID: company, SessionID: uuid.New(), AuthKind: auth.TokenKindBearer}
	valid := execution.RunOrigin{
		RunID:              run,
		CompanyID:          company,
		ConversationID:     conversation,
		AgentID:            agent,
		ConversationKind:   "direct",
		OriginUserID:       &user,
		AuthorizedByUserID: &user,
	}
	other := uuid.New()
	for _, tc := range []struct {
		name   string
		change func(*execution.RunOrigin)
		token  string
		want   bool
	}{
		{name: "matching direct Run", token: "brt_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", want: true},
		{name: "missing proof"},
		{name: "other company", change: func(o *execution.RunOrigin) { o.CompanyID = other }},
		{name: "other conversation", change: func(o *execution.RunOrigin) { o.ConversationID = other }},
		{name: "other user", change: func(o *execution.RunOrigin) { o.OriginUserID = &other }},
		{name: "another member's Run", change: func(o *execution.RunOrigin) { o.AuthorizedByUserID = &other }},
		{name: "task", change: func(o *execution.RunOrigin) { o.ConversationKind = "task" }},
		{name: "schedule", change: func(o *execution.RunOrigin) { o.ConversationKind = "schedule" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			origin := valid
			token := tc.token
			if tc.change != nil {
				tc.change(&origin)
				token = "brt_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
			}
			h := &Handler{RunProofResolver: runProofResolverStub{origin: origin}}
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Params = gin.Params{{Key: "conversationId", Value: conversation.String()}}
			c.Request = httptest.NewRequest(http.MethodGet, "/", nil).
				WithContext(auth.WithUserPrincipal(t.Context(), p))
			c.Request.Header.Set(runTokenHeader, token)
			_, _, ok := h.socialRunContext(c)
			if ok != tc.want {
				t.Fatalf("authorized=%v, want %v", ok, tc.want)
			}
			if !tc.want && w.Code != http.StatusUnauthorized {
				t.Fatalf("status=%d, want 401", w.Code)
			}
		})
	}
}
