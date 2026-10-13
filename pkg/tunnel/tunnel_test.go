package tunnel

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Valentin-Kaiser/backup/pkg/peer"
)

// TestEchoHelper is re-executed as a fake unison server: it answers with what it gets and tells where it runs.
func TestEchoHelper(t *testing.T) {
	if os.Getenv("GO_FAKE_UNISON") != "1" {
		return
	}
	wd, _ := os.Getwd()
	os.Stdout.WriteString("dir=" + wd + " home=" + os.Getenv("HOME") + " unison=" + os.Getenv("UNISON") + " host=" + os.Getenv("UNISONLOCALHOSTNAME") + "\n")
	_, _ = io.Copy(os.Stdout, os.Stdin)
	os.Exit(0)
}

func newServer(t *testing.T, authorize func(job, owner string) error) (*httptest.Server, string) {
	t.Helper()
	t.Setenv("GO_FAKE_UNISON", "1")
	root := t.TempDir()
	s := &Server{
		Binary:    os.Args[0],
		Prefix:    []string{"-test.run=TestEchoHelper", "--"},
		Root:      func() string { return root },
		State:     func() string { return filepath.Join(root, "..", "state") },
		Host:      func() string { return "east" },
		Authorize: authorize,
	}
	mux := http.NewServeMux()
	mux.Handle(Path, s)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, root
}

func TestTunnelRelaysBothWays(t *testing.T) {
	srv, root := newServer(t, func(job, owner string) error {
		if job != "mirror" || owner != "west" {
			return errors.New("denied")
		}
		return nil
	})
	in, inW := io.Pipe()
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- Dial(context.Background(), peer.Endpoint{URL: srv.URL}, "mirror", "west", []string{"-server", "__new-rpc-mode"}, in, &out)
	}()
	payload := strings.Repeat("0123456789", 20000) // more than one frame
	go func() {
		_, _ = inW.Write([]byte(payload))
		_ = inW.Close() // unison ends: the server side has to end as well
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the tunnel did not end")
	}
	got := out.String()
	if !strings.HasSuffix(got, payload) {
		t.Errorf("the payload did not come back, got %d bytes", len(got))
	}
	head, _, _ := strings.Cut(got, "\n")
	for _, want := range []string{"dir=", "home=" + root, "host=east", filepath.Join("state", "_server", "west", "mirror")} {
		if !strings.Contains(head, want) {
			t.Errorf("%q is missing in %q", want, head)
		}
	}
}

func TestTunnelRefusesWhatIsNotAuthorized(t *testing.T) {
	srv, _ := newServer(t, func(job, owner string) error { return errors.New("the job is not reserved for the caller") })
	err := Dial(context.Background(), peer.Endpoint{URL: srv.URL}, "mirror", "west", nil, strings.NewReader(""), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "not reserved") {
		t.Errorf("err = %v", err)
	}
}

func TestTunnelOnlyStartsTheUnisonServer(t *testing.T) {
	srv, _ := newServer(t, func(string, string) error { return nil })
	for _, args := range [][]string{{"sh", "-c", "id"}, {"-server", "-rsh", "x"}} {
		err := Dial(context.Background(), peer.Endpoint{URL: srv.URL}, "mirror", "west", args, strings.NewReader(""), io.Discard)
		if err == nil || !strings.Contains(err.Error(), "400") {
			t.Errorf("%v: err = %v", args, err)
		}
	}
}

func TestTunnelNeedsAPeerThatAnswers(t *testing.T) {
	srv, _ := newServer(t, func(string, string) error { return nil })
	url := srv.URL
	srv.Close()
	err := Dial(context.Background(), peer.Endpoint{URL: url}, "mirror", "west", nil, strings.NewReader(""), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "not reachable") {
		t.Errorf("err = %v", err)
	}
}

func TestSafeName(t *testing.T) {
	if got := safeName("../etc/pass wd"); strings.ContainsAny(got, "/. ") {
		t.Errorf("safeName = %q", got)
	}
}
