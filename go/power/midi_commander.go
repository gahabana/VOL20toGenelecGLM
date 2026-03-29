package power

import (
	"fmt"
	"log/slog"

	"vol20toglm/midi"
	"vol20toglm/types"
)

// MIDICommander controls power via MIDI CC28 in Toggle mode.
// GLM 5.2.0 Toggle mode semantics: value=0 → OFF, value>0 (127) → ON.
// Requires GLM MIDI Settings to have Power set to "Toggle" (powerMessageType=0).
type MIDICommander struct {
	writer      midi.Writer
	midiChannel int
	log         *slog.Logger
}

// NewMIDICommander creates a MIDICommander that sends power commands on the
// given MIDI channel using the provided writer.
func NewMIDICommander(writer midi.Writer, midiChannel int, log *slog.Logger) *MIDICommander {
	return &MIDICommander{
		writer:      writer,
		midiChannel: midiChannel,
		log:         log,
	}
}

// PowerOn sends CC28=127 to turn speakers ON. Implements Commander.
func (mc *MIDICommander) PowerOn(traceID string) error {
	if mc.writer == nil {
		return fmt.Errorf("MIDI writer not available")
	}
	mc.log.Info("power ON via MIDI", "cc", types.CCPower, "value", 127, "trace_id", traceID)
	return mc.writer.SendCC(mc.midiChannel, types.CCPower, 127, traceID)
}

// PowerOff sends CC28=0 to turn speakers OFF. Implements Commander.
func (mc *MIDICommander) PowerOff(traceID string) error {
	if mc.writer == nil {
		return fmt.Errorf("MIDI writer not available")
	}
	mc.log.Info("power OFF via MIDI", "cc", types.CCPower, "value", 0, "trace_id", traceID)
	return mc.writer.SendCC(mc.midiChannel, types.CCPower, 0, traceID)
}
