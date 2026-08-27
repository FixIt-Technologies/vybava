package shrt

import (
	"bytes"
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// codeLen is fixed: route dispatch and namespace-collision safety rely on
// every minted code being exactly this long.
const codeLen = 7

var codeEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// Code derives the permanent, idempotent short code for a URL.
func Code(long string) string {
	sum := sha256.Sum256([]byte(long))
	return strings.ToLower(codeEncoding.EncodeToString(sum[:]))[:codeLen]
}

// Result is one shortened URL, JSON-stable for automation.
type Result struct {
	Long   string `json:"long"`
	Short  string `json:"short"`
	Static bool   `json:"static"`
	Minted bool   `json:"minted"`
}

// Client shortens URLs: static rules offline first, the mint API otherwise.
type Client struct {
	Base  string // redirector origin, DefaultBase when empty
	Token string // mint bearer token; empty means static-only
	HTTP  *http.Client
}

func (c Client) base() string {
	if c.Base != "" {
		return strings.TrimRight(c.Base, "/")
	}
	return DefaultBase
}

// Shorten resolves one URL. URLs already shorter than the short form (or not
// shortenable without a token) come back unchanged with Static/Minted false —
// printing something clickable always beats erroring mid-pipeline.
func (c Client) Shorten(long string) (Result, error) {
	if err := ValidTarget(long); err != nil {
		return Result{}, err
	}
	if path := ShortenStatic(long); path != "" {
		return Result{Long: long, Short: c.base() + "/" + path, Static: true}, nil
	}
	if c.Token == "" {
		return Result{Long: long, Short: long}, fmt.Errorf("no mint token (set LUKO_TOKEN or keychain item %q)", keychainService)
	}
	short, err := c.mint(long)
	if err != nil {
		return Result{Long: long, Short: long}, err
	}
	return Result{Long: long, Short: short, Minted: true}, nil
}

type mintRequest struct {
	URL string `json:"url"`
}

type mintResponse struct {
	Short string `json:"short"`
	Code  string `json:"code"`
}

func (c Client) mint(long string) (string, error) {
	body, err := json.Marshal(mintRequest{URL: long})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequest(http.MethodPost, c.base()+"/api/mint", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("mint: %s: %s", resp.Status, strings.TrimSpace(string(payload)))
	}
	var out mintResponse
	if err := json.Unmarshal(payload, &out); err != nil {
		return "", err
	}
	if out.Short == "" {
		return "", fmt.Errorf("mint: empty short URL in response")
	}
	return out.Short, nil
}

const keychainService = "luko.to"

// LoadToken resolves the mint token: $LUKO_TOKEN first, then the macOS
// Keychain (service luko.to, account mint). Empty means static-only mode.
func LoadToken() string {
	if t := strings.TrimSpace(os.Getenv("LUKO_TOKEN")); t != "" {
		return t
	}
	if runtime.GOOS != "darwin" {
		return ""
	}
	out, err := exec.Command("security", "find-generic-password",
		"-s", keychainService, "-a", "mint", "-w").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// StoreToken writes the mint token into the macOS Keychain.
func StoreToken(token string) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("keychain storage is macOS-only; export LUKO_TOKEN instead")
	}
	cmd := exec.Command("security", "add-generic-password",
		"-s", keychainService, "-a", "mint", "-w", token, "-U")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("security add-generic-password: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// OSC8 wraps label as a terminal hyperlink to target (Warp, iTerm2, kitty…).
func OSC8(label, target string) string {
	return "\x1b]8;;" + target + "\x1b\\" + label + "\x1b]8;;\x1b\\"
}
