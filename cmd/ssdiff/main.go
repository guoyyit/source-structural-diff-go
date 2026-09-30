// Command ssdiff is the CLI front-end for the structural diff service.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/guoyyit/source-structural-diff-go/internal/diff"
	"github.com/guoyyit/source-structural-diff-go/internal/report"
	"github.com/guoyyit/source-structural-diff-go/internal/service"
)

const usage = `ssdiff - structural source diff and review baseline service

Usage:
  ssdiff commit <root> [--store dir]
      Scan the Go source tree at root and atomically commit a baseline.
  ssdiff diff <oldID> <newID> [--store dir] [--format text|json]
      Diff two committed baselines.
  ssdiff diff-workspace <baseID> <root> [--store dir] [--format text|json]
      Diff a committed baseline (or "" for empty) against a working tree.
  ssdiff list [--store dir]
      List committed baseline IDs, oldest first.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch os.Args[1] {
	case "commit":
		err = cmdCommit(ctx, os.Args[2:])
	case "diff":
		err = cmdDiff(ctx, os.Args[2:])
	case "diff-workspace":
		err = cmdDiffWorkspace(ctx, os.Args[2:])
	case "list":
		err = cmdList(os.Args[2:])
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	return fs
}

func progressToStderr(p service.Progress) {
	fmt.Fprintf(os.Stderr, "\rprogress: %d/%d files (slowest: %s)   ",
		p.ProcessedFiles, p.TotalFiles, p.SlowFile)
	if p.ProcessedFiles == p.TotalFiles {
		fmt.Fprintln(os.Stderr)
	}
}

func cmdCommit(ctx context.Context, args []string) error {
	fs := newFlagSet("commit")
	store := fs.String("store", ".ssdiff", "baseline store directory")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("commit requires exactly one source tree root")
	}
	svc, err := service.New(*store)
	if err != nil {
		return err
	}
	id, err := svc.CommitTree(ctx, fs.Arg(0), progressToStderr)
	if err != nil {
		return err
	}
	fmt.Println(id)
	return nil
}

func cmdDiff(ctx context.Context, args []string) error {
	fs := newFlagSet("diff")
	store := fs.String("store", ".ssdiff", "baseline store directory")
	format := fs.String("format", "text", "output format: text or json")
	fs.Parse(args)
	if fs.NArg() != 2 {
		return fmt.Errorf("diff requires two baseline IDs")
	}
	svc, err := service.New(*store)
	if err != nil {
		return err
	}
	rep, err := svc.DiffBaselines(ctx, fs.Arg(0), fs.Arg(1))
	if err != nil {
		return err
	}
	return writeReport(rep, *format)
}

func cmdDiffWorkspace(ctx context.Context, args []string) error {
	fs := newFlagSet("diff-workspace")
	store := fs.String("store", ".ssdiff", "baseline store directory")
	format := fs.String("format", "text", "output format: text or json")
	fs.Parse(args)
	if fs.NArg() != 2 {
		return fmt.Errorf("diff-workspace requires a baseline ID and a root")
	}
	svc, err := service.New(*store)
	if err != nil {
		return err
	}
	rep, err := svc.DiffWorkspace(ctx, fs.Arg(0), fs.Arg(1), progressToStderr)
	if err != nil {
		return err
	}
	return writeReport(rep, *format)
}

func cmdList(args []string) error {
	fs := newFlagSet("list")
	store := fs.String("store", ".ssdiff", "baseline store directory")
	fs.Parse(args)
	svc, err := service.New(*store)
	if err != nil {
		return err
	}
	ids, err := svc.ListBaselines()
	if err != nil {
		return err
	}
	for _, id := range ids {
		fmt.Println(id)
	}
	return nil
}

func writeReport(rep *diff.Report, format string) error {
	switch format {
	case "text":
		return report.WriteText(os.Stdout, rep)
	case "json":
		return report.WriteJSON(os.Stdout, rep)
	default:
		return fmt.Errorf("unknown format %q", format)
	}
}
