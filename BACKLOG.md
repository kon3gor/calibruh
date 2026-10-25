# Backlog

## MCP support

Expose the libraries to AI assistants over the Model Context Protocol, so you
can ask things like "what Tolkien do I have?" or "add this EPUB to Mom's
library".

- Serve MCP over Streamable HTTP at `/mcp` on the existing server, behind the
  same Basic auth.
- Tools:
  - `list_libraries`
  - `search_books`: query, library, author/series/tag filters, paging
  - `get_book`: full metadata, formats, description
  - `upload_book`: add a file to a library through calibredb. The file
    arrives as base64 or as a URL to fetch, with the same format allowlist and
    size limit as web uploads.
- Reuse `library.Query` and the upload path, so MCP and the web UI behave the
  same.

## Pinky promise

Add a book before you have its file: a placeholder that holds the metadata
and waits for the file to show up.

- Create with `calibredb add --empty` plus title, authors, series, tags and
  ISBN, from a form in the web UI.
- Mark promised books clearly in the grid and on the book page, and list them
  in a "Promised" view.
- On the book page, an upload button attaches the file later with `calibredb
  add_format <id> <file>`. The promise is kept: the book becomes a normal one.
- OPDS: leave promised books out of the feeds, or show them without download
  links. Decide which.
- Open question: how to mark a book as promised. "No formats" is free and
  needs no schema change, but it also catches books whose files were removed
  on purpose. A tag (e.g. `pinky-promise`) is explicit and visible in Calibre
  desktop.
