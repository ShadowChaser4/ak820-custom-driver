# Hardware test results

Automated tests use fakes (see the Testing section of the README). This page records what has been
checked on **real keyboards**, so users and maintainers can see what is proven and what is not.

## How to run

```sh
go test -tags hardware -v ./internal/hw                              # read-only: layout, descriptors, GET_FEATURE
AK820_HW_WRITE=1  go test -tags hardware -v -run Clock  ./internal/hw   # sets the keyboard clock
AK820_HW_UPLOAD=1 go test -tags hardware -v -run Upload ./internal/hw   # overwrites the stored picture (bongo_cat)
```

The write tests cannot know whether the screen changed. **Look at the keyboard**, then add a row below
(copy the output of `ak820 report --issue` into the pull request or issue as well).

## Results

| Layout | Product | Firmware | Date | Tester | Read-only checks | Clock sync | Picture upload | Survives replug |
|---|---|---|---|---|---|---|---|---|
| sonix (`FF68` 4096 B + `FF13` 64 B feature) | AK820 Pro `0c45:800a` | USB revision 1.10 (`bcdDevice`) | 2026-11-01 | project author | pass (4 interfaces, layout `sonix`, `GET_FEATURE` readable) | pass, screen shows the right time | pass, default format | pass, opened with **Fn+Del** |
| legacy (`FF67` + config page) | | | | | not run | not run | not run | not run |

"Command accepted" means every control frame was sent without an I/O error. It is not proof that the
keyboard did what was asked.

After a replug this keyboard plays the boot animation and then shows the clock. The picture is not shown
until you press Fn+Del, so check with Fn+Del before recording a picture as lost.

The replug test unplugged and replugged the USB cable. A longer time without power has not been tested.

The keyboard failed to connect on a USB hub shared with three other devices (`Not enough bandwidth`,
`can't set config #1, error -28`). It connected on a port on the laptop itself.

## Known open questions

- The reference notes for the `sonix` clock sequence disagree with the reference code about byte 8 of the
  final SAVE frame (`01` in the notes, `00` in the code) and mention a `FINISH` frame (`04 F0`) that the
  code does not send. This tool follows the code, and that works on the keyboard above. If the clock does
  not change on your keyboard, those two details are the first things to try.
- Whether a command can make the picture open by itself at startup.
- Other projects disagree about the upload's slot byte (1, 2 or 3). This tool sends 3, which worked on the
  keyboard above. A report that it only shows briefly on another `0c45:800a` suggests trying slot 1 there.
