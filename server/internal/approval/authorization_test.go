package approval

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
)

// Approving a Company Plan raised in a Task chat with a Project needs
// project.tasks.write there and no Company permission; any other plan needs
// company.plan.apply (ADR 0017).
func TestCompanyPlanApplyScope(t *testing.T) {
	t.Parallel()
	project := uuid.New()
	writer := auth.ProjectAccess{ProjectID: project, Permissions: []auth.ProjectPermission{
		auth.ProjectPermissionApprovalsResolve, auth.ProjectPermissionTasksWrite,
	}}
	resolverOnly := auth.ProjectAccess{ProjectID: project, Permissions: []auth.ProjectPermission{
		auth.ProjectPermissionApprovalsResolve,
	}}
	bound := uuid.NullUUID{UUID: project, Valid: true}
	tests := []struct {
		name   string
		role   auth.CompanyRole
		access auth.ProjectAccess
		scoped bool
		want   uuid.NullUUID
		err    error
	}{
		{"member writer in the Task chat's Project", auth.CompanyRoleMember, writer, true, bound, nil},
		{
			"member without project.tasks.write",
			auth.CompanyRoleMember,
			resolverOnly,
			true,
			uuid.NullUUID{},
			auth.ErrProjectPermissionRequired,
		},
		{
			"owner without project.tasks.write",
			auth.CompanyRoleOwner,
			resolverOnly,
			true,
			uuid.NullUUID{},
			auth.ErrProjectPermissionRequired,
		},
		{"Company plan, owner", auth.CompanyRoleOwner, auth.ProjectAccess{}, false, uuid.NullUUID{}, nil},
		{
			"Company plan, member",
			auth.CompanyRoleMember,
			auth.ProjectAccess{},
			false,
			uuid.NullUUID{},
			ErrApprovalForbidden,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := companyPlanApplyScope(test.role, test.access, test.scoped)
			if got != test.want || !errors.Is(err, test.err) || (test.err == nil && err != nil) {
				t.Fatalf("companyPlanApplyScope() = %v, %v; want %v, %v", got, err, test.want, test.err)
			}
		})
	}
}

func TestHumanInputMutationLockOrder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		file     string
		function string
		want     []string
	}{
		{
			name:     "shared member fence",
			file:     "authorization.go",
			function: "lockActiveHumanInputMember",
			want:     []string{"LockCompanyForMembershipMutation", "GetCompanyMemberForUpdate"},
		},
		{
			name:     "live origin fence",
			file:     "authorization.go",
			function: "lockLiveHumanInputOrigin",
			want:     []string{"lockHumanInputOriginMember", "lockHumanInputOriginChat"},
		},
		{
			name:     "origin member fence",
			file:     "authorization.go",
			function: "lockHumanInputOriginMember",
			want:     []string{"lockActiveHumanInputMember"},
		},
		{
			name:     "origin chat fence",
			file:     "authorization.go",
			function: "lockHumanInputOriginChat",
			want:     []string{"LockHumanInputOriginConversation", "LockLiveHumanInputOriginRun"},
		},
		{
			name:     "Approval request",
			file:     "approval_service.go",
			function: "RequestCompanyPlanApply",
			// The plan's Project and Task chat locks come after the member
			// locks and before the origin chat and Run locks: company ->
			// member -> project -> conversation -> run.
			want: []string{
				"lockHumanInputOriginMember",
				"LockCompanyPlanScope",
				"lockHumanInputOriginChat",
				"GetApprovalByOriginRequestKey",
				"CreateApproval",
			},
		},
		{
			name:     "Approval resolve",
			file:     "approval_service.go",
			function: "resolve",
			want:     []string{"lockActiveHumanInputMember", "LockApprovalForResolution"},
		},
		{
			name:     "Question ask",
			file:     "question_service.go",
			function: "Ask",
			want: []string{
				"lockLiveHumanInputOrigin",
				"GetQuestionByOriginRequestKey",
				"CreateQuestion",
			},
		},
		{
			name:     "Question resolve",
			file:     "question_service.go",
			function: "resolve",
			want:     []string{"lockActiveHumanInputMember", "LockQuestionForResolution"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := functionCallOrder(t, test.file, test.function)
			position := -1
			for _, want := range test.want {
				next := callIndexAfter(got, want, position)
				if next < 0 {
					t.Fatalf(
						"%s call order = %v, want %q after position %d",
						test.function,
						got,
						want,
						position,
					)
				}
				position = next
			}
		})
	}
}

func functionCallOrder(t *testing.T, filename, functionName string) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), filename, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name.Name != functionName || function.Body == nil {
			continue
		}
		var calls []string
		ast.Inspect(function.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch callee := call.Fun.(type) {
			case *ast.Ident:
				calls = append(calls, callee.Name)
			case *ast.SelectorExpr:
				calls = append(calls, callee.Sel.Name)
			}
			return true
		})
		return calls
	}
	t.Fatalf("function %s not found in %s", functionName, filename)
	return nil
}

func callIndexAfter(calls []string, name string, after int) int {
	for index := after + 1; index < len(calls); index++ {
		if calls[index] == name {
			return index
		}
	}
	return -1
}
