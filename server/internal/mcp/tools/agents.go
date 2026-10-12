package tools

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"boardly.dev/boardly/server/internal/mcp/companyplan"
)

// ListAgentsInput has no fields: the Company is the authenticated user's
// only Company and the roster is what the CEO plans against.
type ListAgentsInput struct{}

// PlanAgent is one Agent a Company Plan may name. Instructions are the
// Agent's own charter, clipped so a roster read stays a roster read.
type PlanAgent struct {
	AgentID          string `json:"agentId"`
	Name             string `json:"name"`
	Title            string `json:"title"`
	Instructions     string `json:"instructions"`
	IsRoot           bool   `json:"isRoot"`
	RuntimeAvailable bool   `json:"runtimeAvailable"`
}

type ListAgentsResult struct {
	CompanyID   string      `json:"companyId"`
	CompanySlug string      `json:"companySlug"`
	TotalCount  int         `json:"totalCount"`
	Agents      []PlanAgent `json:"agents"`
}

type rosterCompany struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
}

type rosterCompanyCollection struct {
	Items []rosterCompany `json:"items"`
}

// rosterAgent is the slice of the Agent list response a plan needs: the
// identity, the charter, the place in the reporting tree and whether a
// runtime can execute it.
type rosterAgent struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	Title            string    `json:"title"`
	Instructions     string    `json:"instructions"`
	ReportsTo        *struct{} `json:"reportsTo"`
	RuntimeAvailable bool      `json:"runtimeAvailable"`
	ArchivedAt       *string   `json:"archivedAt"`
}

type rosterPage struct {
	Items []rosterAgent `json:"items"`
}

const maxPlanAgentInstructionRunes = 600

// ListAgents returns the active Agents of the user's only Company: who the
// CEO can route work to, with the charter each one carries.
func (s *DefaultCompanyPlanService) ListAgents(
	ctx context.Context,
	_ ListAgentsInput,
) (ListAgentsResult, error) {
	if s == nil || s.api == nil {
		return ListAgentsResult{}, errors.New("Zenith Team API client is unavailable")
	}
	company, err := s.onlyRosterCompany(ctx)
	if err != nil {
		return ListAgentsResult{}, err
	}
	var page rosterPage
	path := fmt.Sprintf("/companies/%s/agents", url.PathEscape(company.ID))
	if err := s.api.FetchRunJSON(ctx, path, &page); err != nil {
		return ListAgentsResult{}, fmt.Errorf("list Agents: %w", err)
	}
	result := ListAgentsResult{CompanyID: company.ID, CompanySlug: company.Slug, Agents: []PlanAgent{}}
	for _, item := range page.Items {
		if item.ArchivedAt != nil || strings.TrimSpace(item.ID) == "" {
			continue
		}
		result.Agents = append(result.Agents, PlanAgent{
			AgentID:          item.ID,
			Name:             item.Name,
			Title:            item.Title,
			Instructions:     clipRunes(item.Instructions, maxPlanAgentInstructionRunes),
			IsRoot:           item.ReportsTo == nil,
			RuntimeAvailable: item.RuntimeAvailable,
		})
	}
	sort.Slice(result.Agents, func(i, j int) bool {
		if result.Agents[i].IsRoot != result.Agents[j].IsRoot {
			return result.Agents[i].IsRoot
		}
		return strings.ToLower(result.Agents[i].Name) < strings.ToLower(result.Agents[j].Name)
	})
	result.TotalCount = len(result.Agents)
	return result, nil
}

func clipRunes(value string, limit int) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit]) + "…"
}

func (s *DefaultCompanyPlanService) onlyRosterCompany(ctx context.Context) (rosterCompany, error) {
	var collection rosterCompanyCollection
	if err := s.api.FetchRunJSON(ctx, "/companies", &collection); err != nil {
		return rosterCompany{}, fmt.Errorf("list Companies: %w", err)
	}
	switch len(collection.Items) {
	case 0:
		return rosterCompany{}, fmt.Errorf(
			"%w: the account has no Companies",
			companyplan.ErrCompanyNotFound,
		)
	case 1:
		return collection.Items[0], nil
	default:
		return rosterCompany{}, companyplan.ErrMultipleCompanies
	}
}
