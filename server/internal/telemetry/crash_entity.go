package telemetry

// ReportType is the closed crash report classification.
type ReportType string

const (
	ReportTypeCrash  ReportType = "crash"
	ReportTypeFreeze ReportType = "freeze"
)

const (
	// MaxReasonBytes bounds the stored redacted reason/stack. Oversize input
	// is trimmed to this bound before and after redaction.
	MaxReasonBytes = 16 * 1024

	maxPlatformBytes   = 64
	maxAppVersionBytes = 120
)

// Submission is one client-supplied crash/freeze report before redaction.
type Submission struct {
	Type         ReportType
	Platform     string
	Architecture string
	AppVersion   string
	Reason       string
}

// Report is the redacted write input persisted by the service. PostgreSQL owns
// the row identity and receipt timestamp.
type Report struct {
	Type         ReportType
	Platform     string
	Architecture string
	AppVersion   string
	Reason       string
}
