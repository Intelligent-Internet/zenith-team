package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"unicode"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// sessionOnlyLogo reports whether a logo loads only inside a signed-in
// ChatGPT browser session. Every image chatgpt.com serves, the first-party
// app paths /images/ecosystem/apps/<slug>/icon.png and the signed
// /backend-api/estuary/content URLs alike, answers 403 to anyone else, while
// files.openai.com serves logos to everyone (measured 2026-09-25). Codex
// reports only such a URL for its first-party apps (Gmail, Google Calendar,
// GitHub, Outlook and others). This is the server's one test of it.
func sessionOnlyLogo(logo string) bool {
	parsed, err := url.Parse(logo)
	return err == nil && strings.EqualFold(parsed.Hostname(), "chatgpt.com")
}

// loadableLogo reports whether a client can show the logo.
func loadableLogo(logo *string) bool {
	return logo != nil && *logo != "" && !sessionOnlyLogo(*logo)
}

// preferLoadableLogo returns the first candidate a client can load, else the
// first one present: with nothing loadable to stand in, an app keeps its own
// session-only logo and the client's failover draws a brand mark or monogram.
func preferLoadableLogo(candidates ...*string) *string {
	var fallback *string
	for _, candidate := range candidates {
		switch {
		case candidate == nil || *candidate == "":
		case !sessionOnlyLogo(*candidate):
			return candidate
		case fallback == nil:
			fallback = candidate
		}
	}
	return fallback
}

// appLogoFallbacks holds the loadable logos an app may show in place of a
// missing or session-only one of its own.
type appLogoFallbacks struct {
	bundles []pluginLogoBundle
	byName  map[string][]nameLender
}

// nameLender is one loadable logo a name borrows and the developer of the
// Codex Plugin that shows it; an app lends with no developer.
type nameLender struct {
	logo      string
	developer string
}

// pluginLogoBundle is an installed Codex Plugin's loadable logo and the
// native ids of the Codex apps it bundles.
type pluginLogoBundle struct {
	name string
	logo string
	apps []string
}

// readAppLogoFallbacks reads the stand-ins for apps without a loadable logo:
// with bundles set, the Plugins installed on the apps' Runtime, which every
// caller has already resolved as one of the viewer's own; and the logos
// borrowed by the given names.
func readAppLogoFallbacks(
	ctx context.Context,
	q *generated.Queries,
	viewer auth.UserPrincipal,
	runtimeID uuid.UUID,
	bundles bool,
	names []string,
) (appLogoFallbacks, error) {
	var fallbacks appLogoFallbacks
	if bundles {
		rows, err := q.ListInstalledPluginLogosForRuntime(
			ctx,
			generated.ListInstalledPluginLogosForRuntimeParams{
				CompanyID: viewer.CompanyID, RuntimeID: runtimeID,
			},
		)
		if err != nil {
			return appLogoFallbacks{}, fmt.Errorf("read installed Plugin logos: %w", err)
		}
		for _, row := range rows {
			if sessionOnlyLogo(row.IconUrl) {
				continue
			}
			var identities []PluginConnectorIdentity
			// Trusted structured product state, validated before persistence.
			_ = json.Unmarshal(row.ConnectorIdentities, &identities)
			bundle := pluginLogoBundle{name: row.Name, logo: row.IconUrl}
			for _, identity := range identities {
				if identity.Source == ConnectorSourceCodexApp {
					bundle.apps = append(bundle.apps, identity.NativeID)
				}
			}
			if len(bundle.apps) > 0 {
				fallbacks.bundles = append(fallbacks.bundles, bundle)
			}
		}
	}
	byName, err := codexIconsByName(ctx, q, viewer, names)
	if err != nil {
		return appLogoFallbacks{}, err
	}
	fallbacks.byName = byName
	return fallbacks, nil
}

