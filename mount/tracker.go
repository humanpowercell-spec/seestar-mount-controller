package mount

import (
	"context"
	"time"
)

// RateFunc returns the desired dual-axis angular velocity at a given moment.
//
// In alt-az mode (the normal S30 operating mode):
//   axis0 (az)  > 0 = east (clockwise),  < 0 = west
//   axis1 (el)  > 0 = up / north,        < 0 = down / south
//
// In equatorial mode (S30 on a wedge, firmware Mode 1 via :AP#):
//   axis0 (RA)  > 0 = west (decreasing RA), < 0 = east — matches ASCOM MoveAxis convention
//   axis1 (Dec) > 0 = north,                < 0 = south
type RateFunc func(t time.Time) (axis0DegPerSec, axis1DegPerSec float64)

// Track runs a closed-loop tracking loop at the given interval.
// On each tick it calls fn(now) to obtain the desired angular velocities,
// then sends them to the mount via TrackRate.
// The loop stops (and calls Stop) when ctx is cancelled.
// Returns the context error on normal cancellation, or any serial error.
//
// Typical use for LEO satellite tracking:
//
//	fn := func(t time.Time) (float64, float64) {
//	    az, el := sgp4.AngularVelocity(t, sat, observer)
//	    return az, el
//	}
//	err := m.Track(ctx, 100*time.Millisecond, fn)
func (m *Mount) Track(ctx context.Context, interval time.Duration, fn RateFunc) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			_ = m.Stop()
			return ctx.Err()
		case t := <-ticker.C:
			ra, dec := fn(t)
			if err := m.TrackRate(ra, dec); err != nil {
				_ = m.Stop()
				return err
			}
		}
	}
}
