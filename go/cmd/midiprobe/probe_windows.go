//go:build windows

package main

import (
	"fmt"
	"log/slog"
	"os"
	"time"

	"vol20toglm/midi"
	"vol20toglm/types"
)

func run() error {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	writer, err := midi.OpenWinMMWriter("GLMMIDI", log)
	if err != nil {
		return fmt.Errorf("open MIDI output: %w", err)
	}
	defer writer.Close()

	reader, err := midi.OpenWinMMReader("GLMOUT", log)
	if err != nil {
		return fmt.Errorf("open MIDI input: %w", err)
	}
	defer reader.Close()

	type msg struct{ cc, value int }
	responses := make(chan msg, 20)

	go reader.Start(func(channel, cc, value int) {
		responses <- msg{cc, value}
	})
	time.Sleep(100 * time.Millisecond)

	// Test 1: Send CC99 (unused) — does GLM respond?
	fmt.Println("=== Test 1: CC99 (unused) ===")
	writer.SendCC(0, 99, 127)
	printResponses(responses, 1*time.Second)

	// Test 2: Send CC21+CC22 (Vol+/Vol-) — known to trigger state burst
	fmt.Println("\n=== Test 2: CC21 (Vol+) + CC22 (Vol-) ===")
	writer.SendCC(0, types.CCVolUp, 127)
	time.Sleep(50 * time.Millisecond)
	writer.SendCC(0, types.CCVolDown, 127)
	printResponses(responses, 1*time.Second)

	return nil
}

func printResponses(ch chan struct{ cc, value int }, timeout time.Duration) {
	deadline := time.After(timeout)
	count := 0
	for {
		select {
		case m := <-ch:
			name := types.CCNames[m.cc]
			if name == "" {
				name = fmt.Sprintf("CC%d", m.cc)
			}
			fmt.Printf("  Received: %-8s (CC%d) = %d\n", name, m.cc, m.value)
			count++
		case <-deadline:
			if count == 0 {
				fmt.Println("  (no response)")
			}
			return
		}
	}
}
