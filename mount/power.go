package mount

// Battery / charger / thermal telemetry — standard Linux power_supply and
// thermal sysfs classes, no ZWO-specific interface.
//
// RE: seestar-re/docs/peripherals.md § "Power / battery / thermal — sysfs"
// (CW2217 fuel gauge → /sys/class/power_supply/battery, BQ25890 charger →
// /sys/class/power_supply/bq25890-charger, SoC thermal zones).

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	sysBattery = "/sys/class/power_supply/battery"
	sysCharger = "/sys/class/power_supply/bq25890-charger"
	sysThermal = "/sys/class/thermal"
)

// PowerStatus is a snapshot of the battery and charger.
type PowerStatus struct {
	// Battery (CW2217 gauge).
	CapacityPct   int     // 0–100
	CapacityLevel string  // "Normal", "Low", ...
	Status        string  // "Charging" / "Discharging" / "Full" / "Not charging"
	Health        string  // "Good", ...
	VoltageV      float64 // pack voltage
	CurrentA      float64 // pack current, signed (+ = into battery while charging)
	TempC         float64 // battery temperature
	ChargeFullAh  float64 // present full-charge capacity
	TimeToFull    int     // seconds, -1 if unknown / not charging

	// Charger (BQ25890).
	VBUSPresent    bool
	ChargeType     string // "Fast" / "Trickle" / "N/A"
	VBUSVoltageV   float64
	ChargeCurrentA float64
	InputLimitA    float64
}

// ReadPowerStatus reads the battery + charger sysfs nodes.
func ReadPowerStatus() (PowerStatus, error) {
	var p PowerStatus
	p.CapacityPct = sysInt(sysBattery, "capacity")
	p.CapacityLevel = sysStr(sysBattery, "capacity_level")
	p.Status = sysStr(sysBattery, "status")
	p.Health = sysStr(sysBattery, "health")
	p.VoltageV = float64(sysInt(sysBattery, "voltage_now")) / 1e6
	p.CurrentA = float64(sysInt(sysBattery, "current_now")) / 1e6
	p.TempC = float64(sysInt(sysBattery, "temp")) / 10
	p.ChargeFullAh = float64(sysInt(sysBattery, "charge_full")) / 1e6
	if ttf, ok := sysIntOK(sysBattery, "time_to_full_now"); ok {
		p.TimeToFull = ttf
	} else {
		p.TimeToFull = -1
	}

	p.VBUSPresent = sysInt(sysCharger, "online") == 1
	p.ChargeType = sysStr(sysCharger, "charge_type")
	p.VBUSVoltageV = float64(sysInt(sysCharger, "voltage_now")) / 1e6
	p.ChargeCurrentA = float64(sysInt(sysCharger, "current_now")) / 1e6
	p.InputLimitA = float64(sysInt(sysCharger, "input_current_limit")) / 1e6
	return p, nil
}

// Temperature is one thermal zone reading.
type Temperature struct {
	Type string  // "cpu-thermal", "battery", "bq25890-charger"
	C    float64 // degrees Celsius
}

// ReadTemperatures returns every /sys/class/thermal zone.
func ReadTemperatures() ([]Temperature, error) {
	zones, err := filepath.Glob(filepath.Join(sysThermal, "thermal_zone*"))
	if err != nil {
		return nil, err
	}
	var out []Temperature
	for _, z := range zones {
		milliC, ok := sysIntOK(z, "temp")
		if !ok {
			continue
		}
		out = append(out, Temperature{Type: sysStr(z, "type"), C: float64(milliC) / 1000})
	}
	return out, nil
}

func sysStr(dir, name string) string {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func sysIntOK(dir, name string) (int, bool) {
	s := sysStr(dir, name)
	if s == "" {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return n, true
}

func sysInt(dir, name string) int {
	n, _ := sysIntOK(dir, name)
	return n
}
