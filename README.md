# seestar-mount-controller

Go library for direct serial control of the **Seestar S30** telescope mount.
Communicates over `/dev/ttyS3` (115200 8N1) using the LX200-compatible protocol
exposed by the ESP32-S3 firmware.

Protocol and command map derived from firmware reverse engineering (Ghidra,
ESP32-S3 main.bin). All axis rate commands are verified direct motor writes —
no internal coordinate transform is applied.

```
go get github.com/humanpowercell-spec/seestar-mount-controller-@latest
```

> **Platform note:** the library uses `golang.org/x/sys/unix` for raw serial
> access. Linux only (the mount runs Linux internally and exposes `/dev/ttyS3`
> to processes running on it).

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

    "github.com/humanpowercell-spec/seestar-mount-controller-/mount"
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

For closed-loop tracking, `Track` calls your `RateFunc` on each tick and
sends the result to `TrackRate`. The loop exits cleanly when the context is
cancelled.

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

    "github.com/humanpowercell-spec/seestar-mount-controller-/mount"
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
| `Open(dev string) (*Mount, error)` | Open serial port. Pass `""` for `/dev/ttyS3`. |
| `Close() error` | Release the port. |
| `HomeAndWait(ctx) error` | Home, wait for completion, exit home mode. Normal startup call. |
| `GotoCoords(raHours, decDeg float64) error` | Start GoTo slew (non-blocking). |
| `GotoAndWait(ctx, raHours, decDeg, toleranceDeg float64) error` | GoTo + block until arrived. |
| `WaitGoTo(ctx, toleranceDeg float64) error` | Block until last GoTo completes. |
| `TrackRate(azDeg/s, elDeg/s float64) error` | Set both axis rates directly. Primary tracking primitive. |
| `Track(ctx, interval, RateFunc) error` | Closed-loop tracking loop. |
| `Stop() error` | Stop both axes. |
| `StopRA() error` | Stop azimuth axis only. |
| `StopDec() error` | Stop elevation axis only. |
| `Slew(dir, speed) error` | Preset-rate slew (1–9). |
| `SlewRate(dir, degPerSec) error` | Variable-rate continuous slew. |
| `SetTracking(mode) error` | `"sidereal"`, `"solar"`, or `"lunar"`. |
| `EnableTracking(bool) error` | Enable / disable autonomous tracking. |
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

---

## CLI tool

`seestar-ctrl` is a thin CLI wrapper over the library for manual control and scripting.

```bash
go install github.com/humanpowercell-spec/seestar-mount-controller-/cmd/seestar-ctrl@latest

seestar-ctrl home
seestar-ctrl gotowait 18.6156 38.78 0.1
seestar-ctrl trackrate 0.5 0.2
seestar-ctrl stop
seestar-ctrl radec
seestar-ctrl raw ':GU#'
```

Run `seestar-ctrl` with no arguments for the full command list.

---

## Notes

- `Stop it` before running these programs if `zwoair_guider` is running on the telescope — both processes would otherwise fight over `/dev/ttyS3`.
- The firmware enforces a **5 ms minimum reply latency** (`command_task` sleep) and a **50-byte reply cap**. Neither affects `TrackRate` or `Track`, which send fire-and-forget commands.
- Rate units: the firmware uses *sidereal multiples* (1 unit = 15 arcsec/s). The library converts from deg/s transparently. Maximum is 6 deg/s (1440 sidereal multiples) per axis.
- `TrackRate` is concurrency-safe. A position-polling goroutine and a tracking goroutine can share the same `*Mount`.
