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
	items, err := c.Resolve([]string{"recommended", "memorylint", "experimental"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got, want := len(items), 6; got != want {
		t.Fatalf("Resolve() item count = %d, want %d", got, want)
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
