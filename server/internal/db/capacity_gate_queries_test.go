package db

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The per-agent task-lane capacity rule is written down twice: once in
// CountActiveTaskLaneRuns (runs.sql), which the claim gate enforces under the
// agent-row lock, and once in CountActiveTaskLaneRunsByAgentForBinder (agents.sql),
// which is the number the Agent projection renders. A comment in each says
// they must stay identical. A comment cannot fail, so these do.
//
// Source-shape assertions, in the unit lane, because the failure they catch is
// an edit to one file that forgets the other — and that edit is visible in the
// text long before any database is involved.

// gateStatuses is the live-status vocabulary the capacity gate counts: a run in
// one of these has been handed to a daemon and has not finished.
var gateStatuses = []string{"dispatched", "running", "waiting_local_directory"}

// exemptStatuses is the rest of runs_status_check, pinned so that adding a
// status to the schema fails here instead of quietly landing on whichever side
// of the gate the new value happens to fall.
var exemptStatuses = []string{
	// Not dispatched yet: a queued or deferred run is waiting FOR a slot, not
	// holding one. Counting it would make an agent block itself.
	"queued", "deferred",
	// Terminal: the slot went back when the run finished, however it finished.
	"completed", "failed", "cancelled",
}

// TestCapacityGateCountPredicatesMatch holds the two counts to the same rule.
//
// The honest limit of this test: equality catches the two drifting apart, but
// not both being stale together — if the rule itself becomes wrong, two
// matching copies of it still pass. That is what the status-vocabulary check
// below is for.
func TestCapacityGateCountPredicatesMatch(t *testing.T) {
	t.Parallel()

	gate := capacityPredicate(t, queryFileSource(t, "runs.sql"), "CountActiveTaskLaneRuns")
	display := capacityPredicate(t, queryFileSource(t, "agents.sql"), "CountActiveTaskLaneRunsByAgentForBinder")
	if gate != display {
		t.Errorf("the claim gate's count and the projection's count disagree:\n"+
			"  CountActiveTaskLaneRuns       (runs.sql)   %s\n"+
			"  CountActiveTaskLaneRunsByAgentForBinder (agents.sql) %s",
			gate, display)
	}
	if !strings.Contains(gate, "conversations.kind = 'task'") {
		t.Errorf("the capacity count reads %q; it must read the lane from the "+
			"run's conversation, not from task_id — task_id is ON DELETE SET "+
			"NULL, so an executing run can lose it and leave the lane whose "+
			"slot it still holds, while conversations.task_id is also SET NULL "+
			"and the conversation keeps kind = 'task'", gate)
	}
	if strings.Contains(gate, "runs.task_id") {
		t.Errorf("the capacity count still reads runs.task_id: %s", gate)
	}
}

func TestCapacityGateExemptsNonTaskLanes(t *testing.T) {
	t.Parallel()

	claim := strings.ToLower(strings.Join(strings.Fields(
		statementsOnly(namedQuerySource(t, queryFileSource(t, "runs.sql"), "SelectClaimableRun")),
	), " "))
	if !strings.Contains(claim, "conversations.kind in ('direct', 'schedule', 'channel_agent')") {
		t.Fatalf("ClaimRun does not exempt all non-Task lanes: %s", claim)
	}
	if strings.Contains(claim, "conversations.kind = 'direct'") {
		t.Fatalf("ClaimRun still exempts only the direct lane: %s", claim)
	}
}

// TestCapacityGateCountsEveryLiveStatus pins both halves of the status
// vocabulary. runs_status_check is the whole set a run's status may take, and
// every one of those values is either counted by the gate or deliberately
// exempt. Adding a ninth status to the schema therefore fails here, which is
// the point: whoever adds it has to say which side of the capacity gate it
// falls on rather than discovering the answer in production.
func TestCapacityGateCountsEveryLiveStatus(t *testing.T) {
	t.Parallel()

	schema := sortedCopy(runStatusVocabulary(t))
	classified := sortedCopy(append(append([]string{}, gateStatuses...), exemptStatuses...))
	if strings.Join(schema, ",") != strings.Join(classified, ",") {
		t.Fatalf("runs_status_check and this test disagree about the status vocabulary:\n"+
			"  schema     %v\n"+
			"  classified %v\n"+
			"decide whether the new status holds a capacity slot, then add it to "+
			"gateStatuses or exemptStatuses", schema, classified)
	}

	for _, source := range []struct{ file, query string }{
		{"runs.sql", "CountActiveTaskLaneRuns"},
		{"agents.sql", "CountActiveTaskLaneRunsByAgentForBinder"},
	} {
		predicate := capacityPredicate(t, queryFileSource(t, source.file), source.query)
		for _, status := range gateStatuses {
			if !strings.Contains(predicate, "'"+status+"'") {
				t.Errorf("%s does not count %q", source.query, status)
			}
		}
		for _, status := range exemptStatuses {
			if strings.Contains(predicate, "'"+status+"'") {
				t.Errorf("%s counts %q, which holds no capacity slot", source.query, status)
			}
		}
	}
}

// capacityPredicate reduces one of the two counts to the rule they share: the
// WHERE terms from `status IN (…)` onward, with comments dropped and runs of
// whitespace flattened, so the two can be compared as text despite one being a
// scalar count keyed on agent_id and the other a company-wide GROUP BY. The
// `runs.` qualifier is stripped for the same reason — the grouped query needs
// it for its join-free but qualified style, and it is not part of the rule.
func capacityPredicate(t *testing.T, source, name string) string {
	t.Helper()

	query := strings.ToLower(statementsOnly(namedQuerySource(t, source, name)))
	query = strings.ReplaceAll(query, "runs.", "")
	query = strings.Join(strings.Fields(query), " ")

	start := strings.Index(query, "status in (")
	if start < 0 {
		t.Fatalf("%s has no `status IN (…)` term: %s", name, query)
	}
	predicate := query[start:]
	for _, terminator := range []string{" group by ", ";"} {
		if end := strings.Index(predicate, terminator); end >= 0 {
			predicate = predicate[:end]
		}
	}
	return strings.TrimSpace(predicate)
}

// runStatusVocabulary reads the status values runs_status_check allows straight
// out of the migration, so the pin below is against the schema itself rather
// than a second list of the same words.
func runStatusVocabulary(t *testing.T) []string {
	t.Helper()

	source := statementsOnly(upMigrationsSource(t))
	marker := "CONSTRAINT runs_status_check CHECK (status IN ("
	start := strings.Index(source, marker)
	if start < 0 {
		t.Fatal("runs_status_check not found in the migrations")
	}
	rest := source[start+len(marker):]
	end := strings.Index(rest, "))")
	if end < 0 {
		t.Fatal("runs_status_check is not closed")
	}

	var statuses []string
	for _, match := range regexp.MustCompile(`'([a-z_]+)'`).FindAllStringSubmatch(rest[:end], -1) {
		statuses = append(statuses, match[1])
	}
	if len(statuses) == 0 {
		t.Fatal("runs_status_check names no statuses")
	}
	return statuses
}

func sortedCopy(values []string) []string {
	out := append([]string{}, values...)
	sort.Strings(out)
	return out
}

func queryFileSource(t *testing.T, name string) string {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join("..", "..", "pkg", "db", "queries", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(raw)
}
