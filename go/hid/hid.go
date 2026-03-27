package hid

import (
	"context"
	"vol20toglm/types"
)

// Reader reads HID events from a USB device and sends actions to a channel.
type Reader interface {
	Run(ctx context.Context, actions chan<- types.Action) error
}
