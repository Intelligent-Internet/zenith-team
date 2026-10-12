package handler

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"boardly.dev/boardly/server/internal/calendar"
	"github.com/gin-gonic/gin"
)

func TestCalendarRequestBindingPreservesEventFieldsAndRevision(t *testing.T) {
	t.Parallel()

	body := `{"type":"event","title":"Review","color":"sage","startLocal":"2026-09-22T09:00","endLocal":"2026-09-22T10:00","timeZone":"Asia/Ho_Chi_Minh","recurrence":"none","expectedRevision":`
	bind := func(revision string, update bool) (calendar.Input, error) {
		t.Helper()
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(body+revision+`}`))
		c.Request.Header.Set("Content-Type", "application/json")
		if update {
			request, err := bindRequest[calendarUpdateRequest](c, 4096)
			input := request.input()
			input.ExpectedRevision = request.ExpectedRevision
			return input, err
		}
		request, err := bindRequest[calendarCreateRequest](c, 4096)
		return request.input(), err
	}

	for _, test := range []struct {
		name, revision string
		update         bool
		valid          bool
	}{
		{"create", "0", false, true},
		{"create negative revision", "-1", false, false},
		{"create positive revision", "1", false, false},
		{"update", "2", true, true},
		{"update zero revision", "0", true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			input, err := bind(test.revision, test.update)
			if (err == nil) != test.valid {
				t.Fatalf("bind revision %s: error = %v, want valid %v", test.revision, err, test.valid)
			}
			if !test.valid {
				return
			}
			if input.Title != "Review" || input.StartLocal != "2026-09-22T09:00" {
				t.Fatalf("event fields lost during request mapping: %+v", input)
			}
			if test.update && input.ExpectedRevision != 2 {
				t.Fatalf("update revision = %d; want 2", input.ExpectedRevision)
			}
		})
	}
}

func TestCalendarErrorContracts(t *testing.T) {
	cases := []struct {
		name         string
		write        func(*gin.Context, error)
		sentinel     error
		status       int
		detail, code string
	}{
		{"calendar not found", writeCalendarError, calendar.ErrNotFound, 404, "calendar event not found", "calendar_not_found"},
		{"calendar invalid", writeCalendarError, calendar.ErrInvalid, 422, "check the event fields, time zone and date range", "calendar_invalid"},
		{"calendar conflict", writeCalendarError, calendar.ErrConflict, 409, "this event changed; refresh before saving", "calendar_conflict"},
		{"calendar range", writeCalendarError, calendar.ErrRange, 422, "choose a smaller calendar date range", "calendar_range_exceeded"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			assertMappedErrorResponse(t, func(c *gin.Context) { tt.write(c, errors.Join(errors.New("private provider detail"), tt.sentinel)) }, tt.status, tt.detail, tt.code)
		})
	}
}

func TestCalendarCustomRecurrenceBinding(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, fields string
		wantValid    bool
	}{
		{"custom weekly", `"recurrenceInterval":2,"recurrenceWeekdays":[1,3],"recurrenceCount":13`, true},
		{"zero interval", `"recurrenceInterval":0`, false},
		{"invalid weekday", `"recurrenceWeekdays":[7]`, false},
		{"duplicate weekdays", `"recurrenceWeekdays":[1,1]`, false},
		{"zero occurrences", `"recurrenceCount":0`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{"type":"event","title":"Review","color":"sage","startLocal":"2026-09-23T09:00","endLocal":"2026-09-23T10:00","timeZone":"UTC","recurrence":"weekly",`+tt.fields+`}`))
			c.Request.Header.Set("Content-Type", "application/json")
			request, err := bindRequest[calendarCreateRequest](c, 4096)
			if (err == nil) != tt.wantValid {
				t.Fatalf("bind error = %v, want valid %v", err, tt.wantValid)
			}
			if tt.wantValid {
				got := request.input()
				if got.RecurrenceInterval != 2 || len(got.RecurrenceWeekdays) != 2 || got.RecurrenceCount == nil || *got.RecurrenceCount != 13 {
					t.Fatalf("custom fields lost: %+v", got)
				}
			}
		})
	}
}
