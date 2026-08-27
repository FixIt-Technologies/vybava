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
	// Derive the expectation from the catalog rather than hard-coding a count,
	// so adding an item to a group cannot fail a test about deduplication.
	// "memorylint" is deliberately named twice: once directly and once through
	// the recommended group.
	want := expandUnique(t, c, selectors)
	if got := len(items); got != len(want) {
		t.Fatalf("Resolve() item count = %d, want %d (%v)", got, len(want), want)
	}
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if _, duplicate := seen[item.ID]; duplicate {
			t.Fatalf("Resolve() returned %q twice", item.ID)
		}
		seen[item.ID] = struct{}{}
		if _, expected := want[item.ID]; !expected {
			t.Fatalf("Resolve() returned unselected item %q", item.ID)
		}
	}
}

// expandUnique flattens selectors into the set of item IDs they name, treating a
// selector as a group first and an item ID otherwise.
func expandUnique(t *testing.T, c catalog.Catalog, selectors []string) map[string]struct{} {
	t.Helper()
	groups := make(map[string][]string, len(c.Groups))
	for _, group := range c.Groups {
		groups[group.ID] = group.Items
	}
	unique := make(map[string]struct{})
	for _, selector := range selectors {
		if members, isGroup := groups[selector]; isGroup {
			for _, member := range members {
				unique[member] = struct{}{}
			}
			continue
		}
		unique[selector] = struct{}{}
	}
	return unique
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
