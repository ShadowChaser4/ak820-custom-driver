# Architecture

```
cmd/ak820  ──┐
             ├──> internal/keyboard/drivers ──> keyboard/sonix, keyboard/legacy
internal/web ┘                                        │
                                                      v
                        internal/keyboard  <── Driver, Plan, Finder, errors
                                │
             internal/hidraw (devices)      internal/animation (pictures)
```

## The ideas that matter

**A `Plan` is what will be sent.** A driver turns a request into a `Plan` without touching
the device. `Describe()` shows it, and `Run()` sends it. A dry run and a real run use the same plan, so
what you are shown is what happens. There is no "dry" flag to forget to check.

**A `Driver` is one protocol.** Nothing outside the driver knows how a protocol works. Callers ask
`Capabilities()` what is possible, and unsupported operations return `keyboard.ErrUnsupported`.

**Device numbers are never stored.** `/dev/hidrawN` changes on every replug and differs on every
machine, so each run enumerates again and detects the protocol from the HID descriptors.

**Nothing unfinished is committed.** Uploads end with a SAVE that is only sent after every chunk was
acknowledged. Any failure, or cancelling the context (Ctrl-C, closing the browser tab), stops before it.

**Wording lives at the edges.** Packages return plain errors, tested with `errors.Is` and `errors.As`.
Only `cmd/ak820/errors.go` and `internal/web/device.go` decide what to tell a person.

**Writes need a clear yes.** `--yes` on the command line, a confirm click in the page. In the page,
only the Connect step may write before the keyboard is connected, because Connect is the check.

## Adding a keyboard protocol

1. Get `ak820 report --issue` from a real unit. Its descriptors show what to detect.
2. Create `internal/keyboard/<name>/` with `Detect(ifaces, deps) (keyboard.Driver, bool)` and a `Driver`.
   Do no device I/O while planning. Take `Lock` and open the devices inside `Run`.
3. Add the detector to `internal/keyboard/drivers/drivers.go`, and a new `Layout` to `keyboard.go`.
4. Add the descriptors to the conformance table in `drivers_test.go`. Add golden files for the frames,
   and a fake of the firmware if the protocol has state worth modelling (see `sonix/firmware_test.go`).
5. Add a row to [HARDWARE-TESTS.md](HARDWARE-TESTS.md) once it has run on a real keyboard. Until then,
   say that it has not.
