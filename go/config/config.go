package config

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"vol20toglm/version"
)

// Config holds all application configuration.
type Config struct {
	// Discovery
	ListDevices bool // Print available HID devices and MIDI ports, then exit

	// Operating mode
	Desktop     bool // Desktop mode: disables GLM manager, RDP priming, MIDI restart, elevated priority
	PixelVerify bool // Enable pixel reading for power state verification (opt-in)
	UIPower     bool // Use UI click for power instead of MIDI (implies PixelVerify)

	// Startup
	StartupVolume *int   // nil = discover from GLM startup burst
	StartupPower  string // "on" or "off"

	// HID device
	VID uint16
	PID uint16

	// MIDI
	MIDIInChannel  string // Port to send commands TO GLM
	MIDIOutChannel string // Port to receive state FROM GLM

	// REST API
	APIPort    int
	CORSOrigin string // CORS Allow-Origin header value ("*" = all, "" = disabled)

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

	// VM Automation
	RDPPriming   bool
	MIDIRestart  bool
	HighPriority bool // Set process priority to AboveNormal

	// Logging
	LogLevel    string
	LogFileName string

	// Volume acceleration
	MinClickTime    float64
	MaxAvgClickTime float64
	VolumeIncreases []int

	// Debug
	DebugCaptures bool // Dump pixel captures to BMP files for inspection
}

