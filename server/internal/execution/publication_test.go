package execution

import (
	"errors"
	"testing"

	"github.com/Intelligent-Internet/zenith-team/server/internal/events"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	dbgen "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

func TestNormalizePublicationCreateInputRequiresExplicitExposureConfirmation(t *testing.T) {
	t.Parallel()

	if _, err := normalizePublicationCreateInput(PublicationCreateInput{}); !errors.Is(
		err,
		ErrPublicationInvalidInput,
	) {
		t.Fatalf("normalizePublicationCreateInput() error = %v, want ErrPublicationInvalidInput", err)
	}
	input, err := normalizePublicationCreateInput(PublicationCreateInput{ConfirmPublicExposure: true})
	if err != nil || !input.ConfirmPublicExposure {
		t.Fatalf("normalizePublicationCreateInput() = %+v, %v", input, err)
	}
}

func TestPublicationProviderKeyAcceptsNewProviderTypesButRejectsUnsafeBindings(t *testing.T) {
	t.Parallel()

	key := PublicationProviderKey{
		Type:      PublicationProviderType("azure_container_apps"),
		BindingID: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	if !key.IsValid() {
		t.Fatalf("PublicationProviderKey.IsValid() = false for %+v", key)
	}
	key.BindingID = "subscription/secret/resource"
	if key.IsValid() {
		t.Fatalf("PublicationProviderKey.IsValid() = true for %+v", key)
	}
}

func TestPublicationIssueReportIncludesClaimIdentity(t *testing.T) {
	t.Parallel()

	publicationID := uuid.Must(uuid.NewV7())
	err := wrapPublicationWorkError(dbgen.ClaimOutputPublicationWorkRow{
		ID: publicationID, AttemptCount: 2, ProviderType: string(PublicationProviderTypeCloudRun),
	}, errors.Join(errPublicationPublishFailed, errors.New("provider offline")))
	report := publicationIssueReport(err)
	if report.Issue != PublicationIssuePublishFailed || report.PublicationID != publicationID ||
		report.Attempt != 2 || report.ProviderType != PublicationProviderTypeCloudRun ||
		report.Err == nil {
		t.Fatalf("publicationIssueReport() = %+v", report)
	}
}

func TestPublicationFailureCopyIsClosedAndActionable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		code PublicationFailureCode
		want string
	}{
		{PublicationFailureSourceInvalid, "The ZIP is not a supported application source archive."},
		{PublicationFailureBuildFailed, "The application source could not be built."},
		{PublicationFailureDeployFailed, "The hosting provider could not start this application."},
		{
			PublicationFailureProviderUnavailable,
			"The hosting provider is temporarily unavailable. Try publishing again.",
		},
	}
	for _, test := range tests {
		t.Run(string(test.code), func(t *testing.T) {
			t.Parallel()
			if got := publicationFailureDetail(test.code); got != test.want {
				t.Fatalf("publicationFailureDetail(%q) = %q, want %q", test.code, got, test.want)
			}
		})
	}
}

func TestPublicationCanRetryRecoverableFailures(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name            string
		status          PublicationStatus
		failureCode     PublicationFailureCode
		serviceMayExist bool
		expected        bool
	}{
		{
			name:   "provider unavailable before deployment",
			status: PublicationStatusFailed, failureCode: PublicationFailureProviderUnavailable,
			expected: true,
		},
		{
			name:   "provider unavailable after deployment started",
			status: PublicationStatusFailed, failureCode: PublicationFailureProviderUnavailable,
			serviceMayExist: true, expected: true,
		},
		{
			name:   "invalid source",
			status: PublicationStatusFailed, failureCode: PublicationFailureSourceInvalid,
		},
		{
			name:   "failed build",
			status: PublicationStatusFailed, failureCode: PublicationFailureBuildFailed,
		},
		{
			name:   "failed deployment",
			status: PublicationStatusFailed, failureCode: PublicationFailureDeployFailed,
			expected: true,
		},
		{
			name:   "failed deployment after resources created",
			status: PublicationStatusFailed, failureCode: PublicationFailureDeployFailed,
			serviceMayExist: true, expected: true,
		},
		{name: "unpublished", status: PublicationStatusUnpublished, expected: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			row := dbgen.OutputPublication{
				Status:                    string(test.status),
				ProviderResourcesMayExist: test.serviceMayExist,
			}
			if test.failureCode != "" {
				row.FailureCode = pgtype.Text{String: string(test.failureCode), Valid: true}
			}
			if got := publicationCanRetry(row); got != test.expected {
				t.Fatalf("publicationCanRetry() = %t, want %t", got, test.expected)
			}
		})
	}
}

func TestPublicationSourceLookupFailureCodeDistinguishesMissingSourceFromStoreOutage(t *testing.T) {
	t.Parallel()

	if got := publicationSourceLookupFailureCode(pgx.ErrNoRows); got != PublicationFailureSourceInvalid {
		t.Fatalf(
			"publicationSourceLookupFailureCode(pgx.ErrNoRows) = %q, want %q",
			got,
			PublicationFailureSourceInvalid,
		)
	}
	if got := publicationSourceLookupFailureCode(
		errors.New("database unavailable"),
	); got != PublicationFailureProviderUnavailable {
		t.Fatalf(
			"publicationSourceLookupFailureCode(database error) = %q, want %q",
			got,
			PublicationFailureProviderUnavailable,
		)
	}
}

func TestValidatePublicationResultRejectsUntrustedProviderMetadata(t *testing.T) {
	t.Parallel()

	digest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	valid := PublicationProviderResult{
		PublicationProviderResources: PublicationProviderResources{
			SourceRef:      "provider://sources/publication-1",
			SourceTemplate: "vite",
			BuildRef:       "provider://builds/build-1",
			ArtifactRef:    "provider://artifacts/application",
			ArtifactDigest: digest,
			ServiceRef:     "provider://services/application",
		},
		ServiceURI: "https://application.example.run.app",
	}
	if err := validatePublicationResult(valid); err != nil {
		t.Fatalf("validatePublicationResult(valid) error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*PublicationProviderResult)
	}{
		{name: "short digest", mutate: func(result *PublicationProviderResult) {
			result.ArtifactDigest = "sha256:abc"
		}},
		{name: "missing service ref", mutate: func(result *PublicationProviderResult) {
			result.ServiceRef = ""
		}},
		{name: "insecure service URI", mutate: func(result *PublicationProviderResult) {
			result.ServiceURI = "http://application.example.run.app"
		}},
		{name: "service URI credentials", mutate: func(result *PublicationProviderResult) {
			result.ServiceURI = "https://user:password@application.example.run.app"
		}},
		{name: "service URI fragment", mutate: func(result *PublicationProviderResult) {
			result.ServiceURI = "https://application.example.run.app/#fragment"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			result := valid
			test.mutate(&result)
			if err := validatePublicationResult(result); !errors.Is(
				err,
				ErrPublicationStoreUnavailable,
			) {
				t.Fatalf(
					"validatePublicationResult() error = %v, want ErrPublicationStoreUnavailable",
					err,
				)
			}
		})
	}
}

func publicationTestBus(t *testing.T) *events.Bus {
	t.Helper()
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return bus
}
