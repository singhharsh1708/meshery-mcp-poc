// Command dualera puts a dual-era front end on a legacy MCP server.
//
//	dualera ./legacy-mcp-server
//
// The wrapped server is spoken to over stdio and keeps answering exactly as it
// did. Clients on either protocol era talk to this process instead.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"

	"github.com/singhharsh1708/meshery-mcp-poc/dualera"
)

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: dualera <server> [args...]\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}

	ctx := context.Background()
	child := exec.CommandContext(ctx, flag.Arg(0), flag.Args()[1:]...)
	child.Stderr = os.Stderr

	toChild, err := child.StdinPipe()
	if err != nil {
		fail(err)
	}
	fromChild, err := child.StdoutPipe()
	if err != nil {
		fail(err)
	}
	if err := child.Start(); err != nil {
		fail(err)
	}
	defer func() {
		_ = toChild.Close()
		_ = child.Process.Kill()
		_ = child.Wait()
	}()

	b := dualera.New(toChild, fromChild, os.Stdout)
	if err := b.Run(ctx, os.Stdin); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "dualera:", err)
	os.Exit(1)
}
