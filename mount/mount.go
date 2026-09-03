// Package mount provides direct serial control of the Seestar S30 telescope mount
// via the LX200-compatible protocol on /dev/ttyS3 (115200 8N1).
//
// Protocol source: ESP32-S3 firmware (main.bin) reverse engineered via Ghidra.
// See seestar_esp32_re.md for the full command map and state machine description.
//
// Startup sequence: Open → ExitHome (sends :SH0# to clear home mode) → motion commands.
// Without ExitHome, SlewRate and TrackRate silently no-op (firmware state != 3).
//
// Warning: zwoair_guider on the telescope also owns /dev/ttyS3. Stop it first
// (or use port 4030 TCP bridge) to avoid command conflicts.
package mount

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const (
	DefaultDev = "/dev/ttyS3"

	// maxDegPerSec is the firmware-enforced maximum angular velocity.
	maxDegPerSec = 6.0

	// siderealUnitsPerDegSec converts deg/s to the sidereal-multiple units used
	// by :Rvr# and :Rvd#.  1 sidereal unit = 15 arcsec/s = 1/240 deg/s.
	siderealUnitsPerDegSec = 240.0

	// maxSiderealUnits is the firmware cap for :Rv rates (1440 × 15 arcsec/s = 6 deg/s).
	maxSiderealUnits = 1440.0
)

// Mount represents an open connection to the Seestar S30 mount.
// Safe for concurrent use: a background tracking goroutine and a
// position-polling goroutine may share the same Mount safely.
type Mount struct {
	fd int
	mu sync.Mutex

	// last GoTo target, stored for WaitGoTo
	gotoRA  float64 // hours
	gotoDec float64 // degrees
}

// Open opens the serial port and configures it for mount communication (115200 8N1 raw).
// Pass an empty string to use DefaultDev.
func Open(dev string) (*Mount, error) {
	if dev == "" {
		dev = DefaultDev
	}
	fd, err := unix.Open(dev, unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", dev, err)
	}
	t := unix.Termios{}
	t.Cflag = unix.B115200 | unix.CS8 | unix.CLOCAL | unix.CREAD
	// VMIN=0, VTIME=10: read() returns on first byte or after 1 s with 0 bytes.
	t.Cc[unix.VMIN] = 0
	t.Cc[unix.VTIME] = 10
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, &t); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("tcsetattr: %w", err)
	}
	return &Mount{fd: fd}, nil
}

// Close releases the serial port.
func (m *Mount) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return unix.Close(m.fd)
}

// Cmd sends a raw LX200 command and reads the '#'-terminated response.
// Flushes the input buffer before writing to discard any stale bytes.
// The leading ':' and trailing '#' are added if absent.
func (m *Mount) Cmd(cmd string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.query(cmd)
}

// cmdNoReply sends a fire-and-forget command.  Does not flush and does not
// read a reply — intended for high-rate motion commands (:Rvr#, :MTD#, etc.)
// where reply latency would hurt a tracking loop.
func (m *Mount) cmdNoReply(cmd string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.send(cmd)
}

// query flushes input, writes cmd, reads reply.  Caller must hold mu.
func (m *Mount) query(cmd string) (string, error) {
	unix.IoctlSetInt(m.fd, unix.TCFLSH, unix.TCIFLUSH) //nolint:errcheck
	if err := m.send(cmd); err != nil {
		return "", err
	}
	return m.readReply()
}

// send writes cmd without flushing or reading.  Caller must hold mu.
func (m *Mount) send(cmd string) error {
	if !strings.HasPrefix(cmd, ":") {
		cmd = ":" + cmd
	}
	if !strings.HasSuffix(cmd, "#") {
		cmd += "#"
	}
	_, err := unix.Write(m.fd, []byte(cmd))
	return err
}

