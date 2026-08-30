package catalog_test

import (
	"testing"

	assets "github.com/FixIt-Technologies/vybava"
	"github.com/FixIt-Technologies/vybava/internal/catalog"
)

func TestEmbeddedCatalogIsValid(t *testing.T) {
	t.Parallel()

	c, err := catalog.Load(assets.FS)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(c.Items) == 0 {
		t.Fatal("catalog contains no items")
	}
}

func TestResolveCombinesAndDeduplicatesSelectors(t *testing.T) {
	t.Parallel()

	c, err := catalog.Load(assets.FS)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	selectors := []string{"recommended", "memorylint", "experimental"}
	items, err := c.Resolve(selectors)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	// The union of the selected groups and the bare item, deduplicated.
	// Derived from the catalog rather than hardcoded, so adding a package
	// does not break this test — only a genuine dedup regression does.
	want := map[string]bool{}
	for _, selector := range selectors {
		expanded, err := c.Resolve([]string{selector})
		if err != nil {
			t.Fatalf("Resolve(%q) error = %v", selector, err)
		}
		for _, item := range expanded {
			want[item.ID] = true
		}
	}
	if got := len(items); got != len(want) {
		t.Fatalf("Resolve() item count = %d, want %d (union of %v)", got, len(want), selectors)
	}
	seen := map[string]bool{}
	for _, item := range items {
		if seen[item.ID] {
			t.Fatalf("Resolve() returned %q twice", item.ID)
		}
		seen[item.ID] = true
		if !want[item.ID] {
			t.Fatalf("Resolve() returned unselected item %q", item.ID)
		}
	}
}

func TestResolveRejectsUnknownSelector(t *testing.T) {
	t.Parallel()

	c, err := catalog.Load(assets.FS)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if _, err := c.Resolve([]string{"missing"}); err == nil {
		t.Fatal("Resolve() accepted an unknown selector")
	}
}
