package configdiscover

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/henderson-tech/vybava/internal/vconfig"
)

var defaultExport = regexp.MustCompile(`(?m)^export default `)

// WriteAbsent preserves the existing TypeScript expression and wraps its value.
// Existing top-level sections are never merged, regenerated or overwritten.
func (r Result) WriteAbsent(cfg *vconfig.Config) ([]string, error) {
	missing := map[string]json.RawMessage{}
	if _, ok := cfg.Sections["guards"]; !ok {
		raw, err := json.Marshal(r.Guards)
		if err != nil {
			return nil, err
		}
		missing["guards"] = raw
	}
	if _, ok := cfg.Sections["lok"]; !ok {
		raw, err := json.Marshal(r.Lok)
		if err != nil {
			return nil, err
		}
		missing["lok"] = raw
	}
	if len(missing) == 0 {
		return []string{}, nil
	}
	raw, err := os.ReadFile(cfg.Path)
	if err != nil {
		return nil, err
	}
	var next []byte
	sections := []string{}
	for _, key := range []string{"guards", "lok"} {
		if _, ok := missing[key]; ok {
			sections = append(sections, key)
		}
	}
	if filepath.Ext(cfg.Path) == ".json" {
		at := strings.LastIndex(string(raw), "}")
		if at < 0 {
			return nil, fmt.Errorf("config is not an object")
		}
		addition := strings.Builder{}
		if len(cfg.Sections) > 0 {
			addition.WriteString(",")
		}
		for i, key := range sections {
			if i > 0 {
				addition.WriteString(",")
			}
			fmt.Fprintf(&addition, "\n  %q: %s", key, missing[key])
		}
		next = []byte(string(raw[:at]) + addition.String() + "\n" + string(raw[at:]))
	} else {
		matches := defaultExport.FindAllIndex(raw, -1)
		if len(matches) != 1 || strings.Contains(string(raw), "__vybavaDiscoveredBase") {
			return nil, fmt.Errorf("cannot safely wrap this default export; apply the discover snippet manually")
		}
		m := matches[0]
		body := string(raw[:m[0]]) + "const __vybavaDiscoveredBase = " + string(raw[m[1]:])
		addition := strings.Builder{}
		addition.WriteString("\nexport default {\n  ...__vybavaDiscoveredBase,\n")
		for _, key := range sections {
			fmt.Fprintf(&addition, "  %q: %s,\n", key, missing[key])
		}
		addition.WriteString("};\n")
		next = []byte(body + addition.String())
	}
	st, err := os.Stat(cfg.Path)
	if err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(cfg.Root, ".vybava-discover-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(st.Mode().Perm()); err != nil {
		f.Close()
		return nil, err
	}
	if _, err = f.Write(next); err != nil {
		f.Close()
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	// Write THROUGH a symlink, never over it: vconfig.Find stats rather than
	// lstats, so a config symlinked into the repo resolves fine on read, and a
	// plain rename would silently replace the link with a regular file and
	// strand whatever it pointed at.
	dest := cfg.Path
	if resolved, err := filepath.EvalSymlinks(dest); err == nil {
		dest = resolved
	}
	if err = os.Rename(f.Name(), dest); err != nil {
		return nil, err
	}
	return sections, nil
}
