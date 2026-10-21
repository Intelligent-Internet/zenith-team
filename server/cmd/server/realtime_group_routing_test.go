package main

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/company"
)

// Every active member reads a Company's Groups, so
// group.changed and group.deleted go to the Company room, and carry the
// Group and its revision only.
func TestGroupFramesGoToTheCompanyRoom(t *testing.T) {
	t.Parallel()
	companyID, groupID := uuid.New(), uuid.New()
	for _, testCase := range []struct {
		name    string
		payload any
		want    string
	}{
		{
			name:    company.EventGroupChanged,
			payload: company.GroupChanged{GroupID: groupID, Revision: 4},
			want:    `{"groupId":"` + groupID.String() + `","revision":4}`,
		},
		{
			name:    company.EventGroupDeleted,
			payload: company.GroupDeleted{GroupID: groupID},
			want:    `{"groupId":"` + groupID.String() + `"}`,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			bus, recorder := routedBus(t)
			publish(t, bus, companyID, company.TopicGroup, testCase.name, testCase.payload)
			if len(recorder.sent) != 1 || recorder.sent[0] != "company/"+companyID.String() {
				t.Fatalf("rooms = %v, want the Company room", recorder.sent)
			}
			payload, err := json.Marshal(testCase.payload)
			if err != nil {
				t.Fatal(err)
			}
			if string(payload) != testCase.want {
				t.Fatalf("payload = %s, want %s", payload, testCase.want)
			}
		})
	}
}
