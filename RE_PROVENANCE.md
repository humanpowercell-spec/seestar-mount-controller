# RE provenance policy

**[github.com/humanpowercell-spec/seestar-re](https://github.com/humanpowercell-spec/seestar-re)
is the authoritative source for everything this library claims about the
Seestar S30's wire protocols** (ESP32 LX200 command set, guider/imager JSON-RPC,
`/dev/eaf` and `/dev/pwm-gpio-misc` ioctls, IIO compass sample format, WMM
declination model). This library is the *implementation*; seestar-re is the
*evidence*.

## The rule

Every function that encodes a protocol-level fact — a wire command, an ioctl
number, a struct layout, a response format, a physical constant pulled from a
datasheet or a decompile — carries a `// RE:` comment immediately above it,
citing:

1. the seestar-re file and line(s) the fact comes from, and
2. a **stable anchor** alongside the line number — the LX200 command token
   (`:SY#`), the decompiled function name (`FUN_42011bd0`) or address
   (`0x42008f94`), or an NVS/RPC method name (`scope_get_mode`) — because line
   numbers drift as seestar-re's docs are edited and the anchor is how you
   re-find the citation when they do.

```go
// RE: seestar-re/docs/esp32_firmware.md:574 (:SY# / FUN_42011bd0 RA, FUN_42011cac Dec)
func (m *Mount) TrackRateSY(azDegPerSec, elDegPerSec float64) error {
```

Pure Go composition that doesn't add a new protocol claim (e.g. `GotoAndWait`
calling `GotoCoords`+`WaitGoTo`) doesn't need its own citation — the citation
lives on the function that actually encodes the wire fact.

**If a feature isn't decompiled/documented in seestar-re, it doesn't get
implemented here on a guess.** Two examples currently blocked on this (see
`mount/config.go`):

- `:SB…#` backlash-arcsec **setter** — the getters (`:GBGR#/:GBGD#/:GBZR#/:GBZD#`)
  and the mode setter (`:SBu{0,1,2}#`) are pinned; the exact per-slot/per-axis
  sub-command letters for *writing* an arcsec value were never decompiled
  (`docs/esp32_firmware.md:729`, "Setters ... FUN_4200ca00/.../FUN_4200cb80" —
  those are the NVS-load-time appliers, not the confirmed LX200 dispatch).
- `Park()` / `GetParkPosition()` — `:GP#`/`:SP#` (`0x420084c8`/`0x42008e80`) are
  only listed by address in `docs/esp32_firmware.md`; the argument/response
  format was never decompiled.

When you hit one of these, RE it in seestar-re first (decompile the handler,
add it to the command table with its wire format), cite that, then implement
here. Don't skip straight to the Go.

## Pinning

Citations in this repo were written against **seestar-re commit
[`bbfcc79`](https://github.com/humanpowercell-spec/seestar-re/commit/bbfcc79)**
(2026-09-04). seestar-re's own docs get corrected and expanded over time — if a
cited line number looks wrong, `grep` the anchor token in the current seestar-re
checkout; it hasn't moved even when the line has. If you re-verify a citation
against a newer seestar-re commit, bump the pin above.

## What's already covered vs. not

`docs/esp32_firmware.md` § Full Command Map is the ground truth for the LX200
surface. Commands intentionally **not** wrapped here (redundant aliases,
hardware-fixed, or never decompiled in enough detail to trust) are listed in
`README.md` § Command coverage, each with the reason.
