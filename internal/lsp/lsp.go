// Package lsp implements a minimal Language Server Protocol (LSP) client
// over stdio (JSON-RPC 2.0, Content-Length framed). It feeds instant
// diagnostics back to the agent after it edits source files — Phase 3 of
// the Qwen-code steal plan (stealing.md).
//
// Design notes:
//   - Best-effort: if a language server binary is missing, Diagnostics simply
//     returns nil — no hard dependency, no crash, no user-visible latency.
//   - One server process per language, started lazily on the first edit.
//   - Servers are killed via KillAll (called from the TUI on Ctrl+C/shutdown),
//     mirroring the tools.KillRunningTools pattern.
package lsp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Diagnostic is a single finding reported by the language server.
type Diagnostic struct {
	Severity int    `json:"severity"` // 1=Error 2=Warning 3=Information 4=Hint
	Message  string `json:"message"`
	Line     int    `json:"line"` // 0-based
	Col      int    `json:"col"`  // 0-based
	Source   string `json:"source"`
}

// SeverityText returns a short uppercase label for the diagnostic severity.
func (d Diagnostic) SeverityText() string {
	switch d.Severity {
	case 1:
		return "ERR"
	case 2:
		return "WARN"
	case 3:
		return "INFO"
	default:
		return "HINT"
	}
}

// TextEdit is a single file edit produced by a rename (A6). Positions are
// 0-based LSP positions (line + UTF-16 code-unit column).
type TextEdit struct {
	Path    string // absolute file path
	Line    int    // 0-based start line
	Col     int    // 0-based start column
	EndLine int    // 0-based end line
	EndCol  int    // 0-based end column
	NewText string
}

// applyEditHook receives workspace/applyEdit edits from any live server. The
// tools layer installs it (lsp.SetApplyEditHandler) so renames are applied
// through the same backup+lock path as edit/write.
var applyEditHook func([]TextEdit) error

// SetApplyEditHandler installs the handler invoked when a server pushes
// workspace/applyEdit. Called by the tools package at init (A6).
func SetApplyEditHandler(h func([]TextEdit) error) {
	applyEditHook = h
}

// Config controls the LSP integration.
type Config struct {
	Enabled bool
	// Servers maps a language key (go/python/typescript) to the server binary
	// name, e.g. "go": "gopls". Missing binaries are silently skipped.
	Servers map[string]string
}

// DefaultConfig returns the recommended LSP configuration.
func DefaultConfig() Config {
	return Config{
		Enabled: true,
		Servers: map[string]string{
			"go":         "gopls",
			"python":     "pyright-langserver",
			"typescript": "typescript-language-server",
		},
	}
}

// Timeouts and caps for the best-effort client.
const (
	initializeTimeout = 20 * time.Second       // first-ever gopls init can be slow
	replyTimeout      = 30 * time.Second       // generic request timeout
	diagnosticsWait   = 400 * time.Millisecond // grace period for publishDiagnostics
	maxDiagnostics    = 20                     // cap appended diagnostics per file
)

// rpcResponse is a JSON-RPC reply (or error) matched to a request id.
type rpcResponse struct {
	result json.RawMessage
	err    error
}

// Server wraps a single LSP server subprocess (one per language).
type Server struct {
	lang string
	bin  string

	cmd *exec.Cmd
	in  io.WriteCloser
	out *bufio.Reader

	mu          sync.Mutex // guards seq, pending, diagnostics, opened
	writeMu     sync.Mutex // serializes writes to stdin
	seq         int
	pending     map[int]chan rpcResponse
	diagnostics map[string][]Diagnostic // absolute path -> latest diagnostics
	opened      map[string]int          // absolute path -> didOpen version
	closed      bool
}

// readLoop consumes framed JSON-RPC messages from the server until the
// process exits or the pipe breaks.
func (s *Server) readLoop() {
	for {
		msg, err := readMessage(s.out)
		if err != nil {
			return
		}
		var base struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(msg, &base); err != nil {
			continue
		}

		if base.Method != "" && base.ID != nil {
			// Server-initiated REQUEST (e.g. workspace/applyEdit): a message
			// with both a method and an id is a request the client must
			// answer — NOT a response to one of our requests. (Before A6 the
			// read loop only saw notifications here; rename added requests.)
			switch base.Method {
			case "workspace/applyEdit":
				s.handleApplyEdit(base.ID, base.Params)
			default:
				s.respond(base.ID, struct{}{}) // ack unknown requests so the server never blocks
			}
			continue
		}

		if base.ID != nil {
			// Response to one of our requests.
			var id int
			if err := json.Unmarshal(base.ID, &id); err != nil {
				continue
			}
			s.mu.Lock()
			ch, ok := s.pending[id]
			delete(s.pending, id)
			s.mu.Unlock()
			if ok {
				if base.Error != nil {
					ch <- rpcResponse{err: fmt.Errorf("lsp %s: %s", base.Error.Message, base.Error.Message)}
				} else {
					ch <- rpcResponse{result: base.Result}
				}
			}
			continue
		}

		// Server-initiated notification.
		switch base.Method {
		case "textDocument/publishDiagnostics":
			s.handleDiagnostics(base.Params)
		}
	}
}

