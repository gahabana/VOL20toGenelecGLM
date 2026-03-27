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

type msg struct{ cc, value int }

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

	responses := make(chan msg, 20)

	go reader.Start(func(channel, cc, value int) {
		responses <- msg{cc, value}
	})
	time.Sleep(100 * time.Millisecond)

	// Test 1: CC21 (Vol+), wait for full response, then CC22 (Vol-), wait for response
	fmt.Println("=== Test 1: CC21 (Vol+) — wait for response — then CC22 (Vol-) ===")
	fmt.Println("Sending CC21 (Vol+)...")
	writer.SendCC(0, types.CCVolUp, 127)
	vol1 := waitForVolume(responses, 1*time.Second)
	fmt.Printf("  Volume after Vol+: %d\n", vol1)

	fmt.Println("Sending CC22 (Vol-)...")
	writer.SendCC(0, types.CCVolDown, 127)
	vol2 := waitForVolume(responses, 1*time.Second)
	fmt.Printf("  Volume after Vol-: %d\n", vol2)

	fmt.Printf("  Net change: %d (should be 0)\n", vol2-vol1+1)

	// Test 2: CC21 (Vol+), wait, then CC20 (absolute) to restore
	fmt.Println("\n=== Test 2: CC21 (Vol+) — wait — CC20 (restore) ===")
	fmt.Println("Sending CC21 (Vol+)...")
	writer.SendCC(0, types.CCVolUp, 127)
	vol3 := waitForVolume(responses, 1*time.Second)
	fmt.Printf("  Volume after Vol+: %d\n", vol3)

	restore := vol3 - 1
	fmt.Printf("Sending CC20 (Volume=%d) to restore...\n", restore)
	writer.SendCC(0, types.CCVolumeAbs, restore)
	vol4 := waitForVolume(responses, 1*time.Second)
	fmt.Printf("  Volume after restore: %d\n", vol4)

	fmt.Printf("  Net change: %d (should be 0)\n", vol4-vol3+1)

	return nil
}

// waitForVolume reads responses until CC20 (Volume) arrives or timeout.
// Prints all received messages. Returns the volume value, or -1 on timeout.
func waitForVolume(ch chan msg, timeout time.Duration) int {
	deadline := time.After(timeout)
	for {
		select {
		case m := <-ch:
			name := types.CCNames[m.cc]
			if name == "" {
				name = fmt.Sprintf("CC%d", m.cc)
			}
			fmt.Printf("    recv: %-8s (CC%d) = %d\n", name, m.cc, m.value)
			if m.cc == types.CCVolumeAbs {
				return m.value
			}
		case <-deadline:
			fmt.Println("    (timeout — no volume response)")
			return -1
		}
	}
}
