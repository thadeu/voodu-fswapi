// Command voodu-fswapi is the voodu plugin for the `fswapi` resource kind —
// an HTTP API in front of a FreeSWITCH event socket (ESL), reached by
// the WebUI through the controller's PAT proxy. The controller invokes it as
// a single binary, dispatching on argv[1]:
//
//	expand  — (internal) emit the API deployment (local image) on apply
//	serve   — run the API server (the container the plugin deploys)
//	help    — operator overview
//
// The API is a plain deployment once expanded, so its lifecycle
// (start/stop/restart/logs/delete) is the generic `vd` — no plugin command.
package main

import (
	"fmt"
	"log"
	"os"
)

// version is set via -ldflags at release time.
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		emitErr("usage: voodu-fswapi <expand|serve|help|--version>")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "--version", "-v", "version":
		fmt.Println(version)

	case "expand":
		if err := cmdExpand(); err != nil {
			emitErr(err.Error())
			os.Exit(1)
		}

	case "serve":
		// Long-running HTTP server. Run as a container by the plugin;
		// errors go to stderr (not the JSON envelope).
		if err := cmdServe(); err != nil {
			log.Fatalf("voodu-fswapi serve: %v", err)
		}

	case "help":
		printPluginOverview()

	default:
		emitErr(fmt.Sprintf("unknown subcommand %q (want expand|serve|help)", os.Args[1]))
		os.Exit(1)
	}
}