// send writes a JSON-RPC message. If wantReply is true it registers a pending
// id and blocks until the response arrives (or times out).
func (s *Server) send(method string, params interface{}, wantReply bool) (json.RawMessage, error) {
	s.mu.Lock()
	s.seq++
	id := s.seq
	s.mu.Unlock()

	payload, err := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		return nil, err
	}
	if !wantReply {
		// Notifications must not carry an id.
		payload, err = json.Marshal(map[string]interface{}{
			"jsonrpc": "2.0",
			"method":  method,
			"params":  params,
		})
		if err != nil {
			return nil, err
		}
	}

	var ch chan rpcResponse
	if wantReply {
		ch = make(chan rpcResponse, 1)
		s.mu.Lock()
		s.pending[id] = ch
		s.mu.Unlock()
	}

	s.writeMu.Lock()
	_, werr := fmt.Fprintf(s.in, "Content-Length: %d\r\n\r\n", len(payload))
	if werr == nil {
		_, werr = s.in.Write(payload)
	}
	s.writeMu.Unlock()
	if werr != nil {
		if wantReply {
			s.mu.Lock()
			delete(s.pending, id)
			s.mu.Unlock()
		}
		return nil, werr
	}

	if !wantReply {
		return nil, nil
	}

	timeout := replyTimeout
	if method == "initialize" {
		timeout = initializeTimeout
	}
	select {
	case resp := <-ch:
		return resp.result, resp.err
	case <-time.After(timeout):
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return nil, fmt.Errorf("lsp: timeout waiting for %q", method)
	}
}

// handleDiagnostics stores publishDiagnostics notifications per file path.
func (s *Server) handleDiagnostics(params json.RawMessage) {
	var p struct {
		URI         string `json:"uri"`
		Diagnostics []struct {
			Severity int    `json:"severity"`
			Message  string `json:"message"`
			Source   string `json:"source"`
			Range    struct {
				Start struct {
					Line      int `json:"line"`
					Character int `json:"character"`
				} `json:"start"`
			} `json:"range"`
		} `json:"diagnostics"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return
	}
	path := uriToPath(p.URI)
	if path == "" {
		return
	}
	diags := make([]Diagnostic, 0, len(p.Diagnostics))
	for _, d := range p.Diagnostics {
		diags = append(diags, Diagnostic{
			Severity: d.Severity,
			Message:  d.Message,
			Line:     d.Range.Start.Line,
			Col:      d.Range.Start.Character,
			Source:   d.Source,
		})
	}
	s.mu.Lock()
	s.diagnostics[path] = diags
	s.mu.Unlock()
}

// respond sends a JSON-RPC response to a server-initiated request (e.g.
// workspace/applyEdit). Without an answer the server blocks forever waiting
// for our ack — rename requests would hang. Writes are serialized with send
// via writeMu.
func (s *Server) respond(id json.RawMessage, result interface{}) {
	if len(id) == 0 {
		return
	}
	payload, err := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(id),
		"result":  result,
	})
	if err != nil {
		return
	}
	s.writeMu.Lock()
	_, _ = fmt.Fprintf(s.in, "Content-Length: %d\r\n\r\n", len(payload))
	_, _ = s.in.Write(payload)
	s.writeMu.Unlock()
}

// handleApplyEdit converts a server-initiated workspace/applyEdit request
// into TextEdits and forwards them to the tools-layer hook (installed via
// SetApplyEditHandler) so they land through the same backup + per-file-lock
// path as edit/write. Always answers the request so the server never blocks.
func (s *Server) handleApplyEdit(id json.RawMessage, params json.RawMessage) {
	edits, err := parseApplyEdit(params)
	if err != nil {
		s.respond(id, map[string]interface{}{"applied": false, "failureReason": err.Error()})
		return
	}
	if applyEditHook == nil {
		s.respond(id, map[string]interface{}{"applied": false, "failureReason": "no apply-edit handler installed"})
		return
	}
	if err := applyEditHook(edits); err != nil {
		s.respond(id, map[string]interface{}{"applied": false, "failureReason": err.Error()})
		return
	}
	s.respond(id, map[string]interface{}{"applied": true})
}

// parseApplyEdit extracts the edit payload from workspace/applyEdit params
// into []TextEdit. Handles both `edit.changes` (legacy) and
// `edit.documentChanges` (modern) — gopls ≥0.23 pushes the latter.
func parseApplyEdit(params json.RawMessage) ([]TextEdit, error) {
	var p struct {
		Edit json.RawMessage `json:"edit"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("bad workspace/applyEdit params: %w", err)
	}
	if len(p.Edit) == 0 {
		return nil, fmt.Errorf("bad workspace/applyEdit params: missing edit")
	}
	return parseWorkspaceEdits(p.Edit)
}

