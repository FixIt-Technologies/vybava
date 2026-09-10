package claudeguards

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------
// Context-budget rules (incident 2026-09-10: one epic session burned 750k
// tokens — 271k of it the agent's own shell-written files, ~190k whole-file
// dumps, 18k reading its own transcript — with zero Edit/Write calls,
// because the bypass-permissions harness text says "prefer Bash over
// Read/Edit/Write"). These rules make the cheaper path the only path:
//
//   context:inline-script-write  python/node/… heredoc or -c/-e script that
//                                writes files → use Edit/Write
//   context:heredoc-overwrite    cat/tee > <existing file> → use Edit
//   context:whole-file-dump      cat/sed/head/tail (or Read without limit)
//                                exceeding maxDumpLines → read a range or
//                                delegate the survey to Explore
//   context:transcript-dump      dumping a ~/.claude/projects/**/*.jsonl
//                                session transcript → aggregate with jq/node
//
// Throwaway locations (/tmp, /private/tmp, /var/folders, $TMPDIR) are exempt
// from the write rules; piped or redirected reads never reach the context and
// are exempt from the read rules.
// ---------------------------------------------------------------------------

// maxDumpLines is the largest single read that may land in context unasked.
const maxDumpLines = 200

const (
	contextWriteEscape = "If a shell write is genuinely the only option (binary content, generated megafile), re-run prefixed with CLAUDE_ALLOW_SHELL_EDIT=1 and say why in your reply."
	contextReadEscape  = "If the whole file is genuinely needed (porting it, auditing every line), re-run prefixed with CLAUDE_ALLOW_CONTEXT_DUMP=1 and say why in your reply."
)

var (
	// An interpreter fed an inline program: heredoc on stdin, -c / -e source.
	reInlineScript = regexp.MustCompile(`(^|[[:space:];&|(])(python[0-9.]*|node|bun|deno|ruby|perl)[[:space:]]+(-[[:space:]]*<<|<<|-c[[:space:]]|-e[[:space:]])`)
	// Something in that program writes a file.
	reScriptWrites = regexp.MustCompile(`open\([^)]*['"][wax]|\.write_text\(|\.write_bytes\(|writeFileSync\(|writeFile\(|Bun\.write\(|Deno\.writeTextFile\(|File\.(write|open)\(|open\([^)]*['"]w`)

	// `cat > path`, `cat >| path`, `cat <<EOF > path`, `tee path` (no -a).
	reCatRedirect = regexp.MustCompile(`^cat[[:space:]]*(?:<<-?[[:space:]]*['"]?[A-Za-z_][A-Za-z0-9_]*['"]?[[:space:]]*)?>\|?[[:space:]]*([^[:space:]>]+)`)
	reTeeWrite    = regexp.MustCompile(`^tee[[:space:]]+(?:-[^a[:space:]]+[[:space:]]+)*([^-][^[:space:]]*)`)

	reFileRedirect = regexp.MustCompile(`(^|[^0-9&])>\|?[[:space:]]*[^&[:space:]]`)
	reSedRange     = regexp.MustCompile(`^(?:(\d+)|\$)(?:,(?:(\d+)|\$))?p$`)
	// A sed script made only of print ranges: `120,180p`, `5p;40,60p`, `1,$p`.
	reSedScript = regexp.MustCompile(`^[0-9,$p;[:space:]]+p[;[:space:]]*$`)
)

// dumpCommands are the readers whose unpiped output lands in context whole.
var dumpCommands = map[string]bool{"cat": true, "sed": true, "head": true, "tail": true, "less": true, "more": true, "bat": true}

// noLineBudget are Read-tool targets the offset/limit knobs do not apply to.
var noLineBudget = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".pdf": true, ".ipynb": true}

// throwawayRoots are scratch locations the write rules ignore.
var throwawayRoots = func() []string {
	roots := []string{"/tmp/", "/private/tmp/", "/var/folders/", "/private/var/folders/"}
	if t := os.Getenv("TMPDIR"); t != "" {
		roots = append(roots, strings.TrimRight(t, "/")+"/")
	}
	return roots
}()