func (m *Mount) readReply() (string, error) {
	var result []byte
	b := make([]byte, 1)
	for len(result) < 256 {
		n, err := unix.Read(m.fd, b)
		if n > 0 {
			if b[0] == '#' {
				return string(result), nil
			}
			result = append(result, b[0])
		}
		if err != nil {
			if len(result) > 0 {
				return string(result), nil
			}
			return "", fmt.Errorf("read: %w", err)
		}
		if n == 0 {
			if len(result) > 0 {
				return string(result), nil
			}
			return "", fmt.Errorf("read timeout")
		}
	}
	return string(result), nil
}

// ---- Motion — single axis ----

// Slew starts movement at a preset integer speed (1–9) in the given direction.
// Sends :R{speed}# then :M{dir}#.
func (m *Mount) Slew(dir string, speed int) error {
	dir = strings.ToLower(dir)
	if speed < 1 || speed > 9 {
		return fmt.Errorf("speed %d out of range 1-9", speed)
	}
	if !isDir(dir) {
		return fmt.Errorf("direction must be e, w, n, or s")
	}
	if err := m.cmdNoReply(fmt.Sprintf(":R%d#", speed)); err != nil {
		return err
	}
	return m.cmdNoReply(fmt.Sprintf(":M%s#", dir))
}

// SlewRate starts continuous motion using :MT{dir}{nn}# at the given speed.
// degPerSec is clamped to 0–6 deg/s and mapped to firmware integers 1–10.
// Requires the mount to be in motion state (call ExitHome first).
func (m *Mount) SlewRate(dir string, degPerSec float64) error {
	dir = strings.ToLower(dir)
	if !isDir(dir) {
		return fmt.Errorf("direction must be e, w, n, or s")
	}
	// Firmware validates (nn-1) < 10, so valid range is 1-10.
	nn := int(math.Round(math.Max(1, math.Min(10, (degPerSec/maxDegPerSec)*10))))
	return m.cmdNoReply(fmt.Sprintf(":MT%s%02d#", dir, nn))
}

// SetRate sets the per-axis variable rate via :Rv{axis}{speed}# in deg/s.
// axis must be "ra" or "dec".  Use before PulseMove on the same axis.
// Range: 0–6 deg/s; internally converted to sidereal multiples (0–1440).
func (m *Mount) SetRate(axis string, degPerSec float64) error {
	axisChar, err := axisToChar(axis)
	if err != nil {
		return err
	}
	units := math.Max(0, math.Min(maxSiderealUnits, degPerSec*siderealUnitsPerDegSec))
	return m.cmdNoReply(fmt.Sprintf(":Rv%s%.4f#", axisChar, units))
}

// PulseMove fires a timed pulse using :MTD{dir}{dur:02d}#.
// Call SetRate first to set the pulse speed.
// dur is in mount-native units (believed to be centiseconds; dur=10 ≈ 100 ms).
func (m *Mount) PulseMove(dir string, dur int) error {
	dir = strings.ToLower(dir)
	if !isDir(dir) {
		return fmt.Errorf("direction must be e, w, n, or s")
	}
	if dur < 0 || dur > 99 {
		return fmt.Errorf("dur %d out of range 0-99", dur)
	}
	return m.cmdNoReply(fmt.Sprintf(":MTD%s%02d#", dir, dur))
}

// ---- Motion — dual axis (tracking) ----

