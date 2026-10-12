package livekit

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
)

type agentDispatch struct {
	ID       string `json:"id"`
	Metadata string `json:"metadata"`
}

func (p *HTTPRoomProvider) listAgentDispatches(ctx context.Context, room string) ([]agentDispatch, error) {
	body, err := p.callService(ctx, "AgentDispatchService", "ListDispatch", room, map[string]any{"room": room})
	if err != nil {
		return nil, err
	}
	var response struct {
		AgentDispatches []agentDispatch `json:"agentDispatches"`
	}
	err = json.Unmarshal(body, &response)
	if err == nil && response.AgentDispatches == nil {
		var fields map[string]json.RawMessage
		if err = json.Unmarshal(body, &fields); err != nil {
			return nil, err
		}
		if raw, ok := fields["agent_dispatches"]; ok {
			err = json.Unmarshal(raw, &response.AgentDispatches)
		}
	}
	return response.AgentDispatches, err
}

// DispatchHuddleAI reuses an observed dispatch after an ambiguous create timeout.
// A duplicate job still has to win the database claim before opening a model.
func (p *HTTPRoomProvider) DispatchHuddleAI(ctx context.Context, room string, id uuid.UUID) error {
	rows, err := p.listAgentDispatches(ctx, room)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.Metadata == id.String() {
			return nil
		}
	}
	_, err = p.callService(ctx, "AgentDispatchService", "CreateDispatch", room, map[string]any{"room": room, "agentName": "zenith-helia", "metadata": id.String()})
	return err
}
func (p *HTTPRoomProvider) RemoveHuddleAI(ctx context.Context, room string, id uuid.UUID) error {
	ready, err := p.RoomReady(ctx, room)
	if err != nil {
		return err
	}
	if !ready {
		return nil
	}
	rows, err := p.listAgentDispatches(ctx, room)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.Metadata == id.String() {
			if _, err = p.callService(ctx, "AgentDispatchService", "DeleteDispatch", room, map[string]any{"room": room, "dispatchId": row.ID}); err != nil {
				return err
			}
		}
	}
	// Dispatch shutdown can outlive its control record. Remove the exact media
	// participant too, without disconnecting humans or a newer Agent attempt.
	body, err := p.callService(ctx, "RoomService", "ListParticipants", room, map[string]any{"room": room})
	if err != nil {
		return err
	}
	var response struct {
		Participants []struct {
			Identity string `json:"identity"`
		} `json:"participants"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return err
	}
	for _, participant := range response.Participants {
		if participant.Identity == "helia:"+id.String() {
			_, err := p.callService(ctx, "RoomService", "RemoveParticipant", room, map[string]any{
				"room": room, "identity": participant.Identity,
			})
			return err
		}
	}
	return nil
}
