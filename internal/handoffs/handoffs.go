// Package handoffs keeps the handoff ledger honest: it decides which open
// handoffs under `~/.claude/handoffs` still have a branch or pull request
// alive and archives the rest — done when the work merged, abandoned when it
// simply stopped. Nothing is ever deleted and an
// `unknown` verdict is never acted on.
package handoffs

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/FixIt-Technologies/vybava/internal/memorylint"
)

const (
	VerdictLive    = "live"
	VerdictDead    = "dead"
	VerdictUnknown = "unknown"

	// DefaultStaleDays is how long a handoff without branch/PR evidence may
	// sit untouched before it counts as dead.
	DefaultStaleDays = 14

	workers = 8
)

var liveStatuses = []string{"open", "in-progress"}

// Env is the machine the reconciler runs against; tests substitute it so no
// test ever shells out or reads the real registry.
type Env struct {
	Home     string // the handoffs home, e.g. ~/.claude/handoffs
	UserHome string // expands `~` in registry paths
	Projects string // ~/Work/Projects — fallback repo lookup, depth ≤ 4
	Now      time.Time
	Registry func() ([]byte, error) // timesheet-repo-registry.md
	Exec     func(ctx context.Context, name string, args ...string) ([]byte, error)
}

type Options struct {
	Project   string // only this project slug
	Apply     bool   // archive the dead ones
	StaleDays int    // 0 → DefaultStaleDays
}

type Item struct {
	Path          string   `json:"path"`
	Project       string   `json:"project"`
	Slug          string   `json:"slug"`
	Status        string   `json:"status"`
	Verdict       string   `json:"verdict"`
	Reason        string   `json:"reason"`
	Branches      []string `json:"branches"`                // repo@branch
	PRs           []string `json:"prs"`                     // owner/repo#N, or #N when the repo is unknown
	Mentions      []string `json:"mentions"`                // PRs named below the first heading; context, never evidence
	ArchiveStatus string   `json:"archiveStatus,omitempty"` // done · abandoned — what --apply writes for a dead item
	Archived      string   `json:"archived,omitempty"`
}

type Summary struct {
	Live     int `json:"live"`
	Dead     int `json:"dead"`
	Unknown  int `json:"unknown"`
	Archived int `json:"archived"`
}

type Report struct {
	Items   []Item  `json:"items"`
	Summary Summary `json:"summary"`
}

type candidate struct {
	path, project, slug, status string
	data                        []byte
	mtime                       time.Time
	directory                   bool // <slug>/handoff.md shape
}

// Reconcile judges every open handoff under env.Home and, with opts.Apply,
// archives the dead ones. Items come back sorted by project then slug.
func Reconcile(ctx context.Context, env Env, opts Options) (Report, error) {
	if !memorylint.IsHandoffHome(env.Home) {
		return Report{}, fmt.Errorf("%s is not a .claude/handoffs home", env.Home)
	}
	if opts.StaleDays == 0 {
		opts.StaleDays = DefaultStaleDays
	}
	candidates, err := scan(env.Home, opts.Project)
	if err != nil {
		return Report{}, err
	}
	res := newResolver(env)
	items := make([]Item, len(candidates))
	var wg sync.WaitGroup
	sem := make(chan struct{}, workers)
	for i, c := range candidates {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, c candidate) {
			defer wg.Done()
			defer func() { <-sem }()
			items[i] = judge(ctx, env, res, opts, c)
		}(i, c)
	}
	wg.Wait()
	report := Report{Items: items}
	for i := range items {
		switch items[i].Verdict {
		case VerdictLive:
			report.Summary.Live++
		case VerdictDead:
			report.Summary.Dead++
			if opts.Apply {
				if err := archive(env, candidates[i], &items[i]); err != nil {
					return report, err
				}
				report.Summary.Archived++
			}
		default:
			report.Summary.Unknown++
		}
	}
	return report, nil
}

// scan lists the live handoffs: both file shapes, never archive/ or context
// files, status open or in-progress.
func scan(home, project string) ([]candidate, error) {
	var out []candidate
	err := filepath.WalkDir(home, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(path), ".md") {
			return nil
		}
		relative, err := filepath.Rel(home, path)
		if err != nil {
			return err
		}
		slug, isHandoff, archived := memorylint.HandoffSlug(relative)
		if !isHandoff || archived {
			return nil
		}
		parts := strings.Split(filepath.ToSlash(relative), "/")
		if project != "" && parts[0] != project {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		status, _ := statusLine(data)
		if !contains(liveStatuses, status) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		out = append(out, candidate{
			path: path, project: parts[0], slug: slug, status: status,
			data: data, mtime: info.ModTime(), directory: len(parts) == 3,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].project != out[j].project {
			return out[i].project < out[j].project
		}
		return out[i].slug < out[j].slug
	})
	return out, nil
}

