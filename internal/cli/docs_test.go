package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Every schedule example that appears in the shipped documentation must still
// parse.
//
// The README, the man page and `every help` are promises. A user who copies a
// line out of one of them and finds it rejected has hit a regression, whatever
// the changelog says -- and documentation drifts from behavior silently, since
// nothing normally connects the two. This walks the actual files and checks
// each example against the real parser.
func TestDocumentedSchedulesAllParse(t *testing.T) {
	root := repoRoot(t)
	bin := buildBinary(t)

	files := []string{
		filepath.Join(root, "README.md"),
		filepath.Join(root, "man", "every.1"),
		filepath.Join(root, "ROADMAP.md"),
		filepath.Join(root, "DECISIONS.md"),
	}

	// `every <schedule tokens> --` or `every <schedule tokens> -- <cmd>`: the
	// tokens between the command name and the separator (or a flag) are what
	// the parser has to accept.
	// Up to three tokens: `once tomorrow 9am` and `monthly 1st 9am`. A
	// flag after the tokens still ends the match at its leading dashes.
	re := regexp.MustCompile(`every ((?:[a-z0-9:,]+ ){1,3}?)(?:--|\z)`)

	seen := map[string]bool{}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue // an optional doc; not every checkout has all of them
		}
		for _, m := range re.FindAllStringSubmatch(string(raw), -1) {
			tokens := strings.Fields(m[1])
			if len(tokens) == 0 || isSubcommand(tokens[0]) {
				continue
			}
			seen[strings.Join(tokens, " ")] = true
		}
	}

	// The help text ships inside the binary rather than a file, so it is read
	// from the binary itself -- which is also the only way to catch help
	// drifting from the parser.
	helpOut, err := exec.Command(bin, "help").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range re.FindAllStringSubmatch(string(helpOut), -1) {
		tokens := strings.Fields(m[1])
		if len(tokens) == 0 || isSubcommand(tokens[0]) {
			continue
		}
		seen[strings.Join(tokens, " ")] = true
	}

	if len(seen) < 8 {
		t.Fatalf("only extracted %d schedule examples (%v); the extractor is broken, not the docs",
			len(seen), keys(seen))
	}

	var examples []string
	for s := range seen {
		examples = append(examples, s)
	}
	sort.Strings(examples)

	for _, ex := range examples {
		t.Run(ex, func(t *testing.T) {
			cmd := exec.Command(bin, append([]string{"__parse"}, strings.Fields(ex)...)...)
			cmd.Env = append(os.Environ(), "EVERY_HOME="+t.TempDir())
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("a documented schedule is rejected: %q\n%s", ex, out)
			}
		})
	}
	t.Logf("checked %d documented schedule examples: %v", len(examples), examples)
}

// isSubcommand filters out `every list`, `every inspect` and friends -- those
// are commands, not schedules, and are covered by the surface table.
//
// Driven off Commands rather than its own list, which is how `every inspect
// backup --json` in the README came to be checked as a schedule: the local copy
// had not heard of the verbs 0.5.0 added.
func isSubcommand(tok string) bool {
	for _, c := range Commands {
		if tok == c {
			return true
		}
	}
	switch tok {
	case "task:", "add":
		return true
	}
	return strings.HasPrefix(tok, "-")
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Every subcommand the man page documents must exist, and every subcommand the
// binary accepts must be documented. Drift in either direction is a bug: an
// undocumented command is a promise nobody knows about, and a documented one
// that does not exist is a lie.
func TestManPageAndBinaryAgreeOnSubcommands(t *testing.T) {
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "man", "every.1"))
	if err != nil {
		t.Skipf("no man page: %v", err)
	}
	man := string(raw)

	documented := []string{"list", "log", "run", "pause", "resume", "rm", "doctor", "version", "help"}
	for _, cmd := range documented {
		if !strings.Contains(man, cmd) {
			t.Errorf("man page no longer mentions %q", cmd)
		}
	}

	bin := buildBinary(t)
	for _, cmd := range documented {
		// `help` and `version` take no arguments; the rest report a usage
		// error without one. Either way the command must be RECOGNISED --
		// what must not happen is it falling through to `add` and being
		// reported as "isn't a command".
		out, _ := runWithHome(t, bin, cmd)
		if strings.Contains(out, "isn't a command") {
			t.Errorf("documented subcommand %q is not recognised by the binary", cmd)
		}
	}
}

