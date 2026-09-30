// Command ssdiff is the CLI for the structural diff baseline service.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/guoyyit/source-structural-diff-go/internal/baseline"
	"github.com/guoyyit/source-structural-diff-go/internal/diff"
	"github.com/guoyyit/source-structural-diff-go/internal/service"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "commit":
		err = cmdCommit(os.Args[2:])
	case "diff":
		err = cmdDiff(os.Args[2:])
	case "diff-workspace":
		err = cmdDiffWorkspace(os.Args[2:])
	case "list":
		err = cmdList(os.Args[2:])
	case "head":
		err = cmdHead(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  ssdiff commit         -store DIR -root DIR [-workers N] [-v]
  ssdiff diff           -store DIR [-json] OLD_ID NEW_ID
  ssdiff diff-workspace -store DIR [-json] BASE_ID -root DIR
  ssdiff list           -store DIR
  ssdiff head           -store DIR

IDs accept the special value HEAD for the current baseline.`)
}

func openService(storeDir string, verbose bool, workers int) *service.Service {
	st, err := baseline.Open(storeDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	opts := service.Options{Workers: workers}
	if verbose {
		opts.Progress = func(ev service.ProgressEvent) {
			switch ev.Kind {
			case "scan":
				fmt.Fprintf(os.Stderr, "scanning %d files\n", ev.Total)
			case "slow":
				fmt.Fprintf(os.Stderr, "slow file %s (%s)\n", ev.Path, ev.Duration.Round(time.Millisecond))
			case "commit":
				fmt.Fprintf(os.Stderr, "committed %s\n", ev.Baseline)
			}
		}
	}
	return service.New(st, opts)
}

func cmdCommit(args []string) error {
	fs := flag.NewFlagSet("commit", flag.ExitOnError)
	store := fs.String("store", ".ssdiff", "store directory")
	root := fs.String("root", ".", "source tree root")
	workers := fs.Int("workers", 0, "parse workers")
	verbose := fs.Bool("v", false, "verbose progress")
	fs.Parse(args)
	svc := openService(*store, *verbose, *workers)
	id, err := svc.Commit(context.Background(), *root)
	if err != nil {
		return err
	}
	fmt.Println(id)
	return nil
}

func mustStore(dir string) *baseline.Store {
	st, err := baseline.Open(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	return st
}

func cmdDiff(args []string) error {
	fs := flag.NewFlagSet("diff", flag.ExitOnError)
	store := fs.String("store", ".ssdiff", "store directory")
	asJSON := fs.Bool("json", false, "emit JSON report")
	fs.Parse(args)
	if fs.NArg() != 2 {
		return fmt.Errorf("diff requires OLD_ID and NEW_ID")
	}
	svc := openService(*store, false, 0)
	rep, err := svc.Diff(context.Background(), fs.Arg(0), fs.Arg(1))
	if err != nil {
		return err
	}
	return printReport(rep, *asJSON)
}

func cmdDiffWorkspace(args []string) error {
	fs := flag.NewFlagSet("diff-workspace", flag.ExitOnError)
	store := fs.String("store", ".ssdiff", "store directory")
	root := fs.String("root", ".", "source tree root")
	asJSON := fs.Bool("json", false, "emit JSON report")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("diff-workspace requires BASE_ID")
	}
	svc := openService(*store, false, 0)
	rep, err := svc.DiffWorkspace(context.Background(), fs.Arg(0), *root)
	if err != nil {
		return err
	}
	return printReport(rep, *asJSON)
}

func cmdList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	store := fs.String("store", ".ssdiff", "store directory")
	fs.Parse(args)
	st := mustStore(*store)
	ids, err := st.List()
	if err != nil {
		return err
	}
	head, _ := st.Head()
	for _, id := range ids {
		mark := "  "
		if id == head {
			mark = "* "
		}
		fmt.Println(mark + id)
	}
	return nil
}

func cmdHead(args []string) error {
	fs := flag.NewFlagSet("head", flag.ExitOnError)
	store := fs.String("store", ".ssdiff", "store directory")
	fs.Parse(args)
	head, err := mustStore(*store).Head()
	if err != nil {
		return err
	}
	fmt.Println(head)
	return nil
}

func printReport(rep *diff.Report, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}
	for _, e := range rep.Entries {
		line := fmt.Sprintf("%-9s %-6s %s %s", e.Category, e.Kind, e.File, e.Name)
		if e.OldName != "" {
			line += " (was " + e.OldName + ")"
		}
		if e.OldFile != "" {
			line += " (from " + e.OldFile + ")"
		}
		if e.Confidence > 0 {
			line += fmt.Sprintf(" [confidence %.2f]", e.Confidence)
		}
		if e.Candidate {
			line += " [candidate]"
		}
		fmt.Println(line)
	}
	fmt.Printf("summary:")
	for _, c := range []diff.Category{diff.Unchanged, diff.Modified, diff.Added, diff.Deleted, diff.Renamed, diff.Moved} {
		fmt.Printf(" %s=%d", c, rep.Summary[c])
	}
	fmt.Println()
	return nil
}
