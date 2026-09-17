// Command runholder holds a task's run lock until it is killed, for the test
// that a dead run never leaves a task looking like it is still running.
package main

import (
	"fmt"
	"os"
	"time"

	"github.com/serhiileniv/every/internal/store"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: runholder <data-dir> <task-name>")
		os.Exit(2)
	}
	if _, err := store.HoldRun(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, "hold:", err)
		os.Exit(1)
	}
	fmt.Println("held")
	time.Sleep(time.Hour)
}
