// Package usage owns immutable run-usage ingestion and transport-independent
// usage reporting.
package usage

import (
	"errors"
	"net/url"
	"time"
)

var (
	ErrInvalidInput             = errors.New("invalid usage report input")
	ErrUnauthenticated          = errors.New("usage report authentication is required")
	ErrForbidden                = errors.New("usage report is forbidden")
	ErrActivityInsightForbidden = errors.New("activity insight is forbidden")
	ErrNotFound                 = errors.New("usage report agent not found")
	ErrStore                    = errors.New("usage report store failure")
)

type Bucket string

const (
	BucketDay   Bucket = "day"
	BucketMonth Bucket = "month"
)

type CompanyPeriodType string

const (
	CompanyPeriodWeek  CompanyPeriodType = "week"
	CompanyPeriodMonth CompanyPeriodType = "month"
	CompanyPeriodYear  CompanyPeriodType = "year"
)

type CompanyPeriod struct {
	Type     CompanyPeriodType
	TimeZone string
}

type Period struct {
	StartAt  time.Time
	EndAt    time.Time
	TimeZone string
	Bucket   Bucket
}

func ParsePeriodQuery(rawQuery string) (Period, error) {
	query, err := url.ParseQuery(rawQuery)
	if err != nil || len(query) != 4 {
		return Period{}, ErrInvalidInput
	}
	value := func(key string) (string, bool) {
		values, ok := query[key]
		return firstSingleNonempty(values, ok)
	}
	startText, startOK := value("start_at")
	endText, endOK := value("end_at")
	timeZone, zoneOK := value("time_zone")
	bucketText, bucketOK := value("bucket")
	if !startOK || !endOK || !zoneOK || !bucketOK {
		return Period{}, ErrInvalidInput
	}
	for key := range query {
		switch key {
		case "start_at", "end_at", "time_zone", "bucket":
		default:
			return Period{}, ErrInvalidInput
		}
	}
	startAt, err := time.Parse(time.RFC3339Nano, startText)
	if err != nil {
		return Period{}, ErrInvalidInput
	}
	endAt, err := time.Parse(time.RFC3339Nano, endText)
	if err != nil {
		return Period{}, ErrInvalidInput
	}
	location, err := time.LoadLocation(timeZone)
	if err != nil || !isExactLocalMidnight(startAt, location) || !isExactLocalMidnight(endAt, location) {
		return Period{}, ErrInvalidInput
	}
	bucket := Bucket(bucketText)
	if bucket != BucketDay && bucket != BucketMonth {
		return Period{}, ErrInvalidInput
	}
	startLocal := startAt.In(location)
	if !endAt.After(startAt) || endAt.After(startLocal.AddDate(0, 0, 367)) {
		return Period{}, ErrInvalidInput
	}
	return Period{
		StartAt: startAt.UTC(), EndAt: endAt.UTC(), TimeZone: timeZone, Bucket: bucket,
	}, nil
}

func ParseCompanyPeriodQuery(rawQuery string) (CompanyPeriod, error) {
	query, err := url.ParseQuery(rawQuery)
	if err != nil || len(query) != 2 {
		return CompanyPeriod{}, ErrInvalidInput
	}
	periodText, periodOK := firstSingleNonempty(query["period"], query.Has("period"))
	timeZone, zoneOK := firstSingleNonempty(query["time_zone"], query.Has("time_zone"))
	if !periodOK || !zoneOK {
		return CompanyPeriod{}, ErrInvalidInput
	}
	for key := range query {
		if key != "period" && key != "time_zone" {
			return CompanyPeriod{}, ErrInvalidInput
		}
	}
	periodType := CompanyPeriodType(periodText)
	validPeriodType := periodType == CompanyPeriodWeek ||
		periodType == CompanyPeriodMonth ||
		periodType == CompanyPeriodYear
	if !validPeriodType {
		return CompanyPeriod{}, ErrInvalidInput
	}
	if timeZone == "Local" {
		return CompanyPeriod{}, ErrInvalidInput
	}
	if _, err := time.LoadLocation(timeZone); err != nil {
		return CompanyPeriod{}, ErrInvalidInput
	}
	return CompanyPeriod{Type: periodType, TimeZone: timeZone}, nil
}

func companyCalendarPeriods(selection CompanyPeriod, asOf time.Time) (Period, Period, error) {
	location, err := time.LoadLocation(selection.TimeZone)
	if err != nil || asOf.IsZero() {
		return Period{}, Period{}, ErrInvalidInput
	}
	local := asOf.In(location)
	var start, end, comparisonStart time.Time
	bucket := BucketDay
	switch selection.Type {
	case CompanyPeriodWeek:
		daysSinceMonday := (int(local.Weekday()) + 6) % 7
		start = time.Date(local.Year(), local.Month(), local.Day()-daysSinceMonday, 0, 0, 0, 0, location)
		end = start.AddDate(0, 0, 7)
		comparisonStart = start.AddDate(0, 0, -7)
	case CompanyPeriodMonth:
		start = time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, location)
		end = start.AddDate(0, 1, 0)
		comparisonStart = start.AddDate(0, -1, 0)
	case CompanyPeriodYear:
		start = time.Date(local.Year(), time.January, 1, 0, 0, 0, 0, location)
		end = start.AddDate(1, 0, 0)
		comparisonStart = start.AddDate(-1, 0, 0)
		bucket = BucketMonth
	default:
		return Period{}, Period{}, ErrInvalidInput
	}
	current := Period{
		StartAt: start.UTC(), EndAt: end.UTC(), TimeZone: selection.TimeZone, Bucket: bucket,
	}
	comparison := Period{
		StartAt: comparisonStart.UTC(), EndAt: start.UTC(), TimeZone: selection.TimeZone, Bucket: bucket,
	}
	return current, comparison, nil
}

func firstSingleNonempty(values []string, exists bool) (string, bool) {
	if !exists || len(values) != 1 || values[0] == "" {
		return "", false
	}
	return values[0], true
}

func isExactLocalMidnight(value time.Time, location *time.Location) bool {
	if location == nil {
		return false
	}
	local := value.In(location)
	if local.Hour() != 0 || local.Minute() != 0 || local.Second() != 0 || local.Nanosecond() != 0 {
		return false
	}
	expected := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location)
	return expected.Equal(value)
}
