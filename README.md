# obs-pktm

Windows helper for OBS recordings with global hotkeys, OBS WebSocket, RTSS timer output, scene validation, and recording file post-processing.

## Features

- `F9` starts OBS recording and the RTSS countdown.
- `F10` stops the current recording and timer.
- `F12` exits the app.
- The timer is written to RivaTuner Statistics Server shared memory, so it can appear over fullscreen games.
- When the countdown reaches zero, the app shows a large `RESET / WAITING` message in RTSS and waits for OBS to finish the current recording before starting the next one.
- When a recording starts, the app verifies that OBS is actually recording and that the current scene screenshot is not black.
- Finished recordings can be renamed with a configurable prefix, normalized window name, and `[n]` counter.
- Manual recordings that are too short can be moved to a separate folder.
- Session actions are written to `logs/last_session.jsonl`.

## Requirements

See [REQUIREMENTS.md](REQUIREMENTS.md).

Short version:

- Windows.
- Go 1.22+.
- OBS Studio with WebSocket enabled.
- RivaTuner Statistics Server running.

## Configuration

Copy `config.example.json` to `config.json` and edit your local values:

```powershell
Copy-Item config.example.json config.json
```

`config.json` is ignored by Git because it can contain your OBS WebSocket password.

Example:

```json
{
  "timer": {
    "duration_seconds": 600
  },
  "obs_websocket": {
    "url": "ws://localhost:4455",
    "password": "your-obs-websocket-password",
    "request_timeout_seconds": 5
  },
  "recording_rename": {
    "prefix": "PKTM_",
    "min_duration_seconds": 580,
    "min_size_megabytes": 0,
    "directory": "${USERPROFILE}\\Videos\\Work\\",
    "manual_short_duration_seconds": 5,
    "manual_short_directory": "${USERPROFILE}\\Videos\\Work\\TRASH\\"
  },
  "recording_check": {
    "startup_delay_seconds": 1,
    "startup_timeout_seconds": 5
  }
}
```

Portable paths support `${USERPROFILE}`, `%USERPROFILE%`, and `~`.

If `recording_rename.directory` is empty or does not exist, the app tries to ask OBS for its current recording directory. If `manual_short_directory` is empty, short manual recordings are moved to a `TRASH` folder next to the original OBS output.

## Run

```powershell
go run .
```

## Build

Console build:

```powershell
go build -o obs-pktm.exe .
```

Build without a console window:

```powershell
go build -ldflags="-H windowsgui" -o obs-pktm.exe .
```

## Repository Notes

The repository intentionally ignores:

- `config.json`
- `logs/`
- compiled `.exe` files

Commit `config.example.json`, not your local configuration.
