# lanxfer

**LAN transfer** — send a file directly from one machine to another on your
home LAN, with no relay server in between.

lanxfer is a small, dependency-free Go CLI. Run `lanxfer recv` on the machine
that should receive, then `lanxfer send <name> <file>` on the machine that has
the file. Receivers are found automatically on the local network.

## Status

Early but usable: single-file transfer, automatic peer discovery, and sending
by name. No progress bar, no directory transfer, and no encryption yet.

## Usage

Receiver (waits in the foreground; stop with Ctrl-C):

    lanxfer recv [--dir <path>] [--port 8425] [--max-size <bytes>] [--name <name>]

See who is receiving on your network:

    lanxfer peers [--port 8425] [--wait 1s]
    NAME    ADDRESS            OS
    mac2    192.168.1.20:8425  darwin
    ubuntu  192.168.1.30:8425  linux

Sender, by name or by IP address:

    lanxfer send [--port 8425] <ip-or-name> <file>

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
- Discovery replies reveal only the receiver's name, port, and OS, and are
  sent only to private, link-local, or loopback addresses.

Discovery uses UDP broadcast on the same port number (8425/udp). If the
receiver has a firewall, allow both TCP and UDP on that port. Names are
self-declared and unauthenticated: anyone on the LAN can announce any name,
so treat a name as a convenience, not as proof of identity.

Known limitation: if the receiver process is killed (Ctrl-C) in the middle
of a transfer, the in-progress `.lanxfer-*.part` file is left behind. It is
safe to delete.

## Troubleshooting

If a machine does not show up in `lanxfer peers`, files sent to it will not
arrive either: the same network path is blocked. Check on that machine:

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

    <raw file bytes>

Responses: `201` (body contains the saved filename), `400` invalid filename,
`403` disallowed source address, `411` missing Content-Length, `413` too
large, `500` storage error.

Because it is plain HTTP, you can test a receiver with curl:

    curl -T file.bin http://192.168.1.20:8425/files/file.bin

### Discovery

One JSON object per UDP datagram on port 8425. A querier broadcasts

    {"lanxfer":1,"type":"query"}

to the directed broadcast address of each attached private network, and every
running receiver answers the sender directly with

    {"lanxfer":1,"type":"reply","name":"mac2","port":8425,"os":"darwin"}

The receiver's IP is taken from the reply's source address. Datagrams that
are not valid lanxfer packets are ignored. You can query by hand:

    echo '{"lanxfer":1,"type":"query"}' | nc -u -w1 192.168.1.20 8425

## Install

### Homebrew (macOS / Linux)

    brew install capybara-translation/tap/lanxfer

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

1. ~~TCP/HTTP file transfer to an IP address~~
2. ~~Peer discovery on the LAN (`lanxfer peers`)~~
3. ~~Send by peer name (`lanxfer send mac2 file.zip`)~~
4. Send short text between machines (`lanxfer say mac2 "hello"`)
5. Progress display and cancellation
6. Directory transfer
7. Device pairing and TLS
8. GUI with drag and drop

## License

[MIT](LICENSE)
