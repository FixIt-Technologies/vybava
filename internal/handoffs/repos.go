package handoffs

import (
	"context"
	"io/fs"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

const projectsDepth = 4

var (
	registryPath = regexp.MustCompile("^\\|\\s*`([^`]+)`")
	nonAlnum     = regexp.MustCompile(`[^a-z0-9]`)
	remoteRepo   = regexp.MustCompile(`github\.com[:/]([^/\s]+/[^/\s]+?)(?:\.git)?/?\s*$`)
)

// resolver maps project slugs and repo names to checkouts and answers every
// git/gh question at most once per key, safely from the judge workers.
type resolver struct {
	env      Env
	mu       sync.Mutex
	registry map[string]string // slug → checkout, from the timesheet registry
	walked   map[string]string // slug → checkout, from ~/Work/Projects
	repos    map[string]string // slug → checkout ("" = not found)
	remotes  map[string]string // checkout → owner/repo
	branches map[string]bool   // checkout + "\x00" + branch
	prs      map[string]string // owner/repo#N → OPEN · MERGED · CLOSED · ""
}

func newResolver(env Env) *resolver {
	return &resolver{
		env: env, repos: map[string]string{}, remotes: map[string]string{},
		branches: map[string]bool{}, prs: map[string]string{},
	}
}

// slugify is how a checkout's basename becomes a handoff project slug.
func slugify(name string) string {
	return nonAlnum.ReplaceAllString(strings.ToLower(name), "-")
}

// resolve finds the checkout for a slug: the registry first, then a shallow
// walk of the projects tree.
func (r *resolver) resolve(slug string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	slug = slugify(slug)
	if path, done := r.repos[slug]; done {
		return path, path != ""
	}
	if r.registry == nil {
		r.registry = r.loadRegistry()
	}
	path, ok := r.registry[slug]
	if !ok {
		if r.walked == nil {
			r.walked = r.walkProjects()
		}
		path = r.walked[slug]
	}
	r.repos[slug] = path
	return path, path != ""
}

func (r *resolver) loadRegistry() map[string]string {
	out := map[string]string{}
	if r.env.Registry == nil {
		return out
	}
	data, err := r.env.Registry()
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(data), "\n") {
		m := registryPath.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		path := m[1]
		if rest, ok := strings.CutPrefix(path, "~/"); ok {
			path = filepath.Join(r.env.UserHome, rest)
		}
		slug := slugify(filepath.Base(path))
		if _, taken := out[slug]; !taken {
			out[slug] = path
		}
	}
	return out
}

func (r *resolver) walkProjects() map[string]string {
	out := map[string]string{}
	if r.env.Projects == "" {
		return out
	}
	_ = filepath.WalkDir(r.env.Projects, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || !entry.IsDir() {
			return nil
		}
		if path == r.env.Projects {
			return nil
		}
		name := entry.Name()
		if strings.HasPrefix(name, ".") || name == "node_modules" {
			return fs.SkipDir
		}
		rel, _ := filepath.Rel(r.env.Projects, path)
		if slug := slugify(name); out[slug] == "" {
			out[slug] = path
		}
		if strings.Count(rel, string(filepath.Separator))+1 >= projectsDepth {
			return fs.SkipDir
		}
		return nil
	})
	return out
}

// branchLive: the branch exists locally, on origin (as last fetched), or is
// checked out in a worktree. Never fetches.
func (r *resolver) branchLive(ctx context.Context, repo, branch string) bool {
	key := repo + "\x00" + branch
	r.mu.Lock()
	live, done := r.branches[key]
	r.mu.Unlock()
	if done {
		return live
	}
	live = r.ok(ctx, "git", "-C", repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch) ||
		r.ok(ctx, "git", "-C", repo, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+branch)
	if !live {
		out, err := r.env.Exec(ctx, "git", "-C", repo, "worktree", "list", "--porcelain")
		live = err == nil && strings.Contains(string(out), "branch refs/heads/"+branch+"\n")
	}
	r.mu.Lock()
	r.branches[key] = live
	r.mu.Unlock()
	return live
}

// remote returns origin's owner/repo, "" when there is none or it is not GitHub.
func (r *resolver) remote(ctx context.Context, repo string) string {
	r.mu.Lock()
	ownerRepo, done := r.remotes[repo]
	r.mu.Unlock()
	if done {
		return ownerRepo
	}
	if out, err := r.env.Exec(ctx, "git", "-C", repo, "remote", "get-url", "origin"); err == nil {
		if m := remoteRepo.FindStringSubmatch(strings.TrimSpace(string(out))); m != nil {
			ownerRepo = m[1]
		}
	}
	r.mu.Lock()
	r.remotes[repo] = ownerRepo
	r.mu.Unlock()
	return ownerRepo
}

// prState asks gh for OPEN, MERGED or CLOSED; "" when gh cannot answer.
func (r *resolver) prState(ctx context.Context, ownerRepo string, number int) string {
	key := ownerRepo + "#" + strconv.Itoa(number)
	r.mu.Lock()
	state, done := r.prs[key]
	r.mu.Unlock()
	if done {
		return state
	}
	if out, err := r.env.Exec(ctx, "gh", "pr", "view", strconv.Itoa(number), "--repo", ownerRepo, "--json", "state", "-q", ".state"); err == nil {
		state = strings.ToUpper(strings.TrimSpace(string(out)))
	}
	r.mu.Lock()
	r.prs[key] = state
	r.mu.Unlock()
	return state
}

func (r *resolver) ok(ctx context.Context, name string, args ...string) bool {
	_, err := r.env.Exec(ctx, name, args...)
	return err == nil
}
