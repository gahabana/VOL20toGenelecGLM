package config

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config holds all application configuration.
type Config struct {
	// Discovery
	ListDevices bool // Print available HID devices and MIDI ports, then exit

	// Logging
	LogLevel    string
	LogFileName string

	// Click timing
	MinClickTime    float64
	MaxAvgClickTime float64

	// Volume acceleration
	VolumeIncreases []int

	// HID device
	VID uint16
	PID uint16

	// MIDI
	MIDIInChannel  string // Port to send commands TO GLM
	MIDIOutChannel string // Port to receive state FROM GLM

	// Startup
	StartupVolume *int // nil = query current volume

	// REST API
	APIPort int

	// MQTT
	MQTTBroker      string
	MQTTPort        int
	MQTTUser        string
	MQTTPass        string
	MQTTTopic       string
	MQTTHADiscovery bool

	// GLM Manager
	GLMManager   bool
	GLMPath      string
	GLMCPUGating bool

	// Startup automation
	RDPPriming   bool
	MIDIRestart  bool
	HighPriority bool // Set process priority to AboveNormal

	// UI automation mode
	NoUIAutomation bool // Disable all pixel reading and mouse click simulation
	Headless       bool // Enable UI automation for screen reading (verification, health monitoring)
	UIPower        bool // Use UI click for power instead of MIDI (requires --headless)

	// Debug
	DebugCaptures bool // Dump pixel captures to BMP files for inspection
}