// TrackRate sets both axis angular velocities simultaneously using :Rvr# (az/axis1)
// and :Rvd# (el/axis2).  See also TrackRateSY, which does the same thing in a
// single accel-limited :SY# command (ESP32 motion mode 7) and is usually the
// better choice for a streamed tracking loop.
//
//   azDegPerSec > 0 = east (clockwise),   < 0 = west
//   elDegPerSec > 0 = up / north,         < 0 = down / south
//
// Verified by firmware decompile: :Rvr# and :Rvd# are direct axis motor rate
// commands.  The rate value is parsed, clamped to [0.25, 1440] sidereal multiples,
// and written straight to the hardware step-rate register (DAT_3fc9586c/68 and
// DAT_3fc95864/60) with only float multiply/add for velocity ramping — no sin/cos,
// no coordinate frame transform.
//
// The firmware's "RA/Dec" axis labels are an artefact of the equatorial mount codebase it was derived from;
// axis 1 is the physical azimuth motor and axis 2 is the physical elevation motor.
// Coordinate math (spherical trig using sin/cos of latitude) is used only by the
// GoTo planner (:MS#) and the autonomous sidereal/solar/lunar tracking loop (:TQ#/:TS#/:TL#).
// It is not involved when you set rates directly with :Rv#.
//
// A satellite tracker that computes az/el angular velocities externally should feed
// them here without any prior coordinate rotation.
//
// Both commands are sent without flushing so a 10–20 Hz loop incurs minimal overhead.
func (m *Mount) TrackRate(azDegPerSec, elDegPerSec float64) error {
	azUnits := math.Abs(azDegPerSec) * siderealUnitsPerDegSec
	azUnits = math.Max(0, math.Min(maxSiderealUnits, azUnits))
	elUnits := math.Abs(elDegPerSec) * siderealUnitsPerDegSec
	elUnits = math.Max(0, math.Min(maxSiderealUnits, elUnits))

	m.mu.Lock()
	defer m.mu.Unlock()

	// Azimuth axis (Axis1 / :Rvr#)
	if azUnits == 0 {
		if err := m.send(":Qe#"); err != nil {
			return fmt.Errorf("Qe: %w", err)
		}
	} else {
		if err := m.send(fmt.Sprintf(":Rvr%.4f#", azUnits)); err != nil {
			return fmt.Errorf("Rvr: %w", err)
		}
		azDir := "e"
		if azDegPerSec < 0 {
			azDir = "w"
		}
		if err := m.send(fmt.Sprintf(":M%s#", azDir)); err != nil {
			return fmt.Errorf("M%s: %w", azDir, err)
		}
	}

	// Elevation axis (Axis2 / :Rvd#)
	if elUnits == 0 {
		if err := m.send(":Qn#"); err != nil {
			return fmt.Errorf("Qn: %w", err)
		}
	} else {
		if err := m.send(fmt.Sprintf(":Rvd%.4f#", elUnits)); err != nil {
			return fmt.Errorf("Rvd: %w", err)
		}
		elDir := "n"
		if elDegPerSec < 0 {
			elDir = "s"
		}
		if err := m.send(fmt.Sprintf(":M%s#", elDir)); err != nil {
			return fmt.Errorf("M%s: %w", elDir, err)
		}
	}
	return nil
}

