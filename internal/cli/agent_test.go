package cli

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// withJSON inserts --json where it belongs: BEFORE the `--`, if there is one.
//
// Appending it blindly puts it after the separator, where it is part of the
// user's command and not a flag of ours -- which is correct behavior and was
// briefly mistaken for a bug in these tests.
func withJSON(argv []string) []string {
	for i, tok := range argv {
		if tok == "--" {
			out := append([]string{}, argv[:i]...)
			out = append(out, "--json")
			return append(out, argv[i:]...)
		}
	}
	return append(append([]string{}, argv...), "--json")
}

// run drives the built binary with an isolated store.
func runBin(t *testing.T, bin, home string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), "EVERY_HOME="+home, "NO_COLOR=1")
	var out, errB strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errB
	err := cmd.Run()
	return out.String(), errB.String(), exitCodeOf(cmd, err)
}

// The text and JSON forms of one command must agree about whether it failed.
//
// Otherwise a program and a person reading the same exit code reach opposite
// conclusions, and the exit code is the part every caller sees. Caught a real
// divergence: log --json returned an empty list and exit 0 where the text form
// reported "no logs yet" and exit 66.
func TestTextAndJSONAgreeOnExitCodes(t *testing.T) {
	bin := buildBinary(t)
	home := t.TempDir()

	if _, _, code := runBin(t, bin, home, "__seed", "neverran", "true"); code != 0 {
		t.Fatalf("seeding: exit %d", code)
	}

	cases := [][]string{
		{"log", "nosuch"},
		{"log", "neverran"},
		{"rm", "nosuch"},
		{"pause", "nosuch"},
		{"resume", "nosuch"},
		{"inspect", "nosuch"},
		{"banana", "--", "true"},
		{"15m", "--timeout", "0s", "--", "true"},
	}
	for _, argv := range cases {
		t.Run(strings.Join(argv, "_"), func(t *testing.T) {
			_, _, textCode := runBin(t, bin, t.TempDir(), argv...)
			_, _, jsonCode := runBin(t, bin, t.TempDir(), withJSON(argv)...)
			if textCode != jsonCode {
				t.Errorf("exit codes differ: text=%d json=%d", textCode, jsonCode)
			}
		})
	}
}

// Every failure under --json is a parseable object with a code, on stderr.
//
// stderr and not stdout, deliberately: stdout is the data channel, so
// `every list --json | jq` never has to defend against an error object turning
// up where an array was promised.
func TestJSONErrorsAreStructuredOnStderr(t *testing.T) {
	bin := buildBinary(t)

	cases := []struct {
		argv []string
		code string
		exit int
	}{
		{[]string{"log", "nosuch"}, CodeNoLogs, 66},
		{[]string{"rm", "nosuch"}, CodeNoSuchTask, 66},
		{[]string{"inspect", "nosuch"}, CodeNoSuchTask, 66},
		{[]string{"banana", "--", "true"}, CodeBadSchedule, 64},
		{[]string{"15m", "--timeout", "0s", "--", "true"}, CodeBadDuration, 64},
		{[]string{"15m", "--name", "...", "--", "true"}, CodeBadName, 64},
	}

	for _, tc := range cases {
		t.Run(strings.Join(tc.argv, "_"), func(t *testing.T) {
			stdout, stderr, exit := runBin(t, bin, t.TempDir(), withJSON(tc.argv)...)

			if exit != tc.exit {
				t.Errorf("exit = %d, want %d", exit, tc.exit)
			}
			if strings.TrimSpace(stdout) != "" {
				t.Errorf("stdout carried %q; errors belong on stderr", stdout)
			}

			var payload errorPayload
			if err := json.Unmarshal([]byte(strings.TrimSpace(stderr)), &payload); err != nil {
				t.Fatalf("stderr is not a JSON object: %v\n%s", err, stderr)
			}
			if payload.Error != tc.code {
				t.Errorf("error = %q, want %q", payload.Error, tc.code)
			}
			if payload.Message == "" {
				t.Error("message is empty; the human sentence must survive")
			}
		})
	}
}