// bundled returns the logo of an installed Plugin that bundles the Codex app,
// preferring one named like the app, then the one bundling the fewest apps:
// its logo is the likeliest to be the app's own.
func (f appLogoFallbacks) bundled(nativeID, name string) *string {
	var best *pluginLogoBundle
	for index := range f.bundles {
		bundle := &f.bundles[index]
		if !slices.Contains(bundle.apps, nativeID) {
			continue
		}
		if best == nil {
			best = bundle
			continue
		}
		named, bestNamed := strings.EqualFold(bundle.name, name), strings.EqualFold(best.name, name)
		if (named && !bestNamed) || (named == bestNamed && len(bundle.apps) < len(best.apps)) {
			best = bundle
		}
	}
	if best == nil {
		return nil
	}
	logo := best.logo
	return &logo
}

// named returns the logo borrowed by a name.
func (f appLogoFallbacks) named(name string) *string {
	if lenders := f.byName[name]; len(lenders) > 0 {
		logo := lenders[0].logo
		return &logo
	}
	return nil
}

// namedFor returns the logo a Plugin borrows by its name from a lender whose
// developer does not contradict the Plugin's: a shared name can belong to two
// products, so "data" by Astronomer does not take the logo of "Data" by
// OpenAI, while "notion" by Notion Labs still takes Notion's.
func (f appLogoFallbacks) namedFor(name string, publisher pluginPublisher) *string {
	for _, lender := range f.byName[name] {
		if publisher.developerName != nil && lender.developer != "" &&
			!pluginNamesAgree(*publisher.developerName, lender.developer) {
			continue
		}
		logo := lender.logo
		return &logo
	}
	return nil
}

// appLogo picks one app's logo: its own when a client can load it, then for
// a Codex app the logo of an installed Plugin that bundles it, then one
// borrowed by its name, and otherwise its own.
func (f appLogoFallbacks) appLogo(source ConnectorSource, nativeID, name string, own *string) *string {
	var bundled *string
	if source == ConnectorSourceCodexApp {
		bundled = f.bundled(nativeID, name)
	}
	return preferLoadableLogo(own, bundled, f.named(name))
}

// codexIconsByName maps each name that a Codex Plugin matches ignoring case,
// spaces and punctuation, or a Codex app matches ignoring case, on one of the
// viewer's own Runtimes (ADR 0014), to its loadable logos, newest first and a
// Plugin's before an app's, each with its Plugin's developer. A name with
// only session-only logos is absent.
func codexIconsByName(
	ctx context.Context,
	q *generated.Queries,
	viewer auth.UserPrincipal,
	names []string,
) (map[string][]nameLender, error) {
	icons := make(map[string][]nameLender, len(names))
	if len(names) == 0 {
		return icons, nil
	}
	rows, err := q.ListCompanyCodexIconsByName(ctx, generated.ListCompanyCodexIconsByNameParams{
		ViewerUserID: viewer.UserID, CompanyID: viewer.CompanyID, Names: names,
	})
	if err != nil {
		return nil, fmt.Errorf("read Codex logos by name: %w", err)
	}
	for _, row := range rows {
		if !sessionOnlyLogo(row.IconUrl) {
			icons[row.Name] = append(icons[row.Name], nameLender{
				logo: row.IconUrl, developer: row.DeveloperName.String,
			})
		}
	}
	return icons, nil
}

// pluginPublisher is what a Plugin without a loadable logo borrows by when its
// name lends none: its website's host (the stored website_host) and its
// developer, as its Runtime observed them. A Claude Plugin reports neither.
type pluginPublisher struct {
	websiteHost   *string
	developerName *string
}

// publisherLogos maps a website host and a developer, each as its Plugin
// reported it, to the one Codex Plugin with a loadable logo that shares it.
type publisherLogos struct {
	byWebsite   map[string]publisherLender
	byDeveloper map[string]publisherLender
}

// publisherLender is the name and newest loadable logo of a Codex Plugin that
// lends by its publisher.
type publisherLender struct {
	name string
	logo string
}

