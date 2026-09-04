package cli

import (
	"bytes"
	"testing"
	"time"

	"github.com/serhiileniv/every/internal/paths"
	"github.com/serhiileniv/every/internal/schedule"
	"github.com/serhiileniv/every/internal/ui"
)

// stubBackend stands in for a real scheduler.
//
// Every test that MUTATES a task uses one. Driving the real binary for those
// registers a genuine launchd agent on whoever runs the suite -- which happened
// twice before it was noticed, once in the surface generator and once here.
// The live lifecycle belongs to test/e2e, which cleans up and asserts it did.
type stubBackend struct {
	units     map[string]string
	writeErr  error
	enableErr error
}

func newStubBackend() *stubBackend { return &stubBackend{units: map[string]string{}} }

func (b *stubBackend) Write(name string, s *schedule.Schedule) error {
	if b.writeErr != nil {
		return b.writeErr
	}
	b.units[name] = s.Raw
	return nil
}
func (b *stubBackend) Enable(name string) error { return b.enableErr }
func (b *stubBackend) Disable(string) error     { return nil }
func (b *stubBackend) DeleteUnits(name string) error {
	delete(b.units, name)
	return nil
}
func (b *stubBackend) Loaded(name string) bool { _, ok := b.units[name]; return ok }
func (b *stubBackend) LoadedNames() ([]string, error) {
	out := make([]string, 0, len(b.units))
	for n := range b.units {
		out = append(out, n)
	}
	return out, nil
}
func (b *stubBackend) ResourceExists(name string) bool { _, ok := b.units[name]; return ok }
func (b *stubBackend) UnitPath(name string) string     { return "/stub/" + name }
func (b *stubBackend) Name() string                    { return "stub" }
func (b *stubBackend) Render(name string, s *schedule.Schedule) (string, error) {
	return "unit:" + name + ":" + s.Raw, nil
}

// stubCLI builds a CLI wired to a stub scheduler and an isolated store.
func stubCLI(t *testing.T) (*CLI, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	out := &bytes.Buffer{}
	return &CLI{
		Dirs: paths.Dirs{
			Data: dir, Logs: dir + "/logs", Runs: dir + "/runs",
			Config: dir + "/config", Agents: dir + "/agents",
		},
		Stdout:  out,
		Stderr:  &bytes.Buffer{},
		Color:   ui.Color{Enabled: false},
		Backend: newStubBackend(),
		Now:     time.Now,
	}, out
}