// Parse parses CLI arguments and returns a Config with defaults applied.
func Parse(args []string) Config {
	fs := flag.NewFlagSet("vol20toglm", flag.ContinueOnError)
	fs.Usage = func() { printUsage(os.Stderr) }

	cfg := Config{
		VolumeIncreases: []int{1, 1, 2, 2, 3},
	}

	// Operating mode
	fs.BoolVar(&cfg.Desktop, "desktop", false, "Desktop mode")
	fs.BoolVar(&cfg.PixelVerify, "pixel_verify", false, "Enable pixel reading for power state verification")
	fs.BoolVar(&cfg.UIPower, "ui_power", false, "Use UI click for power (implies --pixel_verify)")

	// Startup
	var startupVolume int
	fs.IntVar(&startupVolume, "startup_volume", -1, "Startup volume (0-127), -1 to discover from GLM startup burst")
	fs.StringVar(&cfg.StartupPower, "startup_power", "on", `Power state at startup: "on" or "off"`)

	// Devices & MIDI
	fs.BoolVar(&cfg.ListDevices, "list", false, "List available HID devices and MIDI ports, then exit")
	var vidPid string
	fs.StringVar(&vidPid, "device", "0x07d7,0x0000", "HID device VID,PID in hex")
	fs.StringVar(&cfg.MIDIInChannel, "midi_in_channel", "GLMMIDI", "MIDI input port name (commands TO GLM)")
	fs.StringVar(&cfg.MIDIOutChannel, "midi_out_channel", "GLMOUT", "MIDI output port name (state FROM GLM)")

	// REST API
	fs.IntVar(&cfg.APIPort, "api_port", 8080, "REST API port (0 to disable)")
	fs.StringVar(&cfg.CORSOrigin, "cors_origin", "*", "CORS Allow-Origin header (empty to disable)")

	// MQTT / Home Assistant
	fs.StringVar(&cfg.MQTTBroker, "mqtt_broker", "", "MQTT broker hostname (empty to disable)")
	fs.IntVar(&cfg.MQTTPort, "mqtt_port", 1883, "MQTT broker port")
	fs.StringVar(&cfg.MQTTUser, "mqtt_user", "", "MQTT username")
	fs.StringVar(&cfg.MQTTPass, "mqtt_pass", "", "MQTT password")
	fs.StringVar(&cfg.MQTTTopic, "mqtt_topic", "glm", "MQTT topic prefix")
	fs.BoolVar(&cfg.MQTTHADiscovery, "mqtt_ha_discovery", true, "Enable Home Assistant MQTT Discovery")
	noHADiscovery := fs.Bool("no_mqtt_ha_discovery", false, "Disable Home Assistant MQTT Discovery")

	// GLM Process Manager
	fs.BoolVar(&cfg.GLMManager, "glm_manager", true, "Enable GLM process manager")
	noGLMManager := fs.Bool("no_glm_manager", false, "Disable GLM process manager")
	fs.StringVar(&cfg.GLMPath, "glm_path", `C:\Program Files (x86)\Genelec\GLMv5\GLMv5.exe`, "Path to GLM executable")
	fs.BoolVar(&cfg.GLMCPUGating, "glm_cpu_gating", true, "Wait for CPU idle before starting GLM")
	noGLMCPUGating := fs.Bool("no_glm_cpu_gating", false, "Disable CPU gating")

	// VM Automation
	fs.BoolVar(&cfg.RDPPriming, "rdp_priming", true, "Prime RDP session at startup (headless VM)")
	noRDPPriming := fs.Bool("no_rdp_priming", false, "Disable RDP session priming")
	fs.BoolVar(&cfg.MIDIRestart, "midi_restart", true, "Restart Windows MIDI service at startup")
	noMIDIRestart := fs.Bool("no_midi_restart", false, "Disable MIDI service restart")
	fs.BoolVar(&cfg.HighPriority, "high_priority", true, "Set process priority to AboveNormal")
	noHighPriority := fs.Bool("no_high_priority", false, "Disable elevated process priority")

	// Volume Acceleration
	var volumeList string
	fs.StringVar(&volumeList, "volume_increases_list", "1,1,2,2,3", "Comma-separated volume increments per acceleration level")
	fs.Float64Var(&cfg.MinClickTime, "min_click_time", 0.2, "Min time between clicks (seconds)")
	fs.Float64Var(&cfg.MaxAvgClickTime, "max_avg_click_time", 0.15, "Max average click time for acceleration")

	// Logging & Debug
	fs.StringVar(&cfg.LogLevel, "log_level", "DEBUG", "Logging level: DEBUG, INFO, NONE")
	fs.StringVar(&cfg.LogFileName, "log_file_name", "vol20toglm.log", "Log file name")
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

	// Apply --desktop shorthand
	if cfg.Desktop {
		cfg.GLMManager = false
		cfg.RDPPriming = false
		cfg.MIDIRestart = false
		cfg.HighPriority = false
	}

	// --ui_power implies --pixel_verify
	if cfg.UIPower {
		cfg.PixelVerify = true
	}

	// Validate
	if cfg.Desktop && cfg.UIPower {
		fmt.Fprintln(os.Stderr, "error: --ui_power and --desktop are mutually exclusive")
		os.Exit(1)
	}
	if cfg.StartupPower != "on" && cfg.StartupPower != "off" {
		fmt.Fprintln(os.Stderr, `error: --startup_power must be "on" or "off"`)
		os.Exit(1)
	}

	return cfg
}

// PrintStartupSummary prints the effective configuration to stdout.
func (cfg *Config) PrintStartupSummary() {
	mode := "Default (headless VM)"
	if cfg.Desktop {
		mode = "Desktop"
	}

	power := "MIDI CC28 (deterministic)"
	if cfg.UIPower {
		power = "UI click (pixel verified)"
	} else if cfg.PixelVerify {
		power = "MIDI CC28 (pixel verified)"
	}

	pixelStr := "OFF"
	if cfg.PixelVerify {
		pixelStr = "ON"
	}

	mqttStr := "disabled"
	if cfg.MQTTBroker != "" {
		mqttStr = fmt.Sprintf("%s:%d (topic: %s)", cfg.MQTTBroker, cfg.MQTTPort, cfg.MQTTTopic)
	}

	apiStr := "disabled"
	if cfg.APIPort > 0 {
		apiStr = fmt.Sprintf("http://localhost:%d", cfg.APIPort)
	}

	fmt.Printf("  Mode:           %s\n", mode)
	fmt.Printf("  GLM manager:    %s\n", onOff(cfg.GLMManager))
	if !cfg.Desktop {
		fmt.Printf("  RDP priming:    %s\n", onOff(cfg.RDPPriming))
		fmt.Printf("  MIDI restart:   %s\n", onOff(cfg.MIDIRestart))
	}
	fmt.Printf("  Power control:  %s\n", power)
	fmt.Printf("  Pixel verify:   %s\n", pixelStr)
	fmt.Printf("  API:            %s\n", apiStr)
	fmt.Printf("  MQTT:           %s\n", mqttStr)
	fmt.Println()
}

