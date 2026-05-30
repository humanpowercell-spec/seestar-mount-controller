package mount

import (
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"math"
	"os"
	"time"
)

const (
	// DefaultIIODev is the Linux IIO chardev for the AK09915 magnetometer.
	// This device is exclusively held by zwoair_imager when it is running;
	// stop the imager before calling ReadCompass.
	DefaultIIODev = "/dev/iio:device2"

	// DefaultCalibPath is where the ZWO app stores hard/soft-iron calibration.
	DefaultCalibPath = "/home/pi/.ZWO/ASIAIR_imager.xml"

	// scaleUT converts AK09915 raw LSBs to microtesla.
	// AK09915 sensitivity: 0.0015 Gauss/LSB; 1 Gauss = 100 µT.
	scaleUT = 0.0015 * 100.0 // 0.15 µT/LSB
)

// CompassReading holds one magnetometer measurement.
type CompassReading struct {
	// X, Y, Z are the field components in microtesla, scale-corrected but
	// before any hard/soft-iron calibration is applied.
	X, Y, Z float64

	// Heading is the compass bearing in [0, 360) degrees, true north
	// when calibrated.  Uncalibrated headings use raw atan2(Y, X) and
	// may not align with geographic north.
	Heading float64

	// Calibrated is true when the ZWO hard/soft-iron calibration from
	// DefaultCalibPath was applied to compute Heading.
	Calibrated bool
}

type compassCalib struct {
	hardX, hardY float64
	mat          [2][2]float64
}

// ReadCompass opens DefaultIIODev, reads one AK09915 sample, applies the ZWO
// hard/soft-iron calibration from DefaultCalibPath if available, and returns a
// CompassReading.
//
// The IIO device is held exclusively by zwoair_imager while it is running.
// Stop the imager (or use start_astroscope.py) before calling this function.
func ReadCompass() (CompassReading, error) {
	return readCompassOnce(DefaultIIODev, DefaultCalibPath)
}

// AverageCompass takes n samples at 200 ms intervals and returns a single
// CompassReading whose X/Y/Z are arithmetic means and whose Heading is the
// circular mean (via unit-vector sum) to correctly handle the 0°/360° wrap.
// n must be at least 1.
func AverageCompass(n int) (CompassReading, error) {
	if n < 1 {
		n = 1
	}
	var sumX, sumY, sumZ, sinSum, cosSum float64
	var lastCalib bool
	for i := 0; i < n; i++ {
		if i > 0 {
			time.Sleep(200 * time.Millisecond)
		}
		r, err := readCompassOnce(DefaultIIODev, DefaultCalibPath)
		if err != nil {
			return CompassReading{}, err
		}
		sumX += r.X
		sumY += r.Y
		sumZ += r.Z
		rad := r.Heading * math.Pi / 180
		sinSum += math.Sin(rad)
		cosSum += math.Cos(rad)
		lastCalib = r.Calibrated
	}
	fn := float64(n)
	heading := math.Mod(math.Atan2(sinSum, cosSum)*180/math.Pi+360, 360)
	return CompassReading{
		X: sumX / fn, Y: sumY / fn, Z: sumZ / fn,
		Heading:    heading,
		Calibrated: lastCalib,
	}, nil
}

func readCompassOnce(dev, calibPath string) (CompassReading, error) {
	f, err := os.Open(dev)
	if err != nil {
		return CompassReading{}, fmt.Errorf("open %s: %w (is zwoair_imager still running?)", dev, err)
	}
	defer f.Close()

	var raw [3]int16
	if err := binary.Read(f, binary.LittleEndian, &raw); err != nil {
		return CompassReading{}, fmt.Errorf("read sample from %s: %w", dev, err)
	}
	x := float64(raw[0]) * scaleUT
	y := float64(raw[1]) * scaleUT
	z := float64(raw[2]) * scaleUT

	calib, _ := loadCompassCalib(calibPath)
	if calib == nil {
		// No calibration: raw atan2 — may not align with geographic north.
		heading := math.Mod(math.Atan2(y, x)*180/math.Pi+360, 360)
		return CompassReading{X: x, Y: y, Z: z, Heading: heading, Calibrated: false}, nil
	}

	// Hard-iron correction: remove static bias from nearby ferrous material.
	xc := x - calib.hardX
	yc := y - calib.hardY
	// Soft-iron correction: compensate for field distortion (ellipse → circle).
	xCal := calib.mat[0][0]*xc + calib.mat[0][1]*yc
	yCal := calib.mat[1][0]*xc + calib.mat[1][1]*yc
	// atan2(-xCal, yCal): sensor x-axis ≈ West, y-axis ≈ North in S30 physical mounting.
	heading := math.Mod(math.Atan2(-xCal, yCal)*180/math.Pi+360, 360)
	return CompassReading{X: x, Y: y, Z: z, Heading: heading, Calibrated: true}, nil
}

// loadCompassCalib parses the ZWO imager XML for msensor hard/soft-iron values.
// Returns nil, nil when the file doesn't exist (uncalibrated is valid).
func loadCompassCalib(path string) (*compassCalib, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	// XML structure: <setting2><imager><sensor_calibration><msensor>…
	var doc struct {
		Imager struct {
			SensorCalib struct {
				MSensor struct {
					X   float64 `xml:"x"`
					Y   float64 `xml:"y"`
					X11 float64 `xml:"x11"`
					X12 float64 `xml:"x12"`
					Y11 float64 `xml:"y11"`
					Y12 float64 `xml:"y12"`
				} `xml:"msensor"`
			} `xml:"sensor_calibration"`
		} `xml:"imager"`
	}
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	ms := doc.Imager.SensorCalib.MSensor
	return &compassCalib{
		hardX: ms.X,
		hardY: ms.Y,
		mat:   [2][2]float64{{ms.X11, ms.X12}, {ms.Y11, ms.Y12}},
	}, nil
}
