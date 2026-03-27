// midiprobe sends CC21 (Vol+) then CC22 (Vol-) and prints GLM's response.
// Usage: go run ./cmd/midiprobe
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Println("MIDI Probe — send Vol+/Vol- to discover GLM state")
	fmt.Println("This tool only works on Windows (MIDI syscalls)")
	fmt.Println()

	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
