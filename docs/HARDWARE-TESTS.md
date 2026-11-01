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
| sonix (`FF68` 4096 B + `FF13` 64 B feature) | AK820 Pro `0c45:800a` | not read | 2026-11-01 | project author | pass (4 interfaces, layout `sonix`, `GET_FEATURE` readable) | command accepted, **screen change not confirmed** | **not run** | not run |
| legacy (`FF67` + config page) | | | | | not run | not run | not run | not run |

"Command accepted" means every control frame was sent without an I/O error. It is not proof that the
keyboard did what was asked.

## Known open questions

- The reference notes for the `sonix` clock sequence disagree with the reference code about byte 8 of the
  final SAVE frame (`01` in the notes, `00` in the code) and mention a `FINISH` frame (`04 F0`) that the
  code does not send. This tool follows the code. If the clock does not change on your keyboard, those two
  details are the first things to try.
- Picture upload has only run against a fake of the firmware.