// The three completion scripts scrape task names out of `list --json` with a
// regex. They are shipped files that nothing else tests, and a change to the
// JSON shape breaks tab-completion silently -- the user just stops getting
// suggestions and never files a bug.
func TestCompletionScriptsCanScrapeTaskNames(t *testing.T) {
	root := repoRoot(t)
	bin := buildBinary(t)
	home := t.TempDir()

	for _, name := range []string{"alpha", "beta-two", "gamma.three"} {
		cmd := exec.Command(bin, "__seed", name, "true")
		cmd.Env = append(os.Environ(), "EVERY_HOME="+home)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("seeding %s: %v\n%s", name, err, out)
		}
	}

	cmd := exec.Command(bin, "list", "--json")
	cmd.Env = append(os.Environ(), "EVERY_HOME="+home, "NO_COLOR=1")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}

	// The exact expression all three scripts use, in their three dialects.
	// completions/every.bash:  grep -o '"name":"[^"]*"'
	// completions/_every:      the same pipeline
	// completions/every.fish:  string match -r '"name":"[^"]*"'
	re := regexp.MustCompile(`"name":"[^"]*"`)
	matches := re.FindAllString(string(out), -1)
	if len(matches) != 3 {
		t.Fatalf("the completion regex found %d names in:\n%s\nwant 3", len(matches), out)
	}

	var got []string
	for _, m := range matches {
		got = append(got, strings.TrimSuffix(strings.TrimPrefix(m, `"name":"`), `"`))
	}
	sort.Strings(got)
	want := []string{"alpha", "beta-two", "gamma.three"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("scraped %v, want %v", got, want)
		}
	}

	// And the shipped scripts must still contain that expression, so this test
	// fails if someone edits them apart from the format.
	for _, f := range []string{"every.bash", "_every", "every.fish"} {
		raw, err := os.ReadFile(filepath.Join(root, "completions", f))
		if err != nil {
			t.Errorf("reading %s: %v", f, err)
			continue
		}
		if !strings.Contains(string(raw), `"name":"[^"]*"`) {
			t.Errorf("%s no longer uses the scraping expression this test verifies", f)
		}
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func runWithHome(t *testing.T, bin string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), "EVERY_HOME="+t.TempDir(), "NO_COLOR=1")
	out, err := cmd.CombinedOutput()
	return string(out), exitCodeOf(cmd, err)
}

// Commands is every user-facing verb the dispatcher accepts.
//
// Duplicating the dispatch switch is the point: this list is the promise, the
// switch is the implementation, and a command added to one but not the other is
// the drift TestEverySurfaceDocumentsEveryCommand catches. The `__` test hooks
// are deliberately absent -- they are not user-facing and must stay
// undocumented.
var Commands = []string{
	"list", "ls", "log", "run", "pause", "resume", "rm", "remove",
	"doctor", "inspect", "show", "exists", "set", "schema", "version", "help",
}

// Every command must be recognised by the binary AND documented everywhere a
// user might look for it.
//
// This test exists because half of a 0.6.0 doc audit was one finding repeated:
// `set`, `inspect`, `exists` and `schema` shipped in 0.5.0 and reached none of
// the README, the completions, or (in places) the man page. Nothing failed,
// because nothing connected the dispatcher to the files that describe it.
func TestEverySurfaceDocumentsEveryCommand(t *testing.T) {
	root := repoRoot(t)
	bin := buildBinary(t)

	helpOut, err := exec.Command(bin, "help").Output()
	if err != nil {
		t.Fatal(err)
	}

	surfaces := map[string]string{
		"every help":             string(helpOut),
		"man/every.1":            readOrSkip(t, filepath.Join(root, "man", "every.1")),
		"README.md":              readOrSkip(t, filepath.Join(root, "README.md")),
		"completions/every.bash": readOrSkip(t, filepath.Join(root, "completions", "every.bash")),
		"completions/_every":     readOrSkip(t, filepath.Join(root, "completions", "_every")),
		"completions/every.fish": readOrSkip(t, filepath.Join(root, "completions", "every.fish")),
	}

	for _, cmd := range Commands {
		t.Run(cmd, func(t *testing.T) {
			// Recognised: it must not fall through to `add`.
			out, _ := runWithHome(t, bin, cmd)
			if strings.Contains(out, "isn't a command") {
				t.Errorf("%q is not recognised by the binary", cmd)
			}
			for surface, text := range surfaces {
				if !mentionsCommand(text, cmd) {
					t.Errorf("%s does not mention the %q command", surface, cmd)
				}
			}
		})
	}
}

