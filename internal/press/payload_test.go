package press

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func skillsRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "skills"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("skills payload root not found at %s: %v", root, err)
	}
	return root
}

func pressPayloadFiles(t *testing.T) map[string]string {
	t.Helper()
	files := map[string]string{}
	root := skillsRoot(t)
	for _, skill := range []string{"press-pdf", "press-logo", "press-offer"} {
		err := filepath.WalkDir(filepath.Join(root, skill), func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			b, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			rel, _ := filepath.Rel(root, path)
			files[rel] = string(b)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(files) == 0 {
		t.Fatal("no press payload files found")
	}
	return files
}

// The load-bearing guarantee of decision 0003: Výbava is public and
// brew-distributed, so no issuer's registry identity, commercial rate, or
// client name may live in a shipped payload. This is a shape check, not a
// blocklist of one company's values — a future contributor pasting THEIR IČO
// or day rate into a skill must fail here too.
func TestShippedPayloadCarriesNoIssuerIdentity(t *testing.T) {
	patterns := []struct {
		name string
		re   *regexp.Regexp
	}{
		{"a Czech company registration number (IČO)", regexp.MustCompile(`(?i)\bI[ČC]O[\s:]*\d{8}\b`)},
		{"a Czech VAT number (DIČ)", regexp.MustCompile(`\bCZ\d{8,10}\b`)},
		{"a Czech legal-entity suffix", regexp.MustCompile(`(?i)\b(s\.r\.o\.|a\.s\.)`)},
		{"a data box id", regexp.MustCompile(`(?i)datov[áa]\s+schr[áa]nka['"\s:]+[a-z0-9]{7}\b`)},
		{"a concrete day rate", regexp.MustCompile(`\d[\d\s\x{00A0}]{3,}(K[čc]|CZK|EUR)\s*/\s*(člověkoden|MD|man-day)`)},
		{"a Czech postcode + city", regexp.MustCompile(`\b\d{3}\s?\d{2}\s+Praha\b`)},
	}
	for name, body := range pressPayloadFiles(t) {
		// The identity SHAPE may be named freely; only values are forbidden.
		for _, p := range patterns {
			if match := p.re.FindString(body); match != "" {
				t.Errorf("%s contains %s: %q\n"+
					"Issuer identity belongs in ~/.config/press/identity.json, never in this repo "+
					"(docs/decisions/0003-press-family-absorption.md).", name, p.name, strings.TrimSpace(match))
			}
		}
	}
}

// Every identity field the generator reads without a fallback must be one that
// MissingIdentityFields refuses to leave empty — otherwise the decision record's
// promise ("refuses to render while a required field is empty") is false and a
// document renders with a blank in it.
func TestGeneratorConsumesOnlyValidatedIdentityFields(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(skillsRoot(t), "press-offer", "template.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)

	// The template aliases the three blocks; map each alias to its JSON prefix.
	aliases := map[string]string{"ISSUER": "issuer", "RATE": "commercial", "identity.brand": "brand"}

	var empty Identity
	required := map[string]bool{}
	for _, field := range empty.MissingIdentityFields() {
		required[field] = true
	}
	// DayRate is numeric, so the empty-Identity sweep above already lists it.

	for alias, prefix := range aliases {
		re := regexp.MustCompile(regexp.QuoteMeta(alias) + `\.([A-Za-z]+)`)
		for _, match := range re.FindAllStringSubmatch(source, -1) {
			field := prefix + "." + match[1]
			// A consumption guarded by `|| fallback` is deliberately optional.
			guarded := regexp.MustCompile(regexp.QuoteMeta(match[0]) + `\s*\|\|`)
			if guarded.MatchString(source) {
				continue
			}
			if !required[field] {
				t.Errorf("template.mjs consumes %s with no fallback, but MissingIdentityFields "+
					"does not require it — a document could render with that value blank", field)
			}
		}
	}
}

// The skill compares a project's pipelineVersion against build.py and offers an
// upgrade when it is behind. If the shipped defaults lag build.py, every freshly
// scaffolded project is immediately reported as out of date.
func TestShippedPipelineVersionsAgree(t *testing.T) {
	root := filepath.Join(skillsRoot(t), "press-pdf", "assets")
	readConst := func(rel string) string {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		m := regexp.MustCompile(`(?m)^PIPELINE_VERSION\s*=\s*(\d+)`).FindStringSubmatch(string(b))
		if m == nil {
			t.Fatalf("%s declares no PIPELINE_VERSION", rel)
		}
		return m[1]
	}
	readConfig := func(rel string) string {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		var cfg struct {
			PipelineVersion json.Number `json:"pipelineVersion"`
		}
		if err := json.Unmarshal(b, &cfg); err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		return cfg.PipelineVersion.String()
	}

	want := readConst("pipeline/build.py")
	for rel, got := range map[string]string{
		"pipeline/lint.py":                       readConst("pipeline/lint.py"),
		"pdf-press.config.default.json":          readConfig("pdf-press.config.default.json"),
		"examples/example.pdf-press.config.json": readConfig("examples/example.pdf-press.config.json"),
	} {
		if got != want {
			t.Errorf("%s is at pipeline v%s but build.py is at v%s — a fresh scaffold "+
				"would immediately report itself out of date", rel, got, want)
		}
	}
}

// content has additionalProperties:false, so a key the skill documents but the
// schema omits makes a documented config fail validation.
func TestConfigSchemaAcceptsEveryDocumentedContentKey(t *testing.T) {
	root := filepath.Join(skillsRoot(t), "press-pdf")
	b, err := os.ReadFile(filepath.Join(root, "assets", "pdf-press.config.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties struct {
			Content struct {
				AdditionalProperties *bool                      `json:"additionalProperties"`
				Properties           map[string]json.RawMessage `json:"properties"`
			} `json:"content"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(b, &schema); err != nil {
		t.Fatal(err)
	}
	content := schema.Properties.Content
	if content.AdditionalProperties == nil || *content.AdditionalProperties {
		t.Skip("content is open; unknown keys cannot fail validation")
	}
	// pagecheck.py reads content.pagination; the SKILL documents it.
	for _, key := range []string{"pagination"} {
		if _, ok := content.Properties[key]; !ok {
			t.Errorf("the schema forbids content.%s, but the pipeline reads it and "+
				"SKILL.md documents it — a documented config would fail validation", key)
		}
	}
	pagination := content.Properties["pagination"]
	pagecheck, err := os.ReadFile(filepath.Join(root, "assets", "pipeline", "pagecheck.py"))
	if err != nil {
		t.Fatal(err)
	}
	var paginationSchema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(pagination, &paginationSchema); err != nil {
		t.Fatal(err)
	}
	for _, key := range regexp.MustCompile(`"(min[A-Za-z]+|max[A-Za-z]+|marginTolerancePt|widowGapFactor)":`).
		FindAllStringSubmatch(string(pagecheck), -1) {
		if _, ok := paginationSchema.Properties[key[1]]; !ok {
			t.Errorf("pagecheck.py honours content.pagination.%s but the schema omits it", key[1])
		}
	}
}