// didOpenOrChange sends didOpen on first sight of a file, didChange afterwards
// (full-text sync, version 1+).
func (s *Server) didOpenOrChange(path, content string) {
	s.mu.Lock()
	version := s.opened[path] + 1
	s.opened[path] = version
	s.mu.Unlock()

	uri := pathToURI(path)
	_, languageID := langForPath(path)
	if version == 1 {
		_, _ = s.send("textDocument/didOpen", map[string]interface{}{
			"textDocument": map[string]interface{}{
				"uri":        uri,
				"languageId": languageID,
				"version":    version,
				"text":       content,
			},
		}, false)
		return
	}
	_, _ = s.send("textDocument/didChange", map[string]interface{}{
		"textDocument": map[string]interface{}{"uri": uri, "version": version},
		"contentChanges": []map[string]interface{}{
			{"text": content}, // full-sync (textDocumentSync kind 1)
		},
	}, false)
}

// getDiagnostics returns the latest diagnostics stored for path.
func (s *Server) getDiagnostics(path string) []Diagnostic {
	abs, _ := filepath.Abs(path)
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Diagnostic, 0, len(s.diagnostics[abs]))
	out = append(out, s.diagnostics[abs]...)
	return out
}

// rename performs the textDocument/rename request and flattens the result's
// per-URI edits into TextEdits. Handles both legacy `changes` (map URI →
// edits) and modern `documentChanges` (list of textDocument + edits) forms —
// gopls ≥0.23 replies with documentChanges by default.
func (s *Server) rename(path string, line, col int, newName string) ([]TextEdit, error) {
	uri := pathToURI(path)
	res, err := s.send("textDocument/rename", map[string]interface{}{
		"textDocument": map[string]interface{}{"uri": uri},
		"position":     map[string]interface{}{"line": line, "character": col},
		"newName":      newName,
	}, true)
	if err != nil {
		return nil, err
	}
	return parseWorkspaceEdits(res)
}

// parseWorkspaceEdits converts either a rename result or applyEdit edit
// payload into []TextEdit. Both share the same shape:
//
//	changes: {uri: [{range, newText}]}              // legacy form
//	documentChanges: [{textDocument:{uri}, edits:[{range,newText}]}]  // modern form
func parseWorkspaceEdits(res json.RawMessage) ([]TextEdit, error) {
	var legacy struct {
		Changes map[string][]struct {
			Range struct {
				Start struct {
					Line      int `json:"line"`
					Character int `json:"character"`
				} `json:"start"`
				End struct {
					Line      int `json:"line"`
					Character int `json:"character"`
				} `json:"end"`
			} `json:"range"`
			NewText string `json:"newText"`
		} `json:"changes"`
	}
	if err := json.Unmarshal(res, &legacy); err != nil {
		return nil, fmt.Errorf("parse workspace edits: %w", err)
	}
	if len(legacy.Changes) > 0 {
		return flattenChanges(legacy.Changes), nil
	}
	var modern struct {
		DocumentChanges []struct {
			TextDocument struct {
				URI string `json:"uri"`
			} `json:"textDocument"`
			Edits []struct {
				Range struct {
					Start struct {
						Line      int `json:"line"`
						Character int `json:"character"`
					} `json:"start"`
					End struct {
						Line      int `json:"line"`
						Character int `json:"character"`
					} `json:"end"`
				} `json:"range"`
				NewText string `json:"newText"`
			} `json:"edits"`
		} `json:"documentChanges"`
	}
	if err := json.Unmarshal(res, &modern); err != nil {
		return nil, fmt.Errorf("parse documentChanges: %w", err)
	}
	var edits []TextEdit
	for _, dc := range modern.DocumentChanges {
		p := uriToPath(dc.TextDocument.URI)
		if p == "" {
			continue
		}
		for _, e := range dc.Edits {
			edits = append(edits, TextEdit{
				Path:    p,
				Line:    e.Range.Start.Line,
				Col:     e.Range.Start.Character,
				EndLine: e.Range.End.Line,
				EndCol:  e.Range.End.Character,
				NewText: e.NewText,
			})
		}
	}
	return edits, nil
}

