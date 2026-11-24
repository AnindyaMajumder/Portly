// Portly shows what uses local ports and stops it safely.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/AnindyaMajumder/Portly/internal/app"
	"github.com/AnindyaMajumder/Portly/internal/cli"
	"github.com/AnindyaMajumder/Portly/internal/modules/discovery"
	"github.com/AnindyaMajumder/Portly/internal/modules/docker"
	"github.com/AnindyaMajumder/Portly/internal/modules/project"
	"github.com/AnindyaMajumder/Portly/internal/modules/stopper"
)

var version = "dev"

// main wires the modules and sets the exit status.
func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	engines := docker.New()
	world := app.NewWorld(discovery.New(), engines)
	portly := app.New(world, project.New(), stopper.New(world, engines))
	exit := cli.Run(ctx, os.Args[1:], cli.SystemConsole(buildVersion()), portly)
	cancel()
	os.Exit(exit)
}

// buildVersion marks builds that can stop things before their gate passed.
func buildVersion() string {
	if stopper.EvidenceBuild {
		return version + " (evidence build, for tests only)"
	}
	return version
}