// transcriptRoot is where Claude Code keeps session transcripts.
var transcriptRoot = func() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "projects")
}()

func throwawayPath(p string) bool {
	for _, d := range throwawayRoots {
		if strings.HasPrefix(p, d) {
			return true
		}
	}
	return false
}

// resolvePath expands ~ and makes p absolute against cwd. Shell-expanded
// forms ($VAR, globs) stay as-is and simply fail to stat, which means pass.
func resolvePath(p, cwd string) string {
	p = strings.Trim(p, `'"`)
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, p[2:])
		}
	}
	if !filepath.IsAbs(p) {
		if cwd == "" {
			cwd, _ = os.Getwd()
		}
		p = filepath.Join(cwd, p)
	}
	return filepath.Clean(p)
}

func isTranscript(abs string) bool {
	return transcriptRoot != "" && strings.HasPrefix(abs, transcriptRoot+"/") && strings.HasSuffix(abs, ".jsonl")
}

// lineCount counts newlines in a regular file; (0, false) when it is not one.
// Reads at most 4 MiB — anything past that is over budget regardless.
func lineCount(abs string) (int, bool) {
	st, err := os.Stat(abs)
	if err != nil || !st.Mode().IsRegular() {
		return 0, false
	}
	f, err := os.Open(abs)
	if err != nil {
		return 0, false
	}
	defer f.Close()
	n := 0
	buf := make([]byte, 64<<10)
	var total int64
	for total < 4<<20 {
		k, err := f.Read(buf)
		n += bytes.Count(buf[:k], []byte{'\n'})
		total += int64(k)
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, false
		}
	}
	if total >= 4<<20 {
		return maxDumpLines + 1, true
	}
	return n, true
}

// shellFields splits a segment on whitespace, honouring single and double
// quotes (kept out of the field). Good enough for argv-shaped commands.
func shellFields(s string) []string {
	var out []string
	var cur strings.Builder
	inField, q := false, byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case q != 0:
			if c == q {
				q = 0
			} else {
				cur.WriteByte(c)
			}
		case c == '\'' || c == '"':
			q, inField = c, true
		case c == ' ' || c == '\t' || c == '\r':
			if inField {
				out = append(out, cur.String())
				cur.Reset()
				inField = false
			}
		default:
			cur.WriteByte(c)
			inField = true
		}
	}
	if inField {
		out = append(out, cur.String())
	}
	return out
}

// dumpSegment is one shell segment plus whether its stdout goes somewhere
// other than the tool result (a pipe into the next segment, or a redirect).
type dumpSegment struct {
	text     string
	consumed bool
}

// dumpSegments splits like segments() but remembers when a segment's output
// is consumed by a pipe. `cat f | grep x` never reaches context; `cat f` does.
func dumpSegments(cmd string) []dumpSegment {
	src := stripQuotedHeredocs(cmd)
	var out []dumpSegment
	pos := 0
	for _, m := range segmentSplit.FindAllStringIndex(src, -1) {
		out = appendDumpSegment(out, src[pos:m[0]], src[m[0]:m[1]] == "|")
		pos = m[1]
	}
	return appendDumpSegment(out, src[pos:], false)
}

func appendDumpSegment(out []dumpSegment, raw string, piped bool) []dumpSegment {
	s := strings.Trim(raw, " \t\r")
	if s == "" {
		return out
	}
	return append(out, dumpSegment{text: s, consumed: piped || reFileRedirect.MatchString(s)})
}

// dumpVerdict is what dumpBudget decided about one read segment.
type dumpVerdict int

const (
	dumpOK         dumpVerdict = iota // within budget, or not a read we track
	dumpOverBudget                    // more than maxDumpLines would land in context
	dumpTranscript                    // a ~/.claude/projects session transcript
	dumpCatalog                       // a lok-managed locale catalog
)

