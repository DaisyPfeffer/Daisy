# GoDM — a download manager written in Go

GoDM is an original, open-source download manager inspired by the feature set of
Internet Download Manager (IDM). It does **not** contain, copy or decompile any IDM code.

It comes as two programs:

- **GoDM.exe** — the Windows desktop app: toolbar, category tree, download list, progress
  windows, tray icon, clipboard catching, scheduler and options (dark theme).
- **godm-cli** — the same engine on the command line (Windows, macOS, Linux), plus the
  helper that browsers talk to.

The engine only uses the Go standard library; the desktop app uses the
[Walk](https://github.com/lxn/walk) Windows toolkit (no CGO needed).

## The desktop app (Windows)

Run `GoDM-Setup-2.0.0.exe`, or build it yourself (below), then start **GoDM** from the Start menu.

| Part of the window | What it does |
|---|---|
| Toolbar | Add URL · Resume · Stop · Stop All · Delete · Delete Completed · Options · Scheduler · Start Queue · Stop Queue · About |
| Categories (left) | All Downloads → Compressed, Documents, Music, Programs, Video; Unfinished; Finished; Queues. Each shows how many files it has |
| Download list | File Name, Size, Status (percent), Time left, Transfer rate, Last Try, Description. Click a heading to sort, double-click a row to open its progress window (or the file when finished), right-click for Open, Open folder, Resume, Stop, Redownload, Copy address, Delete |
| Progress window | Address, status, size, downloaded, speed, time left, resume support, overall bar, a bar of every connection's part, and a table of connections. Pause/Resume, Cancel, Hide |
| Add URL | Paste a link (it is filled in from the clipboard), optional login. Then **Download File Info** asks for category, folder and name, shows the real size, then *Start Download* or *Download Later* |
| Download complete | Open, Open with..., Open folder |
| Tray icon | Closing the window keeps GoDM in the tray; downloads keep going. Right-click the icon to exit |
| Clipboard | Copy a link to a file (zip, exe, mp4, pdf, iso...) anywhere and GoDM offers to download it |
| Scheduler | Start the queue at a time, stop it at a time, every day or once, and when it is done: nothing, exit GoDM, or shut down the PC |
| Options | Connections per file, downloads at once, speed limit, auto retry, download folder, sort by type, clipboard, dialogs, tray, start with Windows, browser setup |

Keyboard: `Ctrl+N` add URL, `Del` delete, `Ctrl+A` select all, `Enter` open.

Only one GoDM runs at a time: starting it again brings the open window to the front.
`GoDM.exe -tray` starts it hidden in the tray (used by "Start with Windows").


## Theme, language and file type

- **Dark / Light theme** – `View → Theme` or `Options → Appearance`: *Dark*, *Light* or *Same as Windows*.
  It changes live (window, toolbar, categories, list, title bar, menus) and is saved in `config.json` (`"theme"`).
- **Languages** – `View → Language` or `Options → Appearance`: English, Français, العربية, Español, Deutsch.
  Saved in `config.json` (`"language"`). The list, toolbar, menus, status bar, tray menu and all
  windows are translated live.
- **Any other language** – `View → Language → More languages...` opens the GoDM `lang` folder and writes
  `_template.json` (all texts). Copy it to e.g. `it.json`, translate the right-hand side, set `"name"`
  (and `"rtl": true` for right-to-left languages) and restart GoDM. The same works for a `lang` folder
  next to `GoDM.exe`. A language file may translate only some texts; the rest stays English.
- **File type** – in *Download File Info* the **File type** box changes the extension the file is saved with
  (pick one from the list or type any, e.g. `.mp4`); *Save As* and the category folder follow it.

## Features

| Feature | How GoDM does it |
|---|---|
| Faster downloads with several connections | Up to 32 parallel HTTP range connections per file (default 8). HTTP/2 is turned off on purpose so each connection is a real TCP connection |
| Dynamic segmentation | When one connection finishes, it splits the largest unfinished part in half and helps, so all connections stay busy until the end |
| Pause / resume, survives restarts and crashes | Progress per segment is saved every 2 s. Resuming checks `ETag`/`Last-Modified` with `If-Range`, so a file that changed on the server is never mixed up |
| Automatic retry | Each connection retries up to 5 times with backoff. Failed downloads are re-queued every 30 s (busy server, timeouts, network drops). 404/403 and similar errors are not retried |
| Queue + scheduler | Limits how many downloads run at once (default 3), and can start a download at a set time (`-at 02:00`) |
| Speed limiter | Global limit, plus a per-download limit |
| Browser integration | A Chrome/Edge/Brave/Firefox extension + a native-messaging helper send browser downloads (with cookies and referer) to GoDM. Right-click a link → "Download with GoDM" |
| Site logins | Saved user/password per site (HTTP Basic). `user:pass@host` URLs work too |
| Categories | Files are sorted into `Compressed/`, `Documents/`, `Music/`, `Video/`, `Programs/`, `General/` |
| Correct file names | `Content-Disposition` (including UTF-8 names), then the URL, then the content type. Names are made safe on Windows. Duplicates become `name (1).ext` |
| Checksum | Optional `sha256:` / `sha1:` / `md5:` check before the file is saved under its final name |
| Web dashboard | `http://127.0.0.1:9614` shows live per-connection progress bars, speed, ETA, and lets you add, pause, resume and remove downloads, and change settings |
| Safe local API | Only listens on 127.0.0.1, every API call needs a random token, and passwords and cookies are never sent back by the API |

## Build

You need Go 1.22 or newer. Windows programs can be built from any system.

```bash
make windows          # -> installer/GoDM.exe and installer/godm-cli.exe
make installer        # also builds installer/GoDM-Setup-2.0.0.exe (needs NSIS)
make build            # -> ./godm-cli for the current system
make test             # runs all tests with the race detector
make release          # all binaries in ./dist
```

Without `make` (on Windows):

```bat
go build -ldflags "-H windowsgui" -o GoDM.exe .\cmd\godm-gui
go build -o godm-cli.exe .\cmd\godm
```

`cmd/godm-gui/rsrc_windows_amd64.syso` holds the app icon and the Windows manifest the
window toolkit needs; recreate it with `make resources` if you change them. For 32-bit or
ARM Windows, regenerate it with the matching `-arch` before building.

## Command line (godm-cli)

The desktop app and `godm-cli serve` share the same downloads and settings, so run only one of them at a time.

### 1. One-off download in the terminal (no server needed)

```bash
godm-cli get https://example.com/big.iso -c 16
godm-cli get https://example.com/file.zip -o renamed.zip -d ~/Downloads -limit 500   # 500 KB/s
```

Press Ctrl+C to stop. Run the **same command** again and it resumes where it stopped.

### 2. Download manager mode without the desktop app (queue + web dashboard)

```bash
godm-cli serve                       # prints the dashboard address with your token
godm-cli ui                          # prints the dashboard address again
godm-cli add URL1 URL2 ...           # queue downloads
godm-cli add URL -at "23:30"         # scheduled start
godm-cli add URL -paused -c 4 -referer https://site/ -cookie "a=b" -H "X-Key: 1"
godm-cli list
godm-cli pause <id|all>
godm-cli resume <id|all>
godm-cli rm <id> [-delete]
godm-cli limit 1024                  # global limit 1024 KB/s (0 = unlimited)
```

Settings live in `config.json` inside the GoDM folder:
- Windows: `%AppData%\godm`
- macOS: `~/Library/Application Support/godm`
- Linux: `~/.config/godm`
- or set `GODM_HOME` to use another folder

To save site logins, add them to `config.json`:

```json
"logins": { "files.example.com": { "username": "me", "password": "secret" } }
```

### 3. Browser integration

**With the desktop app:** Options → Browser → *Set up browser integration* (the installer
already does this), then load the `extension` folder from the GoDM install folder:
open `chrome://extensions` (or `edge://extensions`), turn on **Developer mode**, click
**Load unpacked**. By default GoDM asks where to save each browser download.

**Without the desktop app:**

1. Run `godm-cli serve` and leave it running.
2. Open `chrome://extensions` (or `edge://extensions`), turn on **Developer mode**, click
   **Load unpacked** and choose the `extension/` folder. The extension has a fixed ID, so
   step 3 works as written.
3. Register the helper:
   ```bash
   godm-cli install-host -chrome-id <the-id>
   godm-cli install-host -firefox          # Firefox: load extension/ via about:debugging
   ```
4. Restart the browser. New downloads now go to GoDM. Click the extension icon to turn
   capturing on or off. If GoDM is not running, the browser keeps the download, so nothing is lost.

## HTTP API (for scripts)

Every call needs the header `X-GoDM-Token: <token>` (or `Authorization: Bearer <token>`).

| Method | Path | Body |
|---|---|---|
| GET | `/api/downloads` | – |
| POST | `/api/downloads` | `{"url": "...", "urls": [...], "fileName", "dir", "connections", "referer", "cookie", "userAgent", "headers", "username", "password", "checksum", "speedLimit", "paused", "startAt"}` |
| GET | `/api/downloads/{id}` | – |
| POST | `/api/downloads/{id}/pause` · `/resume` | – |
| DELETE | `/api/downloads/{id}?deleteFile=1` | – |
| POST | `/api/pause-all` · `/api/resume-all` | – |
| GET / PUT | `/api/settings` | `{"downloadDir", "maxConcurrent", "connections", "speedLimit", "autoRetry", "categorize"}` |

## Project layout

```
cmd/godm-gui/        GoDM.exe, the Windows desktop app (+ icon/manifest resource)
cmd/godm/            godm-cli: get, serve, add, list, pause, resume, rm, limit, host, install-host
internal/gui/        desktop app: main window, toolbar, category tree, list, dialogs, progress windows,
                     tray, clipboard, scheduler (view.go / schedule.go are tested on every system)
internal/engine/     the download engine: probing, segments, dynamic splitting, retries, limiter, names, checksum
internal/manager/    queue, concurrency, scheduler, auto-retry, speed, saving/loading state
internal/server/     token-protected local API + embedded web dashboard (web/index.html)
internal/nativehost/ browser native-messaging protocol (4-byte length + JSON)
internal/config/     settings and saved logins
extension/           Chrome / Edge / Firefox extension (Manifest V3)
```

## Not included (yet)

- FTP, video stream grabbing (HLS/DASH) and the site grabber/spider
- Several named queues (there is one main queue)
- A macOS / Linux desktop window (use `godm-cli serve` and the web dashboard there)