// codexPluginLogosByPublisher finds, per website host and per developer, the
// Codex Plugin on one of the viewer's own Runtimes (ADR 0014) that may lend
// its logo, when exactly one Plugin with a loadable logo matches: a shared
// host or developer lends nothing, since its Plugins' logos differ.
func codexPluginLogosByPublisher(
	ctx context.Context,
	q *generated.Queries,
	viewer auth.UserPrincipal,
	hosts []string,
	developers []string,
) (publisherLogos, error) {
	if len(hosts) == 0 && len(developers) == 0 {
		return publisherLogos{}, nil
	}
	rows, err := q.ListCompanyCodexPluginLogosByPublisher(
		ctx,
		generated.ListCompanyCodexPluginLogosByPublisherParams{
			WebsiteHosts: hosts, DeveloperNames: developers,
			CompanyID: viewer.CompanyID, ViewerUserID: viewer.UserID,
		},
	)
	if err != nil {
		return publisherLogos{}, fmt.Errorf("read Codex logos by publisher: %w", err)
	}
	return lendPublisherLogos(rows), nil
}

// lendPublisherLogos keeps, per website host and per developer, the name and
// newest loadable logo of the only Plugin with one; a match that several
// Plugins share lends nothing.
func lendPublisherLogos(rows []generated.ListCompanyCodexPluginLogosByPublisherRow) publisherLogos {
	logos := publisherLogos{
		byWebsite:   map[string]publisherLender{},
		byDeveloper: map[string]publisherLender{},
	}
	type match struct {
		byWebsite bool
		requested string
	}
	// Each lending Plugin's newest loadable logo, per match.
	lenders := make(map[match]map[uuid.UUID]publisherLender)
	for _, row := range rows {
		if sessionOnlyLogo(row.IconUrl) {
			continue
		}
		key := match{byWebsite: row.ByWebsite, requested: row.Requested}
		if lenders[key] == nil {
			lenders[key] = make(map[uuid.UUID]publisherLender, 1)
		}
		if _, chosen := lenders[key][row.PluginID]; !chosen {
			lenders[key][row.PluginID] = publisherLender{name: row.Name, logo: row.IconUrl}
		}
	}
	for key, plugins := range lenders {
		if len(plugins) != 1 {
			continue
		}
		for _, lender := range plugins {
			if key.byWebsite {
				logos.byWebsite[key.requested] = lender
			} else {
				logos.byDeveloper[key.requested] = lender
			}
		}
	}
	return logos
}

// lend returns the logo lent to a Plugin's website host, else to its
// developer, when the lender's name agrees with the Plugin's: one publisher
// ships several products with their own logos, so "AlloyDB" on
// cloud.google.com does not take BigQuery's.
func (l publisherLogos) lend(name string, publisher pluginPublisher) *string {
	if publisher.websiteHost != nil {
		if lender, found := l.byWebsite[*publisher.websiteHost]; found && pluginNamesAgree(name, lender.name) {
			return &lender.logo
		}
	}
	if publisher.developerName != nil {
		if lender, found := l.byDeveloper[*publisher.developerName]; found &&
			pluginNamesAgree(name, lender.name) {
			return &lender.logo
		}
	}
	return nil
}

// pluginNamesAgree reports whether two Plugin names can name one product:
// one's letters and digits, ignoring case, contain the other's ("Sentry CLI"
// and "Sentry"), or the first word of one is a word of the other ("Learn with
// Coursera" and "Coursera Learning").
func pluginNamesAgree(left, right string) bool {
	leftWords, rightWords := pluginNameWords(left), pluginNameWords(right)
	if len(leftWords) == 0 || len(rightWords) == 0 {
		return false
	}
	leftKey, rightKey := strings.Join(leftWords, ""), strings.Join(rightWords, "")
	return strings.Contains(leftKey, rightKey) || strings.Contains(rightKey, leftKey) ||
		slices.Contains(rightWords, leftWords[0]) || slices.Contains(leftWords, rightWords[0])
}

// pluginNameWords splits a name into its lowercased runs of letters and digits.
func pluginNameWords(name string) []string {
	return strings.FieldsFunc(strings.ToLower(name), func(character rune) bool {
		return !unicode.IsLetter(character) && !unicode.IsDigit(character)
	})
}