// TrackRateSY sets both axis angular velocities in a single :SY command
// (ESP32 motion "mode 7").  It is the newer, cleaner equivalent of TrackRate:
//
//   - one write for both axes instead of four (:Rvr + :Me + :Rvd + :Mn)
//   - the firmware accel-limits the current rate toward the target (±32 units
//     per control tick, snapping through zero), so direction reversals and
//     rate steps are ramped rather than instantaneous — smoother to stream
//   - a zero value on an axis decelerates that axis to a stop
//
// The value is a signed rate in the same sidereal-multiple units as :Rv,
// clamped to ±1440 (6 deg/s).  Sign encodes direction:
//
//   azDegPerSec > 0 = east,   < 0 = west
//   elDegPerSec > 0 = north,  < 0 = south
//
// Wire format: ":SY%+05d%+05d#" (sign + 4-digit magnitude per axis), matching
// what zwoair_imager's object tracker streams.  Pair with EnableTracking(true).
//
// RE: seestar-s30-re docs/esp32_firmware.md (:SY# / motion mode 7,
// FUN_42011bd0 / FUN_42011cac), docs/rate_control.md, CAP-MOUNT-014.
func (m *Mount) TrackRateSY(azDegPerSec, elDegPerSec float64) error {
	clamp := func(deg float64) int {
		u := deg * siderealUnitsPerDegSec
		if u > maxSiderealUnits {
			u = maxSiderealUnits
		} else if u < -maxSiderealUnits {
			u = -maxSiderealUnits
		}
		return int(math.Round(u))
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.send(fmt.Sprintf(":SY%+05d%+05d#", clamp(azDegPerSec), clamp(elDegPerSec)))
}

// Stop stops all axes immediately.
func (m *Mount) Stop() error { return m.cmdNoReply(":Q#") }

// StopRA stops the RA (east/west) axis only.
func (m *Mount) StopRA() error { return m.cmdNoReply(":Qe#") }

// StopDec stops the Dec (north/south) axis only.
func (m *Mount) StopDec() error { return m.cmdNoReply(":Qn#") }

// Home slews to the mount's home position.
func (m *Mount) Home() error { return m.cmdNoReply(":hC#") }

// ExitHome clears home/park mode, transitioning the firmware to motion state 3.
// Required after Open and after any Home call before SlewRate or TrackRate will work.
func (m *Mount) ExitHome() error { return m.cmdNoReply(":SH0#") }

// HomeAndWait slews to the home position, blocks until homing completes, then
// calls ExitHome so the mount is immediately ready for GoTo or TrackRate calls.
// Returns when the mount is in motion state 3 (ready), or when ctx is cancelled.
// This is the normal startup sequence; callers do not need to call Home,
// HomeStatus, or ExitHome separately.
func (m *Mount) HomeAndWait(ctx context.Context) error {
	if err := m.Home(); err != nil {
		return fmt.Errorf("home: %w", err)
	}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			done, err := m.HomeStatus()
			if err != nil {
				return fmt.Errorf("homestatus: %w", err)
			}
			if done {
				return m.ExitHome()
			}
		}
	}
}

// HomeStatus returns true when the homing sequence is complete.
// Returns false (no error) if the mount is not in a homing sequence — the
// firmware silently drops :hF# outside of homing, causing a read timeout.
func (m *Mount) HomeStatus() (bool, error) {
	resp, err := m.Cmd(":hF#")
	if err != nil {
		if strings.Contains(err.Error(), "timeout") {
			return false, nil
		}
		return false, err
	}
	return strings.TrimSpace(resp) == "1", nil
}

// EnableTracking enables (true) or disables (false) active tracking (:TO1# / :TO0#).
func (m *Mount) EnableTracking(enable bool) error {
	if enable {
		return m.cmdNoReply(":TO1#")
	}
	return m.cmdNoReply(":TO0#")
}

// SyncMotors syncs the motor position registers to the current physical position (:SIM#).
func (m *Mount) SyncMotors() error { return m.cmdNoReply(":SIM#") }

// Reset triggers an immediate ESP32-S3 hard reset (:AR#).
// The mount will be unresponsive for several seconds while it reboots.
func (m *Mount) Reset() error { return m.cmdNoReply(":AR#") }

// ---- GoTo ----

// GotoCoords slews to the given equatorial coordinates and stores them for WaitGoTo.
// raHours: right ascension in decimal hours (0–24).
// decDeg: declination in decimal degrees (−90 to +90).
func (m *Mount) GotoCoords(raHours, decDeg float64) error {
	raCmd, decCmd := formatRADec(raHours, decDeg)

	m.mu.Lock()
	m.gotoRA = raHours
	m.gotoDec = decDeg
	if err := m.send(raCmd); err != nil {
		m.mu.Unlock()
		return err
	}
	if err := m.send(decCmd); err != nil {
		m.mu.Unlock()
		return err
	}
	if err := m.send(":MS#"); err != nil {
		m.mu.Unlock()
		return err
	}
	m.mu.Unlock()
	return nil
}

// SyncPosition updates the mount's internal position registers without moving.
// Use after GotoCoords completes to correct accumulated pointing error, or to
// manually tell the mount where it is currently pointing.
// Sends :Sr# + :Sd# + :SIM# (set target coordinates then sync motors to them).
func (m *Mount) SyncPosition(raHours, decDeg float64) error {
	raCmd, decCmd := formatRADec(raHours, decDeg)

	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.send(raCmd); err != nil {
		return err
	}
	if err := m.send(decCmd); err != nil {
		return err
	}
	return m.send(":SIM#")
}

