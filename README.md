# lanxfer

**LAN transfer** — send a file directly from one machine to another on your
home LAN, with no relay server in between.

lanxfer is a small, dependency-free Go CLI. Run `lanxfer recv` on the machine
that should receive, then `lanxfer send <ip> <file>` on the machine that has
the file. That's it.

## Status

This is an MVP (Phase 1 of the roadmap below). It works, but it is
deliberately minimal: single file, IP address required, no peer discovery,
no progress bar, no encryption.

## Usage

Receiver (waits in the foreground; stop with Ctrl-C):

    lanxfer recv [--dir <path>] [--port 8425] [--max-size <bytes>]

Sender:

    lanxfer send [--port 8425] <receiver-ip> <file>

Defaults: port `8425`, save directory `~/lanxfer`, maximum file size 50 GiB.

Example:

    # on 192.168.1.20
    lanxfer recv

    # on another machine
    lanxfer send 192.168.1.20 ~/Downloads/movie.mp4
    sending movie.mp4 -> 192.168.1.20
    done: 4.8 GiB in 41.2s (119.3 MiB/s)

If a file with the same name already exists on the receiver, the new one is
saved as `movie (1).mp4`, `movie (2).mp4`, and so on. Existing files are
never overwritten.

## Security notes

**lanxfer has no authentication and no encryption. Use it only on a LAN you
trust.** Anyone who can reach the receiver's port can upload files to it.

Built-in protections:

- Connections are accepted only from private (RFC 1918 / ULA), link-local,
  and loopback addresses. This is a safety net in case the port is ever
  exposed to the internet, not a substitute for a firewall.
- Filenames are validated; the receiver never writes outside its save
  directory (path traversal, absolute paths, and control characters are
  rejected).
- Existing files are never overwritten; collisions are renamed.
- Files are written to a temporary `.part` file and renamed into place only
  after the full declared size has arrived, so an interrupted transfer never
  leaves a truncated file that looks complete.
- Uploads larger than `--max-size` are rejected before any data is read.
- The save directory is created with mode `0700` and files with `0600`.

Known limitation: if the receiver process is killed (Ctrl-C) in the middle
of a transfer, the in-progress `.lanxfer-*.part` file is left behind. It is
safe to delete.

## Protocol

The receiver is a plain HTTP server. A transfer is a single request:

    PUT /files/<url-encoded-filename>
    Content-Length: <size>
    Content-Type: application/octet-stream

    <raw file bytes>

Responses: `201` (body contains the saved filename), `400` invalid filename,
`403` disallowed source address, `411` missing Content-Length, `413` too
large, `500` storage error.

Because it is plain HTTP, you can test a receiver with curl:

    curl -T file.bin http://192.168.1.20:8425/files/file.bin

## Install

### Homebrew (macOS / Linux)

    brew install capybara-translation/tap/lanxfer

### go install

    go install github.com/capybara-translation/lanxfer@latest

### Pre-built binaries

Download the archive for your platform from the
[Releases page](https://github.com/capybara-translation/lanxfer/releases)
(`tar.gz` for macOS/Linux, `zip` for Windows) and put the `lanxfer` binary
somewhere on your `PATH`. `checksums.txt` on the same page lists the SHA-256
of every archive.

### Build from source

    git clone https://github.com/capybara-translation/lanxfer.git
    cd lanxfer
    go build -ldflags "-s -w -X main.version=$(git describe --tags --always --dirty)" .

Requires the Go version declared in `go.mod`. No third-party dependencies.

### What `lanxfer --version` prints

| How you installed | Output |
|---|---|
| Homebrew or a Releases archive | `lanxfer vX.Y.Z` |
| `go install ...@vX.Y.Z` | `lanxfer vX.Y.Z` |
| `go install ...@latest` from a non-tagged commit | `lanxfer dev` (pseudo versions are intentionally hidden) |
| `go build` with the `ldflags` example above | whatever `git describe` resolves to |
| Plain `go build` without `ldflags` | `lanxfer dev` |

## Roadmap

1. ~~TCP/HTTP file transfer to an IP address~~ (this MVP)
2. Peer discovery on the LAN (`lanxfer peers`)
3. Send by peer name (`lanxfer send mac2 file.zip`)
4. Progress display and cancellation
5. Directory transfer
6. Device pairing and TLS
7. GUI with drag and drop

## License

[MIT](LICENSE)
