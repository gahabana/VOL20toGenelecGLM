package config

import "testing"

func TestDefaults(t *testing.T) {
	cfg := Parse([]string{})

	if cfg.LogLevel != "DEBUG" {
		t.Errorf("LogLevel = %q, want DEBUG", cfg.LogLevel)
	}
	if cfg.MinClickTime != 0.2 {
		t.Errorf("MinClickTime = %f, want 0.2", cfg.MinClickTime)
	}
	if cfg.MaxAvgClickTime != 0.15 {
		t.Errorf("MaxAvgClickTime = %f, want 0.15", cfg.MaxAvgClickTime)
	}
	if cfg.VID != 0x07d7 {
		t.Errorf("VID = 0x%04x, want 0x07d7", cfg.VID)
	}
	if cfg.PID != 0x0000 {
		t.Errorf("PID = 0x%04x, want 0x0000", cfg.PID)
	}
	if cfg.MIDIInChannel != "GLMMIDI" {
		t.Errorf("MIDIInChannel = %q, want GLMMIDI", cfg.MIDIInChannel)
	}
	if cfg.MIDIOutChannel != "GLMOUT" {
		t.Errorf("MIDIOutChannel = %q, want GLMOUT", cfg.MIDIOutChannel)
	}
	if cfg.APIPort != 8080 {
		t.Errorf("APIPort = %d, want 8080", cfg.APIPort)
	}
	if cfg.MQTTTopic != "glm" {
		t.Errorf("MQTTTopic = %q, want glm", cfg.MQTTTopic)
	}
	if cfg.MQTTPort != 1883 {
		t.Errorf("MQTTPort = %d, want 1883", cfg.MQTTPort)
	}
	if !cfg.MQTTHADiscovery {
		t.Error("MQTTHADiscovery should default to true")
	}
	if !cfg.GLMManager {
		t.Error("GLMManager should default to true")
	}
	if !cfg.GLMCPUGating {
		t.Error("GLMCPUGating should default to true")
	}
	if !cfg.RDPPriming {
		t.Error("RDPPriming should default to true")
	}
	if !cfg.MIDIRestart {
		t.Error("MIDIRestart should default to true")
	}
	wantList := []int{1, 1, 2, 2, 3}
	if len(cfg.VolumeIncreases) != len(wantList) {
		t.Fatalf("VolumeIncreases len = %d, want %d", len(cfg.VolumeIncreases), len(wantList))
	}
	for i, v := range wantList {
		if cfg.VolumeIncreases[i] != v {
			t.Errorf("VolumeIncreases[%d] = %d, want %d", i, cfg.VolumeIncreases[i], v)
		}
	}
}

func TestOverrides(t *testing.T) {
	cfg := Parse([]string{
		"--log_level", "INFO",
		"--api_port", "9090",
		"--midi_in_channel", "TEST IN",
		"--mqtt_broker", "192.168.1.100",
		"--startup_volume", "79",
		"--no_glm_manager",
	})

	if cfg.LogLevel != "INFO" {
		t.Errorf("LogLevel = %q, want INFO", cfg.LogLevel)
	}
	if cfg.APIPort != 9090 {
		t.Errorf("APIPort = %d, want 9090", cfg.APIPort)
	}
	if cfg.MIDIInChannel != "TEST IN" {
		t.Errorf("MIDIInChannel = %q, want TEST IN", cfg.MIDIInChannel)
	}
	if cfg.MQTTBroker != "192.168.1.100" {
		t.Errorf("MQTTBroker = %q, want 192.168.1.100", cfg.MQTTBroker)
	}
	if cfg.StartupVolume == nil || *cfg.StartupVolume != 79 {
		t.Errorf("StartupVolume = %v, want 79", cfg.StartupVolume)
	}
	if cfg.GLMManager {
		t.Error("GLMManager should be false with --no_glm_manager")
	}
}
