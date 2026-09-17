package cli

// SurfaceCases is every invocation the frozen table replays.
//
// The list lives in code and the captured answers live in
// testdata/golden/cli/cli.json, regenerated with:
//
//	go test ./internal/cli/ -run Frozen -update
//
// It moved here when the Ruby tree was deleted. Until then the fixture was
// captured FROM the Ruby and proved the port faithful; now it is captured from
// this binary and proves only that today's behavior matches yesterday's. That
// is a weaker claim, and -update is therefore a decision rather than a step in
// fixing a red test -- regenerating to make a failure go away is how a baseline
// quietly stops meaning anything.
//
// Only invocations that never reach a scheduler belong here, so the table stays
// hermetic and replayable on any machine. The live lifecycle is test/e2e's job.
var SurfaceCases = [][]string{
	{},
	{"help"},
	{"-h"},
	{"--help"},
	{"version"},
	{"--version"},
	{"log"},
	{"rm"},
	{"remove"},
	{"pause"},
	{"resume"},
	{"run"},
	{"log", "nosuch"},
	{"rm", "nosuch"},
	{"pause", "nosuch"},
	{"resume", "nosuch"},
	{"run", "nosuch"},
	{"log", "-n", "5", "nosuch"},
	{"log", "nosuch", "-n", "5"},
	{"log", "-n", "0", "nosuch"},
	{"log", "-n", "notanumber", "nosuch"},
	{"list"},
	{"ls"},
	{"list", "--json"},
	{"ls", "--json"},
	{"list", "--json", "extra"},
	{"frobnicate"},
	{"15m"},
	{"day", "9am"},
	{"15m", "--"},
	{"banana", "--", "true"},
	{"0m", "--", "true"},
	{"day", "13pm", "--", "true"},
	{"15m", "--name", "--", "true"},
	{"15m", "--name", "--quiet", "--", "true"},
	{"15m", "--name=", "--", "true"},
	{"15m", "--name", "...", "--", "true"},
	{"15m", "--name", ".", "--", "true"},
	{"15m", "--name", "..", "--", "true"},
	{"15m", "--timeout", "--", "true"},
	{"15m", "--timeout", "0s", "--", "true"},
	{"15m", "--timeout", "5x", "--", "true"},
	{"15m", "--timeout=0s", "--", "true"},
	{"15m", "--timeout", "notaduration", "--", "true"},
	{"15m", "--timeout", "5", "--", "true"},
	{"15m", "--timeout", "-5m", "--", "true"},
	{"15m", "--timeout", "0", "--", "true"},
	{"15m", "--name", "/", "--", "true"},
	{"15m", "--name", "//", "--", "true"},
	{"15m", "--name", "---", "--", "true"},
	{"15m", "--name", " ", "--", "true"},
	{"15m", "--name", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "--", "true"},
	{"--", "true"},
	{"0s", "--", "true"},
	{"9s", "--", "true"},
	{"1d", "--", "true"},
	{"25h99", "--", "true"},
	{"mon", "8am", "--", "true"},
	{"day", "5s", "--", "true"},
	{"day", "9am,", "--", "true"},
	{"day", ",", "--", "true"},
	{"monthly", "32nd", "9am", "--", "true"},
	{"monthly", "0", "9am", "--", "true"},
	{"monthly", "1,,15", "9am", "--", "true"},
	{"monthly", "1st", "--", "true"},
	{"monthly", "1st", "9am", "6pm", "--", "true"},
	{"once", "--", "true"},
	{"once", "2020-01-01", "9am", "--", "true"},
	{"once", "2026-02-30", "9am", "--", "true"},
	{"once", "5s", "--", "true"},
	{"once", "1d", "--", "true"},
	{"once", "banana", "9am", "--", "true"},
	{"once", "9am", "6pm", "--", "true"},
	{"set", "once", "yesterday", "9am", "--name", "x", "--", "true"},
	{",monday", "10:00", "--", "true"},
	{"day", "9am", "6pm", "--", "true"},
	{"", "--", "true"},
	{"list", "--json"},
	{"--json", "list"},
	{"ls", "--json", "extra"},
	{"list", "extra"},
	{"ls"},
	{"log", "-n", "1", "nosuch"},
	{"log", "nosuch", "-n", "1"},
	{"log", "-n", "-1", "nosuch"},
	{"log", "-n", "999999", "nosuch"},
	{"log", "-n", "nosuch"},
	{"version", "extra"},
	{"help", "extra"},
	{"rm", "nosuch", "extra"},
	{"pause", "nosuch", "extra"},
	{"--nonsense"},
	{"-x"},
	{"--json"},

	// 0.5.0 additions. Every one of these was a usage error before the verb
	// existed, so their old answers are in the fixture too -- the table records
	// the change rather than hiding it.
	{"inspect"},
	{"inspect", "nosuch"},
	{"inspect", "nosuch", "--json"},
	{"exists"},
	{"exists", "nosuch"},
	{"set"},
	{"set", "15m"},
	{"set", "15m", "--", "true"},
	{"set", "banana", "--name", "x", "--", "true"},
	{"set", "15m", "--name", "", "--", "true"},
	{"schema"},
	{"schema", "list"},
	{"schema", "inspect"},
	{"schema", "error"},
	{"schema", "nosuch"},
	{"version", "--json"},
	{"list", "--json"},
	{"log", "nosuch", "--json"},
	{"rm", "nosuch", "--json"},
	{"pause", "nosuch", "--json"},
	{"resume", "nosuch", "--json"},
	{"run", "nosuch", "--json"},
	{"run", "nosuch", "--dry-run"},
	// doctor is deliberately absent, in both forms. Its output names the host's
	// scheduler and its session -- "launchd user session reachable (gui/501)"
	// on a Mac, systemd on Linux -- so a fixture captured on one platform can
	// never match another. It is inherently machine-specific, which is the same
	// reason scheduler-touching commands are excluded: this table has to be
	// replayable anywhere. doctor is covered by test/e2e on each platform,
	// against that platform's real scheduler.
	{"banana", "--", "true"},
	{"15m", "--timeout", "0s", "--json", "--", "true"},

	// 0.6.0: unknown flags are usage errors rather than silently ignored.
	// The old answers for these are in the fixture's history -- `list --jsn`
	// used to print the human table and exit 0, which is the bug.
	{"list", "--jsn"},
	{"list", "--verbose"},
	{"ls", "--jsno"},
	{"inspect", "nosuch", "--oops"},
	{"exists", "nosuch", "--oops"},
	{"rm", "--oops", "nosuch"},
	{"pause", "nosuch", "--oops"},
	{"resume", "nosuch", "--oops"},
	{"run", "nosuch", "--oops"},
	{"doctor", "--oops"},
	{"schema", "--oops"},
	{"version", "--oops"},
	{"log", "nosuch", "--oops"},
	{"15m", "--oopsie", "--", "true"},
	{"set", "15m", "--name", "x", "--oopsie", "--", "true"},
	{"list", "--jsn", "--json"},

	// -n is validated rather than falling back to 40.
	{"log", "nosuch", "-n", "abc"},
	{"log", "nosuch", "-n", "0"},
	{"log", "nosuch", "-n", "-1"},
	{"log", "nosuch", "-n"},

	// -h/--help from any subcommand, and the version aliases.
	{"log", "--help"},
	{"list", "-h"},
	{"rm", "--help"},
	{"doctor", "-h"},
	{"-V"},
	{"-v"},

	// Stray positionals are rejected too, not just stray flags. These four
	// were tolerated from 0.4 through 0.5 and are the widest break in 0.6.
	{"doctor", "extra"},
	{"inspect", "nosuch", "extra"},
	{"exists", "nosuch", "extra"},
	{"run", "nosuch", "extra"},
	{"schema", "list", "extra"},
	{"log", "nosuch", "extra"},
	{"list", "extra", "--json"},
	// ...except after `help`, which just helps.
	{"help", "log"},

	// 0.6.0 round 2: follow, suggestions, filtering.
	{"log", "nosuch", "-f", "--json"},
	{"log", "nosuch", "--follow", "--json"},
	{"list", "--failing"},
	{"list", "--failing", "--json"},
	{"lst"},
	{"lis"},
	{"reusme"},
	{"doctr"},
	// Ties and schedules must NOT be corrected: "rn" is one edit from both
	// "rm" and "run", and 15m is a correct invocation missing only its `--`.
	{"rn"},
	{"15m"},
	{"2h"},

	// Per-command help: `every help <cmd>` and `every <cmd> --help` are the
	// same page, aliases resolve, and an unknown topic is a usage error rather
	// than the whole manual printed as if it answered.
	{"help", "list"},
	{"help", "ls"},
	{"help", "log"},
	{"help", "run"},
	{"help", "rm"},
	{"help", "remove"},
	{"help", "doctor"},
	{"help", "inspect"},
	{"help", "show"},
	{"help", "exists"},
	{"help", "set"},
	{"help", "schema"},
	{"help", "pause"},
	{"help", "resume"},
	{"help", "version"},
	{"help", "help"},
	{"help", "schedules"},
	{"help", "lst"},
	{"help", "frobnicate"},
	{"help", "list", "extra"},
	{"run", "--help"},
	{"inspect", "--help"},
	{"set", "--help"},
	{"schema", "--help"},
	{"15m", "--help"},

	// --color is global, validated, and stripped before dispatch.
	{"list", "--color=never"},
	{"list", "--color", "never"},
	{"list", "--color=always"},
	{"list", "--color=bogus"},
	{"list", "--color"},
	{"version", "--color=never"},
}