// dumpBudget estimates how many lines a cat/sed/head/tail segment prints.
// file and lines describe the offending file when the verdict is not dumpOK.
func dumpBudget(seg, cwd string) (verdict dumpVerdict, file string, lines, total int) {
	fields := shellFields(seg)
	if len(fields) == 0 || !dumpCommands[fields[0]] {
		return dumpOK, "", 0, 0
	}
	limit := -1 // -1 = whole file
	var paths []string
	args := fields[1:]
	switch fields[0] {
	case "sed":
		limit = 0
		for i := 0; i < len(args); i++ {
			a := args[i]
			switch {
			case a == "-n" || a == "-E" || a == "-r" || a == "--quiet" || a == "-i" || strings.HasPrefix(a, "-i"):
				if a == "-i" || strings.HasPrefix(a, "-i") {
					return dumpOK, "", 0, 0 // in-place edit, prints nothing
				}
			case a == "-e" && i+1 < len(args):
				i++
				limit = addSedRange(limit, args[i])
			case strings.HasPrefix(a, "-"):
			case reSedScript.MatchString(a):
				limit = addSedRange(limit, a)
			default:
				paths = append(paths, a)
			}
		}
		if limit == 0 {
			return dumpOK, "", 0, 0 // no print range we understand → pass
		}
	case "head", "tail":
		limit = 10
		for i := 0; i < len(args); i++ {
			a := args[i]
			switch {
			case (a == "-n" || a == "-c") && i+1 < len(args):
				i++
				if a == "-c" {
					return dumpOK, "", 0, 0
				}
				limit = atoiOr(strings.TrimPrefix(args[i], "-"), maxDumpLines+1)
			case strings.HasPrefix(a, "-n"):
				limit = atoiOr(strings.TrimPrefix(a[2:], "-"), maxDumpLines+1)
			case strings.HasPrefix(a, "-c"):
				return dumpOK, "", 0, 0
			case strings.HasPrefix(a, "-") && len(a) > 1 && isDigits(a[1:]):
				limit = atoiOr(a[1:], maxDumpLines+1)
			case strings.HasPrefix(a, "-"):
			default:
				paths = append(paths, a)
			}
		}
	default: // cat, less, more, bat
		for _, a := range args {
			if !strings.HasPrefix(a, "-") {
				paths = append(paths, a)
			}
		}
	}
	for _, p := range paths {
		abs := resolvePath(p, cwd)
		if isTranscript(abs) {
			return dumpTranscript, abs, 0, 0
		}
		if isLokCatalog(abs, cwd) {
			return dumpCatalog, abs, 0, 0
		}
		n, ok := lineCount(abs)
		if !ok {
			continue
		}
		printed := n
		if limit >= 0 && limit < n {
			printed = limit
		}
		total += printed
		if total > maxDumpLines && file == "" {
			file, lines = abs, n
		}
	}
	if total > maxDumpLines {
		return dumpOverBudget, file, lines, total
	}
	return dumpOK, "", 0, 0
}

// addSedRange folds one `A,Bp` / `Ap` / `A,$p` expression into a line budget;
// `$` means "the rest", which the caller caps at the file length.
func addSedRange(limit int, expr string) int {
	for _, part := range strings.Split(expr, ";") {
		m := reSedRange.FindStringSubmatch(strings.TrimSpace(part))
		if m == nil {
			continue
		}
		switch {
		case m[1] == "" || (strings.Contains(part, ",") && m[2] == ""):
			return maxDumpLines + 1 // `$p` or `A,$p`: rest of file
		case m[2] == "":
			limit++
		default:
			a, b := atoiOr(m[1], 0), atoiOr(m[2], 0)
			if b >= a {
				limit += b - a + 1
			}
		}
	}
	return limit
}

func atoiOr(s string, fallback int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return fallback
}

func isDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != ""
}