// Parse parses CLI arguments and returns a Config with defaults applied.
func Parse(args []string) Config {
	fs := flag.NewFlagSet("vol20toglm", flag.ContinueOnError)

	cfg := Config{
		VolumeIncreases: []int{1, 1, 2, 2, 3},
	}

	fs.BoolVar(&cfg.ListDevices, "list", false, "List available HID devices and MIDI ports, then exit")

	fs.StringVar(&cfg.LogLevel, "log_level", "DEBUG", "Logging level: DEBUG, INFO, NONE")
	fs.StringVar(&cfg.LogFileName, "log_file_name", "vol20toglm.log", "Log file name")
	fs.Float64Var(&cfg.MinClickTime, "min_click_time", 0.2, "Min time between clicks (seconds)")
	fs.Float64Var(&cfg.MaxAvgClickTime, "max_avg_click_time", 0.15, "Max average click time for acceleration")

	var volumeList string
	fs.StringVar(&volumeList, "volume_increases_list", "1,1,2,2,3", "Comma-separated volume increments per acceleration level")

	var vidPid string
	fs.StringVar(&vidPid, "device", "0x07d7,0x0000", "HID device VID,PID in hex")

	fs.StringVar(&cfg.MIDIInChannel, "midi_in_channel", "GLMMIDI", "MIDI input port name (commands TO GLM)")
	fs.StringVar(&cfg.MIDIOutChannel, "midi_out_channel", "GLMOUT", "MIDI output port name (state FROM GLM)")

	var startupVolume int
	fs.IntVar(&startupVolume, "startup_volume", -1, "Startup volume (0-127), -1 to probe via Vol+/Vol- round-trip")

	fs.IntVar(&cfg.APIPort, "api_port", 8080, "REST API port (0 to disable)")

	fs.StringVar(&cfg.MQTTBroker, "mqtt_broker", "", "MQTT broker hostname (empty to disable)")
	fs.IntVar(&cfg.MQTTPort, "mqtt_port", 1883, "MQTT broker port")
	fs.StringVar(&cfg.MQTTUser, "mqtt_user", "", "MQTT username")
	fs.StringVar(&cfg.MQTTPass, "mqtt_pass", "", "MQTT password")
	fs.StringVar(&cfg.MQTTTopic, "mqtt_topic", "glm", "MQTT topic prefix")
	fs.BoolVar(&cfg.MQTTHADiscovery, "mqtt_ha_discovery", true, "Enable Home Assistant MQTT Discovery")
	noHADiscovery := fs.Bool("no_mqtt_ha_discovery", false, "Disable Home Assistant MQTT Discovery")

	fs.BoolVar(&cfg.GLMManager, "glm_manager", true, "Enable GLM process manager")
	noGLMManager := fs.Bool("no_glm_manager", false, "Disable GLM process manager")
	fs.StringVar(&cfg.GLMPath, "glm_path", `C:\Program Files (x86)\Genelec\GLMv5\GLMv5.exe`, "Path to GLM executable")
	fs.BoolVar(&cfg.GLMCPUGating, "glm_cpu_gating", true, "Wait for CPU idle before starting GLM")
	noGLMCPUGating := fs.Bool("no_glm_cpu_gating", false, "Disable CPU gating")

	fs.BoolVar(&cfg.RDPPriming, "rdp_priming", true, "Prime RDP session at startup (headless VM)")
	noRDPPriming := fs.Bool("no_rdp_priming", false, "Disable RDP session priming")
	fs.BoolVar(&cfg.MIDIRestart, "midi_restart", true, "Restart Windows MIDI service at startup")
	noMIDIRestart := fs.Bool("no_midi_restart", false, "Disable MIDI service restart")
	fs.BoolVar(&cfg.HighPriority, "high_priority", true, "Set process priority to AboveNormal")
	noHighPriority := fs.Bool("no_high_priority", false, "Disable elevated process priority")

	fs.BoolVar(&cfg.NoUIAutomation, "no_ui_automation", false, "Disable all pixel reading and mouse click simulation (MIDI-only power control)")
	fs.BoolVar(&cfg.Headless, "headless", false, "Enable UI automation for screen reading (verification, health monitoring)")
	fs.BoolVar(&cfg.UIPower, "ui_power", false, "Use UI click for power instead of MIDI (requires --headless)")
	fs.BoolVar(&cfg.DebugCaptures, "debug_captures", false, "Dump pixel captures to BMP files for inspection")

	if err := fs.Parse(args); err != nil {
		os.Exit(0)
	}

	// Parse volume increases list
	if volumeList != "" {
		cfg.VolumeIncreases = parseIntList(volumeList)
	}

	// Parse VID:PID
	cfg.VID, cfg.PID = parseVIDPID(vidPid)

	// Handle startup volume
	if startupVolume >= 0 && startupVolume <= 127 {
		sv := startupVolume
		cfg.StartupVolume = &sv
	}

	// Handle negation flags
	if *noHADiscovery {
		cfg.MQTTHADiscovery = false
	}
	if *noGLMManager {
		cfg.GLMManager = false
	}
	if *noGLMCPUGating {
		cfg.GLMCPUGating = false
	}
	if *noRDPPriming {
		cfg.RDPPriming = false
	}
	if *noMIDIRestart {
		cfg.MIDIRestart = false
	}
	if *noHighPriority {
		cfg.HighPriority = false
	}

	// Validate mutually exclusive / dependent flags
	if cfg.UIPower && !cfg.Headless {
		fmt.Fprintln(os.Stderr, "error: --ui_power requires --headless")
		os.Exit(1)
	}
	if cfg.NoUIAutomation && cfg.Headless {
		fmt.Fprintln(os.Stderr, "error: --no_ui_automation and --headless are mutually exclusive")
		os.Exit(1)
	}

	return cfg
}

func parseIntList(s string) []int {
	s = strings.Trim(s, "[] ")
	parts := strings.Split(s, ",")
	result := make([]int, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		v, err := strconv.Atoi(p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: invalid volume increment %q, using 0\n", p)
			v = 0
		}
		result = append(result, v)
	}
	return result
}

func parseVIDPID(s string) (uint16, uint16) {
	parts := strings.SplitN(s, ",", 2)
	if len(parts) != 2 {
		return 0x07d7, 0x0000
	}
	vid := parseHex(strings.TrimSpace(parts[0]))
	pid := parseHex(strings.TrimSpace(parts[1]))
	return uint16(vid), uint16(pid)
}

func parseHex(s string) int {
	s = strings.TrimPrefix(s, "0x")
	s = strings.TrimPrefix(s, "0X")
	v, err := strconv.ParseUint(s, 16, 16)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: invalid hex value %q, using 0\n", s)
		return 0
	}
	return int(v)
}