func onOff(b bool) string {
	if b {
		return "ON"
	}
	return "OFF"
}

// printUsage writes grouped CLI help to w.
func printUsage(w *os.File) {
	fmt.Fprintf(w, "vol20toglm v%s — Fosi VOL20 to Genelec GLM bridge\n", version.Version)
	fmt.Fprintf(w, `
OPERATING MODE
  --desktop              Desktop mode (disables GLM manager, RDP priming, MIDI restart)
  --pixel_verify         Enable pixel reading for power state verification (opt-in)
  --ui_power             Use UI click for power instead of MIDI (implies --pixel_verify)

STARTUP
  --startup_volume N     Startup volume 0-127, -1 to discover from GLM burst (default -1)
  --startup_power STATE  Power state at startup: "on" or "off" (default "on")

DEVICES & MIDI
  --list                 List available HID devices and MIDI ports, then exit
  --device VID,PID       HID device VID,PID in hex (default "0x07d7,0x0000")
  --midi_in_channel NAME MIDI port for commands TO GLM (default "GLMMIDI")
  --midi_out_channel NAME
                         MIDI port for state FROM GLM (default "GLMOUT")

REST API
  --api_port PORT        HTTP port for REST API and web UI, 0 to disable (default 8080)
  --cors_origin ORIGIN   CORS Allow-Origin header, empty to disable (default "*")

MQTT / HOME ASSISTANT
  --mqtt_broker HOST     MQTT broker hostname (empty to disable)
  --mqtt_port PORT       MQTT broker port (default 1883)
  --mqtt_user USER       MQTT username
  --mqtt_pass PASS       MQTT password
  --mqtt_topic PREFIX    MQTT topic prefix (default "glm")
  --mqtt_ha_discovery    Enable Home Assistant MQTT Discovery (default true)
  --no_mqtt_ha_discovery Disable Home Assistant MQTT Discovery

GLM PROCESS MANAGER
  --glm_manager          Launch and monitor GLM process (default true)
  --no_glm_manager       Disable GLM management
  --glm_path PATH        Path to GLM executable
                         (default "C:\Program Files (x86)\Genelec\GLMv5\GLMv5.exe")
  --glm_cpu_gating       Wait for CPU idle before launching GLM (default true)
  --no_glm_cpu_gating    Disable CPU gating

VM AUTOMATION (override defaults for fine-tuning)
  --rdp_priming          RDP session priming at startup (default true)
  --no_rdp_priming       Disable RDP priming
  --midi_restart         Restart Windows MIDI service at startup (default true)
  --no_midi_restart      Disable MIDI service restart
  --high_priority        Set process priority to AboveNormal (default true)
  --no_high_priority     Run at normal priority

VOLUME ACCELERATION
  --min_click_time SEC   Min seconds between clicks (default 0.2)
  --max_avg_click_time SEC
                         Max avg click time for acceleration (default 0.15)
  --volume_increases_list N,N,...
                         Volume delta per acceleration level (default "1,1,2,2,3")

LOGGING & DEBUG
  --log_level LEVEL      DEBUG, INFO, or NONE (default "DEBUG")
  --log_file_name NAME   Log file name (default "vol20toglm.log")
  --debug_captures       Dump pixel captures to BMP files
`)
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
