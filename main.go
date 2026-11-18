// Command garm-reconcile brings a garm controller to a declared state.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/katexochen/garm-reconcile/internal/config"
	"github.com/katexochen/garm-reconcile/internal/garm"
	"github.com/katexochen/garm-reconcile/internal/reconcile"
	"github.com/katexochen/garm-reconcile/internal/sdnotify"
)

const usage = `Usage: garm-reconcile <command> [flags] <desired state file>

Commands:
  validate  check the desired state file without contacting garm
  plan      show what apply would change
  apply     create and update what is declared, and with -prune delete the rest

Run "garm-reconcile <command> -h" for the flags of a command.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	app := cli{stdout: os.Stdout, stderr: os.Stderr}
	err := app.run(ctx, os.Args[1:])
	if err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(os.Stderr, "garm-reconcile:", err)
		os.Exit(1)
	}
}

type cli struct {
	stdout, stderr io.Writer
	// pollInterval is how often apply checks on the runners it waits for.
	// Zero selects the default.
	pollInterval time.Duration
}

func (c cli) run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		fmt.Fprint(c.stderr, usage)
		return errors.New("missing command")
	}
	switch command, args := args[0], args[1:]; command {
	case "validate":
		return c.validate(args)
	case "plan":
		return c.plan(ctx, args)
	case "apply":
		return c.apply(ctx, args)
	case "help", "-h", "-help", "--help":
		fmt.Fprint(c.stdout, usage)
		return nil
	default:
		fmt.Fprint(c.stderr, usage)
		return fmt.Errorf("unknown command %q", command)
	}
}

// options are the flags plan and apply have in common.
type options struct {
	url   string
	wait  time.Duration
	prune bool
}

func (o *options) register(fs *flag.FlagSet) {
	fs.StringVar(&o.url, "url", "http://127.0.0.1:9997", "base URL of the garm API")
	fs.DurationVar(&o.wait, "wait", time.Minute, "how long to wait for the garm API to come up")
	fs.BoolVar(&o.prune, "prune", false, "delete what is not declared")
}

// parse parses the flags of a command and returns the path of the desired
// state file, which is its only argument.
func (c cli) parse(fs *flag.FlagSet, args []string) (string, error) {
	// Errors are printed by main, only the help is printed here.
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fs.SetOutput(c.stderr)
			fs.Usage()
		}
		return "", err
	}
	if fs.NArg() != 1 {
		return "", errors.New("expected the desired state file as the only argument")
	}
	return fs.Arg(0), nil
}

// prepare parses the arguments of a command that talks to garm, loads the
// desired state and waits for the API to be reachable.
func (c cli) prepare(ctx context.Context, fs *flag.FlagSet, opts *options, args []string) (*config.Config, *garm.Client, error) {
	opts.register(fs)
	path, err := c.parse(fs, args)
	if err != nil {
		return nil, nil, err
	}
	cfg, err := config.Load(path, config.FileSecrets(os.Getenv("CREDENTIALS_DIRECTORY")))
	if err != nil {
		return nil, nil, err
	}
	client, err := garm.NewClient(opts.url)
	if err != nil {
		return nil, nil, fmt.Errorf("-url: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, opts.wait)
	defer cancel()
	if err := client.WaitReady(ctx); err != nil {
		return nil, nil, err
	}
	return cfg, client, nil
}

func (c cli) validate(args []string) error {
	path, err := c.parse(flag.NewFlagSet("validate", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	_, err = config.Load(path, config.PlaceholderSecrets)
	return err
}

func (c cli) plan(ctx context.Context, args []string) error {
	var opts options
	cfg, client, err := c.prepare(ctx, flag.NewFlagSet("plan", flag.ContinueOnError), &opts, args)
	if err != nil {
		return err
	}

	client.ReadOnly()
	changes := 0
	rec := reconcile.New(client, cfg, reconcile.Options{
		DryRun: true,
		Report: func(change reconcile.Change) {
			changes++
			fmt.Fprintln(c.stdout, change)
		},
	})
	err = rec.Ensure(ctx)
	if errors.Is(err, reconcile.ErrNotInitialized) {
		fmt.Fprintln(c.stdout, "garm is not initialized, apply creates everything that is declared")
		return nil
	}
	if err != nil {
		return err
	}
	if opts.prune {
		if err := rec.Prune(ctx); err != nil {
			return err
		}
	}
	if changes == 0 {
		fmt.Fprintln(c.stdout, "garm matches the desired state")
	}
	return nil
}

func (c cli) apply(ctx context.Context, args []string) error {
	var opts options
	var drainTimeout time.Duration
	fs := flag.NewFlagSet("apply", flag.ContinueOnError)
	fs.DurationVar(&drainTimeout, "drain-timeout", 0,
		"how long -prune waits for the jobs on runners of deleted pools, 0 to wait until they finish")
	cfg, client, err := c.prepare(ctx, fs, &opts, args)
	if err != nil {
		return err
	}

	rec := reconcile.New(client, cfg, reconcile.Options{
		Report: func(change reconcile.Change) { fmt.Fprintln(c.stdout, change) },
		Progress: func(msg string) {
			fmt.Fprintln(c.stderr, msg)
			_ = sdnotify.Status(msg)
		},
		PollInterval: c.pollInterval,
	})
	if err := rec.Ensure(ctx); err != nil {
		return err
	}
	// Garm now serves everything that is declared. Deleting the rest can
	// wait for running jobs, which must not block whoever waits for the
	// service to start.
	if err := sdnotify.Ready(); err != nil {
		return fmt.Errorf("notifying systemd: %w", err)
	}
	if !opts.prune {
		return nil
	}
	if drainTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, drainTimeout)
		defer cancel()
	}
	if err := rec.Prune(ctx); err != nil {
		return err
	}
	// Clear what was reported as being waited for.
	_ = sdnotify.Status("")
	return nil
}
