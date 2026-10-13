package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

	pb "github.com/Valentin-Kaiser/backup/pkg/proto/api/v1"
	"github.com/valentin-kaiser/go-core/apperror"
)

// resync drops the state of a sync job and runs it from scratch.
func (c *client) resync(args []string) error {
	fs := flag.NewFlagSet("resync", flag.ContinueOnError)
	wait := fs.Bool("wait", false, "wait for the run and print its output")
	job, err := parseJob(fs, args)
	if err != nil {
		return err
	}
	r, err := c.control.ResyncJob(context.Background(), &pb.JobRequest{Job: job})
	if err != nil {
		return err
	}
	fmt.Println("started run", r.Id)
	if *wait {
		return c.wait(r.Id)
	}
	return nil
}

// peer manages the peers that sync jobs can use.
func (c *client) peer(args []string) error {
	ctx := context.Background()
	if len(args) == 0 {
		return apperror.NewError("usage: control peer list|add|test|delete ...")
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("peer "+sub, flag.ContinueOnError)
	switch sub {
	case "list":
		res, err := c.control.ListPeers(ctx, &pb.ListPeersRequest{})
		if err != nil {
			return err
		}
		fmt.Println("this worker:", res.WorkerName)
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tSYNC SERVER\tUSER\tCONTROL API")
		for _, p := range res.Peers {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", p.Name, p.Url, p.Username, p.ApiUrl)
		}
		return w.Flush()
	case "add":
		p := &pb.Peer{}
		fs.StringVar(&p.Url, "url", "", "URL of the sync server of the peer (https://host:9081)")
		fs.StringVar(&p.Username, "user", "sync", "user of the sync server")
		fs.StringVar(&p.PasswordFile, "password-file", "", "file with the password of the sync server")
		fs.StringVar(&p.PasswordSecret, "password-secret", "", "stored secret with the password of the sync server")
		fs.StringVar(&p.CaFile, "ca-file", "", "CA file of the sync server")
		fs.StringVar(&p.ApiUrl, "api-url", "", "URL of the control API of the peer (https://host:9080)")
		fs.StringVar(&p.ApiPasswordFile, "api-password-file", "", "file with the password of the control API")
		fs.StringVar(&p.ApiPasswordSecret, "api-password-secret", "", "stored secret with the password of the control API")
		fs.StringVar(&p.ApiCaFile, "api-ca-file", "", "CA file of the control API")
		name, err := parseJob(fs, rest)
		if err != nil {
			return err
		}
		p.Name = name
		_, err = c.control.SetPeer(ctx, p)
		return err
	case "test":
		name, err := parseJob(fs, rest)
		if err != nil {
			return err
		}
		st, err := c.control.TestPeer(ctx, &pb.PeerRequest{Name: name})
		if err != nil {
			return err
		}
		fmt.Printf("sync server: %s\ncontrol API: %s (%d ms)\n", okText(st.DataOk), okText(st.ApiOk), st.LatencyMs)
		if st.Error != "" {
			return apperror.NewError(st.Error)
		}
		return nil
	case "delete":
		name, err := parseJob(fs, rest)
		if err != nil {
			return err
		}
		_, err = c.control.DeletePeer(ctx, &pb.PeerRequest{Name: name})
		return err
	}
	return apperror.NewErrorf("unknown peer command %q", sub)
}

func okText(ok bool) string {
	if ok {
		return "ok"
	}
	return "failed"
}
