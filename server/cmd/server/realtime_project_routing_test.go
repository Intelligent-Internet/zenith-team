package main

import (
	"bytes"
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/activity"
	"github.com/Intelligent-Internet/zenith-team/server/internal/approval"
	"github.com/Intelligent-Internet/zenith-team/server/internal/attachment"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/canvas"
	"github.com/Intelligent-Internet/zenith-team/server/internal/company"
	"github.com/Intelligent-Internet/zenith-team/server/internal/conversation"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	"github.com/Intelligent-Internet/zenith-team/server/internal/realtime"
	"github.com/Intelligent-Internet/zenith-team/server/internal/task"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

// project.access_changed reaches exactly the people the change concerned
// (ADR 0017), and the frame names nobody: only the Project and its access
// revision cross the wire.
func TestProjectAccessChangedRoutesToItsRecipients(t *testing.T) {
	t.Parallel()

	companyID := uuid.New()
	projectID := uuid.New()
	reader, manager := uuid.New(), uuid.New()

	bus, recorder := routedBus(t)
	publish(
		t,
		bus,
		companyID,
		company.TopicProject,
		company.EventProjectAccessChanged,
		company.ProjectAccessChanged{
			ProjectID: projectID, Revision: 7,
			RecipientUserIDs: []uuid.UUID{reader, manager, reader, uuid.Nil},
		},
	)
	want := []string{
		"company_user/" + companyID.String() + "/" + reader.String(),
		"company_user/" + companyID.String() + "/" + manager.String(),
	}
	if !slices.Equal(recorder.sent, want) {
		t.Fatalf("rooms = %v, want %v", recorder.sent, want)
	}
	for _, frame := range recorder.frames {
		encoded, err := json.Marshal(frame.Payload)
		if err != nil {
			t.Fatal(err)
		}
		wire := `{"projectId":"` + projectID.String() + `","revision":7}`
		if !bytes.Equal(encoded, []byte(wire)) {
			t.Fatalf("payload = %s, want %s", encoded, wire)
		}
	}

	bus, recorder = routedBus(t)
	publish(
		t,
		bus,
		companyID,
		company.TopicProject,
		company.EventProjectAccessChanged,
		company.ProjectAccessChanged{
			ProjectID: projectID, Revision: 8, RecipientUserIDs: []uuid.UUID{reader}, CompanyWide: true,
		},
	)
	if want := []string{"company/" + companyID.String()}; !slices.Equal(recorder.sent, want) {
		t.Fatalf("Company-visible rooms = %v, want %v", recorder.sent, want)
	}

	bus, recorder = routedBus(t)
	publish(
		t,
		bus,
		companyID,
		company.TopicProject,
		company.EventProjectAccessChanged,
		company.ProjectAccessChanged{
			ProjectID: projectID, Revision: 9,
		},
	)
	if len(recorder.sent) != 0 {
		t.Fatalf("rooms with no recipients = %v, want none", recorder.sent)
	}
}

// projectFrame is one frame about work in a restricted Project, built with
// the audience its producer would have stamped.
type projectFrame struct {
	topic, name string
	payload     func(*auth.ProjectAudience) any
}

// projectFrames is every frame the router sends by a Project audience: each
// carries a Task or chat id of the Project it is about.
func projectFrames(taskID, conversationID, projectID uuid.UUID) []projectFrame {
	taskKind := string(conversation.KindTask)
	frame := func(audience *auth.ProjectAudience) auth.ProjectFrame {
		return auth.ProjectFrame{Audience: audience}
	}
	route := func(audience *auth.ProjectAudience) execution.ConversationRoute {
		return execution.ConversationRoute{ConversationKind: taskKind, ProjectFrame: frame(audience)}
	}
	return []projectFrame{
		{canvas.Topic, canvas.EventChanged, func(a *auth.ProjectAudience) any {
			return canvas.Changed{ProjectFrame: frame(a), ProjectID: projectID, Sequence: 1}
		}},
		{canvas.Topic, canvas.EventDocumentChanged, func(a *auth.ProjectAudience) any {
			return canvas.DocumentChanged{
				ProjectFrame: frame(a),
				ProjectID:    projectID,
				PageID:       uuid.New(),
				Sequence:     1,
			}
		}},
		{canvas.Topic, canvas.EventPagePreviewChanged, func(a *auth.ProjectAudience) any {
			return canvas.PagePreviewChanged{
				ProjectFrame: frame(a),
				ProjectID:    &projectID,
				PageID:       uuid.New(),
				GenerationID: uuid.New(),
				Revision:     1,
			}
		}},
		{task.Topic, task.EventCreated, func(a *auth.ProjectAudience) any {
			return task.Response{
				ID:             taskID,
				ProjectID:      projectID,
				ConversationID: conversationID,
				ProjectFrame:   frame(a),
			}
		}},
		{task.Topic, task.EventDeleted, func(a *auth.ProjectAudience) any {
			return task.Removal{TaskID: taskID, ProjectID: projectID, ProjectFrame: frame(a)}
		}},
		{task.Topic, task.EventReportReady, func(a *auth.ProjectAudience) any {
			return task.ReportReadyPayload{TaskID: taskID.String(), ProjectFrame: frame(a)}
		}},
		{task.Topic, task.EventReportChanged, func(a *auth.ProjectAudience) any {
			return task.ReportChangedPayload{TaskID: taskID.String(), ProjectFrame: frame(a)}
		}},
		{execution.Topic, execution.EventRunCompleted, func(a *auth.ProjectAudience) any {
			return execution.RunSnapshot{
				TaskID:            &taskID,
				ConversationID:    conversationID,
				ConversationRoute: route(a),
			}
		}},
		{execution.Topic, execution.EventRunMessageCreated, func(a *auth.ProjectAudience) any {
			return execution.TranscriptItem{
				RunMessagePayload: protocol.RunMessagePayload{ConversationID: conversationID.String()},
				ConversationRoute: route(a),
			}
		}},
		{conversation.Topic, conversation.EventConversationUpdated, func(a *auth.ProjectAudience) any {
			return conversation.Snapshot{
				Conversation: conversation.Response{
					ID:     conversationID,
					Kind:   conversation.KindTask,
					TaskID: &taskID,
				},
				ProjectFrame: frame(a),
			}
		}},
		{conversation.TopicMessage, conversation.EventMessageCreated, func(a *auth.ProjectAudience) any {
			return conversation.PostedMessage{
				MessageResponse:  conversation.MessageResponse{ConversationID: conversationID},
				ConversationKind: conversation.KindTask, ProjectFrame: frame(a),
			}
		}},
		{conversation.TopicMessage, conversation.EventMessageUpdated, func(a *auth.ProjectAudience) any {
			return conversation.MessageUpdated{
				ConversationID:   conversationID,
				ConversationKind: conversation.KindTask,
				ProjectFrame:     frame(a),
			}
		}},
		{attachment.Topic, attachment.EventDeleted, func(a *auth.ProjectAudience) any {
			return attachment.Removal{
				AttachmentID: uuid.New(), ConversationID: &conversationID, ConversationKind: taskKind,
				ProjectFrame: frame(a),
			}
		}},
		{approval.Topic, approval.EventApprovalRequested, func(a *auth.ProjectAudience) any {
			return approval.Approval{
				TaskID:         &taskID,
				ConversationID: conversationID,
				ProjectFrame:   frame(a),
			}
		}},
		{approval.TopicQuestion, approval.EventQuestionAsked, func(a *auth.ProjectAudience) any {
			return approval.Question{
				TaskID:         &taskID,
				ConversationID: conversationID,
				ProjectFrame:   frame(a),
			}
		}},
		{activity.Topic, activity.EventCreated, func(a *auth.ProjectAudience) any {
			return activity.Entry{
				ID: uuid.NewString(), Action: task.EventUpdated,
				Entity:  activity.EntityRef{ID: taskID.String(), Type: protocol.EntityTask},
				Details: json.RawMessage(`{}`), ProjectFrame: frame(a),
			}
		}},
		{company.TopicProject, company.EventProjectUpdated, func(a *auth.ProjectAudience) any {
			return company.ProjectLifecycle{ID: projectID, ProjectFrame: frame(a)}
		}},
	}
}

// Nothing about work in a restricted Project reaches someone who cannot read
// it (ADR 0017): every frame that carries a Task or chat id of the Project
// goes to exactly the audience its producer decided in the transaction, never
// to the Company room and never to a non-reader. A frame staged without an
// audience is dropped and counted, not widened.
func TestProjectFramesReachOnlyTheProjectsReaders(t *testing.T) {
	t.Parallel()
	companyID, projectID := uuid.New(), uuid.New()
	taskID, conversationID := uuid.New(), uuid.New()
	readerA, readerB, nonReader := uuid.New(), uuid.New(), uuid.New()
	readers := &auth.ProjectAudience{UserIDs: []uuid.UUID{readerA, readerB, readerA, uuid.Nil}}

	for _, frame := range projectFrames(taskID, conversationID, projectID) {
		t.Run(frame.name, func(t *testing.T) {
			t.Parallel()
			bus, recorder := routedBus(t)
			publish(t, bus, companyID, frame.topic, frame.name, frame.payload(readers))
			want := []string{
				"company_user/" + companyID.String() + "/" + readerA.String(),
				"company_user/" + companyID.String() + "/" + readerB.String(),
			}
			got := slices.Clone(recorder.sent)
			sort.Strings(got)
			sort.Strings(want)
			if !slices.Equal(got, want) {
				t.Fatalf("rooms = %v, want exactly the readers %v", recorder.sent, want)
			}
			for index, room := range recorder.sent {
				if strings.HasSuffix(room, nonReader.String()) || strings.HasPrefix(room, "company/") {
					t.Fatalf(
						"room %s received a frame about Task %s / chat %s",
						room,
						taskID,
						conversationID,
					)
				}
				encoded, err := json.Marshal(recorder.frames[index].Payload)
				if err == nil && strings.Contains(string(encoded), readerB.String()) &&
					!strings.Contains(room, readerB.String()) {
					t.Fatalf("the audience leaked onto the wire: %s", encoded)
				}
			}

			if frame.topic == activity.Topic {
				// An Activity row with no audience is one that names no
				// Project; activity.entryAudience decides that, and the
				// row keeps its frozen visibility route.
				return
			}
			bus, recorder = routedBus(t)
			before := realtime.M.ProjectFramesDroppedTotal.Load()
			publish(t, bus, companyID, frame.topic, frame.name, frame.payload(nil))
			if len(recorder.sent) != 0 {
				t.Fatalf("a frame with no audience reached %v; it must be dropped", recorder.sent)
			}
			if realtime.M.ProjectFramesDroppedTotal.Load() <= before {
				t.Fatal("a frame with no audience was dropped without being counted")
			}
		})
	}
}

// A Company-visible Project's frames go to the Company room once; an empty
// audience reaches nobody.
func TestProjectFramesOfACompanyVisibleProjectUseTheCompanyRoom(t *testing.T) {
	t.Parallel()
	companyID := uuid.New()
	for _, frame := range projectFrames(uuid.New(), uuid.New(), uuid.New()) {
		bus, recorder := routedBus(t)
		publish(
			t,
			bus,
			companyID,
			frame.topic,
			frame.name,
			frame.payload(&auth.ProjectAudience{CompanyWide: true}),
		)
		if want := []string{"company/" + companyID.String()}; !slices.Equal(recorder.sent, want) {
			t.Fatalf("%s rooms = %v, want %v", frame.name, recorder.sent, want)
		}
		bus, recorder = routedBus(t)
		publish(
			t,
			bus,
			companyID,
			frame.topic,
			frame.name,
			frame.payload(&auth.ProjectAudience{UserIDs: []uuid.UUID{}}),
		)
		if len(recorder.sent) != 0 {
			t.Fatalf("%s with no readers reached %v", frame.name, recorder.sent)
		}
	}
}

// byProject keeps to the broadcaster's bound of 50 users per batch.
func TestProjectFramesGoOutInBatchesOfFifty(t *testing.T) {
	t.Parallel()
	companyID := uuid.New()
	readers := make([]uuid.UUID, 120)
	for index := range readers {
		readers[index] = uuid.New()
	}
	bus, recorder := routedBus(t)
	publish(t, bus, companyID, task.Topic, task.EventUpdated, task.Response{
		ProjectFrame: auth.ProjectFrame{Audience: &auth.ProjectAudience{UserIDs: readers}},
	})
	if !slices.Equal(recorder.batches, []int{50, 50, 20}) {
		t.Fatalf("batches = %v, want [50 50 20]", recorder.batches)
	}
	if len(recorder.sent) != len(readers) {
		t.Fatalf("delivered to %d users, want %d", len(recorder.sent), len(readers))
	}
}

// A Project lifecycle frame reaches management holders who cannot read the
// Project, so it carries the Project's identity and settings only: never its
// files, anyone's access, or the audience itself (ADR 0017).
func TestProjectLifecycleFrameCarriesNoFilesOrAccess(t *testing.T) {
	t.Parallel()
	archived := time.Now().UTC()
	payload := company.ProjectLifecycle{
		ID: uuid.New(), CompanyID: uuid.New(), Key: "OPS", Title: "Ops", Description: "d",
		Visibility: auth.ProjectVisibilityRestricted, Revision: 3,
		CreatedAt: archived, UpdatedAt: archived, ArchivedAt: &archived,
		ProjectFrame: auth.ProjectFrame{Audience: &auth.ProjectAudience{UserIDs: []uuid.UUID{uuid.New()}}},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	want := []string{
		"archivedAt", "companyId", "createdAt", "description", "id", "key", "revision", "title",
		"updatedAt", "visibility",
	}
	if !slices.Equal(keys, want) {
		t.Fatalf("project frame keys = %v, want %v", keys, want)
	}
}