// WaitGoTo blocks until the mount's reported position is within toleranceDeg of
// the coordinates last passed to GotoCoords, or until ctx is cancelled.
// Polls at ~500 ms intervals.  A tolerance of 0.1° is adequate for visual; use
// 0.02° for satellite acquisition where pointing precision matters.
func (m *Mount) WaitGoTo(ctx context.Context, toleranceDeg float64) error {
	m.mu.Lock()
	targetRA := m.gotoRA
	targetDec := m.gotoDec
	m.mu.Unlock()

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			ra, dec, err := m.RADec()
			if err != nil {
				continue
			}
			// Great-circle distance: RA component is scaled by cos(Dec) so that
			// 1h of RA near the pole doesn't count as 15° of angular separation.
			cosDecTarget := math.Cos(targetDec * math.Pi / 180)
			dRA := (ra - targetRA) * 15 * cosDecTarget
			dDec := dec - targetDec
			dist := math.Sqrt(dRA*dRA + dDec*dDec)
			if dist <= toleranceDeg {
				return nil
			}
		}
	}
}

// GotoAndWait combines GotoCoords and WaitGoTo.  Returns when the mount has
// arrived within toleranceDeg, or when ctx is cancelled.
func (m *Mount) GotoAndWait(ctx context.Context, raHours, decDeg, toleranceDeg float64) error {
	if err := m.GotoCoords(raHours, decDeg); err != nil {
		return err
	}
	return m.WaitGoTo(ctx, toleranceDeg)
}

// ---- Tracking ----

// SetTracking sets the mount tracking rate. mode must be "sidereal", "solar", or "lunar".
// Only takes effect outside motion state (call Stop first if mount is moving).
func (m *Mount) SetTracking(mode string) error {
	cmds := map[string]string{
		"sidereal": ":TQ#",
		"solar":    ":TS#",
		"lunar":    ":TL#",
	}
	cmd, ok := cmds[strings.ToLower(mode)]
	if !ok {
		return fmt.Errorf("unknown tracking mode %q (sidereal, solar, or lunar)", mode)
	}
	return m.cmdNoReply(cmd)
}

// ---- Reads ----

// RADec returns the current right ascension (decimal hours) and declination (decimal degrees).
func (m *Mount) RADec() (ra, dec float64, err error) {
	m.mu.Lock()
	raStr, err := m.query(":GR#")
	if err != nil {
		m.mu.Unlock()
		return 0, 0, fmt.Errorf("GR: %w", err)
	}
	decStr, err := m.query(":GD#")
	m.mu.Unlock()
	if err != nil {
		return 0, 0, fmt.Errorf("GD: %w", err)
	}
	if ra, err = parseLX200RA(raStr); err != nil {
		return 0, 0, fmt.Errorf("parse RA %q: %w", raStr, err)
	}
	if dec, err = parseLX200Dec(decStr); err != nil {
		return 0, 0, fmt.Errorf("parse Dec %q: %w", decStr, err)
	}
	return ra, dec, nil
}

// AltAz returns the current altitude and azimuth in decimal degrees.
func (m *Mount) AltAz() (alt, az float64, err error) {
	m.mu.Lock()
	altStr, err := m.query(":GA#")
	if err != nil {
		m.mu.Unlock()
		return 0, 0, fmt.Errorf("GA: %w", err)
	}
	azStr, err := m.query(":GZ#")
	m.mu.Unlock()
	if err != nil {
		return 0, 0, fmt.Errorf("GZ: %w", err)
	}
	if alt, err = parseLX200Angle(altStr); err != nil {
		return 0, 0, fmt.Errorf("parse Alt %q: %w", altStr, err)
	}
	if az, err = parseLX200Angle(azStr); err != nil {
		return 0, 0, fmt.Errorf("parse Az %q: %w", azStr, err)
	}
	return alt, az, nil
}