// flattenChanges converts a legacy {uri: [edits]} map into []TextEdit.
func flattenChanges(changes map[string][]struct {
	Range struct {
		Start struct {
			Line      int `json:"line"`
			Character int `json:"character"`
		} `json:"start"`
		End struct {
			Line      int `json:"line"`
			Character int `json:"character"`
		} `json:"end"`
	} `json:"range"`
	NewText string `json:"newText"`
}) []TextEdit {
	var edits []TextEdit
	for uri, list := range changes {
		p := uriToPath(uri)
		if p == "" {
			continue
		}
		for _, c := range list {
			edits = append(edits, TextEdit{
				Path:    p,
				Line:    c.Range.Start.Line,
				Col:     c.Range.Start.Character,
				EndLine: c.Range.End.Line,
				EndCol:  c.Range.End.Character,
				NewText: c.NewText,
			})
		}
	}
	return edits
}

// stop gracefully shuts the server down (best-effort, 2s cap) then kills it.
func (s *Server) stop() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()

	done := make(chan struct{})
	go func() {
		_, _ = s.send("shutdown", struct{}{}, true)
		_, _ = s.send("exit", struct{}{}, false)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
		return s.cmd.Wait()
	}
	return nil
}

// Manager owns the per-language server processes.
type Manager struct {
	enabled bool
	servers map[string]string // lang -> binary
	env     []string          // extra env for spawned servers (tests)
	dir     string            // working dir for spawned servers (tests; empty = inherit)

	mu     sync.Mutex
	active map[string]*Server // lang -> server
}

// NewManager creates a Manager. When cfg.Enabled is false every call is a no-op.
func NewManager(cfg Config) *Manager {
	servers := make(map[string]string, len(cfg.Servers))
	for k, v := range cfg.Servers {
		servers[k] = v
	}
	return &Manager{
		enabled: cfg.Enabled,
		servers: servers,
		active:  make(map[string]*Server),
	}
}

// Enabled reports whether the manager will actually run servers.
func (m *Manager) Enabled() bool { return m != nil && m.enabled }

// Diagnostics returns the current diagnostics for path, after (re)notifying
// the language server of the file's content. Best-effort: returns nil when
// disabled, unsupported, server missing, or server failed to start.
func (m *Manager) Diagnostics(path, content string) []Diagnostic {
	if !m.Enabled() {
		return nil
	}
	lang, _ := langForPath(path)
	if lang == "" {
		return nil
	}
	srv := m.serverFor(lang)
	if srv == nil {
		return nil
	}
	srv.didOpenOrChange(path, content)
	// Give the server a brief moment to publish diagnostics before we read.
	time.Sleep(diagnosticsWait)
	return srv.getDiagnostics(path)
}

// Rename requests a symbol rename at (line, col) [0-based] in path and
// returns the resulting TextEdits (usually spanning several files).
// Best-effort: nil when disabled, unsupported extension, server missing, or
// the server rejects the rename (symbol not found, no rename support).
func (m *Manager) Rename(path string, line, col int, newName string) []TextEdit {
	if !m.Enabled() {
		return nil
	}
	lang, _ := langForPath(path)
	if lang == "" {
		return nil
	}
	srv := m.serverFor(lang)
	if srv == nil {
		return nil
	}
	edits, err := srv.rename(path, line, col, newName)
	if err != nil {
		log.Printf("lsp: rename %s:%d:%d -> %s: %v", path, line, col, newName, err)
		return nil
	}
	return edits
}

// serverFor lazily starts the server for lang; nil when unavailable.
func (m *Manager) serverFor(lang string) *Server {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.active[lang]; ok {
		return s
	}
	bin, ok := m.servers[lang]
	if !ok {
		return nil
	}
	if _, err := exec.LookPath(bin); err != nil {
		return nil // silent skip — no hard dependency
	}
	s, err := startServerWithEnv(lang, bin, m.env, m.dir)
	if err != nil {
		log.Printf("lsp: failed to start %s: %v", bin, err)
		return nil
	}
	m.active[lang] = s
	return s
}

// KillAll terminates every running server (called on TUI shutdown / Ctrl+C).
func (m *Manager) KillAll() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for lang, s := range m.active {
		_ = s.stop()
		delete(m.active, lang)
	}
}

// Supports reports whether path has a configured language server.
func Supports(path string) bool {
	lang, _ := langForPath(path)
	return lang != ""
}

