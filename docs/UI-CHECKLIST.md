# UI manual checklist

The automated tests check that the page and the server agree with each other. These things need eyes.
Use `ak820 ui`, only press **dry-run** style buttons (the page always asks before it sends anything).

- [ ] **Not connected:** unplug the keyboard, reload. The card explains what to check in plain words, and **Send** is locked with a reason.
- [ ] **Permission problem:** without the udev rule, the card says to run `ak820 setup` (no jargon).
- [ ] **Connected:** green card; the gallery shows four thumbnails; choosing one shows an animated preview.
- [ ] **Own picture:** drop a PNG, a JPEG and an animated GIF. Previews look right for *Fill*, *Show everything* and *Stretch*.
- [ ] **Huge picture:** drop a very large image; you get a friendly "too large" message, not a hang.
- [x] **Confirm dialog:** shows plain words first; the technical bytes are only under "Technical details". Cancel sends nothing.
- [x] **Narrow screen (phone width, ~360 px):** no sideways scrolling; buttons are easy to tap.
- [x] **Light and dark mode:** text is readable in both.
- [x] **Keyboard only:** Tab reaches every control in a sensible order, focus is visible, Enter/Space activate, the dialog traps focus and Esc cancels.
- [ ] **Screen reader:** status and progress messages are announced (they use `aria-live`).
- [ ] **Restart the UI:** it asks to connect again (the connection is not remembered between runs).
- [ ] **Two keyboards:** the card tells you to unplug one or use `--device`.

## Last run

2026-11-01, v1.0.0 plus the v1.0.1 changes, automated in headless Chrome on Linux with every write request blocked. Ticked items passed:

- **Narrow screen:** at 360 px the page doesn't scroll sideways (the interface table scrolls inside its own box), and every control is at least 24 px in both directions.
- **Light and dark mode:** all visible text except disabled controls reaches a contrast of 4.5:1 (3:1 for large text), by calculation at 360 and 1280 px.
- **Keyboard only:** Tab reaches the controls in page order with visible focus, Enter and Space open the Connect dialog, the page behind the dialog can't be reached, and Esc cancels and returns focus.
- **Confirm dialog:** plain words first, technical details collapsed, and Cancel sends only the dry run.

The connected state was simulated for these runs. The rest of the list still needs a person. No real phone or screen reader was used.

