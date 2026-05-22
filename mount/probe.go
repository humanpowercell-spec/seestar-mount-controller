package mount

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// HardwareProbe reports which Seestar S30 environment indicators were found.
// Checks are non-invasive: no serial port is opened, no processes are killed.
type HardwareProbe struct {
	// TTY is true if the mount serial device exists as a character device.
	TTY bool
	// ZWOConfig is true if the ASIAIR runtime config is present (/home/pi/ASIAIR/config).
	ZWOConfig bool
	// ZWORunning is true if a ZWO guider or imager process is found in /proc.
	ZWORunning bool
	// Alpaca is true if the ZWO Alpaca server directory exists (/etc/zwo/Alpaca).
	Alpaca bool
}

// Detected returns true if at least two independent indicators are present,
// which is strong evidence the process is running on Seestar S30 hardware.
func (p HardwareProbe) Detected() bool {
	n := 0
	for _, b := range []bool{p.TTY, p.ZWOConfig, p.ZWORunning, p.Alpaca} {
		if b {
			n++
		}
	}
	return n >= 2
}

// ProbeHardware scans the local environment for Seestar S30 indicators without
// opening the serial port or affecting any running process.
// Pass an empty string to use DefaultDev for the TTY check.
func ProbeHardware(dev string) HardwareProbe {
	if dev == "" {
		dev = DefaultDev
	}
	return HardwareProbe{
		TTY:        isCharDev(dev),
		ZWOConfig:  fileExists("/home/pi/ASIAIR/config"),
		ZWORunning: zwoRunning(),
		Alpaca:     dirExists("/etc/zwo/Alpaca"),
	}
}

// isCharDev returns true if path exists and is a character device.
func isCharDev(path string) bool {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return false
	}
	return st.Mode&syscall.S_IFMT == syscall.S_IFCHR
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// zwoRunning scans /proc for a running zwoair_guider or zwoair_imager process.
func zwoRunning() bool {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		// Only numeric entries are PIDs.
		name := e.Name()
		if len(name) == 0 || name[0] < '1' || name[0] > '9' {
			continue
		}
		cmdline, err := os.ReadFile(filepath.Join("/proc", name, "cmdline"))
		if err != nil {
			continue
		}
		s := string(cmdline)
		if strings.Contains(s, "zwoair_guider") || strings.Contains(s, "zwoair_imager") {
			return true
		}
	}
	return false
}
