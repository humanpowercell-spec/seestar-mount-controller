package mount

import (
	"context"
	"time"
)

// RateFunc returns the desired dual-axis angular velocity at a given moment.
// azDegPerSec > 0 = east (clockwise),  < 0 = west.
// elDegPerSec > 0 = up / north,        < 0 = down / south.
type RateFunc func(t time.Time) (azDegPerSec, elDegPerSec float64)

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
