# esp-football — Implementation Plan

## Board: Adafruit ESP32-S3 Reverse TFT Feather

| Property | Value |
|---|---|
| Chip | ESP32-S3 (dual-core 240 MHz) |
| Flash | 4 MB |
| PSRAM | 2 MB (available for image caching) |
| Display | 1.14" IPS TFT, ST7789, **240×135 px**, landscape |
| Display bus | SPI (40 MHz) |
| WiFi | 2.4 GHz 802.11 b/g/n, built-in |

### Button Pinout

| Button | GPIO | Default level | Pressed level | Notes |
|---|---|---|---|---|
| D0 | 0 | HIGH | LOW | Also BOOT button |
| D1 | 1 | LOW | HIGH | |
| D2 | 2 | LOW | HIGH | |

### Display Pin Assignments (from esp-football.c)

| Signal | GPIO |
|---|---|
| PWR | 7 |
| SCK | 36 |
| MOSI | 35 |
| CS | 42 |
| DC | 40 |
| RST | 41 |
| Backlight | 45 |

Display is configured with `swap_xy=true, mirror_y=true` — logical resolution is **240 wide × 135 tall** (landscape).
Gap offsets: `set_gap(40, 52)` — confirms non-standard ST7789 panel offset.

---

## Lambda API

**Endpoint:** AWS Lambda function URL (public, HTTPS)

**Response shape:**

```json
{
  "game_state":    "in | pre | post | none",
  "sleep_seconds": 60,
  "current_match": {
    "home_team":      "NOR",
    "away_team":      "SWE",
    "home_score":     "2",
    "away_score":     "1",
    "home_image_url": "https://...",
    "away_image_url": "https://...",
    "match_clock":    "45:00"
  },
  "next_match": {
    "date":               "2026-07-10",
    "time":               "19:00Z",
    "opponent":           "DEN",
    "opponent_image_url": "https://..."
  }
}
```

**`game_state` values and field presence:**

| `game_state` | `current_match` | `next_match` | Notes |
|---|---|---|---|
| `"in"` | ✅ populated | `null` | Live match |
| `"post"` | ✅ populated | ✅ if available | Match finished today |
| `"pre"` | `null` | ✅ populated | Today's match not yet kicked off |
| `"none"` | `null` | ✅ if available | No match today |

`sleep_seconds` is always present — the ESP waits this long before re-polling.

---

## Display Modes

### Mode 1 — Live/Score View (current goal)

Activated when: `game_state == "in"` or `game_state == "post"`

Layout (240×135 landscape):

```
+------------------------------------------+
|  [HOME LOGO]   0 - 0   [AWAY LOGO]       |
|    HOM                     AWY           |
|              45:00'                      |
+------------------------------------------+
```

- Home team logo: left third of screen
- Away team logo: right third of screen
- Scores: centred between logos, large font
- 3-char abbreviation: below each logo
- Match clock (or "FT" for post): centred bottom

### Mode 1b — Upcoming View

Activated when: `game_state == "pre"` or `game_state == "none"`

Layout:

```
+------------------------------------------+
|  [NOR LOGO]           [OPP LOGO]         |
|    NOR                    OPP            |
|         Sun 6 Jul · 15:00 BST            |
+------------------------------------------+
```

- Both team logos + abbreviations
- Date and local time centred below

### Mode 2 — TBD

Future mode, toggled with D1/D2 buttons.

---

## Button Behaviour

- **D1** — cycle forward through display modes
- **D2** — cycle backward through display modes  
- **D0** — reserved (BOOT, do not use for app logic)

---

## Architecture (ESP32 firmware)

### Tasks / Threads

```
app_main()
 ├── init_display()          — SPI, ST7789, LVGL port (already works)
 ├── init_wifi()             — connect to WiFi (NVS-stored credentials)
 ├── init_buttons()          — configure GPIO interrupts for D1, D2
 ├── ui_task()               — LVGL tick task (already handled by lvgl_port)
 ├── poll_task()             — HTTP GET lambda, parse JSON, update state
 └── render_task()           — read state, draw LVGL widgets
```

### State machine

```
global app_state {
    game_state:  enum { IN, PRE, POST, NONE }
    display_mode: enum { MODE_SCORE, MODE_TBD }
    home_team, away_team: char[8]
    home_score, away_score: char[8]
    match_clock: char[16]
    next_date, next_time: char[32]
    next_opponent: char[8]
}
```

### Polling flow

1. `poll_task` fires, HTTP GETs lambda URL
2. Parses JSON response into `app_state`
3. Sets a FreeRTOS event group bit to trigger re-render
4. Sleeps for `sleep_seconds` before next poll

### Image caching (future)

- On first poll, download team logo PNGs from ESPN CDN URLs
- Decode JPEG/PNG with `esp_jpeg` (built-in IDF component) into RGB565
- Store decoded bitmaps in PSRAM (2 MB available)
- Cache key: team abbreviation. Re-fetch only if team changes.

---

## Implementation Roadmap

### ✅ Phase 0 — Display proof of life
- [x] Display initialises, renders red background + text

### Phase 1 — WiFi + Lambda polling
- [ ] Add WiFi credentials (NVS or `sdkconfig`)
- [ ] Add `esp_http_client` to fetch lambda URL
- [ ] Add `cJSON` parsing of response
- [ ] Implement poll task with `sleep_seconds` delay
- [ ] Stub out state struct

### Phase 2 — Score/live screen (Mode 1)
- [ ] LVGL layout: two logo placeholders + abbreviations + score + clock
- [ ] Wire state → LVGL labels
- [ ] Handle `in` vs `post` (show "FT" clock for post)

### Phase 3 — Upcoming screen (Mode 1b)
- [ ] LVGL layout: logos + abbreviations + date/time
- [ ] Convert UTC next_match_time to local time
- [ ] Wire `pre` / `none` state → layout

### Phase 4 — Button handling
- [ ] GPIO ISR for D1/D2
- [ ] Mode cycling logic
- [ ] Render correct screen on mode change

### Phase 5 — Team logo images
- [ ] HTTP fetch logo PNG from ESPN URL
- [ ] Decode to RGB565 with esp_jpeg / libjpeg
- [ ] Store in PSRAM, keyed by abbreviation
- [ ] Render as LVGL image objects

### Phase 6 — Polish
- [ ] Error screen (no WiFi, lambda error)
- [ ] Dim backlight when idle / on battery
- [ ] OTA firmware update
