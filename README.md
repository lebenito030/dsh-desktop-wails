# DSH Desktop

[English](README.md) | [简体中文](README.zh-CN.md)

A lightweight desktop shell for **DSH (DeepSeek Harness)** — one executable that
installs DSH, runs it, keeps it alive, and shows its web UI in a native window.
Built with Go + [Wails v2](https://wails.io).

**The shell never modifies DSH.** DSH is installed at runtime from npm
(`@deepseek-ai/dsh`) and treated as a black box: the shell only talks to it over
stdout (the ready line) and HTTP.

## What it does

- **Self-bootstrapping** — on first launch it downloads Node.js and `npm install`s
  DSH into its data directory, with progress + log overlay. No Node on your PATH needed.
- **Managed web UI** — a frameless window loads DSH's own web UI through a local
  reverse proxy that exchanges the login token for session cookies, so you never
  see a browser or a token in a URL bar.
- **Restart** — one click from the tray; the shell re-resolves the port and token.
- **Update check** — compares the installed version against the npm registry on
  every start; updating never breaks the currently installed version.
- **Tray** — closing the window hides it to the tray; quitting is a deliberate act
  from the tray menu.
- **Process safety** — the DSH process tree is contained so nothing is left
  running after the shell goes away.

## Platform support

| | Windows | macOS | Linux |
|---|---|---|---|
| Package | `.exe` (zip) | `.app` (zip, universal) | binary (tar.gz) |
| Tray | native Win32 | `energye/systray` (Cocoa) | `energye/systray` (DBus / StatusNotifierItem) |
| Close button | hide to tray | hide to tray | hide to tray |
| Quit | tray → 退出 / Quit | tray → Quit | tray → Quit |
| Process containment | Job Object (kernel-enforced) | process group | process group |

Notes that matter:

- **Linux / GNOME**: GNOME does **not** show StatusNotifierItem icons by default —
  you need the *AppIndicator* extension (or a proxy such as
  [snixembed](https://git.sr.ht/~steef/snixembed)). KDE and most other desktops
  show it out of the box. Because an invisible tray would lock you out, the shell
  **degrades gracefully**: if no tray is available, closing the window quits the
  app instead of hiding it.
- **Escape hatch**: `dsh-desktop --quit` asks the running instance to exit
  (useful exactly in the case above). If no instance is running, it is a no-op.
- **macOS**: the build is unsigned. First launch: right-click the app → *Open*,
  or `xattr -cr /Applications/DSH\ Desktop.app`.
- `dsh-desktop -h`-style service controls live in the tray; there is deliberately
  no "stop service (keep shell running)" action — a shell without DSH has no
  purpose. See `docs/01-design.md` ("功能准入") for the reasoning.

## Build from source

Requirements: Go 1.25+, Node 20+, Wails CLI v2, and on Linux the webview
development packages.

```bash
go install github.com/wailsapp/wails/v2/cmd/wails@v2.15.0

# Linux only
sudo apt-get install -y libgtk-3-dev libwebkit2gtk-4.0-dev

wails build            # -> build/bin/dsh-desktop(.exe)
wails doctor           # environment self-check
```

Wails v2 does not cross-compile; build on the platform you target.
CI does exactly that — see `.github/workflows/`:
`test.yml` runs `go vet` + `go test ./internal/...` on all three OSes,
`release.yml` builds and publishes artifacts when you push a `v*` tag.

### Icons

All icons are generated, not hand-drawn:

```bash
python build/gen-icons.py    # needs numpy + Pillow
```

`build/gen-icons.py` documents every measurement behind the design (tray icon
sizes were measured against real Windows taskbars) and emits `appicon.png`,
`tray.ico`, `windows/icon.ico`, `tray.png` (Linux) and `tray-template.png`
(macOS monochrome template — the system recolors it for light/dark menu bars).

## Runtime layout

The data directory sits **next to the executable** when that location is
writable (portable mode); otherwise it falls back to
`%LOCALAPPDATA%\dsh-desktop-wails`. **It is not configurable** — on purpose
(see `docs/01-design.md`), because `config.json` lives inside the directory it
would have to choose, which is circular.

```
<dsh-desktop-data>/
├── config.json          # shell configuration
├── tray.log             # the only reliable diagnostic output (GUI process: no stderr)
└── runtime/
    ├── node/            # Node.js, downloaded on first run
    └── dsh/             # npm --prefix install target for @deepseek-ai/dsh
```

Useful consequences:

- **Uninstall = delete the data directory.** The executable itself is stateless.
- **Reset = delete `runtime/`** — the next start re-downloads Node and reinstalls DSH.
- Your DSH user data (plugins, sessions, credentials) is **not** here; it lives in
  `DSH_HOME` (default `~/.dsh`), which *is* configurable via `dshHome` below.
  Deleting this directory never touches your sessions.

## Configuration (`config.json`)

Created with defaults on first run and rewritten (with all fields filled in) on
every start. Read once at startup — restart the shell after changing it.

| Field | Default | Meaning |
|---|---|---|
| `nodeVersion` | `22.20.0` | Node version downloaded on first run |
| `nodeDownloadUrl` | `https://nodejs.org/dist/v%s/node-v%s-win-x64.zip` | URL template, both `%s` filled with the version |
| `npmRegistry` | `https://registry.npmjs.org` | registry for `npm install` and update checks |
| `dshPackage` | `@deepseek-ai/dsh` | package to install |
| `dshVersion` | `latest` | version / dist-tag used **on first install only** (updates always use `latest`) |
| `dshHome` | *(empty)* | overrides `DSH_HOME` — moves DSH's **entire** user-data tree (plugins, sessions, credentials, settings). See `docs/05-configuration.md` §5 before using it: it does not migrate existing data and it splits state from any client that leaves `DSH_HOME` unset |

Offline / China mirrors: point `nodeDownloadUrl` at
`https://npmmirror.com/mirrors/node/v%s/node-v%s-win-x64.zip` and `npmRegistry`
at `https://registry.npmmirror.com`.

## Architecture in one screen

- **Ready protocol** — DSH prints `dsh web: <URL>` (with a login token) on stdout;
  the supervisor parses that line and nothing else (`internal/dsh/supervisor.go`).
- **cookie-in-proxy** — DSH sets `SameSite=Strict` cookies, which a cross-origin
  iframe drops. The shell exchanges the token for cookies in Go and re-attaches
  them on every request through a local reverse proxy (`internal/proxy`).
- **Process containment** — Windows: a Job Object with kill-on-close, so even a
  crashed shell leaves no orphan `node.exe`. macOS/Linux: a dedicated process
  group killed with `SIGKILL` (`internal/dsh/job_*.go`).
- **Two tray implementations** — native Win32 on Windows (message queues are
  thread-private; the shell owns the whole chain), `energye/systray` elsewhere.
  Rationale: `docs/02-architecture.md` §6.

## Tests

```bash
go test ./internal/...
```

The tests cover the pieces that must not silently rot: the ready-line contract,
data-directory layout, the config write-back behaviour, zip root stripping, the
npm semver comparison and the registry client.

## Documentation

| Document | Answers |
|---|---|
| [docs/01-design.md](docs/01-design.md) | why a thin shell; scope rules; UI / icon / logging conventions (Chinese) |
| [docs/02-architecture.md](docs/02-architecture.md) | startup sequence, ready protocol, cookie proxy, process containment, tray (Chinese) |
| [docs/03-layout.md](docs/03-layout.md) | source tree + runtime data layout (Chinese) |
| [docs/04-build-and-run.md](docs/04-build-and-run.md) | build & troubleshooting (Chinese) |
| [docs/05-configuration.md](docs/05-configuration.md) | every `config.json` field (Chinese) |
| [docs/06-dependencies-and-versioning.md](docs/06-dependencies-and-versioning.md) | dependency layers, how DSH updates are tracked (Chinese) |

`AGENTS.md` contains the working rules for AI coding agents.
