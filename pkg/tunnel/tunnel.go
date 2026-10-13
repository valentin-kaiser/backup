// Package tunnel carries the protocol of unison between two workers over the control API, so unison needs neither an
// sshd nor a port of its own. The worker that syncs (the client) starts unison with this program as its "ssh". The
// program opens a websocket to the API of the other worker, which starts `unison -server` and relays its standard
// input and output. The connection is authenticated like every other call to the API.
package tunnel

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/Valentin-Kaiser/backup/pkg/peer"
	"github.com/gorilla/websocket"
	"github.com/valentin-kaiser/go-core/apperror"
	"github.com/valentin-kaiser/go-core/logging/log"
)

// Path is the URL of the tunnel on the control API.
const Path = "/api/v1/tunnel"

// Headers of the request.
const (
	HeaderJob   = "X-Sync-Job"
	HeaderOwner = "X-Sync-Owner"
	HeaderArgs  = "X-Sync-Args"
)

// Environment variables that tell the client program where to connect, set by the runner for unison.
const (
	EnvURL      = "CONTROL_TUNNEL_URL"
	EnvPassword = "CONTROL_TUNNEL_PASSWORD"
	EnvCA       = "CONTROL_TUNNEL_CA"
	EnvJob      = "CONTROL_TUNNEL_JOB"
	EnvOwner    = "CONTROL_TUNNEL_OWNER"
)

// ClientName is the program name unison runs as its "ssh". The binary is the same as control, started through a link.
const ClientName = "control-tunnel"

// Keep alive: a peer that disappears without closing the connection ends the run instead of hanging it.
const (
	pingEvery = 15 * time.Second
	deadAfter = 50 * time.Second
	maxFrame  = 16 << 20
)

// allowedArgs are the only arguments of unison the server accepts, whatever the client asks for.
var allowedArgs = []string{"-server", "__new-rpc-mode"}

// Server answers tunnel requests.
type Server struct {
	// Binary is the unison executable, "unison" when empty.
	Binary string
	// Prefix is inserted before the arguments of unison (used by tests).
	Prefix []string
	// Root returns the directory that is exported: unison runs there and relative roots are resolved below it.
	Root func() string
	// State returns the directory for the archives of the server side.
	State func() string
	// Host is the name this worker has in the archives, stable across container restarts.
	Host func() string
	// Authorize decides whether the caller may open a tunnel for a job.
	Authorize func(job, owner string) error
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  64 << 10,
	WriteBufferSize: 64 << 10,
	// No browser is involved: the request carries a bearer password, which a page on another origin cannot set.
	CheckOrigin: func(*http.Request) bool { return true },
}

func (s *Server) binary() string {
	if s.Binary != "" {
		return s.Binary
	}
	return "unison"
}

// ServeHTTP opens a tunnel.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	job, owner := r.Header.Get(HeaderJob), r.Header.Get(HeaderOwner)
	if err := s.Authorize(job, owner); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	args := strings.Fields(r.Header.Get(HeaderArgs))
	if len(args) == 0 {
		args = []string{"-server"}
	}
	for _, a := range args {
		if !slices.Contains(allowedArgs, a) {
			http.Error(w, "argument not allowed: "+a, http.StatusBadRequest)
			return
		}
	}
	root := s.Root()
	state := filepath.Join(s.State(), "_server", safeName(owner), safeName(job))
	if err := os.MkdirAll(state, 0o750); err != nil {
		http.Error(w, "the state directory cannot be created", http.StatusInternalServerError)
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return // Upgrade has answered
	}
	defer func() { _ = conn.Close() }()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	cmd := exec.CommandContext(ctx, s.binary(), append(append([]string{}, s.Prefix...), args...)...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "HOME="+root, "UNISON="+state, "UNISONLOCALHOSTNAME="+s.Host())
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second
	in, err := cmd.StdinPipe()
	if err != nil {
		return
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return
	}
	cmd.Stderr = logWriter{job: job}
	if err := cmd.Start(); err != nil {
		log.Error().Err(err).Msg("unison server not started")
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "unison not started"), time.Now().Add(time.Second))
		return
	}
	log.Info().Msgf("tunnel for %s from %s opened", job, owner)
	// The tunnel ends when the process has said everything or the connection broke. A client that is done sending
	// only closes the input of the process, the answer still has to come back.
	done := make(chan struct{}, 2)
	go func() {
		if broken := copyToProcess(conn, in); broken {
			done <- struct{}{}
		}
	}()
	go func() { copyFromProcess(conn, out); done <- struct{}{} }()
	<-done
	cancel()
	_ = cmd.Wait()
	log.Info().Msgf("tunnel for %s from %s closed", job, owner)
}

// eofMessage is what a client sends when it has nothing more to say, the equivalent of closing a pipe.
const eofMessage = "eof"

// copyToProcess feeds the messages of the client to the standard input of the process. It reports whether the
// connection broke, as opposed to the client being done.
func copyToProcess(conn *websocket.Conn, in io.WriteCloser) (broken bool) {
	defer func() { _ = in.Close() }()
	conn.SetReadLimit(maxFrame)
	_ = conn.SetReadDeadline(time.Now().Add(deadAfter))
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(deadAfter)) })
	for {
		typ, data, err := conn.ReadMessage()
		if err != nil {
			return true
		}
		_ = conn.SetReadDeadline(time.Now().Add(deadAfter))
		if typ == websocket.TextMessage && string(data) == eofMessage {
			return false
		}
		if typ != websocket.BinaryMessage {
			continue
		}
		if _, err := in.Write(data); err != nil {
			return true
		}
	}
}

