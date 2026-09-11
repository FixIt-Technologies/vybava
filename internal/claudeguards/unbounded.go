package claudeguards

import "strings"

func unboundedOutput(segment string, cfg Config) *Denial {
	f := shellFields(strings.TrimLeft(segment, "( \t"))
	for len(f) > 0 && strings.Contains(f[0], "=") {
		f = f[1:]
	}
	if len(f) < 2 {
		return nil
	}
	command := f[0] + " " + f[1]
	if len(f) > 2 && f[0] == "gh" {
		command += " " + f[2]
	}
	if cfg.UnboundedCommands != nil {
		enabled := false
		for _, c := range cfg.UnboundedCommands {
			if c == command {
				enabled = true
			}
		}
		if !enabled {
			return nil
		}
	}
	has := func(flags ...string) bool {
		for _, a := range f[2:] {
			for _, flag := range flags {
				if a == flag || strings.HasPrefix(a, flag+"=") {
					return true
				}
			}
		}
		return false
	}
	cappedCount := func() bool {
		for i, a := range f[2:] {
			if len(a) > 1 && a[0] == '-' && isDigits(a[1:]) {
				return true
			}
			if strings.HasPrefix(a, "-n") && isDigits(strings.TrimPrefix(a, "-n")) {
				return true
			}
			if strings.HasPrefix(a, "--max-count=") && isDigits(strings.TrimPrefix(a, "--max-count=")) {
				return true
			}
			if (a == "-n" || a == "--max-count") && i+3 < len(f) && isDigits(f[i+3]) {
				return true
			}
		}
		return false
	}
	fix := ""
	switch command {
	case "docker logs":
		if !has("--tail", "--since", "-n") {
			fix = "docker logs --tail 200 <container>"
		}
	case "gh run view":
		if has("--log", "--log-failed") {
			fix = "gh run view <id> --log-failed | tail -200"
		}
	case "git log":
		if !cappedCount() {
			fix = "git log -n 20 --oneline"
		}
	case "git diff", "git show":
		pathBound := false
		for i, a := range f {
			if a == "--" && i+1 < len(f) {
				pathBound = true
			}
		}
		if !has("--stat", "--name-only", "--name-status", "--numstat", "--shortstat") && !pathBound {
			fix = command + " --stat (or -- <path>)"
		}
	case "bun test", "bunx jest", "go test":
		filtered := has("-run", "-test.run", "-t", "--testNamePattern", "--testPathPattern", "--testPathPatterns")
		for i := 2; i < len(f); i++ {
			a := f[i]
			// Values of runner flags are not positional test filters.
			if a == "-count" || a == "-timeout" || a == "-parallel" || a == "-p" || a == "--timeout" || a == "--maxWorkers" || a == "--reporter" || a == "--config" {
				i++
				continue
			}
			if !strings.HasPrefix(a, "-") && a != "./..." {
				filtered = true
			}
		}
		if !filtered {
			fix = command + " > /tmp/test.log 2>&1; tail -100 /tmp/test.log"
		}
	default:
		if cfg.UnboundedCommands != nil {
			fix = command + " | tail -100"
		}
	}
	if fix == "" {
		return nil
	}
	return deny("context:unbounded-output", "This command can flood context. Cap or filter its output: "+fix, contextReadEscape)
}
