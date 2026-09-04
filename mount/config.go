package mount

// Backlash, home/DST flag, and illuminator-LED — ESP32 LX200 commands that are
// fully decompiled (wire format pinned) but weren't wrapped when this package
// was first written. See RE_PROVENANCE.md for the citation convention and for
// why two related features (the backlash arcsec *setter*, and Park) are NOT
// here: their wire format was never nailed down in seestar-re, and this
// package doesn't implement commands on a guess.

import (
	"fmt"
	"strconv"
	"strings"
)

// BacklashReading holds a per-axis backlash compensation value in arcseconds,
// as read from one of the firmware's two runtime slots.
//
// RE: seestar-re/docs/esp32_firmware.md:699-718 (Backlash compensation —
// Config: NVS keys -> 2 runtime slots; slot selection is a hardware-mode ADC
// read at boot, not user-selectable over LX200).
type BacklashReading struct {
	AzArcsec  int
	AltArcsec int
}

// GetBacklashSlot1 reads the "_g" backlash slot (:GBGR#/:GBGD#) — the
// mode-1-hardware calibration set, and the one populated by
// bl_azm_arcsec_g/bl_alt_arcsec_g.
//
// RE: seestar-re/docs/esp32_firmware.md:724 (:GBGR#/:GBGD# "slot 1 (_g)
// azimuth / altitude backlash"), re/esp32/esp32_backlash_comp.c:452-483
// (FUN_42007a0c — G/Z dispatch, 'D'/'R' sub-command, integer reply via the
// same %d# formatter as :GH#).
func (m *Mount) GetBacklashSlot1() (BacklashReading, error) {
	return m.getBacklash(":GBGR#", ":GBGD#")
}

// GetBacklashSlot2 reads the "slot 2" backlash values (:GBZR#/:GBZD#) —
// populated from bl_azm_arcsec/bl_alt_arcsec, defaulting to 0 for hardware
// modes 3 and 4.
//
// RE: seestar-re/docs/esp32_firmware.md:725 (:GBZR#/:GBZD# "slot 2 azimuth /
// altitude backlash"), re/esp32/esp32_backlash_comp.c:452-483 (FUN_42007a0c).
func (m *Mount) GetBacklashSlot2() (BacklashReading, error) {
	return m.getBacklash(":GBZR#", ":GBZD#")
}

func (m *Mount) getBacklash(azCmd, altCmd string) (BacklashReading, error) {
	m.mu.Lock()
	azStr, err := m.query(azCmd)
	if err != nil {
		m.mu.Unlock()
		return BacklashReading{}, fmt.Errorf("%s: %w", azCmd, err)
	}
	altStr, err := m.query(altCmd)
	m.mu.Unlock()
	if err != nil {
		return BacklashReading{}, fmt.Errorf("%s: %w", altCmd, err)
	}
	az, err := strconv.Atoi(strings.TrimSpace(azStr))
	if err != nil {
		return BacklashReading{}, fmt.Errorf("parse %s reply %q: %w", azCmd, azStr, err)
	}
	alt, err := strconv.Atoi(strings.TrimSpace(altStr))
	if err != nil {
		return BacklashReading{}, fmt.Errorf("parse %s reply %q: %w", altCmd, altStr, err)
	}
	return BacklashReading{AzArcsec: az, AltArcsec: alt}, nil
}

// GetBacklashMode returns the raw reply to :GBu# (backlash enable/mode).
// Returned as a string, not parsed: the handler for this command was never
// decompiled in seestar-re (only its counterpart :SBu{0,1,2}# is confirmed,
// from AM_Test's sprintf), so the reply's meaning is unconfirmed.
//
// RE: seestar-re/docs/esp32_firmware.md:726 (":GBu# | backlash enable/mode
// (separate handler — not decompiled)"), re/AM_Test.c:7214 (literal ":GBu#").
func (m *Mount) GetBacklashMode() (string, error) {
	return m.Cmd(":GBu#")
}

// SetBacklashMode sends :SBu{mode}#, mode 0-2. The wire format is confirmed
// (AM_Test builds this exact string), but the firmware-side effect of each
// mode value is not — seestar-re notes the handler was "seen in AM_Test;
// handler not traced". Treat the mode numbers as opaque until that's pinned.
//
// RE: seestar-re/docs/esp32_firmware.md:727 (":SBu0#/:SBu1#/:SBu2#"),
// re/AM_Test.c:7171 (`sprintf(local_38,":SBu%d#",(ulong)param_1)`).
func (m *Mount) SetBacklashMode(mode int) error {
	if mode < 0 || mode > 2 {
		return fmt.Errorf("mode %d out of range 0-2", mode)
	}
	return m.cmdNoReply(fmt.Sprintf(":SBu%d#", mode))
}

// GetHomeFlag reads the raw home/DST flag (:GH#) — the same flag :SH0#/:SH1#
// clear/set. Unlike HomeStatus (:hF#, which only replies during an active
// homing sequence), this can be polled any time.
//
// RE: seestar-re/docs/esp32_firmware.md:541 (":GH# | 0x42007a88 | Get
// home/DST flag (DAT_3fc97954) -> %d#"), :606 (Notable Undocumented
// Commands), :311 (SH_handler — same flag, :SH0#=clear/:SH1#=set).
func (m *Mount) GetHomeFlag() (bool, error) {
	resp, err := m.Cmd(":GH#")
	if err != nil {
		return false, fmt.Errorf("GH: %w", err)
	}
	resp = strings.TrimSpace(resp)
	n, err := strconv.Atoi(resp)
	if err != nil {
		return false, fmt.Errorf("parse GH reply %q: %w", resp, err)
	}
	return n != 0, nil
}

// SetIlluminatorLED turns the ESP32-driven IR illumination LED on or off
// (:FTE#/:FTD#). Despite the LX200 "focus enable/disable" naming, this does
// NOT control the EAF focuser (see Focuser in focuser.go, which is entirely
// Pi-side) — it drives ESP32 GPIO3+GPIO45 directly, most likely an IR
// reference target for the EAF's photodiode endstop sensor.
//
// Note: :FTD# does not explicitly clear the GPIOs (only the DAT_3fc97955
// flag) — seestar-re flags this as asymmetric. Don't rely on :FTD# alone to
// guarantee the LED is physically off if precise state matters.
//
// RE: seestar-re/docs/esp32_firmware.md:485-486 (:FTD#/:FTE# @
// 0x4200bb7c/0x4200bb5c), :80-86 (GPIO3/GPIO45 assignment + asymmetric
// clear), seestar-re/docs/focuser.md:175-189 ("ESP32 commands" section, IR
// illumination hypothesis).
func (m *Mount) SetIlluminatorLED(on bool) error {
	if on {
		return m.cmdNoReply(":FTE#")
	}
	return m.cmdNoReply(":FTD#")
}
