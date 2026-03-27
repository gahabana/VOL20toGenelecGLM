package rdp

// Primer handles RDP session priming to prevent GLM high-CPU issues.
type Primer interface {
	NeedsPriming() bool
	Prime() error
}
