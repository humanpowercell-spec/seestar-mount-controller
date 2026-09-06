package mount

// Beeper — the piezo buzzer on /dev/zwo-beeper (char 10,125).
//
// RE: seestar-re/docs/peripherals.md § "Beeper" (ioctl table, from the OEM
// `beeper` CLI — type 'a'/0x61, _IOC_SIZE 8, int arg by pointer).

import (
	"fmt"
	"syscall"
	"unsafe"
)

// BeeperDev is the buzzer character device.
const BeeperDev = "/dev/zwo-beeper"

const (
	beeperType = 0x61 // 'a'

	// _IOW('a', nr, 8) — set*; _IOR('a', nr, 8) — get*. Size field is 8 even
	// though the payload is a 4-byte int (the OEM CLI passes &int); we hand the
	// driver an 8-byte buffer to satisfy _IOC_SIZE.
	beeperSetFreq      = (1 << 30) | (8 << 16) | (beeperType << 8) | 0x01
	beeperGetFreq      = (2 << 30) | (8 << 16) | (beeperType << 8) | 0x02
	beeperSetDuration  = (1 << 30) | (8 << 16) | (beeperType << 8) | 0x03
	beeperGetDuration  = (2 << 30) | (8 << 16) | (beeperType << 8) | 0x04
	beeperSetCount     = (1 << 30) | (8 << 16) | (beeperType << 8) | 0x05
	beeperGetCount     = (2 << 30) | (8 << 16) | (beeperType << 8) | 0x06
	beeperSetInterval  = (1 << 30) | (8 << 16) | (beeperType << 8) | 0x07
	beeperGetInterval  = (2 << 30) | (8 << 16) | (beeperType << 8) | 0x08
	beeperSetDutyCycle = (1 << 30) | (8 << 16) | (beeperType << 8) | 0x09
	beeperGetDutyCycle = (2 << 30) | (8 << 16) | (beeperType << 8) | 0x0a
	beeperBell         = (1 << 30) | (8 << 16) | (beeperType << 8) | 0x0b // arg 1=start, 0=stop
	beeperGetStatus    = (2 << 30) | (8 << 16) | (beeperType << 8) | 0x0c
)

// Beeper controls the piezo buzzer. A "beep sequence" is Count tones of
// Duration ms at Freq Hz / DutyCycle %, separated by Interval ms; Start() kicks
// it off, Stop() aborts it early.
type Beeper struct{ fd int }

// BeeperSettings is one snapshot of the buzzer's parameters.
type BeeperSettings struct {
	FreqHz       int
	DurationMS   int
	Count        int
	IntervalMS   int
	DutyCyclePct int
}

// OpenBeeper opens the buzzer device. Pass "" for BeeperDev.
func OpenBeeper(dev string) (*Beeper, error) {
	if dev == "" {
		dev = BeeperDev
	}
	fd, err := syscall.Open(dev, syscall.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", dev, err)
	}
	return &Beeper{fd: fd}, nil
}

// Close releases the device.
func (b *Beeper) Close() error { return syscall.Close(b.fd) }

func (b *Beeper) set(cmd uintptr, v int) error {
	buf := int64(v)
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(b.fd), cmd, uintptr(unsafe.Pointer(&buf)))
	if e != 0 {
		return fmt.Errorf("beeper ioctl 0x%x(%d): %w", cmd, v, e)
	}
	return nil
}

func (b *Beeper) get(cmd uintptr) (int, error) {
	var buf int64
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(b.fd), cmd, uintptr(unsafe.Pointer(&buf)))
	if e != 0 {
		return 0, fmt.Errorf("beeper ioctl 0x%x: %w", cmd, e)
	}
	return int(int32(buf)), nil
}

// SetFreq sets the tone frequency in Hz.
func (b *Beeper) SetFreq(hz int) error { return b.set(beeperSetFreq, hz) }

// SetDuration sets the on-time of each beep in milliseconds.
func (b *Beeper) SetDuration(ms int) error { return b.set(beeperSetDuration, ms) }

// SetCount sets how many beeps a Start() sequence emits.
func (b *Beeper) SetCount(n int) error { return b.set(beeperSetCount, n) }

// SetInterval sets the silent gap between beeps in milliseconds.
func (b *Beeper) SetInterval(ms int) error { return b.set(beeperSetInterval, ms) }

// SetDutyCycle sets the PWM duty cycle as a percentage (0–100).
func (b *Beeper) SetDutyCycle(pct int) error { return b.set(beeperSetDutyCycle, pct) }

// Settings reads back the current buzzer parameters.
func (b *Beeper) Settings() (BeeperSettings, error) {
	var s BeeperSettings
	var err error
	if s.FreqHz, err = b.get(beeperGetFreq); err != nil {
		return s, err
	}
	if s.DurationMS, err = b.get(beeperGetDuration); err != nil {
		return s, err
	}
	if s.Count, err = b.get(beeperGetCount); err != nil {
		return s, err
	}
	if s.IntervalMS, err = b.get(beeperGetInterval); err != nil {
		return s, err
	}
	if s.DutyCyclePct, err = b.get(beeperGetDutyCycle); err != nil {
		return s, err
	}
	return s, nil
}

// Start begins the beep sequence with the currently configured parameters.
// The driver returns EINVAL if a sequence is already running — call Stop first
// (Beep does this for you).
func (b *Beeper) Start() error { return b.set(beeperBell, 1) }

// Stop aborts a running beep sequence.
func (b *Beeper) Stop() error { return b.set(beeperBell, 0) }

// Running reports whether a beep sequence is currently playing.
func (b *Beeper) Running() (bool, error) {
	v, err := b.get(beeperGetStatus)
	return v != 0, err
}

// Beep is a one-shot helper: configure freq/duration/count/interval, then Start.
// Matches the OEM `beeper -f -d -c -i` CLI. DutyCycle is left at its current
// value. Any sequence already in progress is stopped first.
func (b *Beeper) Beep(freqHz, durationMS, count, intervalMS int) error {
	_ = b.Stop()
	if err := b.SetFreq(freqHz); err != nil {
		return err
	}
	if err := b.SetDuration(durationMS); err != nil {
		return err
	}
	if err := b.SetCount(count); err != nil {
		return err
	}
	if err := b.SetInterval(intervalMS); err != nil {
		return err
	}
	return b.Start()
}