// mentionsCommand looks for the verb as a whole word, so "run" is not satisfied
// by "running" and "ls" is not satisfied by "false".
//
// roff font escapes are stripped first: the man page writes `\fBls\fR`, where
// the B abutting the word defeats a \b boundary and the command reads as
// undocumented when it is not.
func mentionsCommand(text, cmd string) bool {
	text = roffEscapes.ReplaceAllString(text, " ")
	re := regexp.MustCompile(`(?m)\b` + regexp.QuoteMeta(cmd) + `\b`)
	return re.MatchString(text)
}

var roffEscapes = regexp.MustCompile(`\\f[BIRP]|\\\(em|\\-`)

func readOrSkip(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("a documented surface is missing: %s: %v", path, err)
	}
	return string(raw)
}

// Every command has a help page, and every page names a real command.
//
// The same drift TestEverySurfaceDocumentsEveryCommand catches for the shipped
// files, caught for the pages inside the binary. A command added to dispatch
// with no page is the failure mode: `every newverb --help` would silently fall
// through to the full help, which looks like it worked.
func TestEveryCommandHasAHelpTopic(t *testing.T) {
	for _, cmd := range Commands {
		if _, ok := helpTopicFor(cmd); !ok {
			t.Errorf("no help topic for the %q command", cmd)
		}
	}

	// And nothing documents a command that does not exist. "schedules" is the
	// one page that is not a command, deliberately -- the schedule DSL is the
	// part people actually need to look up.
	for _, topic := range helpTopicNames() {
		if topic == "schedules" {
			continue
		}
		found := false
		for _, cmd := range Commands {
			if topic == cmd {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("help topic %q is not a command", topic)
		}
	}
}

// A topic's page must actually be about that command: the synopsis names it.
// A copy-paste between pages is otherwise invisible.
func TestHelpTopicSynopsisNamesItsCommand(t *testing.T) {
	for _, cmd := range Commands {
		page, ok := helpTopicFor(cmd)
		if !ok {
			continue // reported by TestEveryCommandHasAHelpTopic
		}
		canonical := cmd
		if c, ok := suggestable[cmd]; ok {
			canonical = c
		}
		first := strings.SplitN(page, "\n", 2)[0]
		if !strings.Contains(first, "every "+canonical) {
			t.Errorf("the %q page opens with %q, which does not name the command", cmd, first)
		}
	}
}

// Every example inside a help page has to be a real invocation. A page that
// documents a flag the parser rejects is worse than no page.
func TestHelpTopicExamplesUseRealFlags(t *testing.T) {
	bin := buildBinary(t)

	for _, topic := range helpTopicNames() {
		page, _ := helpTopicFor(topic)
		for _, line := range strings.Split(page, "\n") {
			line = strings.TrimSpace(line)
			// Only the worked examples, not the flag table above them.
			if !strings.HasPrefix(line, "every ") || strings.Contains(line, "|") {
				continue
			}
			// Examples that would register a real task or need a live store
			// are not runnable here; their flags are still checked by the
			// surface table. Only --help-able forms are exercised.
			if strings.Contains(line, " -- ") {
				continue
			}
			t.Run(topic+"/"+line, func(t *testing.T) {
				args := append(strings.Fields(line)[1:], "--help")
				out, code := runWithHome(t, bin, args...)
				if code != 0 {
					t.Errorf("`%s` is documented but rejected (exit %d):\n%s", line, code, out)
				}
			})
		}
	}
}
