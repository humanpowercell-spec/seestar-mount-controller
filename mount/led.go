package mount

// Power / status LED on /dev/pwrled-misc (char 10,126).
//
// RE: seestar-re/docs/peripherals.md § "Power LED". Two interfaces exist; this
// wraps the kernel string protocol (what the OEM `flash_power_led` CLI uses):
// write() an ASCII command, read() 7 bytes of status. The mode→pattern map is
// in the built-in pwrled_gpio driver and is only partly known — hence the raw
// WriteCommand API rather than a typed enum. (The other interface,
// libasisdk.so's ASI_POWERLED_SetMode behind the set_power_led_mode RPC, is
// SDK-locked and not reimplemented here.)

import (
	"fmt"
	"strconv"
	"strings"
	"syscall"
)

// PowerLEDDev is the status-LED character device.
const PowerLEDDev = "/dev/pwrled-misc"

// PowerLED wraps /dev/pwrled-misc.
type PowerLED struct{ fd int }

// OpenPowerLED opens the LED device. Pass "" for PowerLEDDev.
func OpenPowerLED(dev string) (*PowerLED, error) {
	if dev == "" {
		dev = PowerLEDDev
	}
	fd, err := syscall.Open(dev, syscall.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", dev, err)
	}
	return &PowerLED{fd: fd}, nil
}

// Close releases the device.
func (l *PowerLED) Close() error { return syscall.Close(l.fd) }

// WriteCommand writes a raw command string to the driver, exactly as
// `flash_power_led <args>` does (the args are joined with a space).
//
// Known strings (from the OEM update scripts):
//
//	"3 333"  — mode 3, ~333 ms period: fast blink ("updating")
//	"13"     — mode 13: "update done" indication
//
// Other modes exist but aren't enumerated — see seestar-re/docs/peripherals.md.
func (l *PowerLED) WriteCommand(args ...string) error {
	s := strings.Join(args, " ")
	if _, err := syscall.Write(l.fd, []byte(s)); err != nil {
		return fmt.Errorf("pwrled write %q: %w", s, err)
	}
	return nil
}

// Mode is WriteCommand for the common "<mode>" / "<mode> <param>" form.
func (l *PowerLED) Mode(mode int, param ...int) error {
	args := []string{strconv.Itoa(mode)}
	for _, p := range param {
		args = append(args, strconv.Itoa(p))
	}
	return l.WriteCommand(args...)
}

// Status reads the 7-byte status string the driver reports (the OEM CLI prints
// it as "recv data = %s"). Format is not documented.
func (l *PowerLED) Status() (string, error) {
	buf := make([]byte, 7)
	n, err := syscall.Read(l.fd, buf)
	if err != nil {
		return "", fmt.Errorf("pwrled read: %w", err)
	}
	return strings.TrimRight(string(buf[:n]), "\x00 \n"), nil
}
