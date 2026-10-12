package daemon

import (
	"context"
	"sort"
	"sync"
	"time"
)

// Provider re-detection: a running daemon adopts a provider CLI that was
// installed — or fixed — after it started, without a restart. Discovery
// otherwise happens once, in LoadConfig, and the version refresh (§9.8,
// register.go) only compares providers a company has already registered; a
// CLI that appeared later, or one present at start whose probe never
// succeeded, would wait for the next restart. The loop here re-runs
// discovery every providerAvailabilityProbeInterval, adopts what it finds,
// and registers it for every tracked company through the same payload path
// the version refresh uses.
//
// The same loop notices the other direction (DR-FR9a). A provider that
// disappears is never deregistered and stays in the availability set until
// restart, so a re-install is picked up without one; but the round that finds
// it gone re-registers every company that recorded it with a payload that
// omits it. Because the register response replaces the company's runtime ids,
// that is what stops the runtime being heartbeated, and the server's stale
// sweep takes it offline ~180 s later.

// providerAvailabilityProbeInterval is how often the daemon looks for a
// provider CLI it has not registered yet — the "within two minutes" of
// DR-AC20. One discovery pass per tick; a login-shell fork only when a bare
// command name misses PATH. Overridable for tests.
var providerAvailabilityProbeInterval = 2 * time.Minute

// providerAvailabilityProbeMaxBackoff caps the doubling wait between
// attempts at a provider that is found but keeps failing to register
// online. Overridable for tests.
var providerAvailabilityProbeMaxBackoff = 30 * time.Minute

// providerBackoff paces adoption attempts per provider: none for a provider
// never tried, then a doubling wait from base to max after each attempt that
// did not end online, reset by a success.
type providerBackoff struct {
	mu       sync.Mutex
	base     time.Duration
	max      time.Duration
	failures map[string]int
	next     map[string]time.Time
}

func newProviderBackoff(base, maxDelay time.Duration) *providerBackoff {
	return &providerBackoff{
		base:     base,
		max:      maxDelay,
		failures: make(map[string]int),
		next:     make(map[string]time.Time),
	}
}

// due reports whether provider may be attempted at now. A provider without a
// pending wait — never tried, or tried and succeeded — is always due.
func (b *providerBackoff) due(provider string, now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	next, waiting := b.next[provider]
	return !waiting || !now.Before(next)
}

// fail records an attempt that did not end online and returns the wait
// before the next one: base doubled per consecutive failure, capped at max.
func (b *providerBackoff) fail(provider string, now time.Time) time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	delay := b.base << b.failures[provider]
	if delay > b.max || delay <= 0 {
		delay = b.max
	}
	b.failures[provider]++
	b.next[provider] = now.Add(delay)
	return delay
}

// succeed clears the provider's ladder: it registered online.
func (b *providerBackoff) succeed(provider string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.failures, provider)
	delete(b.next, provider)
}

// providerAvailabilityLoop runs the re-probe on its own goroutine until ctx
// ends. The first pass waits one interval: the daemon has just probed
// everything while registering.
func (d *Daemon) providerAvailabilityLoop(ctx context.Context) {
	if err := sleepWithContext(ctx, providerAvailabilityProbeInterval); err != nil {
		return
	}
	d.refreshProviderAvailability(ctx)

	// The ticker starts only now, after the first pass, so an interval
	// shorter than the pass cannot buffer a tick and fire twice in a row.
	ticker := time.NewTicker(providerAvailabilityProbeInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.refreshProviderAvailability(ctx)
		}
	}
}

// refreshProviderAvailability is one pass: discover the CLIs on disk, adopt
// every one that some tracked company has no registration record for and
// whose backoff has elapsed, probe the machine, and register the fresh
// payload for the companies that were missing a provider it carries or that
// still record one the machine no longer has.
//
// The two triggers are the two directions a machine changes. An install (or a
// repair) shows up as a provider without a record; an uninstall shows up as a
// record without a provider, and nothing else notices it — the version
// refresh compares only providers the payload carries, and a CLI that is gone
// answers no probe. Without the second trigger the uninstalled provider's
// runtime would keep being heartbeated online and keep being claimed, and
// every run dispatched to it would fail at launch.
//
// Adoption and registration hold the company-sync mutex, so this pass never
// interleaves with a company sync or the version refresh. Discovery runs
// before taking it: a login-shell fork must not park a company sync.
func (d *Daemon) refreshProviderAvailability(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	found := reprobeAgentCLIs()

	d.reloading.Lock()
	defer d.reloading.Unlock()

	now := time.Now()
	var candidates []string
	for _, provider := range d.providersWithoutRecord(found) {
		if d.providerAdoption.due(provider, now) {
			candidates = append(candidates, provider)
		}
	}
	// A machine with no CLI left on disk cannot be registered at all — the
	// payload would be empty — so an uninstall is only worth a round while
	// something remains to register. Those runtimes wait for the next
	// restart, which is also when the daemon would discover it has nothing
	// to run.
	var vanished []string
	if len(found) > 0 {
		vanished = d.vanishedProviders(found)
	}
	if len(candidates) == 0 && len(vanished) == 0 {
		return
	}
	if len(vanished) > 0 {
		d.logger.Info("registered provider CLI is no longer installed; re-registering without it",
			"providers", vanished)
	}

	for _, provider := range candidates {
		entry := found[provider]
		if known, ok := d.agents()[provider]; ok && known.Path == entry.Path {
			continue
		}
		d.logger.Info("provider CLI detected after start; adopting", "provider", provider, "path", entry.Path)
		d.adoptProvider(provider, entry)
	}

	payload := d.detectRuntimes(ctx)
	online := make(map[string]bool, len(payload))
	for _, rt := range payload {
		online[rt.Provider] = rt.Status == "online"
	}
	skipped := d.skippedAgentsSnapshot()
	for _, provider := range candidates {
		if online[provider] {
			d.providerAdoption.succeed(provider)
			continue
		}
		delay := d.providerAdoption.fail(provider, now)
		d.logger.Warn("provider CLI found but does not register online yet; backing off",
			"provider", provider, "reason", skipped[provider], "retry_in", delay)
	}
	if len(payload) == 0 {
		return
	}

	targets, held := d.companiesToRegister(payload, found)
	for companyID, absent := range held {
		d.logger.Warn("not re-registering company while an installed provider did not answer its probe",
			"company_id", companyID, "providers", absent)
	}
	changed := false
	for _, companyID := range targets {
		previous := d.companyRuntimeIDSet(companyID)
		resp, err := d.registerRuntimesForCompany(ctx, companyID, payload)
		if err != nil {
			d.logger.Warn("register after provider detection failed; will retry",
				"company_id", companyID, "error", err)
			continue
		}
		runtimeIDs := d.indexRegisterResponse(companyID, resp, payload)
		changed = true
		for _, rid := range runtimeIDs {
			if _, had := previous[rid]; had {
				continue
			}
			d.logger.Info("registered runtime for a provider detected after start",
				"company_id", companyID, "runtime_id", rid)
			// The upsert can revive a row an earlier daemon process left
			// behind; anything still in flight on it is an orphan.
			if err := d.client.RecoverOrphans(ctx, rid); err != nil {
				d.logger.Warn("recover-orphans after provider detection failed",
					"runtime_id", rid, "error", err)
			}
		}
	}
	if changed {
		d.notifyRuntimeSetChanged()
	}
}