// set adds when absent and updates in place when present, and an update keeps
// the creation time -- otherwise "how long has this been running" becomes
// unanswerable the first time a schedule is adjusted.
//
// In-process against a stub backend, NOT through the binary. Driving the real
// one registers a real launchd agent on whoever runs the tests: the first
// version of this test left two behind before it was noticed. Anything that
// mutates the scheduler belongs in test/e2e, which cleans up after itself and
// asserts it did.
func TestSetUpsertsAndPreservesCreatedAt(t *testing.T) {
	c, out := stubCLI(t)

	if code := c.Run([]string{"set", "15m", "--name", "up", "--quiet", "--json", "--", "echo v1"}); code != 0 {
		t.Fatalf("set: exit %d", code)
	}
	var first TaskView
	if err := json.Unmarshal(out.Bytes(), &first); err != nil {
		t.Fatalf("set --json: %v\n%s", err, out.String())
	}
	if first.Command != "echo v1" {
		t.Errorf("command = %q", first.Command)
	}

	out.Reset()
	if code := c.Run([]string{"set", "hourly", "--name", "up", "--quiet", "--json", "--", "echo v2"}); code != 0 {
		t.Fatalf("second set: exit %d", code)
	}
	var second TaskView
	if err := json.Unmarshal(out.Bytes(), &second); err != nil {
		t.Fatal(err)
	}
	if second.Command != "echo v2" || second.Schedule != "hourly" {
		t.Errorf("update did not take: %+v", second)
	}
	if second.CreatedAt != first.CreatedAt {
		t.Errorf("created_at changed on update: %q -> %q", first.CreatedAt, second.CreatedAt)
	}
}

