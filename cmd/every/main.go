// Command every schedules anything on your computer, humanely.
//
// One static binary: no runtime to install, nothing to keep in sync. See
// GO-REWRITE.md for why that is the whole point of this tree.
package main

import (
	"fmt"
	"os"
	"time"

	"github.com/serhiileniv/every/internal/backend"
	"github.com/serhiileniv/every/internal/cli"
	"github.com/serhiileniv/every/internal/paths"
	"github.com/serhiileniv/every/internal/ui"
)

func main() {
	os.Exit(run())
}

// run exists so main can os.Exit without skipping anything that needs to
// happen first -- os.Exit does not run deferred functions.
func run() int {
	dirs, err := paths.Resolve(paths.OS)
	if err != nil {
		fmt.Fprintf(os.Stderr, "every: %s\n", err)
		return 1
	}

	launcher, err := launcherPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "every: %s\n", err)
		return 1
	}

	b, err := backend.Current(backend.Config{Dirs: dirs, Launcher: launcher})
	if err != nil {
		fmt.Fprintf(os.Stderr, "every: %s\n", err)
		return 1
	}

	c := &cli.CLI{
		Dirs:    dirs,
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Color:   ui.NewColor(os.Stdout, ui.OSEnv, ui.OSHasEnv),
		Backend: b,
		Now:     time.Now,
	}
	return c.Run(os.Args[1:])
}