// providersWithoutRecord lists, in stable order, the providers in found that
// at least one tracked company has no registration record for: a CLI
// installed after start, or one present at start that never registered
// (its probe was skipped, or the registration failed). A provider recorded
// everywhere — online or unusable — belongs to the version refresh.
func (d *Daemon) providersWithoutRecord(found map[string]AgentEntry) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var providers []string
	for provider := range found {
		for _, cs := range d.companies {
			if _, has := cs.registeredVersions[provider]; !has {
				providers = append(providers, provider)
				break
			}
		}
	}
	sort.Strings(providers)
	return providers
}

// companiesToRegister lists, in stable order, the tracked companies this
// round must send a registration for, and the companies it must not.
//
// A company qualifies for two reasons: it lacks a record for a provider the
// payload carries — the newly usable CLI adoption exists for — or it records
// a provider that is gone from disk and therefore absent from the payload,
// which is the uninstall DR-FR9a drops. Both are the same write: the register
// response replaces the company's runtime ids, so registering without a
// provider is exactly what stops its runtime being heartbeated.
//
// That is also why a company whose recorded provider is absent from the
// payload while still found on disk is held back instead (returned in held,
// keyed by company, naming those providers): a probe that merely failed this
// round must never cost that company the runtime it runs today. Only a
// provider genuinely gone from disk may be dropped.
func (d *Daemon) companiesToRegister(
	payload []RegisterRuntime,
	found map[string]AgentEntry,
) ([]string, map[string][]string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	carried := make(map[string]struct{}, len(payload))
	for _, rt := range payload {
		carried[rt.Provider] = struct{}{}
	}
	var targets []string
	held := make(map[string][]string)
	for id, cs := range d.companies {
		missing := false
		for _, rt := range payload {
			if _, has := cs.registeredVersions[rt.Provider]; !has {
				missing = true
				break
			}
		}
		var absent, uninstalled []string
		for provider := range cs.registeredVersions {
			if _, ok := carried[provider]; ok {
				continue
			}
			if _, installed := found[provider]; installed {
				absent = append(absent, provider)
				continue
			}
			uninstalled = append(uninstalled, provider)
		}
		if !missing && len(uninstalled) == 0 {
			// Nothing to adopt and nothing to drop: a company whose roster
			// already matches the machine is left alone, held back or not.
			continue
		}
		if len(absent) > 0 {
			sort.Strings(absent)
			held[id] = absent
			continue
		}
		targets = append(targets, id)
	}
	sort.Strings(targets)
	return targets, held
}

// vanishedProviders lists, in stable order, the providers some tracked
// company's last accepted registration recorded that are no longer on disk.
// This is the uninstall trigger: providersWithoutRecord only names CLIs that
// ARE installed, so without this a round would never start for an uninstall
// alone. The provider is not deregistered and not removed from the
// availability set — the round it triggers simply registers the affected
// companies without it, and the record it leaves behind is what clears
// afterwards, so the trigger does not repeat.
func (d *Daemon) vanishedProviders(found map[string]AgentEntry) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	seen := make(map[string]struct{})
	var gone []string
	for _, cs := range d.companies {
		for provider := range cs.registeredVersions {
			if _, installed := found[provider]; installed {
				continue
			}
			if _, duplicate := seen[provider]; duplicate {
				continue
			}
			seen[provider] = struct{}{}
			gone = append(gone, provider)
		}
	}
	sort.Strings(gone)
	return gone
}

// companyRuntimeIDSet snapshots the runtime ids a company holds, so a
// registration's response can be told apart into ids kept and ids minted.
func (d *Daemon) companyRuntimeIDSet(companyID string) map[string]struct{} {
	d.mu.Lock()
	defer d.mu.Unlock()
	ids := make(map[string]struct{})
	if cs, ok := d.companies[companyID]; ok {
		for _, rid := range cs.runtimeIDs {
			ids[rid] = struct{}{}
		}
	}
	return ids
}
