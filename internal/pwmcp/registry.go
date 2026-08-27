package pwmcp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// browsersManifest is the subset of playwright-core's browsers.json that decides
// what a given core revision expects to find in the registry.
type browsersManifest struct {
	Browsers []struct {
		Name             string `json:"name"`
		Revision         string `json:"revision"`
		InstallByDefault bool   `json:"installByDefault"`
	} `json:"browsers"`
}

// revisionDir is the layout Playwright uses inside the registry: the browser
// name with dashes folded to underscores, then the revision.
var revisionDir = regexp.MustCompile(`^[a-z0-9]+(_[a-z0-9]+)*-[0-9]+$`)

// Required maps browser names to the registry directory the pinned core wants.
// An unknown name is an error rather than a silent skip, so a typo in a config
// surfaces before it becomes a mystery download.
func (c Config) Required(names []string) (map[string]string, error) {
	manifest, err := c.readManifest()
	if err != nil {
		return nil, err
	}
	known := make(map[string]string, len(manifest.Browsers))
	for _, browser := range manifest.Browsers {
		known[browser.Name] = directoryFor(browser.Name, browser.Revision)
	}
	required := make(map[string]string, len(names))
	for _, name := range names {
		directory, ok := known[name]
		if !ok {
			return nil, fmt.Errorf("playwright-core %s has no browser named %q", c.Version, name)
		}
		required[name] = directory
	}
	return required, nil
}

// Missing lists the requested browsers absent from the shared registry.
func (c Config) Missing(names []string) ([]string, error) {
	required, err := c.Required(names)
	if err != nil {
		return nil, err
	}
	var missing []string
	for name, directory := range required {
		// Playwright writes INSTALLATION_COMPLETE only once the download and
		// unpack both finished, so a half-written directory counts as missing
		// rather than tricking us into launching a broken browser.
		if !exists(filepath.Join(c.Browsers, directory, "INSTALLATION_COMPLETE")) {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return missing, nil
}

// Keep returns every registry directory some installed version still needs.
// It walks all pinned versions under Root, not just the current one, so rolling
// the pin back does not mean re-downloading what the older pin used.
func (c Config) Keep() (map[string]struct{}, error) {
	entries, err := os.ReadDir(c.Root)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]struct{}{}, nil
		}
		return nil, fmt.Errorf("read %s: %w", c.Root, err)
	}
	keep := make(map[string]struct{})
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		version := c
		version.Version = entry.Name()
		manifest, err := version.readManifest()
		if err != nil {
			// A half-installed version tree must not authorize deletion of
			// anything, but it also must not block a prune of the rest.
			continue
		}
		for _, browser := range manifest.Browsers {
			keep[directoryFor(browser.Name, browser.Revision)] = struct{}{}
		}
	}
	return keep, nil
}

// Prune removes browser revisions no installed pin asks for.
//
// This is the targeted replacement for wiping the whole registry: that frees the
// same disk and then charges every session a fresh download. Only directories
// matching Playwright's own <name>-<revision> shape are ever considered, so
// bookkeeping entries such as .links and __dirlock are left alone.
func (c Config) Prune(dryRun bool) ([]string, error) {
	keep, err := c.Keep()
	if err != nil {
		return nil, err
	}
	if len(keep) == 0 {
		return nil, fmt.Errorf("no installed pin under %s could be read; refusing to prune blind", c.Root)
	}
	entries, err := os.ReadDir(c.Browsers)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", c.Browsers, err)
	}
	var removed []string
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || !revisionDir.MatchString(name) {
			continue
		}
		if _, wanted := keep[name]; wanted {
			continue
		}
		path := filepath.Join(c.Browsers, name)
		if !dryRun {
			if err := os.RemoveAll(path); err != nil {
				return removed, fmt.Errorf("remove %s: %w", path, err)
			}
		}
		removed = append(removed, path)
	}
	sort.Strings(removed)
	return removed, nil
}

func (c Config) readManifest() (browsersManifest, error) {
	var manifest browsersManifest
	path := filepath.Join(c.CoreDir(), "browsers.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return manifest, fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return manifest, fmt.Errorf("parse %s: %w", path, err)
	}
	if len(manifest.Browsers) == 0 {
		return manifest, fmt.Errorf("%s lists no browsers", path)
	}
	return manifest, nil
}

func directoryFor(name, revision string) string {
	return strings.ReplaceAll(name, "-", "_") + "-" + revision
}
