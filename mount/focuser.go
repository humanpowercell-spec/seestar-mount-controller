package mount

// Focuser (EAF, /dev/eaf) and FilterWheel (/dev/pwm-gpio-misc) control.
// Ported from the retired seestar-s30-re/mount/ prototype 2026-09-03; the ioctl
// tables below were backfilled into seestar-re proper the same day (they only
// ever existed as Go comments before that — see RE_PROVENANCE.md). The AK09915
// magnetometer lives in compass.go (ReadCompass / AverageCompass) — not
// duplicated here.
//
// RE: seestar-re/docs/focuser.md:9-63 (hardware/motor parameters),
// seestar-re/docs/filter_wheel.md:9-58 (hardware/GPIO allocation).

import (
	"context"
	"fmt"
	"syscall"
	"time"
	"unsafe"
)

// ============================================================
// EAF Focuser — /dev/eaf
// ============================================================
//
// The EAF is a TI DRV8846 stepper motor controlled by the zwo-eaf
// kernel driver.  The driver exposes a character device at /dev/eaf
// with a simple ioctl interface (magic byte 'z' = 0x7a).
//
// Ioctl commands (each carries a 2×uint32 struct):
//
//	_IOR('z', 1, [8]byte)  → {max_steps, 0}         get max position
//	_IOW('z', 2, [8]byte)  ← {target, 0}             move to absolute microstep
//	_IOR('z', 3, [8]byte)  → {pos, 0}               get current microstep position
//	_IOW('z', 4, [8]byte)  ← {0, 0}                  halt motion immediately
//	_IOR('z', 5, [8]byte)  → {remaining, 0}          steps left; 0 = stopped
//	_IOW('z', 6, [8]byte)  ← {dir: 0|1, 0}           direction hint before Goto
//
// Position units: microsteps.  Hardware range: 0–3040 (1/8 microstepping,
// 380 full steps total).  All moves are non-blocking; the driver runs the
// ramp + step sequence in kernel context.
//
// Decoded by disassembling the zwo-eaf kernel driver and confirmed by
// live ioctl probing on hardware.
//
// RE: seestar-re/docs/focuser.md:65-81 (ioctl interface table, magic 'z'/0x7a),
// :42 (microstep range 0-3040), :47-60 (kernel driver / sysfs mirror).

const (
	eafMagic = 0x7a

	eafGetMax       = (2 << 30) | (8 << 16) | (eafMagic << 8) | 1 // _IOR('z',1,8)
	eafGoto         = (1 << 30) | (8 << 16) | (eafMagic << 8) | 2 // _IOW('z',2,8)
	eafGetPos       = (2 << 30) | (8 << 16) | (eafMagic << 8) | 3 // _IOR('z',3,8)
	eafHalt         = (1 << 30) | (8 << 16) | (eafMagic << 8) | 4 // _IOW('z',4,8)
	eafGetRemaining = (2 << 30) | (8 << 16) | (eafMagic << 8) | 5 // _IOR('z',5,8)
	eafSetDir       = (1 << 30) | (8 << 16) | (eafMagic << 8) | 6 // _IOW('z',6,8)
)

type eafArgs [2]uint32

// EAFDev is the device path for the telephoto focuser.
const EAFDev = "/dev/eaf"

// Focuser controls the Seestar S30 EAF stepper via /dev/eaf ioctls.
type Focuser struct{ fd int }

// OpenFocuser opens the EAF device.  Pass "" to use EAFDev.
func OpenFocuser(dev string) (*Focuser, error) {
	if dev == "" {
		dev = EAFDev
	}
	fd, err := syscall.Open(dev, syscall.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", dev, err)
	}
	return &Focuser{fd: fd}, nil
}

// Close releases the device file descriptor.
func (f *Focuser) Close() error { return syscall.Close(f.fd) }

// MaxPos returns the maximum valid microstep position (3040 on the S30).
func (f *Focuser) MaxPos() (uint32, error) {
	a, err := f.eafIoctl(eafGetMax, eafArgs{})
	return a[0], err
}

// Pos returns the current microstep position (0–3040).
func (f *Focuser) Pos() (uint32, error) {
	a, err := f.eafIoctl(eafGetPos, eafArgs{})
	return a[0], err
}

// Remaining returns the number of microsteps left in the current move.
// Zero means the focuser is stopped.
func (f *Focuser) Remaining() (uint32, error) {
	a, err := f.eafIoctl(eafGetRemaining, eafArgs{})
	return a[0], err
}

