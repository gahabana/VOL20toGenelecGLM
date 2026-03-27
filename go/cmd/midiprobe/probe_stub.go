//go:build !windows

package main

import "fmt"

func run() error {
	fmt.Println("MIDI probe only works on Windows")
	return nil
}
