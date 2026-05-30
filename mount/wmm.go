package mount

import (
	"math"
	"time"
)

// Declination returns the magnetic declination in degrees at the given WGS84
// geodetic latitude and longitude for the given time, using the WMM2025 model.
// Positive declination means magnetic north is east of true north.
//
// Altitude is assumed to be zero (surface). The error this introduces is
// < 0.05° at 500 m elevation — negligible for compass heading use.
//
// The model is valid 2025–2030. Results outside this range are extrapolated
// linearly and will degrade in accuracy.
func Declination(latDeg, lonDeg float64, t time.Time) float64 {
	return wmm2025.declination(latDeg, lonDeg, t)
}

// wmmModel holds the parsed WMM Gauss coefficients.
type wmmModel struct {
	epoch float64
	g     [13][13]float64 // g[n][m], nT
	h     [13][13]float64 // h[n][m], nT
	gd    [13][13]float64 // secular variation g, nT/yr
	hd    [13][13]float64 // secular variation h, nT/yr
}

// declination evaluates the WMM at the given location and time.
func (w *wmmModel) declination(latDeg, lonDeg float64, t time.Time) float64 {
	const wmmA = 6371.2 // WMM reference radius, km

	// Decimal year
	yr := decimalYear(t)
	dt := yr - w.epoch

	// Geodetic → geocentric (altitude = 0, WGS84)
	gcLat, rKm := geodeticToGeocentric(latDeg)

	// Colat variables: x = sin(gcLat) = cos(colat), pp = cos(gcLat) = sin(colat)
	x := math.Sin(gcLat)
	pp := math.Cos(gcLat)

	// Longitude trig: sp[m] = sin(m*λ), cp[m] = cos(m*λ)
	lonRad := lonDeg * math.Pi / 180
	var sp, cp [13]float64
	sp[0] = 0; sp[1] = math.Sin(lonRad)
	cp[0] = 1; cp[1] = math.Cos(lonRad)
	for m := 2; m <= 12; m++ {
		sp[m] = sp[1]*cp[m-1] + cp[1]*sp[m-1]
		cp[m] = cp[1]*cp[m-1] - sp[1]*sp[m-1]
	}

	// Schmidt semi-normalised Legendre polynomials P[n][m].
	// Recursion:
	//   P[1][0] = x,  P[1][1] = pp
	//   P[n][n] = pp * sqrt((2n-1)/(2n)) * P[n-1][n-1]          (diagonal)
	//   P[n][m] = [(2n-1)*x*P[n-1][m] - sqrt((n-1)²-m²)*P[n-2][m]] / sqrt(n²-m²)
	var p [13][13]float64
	p[0][0] = 1
	p[1][0] = x
	p[1][1] = pp
	for n := 2; n <= 12; n++ {
		p[n][n] = pp * math.Sqrt(float64(2*n-1)/float64(2*n)) * p[n-1][n-1]
		for m := 0; m < n; m++ {
			nm2 := float64(n*n - m*m)
			bm2 := float64((n-1)*(n-1) - m*m)
			prev := 0.0
			if m <= n-2 {
				prev = p[n-2][m]
			}
			p[n][m] = (float64(2*n-1)*x*p[n-1][m] - math.Sqrt(math.Max(0, bm2))*prev) / math.Sqrt(nm2)
		}
	}

	// Accumulate north (Bx) and east (By) field components.
	//
	// X = -∑ (a/r)^{n+2} * [g*cp[m]+h*sp[m]] * dP[n][m]/dθ
	// Y =  ∑ (a/r)^{n+2} / sinθ * m * [-g*sp[m]+h*cp[m]] * P[n][m]
	//
	// dP[n][m]/dθ = (n*cosθ*P[n][m] - sqrt(n²-m²)*P[n-1][m]) / sinθ
	//             = (n*x*P[n][m]     - sqrt(n²-m²)*P[n-1][m]) / pp
	//
	// Both have a 1/sinθ = 1/pp factor; handle near-pole with a small guard.
	var Bx, By float64
	ppSafe := math.Max(pp, 1e-10)
	for n := 1; n <= 12; n++ {
		ar := math.Pow(wmmA/rKm, float64(n+2))
		for m := 0; m <= n; m++ {
			gEff := w.g[n][m] + dt*w.gd[n][m]
			hEff := w.h[n][m] + dt*w.hd[n][m]

			gcm := gEff*cp[m] + hEff*sp[m]  // [g cos + h sin] factor
			hcm := -gEff*sp[m] + hEff*cp[m] // [-g sin + h cos] factor (for Y)

			// dP/dθ = (n*x*P[n][m] - sqrt(n²-m²)*P[n-1][m]) / pp
			prevP := 0.0
			if m < n {
				prevP = p[n-1][m]
			}
			nm2 := float64(n*n - m*m)
			dpdt := (float64(n)*x*p[n][m] - math.Sqrt(nm2)*prevP) / ppSafe

			Bx += ar * gcm * dpdt
			By -= ar * float64(m) * hcm * p[n][m] / ppSafe
		}
	}

	// D = atan2(Y, X) in degrees
	return math.Atan2(By, Bx) * 180 / math.Pi
}

