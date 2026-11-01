# ak820

A Linux command-line tool and local web UI for the TFT screen on the AJAZZ / Epomaker **AK820 Pro** keyboard.
It is written in Go, uses only the standard library, and builds to a single static binary (the UI is embedded in it).

It finds the screen's HID interface by reading the report descriptors, instead of assuming one fixed layout.
Some firmware revisions expose different interfaces from the ones other tools expect (see [Status](#status)).

> **Safety:** every CLI command that writes to the keyboard needs `--yes`, and every write in the web UI needs a confirm click.
> Without that, the tool prints the target node and the exact bytes, then stops.

## Quick start

```sh
CGO_ENABLED=0 go build -o ak820 ./cmd/ak820      # build
sudo cp udev/70-ak820.rules /etc/udev/rules.d/    # one-time permissions, then replug (see below)
./ak820 list                                      # what does my keyboard expose? (no device I/O)
./ak820 ui                                        # or use the browser UI
```

The UI is the easiest way in: step 1 connects, step 2 picks a picture, step 3 sends it.
From the terminal, on a `sonix` keyboard: `./ak820 time --yes` (connect and set the clock), then `./ak820 upload --yes picture.gif`.

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

- Linux, Go 1.22 or newer (to build)
- The keyboard connected **by USB cable with the back switch in USB mode**. Bluetooth and the 2.4G dongle cannot reach the screen.

## Build

```sh
git clone <this repo> && cd ak820-custom-driver
CGO_ENABLED=0 go build -o ak820 ./cmd/ak820
```

## Permissions (one time)

The tool opens `/dev/hidraw*` as your normal user. Install the udev rule and replug the keyboard:

```sh
sudo cp udev/70-ak820.rules /etc/udev/rules.d/
sudo udevadm control --reload && sudo udevadm trigger
```

Then unplug and replug the cable. `ak820 list` should show `rw=true` on every node.

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

`./ak820 report` prints the same information as a Markdown table, plus the raw descriptors. Paste it into a bug report.

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
| `udev/70-ak820.rules` | permissions rule |

Run the tests with `go test ./...`. They include the real descriptors captured from a 0c45:800a unit.

## Credits

- The legacy packet format and payload layout come from [fromagge/ak820-screen](https://github.com/fromagge/ak820-screen) (see its `PROTOCOL.md`).
- The `sonix` protocol (feature reports, 4096-byte chunks, alignment) and the bundled GIFs come from [ErvinUrban-Bratislava/ak820pro-linux](https://github.com/ErvinUrban-Bratislava/ak820pro-linux) (MIT; its licence is in `internal/gallery/gifs/`). Its `FINDINGS.md` documents the firmware behaviour.

## License

MIT, see [LICENSE](LICENSE). The bundled GIFs are MIT-licensed by their author; see `internal/gallery/gifs/LICENSE-ak820pro-linux.txt`.
