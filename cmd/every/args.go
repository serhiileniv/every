package main

import "os"

// commandName is os.Args[0], factored out so tests can substitute one.
func commandName() string {
	if len(os.Args) == 0 {
		return "every"
	}
	return os.Args[0]
}
