package main

import (
	"encoding/json"
	"log/slog"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/canvas"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/realtime"
)

func TestWhiteboardCommitHintRoutesOnlyToReaders(t *testing.T) {
	t.Parallel()
	bus, recorder := routedBus(t)
	company, board, reader := uuid.New(), uuid.New(), uuid.New()
	publish(
		t,
		bus,
		company,
		canvas.TopicBoard,
		canvas.EventBoardChanged,
		canvas.BoardChanged{BoardID: board, Sequence: 27, RecipientUserIDs: []uuid.UUID{reader}},
	)
	if len(recorder.sent) != 1 || recorder.sent[0] != "company_user/"+company.String()+"/"+reader.String() {
		t.Fatalf("audience=%v", recorder.sent)
	}
	raw, err := json.Marshal(recorder.frames[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	var hint map[string]any
	if err = json.Unmarshal(raw, &hint); err != nil {
		t.Fatal(err)
	}
	if len(hint) != 2 || hint["boardId"] != board.String() || hint["sequence"] != float64(27) {
		t.Fatalf("hint contains unexpected content: %s", raw)
	}
}

// A content hint reaches only the people with the Board open: nobody else
// reads its scene.
func TestWhiteboardHintsReachOnlyViewers(t *testing.T) {
	t.Parallel()
	bus, err := events.NewBus(events.BusOptions{Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	recorder := &recordingBroadcaster{}
	presence := realtime.NewCanvasPresence(nil)
	if err = registerRealtimeRouting(bus, recorder, whiteboardHintRouting{viewers: presence}); err != nil {
		t.Fatal(err)
	}
	company, board, viewer, reader := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	if _, err = presence.Update(t.Context(), company.String()+":whiteboard:"+board.String(),
		realtime.CanvasSession{SessionID: "tab", UserID: viewer.String()}, false); err != nil {
		t.Fatal(err)
	}
	to := "company_user/" + company.String() + "/" + viewer.String()
	for seq := int64(1); seq <= 3; seq++ {
		publish(
			t,
			bus,
			company,
			canvas.TopicBoard,
			canvas.EventBoardChanged,
			canvas.BoardChanged{
				BoardID:          board,
				Sequence:         seq,
				RecipientUserIDs: []uuid.UUID{viewer, reader},
			},
		)
	}
	if sent, _ := recorder.recorded(); !slices.Equal(sent, []string{to, to, to}) {
		t.Fatalf("hints = %v; want the viewer only", sent)
	}
}
