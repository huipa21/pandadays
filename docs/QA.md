# PandaDays — Manual QA Matrix

Run before each release against a fresh data dir and an upgraded one. Use HTTPS (or `localhost`): browsers only allow microphone access in a secure context.

Legend: ✅ passed · ⚠️ passed up to an engine limit (see notes; confirm on a real device) · ⬜ not yet run · ➖ not applicable. Record the date, build and tester for each run.

## Matrix

Last run: 2026-10-18, automated (see *Method*). The iOS Safari and Android Chrome columns are the WebKit and Chromium engines with iPhone 13 / Pixel 7 device emulation (viewport, DPR, touch, mobile UA), **not physical devices**.

| Check | iOS Safari | Android Chrome | Desktop Chrome | Desktop Firefox |
|---|---|---|---|---|
| Setup token → admin → login (incl. forced password change) | ✅ | ✅ | ✅ | ✅ |
| Create / edit / archive / unarchive / delete day | ✅ | ✅ | ✅ | ✅ |
| Voice recording uploads, stored as audio, plays back | ⚠️ (1) | ✅ `audio/webm;codecs=opus` | ✅ `audio/webm;codecs=opus` | ✅ `audio/webm;codecs=opus` |
| Voice recording in the alternate container | ➖ | ✅ `audio/mp4` → `.m4a` | ✅ `audio/mp4` → `.m4a` | ✅ `audio/ogg;codecs=opus` → `.ogg` |
| Large video upload (40 MiB WebM) shows progress, plays inline | ➖ | ✅ | ✅ | ✅ |
| Large video upload (40 MiB H.264 MP4) shows progress, plays inline | ⚠️ (2) | ✅ | ✅ | ✅ |
| iPhone `.mov` (QuickTime `qt  ` brand) stored as video, `<video playsinline>` | ✅ (3) | ✅ | ✅ | ✅ |
| Over-quota upload shows a localized error banner; form recovers | ✅ | ✅ | ✅ | ✅ |
| Network failure shows a localized error banner | ✅ | ✅ | ✅ | ✅ |
| Backup download → restore with password → login, data back | ➖ | ➖ | ✅ | ✅ |
| Share with user; recipient sees read-only view (edit 404, POST 403) | ✅ | ✅ | ✅ | ✅ |
| Export / import ZIP round trip (85 MB with media) | ➖ | ➖ | ✅ | ✅ |
| zh UI (16 pages, no English leftovers) | ✅ | ✅ | ✅ | ✅ |
| Dark mode: axe clean, text ≥ 4.5:1, control borders ≥ 3:1 | ✅ (4) | ✅ | ✅ | ✅ |
| 375 px: no horizontal scroll, header not sticky | ✅ | ✅ | ✅ | ✅ |
| 1280 px: sticky header, anchors not hidden under it | ➖ | ➖ | ✅ | ✅ |
| Accessibility tree: no unnamed controls, decorative emoji hidden | ✅ | ✅ | ✅ | ✅ |

Notes:

Responsive-width regression check: resize the same page through 375, 768, 1280,
and 1600 px. Main content must fill the viewport (including 16 px page padding
on mobile and 24 px at widths of at least 768 px), rather than stop growing at
960 px. Verify no horizontal document overflow; login, setup, and password cards
must remain centered and no wider than 420 px. Check populated day lists,
day forms, settings, and the two-column admin user list as well as empty pages.
Passed on 2026-10-18 in the integrated Chromium browser against a fresh build:
setup, populated home/day, day edit, settings, admin users, and password pages at
all four widths. Home measured 1280 px and 1600 px wide at those viewport widths;
scrolling pages filled the available width minus the browser scrollbar. No
horizontal document overflow; auth cards stayed at or below 420 px.