// decimalYear converts a time.Time to a fractional year (e.g. 2025.5).
func decimalYear(t time.Time) float64 {
	y := float64(t.Year())
	startOfYear := time.Date(t.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
	startOfNext := time.Date(t.Year()+1, 1, 1, 0, 0, 0, 0, time.UTC)
	return y + float64(t.Sub(startOfYear))/float64(startOfNext.Sub(startOfYear))
}

// geodeticToGeocentric converts a WGS84 geodetic latitude (degrees) at
// altitude 0 to geocentric latitude (radians) and geocentric radius (km).
func geodeticToGeocentric(latDeg float64) (gcLatRad, rKm float64) {
	const (
		a = 6378.137    // WGS84 semi-major axis, km
		b = 6356.752314 // WGS84 semi-minor axis, km
	)
	lat := latDeg * math.Pi / 180
	sinL, cosL := math.Sin(lat), math.Cos(lat)
	D := math.Sqrt(a*a*cosL*cosL + b*b*sinL*sinL)
	p := a * a * cosL / D
	z := b * b * sinL / D
	rKm = math.Sqrt(p*p + z*z)
	gcLatRad = math.Atan2(z, p)
	return
}

// wmm2025 is the WMM2025 model coefficients (epoch 2025.0, valid 2025–2030).
// Source: NOAA/NCEI WMM2025, released 2024-12-17.
// https://www.ngdc.noaa.gov/geomag/WMM/data/WMM2025/wmm2025_Linux.zip
var wmm2025 = func() wmmModel {
	type row struct{ n, m int; g, h, gd, hd float64 }
	rows := []row{
		{1, 0, -29351.8, 0.0, 12.0, 0.0},
		{1, 1, -1410.8, 4545.4, 9.7, -21.5},
		{2, 0, -2556.6, 0.0, -11.6, 0.0},
		{2, 1, 2951.1, -3133.6, -5.2, -27.7},
		{2, 2, 1649.3, -815.1, -8.0, -12.1},
		{3, 0, 1361.0, 0.0, -1.3, 0.0},
		{3, 1, -2404.1, -56.6, -4.2, 4.0},
		{3, 2, 1243.8, 237.5, 0.4, -0.3},
		{3, 3, 453.6, -549.5, -15.6, -4.1},
		{4, 0, 895.0, 0.0, -1.6, 0.0},
		{4, 1, 799.5, 278.6, -2.4, -1.1},
		{4, 2, 55.7, -133.9, -6.0, 4.1},
		{4, 3, -281.1, 212.0, 5.6, 1.6},
		{4, 4, 12.1, -375.6, -7.0, -4.4},
		{5, 0, -233.2, 0.0, 0.6, 0.0},
		{5, 1, 368.9, 45.4, 1.4, -0.5},
		{5, 2, 187.2, 220.2, 0.0, 2.2},
		{5, 3, -138.7, -122.9, 0.6, 0.4},
		{5, 4, -142.0, 43.0, 2.2, 1.7},
		{5, 5, 20.9, 106.1, 0.9, 1.9},
		{6, 0, 64.4, 0.0, -0.2, 0.0},
		{6, 1, 63.8, -18.4, -0.4, 0.3},
		{6, 2, 76.9, 16.8, 0.9, -1.6},
		{6, 3, -115.7, 48.8, 1.2, -0.4},
		{6, 4, -40.9, -59.8, -0.9, 0.9},
		{6, 5, 14.9, 10.9, 0.3, 0.7},
		{6, 6, -60.7, 72.7, 0.9, 0.9},
		{7, 0, 79.5, 0.0, -0.0, 0.0},
		{7, 1, -77.0, -48.9, -0.1, 0.6},
		{7, 2, -8.8, -14.4, -0.1, 0.5},
		{7, 3, 59.3, -1.0, 0.5, -0.8},
		{7, 4, 15.8, 23.4, -0.1, 0.0},
		{7, 5, 2.5, -7.4, -0.8, -1.0},
		{7, 6, -11.1, -25.1, -0.8, 0.6},
		{7, 7, 14.2, -2.3, 0.8, -0.2},
		{8, 0, 23.2, 0.0, -0.1, 0.0},
		{8, 1, 10.8, 7.1, 0.2, -0.2},
		{8, 2, -17.5, -12.6, 0.0, 0.5},
		{8, 3, 2.0, 11.4, 0.5, -0.4},
		{8, 4, -21.7, -9.7, -0.1, 0.4},
		{8, 5, 16.9, 12.7, 0.3, -0.5},
		{8, 6, 15.0, 0.7, 0.2, -0.6},
		{8, 7, -16.8, -5.2, -0.0, 0.3},
		{8, 8, 0.9, 3.9, 0.2, 0.2},
		{9, 0, 4.6, 0.0, -0.0, 0.0},
		{9, 1, 7.8, -24.8, -0.1, -0.3},
		{9, 2, 3.0, 12.2, 0.1, 0.3},
		{9, 3, -0.2, 8.3, 0.3, -0.3},
		{9, 4, -2.5, -3.3, -0.3, 0.3},
		{9, 5, -13.1, -5.2, 0.0, 0.2},
		{9, 6, 2.4, 7.2, 0.3, -0.1},
		{9, 7, 8.6, -0.6, -0.1, -0.2},
		{9, 8, -8.7, 0.8, 0.1, 0.4},
		{9, 9, -12.9, 10.0, -0.1, 0.1},
		{10, 0, -1.3, 0.0, 0.1, 0.0},
		{10, 1, -6.4, 3.3, 0.0, 0.0},
		{10, 2, 0.2, 0.0, 0.1, -0.0},
		{10, 3, 2.0, 2.4, 0.1, -0.2},
		{10, 4, -1.0, 5.3, -0.0, 0.1},
		{10, 5, -0.6, -9.1, -0.3, -0.1},
		{10, 6, -0.9, 0.4, 0.0, 0.1},
		{10, 7, 1.5, -4.2, -0.1, 0.0},
		{10, 8, 0.9, -3.8, -0.1, -0.1},
		{10, 9, -2.7, 0.9, -0.0, 0.2},
		{10, 10, -3.9, -9.1, -0.0, -0.0},
		{11, 0, 2.9, 0.0, 0.0, 0.0},
		{11, 1, -1.5, 0.0, -0.0, -0.0},
		{11, 2, -2.5, 2.9, 0.0, 0.1},
		{11, 3, 2.4, -0.6, 0.0, -0.0},
		{11, 4, -0.6, 0.2, 0.0, 0.1},
		{11, 5, -0.1, 0.5, -0.1, -0.0},
		{11, 6, -0.6, -0.3, 0.0, -0.0},
		{11, 7, -0.1, -1.2, -0.0, 0.1},
		{11, 8, 1.1, -1.7, -0.1, -0.0},
		{11, 9, -1.0, -2.9, -0.1, 0.0},
		{11, 10, -0.2, -1.8, -0.1, 0.0},
		{11, 11, 2.6, -2.3, -0.1, 0.0},
		{12, 0, -2.0, 0.0, 0.0, 0.0},
		{12, 1, -0.2, -1.3, 0.0, -0.0},
		{12, 2, 0.3, 0.7, -0.0, 0.0},
		{12, 3, 1.2, 1.0, -0.0, -0.1},
		{12, 4, -1.3, -1.4, -0.0, 0.1},
		{12, 5, 0.6, -0.0, -0.0, -0.0},
		{12, 6, 0.6, 0.6, 0.1, -0.0},
		{12, 7, 0.5, -0.1, -0.0, -0.0},
		{12, 8, -0.1, 0.8, 0.0, 0.0},
		{12, 9, -0.4, 0.1, 0.0, -0.0},
		{12, 10, -0.2, -1.0, -0.1, -0.0},
		{12, 11, -1.3, 0.1, -0.0, 0.0},
		{12, 12, -0.7, 0.2, -0.1, -0.1},
	}
	var w wmmModel
	w.epoch = 2025.0
	for _, r := range rows {
		w.g[r.n][r.m] = r.g
		w.h[r.n][r.m] = r.h
		w.gd[r.n][r.m] = r.gd
		w.hd[r.n][r.m] = r.hd
	}
	return w
}()
