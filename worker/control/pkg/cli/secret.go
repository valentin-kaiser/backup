package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	pb "github.com/Valentin-Kaiser/backup/pkg/proto/api/v1"
	"github.com/Valentin-Kaiser/backup/pkg/secrets"
	"github.com/Valentin-Kaiser/backup/pkg/secure"
	"golang.org/x/term"

	"github.com/valentin-kaiser/go-core/apperror"
)

// masterKeyFile is the file with the key that encrypts the stored secrets of the daemon.
func masterKeyFile() string {
	if p := envOr("CONTROL_MASTERKEYFILE", ""); p != "" {
		return p
	}
	return filepath.Join(dataDir(), "master.key")
}

// rekey re-encrypts the stored secrets with a new master key. It works on the files, the daemon has to be stopped.
func rekey() error {
	fmt.Fprintln(os.Stderr, "Stop the daemon first, it keeps the secrets in memory and would write them with the old key again.")
	err := secure.Rotate(masterKeyFile(), func(old, next secure.Key) error {
		return secrets.Rekey(dataDir(), old, next)
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "done. The new key is in %s, the old one in %s.old: keep it until you have checked that the daemon\nstarts and your backups run, then delete it.\n", masterKeyFile(), masterKeyFile())
	return nil
}

// readValue reads a secret from the terminal (twice) or, when stdin is not a terminal, from its first line.
func readValue(prompt string) (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	fmt.Fprint(os.Stderr, prompt)
	first, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	fmt.Fprint(os.Stderr, "Repeat: ")
	again, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if string(first) != string(again) {
		return "", apperror.NewError("the values differ")
	}
	return string(first), nil
}

func printGenerated(v string) {
	fmt.Fprintln(os.Stderr, "generated, it is shown only now. Store it somewhere safe: without it the repository cannot be opened:")
	fmt.Println(v)
}

// secret manages the stored secrets through the API of the daemon.
func (c *client) secret(args []string) error {
	if len(args) == 0 {
		return apperror.NewError("usage: secret list | set NAME [-generate] | passwd NAME [-generate] | delete NAME | rekey")
	}
	ctx := context.Background()
	fs := flag.NewFlagSet("secret "+args[0], flag.ContinueOnError)
	generate := fs.Bool("generate", false, "generate a strong random value")
	name := ""
	if args[0] != "list" {
		if len(args) < 2 || strings.HasPrefix(args[1], "-") {
			return apperror.NewErrorf("usage: secret %s NAME", args[0])
		}
		name = args[1]
		args = append([]string{args[0]}, args[2:]...)
	}
	switch args[0] {
	case "list":
		res, err := c.control.ListSecrets(ctx, &pb.ListSecretsRequest{})
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(w, "NAME\tUPDATED\tUSED BY")
		for _, s := range res.Secrets {
			users := strings.Join(append(s.Repositories, s.OtherUsers...), ", ")
			if users == "" {
				users = "-"
			}
			_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", s.Name, s.UpdateTime.AsTime().Local().Format(time.DateTime), users)
		}
		return w.Flush()
	case "set", "passwd":
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		value := ""
		if !*generate {
			var err error
			if value, err = readValue("Password: "); err != nil {
				return err
			}
		}
		if args[0] == "set" {
			res, err := c.control.SetSecret(ctx, &pb.SetSecretRequest{Name: name, Value: value, Generate: *generate})
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "secret %s stored. This did not change any repository, use 'secret passwd' for that.\n", name)
			if res.GeneratedValue != "" {
				printGenerated(res.GeneratedValue)
			}
			return nil
		}
		res, err := c.control.ChangeRepositoryPassword(ctx, &pb.ChangeRepositoryPasswordRequest{Name: name, Value: value, Generate: *generate})
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "the password of %d repositories changed.\n", res.Repositories)
		for _, w := range res.Warnings {
			fmt.Fprintln(os.Stderr, "warning:", w)
		}
		if res.GeneratedValue != "" {
			printGenerated(res.GeneratedValue)
		}
		return nil
	case "delete":
		if _, err := c.control.DeleteSecret(ctx, &pb.SecretRequest{Name: name}); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "secret %s deleted\n", name)
		return nil
	}
	return apperror.NewErrorf("unknown secret command %q", args[0])
}
