package web

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/Valentin-Kaiser/backup/pkg/notify"
	pb "github.com/Valentin-Kaiser/backup/pkg/proto/api/v1"
	"github.com/valentin-kaiser/go-core/apperror"
	"github.com/valentin-kaiser/go-core/logging/log"
	"github.com/valentin-kaiser/go-core/web/xrpc"
)

// relayTimeout bounds one hand-over of an event to the daemon.
const relayTimeout = 15 * time.Second

// Alert hands a security event of the portal to the daemon, which mails it with its own mail settings. The portal has no
// SMTP server of its own. It returns at once, a daemon that is not reachable only costs a log line.
func (d *Daemon) Alert(e notify.Event) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), relayTimeout)
		defer cancel()
		if err := d.send(ctx, e); err != nil {
			log.Warn().Err(err).Msg("the security event could not be handed to the daemon for mailing")
		}
	}()
}

func (d *Daemon) send(ctx context.Context, e notify.Event) error {
	target, tls, err := resolve(d.settings())
	if err != nil {
		return apperror.Wrap(err)
	}
	cfg, err := tls.Config()
	if err != nil {
		return apperror.Wrap(err)
	}
	token, err := d.password.Value()
	if err != nil {
		return apperror.Wrap(err)
	}
	opts := []xrpc.ClientOption{
		xrpc.WithHTTPClient(&http.Client{Transport: &http.Transport{TLSClientConfig: cfg}, Timeout: relayTimeout}),
		xrpc.WithHeader(http.Header{"Authorization": []string{"Bearer " + token}}),
	}
	if cfg != nil {
		opts = append(opts, xrpc.WithTLSConfig(cfg))
	}
	c, err := pb.NewControlClient(strings.TrimRight(target.String(), "/")+daemonRoute, opts...)
	if err != nil {
		return apperror.Wrap(err)
	}
	ev := &pb.NotificationEvent{Title: e.Title, Severity: string(e.Severity), Key: e.Key, Detail: e.Detail}
	for _, f := range e.Fields {
		ev.Fields = append(ev.Fields, &pb.NotificationField{Label: f.Label, Value: f.Value})
	}
	_, err = c.SendNotification(ctx, ev)
	return apperror.Wrap(err)
}
