package paths

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A differential test against the implementation being replaced: feed both the
// same environments and require identical answers. This catches the cases a
// hand-written table would not think to include, and it is the strongest
// evidence available that the port did not drift.
//
// It skips when the Ruby tree or a Ruby interpreter is gone, which is the
// expected end state -- the point of the rewrite. Until then it runs.
func TestMatchesRubyImplementation(t *testing.T) {
	ruby, err := exec.LookPath("ruby")
	if err != nil {
		t.Skip("no ruby on PATH; the Ruby tree is what this port replaces")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "lib", "every.rb")); err != nil {
		t.Skip("Ruby tree removed; differential comparison no longer applies")
	}

	// No platform stub here, deliberately. Ruby decides its branch from
	// RUBY_PLATFORM and cannot be told otherwise across a process boundary, so
	// stubbing the Go side pointed the two implementations at different rules
	// and compared the results. Letting each answer for the host it is on makes
	// this test cover the Windows branches on Windows, which is where they
	// matter and where nothing else compares them.
	envs := []map[string]string{
		{},
		{"EVERY_HOME": "/custom/x"},
		{"EVERY_HOME": "/custom/x", "XDG_DATA_HOME": "/xdg"},
		{"EVERY_HOME": "~/some-home"},
		{"EVERY_HOME": "/a/b/../c/"},
		{"EVERY_HOME": "/trailing/slash/"},
		{"EVERY_HOME": "relative/dir"},
		{"XDG_DATA_HOME": "/xdg"},
		{"XDG_DATA_HOME": "/xdg/"},
		{"XDG_DATA_HOME": "relative/path"},
		{"XDG_DATA_HOME": ""},
		{"XDG_CONFIG_HOME": "/cfg"},
		{"XDG_CONFIG_HOME": "relative"},
		{"XDG_DATA_HOME": "/xdg", "XDG_CONFIG_HOME": "/cfg"},
	}

	payload, err := json.Marshal(envs)
	if err != nil {
		t.Fatal(err)
	}
	// Via a file, never argv -- see internal/tail for what argv does to a JSON
	// payload on Windows.
	in := filepath.Join(t.TempDir(), "envs.json")
	if err := os.WriteFile(in, payload, 0o644); err != nil {
		t.Fatal(err)
	}

	const script = `
$LOAD_PATH.unshift File.join(ARGV[0], "lib")
require "every"
require "json"
puts JSON.generate(JSON.parse(File.read(ARGV[1])).map { |e|
  { "data" => Every.resolve_data_dir(e), "config" => Every.resolve_config_dir(e) }
})
`
	cmd := exec.Command(ruby, "-e", script, root, in)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ruby: %v\n%s", err, out)
	}

	var want []struct{ Data, Config string }
	if err := json.Unmarshal(out, &want); err != nil {
		t.Fatalf("decoding ruby output %q: %v", strings.TrimSpace(string(out)), err)
	}
	if len(want) != len(envs) {
		t.Fatalf("ruby returned %d results for %d envs", len(want), len(envs))
	}

	for i, env := range envs {
		gotData, err := DataDir(MapEnv(env))
		if err != nil {
			t.Errorf("env %v: DataDir: %v", env, err)
			continue
		}
		gotCfg, err := ConfigDir(MapEnv(env))
		if err != nil {
			t.Errorf("env %v: ConfigDir: %v", env, err)
			continue
		}
		if gotData != want[i].Data {
			t.Errorf("env %v: DataDir = %q, ruby says %q", env, gotData, want[i].Data)
		}
		if gotCfg != want[i].Config {
			t.Errorf("env %v: ConfigDir = %q, ruby says %q", env, gotCfg, want[i].Config)
		}
	}
}