// IsMoving returns true while a move is in progress.
func (f *Focuser) IsMoving() (bool, error) {
	r, err := f.Remaining()
	return r != 0, err
}

// Goto commands an absolute move to target microsteps (0–MaxPos).
// Returns immediately; the move runs in the kernel driver.
// Sets the direction hint before issuing the move command.
func (f *Focuser) Goto(target uint32) error {
	cur, err := f.Pos()
	if err != nil {
		return err
	}
	dir := uint32(1) // outward (toward higher positions)
	if target < cur {
		dir = 0
	}
	if _, err := f.eafIoctl(eafSetDir, eafArgs{dir, 0}); err != nil {
		return fmt.Errorf("set_dir: %w", err)
	}
	if _, err := f.eafIoctl(eafGoto, eafArgs{target, 0}); err != nil {
		return fmt.Errorf("goto %d: %w", target, err)
	}
	return nil
}

// Halt stops any in-progress move immediately.
func (f *Focuser) Halt() error {
	_, err := f.eafIoctl(eafHalt, eafArgs{})
	return err
}

// WaitStop blocks until the focuser is stopped or ctx is cancelled.
func (f *Focuser) WaitStop(ctx context.Context) error {
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			moving, err := f.IsMoving()
			if err != nil {
				return err
			}
			if !moving {
				return nil
			}
		}
	}
}

// GotoAndWait moves to target and blocks until arrival or ctx is cancelled.
func (f *Focuser) GotoAndWait(ctx context.Context, target uint32) error {
	if err := f.Goto(target); err != nil {
		return err
	}
	return f.WaitStop(ctx)
}

func (f *Focuser) eafIoctl(cmd uintptr, in eafArgs) (eafArgs, error) {
	a := in
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(f.fd), cmd,
		uintptr(unsafe.Pointer(&a)))
	if errno != 0 {
		return eafArgs{}, fmt.Errorf("ioctl 0x%x: %w", cmd, errno)
	}
	return a, nil
}

// ============================================================
// Filter Wheel — /dev/pwm-gpio-misc
// ============================================================
//
// The filter wheel is a 2-phase stepper motor driven by the ZWO
// pwm-gpio kernel driver.  Coil signals are exposed as virtual
// GPIO indices through /dev/pwm-gpio-misc with magic byte 'C' (0x43).
//
// Decoded ioctl commands (each carries a 2×uint32 struct {index, value}):
//
//	_IOR('C', 1, [8]byte)  → {mode, 0}    get GPIO mode (0=input,1=output,2=PWM)
//	_IOW('C', 2, [8]byte)  ← {idx, mode}  set GPIO mode
//	_IOR('C', 3, [8]byte)  → {period_ns, duty_ns}  get PWM config
//	_IOW('C', 4, [8]byte)  ← {idx, period_ns, duty_ns}  set PWM config (12-byte variant)
//	_IOW('C', 5, [8]byte)  ← {idx, 0}    start PWM on GPIO
//	_IOW('C', 6, [8]byte)  ← {idx, 0}    stop PWM on GPIO
//	_IOW('C', 7, [8]byte)  → {idx, level}  get GPIO level (DIR=WRITE in ioctl, returns level)
//	_IOW('C', 8, [8]byte)  ← {idx, level}  set GPIO level (0=low, 1=high)
//
// Filter wheel GPIO index mapping (confirmed by live observation):
//
//	Index 8  → GPIO5[21] = sysfs gpio181   coil A (active-high)
//	Index 9  → GPIO5[28] = sysfs gpio188   coil B (active-high)
//	Index 12 → GPIO5[20] = sysfs gpio180   enable  (active-low: 0=on, 1=off)
//
// Filter positions: 0=Dark, 1=IR (default), 2=LP (dual-narrowband)
//
// NOTE: The step sequence (coil phase pattern for A/B per step) has not yet
// been reverse-engineered from libasisdk.so.  The FilterWheel type below
// implements the ioctl infrastructure but there is no Goto(position) method
// until the sequence is confirmed — see seestar-re/docs/filter_wheel.md's
// "Known Unknowns" section, and RE_PROVENANCE.md for why this isn't guessed.
//
// RE: seestar-re/docs/filter_wheel.md:59-76 (ioctl interface table, magic
// 'C'/0x43), :33-58 (GPIO allocation table — indices 8/9/12).

