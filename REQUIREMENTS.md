# Requirements

- Windows 10/11.
- Go 1.22 or newer.
- OBS Studio with OBS WebSocket 5.x enabled.
- RivaTuner Statistics Server running before starting the timer.

OBS WebSocket usually listens on `ws://localhost:4455`. Configure the password in a local `config.json`; do not commit that file.

The app writes the timer through RTSS shared memory, so fullscreen overlays depend on RTSS being installed and active.