// copyFromProcess sends the output of the process to the client and keeps the connection alive.
func copyFromProcess(conn *websocket.Conn, out io.Reader) {
	stop := make(chan struct{})
	defer close(stop)
	go ping(conn, stop)
	buf := make([]byte, 32<<10)
	for {
		n, err := out.Read(buf)
		if n > 0 {
			if werr := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
			return
		}
	}
}

// ping keeps a connection alive. Control frames may be written while another goroutine writes data.
func ping(conn *websocket.Conn, stop <-chan struct{}) {
	t := time.NewTicker(pingEvery)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			if conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)) != nil {
				return
			}
		}
	}
}

func safeName(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, s)
}

type logWriter struct{ job string }

func (l logWriter) Write(p []byte) (int, error) {
	if m := strings.TrimSpace(string(p)); m != "" {
		log.Debug().Msgf("unison server (%s): %s", l.job, m)
	}
	return len(p), nil
}

// Dial opens a tunnel to the API of a peer and relays in and out through it until one side ends. A nil error means
// the peer ended the tunnel normally.
func Dial(ctx context.Context, p peer.Endpoint, job, owner string, args []string, in io.Reader, out io.Writer) error {
	u, err := url.Parse(p.URL)
	if err != nil {
		return apperror.Wrap(err)
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	default:
		u.Scheme = "ws"
	}
	u.Path = strings.TrimRight(u.Path, "/") + Path
	header := http.Header{}
	header.Set(HeaderJob, job)
	header.Set(HeaderOwner, owner)
	header.Set(HeaderArgs, strings.Join(args, " "))
	if p.Password != "" {
		header.Set("Authorization", "Bearer "+p.Password)
	}
	tlsCfg, err := peer.TLSConfig(p.CAFile)
	if err != nil {
		return err
	}
	d := websocket.Dialer{TLSClientConfig: tlsCfg, HandshakeTimeout: 20 * time.Second, ReadBufferSize: 64 << 10, WriteBufferSize: 64 << 10}
	conn, resp, err := d.DialContext(ctx, u.String(), header)
	if err != nil {
		if resp != nil {
			msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			_ = resp.Body.Close()
			return apperror.NewErrorf("the peer refused the tunnel: %s %s", resp.Status, strings.TrimSpace(string(msg)))
		}
		return apperror.NewError("the peer is not reachable").AddError(err)
	}
	defer func() { _ = conn.Close() }()
	conn.SetReadLimit(maxFrame)
	_ = conn.SetReadDeadline(time.Now().Add(deadAfter))
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(deadAfter)) })
	conn.SetPingHandler(func(m string) error {
		_ = conn.SetReadDeadline(time.Now().Add(deadAfter))
		return conn.WriteControl(websocket.PongMessage, []byte(m), time.Now().Add(5*time.Second))
	})

	stop := make(chan struct{})
	defer close(stop)
	go ping(conn, stop)

	errc := make(chan error, 2)
	go func() { // unison -> peer, this goroutine is the only one that writes data
		buf := make([]byte, 32<<10)
		for {
			n, rerr := in.Read(buf)
			if n > 0 {
				if werr := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
					errc <- werr
					return
				}
			}
			if rerr != nil {
				// unison is done sending: the unison of the peer sees the end of its input and answers one last time,
				// the reading goroutine ends the tunnel when the peer closes it.
				if werr := conn.WriteMessage(websocket.TextMessage, []byte(eofMessage)); werr != nil {
					errc <- werr
				}
				return
			}
		}
	}()
	go func() { // peer -> unison
		for {
			typ, data, rerr := conn.ReadMessage()
			if rerr != nil {
				if websocket.IsCloseError(rerr, websocket.CloseNormalClosure) {
					errc <- nil
				} else {
					errc <- apperror.NewError("the tunnel to the peer broke").AddError(rerr)
				}
				return
			}
			_ = conn.SetReadDeadline(time.Now().Add(deadAfter))
			if typ != websocket.BinaryMessage {
				continue
			}
			if _, werr := out.Write(data); werr != nil {
				errc <- werr
				return
			}
		}
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// RunClient is the program unison starts instead of ssh. The arguments it gets are those of ssh, only the command
// after the host is used. The target comes from the environment. It returns the exit code.
func RunClient(args []string) int {
	p := peer.Endpoint{URL: os.Getenv(EnvURL), Password: os.Getenv(EnvPassword), CAFile: os.Getenv(EnvCA)}
	if p.URL == "" {
		_, _ = os.Stderr.WriteString("control-tunnel: " + EnvURL + " is not set, it only works when started by unison\n")
		return 2
	}
	// The command unison wants to run on the other side follows the host: unison -server __new-rpc-mode.
	var remote []string
	for i, a := range args {
		if filepath.Base(a) == "unison" {
			remote = args[i+1:]
			break
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := Dial(ctx, p, os.Getenv(EnvJob), os.Getenv(EnvOwner), remote, os.Stdin, os.Stdout); err != nil {
		_, _ = os.Stderr.WriteString("control-tunnel: " + err.Error() + "\n")
		return 1
	}
	return 0
}