// contextBashMatch is the pure decision for the Bash rules — unit-testable.
func contextBashMatch(cmd, cwd string) *Denial {
	if cmd == "" {
		return nil
	}
	allowWrite := escapeHatch(cmd, "CLAUDE_ALLOW_SHELL_EDIT")
	allowRead := escapeHatch(cmd, "CLAUDE_ALLOW_CONTEXT_DUMP")

	if !allowWrite && reInlineScript.MatchString(cmd) && reScriptWrites.MatchString(cmd) {
		return deny("context:inline-script-write", inlineScriptMsg, contextWriteEscape)
	}
	for _, seg := range dumpSegments(cmd) {
		if !allowWrite {
			if target := overwriteTarget(seg.text); target != "" {
				abs := resolvePath(target, cwd)
				if !throwawayPath(abs) {
					if st, err := os.Stat(abs); err == nil && st.Mode().IsRegular() {
						return deny("context:heredoc-overwrite", fmt.Sprintf(heredocOverwriteMsg, abs), contextWriteEscape)
					}
				}
			}
		}
		if allowRead || seg.consumed {
			continue
		}
		switch verdict, file, lines, total := dumpBudget(seg.text, cwd); verdict {
		case dumpTranscript:
			return deny("context:transcript-dump", fmt.Sprintf(transcriptMsg, file), contextReadEscape)
		case dumpCatalog:
			return catalogDenial(file)
		case dumpOverBudget:
			return deny("context:whole-file-dump", fmt.Sprintf(wholeFileMsg, total, maxDumpLines, file, lines), contextReadEscape)
		}
	}
	return nil
}

func overwriteTarget(seg string) string {
	if m := reCatRedirect.FindStringSubmatch(seg); m != nil {
		return m[1]
	}
	if strings.HasPrefix(seg, "tee ") && !strings.Contains(seg, " -a") && !strings.Contains(seg, "--append") {
		if m := reTeeWrite.FindStringSubmatch(seg); m != nil {
			return m[1]
		}
	}
	return ""
}

// contextReadMatch is the pure decision for the Read-tool rules.
func contextReadMatch(path string, limit int, cwd string) *Denial {
	if path == "" {
		return nil
	}
	abs := resolvePath(path, cwd)
	if isTranscript(abs) {
		return deny("context:transcript-dump", fmt.Sprintf(transcriptMsg, abs), "")
	}
	if isLokCatalog(abs, cwd) {
		return catalogDenial(abs)
	}
	if limit > 0 || noLineBudget[strings.ToLower(filepath.Ext(abs))] {
		return nil
	}
	n, ok := lineCount(abs)
	if !ok || n <= maxDumpLines {
		return nil
	}
	return deny("context:whole-file-dump", fmt.Sprintf(readToolMsg, abs, n, maxDumpLines), "")
}

func guardContextBash(in *HookInput) *Denial { return contextBashMatch(in.ToolInput.Command, in.CWD) }
func guardContextRead(in *HookInput) *Denial {
	return contextReadMatch(in.ToolInput.FilePath, in.ToolInput.Limit, in.CWD)
}

const inlineScriptMsg = `An inline script (python/node heredoc, -c, -e) that writes files is the most
expensive way to edit: the old text, the new text AND the boilerplate all land
in context, and the harness cannot track the change, so the file is re-shown
in full the next time it is touched.

Edit the file with the Edit tool (exact old_string → new_string, one call per
hunk). Create a new file with the Write tool. For a throwaway script, Write it
under /tmp and run it by path.`

const heredocOverwriteMsg = `cat/tee > <existing file> rewrites the WHOLE file to change part of it:
  %s

The full content is paid for twice — once as the command, again when the file
is next read — and the harness loses track of what changed. Use the Edit tool
for each changed hunk instead. Appending (>>, tee -a) and /tmp targets are
not affected.`

const wholeFileMsg = `This read would put ~%d lines into context (budget: %d per call):
  %s (%d lines)

Read only the range you will act on:  sed -n '120,180p' <file>   ·   rg -n '<symbol>' <file>
Surveying many files? Delegate to an Explore agent and keep the conclusions,
not the dumps. Piped reads (| grep, | head) are never blocked.`

const readToolMsg = `%s has %d lines; a Read without offset/limit puts all of it into context
(budget: %d lines per call).

Pass offset + limit for the range you need, locate it first with Grep, or send
a multi-file survey to an Explore agent and keep only its conclusions.`

const transcriptMsg = `%s is a session transcript — megabytes of JSONL, mostly tool output you have
already seen. Dumping it is pure context waste.

Aggregate instead: jq/node scripts that print counts and sizes, piped through
| head. Reading it raw is never the answer.`
