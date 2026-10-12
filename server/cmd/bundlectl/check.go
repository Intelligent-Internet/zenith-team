package main

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"boardly.dev/boardly/server/internal/contentbundle"
)

// runCheck validates unpacked plugin folders with exactly the rules the
// server and boardly-mcp apply to a pin, merged with the bundles this build
// pins for every other plugin, and prints every problem. A folder whose
// plugin this build already pins is also held to the release rules against
// that pin: no lower version, no removed key without a retirement, and no
// retirement undone. A plugins pull request runs it with --format github,
// which annotates each file and line.
func runCheck(ctx context.Context, args []string, stdout, _ io.Writer) error {
	flags := newFlagSet("check")
	format := flags.String("format", "text", "text or github")
	if err := parseFlags(flags, args, stdout, true); err != nil {
		return err
	}
	switch {
	case *format != "text" && *format != "github":
		return usageErrorf("--format must be text or github, got %q", *format)
	case flags.NArg() == 0:
		return usageErrorf("check needs at least one plugin folder")
	}
	sources := make([]pluginSource, 0, flags.NArg())
	names := make([]string, 0, flags.NArg())
	for _, dir := range flags.Args() {
		// Flag parsing stops at the first folder, so a flag after it would
		// otherwise be taken for a folder.
		if strings.HasPrefix(dir, "-") {
			return usageErrorf("%s after a plugin folder; flags go before the folders", dir)
		}
		source := readPluginDir(ctx, dir, filepath.ToSlash(filepath.Clean(dir)))
		// A check pins no tag; the lock it proves the loader accepts carries
		// the manifest's version and placeholder object ids.
		source.identity.Version = source.manifest.Version
		source.identity.Tag = source.name + "--v" + source.manifest.Version
		source.identity.Commit = strings.Repeat("0", 40)
		source.identity.ContentTreeID = strings.Repeat("0", 40)
		sources = append(sources, source)
		names = append(names, source.name)
	}
	others, own, problems := pinnedBundles(names)
	components, buildProblems := buildPlugins(sources, others)
	problems = append(problems, buildProblems...)
	for _, source := range sources {
		if pinned := own[source.name]; pinned != nil {
			next := contentbundle.Bundle{
				Lock: contentbundle.Lock{
					Plugin:      source.name,
					Component:   source.component(),
					Version:     source.manifest.Version,
					RetiredKeys: source.retiredKeys,
				},
				Files: contentbundle.NewFS(source.files),
			}
			change, err := comparePins(pinned, &next)
			if err != nil {
				return err
			}
			rules := replacementProblems(pinned, &next, change, source.display, false)
			problems = append(problems, rules...)
		}
	}
	if len(problems) > 0 {
		printProblems(stdout, *format, problems)
		return &problemsError{problems: problems}
	}
	for index, component := range components {
		lock := component.bundle.Lock
		if lock.Component == contentbundle.ComponentTypeManagedTools {
			fmt.Fprintf(
				stdout,
				"ok %s: %s %s, managedTools in %d files\n",
				sources[index].display,
				lock.Plugin,
				lock.Version,
				lock.FileCount,
			)
			continue
		}
		fmt.Fprintf(stdout, "ok %s: %s %s, %d Workflows in %d files\n", sources[index].display, lock.Plugin,
			lock.Version, lock.FileCount/2, lock.FileCount)
	}
	return nil
}
