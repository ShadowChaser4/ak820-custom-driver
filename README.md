# ak820

A Linux command-line tool and local web UI for the TFT screen on the AJAZZ / Epomaker **AK820 Pro** keyboard.
It is written in Go, uses only the standard library, and builds to a single static binary (the UI is embedded in it).

It finds the screen's HID interface by reading the report descriptors, instead of assuming one fixed layout.
Some firmware revisions expose different interfaces from the ones other tools expect (see [Status](#status)).

> **Safety:** every CLI command that writes to the keyboard needs `--yes`, and every write in the web UI needs a confirm click.
> Without that, the tool prints the target node and the exact bytes, then stops.

## Install and first run

1. **Get the program.** Download `ak820_<version>_linux_amd64.tar.gz` (or `arm64`) from the project's releases page and unpack it, or build it yourself (needs Go 1.25+):
   ```sh
   make install        # builds and installs to ~/.local/bin/ak820 (no sudo needed)
   ```
   Make sure `~/.local/bin` is on your `PATH` (`make install` tells you if it is not). If you just unpacked the archive, run it as `./ak820`; every hint the program prints uses the name you typed.
2. **Check this computer.** Plug the keyboard in with the USB-C cable, slide the switch on the back to **USB**, then:
   ```sh
   ak820 setup
   ```
   It shows a checklist (keyboard found, permission rule installed, allowed to use it, layout recognised) and, for anything missing, the exact command to fix it. The one thing that needs `sudo` is installing the permission rule; `setup` prints that command for you and never runs `sudo` itself. Unplug and replug the keyboard afterwards.
3. **Use it.**
   ```sh
   ak820               # opens the app in your browser: connect, pick a picture, send
   ```

If anything goes wrong, run `ak820 setup`. If your keyboard is "not supported yet", run `ak820 report --issue` and paste the result into a new issue; that is all that is needed to add support.

Several keyboards plugged in? Choose one with `ak820 --device 2 <command>` (or `AK820_DEVICE=2`).

## Status

| Layout | Protocol | Status |
|---|---|---|
| **`sonix`**: `FF68` (4096-byte output report) + `FF13` (64-byte feature report). This is the AK820 Pro 0c45:800a that this project was developed on. | Feature-report commands plus 4096-byte data chunks (from [ak820pro-linux](https://github.com/ErvinUrban-Bratislava/ak820pro-linux) firmware notes) | Implemented and tested against fakes. On real hardware only the **clock sync** has been run so far (the command succeeded; the screen's clock has not been confirmed). **Image upload has not been run on hardware yet.** |
| **`legacy`**: a config page plus a bulk page `FF67`, as in [fromagge/ak820-screen](https://github.com/fromagge/ak820-screen) | `AA`/`55` framed commands | Implemented, **not tested on hardware** here |
| anything else | n/a | The tool refuses to guess and says so |

The layout is detected from the HID report descriptors; run `./ak820 list` to see yours.

`sonix` uploads are written to the keyboard's flash, so they stay after unplugging. Legacy firmware 1.16 keeps uploads in RAM only.
`sonix` has no built-in-animation command in this tool, so `builtin` is only available on the legacy layout.

## Requirements

- Linux (x86-64 or arm64). The release is a single static file with no dependencies.
- The keyboard connected **by USB cable with the back switch in USB mode**. Bluetooth and the 2.4G dongle cannot reach the screen.
- Go 1.25 or newer only if you build it yourself (`make build`, `make check`, `make release`).

## Permissions (one time)

`ak820 setup` checks this for you. By hand, the rule lets your logged-in user open the keyboard's `/dev/hidraw*` nodes:

```sh
ak820 setup --print-rule | sudo tee /etc/udev/rules.d/70-ak820.rules >/dev/null
sudo udevadm control --reload && sudo udevadm trigger
```

Then unplug and replug the keyboard. (The same rule is in `udev/70-ak820.rules` in the source.)

## How to use

### 1. See what your keyboard exposes (no device I/O)

```sh
./ak820 list
```

Example output (AK820 Pro, 0c45:800a):

```
/dev/hidraw7  if2  0c45:800a  page 0xFF68  in 64  out 4096  feat 0   rw=true  SONiX AK820
/dev/hidraw8  if3  0c45:800a  page 0xFF13  in 64  out 64    feat 64  rw=true  SONiX AK820
```

(The keyboard and consumer-control nodes are also listed.)

`ak820 report` prints the same information as a Markdown table, plus the raw descriptors. `ak820 report --issue` adds the program version and your system, ready to paste into a bug report.

### 2. Find the command channel (`probe`)

Preview first. This sends nothing:

```sh
./ak820 probe
```

It lists the frames it would send. Each is a `GET_DEVICE_INFO` (command `0x10`) frame, sized to each candidate interface's output report. It also lists the feature-report read it would do.

Then run it for real:

```sh
./ak820 probe --yes
```

For each candidate it writes the frame, then listens on every vendor interface for 500 ms and prints any reply.
Results are appended to `PROBE-LOG.md`. Flags:

| Flag | Meaning |
|---|---|
| `--yes` | actually send |
| `--log FILE` | where to append the log (default `PROBE-LOG.md`) |
| `--feature=false` | skip the read-only `GET_FEATURE` |

A reply starting with `55 10` means that node, at that report length, is the command channel.
If nothing replies, include `PROBE-LOG.md` in your issue.

### 3. Device info (legacy layout only)

The `sonix` layout has no device-info command. On those keyboards use `./ak820 time --yes` to connect instead.

```sh
./ak820 info                              # dry run
./ak820 info --yes                        # legacy layout only: picks the config interface itself
./ak820 info --yes --node hidraw8 --len 64   # explicit: use what probe found
```

`--len` defaults to the output report size in the descriptor.
Nothing about device nodes is ever saved to disk: `/dev/hidrawN` numbers change every time the keyboard is replugged, so the tool re-detects your keyboard on every run.

### 4. Upload an image or GIF

```sh
./ak820 upload photo.png                          # dry run: shows frame count and first packet
./ak820 upload --yes photo.png
./ak820 upload --yes --fit contain --bg 202020 logo.png
./ak820 upload --yes --delay 100 --max-frames 60 anim.gif
./ak820 upload --preview preview.png photo.png    # also save frame 0 as PNG
```

The screen is 128×128. Images are converted to RGB565 and sent in an animation payload.

| Flag | Meaning |
|---|---|
| `--yes` | actually send |
| `--fit cover\|contain\|stretch` | how to fit to 128×128 (default `cover`) |
| `--bg black\|white\|RRGGBB` | background for `contain` and transparent areas |
| `--delay MS` | override every frame's delay |
| `--max-frames N` | truncate to N frames (the device limit is 255) |
| `--node`, `--len` | data interface and report length (legacy default: the `FF67` interface) |
| `--preview FILE` | write a PNG preview of frame 0 |

Supported inputs: PNG, JPEG, GIF (animated GIFs keep their frame delays).

A real upload is also stored in the history (`~/.local/share/ak820/history`, newest 50), which the UI can restore or export as a GIF.

The screen redraws at about 60 ms per frame at best, so shorter delays play at that rate.
On firmware 1.16 an uploaded animation is held in RAM. It is lost when you unplug the keyboard or change mode.

### 5. Built-in animation and clock

```sh
./ak820 builtin 3          # dry run
./ak820 builtin --yes 3
./ak820 time --yes        # sync the screen clock (command interface)
```

### 6. Web UI (for everyone, no terminal knowledge needed)

```sh
./ak820 ui            # prints http://127.0.0.1:PORT/#token=... and opens your browser
./ak820 ui --no-open  # just print the URL
```

The UI is embedded in the binary (no npm, no extra files) and listens on loopback only.
It is a three-step page in plain language:

1. **Connect your keyboard**: a status card (with a checklist if the keyboard is not found) and one button that connects and sets the keyboard clock.
2. **Pick a picture**: four ready-made animations (AI Core, Audio Visualizer, Bongo Cat, Cyber Radar, from [ak820pro-linux](https://github.com/ErvinUrban-Bratislava/ak820pro-linux), MIT) or your own PNG / JPEG / GIF, with a live preview of exactly what the screen will show. Recent pictures can be used again.
3. **Send it to the keyboard**: one button, a confirmation, and a progress bar.

Everything technical (interface table, probe, report, interface pickers, built-in animation, history export) is under **Advanced / troubleshooting**.

Nothing is written until you click a red-outlined button and confirm in the dialog, which shows the target node and the first packet's bytes.
**Sending is locked until the keyboard is connected.** On a `sonix` keyboard, "connected" means the clock-sync command went through; on legacy it means `GET_DEVICE_INFO` got a reply.
The connection only lasts for the running session (it is kept in memory, never on disk), so after restarting the UI, or replugging the keyboard, you click Connect again. That is deliberate: it re-tests the connection every time.

Security: the server binds 127.0.0.1, requires the random token printed at startup on every API call (sent as the `X-Token` header), and rejects foreign `Host` / `Origin` headers, so other web pages cannot drive your keyboard.
The token is in the URL fragment, which browsers do not send to the server.

## Things not to do

- On a **legacy** 1.16 keyboard, never send feature report `04 72`: it erases the saved animation slot. (On `sonix` it is the normal image-setup step and the tool sends it for you.)
- On `sonix`, an upload overwrites the animation stored in the keyboard's flash. Don't unplug the cable while the progress bar is moving.
- Do not run `upload` with a `--node` and `--len` you have not checked with `probe` first (the defaults are detected for you and are safe).
  The CLI does not enforce this (the UI does), so check before adding `--yes`.

## Layout of the code

| Path | Contents |
|---|---|
| `cmd/ak820/` | CLI and subcommands (including `ui`) |
| `internal/core/` | device logic shared by CLI and UI: discovery, probe, info, upload, builtin, clock, in-memory session |
| `internal/history/` | upload history on disk |
| `internal/web/` | local web server and the embedded frontend (`static/`) |
| `internal/hidraw/` | sysfs enumeration, HID report-descriptor parser, hidraw open, read and write, `GET_FEATURE` |
| `internal/proto/` | legacy `AA`/`55` packet framing, reply matching, upload, `GET_DEVICE_INFO` decoding |
| `internal/sonix/` | feature-report protocol: clock sync, flash payload, alignment and chunked upload with per-chunk acknowledgement |
| `internal/gallery/` | the embedded ready-made GIFs and their licence |
| `internal/img/` | image and GIF decoding, 128×128 fit, RGB565, payload builder and parser |
| `internal/setup/` | the `setup` checklist and the embedded udev rule |
| `internal/hw/` | opt-in tests that use a real keyboard (`-tags hardware`) |
| `udev/70-ak820.rules` | permissions rule (kept identical to the embedded copy by a test) |
| `docs/` | hardware test results, UI checklist |

Run the tests with `go test ./...`. They include the real descriptors captured from a 0c45:800a unit.

## Testing

```sh
make check          # gofmt + go vet + all tests with the race detector (what CI runs)
make fuzz           # 10 s on each fuzz target (descriptor parser, payload parser, image decoder)
```

What the tests cover, and how they avoid needing a keyboard:

- **No saved device numbers.** Enumeration is tested against fake sysfs trees (the real four-interface unit, the same unit after a replug with different `hidrawN` numbers, two keyboards, other vendors, broken nodes).
- **Nothing is written without `--yes`.** Every CLI command is run against a fake keyboard that counts writes.
- **The `sonix` protocol** is checked byte-for-byte against golden files and against a stateful fake of the firmware (the 64-packet ring counter that `IMAGE_CFG` does not reset, ACK on wrap, the 2 s watchdog). Uploads are checked from all 64 starting positions, and every failure (dropped or corrupt ACK, write error, dead control channel) must never be followed by a SAVE.
- **The web page** is checked for consistency (every element the script uses exists, every API path it calls is routed, no external resources, no HTML injection); the API for token, `Host`/`Origin`, path traversal and size limits.
- **Fuzzing** found and fixed a descriptor-parsing overflow; the failing input is kept as a regression seed. Pictures that claim a huge size are rejected before decoding.
- **Real hardware** is opt-in and read-only by default: `go test -tags hardware -v ./internal/hw`. Writes need `AK820_HW_WRITE=1` (clock) or `AK820_HW_UPLOAD=1` (picture). See [docs/HARDWARE-TESTS.md](docs/HARDWARE-TESTS.md), which also lists what has been verified on real keyboards. Things automation cannot see (phone layout, light mode, keyboard-only use) are in [docs/UI-CHECKLIST.md](docs/UI-CHECKLIST.md).

## Credits

- The legacy packet format and payload layout come from [fromagge/ak820-screen](https://github.com/fromagge/ak820-screen) (see its `PROTOCOL.md`).
- The `sonix` protocol (feature reports, 4096-byte chunks, alignment) and the bundled GIFs come from [ErvinUrban-Bratislava/ak820pro-linux](https://github.com/ErvinUrban-Bratislava/ak820pro-linux) (MIT; its licence is in `internal/gallery/gifs/`). Its `FINDINGS.md` documents the firmware behaviour.

## License

MIT, see [LICENSE](LICENSE). The bundled GIFs are MIT-licensed by their author; see `internal/gallery/gifs/LICENSE-ak820pro-linux.txt`.
