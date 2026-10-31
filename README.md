# lanxfer

**LAN transfer** — send files and messages directly between machines on your
LAN, with no server in between.

lanxfer is a small, dependency-free Go CLI. Run `lanxfer recv` on the machine
that should receive, then `lanxfer send <name> <file-or-folder>` on the
machine that has it. Receivers are found automatically on the local network.
Short text can be sent the same way with `lanxfer say`. A desktop app with
drag and drop is built from the same code; see [GUI](#gui).

## Status

Early but usable: file and folder transfer with progress display, short text
messages, automatic peer discovery with a remembered peer list, sending by
name, and a history of what was sent and received. A desktop app (GUI) is
being added; there is no pre-built download yet. No encryption yet.

## Install

### Homebrew (macOS / Linux)

    brew install --cask capybara-translation/tap/lanxfer

To upgrade to the latest release:

    brew upgrade --cask capybara-translation/tap/lanxfer

### go install

    go install github.com/capybara-translation/lanxfer/cmd/lanxfer@latest

### Pre-built binaries

Download the archive for your platform from the
[Releases page](https://github.com/capybara-translation/lanxfer/releases)
(`tar.gz` for macOS/Linux, `zip` for Windows) and put the `lanxfer` binary
somewhere on your `PATH`. `checksums.txt` on the same page lists the SHA-256
of every archive.

### Build from source

    git clone https://github.com/capybara-translation/lanxfer.git
    cd lanxfer
    go build -ldflags "-s -w -X main.version=$(git describe --tags --always --dirty)" ./cmd/lanxfer

Requires the Go version declared in `go.mod`. The command-line tool has no
third-party dependencies. The desktop app in `gui/` is a separate Go module
with its own dependencies; see [GUI](#gui).

### What `lanxfer --version` prints

| How you installed | Output |
|---|---|
| Homebrew or a Releases archive | `lanxfer vX.Y.Z` |
| `go install ...@vX.Y.Z` | `lanxfer vX.Y.Z` |
| `go install ...@latest` from a non-tagged commit | `lanxfer dev` (pseudo versions are intentionally hidden) |
| `go build` with the `ldflags` example above | whatever `git describe` resolves to |
| Plain `go build` on a clean, tagged checkout | `lanxfer vX.Y.Z` |
| Plain `go build` on any other checkout | `lanxfer dev` (untagged commits and uncommitted changes are hidden) |

## GUI

The desktop app is a window and a menu bar (system tray) icon around the same
receiver that `lanxfer recv` runs. There is no pre-built app yet; build it
from source as described below.

- The left side lists the receivers on your network. Pick one to open its
  chat.
- Each peer has a chat with the history of what you sent and received,
  including what you sent with `lanxfer send` and `lanxfer say`.
- Drop files or folders anywhere in the window to send them to the open
  chat. Type text and press `⌘+Enter` (`Ctrl+Enter` on Windows and Linux) to
  send it.
- Each transfer shows its progress and a Cancel button.
- A file, folder, or message that arrives raises a notification, unless you
  are already looking at that chat.
- Closing the window only hides it: the app keeps receiving. The tray icon
  offers `Open lanxfer`, `Open Save Folder`, and `Quit`; Quit stops the
  receiver.
- Settings sets the name other machines see and the save folder.

The app uses the same config folder as the CLI (see
[Peers and history](#peers-and-history)), so it shares the device ID and the
history. Its own settings are in `gui.json` there. It listens on port `8425`
like `lanxfer recv`, so the two cannot run on one machine at the same time.
If the port is taken, the app shows `Not receiving` with a Retry button;
sending still works. While the app is not the receiver, it shows only what
it records itself. Files and messages that `lanxfer recv` receives, and what
the CLI sends, go to the shared history but appear in the app after it is
restarted, because the app reads the history once at start. For development,
the environment variable `LANXFER_PORT` moves the app to another port.

### Build the app

The app uses [Wails](https://wails.io) v3.0.0. It needs Go 1.26 or later,
Node.js 20.19 or later (or 22.12 or later) with npm, and the build tools
of your platform (on macOS, the Xcode command line tools). Install the
`wails3` tool, then package the app from the `gui` folder:

    go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0
    cd gui
    wails3 task darwin:package

This builds the web frontend and writes `gui/bin/lanxfer.app`, signed ad hoc
for local use. Use `linux:package` instead of `darwin:package` on Linux.
`wails3 doctor` checks that the prerequisites are installed.

For Windows, this writes `gui/bin/lanxfer.exe`, a single file you can copy to
the machine and run. It needs no C compiler, so it also works on macOS and
Linux:

    wails3 task windows:build ARCH=amd64

The app has been run on macOS and Windows. Linux has not been compiled or run.

### Notifications

On Windows and Linux, notifications come from the system. On macOS the app
shows its own small window in the corner of the screen instead of using
Notification Center: macOS refuses its notification API to an app that is
not signed with a developer certificate. Clicking a notification opens that
chat.

### GUI security notes

Names and messages from other machines are shown as text only, never as
HTML. `Show in Finder` (`Show in Explorer`, `Show in Folder`) reveals a
received item in its folder and never opens it, so a received program or
`.app` is not run by accident. The app has the same trust model as the CLI:
no authentication and no encryption (see [Security notes](#security-notes)).

On macOS the app marks what it receives as downloaded (the quarantine
attribute), so macOS applies its usual checks before a received app opens.
macOS may refuse an unsigned app received this way. To open one you trust,
clear the mark with `xattr -cr <path>`, or allow it in System Settings under
Privacy & Security. The CLI (`lanxfer recv`) and the Windows app do not mark
received files.

## Usage

Receiver (waits in the foreground; stop with Ctrl-C):

    lanxfer recv [--dir <path>] [--port 8425] [--max-size <bytes>] [--name <name>]

See who is receiving on your network:

    lanxfer peers [--port 8425] [--wait 1s]
    NAME    ADDRESS            OS      STATUS   LAST SEEN
    mac2    192.168.1.20:8425  darwin  online   -
    ubuntu  192.168.1.30:8425  linux   offline  2026-10-19 21:40

`LAST SEEN` (local time) is shown only for offline peers. See
[Peers and history](#peers-and-history).

Sender, by name or by IP address:

    lanxfer send [--port 8425] [--name <name>] <ip-or-name> <file-or-folder>

Defaults: port `8425`, save directory `~/lanxfer`, maximum file size 50 GiB,
name = hostname without `.local`. Names are matched case-insensitively. If a
receiver uses a non-default `--port`, pass the same `--port` when sending to
it.

Example:

    # on the machine called mac2
    lanxfer recv

    # on another machine
    lanxfer send mac2 ~/Downloads/movie.mp4
    sending movie.mp4 -> mac2 (192.168.1.20:8425)
    done: 4.8 GiB in 41.2s (119.3 MiB/s)

While the transfer runs, a progress line is shown below `sending` and is
replaced by the `done` line when it finishes (it goes to stderr, and only when
stderr is a terminal):

      42%  2.0 GiB / 4.8 GiB  118.3 MiB/s  0:24 left

Press Ctrl-C to cancel; the receiver discards the partial file. The receiver
logs each transfer with the sender's name (`--name`, default hostname) next to
its IP address:

    2026/10/05 22:10:03 receiving "movie.mp4" (4.8 GiB) from mac1 (192.168.1.10)
    2026/10/05 22:10:44 received "movie.mp4" (4.8 GiB) from mac1 (192.168.1.10)

If a file with the same name already exists on the receiver, the new one is
saved as `movie (1).mp4`, `movie (2).mp4`, and so on. Existing files are
never overwritten.

### Sending folders

Pass a folder instead of a file to send it with everything inside:

    lanxfer send mac2 ~/Pictures/trip
    sending trip/ (152 files, 1.2 GiB) -> mac2 (192.168.1.20:8425)
    done: 1.2 GiB in 10.4s (118.0 MiB/s)
    skipped 2 items (system files, links, and other special files):
      .DS_Store (system file)
      2024/._IMG_0001.jpg (system file)

The receiver saves it as `trip` (or `trip (1)` and so on if the name is
taken) only after everything has arrived; an interrupted transfer leaves
nothing behind. The executable bit is kept, and so are modification times
for the files and folders inside (the received top folder itself does not
keep the sender's time; it gets the time it was received).

Not sent: symbolic links, special files, and junk that operating systems
create on their own: `.DS_Store`, `._*`, `Icon\r`, `Thumbs.db`, `desktop.ini`,
and the folders `__MACOSX`, `.Spotlight-V100`, `.fseventsd`, `.Trashes`,
`.TemporaryItems`, and `.DocumentRevisions-V100`. Up to 10 skipped items are
listed with the reason, followed by a count of the rest. A folder containing
a name the receiver would reject (for example a Windows reserved name such as
`CON`) is refused before anything is sent, as is one with paths nested deeper
than 64 folders or longer than 4096 bytes. The receiver rejects a folder
holding two names that differ only in letter case if its file system ignores
case (the default on macOS and Windows).
A receiver running on Windows rejects names containing `< > : " | ? *`, so
such files or folders cannot be sent to Windows.

### Sending text

To send short text, use `say` instead of `send`:

    lanxfer say [--port 8425] [--name <name>] <ip-or-name> [text...]

    lanxfer say mac2 "hello"
    pbpaste | lanxfer say mac2

Without text arguments, `say` reads stdin. Text is limited to 1 MiB; send
anything larger as a file. The receiver prints each message and records it in
the [history](#peers-and-history):

    --- 2026-09-30 10:12:03 from mac1 (192.168.1.10) ---
    hello

The sender's name defaults to its hostname (`--name` overrides it) and is
always shown next to its IP address.

### Peers and history

While `lanxfer recv` runs, it keeps a list of the receivers it has seen and
saves it, so the list survives a restart (everyone is offline until heard
from again). `lanxfer peers` shows that list when a `recv` runs on the same
machine, including peers that are offline. Without one, or while its list is
still empty, it broadcasts a query, waits for `--wait`, and shows only the
receivers that answer, all as online.

A newly started receiver is learned by the others within about a minute. One
that has stopped is shown as offline after about two minutes. A peer's name
is its hostname unless it runs `lanxfer recv --name <name>`, so use `--name`
for a friendly name such as `mac2`.

When you send by name, the local `recv`'s list is used first:

- An online peer is used as listed.
- An offline peer is queried directly at its last address, and used only if
  the same device answers there. If not, for example because it got a new
  address, a broadcast search looks for the same device ID (under any name),
  or for the same name when the entry has no ID. A different device ID with
  the same name is never used. If that finds nothing either, the send fails
  with `<name> is offline (last seen ...)`.
- If several online peers have the name, the send fails and lists their
  addresses; send to one of them by IP address.
- A name that is not in the list falls back to a broadcast search, as does
  any name when there is no `recv` on this machine or its list is empty.

So once a receiver has been seen, sending by name keeps working even while
broadcasts do not get through.

Sends and receipts are recorded in `history.jsonl`, one JSON object per line.
`send` and `say` record every transfer they attempted, whether it succeeded,
failed, or was cancelled. A failure before a target is chosen (an unknown
name, a bad path or text) is not recorded. `recv` records the texts, files,
and folders it received. A CLI command hands its entry to the `recv` on the
same machine, and appends it to the file itself when there is none. Only a
`recv` with the same device ID counts, so on a machine shared by several
users, one user's commands do not use another user's `recv`. If a send
cannot be recorded, the command only prints a warning. When a history is
available but a received text cannot be written to it, the receiver refuses
the text with `500` and does not print it; a file or folder that is already
saved only produces a warning.
If the config folder is unusable, there is no history at all: the receiver
warns at startup, and texts are shown but not saved (and answered with `204`).

The history, the peer list (`peers.json`), and the device ID (`device-id`)
live in the config folder `lanxfer` inside the user config directory:

| OS | Config folder |
|---|---|
| macOS | `~/Library/Application Support/lanxfer` |
| Windows | `%AppData%\lanxfer` |
| Linux | `~/.config/lanxfer` (or `$XDG_CONFIG_HOME/lanxfer`) |

The folder is created with mode `0700` and the files with `0600`. Earlier
versions appended texts to `messages.log` in the save directory. That file is
no longer written; an existing one is left alone.

## Security notes

**lanxfer has no authentication and no encryption. Use it only on a LAN you
trust.** Anyone who can reach the receiver's port can upload files to it.

Built-in protections:

- Connections are accepted only from private (RFC 1918 / ULA), link-local,
  and loopback addresses. This is a safety net in case the port is ever
  exposed to the internet, not a substitute for a firewall.
- Requests that carry a browser `Origin` header are rejected, so a web page
  cannot push messages or files into a receiver. lanxfer's own clients never
  send one.
- Filenames are validated; the receiver never writes outside its save
  directory (path traversal, absolute paths, and control characters are
  rejected, as are names of the form `.lanxfer-*.part`, which are reserved
  for temporary files).
- Existing files are never overwritten; collisions are renamed.
- Folders are checked entry by entry: every path element passes the same
  filename checks, only regular files and folders are accepted (never
  symbolic or hard links), and all writes go through `os.Root`, which
  refuses to leave the temporary folder. Received files are `0600`, or
  `0700` if they were executable; folders are `0700`. The receiver also
  rejects sparse entries, entries larger than the stream that carries them,
  paths deeper than 64 levels or longer than 4096 bytes, and names that
  differ from an earlier entry only in letter case or Unicode normalization
  (instead of merging them).
- Files are written to a temporary `.part` file and renamed into place only
  after the full declared size has arrived, so an interrupted transfer never
  leaves a truncated file that looks complete.
- Uploads larger than `--max-size` (for a folder, its whole tar stream) are
  rejected before any data is read.
- The save directory is created with mode `0700` and files with `0600`.
- Received text is printed with control characters (other than newline and
  tab) and bidirectional formatting characters replaced by visible escapes
  such as `\x1b`, so a sender cannot move the cursor, recolor, hide, or
  reorder what appears on the receiver's terminal. The history keeps the
  text as received, so a program that displays it must do the same.
- The local API (see Protocol) answers only requests from a loopback address
  with a loopback `Host` header, so other machines and DNS-rebinding web
  pages cannot use it. Any program on the same machine can read the peer
  list; recording a send also takes the device ID.
- Discovery packets reveal only the receiver's name, port, OS, and device
  ID, and are sent only to private, link-local, or loopback addresses.

Discovery uses UDP broadcast on the same port number (8425/udp). If the
receiver has a firewall, allow both TCP and UDP on that port. Names are
self-declared and unauthenticated: anyone on the LAN can announce any name,
so treat a name as a convenience, not as proof of identity. The device ID
(a random 128-bit value created on first use) is self-declared in the same
way and proves nothing yet; pairing (see the roadmap) is meant to bind keys
to it.

Stopping either side with Ctrl-C during a transfer normally leaves nothing
behind: the receiver deletes the partial file or folder (it waits up to 5
seconds for this while shutting down). If the receiver is killed forcibly or
the machine loses power, a `.lanxfer-*.part` file or folder can remain in the
save directory. `lanxfer recv` deletes such leftovers on startup once nothing
in them has been modified for 10 minutes.

## Troubleshooting

If a machine does not show up in `lanxfer peers`, wait a minute: a receiver
is picked up at the next broadcast. Discovery relies on broadcast, which
Wi-Fi delivers less reliably than the direct connections used for sending.
Some machines, Macs in particular, can miss broadcasts for minutes at a time
while still receiving files and messages sent to their IP address.

A receiver that your own `lanxfer recv` has seen once can still be reached by
name when broadcasts do not get through. For one it has never seen, send to
its IP address instead of its name.

If sending to the IP address fails too, something is blocking the connection.
Check on that machine:

- **VPN.** Some VPN clients block local-network traffic, depending on their
  state and policy (for example while the tunnel is being established).
  Try again with the VPN disconnected, or allow local LAN access in its
  settings.
- **Firewall.** On macOS, allow incoming connections for `lanxfer` (System
  Settings → Network → Firewall → Options). A binary rebuilt with `go build`
  counts as a new app and must be allowed again. On Ubuntu with ufw:
  `sudo ufw allow 8425`. On Windows, allow `lanxfer.exe` when Windows
  Defender Firewall asks, on private networks.
- **Is recv running?** On the receiver itself,
  `echo '{"lanxfer":1,"type":"query"}' | nc -u -w1 127.0.0.1 8425` should
  print a reply. If it does, the receiver works and something in between is
  blocking it.

The default name is the hostname, which macOS may change depending on the
network (DHCP or a VPN can rename the machine). If you send by name, pin it
with `lanxfer recv --name <name>`.

## Protocol

The receiver is a plain HTTP server. A transfer is a single request:

    PUT /files/<url-encoded-filename>
    Content-Length: <size>
    Content-Type: application/octet-stream
    X-Lanxfer-From: mac1

    <raw file bytes>

Responses: `201` (body contains the saved filename), `400` invalid filename,
`403` disallowed source address, `411` missing Content-Length, `413` too
large, `500` storage error.

`X-Lanxfer-From` is optional and only used in the receiver's log and
history; a missing or invalid one leaves just the IP address.

`X-Lanxfer-From-Id` is also optional: the sender's device ID (32 lowercase
hex characters), which the receiver records in the history. A missing or
malformed one is ignored. Every response from a receiver to an allowed source
address, including an error, carries `X-Lanxfer-Id` with the receiver's own
device ID, unless it has none. A `403` for a disallowed source address
carries none. Older versions neither send nor read either header.

Because it is plain HTTP, you can test a receiver with curl:

    curl -T file.bin http://192.168.1.20:8425/files/file.bin

### Folders

    PUT /dirs/<url-encoded-folder-name>
    Content-Length: <size of the tar stream>
    Content-Type: application/x-tar
    X-Lanxfer-From: mac1

    <tar stream>

The body is an uncompressed tar archive of the folder's contents, with paths
relative to the folder (`sub/a.txt`; a leading `./` is accepted). Only
regular files and folders are allowed. Content-Length is required so that a
transfer cut short is never mistaken for a complete one. Responses are the
same as for files; `400` also covers a rejected entry. A tar file made with
the `tar` command works:

    tar -cf trip.tar -C trip . && curl -T trip.tar http://192.168.1.20:8425/dirs/trip

On macOS, prefix the `tar` command with `COPYFILE_DISABLE=1` so it does not
add `._*` metadata files.

### Text messages

    POST /messages
    Content-Type: text/plain; charset=utf-8
    X-Lanxfer-From: mac1

    <UTF-8 text, at most 1 MiB>

Responses: `204` delivered, `400` empty or not UTF-8, `403` disallowed source
address, `413` too large, `500` could not be stored or recorded in the
history. A missing or invalid `X-Lanxfer-From` is ignored and only the IP
address is shown.

    curl --data-binary 'hello' -H 'X-Lanxfer-From: curl' http://192.168.1.20:8425/messages

### Discovery

One JSON object per UDP datagram on port 8425. A querier broadcasts

    {"lanxfer":1,"type":"query"}

to the directed broadcast address of each attached private network, and every
running receiver answers the sender directly with

    {"lanxfer":1,"type":"reply","name":"mac2","port":8425,"os":"darwin","id":"0123456789abcdef0123456789abcdef"}

The receiver's IP is taken from the reply's source address. `id` is the
device ID and is optional (older versions send none). Datagrams that are not
valid lanxfer packets are ignored. You can query by hand:

    echo '{"lanxfer":1,"type":"query"}' | nc -u -w1 192.168.1.20 8425

Receivers also keep a list of each other. At startup and every 60 seconds a
receiver broadcasts an `announce`, which has the fields of a `reply`, and a
query that describes itself by adding `name`, `port`, `os`, and `id`. A
receiver that gets either one remembers the sender; the query is answered with
a `reply` as usual. Every 30 seconds a receiver also sends the describing
query directly to each peer it remembers. A peer that has not been heard from
for 90 seconds is marked offline (checked every 30 seconds, so after 90 to 120
seconds). Older versions ignore the added fields and drop `announce`, so the
protocol version stays `1`.

### Local API

The receiver also serves two endpoints for programs on the same machine; the
CLI and a GUI use them. They answer only requests from a loopback address
with a `Host` of `localhost`, `127.0.0.1`, or `[::1]`, and reply `403` to
anything else. Requests with an `Origin` header are rejected as everywhere.

    GET /local/peers

Returns the peer list as a JSON array sorted by name; `id` and `os` are left
out when unknown:

    [{"id":"0123456789abcdef0123456789abcdef","name":"mac2","os":"darwin","addr":"192.168.1.20:8425","last_seen":"2026-10-20T21:40:12+09:00","online":true}]

The CLI uses the list only if the response's `X-Lanxfer-Id` is its own device
ID, and ignores entries with a disallowed address or an invalid name.

    POST /local/history
    Content-Type: application/json
    X-Lanxfer-From-Id: <this machine's device ID>

    <one history entry>

Appends a sent item reported by a CLI process, in the format of a line of
`history.jsonl`, so that the receiver stays the only process writing the file.
Only `"dir":"out"` entries are accepted. `X-Lanxfer-From-Id` must match the
receiver's own device ID, or the reply is `409` and nothing is recorded; this
keeps one user's CLI out of another user's receiver on a shared machine. (The
ID is also broadcast on the LAN, so this is not protection against a
deliberate attacker.) Responses: `204` recorded, `400` invalid entry,
`409` not this device's receiver, `413` too large, `415` wrong content type,
`500` could not be recorded.

## Roadmap

1. ~~TCP/HTTP file transfer to an IP address~~
2. ~~Peer discovery on the LAN (`lanxfer peers`)~~
3. ~~Send by peer name (`lanxfer send mac2 file.zip`)~~
4. ~~Send short text between machines (`lanxfer say mac2 "hello"`)~~
5. ~~Progress display and cancellation~~
6. ~~Directory transfer~~
7. GUI with drag and drop
8. Device pairing and TLS

## License

[MIT](LICENSE)
