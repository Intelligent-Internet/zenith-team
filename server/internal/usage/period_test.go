package usage

import (
	"errors"
	"net/url"
	"testing"
	"time"
)

func TestParsePeriodQueryValidatesStrictCalendarBounds(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		query   url.Values
		want    Period
		wantErr bool
	}{
		{
			name: "DST-crossing day buckets",
			query: url.Values{
				"start_at":  {"2026-03-02T00:00:00-05:00"},
				"end_at":    {"2026-03-09T00:00:00-04:00"},
				"time_zone": {"America/New_York"},
				"bucket":    {"day"},
			},
			want: Period{
				StartAt:  time.Date(2026, 3, 2, 5, 0, 0, 0, time.UTC),
				EndAt:    time.Date(2026, 3, 9, 4, 0, 0, 0, time.UTC),
				TimeZone: "America/New_York", Bucket: BucketDay,
			},
		},
		{
			name: "year month buckets",
			query: url.Values{
				"start_at":  {"2026-01-01T00:00:00+07:00"},
				"end_at":    {"2027-01-01T00:00:00+07:00"},
				"time_zone": {"Asia/Ho_Chi_Minh"},
				"bucket":    {"month"},
			},
			want: Period{
				StartAt:  time.Date(2025, 12, 31, 17, 0, 0, 0, time.UTC),
				EndAt:    time.Date(2026, 12, 31, 17, 0, 0, 0, time.UTC),
				TimeZone: "Asia/Ho_Chi_Minh", Bucket: BucketMonth,
			},
		},
		{
			name: "non-midnight rejected",
			query: url.Values{
				"start_at": {"2026-01-01T00:01:00Z"}, "end_at": {"2026-01-02T00:00:00Z"},
				"time_zone": {"UTC"}, "bucket": {"day"},
			},
			wantErr: true,
		},
		{
			name: "timezone offset mismatch rejected",
			query: url.Values{
				"start_at": {"2026-01-01T00:00:00Z"}, "end_at": {"2026-01-02T00:00:00Z"},
				"time_zone": {"Asia/Ho_Chi_Minh"}, "bucket": {"day"},
			},
			wantErr: true,
		},
		{
			name: "more than 367 local days rejected",
			query: url.Values{
				"start_at": {"2026-01-01T00:00:00Z"}, "end_at": {"2027-01-04T00:00:00Z"},
				"time_zone": {"UTC"}, "bucket": {"day"},
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParsePeriodQuery(tt.query.Encode())
			if tt.wantErr {
				if !errors.Is(err, ErrInvalidInput) {
					t.Fatalf("ParsePeriodQuery() error = %v, want ErrInvalidInput", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParsePeriodQuery() error = %v", err)
			}
			if !got.StartAt.Equal(tt.want.StartAt) || !got.EndAt.Equal(tt.want.EndAt) ||
				got.TimeZone != tt.want.TimeZone || got.Bucket != tt.want.Bucket {
				t.Fatalf("ParsePeriodQuery() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParsePeriodQueryRejectsMissingDuplicateAndUnknownKeys(t *testing.T) {
	t.Parallel()
	valid := "start_at=2026-01-01T00%3A00%3A00Z&end_at=2026-01-02T00%3A00%3A00Z&time_zone=UTC&bucket=day"
	for _, test := range []struct {
		name  string
		query string
	}{
		{name: "missing", query: "start_at=2026-01-01T00%3A00%3A00Z"},
		{name: "duplicate", query: valid + "&bucket=month"},
		{name: "unknown", query: valid + "&projectId=ignored"},
		{name: "malformed", query: valid + "&bad=%zz"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := ParsePeriodQuery(test.query); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("ParsePeriodQuery() error = %v, want ErrInvalidInput", err)
			}
		})
	}
}

func TestParseCompanyPeriodQueryRejectsInvalidInput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		query string
	}{
		{name: "missing", query: "period=month"},
		{name: "duplicate", query: "period=month&period=week&time_zone=UTC"},
		{name: "unknown", query: "period=month&time_zone=UTC&provider=codex"},
		{name: "invalid period", query: "period=quarter&time_zone=UTC"},
		{name: "invalid timezone", query: "period=month&time_zone=Mars%2FOlympus"},
		{name: "process local timezone", query: "period=month&time_zone=Local"},
		{name: "malformed", query: "period=month&time_zone=%zz"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := ParseCompanyPeriodQuery(test.query); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("ParseCompanyPeriodQuery() error = %v, want ErrInvalidInput", err)
			}
		})
	}
}

func TestCompanyCalendarPeriodsUseLocalCalendarBounds(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		period         CompanyPeriod
		asOf           time.Time
		wantCurrent    Period
		wantComparison Period
	}{
		{
			name:   "week crosses daylight saving boundary",
			period: CompanyPeriod{Type: CompanyPeriodWeek, TimeZone: "America/New_York"},
			asOf:   time.Date(2026, 3, 8, 16, 0, 0, 0, time.UTC),
			wantCurrent: Period{
				StartAt:  time.Date(2026, 3, 2, 5, 0, 0, 0, time.UTC),
				EndAt:    time.Date(2026, 3, 9, 4, 0, 0, 0, time.UTC),
				TimeZone: "America/New_York", Bucket: BucketDay,
			},
			wantComparison: Period{
				StartAt:  time.Date(2026, 2, 23, 5, 0, 0, 0, time.UTC),
				EndAt:    time.Date(2026, 3, 2, 5, 0, 0, 0, time.UTC),
				TimeZone: "America/New_York", Bucket: BucketDay,
			},
		},
		{
			name:   "month",
			period: CompanyPeriod{Type: CompanyPeriodMonth, TimeZone: "Asia/Ho_Chi_Minh"},
			asOf:   time.Date(2026, 8, 6, 4, 0, 0, 0, time.UTC),
			wantCurrent: Period{
				StartAt:  time.Date(2026, 7, 31, 17, 0, 0, 0, time.UTC),
				EndAt:    time.Date(2026, 8, 31, 17, 0, 0, 0, time.UTC),
				TimeZone: "Asia/Ho_Chi_Minh", Bucket: BucketDay,
			},
			wantComparison: Period{
				StartAt:  time.Date(2026, 6, 30, 17, 0, 0, 0, time.UTC),
				EndAt:    time.Date(2026, 7, 31, 17, 0, 0, 0, time.UTC),
				TimeZone: "Asia/Ho_Chi_Minh", Bucket: BucketDay,
			},
		},
		{
			name:   "year",
			period: CompanyPeriod{Type: CompanyPeriodYear, TimeZone: "UTC"},
			asOf:   time.Date(2026, 8, 6, 4, 0, 0, 0, time.UTC),
			wantCurrent: Period{
				StartAt:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
				EndAt:    time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC),
				TimeZone: "UTC", Bucket: BucketMonth,
			},
			wantComparison: Period{
				StartAt:  time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
				EndAt:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
				TimeZone: "UTC", Bucket: BucketMonth,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			current, comparison, err := companyCalendarPeriods(test.period, test.asOf)
			if err != nil {
				t.Fatalf("companyCalendarPeriods() error = %v", err)
			}
			if current != test.wantCurrent || comparison != test.wantComparison {
				t.Fatalf(
					"companyCalendarPeriods() = %+v / %+v, want %+v / %+v",
					current,
					comparison,
					test.wantCurrent,
					test.wantComparison,
				)
			}
		})
	}
}
