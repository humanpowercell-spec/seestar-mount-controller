# seestar-mount-controller

Go library for direct serial control of the **Seestar S30** telescope mount.
Communicates over `/dev/ttyS3` (115200 8N1) using the LX200-compatible protocol
exposed by the ESP32-S3 firmware.

Protocol and command map derived from firmware reverse engineering (Ghidra,
ESP32-S3 main.bin). All axis rate commands are verified direct motor writes —
no internal coordinate transform is applied.

> **Provenance:** [seestar-re](https://github.com/humanpowercell-spec/seestar-re)
> is the authoritative source for every wire-protocol fact this library
> encodes. Every function that makes a protocol claim cites the specific
> seestar-re file/line it comes from in a `// RE:` comment — see
> [RE_PROVENANCE.md](RE_PROVENANCE.md) for the convention, and for the (short)
> list of decompiled-but-unconfirmed commands this library deliberately does
> not implement rather than guess at.

```
go get github.com/humanpowercell-spec/seestar-mount-controller@latest
```

> **Platform note:** the library uses `golang.org/x/sys/unix` for raw serial
> access. Linux only (the mount runs Linux internally and exposes `/dev/ttyS3`
> to processes running on it).

---

## Hardware detection

`ProbeHardware` checks whether the current process is running on Seestar S30
hardware without opening the serial port. Use it to gate hardware control in a
program that may run on non-Seestar hosts.

```go
p := mount.ProbeHardware("") // "" checks DefaultDev

if !p.Detected() {
    log.Println("not running on Seestar hardware, skipping mount control")
    return
}

// p.TTY        — /dev/ttyS3 exists as a character device
// p.ZWOConfig  — /home/pi/ASIAIR/config present
// p.ZWORunning — zwoair_guider or zwoair_imager found in /proc
// p.Alpaca     — /etc/zwo/Alpaca directory present

m, err := mount.Open("")
```

`Detected()` returns true when at least two indicators are present. The
`ZWORunning` check is the strongest single indicator — it confirms both that
the ZWO binaries exist and that the OS is actively running them.

---

## Startup sequence

The firmware boots in home/park mode. You must call `HomeAndWait` before any
motion command will take effect. `HomeAndWait` slews to the home position, waits
for it to complete, then calls `ExitHome` — leaving the mount in motion state
and ready for commands.

```go
package main

import (
    "context"
    "log"
    "time"

    "github.com/humanpowercell-spec/seestar-mount-controller/mount"
)

func main() {
    m, err := mount.Open("") // "" uses /dev/ttyS3
    if err != nil {
        log.Fatal(err)
    }
    defer m.Close()

    ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
    defer cancel()

    if err := m.HomeAndWait(ctx); err != nil {
        log.Fatal("homing failed:", err)
    }

    log.Println("mount ready")
}
```

---

## GoTo

`GotoAndWait` slews to equatorial coordinates and blocks until the mount
arrives within the given tolerance (degrees). Use 0.1° for visual, 0.02° for
satellite acquisition.

```go
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
defer cancel()

// Slew to Vega: RA 18h 36m 56s, Dec +38° 47'
if err := m.GotoAndWait(ctx, 18.6156, 38.78, 0.1); err != nil {
    log.Fatal(err)
}

ra, dec, _ := m.RADec()
log.Printf("pointing RA=%.4fh Dec=%+.4f°", ra, dec)
```

---

## Satellite tracking

`TrackRate` sets both axis angular velocities in deg/s simultaneously.
The rates are direct motor commands — feed the az/el angular velocity
you compute externally; the firmware does not transform them.

- `azDegPerSec > 0` = east (clockwise), `< 0` = west  
- `elDegPerSec > 0` = up, `< 0` = down  
- Maximum: ±6 deg/s per axis

```go
// Set a fixed rate — mount moves northeast at 0.5 deg/s each axis.
if err := m.TrackRate(0.5, 0.5); err != nil {
    log.Fatal(err)
}
time.Sleep(10 * time.Second)
m.Stop()
```

### `TrackRateSY` — single-command, accel-limited

`TrackRateSY` does the same job as `TrackRate` but in one `:SY#` write instead
of four, and the firmware **accel-ramps** the rate toward the target (ESP32
motion "mode 7") rather than stepping it. For a streamed loop it's the better
choice — less serial traffic and smoother direction reversals. A zero on an axis
decelerates that axis to a stop.

```go
m.TrackRateSY(0.5, 0.25) // one :SY+0120+0060# ; firmware ramps to it
```

`TrackSY(ctx, interval, fn)` is `Track` wired to `TrackRateSY`.

For closed-loop tracking, `Track` (or `TrackSY`) calls your `RateFunc` on each
tick and sends the result to the mount. The loop exits cleanly when the context
is cancelled.

```go
// RateFunc signature: func(t time.Time) (azDegPerSec, elDegPerSec float64)
fn := func(t time.Time) (float64, float64) {
    // Replace with your SGP4 / propagator call.
    az, el := myPropagator.AngularVelocity(t)
    return az, el
}

ctx, cancel := context.WithCancel(context.Background())
defer cancel() // stop tracking on return

if err := m.Track(ctx, 100*time.Millisecond, fn); err != nil && err != context.Canceled {
    log.Fatal(err)
}
```

---

## Full satellite pass example

```go
package main

import (
    "context"
    "log"
    "time"

    "github.com/humanpowercell-spec/seestar-mount-controller/mount"
)

func main() {
    m, err := mount.Open("")
    if err != nil {
        log.Fatal(err)
    }
    defer m.Close()

    // 1. Sync time and location so GoTo pointing is correct.
    if err := m.SetLocation(37.7749, -122.4194); err != nil {
        log.Fatal(err)
    }
    if err := m.SetDateTime(time.Now().UTC()); err != nil {
        log.Fatal(err)
    }

    // 2. Home (required after every power-on).
    ctx := context.Background()
    if err := m.HomeAndWait(ctx); err != nil {
        log.Fatal(err)
    }

    // 3. Slew to acquisition point (where the satellite will rise).
    if err := m.GotoAndWait(ctx, 18.0, 45.0, 0.05); err != nil {
        log.Fatal(err)
    }

    // 4. Track the satellite for 90 seconds.
    trackCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
    defer cancel()

    fn := func(t time.Time) (float64, float64) {
        // Your propagator returns az/el angular velocity in deg/s.
        return myPropagator.AngularVelocity(t)
    }

    if err := m.Track(trackCtx, 100*time.Millisecond, fn); err != nil && err != context.Canceled {
        log.Fatal(err)
    }

    log.Println("pass complete")
}
```

---

## Reading position

```go
// Equatorial
ra, dec, err := m.RADec()
log.Printf("RA=%.6fh  Dec=%+.6f°", ra, dec)

// Horizontal
alt, az, err := m.AltAz()
log.Printf("Alt=%+.4f°  Az=%.4f°", alt, az)

// Raw encoder counts (sub-arcsecond resolution)
azEnc, elEnc, err := m.EncoderPos()
log.Printf("encoder az=%d  el=%d", azEnc, elEnc)
```

---

## API reference

| Method | Description |
|--------|-------------|
| `ProbeHardware(dev string) HardwareProbe` | Check for Seestar S30 environment indicators without opening the port. |
| `(HardwareProbe).Detected() bool` | True if two or more indicators are present. |
| `Open(dev string) (*Mount, error)` | Open serial port. Pass `""` for `/dev/ttyS3`. |
| `Close() error` | Release the port. |
| `HomeAndWait(ctx) error` | Home, wait for completion, exit home mode. Normal startup call. |
| `GotoCoords(raHours, decDeg float64) error` | Start GoTo slew (non-blocking). |
| `GotoAndWait(ctx, raHours, decDeg, toleranceDeg float64) error` | GoTo + block until arrived. |
| `WaitGoTo(ctx, toleranceDeg float64) error` | Block until last GoTo completes. |
| `TrackRate(azDeg/s, elDeg/s float64) error` | Set both axis rates directly (`:Rvr#`/`:Rvd#`). |
| `TrackRateSY(azDeg/s, elDeg/s float64) error` | Same in one accel-limited `:SY#` command (motion mode 7). Preferred for a streamed loop. |
| `Track(ctx, interval, RateFunc) error` | Closed-loop tracking loop (uses `TrackRate`). |
| `TrackSY(ctx, interval, RateFunc) error` | Closed-loop tracking loop (uses `TrackRateSY`). |
| `Stop() error` | Stop both axes. |
| `StopRA() error` | Stop azimuth axis only. |
| `StopDec() error` | Stop elevation axis only. |
| `Slew(dir, speed) error` | Preset-rate slew (1–9). |
| `SlewRate(dir, degPerSec) error` | Variable-rate continuous slew. |
| `SetTracking(mode) error` | `"sidereal"`, `"solar"`, or `"lunar"`. No-op in normal operating state (see Notes). |
| `EnableTracking(bool) error` | Enable / disable autonomous tracking. No-op in normal operating state (see Notes). |
| `RADec() (float64, float64, error)` | Current RA (hours) and Dec (degrees). |
| `AltAz() (float64, float64, error)` | Current altitude and azimuth (degrees). |
| `EncoderPos() (int32, int32, error)` | Raw motor encoder counts (az, el). |
| `Status() (string, error)` | Raw `:GU#` status string. |
| `Version() (string, error)` | Firmware version string. |
| `SetLocation(lat, lon float64) error` | Observer coordinates (decimal degrees). |
| `SetDateTime(time.Time) error` | Sync mount RTC to given UTC time. |
| `SyncPosition(raHours, decDeg float64) error` | Update internal position registers without moving. |
| `SyncMotors() error` | Sync motor registers to current position. |
| `Reset() error` | Hard-reset the ESP32-S3. |
| `Cmd(cmd string) (string, error)` | Send raw LX200 command, read reply. |
| `GetHomeFlag() (bool, error)` | Home/DST flag (`:GH#`) — unlike `HomeStatus`, pollable outside an active homing sequence. |
| `GetBacklashSlot1() / GetBacklashSlot2() (BacklashReading, error)` | Read the two backlash-compensation runtime slots, arcsec (`:GBGR#/:GBGD#`, `:GBZR#/:GBZD#`). Read-only — see Notes on why there's no setter. |
| `GetBacklashMode() (string, error)` / `SetBacklashMode(0-2) error` | Raw `:GBu#` reply / `:SBu{n}#` — wire format confirmed, firmware-side effect of each mode is not. |
| `SetIlluminatorLED(bool) error` | ESP32 GPIO3+45 IR LED (`:FTE#`/`:FTD#`) — despite the LX200 naming, unrelated to the EAF focuser. |

### Accessories (separate device nodes, not the serial port)

| Method | Description |
|--------|-------------|
| `OpenFocuser(dev string) (*Focuser, error)` | EAF telephoto focuser via `/dev/eaf` ioctls. |
| `(Focuser).Goto(target uint32) error` / `GotoAndWait(ctx, target)` | Absolute move in microsteps (0–`MaxPos`, ~3040 on the S30). |
| `(Focuser).Pos() / MaxPos() / IsMoving() / Halt()` | State + control. |
| `OpenFilterWheel(dev string) (*FilterWheel, error)` | Built-in filter wheel via `/dev/pwm-gpio-misc`. GPIO/coil infrastructure only — `Step(a,b)` primitive; the full position sequence isn't reverse-engineered yet. |
| `ReadCompass() (CompassReading, error)` / `AverageCompass(n)` | AK09915 magnetometer heading (`/dev/iio:device2`, held by `zwoair_imager`). |
| `Declination(lat, lon, t) float64` | WMM2025 magnetic declination for true-north correction. |
| `ImagerMode(ctx, host) (ScopeMode, error)` | Probe the imager's alt-az / equatorial mode (wedge detection). |

---

## CLI tool

`seestar-ctrl` is a thin CLI wrapper over the library for manual control and scripting.

```bash
go install github.com/humanpowercell-spec/seestar-mount-controller/cmd/seestar-ctrl@latest

seestar-ctrl probe               # check hardware indicators; exits 1 if not detected
seestar-ctrl home
seestar-ctrl gotowait 18.6156 38.78 0.1
seestar-ctrl trackrate 0.5 0.2
seestar-ctrl trackrate-sy 0.5 0.2   # single :SY# command (motion mode 7)
seestar-ctrl stop
seestar-ctrl radec
seestar-ctrl backlash              # both slots, arcsec
seestar-ctrl homeflag              # pollable home/DST check
seestar-ctrl led on
seestar-ctrl raw ':GU#'
```

`probe` is useful as a guard in shell scripts:

```bash
seestar-ctrl probe && seestar-ctrl home
```

Run `seestar-ctrl` with no arguments for the full command list.

---

## Notes

- Stop `zwoair_guider` before running these programs — both processes would otherwise fight over `/dev/ttyS3`. Use `ProbeHardware` to detect whether it is running (`ZWORunning` field) before attempting `Open`.
- The firmware enforces a **5 ms minimum reply latency** (`command_task` sleep) and a **50-byte reply cap**. Neither affects `TrackRate` or `Track`, which send fire-and-forget commands.
- Rate units: the firmware uses *sidereal multiples* (1 unit = 15 arcsec/s). The library converts from deg/s transparently. Maximum is 6 deg/s (1440 sidereal multiples) per axis.
- `TrackRate` is concurrency-safe. A position-polling goroutine and a tracking goroutine can share the same `*Mount`.
- **`SetTracking` and `EnableTracking` are no-ops in normal operating state.** After `HomeAndWait`, the firmware intercepts all `T`-prefix LX200 commands and silently discards them (firmware state 3, `FUN_42007394`). To stop autonomous tracking use `Stop()` or `TrackRate(0, 0)` — both clear the tracking-enable bits directly via the stop command path.
- **No backlash arcsec *setter*.** The getters and the mode setter are here, but the `:SB…#` sub-command letters for writing a per-slot/per-axis arcsec value were never decompiled in seestar-re — only the NVS-load-time appliers are confirmed, not the LX200 dispatch that reaches them. Configure backlash via the ZWO app until that's pinned; see `RE_PROVENANCE.md`.
- **No `Park()`.** `:GP#`/`:SP#` are only listed by address in seestar-re — the argument/response format was never decompiled. Use `Home()` (`:hC#`), which is fully confirmed.
- **Backlash compensation is not applied to `TrackRate`/`TrackRateSY`/`SlewRate`.** The firmware only takes up gear lash on direction reversal inside the GoTo/directional-move planners (`GotoCoords`/`Slew`/`SyncPosition`'s `:MS#`). A tracker that reverses an axis via `TrackRate`/`TrackRateSY` (e.g. across the meridian) gets no firmware help — read `GetBacklashSlot1`/`GetBacklashSlot2` and inject a compensating move yourself if needed.

## Command coverage

Commands from `docs/esp32_firmware.md` § Full Command Map deliberately **not**
wrapped here, and why:

| Command(s) | Reason |
|---|---|
| `:Xa#`/`:Xb#` (raw encoder) | Superseded by `:GY#` (`EncoderPos`) |
| `:Xc#`/`:Xd#` (direction flags) | Internal state, not meant to be user-set |
| `:Td#`/`:Te#`, `:Qw#`/`:Qs#` | Legacy aliases of `:TO0/1#` (`EnableTracking`) and `:Qe#`/`:Qn#` (`StopRA`/`StopDec`) |
| `:GB#`/`:Gh#`/`:GE#` (beep/elev-limit/eq-mode getters) | Never observed in use by the stock app during RE |
| `:GM*#`/`:SM*#` combined multi-get/set (except `:SMGE#`, used by `SetLocation`) | Pure convenience over fields already exposed individually |
| `:AA#`/`:AP#` (mode transitions) | Hardware-mode-fixed at boot (ADC-detected), not meant to be switched live |
| `:SR#`/`:SA#`/`:SW#` | Redundant with `SetRate`/`SlewRate` and `SetTracking`+`SyncPosition` |
| `:C…#` (motion confirm/enable/disable/reset) | Handler is inline in the ESP32 dispatch table, never decompiled — semantics unclear even in seestar-re |
| `:SB…#` arcsec setter, `:GP#`/`:SP#` (Park) | Wire format not decompiled — see Notes above and `RE_PROVENANCE.md` |
