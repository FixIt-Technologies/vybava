package worktime

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDarwinSampleMetadataAndFailure(t *testing.T) {
	run := func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name == "/usr/sbin/ioreg" {
			return []byte(`"HIDIdleTime" = 2500000000`), nil
		}
		return []byte(`{"application":"com.apple.Terminal"}`), nil
	}
	s, err := DarwinSample(context.Background(), run, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if s.IdleSeconds != 2.5 || s.Application != "com.apple.Terminal" || s.Source != "macos-foreground" {
		t.Fatalf("sample=%+v", s)
	}
	_, err = DarwinSample(context.Background(), func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("permission denied") }, time.Now())
	if err == nil {
		t.Fatal("permission failure became fabricated activity")
	}
}
