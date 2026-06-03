package mount

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// ImagerPort is the JSON-RPC port of the ZWO imager process.
const ImagerPort = "4700"

// ScopeMode represents the Seestar's current operating mode.
type ScopeMode string

const (
	ScopeModeAltAz      ScopeMode = "alt_az"
	ScopeModeEquatorial ScopeMode = "equatorial"
)

// ImagerMode queries the ZWO imager's JSON-RPC server on port 4700 for the
// current scope mode. host is the hostname or IP of the Seestar (no port).
// Returns ScopeModeAltAz or ScopeModeEquatorial on success.
func ImagerMode(ctx context.Context, host string) (ScopeMode, error) {
	d := net.Dialer{}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, ImagerPort))
	if err != nil {
		return "", fmt.Errorf("imager mode: connect %s: %w", host, err)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
	}

	if _, err := fmt.Fprintf(conn, `{"method":"scope_get_mode","id":1}`+"\n"); err != nil {
		return "", fmt.Errorf("imager mode: write: %w", err)
	}

	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	if err != nil {
		return "", fmt.Errorf("imager mode: read: %w", err)
	}
	var resp struct {
		Result string `json:"result"`
		Code   int    `json:"code"`
	}
	if err := json.Unmarshal(buf[:n], &resp); err != nil {
		return "", fmt.Errorf("imager mode: decode: %w", err)
	}
	if resp.Result == "" {
		return "", fmt.Errorf("imager mode: empty result from %s", host)
	}
	// Normalize firmware aliases to canonical constants.
	// The firmware returns "eq" for equatorial mode; callers expect ScopeModeEquatorial.
	switch resp.Result {
	case "eq":
		return ScopeModeEquatorial, nil
	case "alt_az", "altaz":
		return ScopeModeAltAz, nil
	}
	return ScopeMode(resp.Result), nil
}

// ImagerModeFromAlpacaURL extracts the host from an ASCOM Alpaca base URL
// (e.g. "http://192.168.86.218:32323") and calls ImagerMode. Convenience
// wrapper for callers that only have the Alpaca address.
func ImagerModeFromAlpacaURL(ctx context.Context, alpacaURL string) (ScopeMode, error) {
	if !strings.Contains(alpacaURL, "://") {
		alpacaURL = "http://" + alpacaURL
	}
	u, err := url.Parse(alpacaURL)
	if err != nil {
		return "", fmt.Errorf("imager mode: parse %q: %w", alpacaURL, err)
	}
	host := u.Hostname()
	if host == "" {
		return "", fmt.Errorf("imager mode: no host in %q", alpacaURL)
	}
	return ImagerMode(ctx, host)
}

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
	// Compass is true if the AK09915 magnetometer IIO chardev exists (DefaultIIODev).
	// The device is exclusively held by zwoair_imager while running; Compass=true
	// only means the hardware is present, not that it can be read right now.
	Compass bool
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
		Compass:    isCharDev(DefaultIIODev),
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
