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

	// Drain any stale messages
	drain(responses)

	limit := 2000 * time.Millisecond
	minLimit := 10 * time.Millisecond
	round := 1

	fmt.Println("=== GLM MIDI Timing Probe ===")
	fmt.Println("Each round: Vol- then Vol+ (net zero change)")
	fmt.Println("Wait limit shrinks by 20% each round until GLM stops responding")
	fmt.Println()

	for limit >= minLimit {
		fmt.Printf("--- Round %d | wait limit: %dms ---\n", round, limit.Milliseconds())

		// Step 1: Send Vol-
		fmt.Printf("  [%s] SEND Vol- (CC%d=127)\n", ts(), types.CCVolDown)
		sendTime := time.Now()
		writer.SendCC(0, types.CCVolDown, 127, "probe")
		volDownMsgs := waitMessages(responses, 3, limit, sendTime)
		printMessages("Vol-", volDownMsgs, sendTime)

		// Step 2: Send Vol+
		fmt.Printf("  [%s] SEND Vol+ (CC%d=127)\n", ts(), types.CCVolUp)
		sendTime2 := time.Now()
		writer.SendCC(0, types.CCVolUp, 127, "probe")
		volUpMsgs := waitMessages(responses, 3, limit, sendTime2)
		printMessages("Vol+", volUpMsgs, sendTime2)

		// Summary
		gotDown := countVolume(volDownMsgs)
		gotUp := countVolume(volUpMsgs)
		fmt.Printf("  RESULT: Vol- volume_response=%v (%d msgs) | Vol+ volume_response=%v (%d msgs)\n",
			gotDown >= 0, len(volDownMsgs), gotUp >= 0, len(volUpMsgs))

		if gotDown < 0 || gotUp < 0 {
			fmt.Printf("  *** MISSED RESPONSE at limit=%dms ***\n", limit.Milliseconds())
		}
		fmt.Println()

		limit = time.Duration(float64(limit) * 0.8)
		round++
	}

	fmt.Println("=== Probe complete ===")
	return nil
}

// waitMessages waits for up to maxMsgs messages within the timeout.
// Returns as soon as maxMsgs are received or timeout expires.
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
		fmt.Printf("    %s: (no response)\n", label)
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

func countVolume(msgs []timedMsg) int {
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