// A failed set leaves the previous task intact rather than a half-updated one.
//
// This is the race the command exists to remove: rm-then-add leaves a window
// where the task does not exist, and if the second half fails it never comes
// back.
func TestFailedSetKeepsThePreviousTask(t *testing.T) {
	c, out := stubCLI(t)

	if code := c.Run([]string{"set", "15m", "--name", "keep", "--quiet", "--json", "--", "echo original"}); code != 0 {
		t.Fatalf("first set: exit %d", code)
	}

	// The scheduler now refuses everything.
	c.Backend.(*stubBackend).writeErr = errors.New("scheduler said no")

	out.Reset()
	if code := c.Run([]string{"set", "hourly", "--name", "keep", "--quiet", "--", "echo replacement"}); code == 0 {
		t.Fatal("set succeeded despite the scheduler refusing")
	}

	c.Backend.(*stubBackend).writeErr = nil
	out.Reset()
	if code := c.Run([]string{"inspect", "keep", "--json"}); code != 0 {
		t.Fatalf("the task vanished: exit %d", code)
	}
	var view TaskView
	if err := json.Unmarshal(out.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Command != "echo original" {
		t.Errorf("command = %q, want the original to survive", view.Command)
	}
}

// add still refuses a duplicate. Changing that would alter an invocation that
// already had a defined answer, and the refusal is a useful guard for a person.
func TestAddStillRefusesDuplicates(t *testing.T) {
	bin := buildBinary(t)
	home := t.TempDir()

	if _, _, code := runBin(t, bin, home, "__seed", "dupe", "true"); code != 0 {
		t.Fatal("seeding failed")
	}
	_, stderr, code := runBin(t, bin, home, "15m", "--name", "dupe", "--json", "--", "true")
	if code != 64 {
		t.Errorf("exit = %d, want 64", code)
	}
	var payload errorPayload
	if err := json.Unmarshal([]byte(strings.TrimSpace(stderr)), &payload); err != nil {
		t.Fatalf("not JSON: %s", stderr)
	}
	if payload.Error != CodeAlreadyExists {
		t.Errorf("error = %q, want %q", payload.Error, CodeAlreadyExists)
	}
}

// exists answers with an exit code and nothing else -- a program testing for
// absence should not have to discard a message about it.
func TestExistsIsSilent(t *testing.T) {
	bin := buildBinary(t)
	home := t.TempDir()
	runBin(t, bin, home, "__seed", "here", "true")

	stdout, _, code := runBin(t, bin, home, "exists", "here")
	if code != 0 || strings.TrimSpace(stdout) != "" {
		t.Errorf("exists on a present task: exit=%d stdout=%q", code, stdout)
	}
	stdout, _, code = runBin(t, bin, home, "exists", "gone")
	if code != 66 {
		t.Errorf("exists on an absent task: exit=%d, want 66", code)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Errorf("wrote to stdout: %q", stdout)
	}
}

// --dry-run resolves everything and executes nothing.
func TestDryRunExecutesNothing(t *testing.T) {
	bin := buildBinary(t)
	home := t.TempDir()
	marker := t.TempDir() + "/ran"

	runBin(t, bin, home, "__seed", "dry", "touch "+marker)

	stdout, _, code := runBin(t, bin, home, "run", "dry", "--dry-run", "--json")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	var payload runPayload
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.DryRun || payload.Plan == nil {
		t.Fatalf("no plan in %+v", payload)
	}
	if payload.Plan.Command != "touch "+marker {
		t.Errorf("command = %q", payload.Plan.Command)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("--dry-run executed the command")
	}
}

// log --json omits output unless asked, which is what keeps the response
// bounded and the default path off the .log file entirely.
func TestLogJSONOutputIsOptIn(t *testing.T) {
	bin := buildBinary(t)
	home := t.TempDir()
	runBin(t, bin, home, "__seed", "chatty", "echo hello-from-task")
	if _, _, code := runBin(t, bin, home, "run", "chatty"); code != 0 {
		t.Fatal("run failed")
	}

	stdout, _, _ := runBin(t, bin, home, "log", "chatty", "--json")
	if strings.Contains(stdout, "hello-from-task") {
		t.Errorf("output present without --with-output: %s", stdout)
	}

	stdout, _, _ = runBin(t, bin, home, "log", "chatty", "--json", "--with-output")
	if !strings.Contains(stdout, "hello-from-task") {
		t.Errorf("--with-output did not include it: %s", stdout)
	}
}

// The schema is generated from the types, so it cannot describe fields that do
// not exist -- which is the entire reason it is not hand-written.
func TestSchemaMatchesTheTypes(t *testing.T) {
	bin := buildBinary(t)

	stdout, _, code := runBin(t, bin, t.TempDir(), "schema", "inspect")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	var doc struct {
		Type       string         `json:"type"`
		Properties map[string]any `json:"properties"`
		Required   []string       `json:"required"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Type != "object" {
		t.Errorf("type = %q", doc.Type)
	}
	for _, field := range []string{"name", "schedule", "command", "status", "paused"} {
		if _, ok := doc.Properties[field]; !ok {
			t.Errorf("schema omits %q", field)
		}
	}
}

// --json after `--` belongs to the user's command, not to us. Getting this
// wrong would silently change what a scheduled task runs.
func TestJSONFlagAfterSeparatorIsNotOurs(t *testing.T) {
	c, out := stubCLI(t)

	if code := c.Run([]string{"set", "15m", "--name", "flagtest", "--quiet", "--", "echo", "--json"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	// Text output, because our --json was never given.
	if strings.HasPrefix(strings.TrimSpace(out.String()), "{") {
		t.Errorf("treated the command's --json as ours: %s", out.String())
	}

	out.Reset()
	if code := c.Run([]string{"inspect", "flagtest", "--json"}); code != 0 {
		t.Fatalf("inspect: exit %d", code)
	}
	var view TaskView
	if err := json.Unmarshal(out.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Command != "echo --json" {
		t.Errorf("command = %q, want %q", view.Command, "echo --json")
	}
}