// GetLocation returns the observer's geographic coordinates stored in the mount.
// Uses LX200 :Gt# (latitude) and :Gg# (longitude).
// Longitude follows the LX200 convention: West is positive; callers should
// negate to convert to East-positive (WGS-84).
func (m *Mount) GetLocation() (lat, lonWest float64, err error) {
	latStr, err := m.Cmd(":Gt#")
	if err != nil {
		return 0, 0, fmt.Errorf("Gt: %w", err)
	}
	lonStr, err := m.Cmd(":Gg#")
	if err != nil {
		return 0, 0, fmt.Errorf("Gg: %w", err)
	}
	if lat, err = parseLX200Angle(latStr); err != nil {
		return 0, 0, fmt.Errorf("parse lat %q: %w", latStr, err)
	}
	if lonWest, err = parseLX200Angle(lonStr); err != nil {
		return 0, 0, fmt.Errorf("parse lon %q: %w", lonStr, err)
	}
	return lat, lonWest, nil
}

// EncoderPos reads the raw motor encoder counts from :GY#.
// Returns two 5-digit signed integers (az, el) that increment with each motor step.
// Useful for sub-arcsecond closed-loop correction in a tracking loop.
func (m *Mount) EncoderPos() (az, el int32, err error) {
	resp, err := m.Cmd(":GY#")
	if err != nil {
		return 0, 0, fmt.Errorf("GY: %w", err)
	}
	resp = strings.TrimSpace(resp)
	// Format is "%05d%05d" — two 5-digit values concatenated (total 10 chars).
	if len(resp) < 10 {
		return 0, 0, fmt.Errorf("GY response too short: %q", resp)
	}
	azVal, err := strconv.ParseInt(resp[:5], 10, 32)
	if err != nil {
		return 0, 0, fmt.Errorf("GY az parse %q: %w", resp[:5], err)
	}
	elVal, err := strconv.ParseInt(resp[5:10], 10, 32)
	if err != nil {
		return 0, 0, fmt.Errorf("GY el parse %q: %w", resp[5:10], err)
	}
	return int32(azVal), int32(elVal), nil
}

// Status returns the raw :GU# status string from the mount.
func (m *Mount) Status() (string, error) { return m.Cmd(":GU#") }

// Version returns the firmware version string.
func (m *Mount) Version() (string, error) { return m.Cmd(":GV#") }

// ---- Configuration ----

// SetLocation sets the observer's geographic coordinates in decimal degrees.
func (m *Mount) SetLocation(lat, lon float64) error {
	return m.cmdNoReply(fmt.Sprintf(":SMGE%.6f&%.6f#", lat, lon))
}

// GetClock reads the mount's internal local time (:GL#/:GC#) and its current
// sidereal time (:GS#). The local time is returned as a time.Time stamped UTC
// (no timezone conversion — it reflects whatever the firmware's RTC stores).
// siderealHours is the LST in decimal hours as reported by the firmware.
// Comparing siderealHours against the expected GMST+longitude is the definitive
// check for LST offset errors caused by a stale :SG# UTC offset.
func (m *Mount) GetClock() (localTime time.Time, siderealHours float64, err error) {
	timeStr, err := m.Cmd(":GL#")
	if err != nil {
		return time.Time{}, 0, fmt.Errorf("GL: %w", err)
	}
	dateStr, err := m.Cmd(":GC#")
	if err != nil {
		return time.Time{}, 0, fmt.Errorf("GC: %w", err)
	}
	lstStr, err := m.Cmd(":GS#")
	if err != nil {
		return time.Time{}, 0, fmt.Errorf("GS: %w", err)
	}

	var h, min, sec int
	if _, err := fmt.Sscanf(strings.TrimSpace(timeStr), "%d:%d:%d", &h, &min, &sec); err != nil {
		return time.Time{}, 0, fmt.Errorf("parse time %q: %w", timeStr, err)
	}
	var month, day, year int
	if _, err := fmt.Sscanf(strings.TrimSpace(dateStr), "%d/%d/%d", &month, &day, &year); err != nil {
		return time.Time{}, 0, fmt.Errorf("parse date %q: %w", dateStr, err)
	}
	if year < 100 {
		year += 2000
	}
	localTime = time.Date(year, time.Month(month), day, h, min, sec, 0, time.UTC)

	lstH, lstM, lstS := 0, 0, 0
	if _, err := fmt.Sscanf(strings.TrimSpace(lstStr), "%d:%d:%d", &lstH, &lstM, &lstS); err != nil {
		return localTime, 0, fmt.Errorf("parse LST %q: %w", lstStr, err)
	}
	siderealHours = float64(lstH) + float64(lstM)/60 + float64(lstS)/3600
	return localTime, siderealHours, nil
}