// choosePluginLogos fills in logos a client can load. A Plugin without
// one (a Claude Plugin reports none) borrows a Codex logo by name, else by its
// website's host, else by its developer, from a Plugin whose name agrees;
// publishers holds each Plugin's, in the order of plugins. A claude_plugin app that reports none borrows by name.
// A Codex app whose own logo is missing or session-only shows the logo of a
// Plugin installed on the Runtime that bundles it, else one borrowed by name.
// With nothing loadable to borrow, each keeps its own.
func choosePluginLogos(
	ctx context.Context,
	q *generated.Queries,
	viewer auth.UserPrincipal,
	runtimeID uuid.UUID,
	plugins []PluginItemV1,
	publishers []pluginPublisher,
	apps []PluginDetailApp,
) error {
	var names []string
	bundles := false
	for _, plugin := range plugins {
		if !loadableLogo(plugin.IconURL) {
			names = append(names, plugin.Name)
		}
	}
	for _, app := range apps {
		if pluginAppNeedsLogo(app) {
			names = append(names, app.Name)
			bundles = bundles || app.Source == ConnectorSourceCodexApp
		}
	}
	if len(names) == 0 {
		return nil
	}
	fallbacks, err := readAppLogoFallbacks(ctx, q, viewer, runtimeID, bundles, names)
	if err != nil {
		return err
	}
	var hosts, developers []string
	for index, plugin := range plugins {
		if loadableLogo(plugin.IconURL) {
			continue
		}
		plugins[index].IconURL = preferLoadableLogo(
			plugin.IconURL, fallbacks.namedFor(plugin.Name, publishers[index]),
		)
		if loadableLogo(plugins[index].IconURL) {
			continue
		}
		if host := publishers[index].websiteHost; host != nil {
			hosts = append(hosts, *host)
		}
		if developer := publishers[index].developerName; developer != nil {
			developers = append(developers, *developer)
		}
	}
	byPublisher, err := codexPluginLogosByPublisher(ctx, q, viewer, hosts, developers)
	if err != nil {
		return err
	}
	for index, plugin := range plugins {
		if !loadableLogo(plugin.IconURL) {
			plugins[index].IconURL = preferLoadableLogo(
				plugin.IconURL, byPublisher.lend(plugin.Name, publishers[index]),
			)
		}
	}
	for index, app := range apps {
		if pluginAppNeedsLogo(app) {
			apps[index].IconURL = fallbacks.appLogo(app.Source, app.NativeID, app.Name, app.IconURL)
		}
	}
	return nil
}

func pluginAppNeedsLogo(app PluginDetailApp) bool {
	switch app.Source {
	case ConnectorSourceCodexApp:
		return !loadableLogo(app.IconURL)
	case ConnectorSourceClaudePlugin:
		return app.IconURL == nil
	default:
		return false
	}
}

// chooseConnectorLogos gives each Codex app on a Connector page whose own
// logo is missing or session-only a loadable one when there is one:
// the logo of a Plugin installed on the selected Runtime that bundles it,
// else one borrowed by name.
func chooseConnectorLogos(
	ctx context.Context,
	q *generated.Queries,
	viewer auth.UserPrincipal,
	runtimeID uuid.UUID,
	connectors []ConnectorItemV1,
) error {
	var names []string
	for _, connector := range connectors {
		if connectorNeedsLogo(connector) {
			names = append(names, connector.Name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	fallbacks, err := readAppLogoFallbacks(ctx, q, viewer, runtimeID, true, names)
	if err != nil {
		return err
	}
	for index, connector := range connectors {
		if connectorNeedsLogo(connector) {
			connectors[index].IconURL = fallbacks.appLogo(
				connector.Source, connector.NativeID, connector.Name, connector.IconURL,
			)
		}
	}
	return nil
}

func connectorNeedsLogo(connector ConnectorItemV1) bool {
	return connector.Source == ConnectorSourceCodexApp && !loadableLogo(connector.IconURL)
}
