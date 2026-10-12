package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"boardly.dev/boardly/server/internal/daemonws"
	"boardly.dev/boardly/server/internal/events"
)

// Every fact a domain publishes has to reach someone. The browser plane is
// this package's routing ledger and the daemon plane is daemonws; a name in
// neither is a fact that commits, is audited, and then does nothing — which
// is exactly the silent gap the bus was built to remove, and exactly what
// nobody notices until a client stops updating.
//
// The audit sink is deliberately excluded: it takes the whole stream through
// SubscribeAll, so counting it would make this assertion vacuously true.
//
// If an event genuinely should reach nobody, the honest fix is to say so here
// rather than to let it fall through: add it to internalOnly with the reason.
func TestEveryPublishedEventReachesAConsumer(t *testing.T) {
	t.Parallel()

	// Facts that deliberately reach neither plane. Empty today.
	internalOnly := map[string]string{}

	declared := declaredEventNames(t)
	if len(declared) < 40 {
		t.Fatalf("found only %d event names; the parser stopped seeing them", len(declared))
	}

	bus, err := events.NewBus(events.BusOptions{Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatalf("NewBus() error = %v", err)
	}
	if err := registerRealtimeRouting(bus, &recordingBroadcaster{}, whiteboardHintRouting{}); err != nil {
		t.Fatalf("registerRealtimeRouting() error = %v", err)
	}
	// The daemon handlers tolerate nil collaborators; this test is about which
	// keys get registered, not what the handlers then do.
	if err := daemonws.RegisterListeners(bus, nil, nil); err != nil {
		t.Fatalf("daemonws.RegisterListeners() error = %v", err)
	}
	subscribed := bus.Subscriptions()

	var unreachable []string
	for _, name := range declared {
		entityType, _, _ := strings.Cut(name, ".")
		if slices.Contains(subscribed, name) || slices.Contains(subscribed, entityType) {
			continue
		}
		if _, deliberate := internalOnly[name]; deliberate {
			continue
		}
		unreachable = append(unreachable, name)
	}
	if len(unreachable) > 0 {
		t.Fatalf("published but consumed by no plane: %v\n"+
			"route it in realtime_routing.go, consume it in daemonws, "+
			"or record why it reaches nobody in internalOnly", unreachable)
	}
}

// declaredEventNames reads the wire names out of every domain's events.go,
// which is where the rule says they live — so a domain that declares one in
// the wrong place is invisible here, and the count check above is what
// notices.
func declaredEventNames(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "..", "internal", "*", "events.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("glob internal/*/events.go = %v, %v", files, err)
	}

	var names []string
	for _, file := range files {
		parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		for _, decl := range parsed.Decls {
			general, ok := decl.(*ast.GenDecl)
			if !ok || general.Tok != token.CONST {
				continue
			}
			for _, spec := range general.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, expr := range value.Values {
					literal, ok := expr.(*ast.BasicLit)
					if !ok || literal.Kind != token.STRING {
						continue
					}
					name, err := strconv.Unquote(literal.Value)
					if err != nil || !strings.Contains(name, ".") {
						continue
					}
					names = append(names, name)
				}
			}
		}
	}
	slices.Sort(names)
	return slices.Compact(names)
}
