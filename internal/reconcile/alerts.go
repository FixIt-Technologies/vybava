package reconcile

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"
)

// alert ships the digest to every configured channel. Each channel records
// the digest only after ITS OWN successful delivery, so one channel's outage
// never respams the other and never suppresses its own retry. A clean tick
// clears every marker so the next drift alerts fresh.
func (e *Engine) alert(r *Result) {
	st := e.state()
	channels := make([]string, 0, len(e.M.Alerts))
	for _, a := range e.M.Alerts {
		channels = append(channels, channelName(a))
	}
	if r.Digest == "" {
		st.ClearAlertMarkers(channels)
		return
	}
	sum := bytesSHA([]byte(r.Digest))
	for _, a := range e.M.Alerts {
		name := channelName(a)
		if st.AlertMarker(name) == sum {
			continue
		}
		var err error
		switch a.Type {
		case "telegram":
			err = e.notifyTelegram(a, r.Digest)
		case "eve-monitor":
			err = e.notifyEve(a, r.Digest)
		}
		if err != nil {
			e.log("%s alert failed (non-fatal): %v", name, err)
			continue
		}
		if err := st.SetAlertMarker(name, sum); err != nil {
			e.logErr("alert marker %s: %v", name, err)
		}
	}
}

func channelName(a Alert) string {
	if a.Type == "eve-monitor" {
		return "eve"
	}
	return a.Type
}

// notifyTelegram sources the box's shell notify library and calls
// notify_telegram <channel> <emoji> <title> <body>. An absent library is
// "not configured" — not a failure.
func (e *Engine) notifyTelegram(a Alert, digest string) error {
	if _, err := os.Stat(a.Lib); err != nil {
		return nil
	}
	// The library is sourced from $1, never $0: telegram-notify.sh refuses to
	// run when BASH_SOURCE[0] == $0, which is exactly what `. "$0"` triggers.
	cmd := exec.Command("bash", "-c", `. "$1"; shift; notify_telegram "$@"`, "vybava-reconcile", a.Lib,
		a.Channel, "⚠️", "infra-reconcile ("+e.M.Repo+")", digest)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(errb.String()))
	}
	return nil
}

// notifyEve POSTs the digest to the eve server-monitor webhook so an incident
// opens. Config (absent or incomplete = feature off):
//
//	EVE_MONITOR_URL=https://…/eve/v1/monitor
//	EVE_MONITOR_TOKEN=<bearer>
func (e *Engine) notifyEve(a Alert, digest string) error {
	raw, err := os.ReadFile(a.Config)
	if err != nil {
		return nil
	}
	cfg := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if ok {
			cfg[k] = v // last wins, like `sed -n … | tail -1`
		}
	}
	url, token := cfg["EVE_MONITOR_URL"], cfg["EVE_MONITOR_TOKEN"]
	if url == "" || token == "" {
		return nil
	}
	if cfg["EVE_MONITOR_CURL_OPTS"] != "" {
		e.log("eve webhook: EVE_MONITOR_CURL_OPTS is not supported by the Go engine and was ignored")
	}
	payload, err := json.Marshal(map[string]string{
		"source":      "infra-reconcile",
		"title":       "Infra drift (" + e.M.Repo + ")",
		"severity":    "warning",
		"host":        e.M.HostLabel,
		"description": truncateBytes(digest, 3000),
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("eve webhook returned %s", resp.Status)
	}
	return nil
}

// truncateBytes is `head -c n` on a rune boundary.
func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
