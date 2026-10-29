package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	pb "github.com/Valentin-Kaiser/backup/pkg/proto/api/v1"
	"github.com/valentin-kaiser/go-core/apperror"
)

// repo manages the repositories that restic jobs work on.
func (c *client) repo(args []string) error {
	ctx := context.Background()
	if len(args) == 0 {
		return apperror.NewError("usage: control repo list|set|delete ...")
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("repo "+sub, flag.ContinueOnError)
	switch sub {
	case "list":
		res, err := c.control.ListRepositories(ctx, &pb.ListRepositoriesRequest{})
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(w, "NAME\tPATH\tSECRET\tMOUNT\tJOBS")
		for _, r := range res.Repositories {
			mount := r.MountPath
			if r.MountMarker != "" {
				mount += "/" + r.MountMarker
			}
			_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", r.Name, r.Path, dash(r.PasswordSecret), dash(mount), dash(strings.Join(r.Jobs, ", ")))
		}
		return w.Flush()
	case "set":
		r := &pb.Repository{}
		fs.StringVar(&r.Path, "path", "", "restic repository (directory or address)")
		fs.StringVar(&r.PasswordSecret, "secret", "", "stored secret with the repository password")
		fs.StringVar(&r.MountPath, "mount-path", "", "directory that must exist before any job works on the repository")
		fs.StringVar(&r.MountMarker, "mount-marker", "", "file that must exist inside the mount path (e.g. .mounted)")
		fs.StringVar(&r.RestoreRoot, "restore-root", "", "restores are limited to this directory, /restore when empty")
		name, err := parseJob(fs, rest)
		if err != nil {
			return err
		}
		r.Name = name
		_, err = c.control.SetRepository(ctx, r)
		return err
	case "delete":
		name, err := parseJob(fs, rest)
		if err != nil {
			return err
		}
		_, err = c.control.DeleteRepository(ctx, &pb.RepositoryRequest{Name: name})
		return err
	}
	return apperror.NewErrorf("unknown repo command %q", sub)
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
