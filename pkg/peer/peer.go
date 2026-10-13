// Package peer talks to other workers: the control API for coordination and the sync server for data.
package peer

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"google.golang.org/protobuf/types/known/emptypb"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Valentin-Kaiser/backup/pkg/config"
	pb "github.com/Valentin-Kaiser/backup/pkg/proto/api/v1"
	"github.com/valentin-kaiser/go-core/apperror"
	"github.com/valentin-kaiser/go-core/web/xrpc"
)

// rpcPath is the xrpc endpoint of the control API.
const rpcPath = "/api/v1/rpc"

// Client is the part of the control API that workers call on each other.
type Client interface {
	GetStatus(ctx context.Context, in *pb.GetStatusRequest) (*pb.ControlStatus, error)
	ListRuns(ctx context.Context, in *pb.ListRunsRequest) (*pb.ListRunsResponse, error)
	GetRun(ctx context.Context, in *pb.GetRunRequest) (*pb.Run, error)
	StartJob(ctx context.Context, in *pb.JobRequest) (*pb.Run, error)
	CancelJob(ctx context.Context, in *pb.JobRequest) (*emptypb.Empty, error)
	UnlockJob(ctx context.Context, in *pb.JobRequest) (*emptypb.Empty, error)
	PauseJob(ctx context.Context, in *pb.JobRequest) (*emptypb.Empty, error)
	ResumeJob(ctx context.Context, in *pb.JobRequest) (*emptypb.Empty, error)
	ResyncJob(ctx context.Context, in *pb.JobRequest) (*pb.Run, error)
	UpdateJob(ctx context.Context, in *pb.JobDefinition) (*pb.Job, error)
	DeleteJob(ctx context.Context, in *pb.JobRequest) (*emptypb.Empty, error)
	AcquireSyncLock(ctx context.Context, in *pb.SyncLockRequest) (*pb.SyncLockResponse, error)
	ReleaseSyncLock(ctx context.Context, in *pb.SyncLockRequest) (*emptypb.Empty, error)
	PushJob(ctx context.Context, in *pb.PushJobRequest) (*emptypb.Empty, error)
	DropJob(ctx context.Context, in *pb.PushJobRequest) (*emptypb.Empty, error)
}

// Dialer creates the control API client of a peer. password is the resolved API password.
type Dialer func(p config.Peer, password string) (Client, error)

// TLSConfig trusts the CA file when one is given and the system roots otherwise.
func TLSConfig(caFile string) (*tls.Config, error) {
	if caFile == "" {
		return nil, nil
	}
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, apperror.NewErrorf("CA file %s is not readable", caFile).AddError(err)
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pem) {
		return nil, apperror.NewErrorf("CA file %s holds no certificate", caFile)
	}
	return &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, nil
}

func httpClient(caFile string, timeout time.Duration) (*http.Client, *tls.Config, error) {
	cfg, err := TLSConfig(caFile)
	if err != nil {
		return nil, nil, err
	}
	tr := &http.Transport{TLSClientConfig: cfg}
	return &http.Client{Transport: tr, Timeout: timeout}, cfg, nil
}

// Dial connects to the control API of a peer.
func Dial(p config.Peer, password string) (Client, error) {
	if p.APIURL == "" {
		return nil, apperror.NewErrorf("peer %s has no API URL", p.Name)
	}
	hc, cfg, err := httpClient(p.APICAFile, 2*time.Minute)
	if err != nil {
		return nil, err
	}
	h := http.Header{}
	if password != "" {
		h.Set("Authorization", "Bearer "+password)
	}
	opts := []xrpc.ClientOption{xrpc.WithHTTPClient(hc), xrpc.WithHeader(h)}
	if cfg != nil {
		opts = append(opts, xrpc.WithTLSConfig(cfg))
	}
	c, err := pb.NewControlClient(strings.TrimRight(p.APIURL, "/")+rpcPath, opts...)
	if err != nil {
		return nil, apperror.Wrap(err)
	}
	return c, nil
}

// CheckData verifies that the sync server of a peer answers with the given credentials.
func CheckData(ctx context.Context, p config.Peer, password string) error {
	hc, _, err := httpClient(p.CAFile, 15*time.Second)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "PROPFIND", strings.TrimRight(p.URL, "/")+"/", nil)
	if err != nil {
		return apperror.Wrap(err)
	}
	req.Header.Set("Depth", "0")
	req.SetBasicAuth(p.Username, password)
	resp, err := hc.Do(req)
	if err != nil {
		return apperror.NewError("the sync server is not reachable").AddError(err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return apperror.NewError("the sync server refused the credentials")
	case resp.StatusCode >= 300:
		return apperror.NewErrorf("the sync server answered %s", resp.Status)
	}
	return nil
}

// Endpoint is the address and credentials of the control API of a peer.
type Endpoint struct {
	URL      string
	Password string
	CAFile   string
}