// langForPath maps a file extension to (language key, LSP languageId).
func langForPath(path string) (string, string) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "go", "go"
	case ".py":
		return "python", "python"
	case ".ts", ".mts", ".cts":
		return "typescript", "typescript"
	case ".tsx":
		return "typescript", "typescriptreact"
	case ".js", ".jsx", ".mjs", ".cjs":
		return "typescript", "javascript" // TS server also serves JS
	default:
		return "", ""
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Package-level singleton (house style: mirrors tools.DefaultRegistry and the
// tools.KillRunningTools pattern, so the agent and TUI need no plumbing).
// ────────────────────────────────────────────────────────────────────────────

var (
	defaultMu  sync.Mutex
	defaultMgr *Manager
)

// Configure (re)places the global LSP manager. Any previous manager's servers
// are killed first.
func Configure(cfg Config) {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	if defaultMgr != nil {
		defaultMgr.KillAll()
	}
	defaultMgr = NewManager(cfg)
}

// Diagnostics returns instant diagnostics for path via the global manager.
func Diagnostics(path, content string) []Diagnostic {
	defaultMu.Lock()
	m := defaultMgr
	defaultMu.Unlock()
	if m == nil {
		return nil
	}
	return m.Diagnostics(path, content)
}

// Rename requests a symbol rename at (line, col) via the global manager.
// Mirrors Diagnostics; returns nil when no manager is configured.
func Rename(path string, line, col int, newName string) []TextEdit {
	defaultMu.Lock()
	m := defaultMgr
	defaultMu.Unlock()
	if m == nil {
		return nil
	}
	return m.Rename(path, line, col, newName)
}

// KillAll stops all language servers tracked by the global manager.
func KillAll() {
	defaultMu.Lock()
	m := defaultMgr
	defaultMu.Unlock()
	if m != nil {
		m.KillAll()
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Framing + URI helpers
// ────────────────────────────────────────────────────────────────────────────

// readMessage reads one Content-Length framed JSON-RPC message.
func readMessage(r *bufio.Reader) ([]byte, error) {
	contentLength := 0
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		key, value, found := strings.Cut(line, ":")
		if found && strings.EqualFold(strings.TrimSpace(key), "Content-Length") {
			n, err := strconv.Atoi(strings.TrimSpace(value))
			if err == nil {
				contentLength = n
			}
		}
	}
	if contentLength <= 0 {
		return nil, fmt.Errorf("lsp: invalid content length %d", contentLength)
	}
	buf := make([]byte, contentLength)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// writeMessage writes a Content-Length framed JSON-RPC message.
func writeMessage(w io.Writer, payload []byte) error {
	if _, err := fmt.Fprintf(w, "Content-Length: %d\r\n\r\n", len(payload)); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

// pathToURI converts an absolute filesystem path to a file:// URI.
func pathToURI(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}
	return u.String()
}

// uriToPath converts a file:// URI back to a filesystem path.
func uriToPath(uri string) string {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" {
		return ""
	}
	p := filepath.FromSlash(u.Path)
	if u.Host != "" && u.Host != "localhost" {
		p = "//" + u.Host + p
	}
	// Windows: strip the leading slash from "/C:/..."
	if runtime.GOOS == "windows" && len(p) >= 3 && p[0] == '/' && p[2] == ':' {
		p = p[1:]
	}
	return p
}

// startServerWithEnv is startServer with an optional extra environment and
// working directory (used by tests to point at a fake server binary or to
// spawn gopls inside a temp module so its workspace root matches the files).
func startServerWithEnv(lang, bin string, env []string, dir string) (*Server, error) {
	cmd := exec.Command(bin)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	if dir != "" {
		cmd.Dir = dir
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = io.Discard // keep the TUI clean; server logs are noise
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	s := &Server{
		lang:        lang,
		bin:         bin,
		cmd:         cmd,
		in:          stdin,
		out:         bufio.NewReader(stdout),
		pending:     make(map[int]chan rpcResponse),
		diagnostics: make(map[string][]Diagnostic),
		opened:      make(map[string]int),
	}
	go s.readLoop()

	// LSP handshake: initialize (request) -> initialized (notification).
	if _, err := s.send("initialize", map[string]interface{}{
		"processId":    os.Getpid(),
		"clientInfo":   map[string]interface{}{"name": "eling", "version": "0.4.4"},
		"capabilities": map[string]interface{}{},
	}, true); err != nil {
		_ = s.stop()
		return nil, fmt.Errorf("initialize: %w", err)
	}
	_, _ = s.send("initialized", map[string]interface{}{}, false)
	return s, nil
}
