package main

import (
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/canvas"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/realtime"
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

// A content hint reaches the people with the Board open at once. Everyone else
// in the audience only keeps a library fresh, so they get one deferred hint
// carrying the latest sequence. An access change reaches everybody at once.
func TestWhiteboardHintsReachViewersNowAndTheLibraryLater(t *testing.T) {
	t.Parallel()
	bus, err := events.NewBus(events.BusOptions{Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	recorder := &recordingBroadcaster{}
	presence := realtime.NewCanvasPresence(nil)
	if err = registerRealtimeRouting(bus, recorder, whiteboardHintRouting{viewers: presence, libraryDelay: 20 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	company, board, viewer, reader := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	if _, err = presence.Update(t.Context(), company.String()+":whiteboard:"+board.String(),
		realtime.CanvasSession{SessionID: "tab", UserID: viewer.String()}, false); err != nil {
		t.Fatal(err)
	}
	to := func(user uuid.UUID) string { return "company_user/" + company.String() + "/" + user.String() }
	for seq := int64(1); seq <= 3; seq++ {
		publish(t, bus, company, canvas.TopicBoard, canvas.EventBoardChanged,
			canvas.BoardChanged{BoardID: board, Sequence: seq, RecipientUserIDs: []uuid.UUID{viewer, reader}})
	}
	sent, _ := recorder.recorded()
	if !slices.Equal(sent, []string{to(viewer), to(viewer), to(viewer)}) {
		t.Fatalf("immediate hints = %v; want the viewer only", sent)
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(sent) < 4 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
		sent, _ = recorder.recorded()
	}
	sent, frames := recorder.recorded()
	if len(sent) != 4 || sent[3] != to(reader) {
		t.Fatalf("library hints = %v; want one for the reader", sent)
	}
	raw, err := json.Marshal(frames[3].Payload)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"sequence":3`) {
		t.Fatalf("library hint = %s; want the latest sequence", raw)
	}
	publish(t, bus, company, canvas.TopicBoard, canvas.EventBoardChanged,
		canvas.BoardChanged{BoardID: board, Sequence: 4, Access: true, RecipientUserIDs: []uuid.UUID{viewer, reader}})
	if sent, _ = recorder.recorded(); len(sent) != 6 || !slices.Contains(sent[4:], to(reader)) {
		t.Fatalf("access hints = %v; want everyone at once", sent)
	}
}