// judge applies the verdict rules to one handoff. Precedence: any live
// evidence wins; a MERGED marker without live evidence is dead; anything the
// machine could not check is unknown; every branch gone and every PR
// merged/closed is dead; no evidence at all falls back to the mtime.
func judge(ctx context.Context, env Env, res *resolver, opts Options, c candidate) Item {
	item := Item{
		Path: c.path, Project: c.project, Slug: c.slug, Status: c.status,
		Branches: []string{}, PRs: []string{}, Mentions: []string{},
	}
	ev := Extract(string(c.data))
	for _, p := range ev.Mentions {
		item.Mentions = append(item.Mentions, fmt.Sprintf("%s#%d", p.Repo, p.Number))
	}
	var live, unknown, dead []string
	merged := ev.Merged // the work landed: archive as done rather than abandoned
	for _, b := range ev.Branches {
		if IsDefaultBranch(b.Branch) {
			continue
		}
		repoSlug := b.Repo
		if repoSlug == "" {
			repoSlug = c.project
		}
		label := repoSlug + "@" + b.Branch
		item.Branches = append(item.Branches, label)
		repo, ok := res.resolve(repoSlug)
		if !ok {
			unknown = append(unknown, "repo not found: "+repoSlug)
			continue
		}
		if res.branchLive(ctx, repo, b.Branch) {
			live = append(live, "branch "+label+" live")
		} else {
			dead = append(dead, "branch "+label+" gone")
		}
	}
	for _, p := range ev.PRs {
		ownerRepo := p.Repo
		if ownerRepo == "" {
			if repo, ok := res.resolve(c.project); ok {
				ownerRepo = res.remote(ctx, repo)
			}
		}
		label := fmt.Sprintf("%s#%d", ownerRepo, p.Number)
		item.PRs = append(item.PRs, label)
		if ownerRepo == "" {
			unknown = append(unknown, "PR "+label+" repo unknown")
			continue
		}
		switch res.prState(ctx, ownerRepo, p.Number) {
		case "OPEN":
			live = append(live, "PR "+label+" open")
		case "MERGED":
			dead = append(dead, "PR "+label+" merged")
			merged = true
		case "CLOSED":
			dead = append(dead, "PR "+label+" closed")
		default:
			unknown = append(unknown, "PR "+label+" state unknown")
		}
	}
	switch {
	case len(live) > 0:
		item.Verdict, item.Reason = VerdictLive, strings.Join(live, "; ")
	case ev.Merged:
		item.Verdict, item.Reason = VerdictDead, "Branch line marked MERGED"
	case len(unknown) > 0:
		item.Verdict, item.Reason = VerdictUnknown, strings.Join(unknown, "; ")
	case len(dead) > 0:
		item.Verdict, item.Reason = VerdictDead, strings.Join(dead, "; ")
	default:
		days := int(env.Now.Sub(c.mtime).Hours() / 24)
		if days > opts.StaleDays {
			item.Verdict, item.Reason = VerdictDead, fmt.Sprintf("no branch/PR evidence, untouched %d days", days)
		} else {
			item.Verdict, item.Reason = VerdictUnknown, "no branch/PR evidence"
		}
	}
	if item.Verdict == VerdictDead {
		item.ArchiveStatus = "abandoned"
		if merged {
			item.ArchiveStatus = "done"
		}
	}
	return item
}

// archive flips the frontmatter status to item.ArchiveStatus (that one line only) and
// moves the handoff — file or whole directory — under <project>/archive/,
// suffixing -YYYYMMDD when the name is already taken.
func archive(env Env, c candidate, item *Item) error {
	rewritten, ok := setStatus(c.data, item.ArchiveStatus)
	if !ok {
		return fmt.Errorf("%s: no status line in frontmatter", c.path)
	}
	src, name := c.path, filepath.Base(c.path)
	if c.directory {
		src = filepath.Dir(c.path)
		name = filepath.Base(src)
	}
	archiveDir := filepath.Join(env.Home, c.project, "archive")
	if err := os.MkdirAll(archiveDir, 0o755); err != nil {
		return err
	}
	dst := filepath.Join(archiveDir, name)
	if exists(dst) {
		suffix := "-" + env.Now.Format("20060102")
		if c.directory {
			dst += suffix
		} else {
			dst = strings.TrimSuffix(dst, ".md") + suffix + ".md"
		}
		if exists(dst) {
			return fmt.Errorf("%s: archive target %s already exists", c.path, dst)
		}
	}
	if err := os.WriteFile(c.path, rewritten, 0o644); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err != nil {
		return err
	}
	item.Status = item.ArchiveStatus
	item.Archived = dst
	if c.directory {
		item.Archived = filepath.Join(dst, "handoff.md")
	}
	return nil
}

// statusLine finds `status:` inside the frontmatter block; index is -1 when
// there is none.
func statusLine(data []byte) (status string, index int) {
	lines := strings.SplitAfter(string(data), "\n")
	if len(lines) == 0 || strings.TrimRight(lines[0], "\r\n") != "---" {
		return "", -1
	}
	for i := 1; i < len(lines); i++ {
		line := strings.TrimRight(lines[i], "\r\n")
		if line == "---" {
			return "", -1
		}
		if rest, ok := strings.CutPrefix(line, "status:"); ok {
			return strings.TrimSpace(rest), i
		}
	}
	return "", -1
}

// setStatus rewrites only the status line, keeping every other byte and the
// line's own ending.
func setStatus(data []byte, status string) ([]byte, bool) {
	_, index := statusLine(data)
	if index < 0 {
		return nil, false
	}
	lines := strings.SplitAfter(string(data), "\n")
	ending := lines[index][len(strings.TrimRight(lines[index], "\r\n")):]
	lines[index] = "status: " + status + ending
	return []byte(strings.Join(lines, "")), true
}

func isDefaultBranch(branch string) bool {
	return branch == "main" || branch == "master"
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return !errors.Is(err, fs.ErrNotExist)
}

func contains(list []string, value string) bool {
	for _, v := range list {
		if v == value {
			return true
		}
	}
	return false
}
