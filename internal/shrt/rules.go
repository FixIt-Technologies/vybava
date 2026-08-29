// Package shrt shortens long URLs into terminal-safe luko.to forms and serves
// the redirector that expands them back. Static rules here are the single
// source of truth for both directions; the CLI shortens offline with them and
// the server expands with the same table, so they must ship from one commit.
package shrt

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// DefaultBase is the public redirector origin.
const DefaultBase = "https://luko.to"

// repoAliases maps the short gh namespace segment to owner/repo. Adding a
// repo is one line; unknown aliases 404 loudly on the server and are never
// emitted by the CLI.
var repoAliases = map[string]string{
	"fixit":     "FixIt-Technologies/FixIt",
	"vitrinka":  "FixIt-Technologies/vitrinka",
	"vybava":    "FixIt-Technologies/vybava",
	"eve":       "FixIt-Technologies/eve-ai-layer",
	"devinfra":  "FixIt-Technologies/devulinka-infra",
	"prodinfra": "FixIt-Technologies/produlinka-infra",
	"webinfra":  "FixIt-Technologies/webulinka-infra",
	"reservine": "Reservine/reservine",
	"resback":   "genesiscz/ReservineBack",
	"exports":   "LEFTEQ/Exports",
	"claudik":   "LEFTEQ/Claudik",
}

// aliasByRepo is the inverted table, built once at init.
var aliasByRepo = func() map[string]string {
	m := make(map[string]string, len(repoAliases))
	for alias, repo := range repoAliases {
		m[strings.ToLower(repo)] = alias
	}
	return m
}()

// reservedSegments are first path segments the server routes statically.
// Store.Mint skips a code that would spell one of these (only "healthz" is
// even reachable at 7 chars), extending the hash prefix instead.
var reservedSegments = map[string]bool{
	"gh": true, "b": true, "api": true, "healthz": true,
}

var (
	githubItem = regexp.MustCompile(`^https://github\.com/([^/]+)/([^/]+)/(?:pull|issues)/(\d+)(?:[/#?].*)?$`)
	githubRepo = regexp.MustCompile(`^https://github\.com/([^/]+)/([^/]+)/?$`)
	boardShort = regexp.MustCompile(`^https://(?:app\.)?vitrinka\.ai/b/(\d+)/?$`)
	ghPath     = regexp.MustCompile(`^gh/([a-z0-9-]+)(?:/(\d+))?$`)
	bPath      = regexp.MustCompile(`^b/(\d+)$`)
)

// ShortenStatic returns the short path (no leading slash) for a URL covered by
// a static rule, or "" when the URL needs a minted code instead.
func ShortenStatic(long string) string {
	if m := githubItem.FindStringSubmatch(long); m != nil {
		if alias, ok := aliasByRepo[strings.ToLower(m[1]+"/"+m[2])]; ok {
			return "gh/" + alias + "/" + m[3]
		}
	}
	if m := githubRepo.FindStringSubmatch(long); m != nil {
		if alias, ok := aliasByRepo[strings.ToLower(m[1]+"/"+m[2])]; ok {
			return "gh/" + alias
		}
	}
	if m := boardShort.FindStringSubmatch(long); m != nil {
		return "b/" + m[1]
	}
	return ""
}

// ExpandStatic resolves a static short path (no leading slash) back to its
// long URL. ok is false when the path matches no rule or an unknown alias.
func ExpandStatic(path string) (long string, ok bool) {
	if m := ghPath.FindStringSubmatch(path); m != nil {
		repo, known := repoAliases[m[1]]
		if !known {
			return "", false
		}
		if m[2] == "" {
			return "https://github.com/" + repo, true
		}
		// GitHub redirects /pull/N to /issues/N when N is an issue, so one
		// form covers both.
		return "https://github.com/" + repo + "/pull/" + m[2], true
	}
	if m := bPath.FindStringSubmatch(path); m != nil {
		return "https://vitrinka.ai/b/" + m[1], true
	}
	return "", false
}

// ValidTarget rejects mint inputs that are not absolute http(s) URLs.
func ValidTarget(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("not an absolute http(s) URL: %q", raw)
	}
	return nil
}
