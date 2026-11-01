# UI manual checklist

The automated tests check that the page and the server agree with each other. These things need eyes.
Use `ak820 ui`, only press **dry-run** style buttons (the page always asks before it sends anything).

- [ ] **Not connected:** unplug the keyboard, reload. The card explains what to check in plain words, and **Send** is locked with a reason.
- [ ] **Permission problem:** without the udev rule, the card says to run `ak820 setup` (no jargon).
- [ ] **Connected:** green card; the gallery shows four thumbnails; choosing one shows an animated preview.
- [ ] **Own picture:** drop a PNG, a JPEG and an animated GIF. Previews look right for *Fill*, *Show everything* and *Stretch*.
- [ ] **Huge picture:** drop a very large image; you get a friendly "too large" message, not a hang.
- [ ] **Confirm dialog:** shows plain words first; the technical bytes are only under "Technical details". Cancel sends nothing.
- [ ] **Narrow screen (phone width, ~360 px):** no sideways scrolling; buttons are easy to tap.
- [ ] **Light and dark mode:** text is readable in both.
- [ ] **Keyboard only:** Tab reaches every control in a sensible order, focus is visible, Enter/Space activate, the dialog traps focus and Esc cancels.
- [ ] **Screen reader:** status and progress messages are announced (they use `aria-live`).
- [ ] **Restart the UI:** it asks to connect again (the connection is not remembered between runs).
- [ ] **Two keyboards:** the card tells you to unplug one or use `--device`.
