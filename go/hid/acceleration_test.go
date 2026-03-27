package hid

import "testing"

func TestAcceleration_FirstClick_ReturnsOne(t *testing.T) {
	a := NewAccelerationHandler(0.2, 0.15, []int{1, 1, 2, 2, 3})
	delta := a.CalculateSpeed(1.0, 1)
	if delta != 1 {
		t.Errorf("first click: got delta %d, want 1", delta)
	}
}

func TestAcceleration_SlowClicks_NoAcceleration(t *testing.T) {
	a := NewAccelerationHandler(0.2, 0.15, []int{1, 1, 2, 2, 3})
	// Clicks 0.3s apart (> min_click 0.2s) should reset each time
	a.CalculateSpeed(1.0, 1)
	delta := a.CalculateSpeed(1.3, 1) // 0.3s gap > 0.2s min_click
	if delta != 1 {
		t.Errorf("slow click: got delta %d, want 1", delta)
	}
}

func TestAcceleration_FastClicks_Accelerates(t *testing.T) {
	a := NewAccelerationHandler(0.2, 0.15, []int{1, 1, 2, 2, 3})
	// Fast clicks: 0.05s apart, avg well under max_per_click_avg
	a.CalculateSpeed(1.0, 1)      // count=1, delta=1
	a.CalculateSpeed(1.05, 1)     // count=2, delta=volume_list[1]=1
	a.CalculateSpeed(1.10, 1)     // count=3, delta=volume_list[2]=2
	a.CalculateSpeed(1.15, 1)     // count=4, delta=volume_list[3]=2
	delta := a.CalculateSpeed(1.20, 1) // count=5, delta=volume_list[4]=3
	if delta != 3 {
		t.Errorf("fast clicks: got delta %d, want 3", delta)
	}
}

func TestAcceleration_DirectionChange_Resets(t *testing.T) {
	a := NewAccelerationHandler(0.2, 0.15, []int{1, 1, 2, 2, 3})
	// Build up speed in direction 1
	a.CalculateSpeed(1.0, 1)
	a.CalculateSpeed(1.05, 1)
	a.CalculateSpeed(1.10, 1)
	// Change direction — should reset
	delta := a.CalculateSpeed(1.15, 2)
	if delta != 1 {
		t.Errorf("direction change: got delta %d, want 1", delta)
	}
}

func TestAcceleration_BeyondListLength_UsesLastValue(t *testing.T) {
	a := NewAccelerationHandler(0.2, 0.15, []int{1, 2, 3})
	// 4 fast clicks — exceeds list length of 3
	a.CalculateSpeed(1.0, 1)
	a.CalculateSpeed(1.05, 1)
	a.CalculateSpeed(1.10, 1)
	delta := a.CalculateSpeed(1.15, 1) // count=4 > len=3, use last=3
	if delta != 3 {
		t.Errorf("beyond list: got delta %d, want 3", delta)
	}
}
