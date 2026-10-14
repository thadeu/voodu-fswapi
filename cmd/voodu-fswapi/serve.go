package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/thadeu/voodu-fswapi/internal/api"
	"github.com/thadeu/voodu-fswapi/internal/esl"
)

const serveHelp = `voodu-fswapi serve

Serve a JSON API over a FreeSWITCH event socket. This is the container the
voodu-fswapi plugin deploys and the controller reverse-proxies to.

Config via env:

  FREESWITCH_API_ADDR        listen address        (default 0.0.0.0:8721)
  FREESWITCH_ESL_ADDR        event socket          (default host.docker.internal:8021)
  FREESWITCH_ESL_PASSWORD    event socket password (default ClueCon)
  FREESWITCH_ESL_TIMEOUT     per call              (default 5s)
  FREESWITCH_ACTIONS         enabled actions, e.g. reloadxml,hupall (default none)
  FREESWITCH_ACTION_SOURCES  CIDRs allowed to POST (default: the voodu0 gateway)
`

func cmdServe() error {
	if hasHelpFlag(os.Args[2:]) {
		fmt.Print(serveHelp)

		return nil
	}

	cfg, err := api.LoadConfig()
	if err != nil {
		return err
	}

	logger := log.New(os.Stderr, "", log.LstdFlags|log.LUTC)

	actions, err := api.NewActions(cfg.Actions)
	if err != nil {
		return err
	}

	sources, err := cfg.ActionSourcePrefixes("/proc/net/route")
	if err != nil {
		// Not fatal: reads still work, and actions fall back to loopback
		// only — closed, not open.
		logger.Printf("warning: %v", err)
	}

	client := esl.Client{Addr: cfg.ESLAddr, Password: cfg.ESLPassword, Timeout: cfg.ESLTimeout}
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           api.NewServer(client, actions, sources, logger).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      cfg.ESLTimeout + 10*time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)

	go func() {
		logger.Printf("voodu-fswapi %s listening on %s (esl %s, actions: %q, action sources: %v)",
			version, cfg.Addr, cfg.ESLAddr, cfg.Actions, sources)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}

		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		return srv.Shutdown(shutdown)
	}
}

func hasHelpFlag(args []string) bool {
	for _, a := range args {
		if a == "-h" || a == "--help" || a == "help" {
			return true
		}
	}

	return false
}
