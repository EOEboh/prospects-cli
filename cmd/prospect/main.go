// Command prospect discovers, scores and tracks businesses to sell
// lead-capture automation to.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/EOEboh/prospects-cli/internal/cli"
	"github.com/EOEboh/prospects-cli/internal/config"
)

func main() {
	// Ctrl-C cancels in-flight work so a long enrich run stops between
	// businesses with its checkpoints intact rather than being killed mid-write.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := cli.Execute(ctx, config.Version); err != nil {
		fmt.Fprintln(os.Stderr, "prospect: "+err.Error())
		os.Exit(1)
	}
}