1. Playwright's WebKit build has no `MediaRecorder`/`getUserMedia`; the app correctly hides the record button. A Safari-format AAC `.m4a` voice note uploaded through the form is stored and served as audio, but this WebKit build cannot decode AAC (it also fails from a `blob:` URL of the same bytes, so the server is not at fault). **Re-check recording and playback on a real iPhone** — Safari records `audio/mp4`.
2. Upload, progress and `<video playsinline>` markup pass; the WebKit build has no H.264 decoder (same `blob:` cross-check). Re-check playback on a real iPhone.
3. Found during this QA: `.mov` files were sniffed as `application/octet-stream` and shown as plain file links. Fixed in `internal/media/sniff.go` (now `video/quicktime`).
4. In WebKit with mobile emulation, `getComputedStyle` reports wrong colors for some labels (black text on a transparent background) although the page paints correctly. Every computed-style or axe contrast finding is therefore re-measured from screenshot pixels, and only pixel-confirmed findings fail. The flagged labels measured 13.4:1. The pixel check is self-tested on every run with a deliberately low-contrast sample (measured 1.2:1).

### Method

A Playwright (Node) harness, kept outside the repo because the project has no Node tooling, drives each column against a fresh `panda serve` and data dir. Each row goes through the real UI.

- **Voice notes** come from a synthetic WebAudio stream standing in for the microphone. The alternate container is forced through `MediaRecorder.isTypeSupported`.
- **Videos** are browser-recorded fixtures padded to 40 MiB. The `.mov` is an MP4 re-branded to `qt  ` the way iPhone files are.
- **Error banners** come from an intercepted 413 response and from an aborted request.
- **Accessibility audits** run axe-core (WCAG 2.1 A/AA tags) on the home, day, edit, settings and admin pages, in both light and dark themes.

## Screen-reader walkthrough

A real VoiceOver and TalkBack walkthrough was **not** possible in this environment. As a proxy, the accessibility tree that screen readers consume was captured on every engine, for the home, day and upload pages. It was checked for the following:

- [x] The skip link comes first, then `banner`, `navigation "Main"` and `main`.
- [x] Every page has exactly one `h1`. The home page gets a visually hidden "Home" heading, added in this pass so heading navigation has a target.
- [x] Each day link has a single name: title, date, repeat, chips and countdown. Collection and tag chips are announced with their field ("Collection: Family", "Tags: Birthday"). Before this pass, tags were read without a prefix ("Collection: Default Default").
- [x] Decorative emoji and SVG swatches are hidden from the tree. Every button and input has a name. The upload progress bar is labelled, and the error banner is `role="alert"`.
- [ ] Real-device VoiceOver (iOS Safari) and TalkBack (Android Chrome) pass over home, day, upload and voice recording.

## Accessibility checklist

- [x] Every form control has a visible `<label>` or an `aria-label` (upload progress, nav).
- [x] Text contrast ≥ 4.5:1 and control borders ≥ 3:1 in light and dark themes (links #276749 on #f7f7f5 = 6.3:1; dark button text 10:1; future label 5.5:1).
- [x] Visible `:focus-visible` outline on all interactive elements; "Skip to main content" link is the first tab stop.
- [x] Day state is never color-only: icon + text ("2 days left", "Archived"); decorative emoji are `aria-hidden`.
- [x] Error banner is `role="alert"`; the language options carry `lang`.
- [x] axe-core reports no WCAG 2.1 A/AA violations in WebKit, Chromium or Firefox, light or dark (contrast findings pixel-verified; see note 4).
- [ ] VoiceOver / TalkBack on real devices (see *Screen-reader walkthrough*).

## Automated coverage

- `internal/i18n`: en and zh have identical key sets; `internal/web/polish_test.go`: every `t "key"` in templates exists in all locales, the layout landmarks are present, static asset URLs are content-versioned.
- `internal/media/sniff_test.go`: MediaRecorder WebM/MP4/Ogg voice notes are stored as audio, video stays video, and iPhone QuickTime `.mov` files are detected as `video/quicktime`.
- `internal/web/attachments_test.go`: voice upload stored as audio; multi-MB video and `.mov` uploads intact and render `playsinline`.
- `internal/web/a11y_list_test.go`: the home page has a heading, and day-list chips are announced with their field (en and zh).
