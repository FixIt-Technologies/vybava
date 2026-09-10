package claudeguards

import "testing"

func TestE2EReadGuardPatterns(t *testing.T) {
	block := []string{"/x/.e2e/shot.png", "/x/.e2e/a/b/c/shot.png", "/x/proj.e2e-run/shot.png"}
	pass := []string{"/x/.e2e/shot.jpg", "/x/assets/logo.png", "/x/.e2e/notes.md"}
	for _, p := range block {
		// guardE2ERead exits the process on block; test its predicates instead.
		if !(containsE2E(p) && hasPNG(p)) {
			t.Errorf("should block Read of %q", p)
		}
	}
	for _, p := range pass {
		if containsE2E(p) && hasPNG(p) {
			t.Errorf("should pass Read of %q", p)
		}
	}
}

func TestE2EScreenshotPatterns(t *testing.T) {
	if !reRawSimShot.MatchString("xcrun simctl io booted screenshot /tmp/a.png") {
		t.Error("raw simctl screenshot should match")
	}
	if !reRawSimShot.MatchString("sleep 1; xcrun simctl io booted screenshot x.png") {
		t.Error("chained simctl screenshot should match")
	}
	if !reScreencapture.MatchString("screencapture -x /tmp/s.png") {
		t.Error("screencapture should match")
	}
	if reScreencapture.MatchString("man screencapture-notes") {
		t.Error("suffixed word should not match")
	}
}
