// Package midigate provides response-gated MIDI sending.
//
// GLM needs ~30ms after completing a state burst before accepting the next command.
// Gate ensures commands are spaced correctly by waiting for GLM's response before
// sending the next queued command. Volume commands coalesce (latest wins); mute/dim/power
// are queued individually since each toggle matters.
package midigate

import (
	"context"
	"log/slog"
	"time"

	"vol20toglm/midi"
	"vol20toglm/types"
)

const (
	SettleDelay     = 50 * time.Millisecond // gap after last burst message before next send
	ResponseTimeout = 2 * time.Second       // max wait for GLM state burst
	sendChSize      = 32                    // buffered so consumer never blocks
	recvChSize      = 10                    // buffered so MIDI callback never blocks
)

// Gate mediates MIDI sends to ensure GLM has time to process each command.
// Implements midi.Writer so it can replace the raw writer in the consumer.
type Gate struct {
	writer midi.Writer
	log    *slog.Logger
	sendCh chan cmd
	recvCh chan int // CC number received from GLM
}

type cmd struct {
	channel int
	cc      int
	value   int
	traceID string
}

// New creates a Gate wrapping the given MIDI writer.
func New(writer midi.Writer, log *slog.Logger) *Gate {
	return &Gate{
		writer: writer,
		log:    log,
		sendCh: make(chan cmd, sendChSize),
		recvCh: make(chan int, recvChSize),
	}
}

// SendCC queues a MIDI CC message for gated sending.
func (g *Gate) SendCC(channel, cc, value int, traceID string) error {
	g.sendCh <- cmd{channel, cc, value, traceID}
	return nil
}

// Close closes the underlying MIDI writer.
func (g *Gate) Close() error {
	return g.writer.Close()
}

// NotifyReceive informs the gate about an incoming MIDI message from GLM.
// Call this from the MIDI reader callback. Non-blocking.
func (g *Gate) NotifyReceive(cc int) {
	select {
	case g.recvCh <- cc:
	default:
	}
}

// Run is the main gate loop. Start as a goroutine.
func (g *Gate) Run(ctx context.Context) {
	var queue []cmd     // FIFO for mute/dim/power (each matters)
	var pendingVol *cmd // coalesced volume (latest wins)
	waiting := false

	var timeoutTimer *time.Timer
	var settleTimer *time.Timer
	var timeoutCh <-chan time.Time
	var settleCh <-chan time.Time

	for {
		select {
		case <-ctx.Done():
			if timeoutTimer != nil {
				timeoutTimer.Stop()
			}
			if settleTimer != nil {
				settleTimer.Stop()
			}
			return

		case c := <-g.sendCh:
			if !waiting {
				g.rawSend(c)
				waiting = true
				timeoutTimer = time.NewTimer(ResponseTimeout)
				timeoutCh = timeoutTimer.C
			} else {
				if c.cc == types.CCVolumeAbs {
					if pendingVol != nil {
						g.log.Debug("gate: coalescing volume",
							"old", pendingVol.value, "new", c.value, "trace_id", c.traceID)
					}
					pendingVol = &c
				} else {
					queue = append(queue, c)
				}
			}

		case cc := <-g.recvCh:
			if waiting && cc == types.CCVolumeAbs {
				// Volume is the last message in GLM's state burst
				if timeoutTimer != nil {
					timeoutTimer.Stop()
					timeoutCh = nil
				}
				if settleTimer != nil {
					settleTimer.Stop()
				}
				settleTimer = time.NewTimer(SettleDelay)
				settleCh = settleTimer.C
			}

		case <-settleCh:
			settleCh = nil
			waiting = false
			if g.sendNext(&queue, &pendingVol) {
				waiting = true
				timeoutTimer = time.NewTimer(ResponseTimeout)
				timeoutCh = timeoutTimer.C
			}

		case <-timeoutCh:
			timeoutCh = nil
			g.log.Warn("gate: response timeout, proceeding")
			waiting = false
			if g.sendNext(&queue, &pendingVol) {
				waiting = true
				timeoutTimer = time.NewTimer(ResponseTimeout)
				timeoutCh = timeoutTimer.C
			}
		}
	}
}

func (g *Gate) rawSend(c cmd) {
	if err := g.writer.SendCC(c.channel, c.cc, c.value, c.traceID); err != nil {
		g.log.Error("gate: MIDI send failed",
			"cc", c.cc, "value", c.value, "trace_id", c.traceID, "err", err)
	}
}

// sendNext sends the highest-priority queued command.
// Non-volume commands (mute/dim/power) go first, then coalesced volume.
func (g *Gate) sendNext(queue *[]cmd, pendingVol **cmd) bool {
	if len(*queue) > 0 {
		c := (*queue)[0]
		*queue = (*queue)[1:]
		g.rawSend(c)
		return true
	}
	if *pendingVol != nil {
		c := **pendingVol
		*pendingVol = nil
		g.rawSend(c)
		return true
	}
	return false
}
