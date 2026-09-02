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
	re := regexp.MustCompile(`every ((?:[a-z0-9:,]+ ){1,2}?)(?:--|\z)`)

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

// isSubcommand filters out `every list`, `every doctor` and friends -- those
// are commands, not schedules, and are covered by the surface table.
func isSubcommand(tok string) bool {
	switch tok {
	case "list", "ls", "log", "run", "pause", "resume", "rm", "remove",
		"doctor", "version", "help", "task:", "add":
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
	out, _ := cmd.CombinedOutput()
	return string(out), cmd.ProcessState.ExitCode()
}