// SetDateTime syncs the mount's real-time clock to the given UTC time.
// Sends :SG+00.0# first to zero any stored timezone offset so the firmware
// treats the following :SL# value as UTC. Without this, a stale offset (e.g.
// -04:00 for EDT) shifts LST by hours, causing GoTo to aim at the wrong AzAlt.
func (m *Mount) SetDateTime(t time.Time) error {
	if err := m.cmdNoReply(":SG+00.0#"); err != nil {
		return fmt.Errorf("SG: %w", err)
	}
	dateCmd := fmt.Sprintf(":SC%02d/%02d/%02d#", int(t.Month()), t.Day(), t.Year()%100)
	timeCmd := fmt.Sprintf(":SL%02d:%02d:%02d#", t.Hour(), t.Minute(), t.Second())
	if err := m.cmdNoReply(dateCmd); err != nil {
		return err
	}
	return m.cmdNoReply(timeCmd)
}

// ---- Helpers ----

func isDir(d string) bool {
	return d == "e" || d == "w" || d == "n" || d == "s"
}

func axisToChar(axis string) (string, error) {
	switch strings.ToLower(axis) {
	case "ra":
		return "r", nil
	case "dec":
		return "d", nil
	default:
		return "", fmt.Errorf("axis must be ra or dec")
	}
}

// formatRADec returns the :Sr# and :Sd# command strings for the given coordinates.
func formatRADec(raHours, decDeg float64) (raCmd, decCmd string) {
	raH := int(raHours)
	raM := int((raHours - float64(raH)) * 60)
	raS := int(math.Round(((raHours-float64(raH))*60-float64(raM))*60))
	raCmd = fmt.Sprintf(":Sr%02d:%02d:%02d#", raH, raM, raS)

	sign := "+"
	d := decDeg
	if d < 0 {
		sign, d = "-", -d
	}
	decD := int(d)
	decM := int((d - float64(decD)) * 60)
	decS := int(math.Round(((d-float64(decD))*60-float64(decM))*60))
	decCmd = fmt.Sprintf(":Sd%s%02d*%02d:%02d#", sign, decD, decM, decS)
	return
}

func parseLX200RA(s string) (float64, error) {
	s = strings.TrimSpace(s)
	var h, min int
	var sec float64
	if _, err := fmt.Sscanf(s, "%d:%d:%f", &h, &min, &sec); err != nil {
		return 0, err
	}
	return float64(h) + float64(min)/60 + sec/3600, nil
}

func parseLX200Dec(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty response")
	}
	sign := 1.0
	if s[0] == '-' {
		sign, s = -1, s[1:]
	} else if s[0] == '+' {
		s = s[1:]
	}
	s = strings.Replace(s, "*", ":", 1)
	var d, min int
	var sec float64
	n, _ := fmt.Sscanf(s, "%d:%d:%f", &d, &min, &sec)
	if n < 2 {
		return 0, fmt.Errorf("cannot parse %q", s)
	}
	return sign * (float64(d) + float64(min)/60 + sec/3600), nil
}

func parseLX200Angle(s string) (float64, error) { return parseLX200Dec(s) }
