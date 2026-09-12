package worktime

import (
	"context"
	"errors"
	"strings"
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

func TestWindowContextPermissionFailureKeepsActivityWithoutStaleTitle(t *testing.T) {
	s := Sample{Application: "com.apple.Terminal", IdleSeconds: 2, WindowTitle: "previous task"}
	got := DarwinWindowContext(context.Background(), s, func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("not authorised to send Apple events")
	})
	if got.Application != s.Application || got.IdleSeconds != 2 || got.WindowTitle != "" || !strings.Contains(got.ContextError, "Accessibility") {
		t.Fatalf("permission degradation: %+v", got)
	}
	got = DarwinWindowContext(context.Background(), got, func(context.Context, string, ...string) ([]byte, error) { return []byte("New task\n"), nil })
	if got.WindowTitle != "New task" || got.ContextError != "" {
		t.Fatalf("permission recovery: %+v", got)
	}
}
