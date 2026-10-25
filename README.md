# calibruh

A small web UI and OPDS server for Calibre libraries. Serves several libraries
at once and lets you upload books from the browser.

- Reads `metadata.db` directly, read-only.
- Adds books by running `calibredb add`. Calibre's schema has triggers that call
  SQL functions only Calibre defines, so writing to the database ourselves would
  break it.
- Single static binary. Templates and Tailwind (`web/static/tailwind.js`, the
  v4 browser build) are embedded, so nothing loads from a CDN.

## Run

```sh
cp calibruh.example.json calibruh.json   # edit libraries, auth
go build -o calibruh ./cmd/calibruh
CALIBRUH_PASSWORD=... ./calibruh -config calibruh.json
```

## Libraries

Point `libraries_dir` at a folder. Every subfolder that contains a
`metadata.db` is served as a library named after the folder. calibruh rescans
every `rescan_seconds`, so adding or removing a library folder needs no restart.
Hidden folders are skipped, and so are folders whose `metadata.db` won't open
(they're retried on the next scan).

Libraries outside that folder can be listed in `libraries`. They come first in
the switcher, and they win if a discovered folder would get the same URL.

Each library is served at `/lib/<slug>/`, where the slug comes from its name
("My Books" → `my-books`).

Uploads need `calibredb` on the machine, and **no other calibre program may be
running** (the desktop app or `calibre-server`), because calibredb refuses to
write while another one is. Browsing and OPDS work regardless.

## OPDS (KOReader)

Add a catalog with the URL `http://<host>:8080/opds`, plus the username and
password if auth is set. With one library, that URL is the library itself.
With several, it lists them.

Feeds: newest, all titles, authors, series, tags, and search.

## Config

| key                | default     |                                                   |
|--------------------|-------------|---------------------------------------------------|
| `addr`             | `:8080`     | listen address                                    |
| `calibredb`        | `calibredb` | calibredb binary                                  |
| `allow_duplicates` | `false`     | pass `--duplicates` to calibredb                  |
| `max_upload_mb`    | `500`       | per upload request                                |
| `username`, `password` | none    | HTTP Basic auth on everything; `CALIBRUH_PASSWORD` overrides the password |
| `libraries_dir`    | none        | folder to discover libraries in                   |
| `rescan_seconds`   | `30`        | how often to rescan `libraries_dir`               |
| `libraries`        | none        | extra `{"name", "path"}` libraries; this, `libraries_dir`, or both is required |

## Develop

```sh
go test ./...
```

Tests use a generated Calibre-shaped library (`internal/testlib`) and a fake
calibredb script, so they need neither calibre nor a real library.

## License

MIT, see [LICENSE](LICENSE). The vendored Tailwind build in `web/static/` is
MIT-licensed by Tailwind Labs ([notice](web/static/tailwind.LICENSE)). The
Docker image also contains calibre, which is GPLv3 and available from
<https://calibre-ebook.com>; calibruh only runs it as a separate program.
