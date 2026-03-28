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

const responseTimeout = 2 * time.Second // generous fixed timeout for waiting for GLM response

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

	// Drain any stale messages
	drain(responses)

	settle := 200 * time.Millisecond
	minSettle := 10 * time.Millisecond
	round := 1

	fmt.Println("=== GLM MIDI Settle Time Probe ===")
	fmt.Println("Each round: Vol- (wait for response) → settle delay → Vol+ (wait for response)")
	fmt.Println("Settle delay shrinks by 10% each round to find GLM's minimum inter-command gap")
	fmt.Println()

	for settle >= minSettle {
		fmt.Printf("--- Round %d | settle: %dms ---\n", round, settle.Milliseconds())

		// Step 1: Send Vol-
		fmt.Printf("  [%s] SEND Vol- (CC%d=127)\n", ts(), types.CCVolDown)
		t1 := time.Now()
		writer.SendCC(0, types.CCVolDown, 127, "probe")
		downMsgs := waitMessages(responses, 3, responseTimeout, t1)
		printMessages("Vol-", downMsgs, t1)

		// Settle delay
		fmt.Printf("  [%s] settling %dms...\n", ts(), settle.Milliseconds())
		time.Sleep(settle)

		// Step 2: Send Vol+
		fmt.Printf("  [%s] SEND Vol+ (CC%d=127)\n", ts(), types.CCVolUp)
		t2 := time.Now()
		writer.SendCC(0, types.CCVolUp, 127, "probe")
		upMsgs := waitMessages(responses, 3, responseTimeout, t2)
		printMessages("Vol+", upMsgs, t2)

		// Summary
		volDown := findVolume(downMsgs)
		volUp := findVolume(upMsgs)
		fmt.Printf("  RESULT: Vol-=%d (%d msgs) | Vol+=%d (%d msgs)",
			volDown, len(downMsgs), volUp, len(upMsgs))
		if volDown < 0 || volUp < 0 {
			fmt.Printf("  *** MISSED at settle=%dms ***", settle.Milliseconds())
		}
		fmt.Println()

		// Settle before next round (same gap between Vol+ response and next Vol-)
		fmt.Printf("  [%s] settling %dms before next round...\n", ts(), settle.Milliseconds())
		time.Sleep(settle)
		fmt.Println()

		settle = time.Duration(float64(settle) * 0.9)
		round++
	}

	fmt.Println("=== Probe complete ===")
	return nil
}

// waitMessages waits for up to maxMsgs messages within the timeout.
func waitMessages(ch chan msg, maxMsgs int, timeout time.Duration, sendTime time.Time) []timedMsg {
	var result []timedMsg
	deadline := time.After(timeout)

	for len(result) < maxMsgs {
		select {
		case m := <-ch:
			result = append(result, timedMsg{
				msg:     m,
				elapsed: time.Since(sendTime),
			})
		case <-deadline:
			return result
		}
	}
	return result
}

type timedMsg struct {
	msg
	elapsed time.Duration
}

func printMessages(label string, msgs []timedMsg, sendTime time.Time) {
	if len(msgs) == 0 {
		fmt.Printf("    %s: (no response within %s)\n", label, responseTimeout)
		return
	}
	for i, m := range msgs {
		ccName := types.CCNames[m.cc]
		if ccName == "" {
			ccName = fmt.Sprintf("CC%d", m.cc)
		}
		fmt.Printf("    %s recv[%d]: %-8s (CC%d) = %-3d  @ +%dms\n",
			label, i+1, ccName, m.cc, m.value, m.elapsed.Milliseconds())
	}
}

func findVolume(msgs []timedMsg) int {
	for _, m := range msgs {
		if m.cc == types.CCVolumeAbs {
			return m.value
		}
	}
	return -1
}

func drain(ch chan msg) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

func ts() string {
	return time.Now().Format("15:04:05.000")
}
