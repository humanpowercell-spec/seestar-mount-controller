package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/humanpowercell-spec/seestar-mount-controller/mount"
)

func main() {
	dev := flag.String("dev", mount.DefaultDev, "serial device")
	flag.Parse()
	args := flag.Args()

	if len(args) == 0 {
		usage()
		os.Exit(1)
	}

	// probe and compass do not open the serial port.
	if args[0] == "probe" {
		p := mount.ProbeHardware(*dev)
		fmt.Printf("tty          %v  (%s)\n", p.TTY, *dev)
		fmt.Printf("zwo_config   %v\n", p.ZWOConfig)
		fmt.Printf("zwo_running  %v\n", p.ZWORunning)
		fmt.Printf("alpaca       %v\n", p.Alpaca)
		fmt.Printf("compass      %v  (%s)\n", p.Compass, mount.DefaultIIODev)
		if p.Detected() {
			fmt.Println("detected     yes")
		} else {
			fmt.Println("detected     no")
			os.Exit(1)
		}
		return
	}

	if args[0] == "compass" {
		compassFlags := flag.NewFlagSet("compass", flag.ExitOnError)
		n := compassFlags.Int("avg", 1, "number of samples to average")
		compassFlags.Parse(args[1:]) //nolint:errcheck
		var r mount.CompassReading
		var err error
		if *n > 1 {
			r, err = mount.AverageCompass(*n)
		} else {
			r, err = mount.ReadCompass()
		}
		if err != nil {
			fatalf("compass: %v", err)
		}
		caliStr := "uncalibrated"
		if r.Calibrated {
			caliStr = "calibrated"
		}
		fmt.Printf("Heading: %.1f°  X: %.3f µT  Y: %.3f µT  Z: %.3f µT  [%s]\n",
			r.Heading, r.X, r.Y, r.Z, caliStr)
		return
	}

	if args[0] == "declination" {
		if len(args) < 3 {
			fatalf("usage: declination <lat> <lon>")
		}
		lat, err := strconv.ParseFloat(args[1], 64)
		if err != nil {
			fatalf("lat: %v", err)
		}
		lon, err := strconv.ParseFloat(args[2], 64)
		if err != nil {
			fatalf("lon: %v", err)
		}
		decl := mount.Declination(lat, lon, time.Now())
		fmt.Printf("Declination: %+.2f°  (WMM2025, positive = magnetic east of true north)\n", decl)
		return
	}

	m, err := mount.Open(*dev)
	if err != nil {
		fatalf("open: %v", err)
	}
	defer m.Close()

	switch args[0] {
	case "slew":
		requireArgs(args, 3, "slew <e|w|n|s> <speed 1-9>")
		speed, err := strconv.Atoi(args[2])
		if err != nil {
			fatalf("speed: %v", err)
		}
		check(m.Slew(args[1], speed))

	case "slewrate":
		requireArgs(args, 3, "slewrate <e|w|n|s> <deg/s>")
		rate, err := strconv.ParseFloat(args[2], 64)
		if err != nil {
			fatalf("rate: %v", err)
		}
		check(m.SlewRate(args[1], rate))

	case "setrate":
		requireArgs(args, 3, "setrate <ra|dec> <deg/s>")
		rate, err := strconv.ParseFloat(args[2], 64)
		if err != nil {
			fatalf("rate: %v", err)
		}
		check(m.SetRate(args[1], rate))

	case "pulsemove":
		requireArgs(args, 3, "pulsemove <e|w|n|s> <dur 0-99>")
		dur, err := strconv.Atoi(args[2])
		if err != nil {
			fatalf("dur: %v", err)
		}
		check(m.PulseMove(args[1], dur))

	case "stop":
		check(m.Stop())

	case "stopra":
		check(m.StopRA())

	case "stopdec":
		check(m.StopDec())

	case "home":
		check(m.Home())

	case "exithome":
		check(m.ExitHome())
		fmt.Println("home mode cleared (firmware now in motion state)")

	case "homestatus":
		done, err := m.HomeStatus()
		check(err)
		if done {
			fmt.Println("homed: yes")
		} else {
			fmt.Println("homed: no")
		}

	case "tracking":
		requireArgs(args, 2, "tracking <on|off>")
		switch strings.ToLower(args[1]) {
		case "on":
			check(m.EnableTracking(true))
		case "off":
			check(m.EnableTracking(false))
		default:
			fatalf("tracking: must be on or off")
		}

	case "sync":
		check(m.SyncMotors())
		fmt.Println("sync sent")

	case "reset":
		fmt.Fprintln(os.Stderr, "seestar-ctrl: sending :AR# — mount will reboot")
		check(m.Reset())

	case "goto":
		requireArgs(args, 3, "goto <ra_hours> <dec_deg>")
		ra, err := strconv.ParseFloat(args[1], 64)
		if err != nil {
			fatalf("ra: %v", err)
		}
		dec, err := strconv.ParseFloat(args[2], 64)
		if err != nil {
			fatalf("dec: %v", err)
		}
		check(m.GotoCoords(ra, dec))

	case "gotowait":
		requireArgs(args, 3, "gotowait <ra_hours> <dec_deg> [tolerance_deg]")
		ra, err := strconv.ParseFloat(args[1], 64)
		if err != nil {
			fatalf("ra: %v", err)
		}
		dec, err := strconv.ParseFloat(args[2], 64)
		if err != nil {
			fatalf("dec: %v", err)
		}
		tol := 0.1
		if len(args) >= 4 {
			tol, err = strconv.ParseFloat(args[3], 64)
			if err != nil {
				fatalf("tolerance: %v", err)
			}
		}
		ctx := sigContext()
		check(m.GotoAndWait(ctx, ra, dec, tol))
		fmt.Printf("arrived: RA=%.6fh Dec=%+.6f° (within %.3f°)\n", ra, dec, tol)

	case "waitgoto":
		tol := 0.1
		if len(args) >= 2 {
			var err error
			tol, err = strconv.ParseFloat(args[1], 64)
			if err != nil {
				fatalf("tolerance: %v", err)
			}
		}
		ctx := sigContext()
		check(m.WaitGoTo(ctx, tol))
		fmt.Printf("arrived (within %.3f°)\n", tol)

	case "trackrate":
		requireArgs(args, 3, "trackrate <az_deg/s> <el_deg/s>")
		azRate, err := strconv.ParseFloat(args[1], 64)
		if err != nil {
			fatalf("az rate: %v", err)
		}
		elRate, err := strconv.ParseFloat(args[2], 64)
		if err != nil {
			fatalf("el rate: %v", err)
		}
		check(m.TrackRate(azRate, elRate))

	case "trackrate-sy":
		requireArgs(args, 3, "trackrate-sy <az_deg/s> <el_deg/s>")
		azRate, err := strconv.ParseFloat(args[1], 64)
		if err != nil {
			fatalf("az rate: %v", err)
		}
		elRate, err := strconv.ParseFloat(args[2], 64)
		if err != nil {
			fatalf("el rate: %v", err)
		}
		check(m.TrackRateSY(azRate, elRate))

	case "syncpos":
		requireArgs(args, 3, "syncpos <ra_hours> <dec_deg>")
		ra, err := strconv.ParseFloat(args[1], 64)
		if err != nil {
			fatalf("ra: %v", err)
		}
		dec, err := strconv.ParseFloat(args[2], 64)
		if err != nil {
			fatalf("dec: %v", err)
		}
		check(m.SyncPosition(ra, dec))
		fmt.Printf("position synced: RA=%.6fh Dec=%+.6f°\n", ra, dec)

	case "encoders":
		az, el, err := m.EncoderPos()
		check(err)
		fmt.Printf("Az encoder=%d  El encoder=%d\n", az, el)

	case "track":
		requireArgs(args, 2, "track <sidereal|solar|lunar>")
		check(m.SetTracking(args[1]))

	case "radec":
		ra, dec, err := m.RADec()
		check(err)
		fmt.Printf("RA=%.6fh  Dec=%+.6f°\n", ra, dec)

	case "altaz":
		alt, az, err := m.AltAz()
		check(err)
		fmt.Printf("Alt=%+.4f°  Az=%.4f°\n", alt, az)

	case "status":
		s, err := m.Status()
		check(err)
		fmt.Println(s)

	case "version":
		v, err := m.Version()
		check(err)
		fmt.Println(v)

	case "setloc":
		requireArgs(args, 3, "setloc <lat> <lon>")
		lat, err := strconv.ParseFloat(args[1], 64)
		if err != nil {
			fatalf("lat: %v", err)
		}
		lon, err := strconv.ParseFloat(args[2], 64)
		if err != nil {
			fatalf("lon: %v", err)
		}
		check(m.SetLocation(lat, lon))
		fmt.Printf("location set: %.6f, %.6f\n", lat, lon)

	case "settime":
		t := time.Now().UTC()
		check(m.SetDateTime(t))
		fmt.Println("time synced to", t.Format(time.RFC3339))

	case "homeflag":
		on, err := m.GetHomeFlag()
		check(err)
		fmt.Printf("home/DST flag: %v (:GH#, pollable any time — unlike homestatus)\n", on)

	case "backlash":
		s1, err := m.GetBacklashSlot1()
		check(err)
		s2, err := m.GetBacklashSlot2()
		check(err)
		fmt.Printf("slot1 (_g): az=%d\" alt=%d\"\n", s1.AzArcsec, s1.AltArcsec)
		fmt.Printf("slot2:      az=%d\" alt=%d\"\n", s2.AzArcsec, s2.AltArcsec)

	case "backlashmode":
		if len(args) >= 2 {
			mode, err := strconv.Atoi(args[1])
			if err != nil {
				fatalf("mode: %v", err)
			}
			check(m.SetBacklashMode(mode))
			fmt.Printf("backlash mode set to %d (effect unconfirmed — see RE_PROVENANCE.md)\n", mode)
			return
		}
		s, err := m.GetBacklashMode()
		check(err)
		fmt.Printf("%q (raw :GBu# reply — handler not decompiled, meaning unconfirmed)\n", s)

	case "led":
		requireArgs(args, 2, "led <on|off>")
		switch strings.ToLower(args[1]) {
		case "on":
			check(m.SetIlluminatorLED(true))
		case "off":
			check(m.SetIlluminatorLED(false))
		default:
			fatalf("led: must be on or off")
		}

	case "raw":
		requireArgs(args, 2, "raw <:CMD#>")
		reply, err := m.Cmd(strings.Join(args[1:], " "))
		check(err)
		fmt.Printf("%q\n", reply)

	default:
		fatalf("unknown command: %s", args[0])
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `seestar-ctrl [--dev /dev/ttyS3] <command> [args]

  probe                         Check for Seestar S30 hardware indicators (no port needed)
  compass [--avg N]             Read AK09915 magnetometer heading (stop zwoair_imager first)
  declination <lat> <lon>       Compute WMM2025 magnetic declination at lat/lon (degrees)
  slew <e|w|n|s> <1-9>         Preset-rate slew
  slewrate <e|w|n|s> <deg/s>   Variable-rate continuous slew (requires firmware >= V1.1.9)
  setrate <ra|dec> <deg/s>     Set per-axis rate for pulsemove (ZWO :Rv extension)
  pulsemove <e|w|n|s> <dur>    Fire timed pulse at setrate speed; dur 0-99 (unit TBD)
  stop                          Stop all axes
  stopra                        Stop RA axis only
  stopdec                       Stop Dec axis only
  home                          Slew to home position
  exithome                      Clear home mode (:SH0#) — required before slewrate/pulsemove
  homestatus                    Check if homing is complete (1=done)
  track <sidereal|solar|lunar>  Set tracking rate (only works when not in motion state)
  tracking <on|off>             Enable or disable active tracking (:TO1# / :TO0#)
  sync                          Sync motors to current position (:SIM#)
  reset                         Hard-reset the ESP32 firmware (:AR#)
  goto <ra_h> <dec_deg>         GoTo and return immediately
  gotowait <ra_h> <dec_deg> [tol]  GoTo and wait until arrived (default tol=0.1°)
  waitgoto [tol]                Wait for current GoTo to complete (default tol=0.1°)
  trackrate <az_deg/s> <el_deg/s>   Set dual-axis az/el rate for satellite tracking (:Rvr#/:Rvd#)
  trackrate-sy <az_deg/s> <el_deg/s>  Same, single :SY# command (motion mode 7, accel-limited)
  syncpos <ra_h> <dec_deg>      Sync position registers without moving
  encoders                      Read raw RA/Dec encoder counts (:GY#)
  radec                         Print current RA/Dec
  altaz                         Print current Alt/Az
  status                        Print :GU# status response
  version                       Print firmware version
  setloc <lat> <lon>            Set observer location (decimal degrees)
  settime                       Sync mount clock to current UTC
  homeflag                      Read home/DST flag (:GH#) — pollable any time
  backlash                      Read both backlash slots, arcsec (:GBGR/D#, :GBZR/D#)
  backlashmode [0-2]            Get (:GBu#) or set (:SBu{n}#) backlash mode — effect unconfirmed
  led <on|off>                  ESP32 IR illuminator LED (:FTE#/:FTD#) — not the EAF focuser
  raw <:CMD#>                   Send raw LX200 command, print reply
`)
}

// sigContext returns a context that is cancelled on SIGINT or SIGTERM.
func sigContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-ch
		cancel()
	}()
	return ctx
}

func requireArgs(args []string, n int, usage string) {
	if len(args) < n {
		fatalf("usage: %s", usage)
	}
}

func fatalf(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "seestar-ctrl: "+f+"\n", a...)
	os.Exit(1)
}

func check(err error) {
	if err != nil {
		fatalf("%v", err)
	}
}
