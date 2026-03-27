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

	// Test 1: CC21, wait, 200ms settle, CC22, wait
	fmt.Println("=== Test 1: CC21 (Vol+) — 200ms settle — CC22 (Vol-) ===")
	fmt.Println("Sending CC21 (Vol+)...")
	writer.SendCC(0, types.CCVolUp, 127)
	vol1 := waitForVolume(responses, 1*time.Second)
	fmt.Printf("  Volume after Vol+: %d\n", vol1)

	fmt.Println("  (settling 200ms...)")
	time.Sleep(200 * time.Millisecond)

	fmt.Println("Sending CC22 (Vol-)...")
	writer.SendCC(0, types.CCVolDown, 127)
	vol2 := waitForVolume(responses, 1*time.Second)
	fmt.Printf("  Volume after Vol-: %d\n", vol2)
	if vol1 >= 0 && vol2 >= 0 {
		fmt.Printf("  Net change: %d (should be 0)\n", vol2-(vol1-1))
	}

	fmt.Println("  (settling 200ms...)")
	time.Sleep(200 * time.Millisecond)

	// Test 2: CC21, wait, 200ms settle, CC20 absolute restore
	fmt.Println("\n=== Test 2: CC21 (Vol+) — 200ms settle — CC20 (restore) ===")
	fmt.Println("Sending CC21 (Vol+)...")
	writer.SendCC(0, types.CCVolUp, 127)
	vol3 := waitForVolume(responses, 1*time.Second)
	fmt.Printf("  Volume after Vol+: %d\n", vol3)

	fmt.Println("  (settling 200ms...)")
	time.Sleep(200 * time.Millisecond)

	restore := vol3 - 1
	fmt.Printf("Sending CC20 (Volume=%d) to restore...\n", restore)
	writer.SendCC(0, types.CCVolumeAbs, restore)
	vol4 := waitForVolume(responses, 1*time.Second)
	fmt.Printf("  Volume after restore: %d (expected %d)\n", vol4, restore)

	fmt.Println("  (settling 200ms...)")
	time.Sleep(200 * time.Millisecond)

	// Test 3: Just CC21 alone — does it always trigger a response?
	fmt.Println("\n=== Test 3: CC21 (Vol+) x3 — reliability check ===")
	for i := 1; i <= 3; i++ {
		fmt.Printf("Sending CC21 #%d...\n", i)
		writer.SendCC(0, types.CCVolUp, 127)
		v := waitForVolume(responses, 1*time.Second)
		fmt.Printf("  Volume: %d\n", v)
		time.Sleep(200 * time.Millisecond)
	}

	// Restore: send 3x CC22 to undo
	fmt.Println("\nRestoring: 3x CC22 (Vol-)...")
	for i := 0; i < 3; i++ {
		writer.SendCC(0, types.CCVolDown, 127)
		waitForVolume(responses, 1*time.Second)
		time.Sleep(200 * time.Millisecond)
	}
	fmt.Println("Done.")

	return nil
}

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
