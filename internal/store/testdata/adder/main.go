// Command adder performs one locked read-modify-write of a store, for the
// cross-process locking test. It exists as a separate binary because the
// property under test only appears between processes.
package main

import (
	"fmt"
	"os"

	"github.com/serhiileniv/every/internal/schedule"
	"github.com/serhiileniv/every/internal/store"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: adder <data-dir> <task-name>")
		os.Exit(2)
	}
	dir, name := os.Args[1], os.Args[2]

	lock, err := store.AcquireLock(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "lock:", err)
		os.Exit(1)
	}
	defer lock.Close()

	s, err := store.Load(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "load:", err)
		os.Exit(1)
	}
	sched, err := schedule.Parse([]string{"15m"})
	if err != nil {
		fmt.Fprintln(os.Stderr, "parse:", err)
		os.Exit(1)
	}
	if err := s.Add(name, &store.Task{
		Cmd: "true", Schedule: sched.ToRecord(), Cwd: dir, Quiet: true,
	}); err != nil {
		fmt.Fprintln(os.Stderr, "add:", err)
		os.Exit(1)
	}
}
