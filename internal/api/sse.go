package api

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
)

// readSSE consumes a stream of `data: {json}` frames, each carrying the same
// {events, cursor, terminal} object the poll returns, and hands every frame to
// onPage. It returns nil when a frame said Terminal — the command is over —
// and the read error otherwise, so the caller knows a drop from an ending.
//
// The server emits `: ping` comment lines every 20 seconds on an idle stream;
// they are skipped here, as the SSE spec says comments are. Frames can be
// split across reads — bufio's line reader is what reassembles them — and a
// frame's data always fits one `data:` line, because the server writes it
// with a single Fprintf.
func readSSE(r io.Reader, onPage func(EventsPage)) error {
	br := bufio.NewReaderSize(r, 1<<20)
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return err
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case strings.HasPrefix(line, "data:"):
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			var page EventsPage
			if jerr := json.Unmarshal([]byte(payload), &page); jerr != nil {
				// A frame that does not parse is a stream not worth trusting;
				// the caller falls back to polling from its cursor.
				return jerr
			}
			onPage(page)
			if page.Terminal {
				return nil
			}
		default:
			// Comments (": ping"), blank separators, and any field this client
			// has no use for.
		}
	}
}
