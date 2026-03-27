package hid

// AccelerationHandler provides speed-dependent volume acceleration.
// When the user rotates the knob quickly, volume changes faster.
type AccelerationHandler struct {
	minClick       float64 // Min time between clicks to consider separate (seconds)
	maxPerClickAvg float64 // Max average time per click for acceleration
	volumeList     []int   // Volume increments per acceleration level
	listLen        int

	lastButton int
	lastTime   float64
	firstTime  float64
	count      int
}

// NewAccelerationHandler creates a handler with the given acceleration curve.
func NewAccelerationHandler(minClick, maxPerClickAvg float64, volumeList []int) *AccelerationHandler {
	return &AccelerationHandler{
		minClick:       minClick,
		maxPerClickAvg: maxPerClickAvg,
		volumeList:     volumeList,
		listLen:        len(volumeList),
		count:          1,
	}
}

// CalculateSpeed returns the volume delta based on rotation speed.
// button identifies the direction (to detect direction changes).
func (a *AccelerationHandler) CalculateSpeed(currentTime float64, button int) int {
	deltaTime := currentTime - a.lastTime

	var avgStepTime float64
	if a.count > 0 {
		avgStepTime = (currentTime - a.firstTime) / float64(a.count)
	} else {
		avgStepTime = 1e9 // effectively infinity
	}

	var distance int

	if a.lastButton != button || avgStepTime > a.maxPerClickAvg || deltaTime > a.minClick {
		// Reset: direction change, too slow on average, or long gap
		distance = 1
		a.count = 1
		a.firstTime = currentTime
	} else {
		// Accelerate: 1st click always resets to 1, then list[count] from 2nd click.
		// list[0] = 2nd click delta, list[1] = 3rd click delta, etc.
		// Beyond list length, last element repeats (caps acceleration).
		idx := a.count
		if idx >= a.listLen {
			idx = a.listLen - 1
		}
		distance = a.volumeList[idx]
		a.count++
	}

	a.lastButton = button
	a.lastTime = currentTime
	return distance
}
