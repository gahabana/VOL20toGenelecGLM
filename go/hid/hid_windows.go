//go:build windows

package hid

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	usbhid "github.com/sstallion/go-hid"
	"vol20toglm/types"
)

const (
	readTimeout = 1 * time.Second
	retryDelay  = 500 * time.Millisecond
	reportSize  = 3
)

// USBReader reads HID events from a VOL20 USB device.
type USBReader struct {
	VID      uint16
	PID      uint16
	Bindings map[int]types.ActionKind
	Accel    *AccelerationHandler
	TraceGen *types.TraceIDGenerator
	Log      *slog.Logger
}

// Run opens the HID device and reads reports in a loop until ctx is cancelled.
func (r *USBReader) Run(ctx context.Context, actions chan<- types.Action) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		device, err := r.openDevice()
		if err != nil {
			r.Log.Warn("HID device not found, retrying",
				"vid", fmt.Sprintf("0x%04x", r.VID),
				"pid", fmt.Sprintf("0x%04x", r.PID),
				"err", err,
			)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(retryDelay):
				continue
			}
		}

		r.Log.Info("HID device connected",
			"vid", fmt.Sprintf("0x%04x", r.VID),
			"pid", fmt.Sprintf("0x%04x", r.PID),
		)

		err = r.readLoop(ctx, device, actions)
		device.Close()

		if ctx.Err() != nil {
			return ctx.Err()
		}

		r.Log.Warn("HID device disconnected, reconnecting", "err", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(retryDelay):
		}
	}
}

func (r *USBReader) openDevice() (*usbhid.Device, error) {
	if err := usbhid.Init(); err != nil {
		return nil, fmt.Errorf("hid init: %w", err)
	}

	device, err := usbhid.OpenFirst(r.VID, r.PID)
	if err != nil {
		return nil, fmt.Errorf("hid open: %w", err)
	}
	return device, nil
}

func (r *USBReader) readLoop(ctx context.Context, device *usbhid.Device, actions chan<- types.Action) error {
	buf := make([]byte, reportSize)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		numberOfBytesRead, err := device.ReadWithTimeout(buf, readTimeout)
		if err != nil {
			if errors.Is(err, usbhid.ErrTimeout) {
				continue // timeout with no data, check ctx and retry
			}
			return fmt.Errorf("hid read: %w", err)
		}
		if numberOfBytesRead == 0 {
			continue
		}

		keycode := int(buf[0])
		ProcessReport(keycode, time.Now(), r.Bindings, r.Accel, r.TraceGen, actions)
	}
}
