package lsp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"unicode/utf8"
)

// message is a JSON-RPC 2.0 request, response, or notification.
type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return e.Message }

const (
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternal       = -32603
	codeRequestFailed  = -32803
)

// readMessage reads one message framed by a Content-Length header.
func readMessage(r *bufio.Reader) (*message, error) {
	length := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if v, ok := strings.CutPrefix(line, "Content-Length:"); ok {
			length, err = strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				return nil, fmt.Errorf("bad Content-Length %q", v)
			}
		}
	}
	if length < 0 {
		return nil, fmt.Errorf("message without Content-Length")
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	var m message
	if err := json.Unmarshal(buf, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// writeMessage writes v framed by a Content-Length header.
func writeMessage(w io.Writer, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "Content-Length: %d\r\n\r\n%s", len(data), data)
	return err
}

// ---------------------------------------------------------------------------
// LSP structures (the subset the server uses)

type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

type Location struct {
	URI   string `json:"uri"`
	Range Range  `json:"range"`
}

type TextEdit struct {
	Range   Range  `json:"range"`
	NewText string `json:"newText"`
}

type Diagnostic struct {
	Range              Range                          `json:"range"`
	Severity           int                            `json:"severity"`
	Code               string                         `json:"code,omitempty"`
	Source             string                         `json:"source"`
	Message            string                         `json:"message"`
	RelatedInformation []DiagnosticRelatedInformation `json:"relatedInformation,omitempty"`
	Data               *diagnosticData                `json:"data,omitempty"`
}

type DiagnosticRelatedInformation struct {
	Location Location `json:"location"`
	Message  string   `json:"message"`
}

// diagnosticData carries a diagnostic's quick fixes to codeAction.
type diagnosticData struct {
	Fixes []fix `json:"fixes"`
}

type fix struct {
	Title string     `json:"title"`
	Edits []TextEdit `json:"edits"`
}

type textDocumentPositionParams struct {
	TextDocument struct {
		URI string `json:"uri"`
	} `json:"textDocument"`
	Position Position `json:"position"`
}

// ---------------------------------------------------------------------------
// URIs and positions

// uriToPath converts a file:// URI to a file path.
func uriToPath(uri string) string {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" {
		return uri
	}
	p := u.Path
	if runtime.GOOS == "windows" {
		p = strings.TrimPrefix(p, "/")
	}
	return filepath.FromSlash(p)
}

// pathToURI converts a file path to a file:// URI.
func pathToURI(path string) string {
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return (&url.URL{Scheme: "file", Path: p}).String()
}

// lineIndex converts between byte offsets and LSP positions (UTF-16 columns).
type lineIndex struct {
	text  []byte
	lines []int // byte offset of each line start
}

func newLineIndex(text []byte) *lineIndex {
	idx := &lineIndex{text: text, lines: []int{0}}
	for i, b := range text {
		if b == '\n' {
			idx.lines = append(idx.lines, i+1)
		}
	}
	return idx
}

// position returns the LSP position of byte offset off.
func (x *lineIndex) position(off int) Position {
	off = max(0, min(off, len(x.text)))
	line := 0
	lo, hi := 0, len(x.lines)-1
	for lo <= hi {
		mid := (lo + hi) / 2
		if x.lines[mid] <= off {
			line, lo = mid, mid+1
		} else {
			hi = mid - 1
		}
	}
	col := 0
	for i := x.lines[line]; i < off; {
		r, size := utf8.DecodeRune(x.text[i:])
		i += size
		col += utf16Len(r)
	}
	return Position{Line: line, Character: col}
}

// offset returns the byte offset of an LSP position.
func (x *lineIndex) offset(p Position) int {
	if p.Line < 0 {
		return 0
	}
	if p.Line >= len(x.lines) {
		return len(x.text)
	}
	i := x.lines[p.Line]
	for col := 0; col < p.Character && i < len(x.text) && x.text[i] != '\n'; {
		r, size := utf8.DecodeRune(x.text[i:])
		i += size
		col += utf16Len(r)
	}
	return i
}

func (x *lineIndex) rangeOf(start, end int) Range {
	return Range{Start: x.position(start), End: x.position(max(start, end))}
}

func utf16Len(r rune) int {
	if r >= 0x10000 {
		return 2
	}
	return 1
}
