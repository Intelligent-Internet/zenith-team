package company

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
)

func TestCompanyResponseSerializesOnlyPublicCompanyState(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.August, 17, 8, 0, 0, 0, time.UTC)
	value := Response{
		ID:   uuid.MustParse("0197f590-10b8-7cc0-a6c6-160ed7a18b2c"),
		Name: "Northwind Robotics", Slug: "northwind-robotics", TaskPrefix: "NWR",
		BusinessContext: "Warehouse automation", Role: auth.CompanyRoleOwner,
		Revision: 7, CreatedAt: now, UpdatedAt: now,
	}

	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	got := make([]string, 0, len(body))
	for key := range body {
		got = append(got, key)
	}
	slices.Sort(got)
	want := []string{
		"agentSetupCompletedAt", "avatarAttachmentId", "businessContext", "contactEmail", "createdAt",
		"firstPlanAppliedAt", "id", "name", "permissions", "phoneCountryIso2",
		"phoneNumber", "revision", "role", "slug", "taskPrefix", "updatedAt",
		"websiteUrl",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("Company response keys = %v, want %v", got, want)
	}
}

func TestCompanyStatisticsSerializesFlatCounts(t *testing.T) {
	t.Parallel()

	raw, err := json.Marshal(Statistics{MemberCount: 4, RuntimeCount: 3, RuntimeOnlineCount: 2})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if got, want := string(raw), `{"memberCount":4,"runtimeCount":3,"runtimeOnlineCount":2}`; got != want {
		t.Fatalf("Marshal() = %s, want %s", got, want)
	}
}

func TestCompanyCreateNormalizesInput(t *testing.T) {
	t.Parallel()

	input, err := normalizeCreateInput(CreateCompanyInput{
		Name: " Northwind ", Slug: " northwind ", TaskPrefix: " NWR ",
		BusinessContext: " Warehouse automation ",
	})
	if err != nil {
		t.Fatalf("normalizeCreateInput() = %+v, %v", input, err)
	}
	if input.Name != "Northwind" || input.Slug != "northwind" || input.TaskPrefix != "NWR" ||
		input.BusinessContext != "Warehouse automation" {
		t.Fatalf("normalizeCreateInput() = %+v", input)
	}
}

// The website URL is the seed for the CEO Agent's onboarding research, so a
// value that is not an absolute http(s) address is rejected at the boundary
// rather than handed to the Agent to fail on.
func TestNormalizeCreateInputRejectsInvalidWebsiteURL(t *testing.T) {
	t.Parallel()

	for name, website := range map[string]string{
		"relative":     "acme.example",
		"missing host": "https://",
		"other scheme": "ftp://acme.example",
		"not a url":    "://",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := normalizeCreateInput(CreateCompanyInput{
				Name: "Acme", Slug: "acme", TaskPrefix: "AC",
				BusinessContext: "Builds tools for local retailers.",
				WebsiteURL:      website,
			}); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("normalizeCreateInput(%q) error = %v, want ErrInvalidInput", website, err)
			}
		})
	}
}

// A founder without a website is the common case at the idea stage, and the
// absent value must not be mistaken for an invalid one.
func TestNormalizeCreateInputAcceptsAbsentWebsiteURL(t *testing.T) {
	t.Parallel()

	input, err := normalizeCreateInput(CreateCompanyInput{
		Name: "Acme", Slug: "acme", TaskPrefix: "AC",
		BusinessContext: "Builds tools for local retailers.",
		WebsiteURL:      "  https://acme.example  ",
	})
	if err != nil {
		t.Fatalf("normalizeCreateInput() error = %v", err)
	}
	if input.WebsiteURL != "https://acme.example" {
		t.Fatalf("normalizeCreateInput() website = %q, want trimmed", input.WebsiteURL)
	}
}