const (
	pwmMagic = 0x43

	pwmGetMode  = (2 << 30) | (8 << 16) | (pwmMagic << 8) | 1 // _IOR('C',1,8)
	pwmSetMode  = (1 << 30) | (8 << 16) | (pwmMagic << 8) | 2 // _IOW('C',2,8)
	pwmGetLevel = (1 << 30) | (8 << 16) | (pwmMagic << 8) | 7 // _IOW('C',7,8) — reads back
	pwmSetLevel = (1 << 30) | (8 << 16) | (pwmMagic << 8) | 8 // _IOW('C',8,8)

	// GPIO index assignments (from seestar-gpios device tree + live observation).
	filterGPIOCoilA  = 8  // coil A  (active-high)
	filterGPIOCoilB  = 9  // coil B  (active-high)
	filterGPIOEnable = 12 // enable  (active-low: write 0 to energise, 1 to de-energise)

	// gpioModeOutput is the mode value for a driven output.
	gpioModeOutput = 1
)

type pwmArgs [2]uint32

// FilterWheelDev is the pwm-gpio misc device path.
const FilterWheelDev = "/dev/pwm-gpio-misc"

// FilterWheel controls the built-in filter wheel via /dev/pwm-gpio-misc.
// Filter positions: 0=Dark, 1=IR, 2=LP (dual-narrowband).
type FilterWheel struct{ fd int }

// OpenFilterWheel opens the pwm-gpio device.  Pass "" to use FilterWheelDev.
func OpenFilterWheel(dev string) (*FilterWheel, error) {
	if dev == "" {
		dev = FilterWheelDev
	}
	fd, err := syscall.Open(dev, syscall.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", dev, err)
	}
	return &FilterWheel{fd: fd}, nil
}

// Close releases the device file descriptor.
func (fw *FilterWheel) Close() error { return syscall.Close(fw.fd) }

// GPIOMode returns the current mode for the given virtual GPIO index.
// Mode values: 0=input, 1=output, 2=PWM.
func (fw *FilterWheel) GPIOMode(idx uint32) (uint32, error) {
	a, err := fw.pwmIoctl(pwmGetMode, pwmArgs{idx, 0})
	return a[1], err
}

// GPIOLevel returns the current level (0 or 1) for the given GPIO index.
func (fw *FilterWheel) GPIOLevel(idx uint32) (uint32, error) {
	a, err := fw.pwmIoctl(pwmGetLevel, pwmArgs{idx, 0})
	return a[1], err
}

// SetGPIOMode sets the mode of a GPIO (0=input, 1=output, 2=PWM).
func (fw *FilterWheel) SetGPIOMode(idx, mode uint32) error {
	_, err := fw.pwmIoctl(pwmSetMode, pwmArgs{idx, mode})
	return err
}

// SetGPIOLevel drives a GPIO high (level=1) or low (level=0).
// The GPIO must be in output mode first.
func (fw *FilterWheel) SetGPIOLevel(idx, level uint32) error {
	_, err := fw.pwmIoctl(pwmSetLevel, pwmArgs{idx, level})
	return err
}

// Energise turns the filter wheel motor on (enable = active-low → LOW).
func (fw *FilterWheel) Energise() error {
	return fw.SetGPIOLevel(filterGPIOEnable, 0)
}

// Deenergise turns the filter wheel motor off (enable pin → HIGH).
func (fw *FilterWheel) Deenergise() error {
	return fw.SetGPIOLevel(filterGPIOEnable, 1)
}

// CoilState returns the current level of coil A and coil B.
func (fw *FilterWheel) CoilState() (a, b uint32, err error) {
	if a, err = fw.GPIOLevel(filterGPIOCoilA); err != nil {
		return
	}
	b, err = fw.GPIOLevel(filterGPIOCoilB)
	return
}

// Step drives coil A and coil B to the given levels in one paired ioctl sequence.
// This is the primitive used by the full step sequencer.
// coilA, coilB: 0=low, 1=high.
func (fw *FilterWheel) Step(coilA, coilB uint32) error {
	if err := fw.SetGPIOLevel(filterGPIOCoilA, coilA); err != nil {
		return fmt.Errorf("coilA: %w", err)
	}
	if err := fw.SetGPIOLevel(filterGPIOCoilB, coilB); err != nil {
		return fmt.Errorf("coilB: %w", err)
	}
	return nil
}

func (fw *FilterWheel) pwmIoctl(cmd uintptr, in pwmArgs) (pwmArgs, error) {
	a := in
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fw.fd), cmd,
		uintptr(unsafe.Pointer(&a)))
	if errno != 0 {
		return pwmArgs{}, fmt.Errorf("ioctl 0x%x: %w", cmd, errno)
	}
	return a, nil
}
