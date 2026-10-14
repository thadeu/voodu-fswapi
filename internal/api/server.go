// Package api is the HTTP surface of voodu-fswapi: JSON reads over the
// FreeSWITCH event socket, plus a small, allowlisted set of actions. The
// controller's PAT proxy is the only intended client (read scope for GET,
// actions scope for POST — enforced there); this server adds its own guard on
// POST, see Config.ActionSourcePrefixes.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"

	"github.com/thadeu/voodu-fswapi/internal/esl"
)

var (
	errUnknownAction  = errors.New("unknown action")
	errActionDisabled = errors.New("action disabled")
)

// maxActionBody caps an action's JSON parameters — they are a cause or a
// uuid, never a document.
const maxActionBody = 4 << 10

// Runner runs api commands on FreeSWITCH (esl.Client in production, a fake in
// tests).
type Runner interface {
	API(ctx context.Context, commands ...string) ([]string, error)
}

// Server serves the API.
type Server struct {
	esl     Runner
	actions Actions
	sources []netip.Prefix
	logger  *log.Logger
}

// NewServer wires a Server. sources is who may POST an action.
func NewServer(esl Runner, actions Actions, sources []netip.Prefix, logger *log.Logger) *Server {
	return &Server{esl: esl, actions: actions, sources: sources, logger: logger}
}

// Handler is the route table.
//
//	GET  /health                 liveness of THIS process (no FreeSWITCH call)
//	GET  /overview               status + calls + channels, one ESL connection
//	GET  /status                 `status`, parsed
//	GET  /calls, /calls/count    `show calls as json` / `show calls count`
//	GET  /channels, /channels/count
//	GET  /registrations          `show registrations as json`
//	GET  /sofia                  `sofia xmlstatus` (profiles, gateways)
//	GET  /actions                every action + whether it is enabled
//	POST /actions/{name}         run an enabled action (JSON params)
//	POST /calls/{uuid}/kill      uuid_kill one channel
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	mux.HandleFunc("GET /overview", s.overview)
	mux.HandleFunc("GET /status", s.status)
	mux.HandleFunc("GET /calls", s.rows("show calls as json"))
	mux.HandleFunc("GET /calls/count", s.count("show calls count"))
	mux.HandleFunc("GET /channels", s.rows("show channels as json"))
	mux.HandleFunc("GET /channels/count", s.count("show channels count"))
	mux.HandleFunc("GET /registrations", s.rows("show registrations as json"))
	mux.HandleFunc("GET /sofia", s.sofia)
	mux.HandleFunc("GET /actions", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"actions": s.actions.List()})
	})

	mux.HandleFunc("POST /actions/{name}", s.fromHost(s.runAction))
	mux.HandleFunc("POST /calls/{uuid}/kill", s.fromHost(s.killCall))

	return mux
}

func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	replies, err := s.esl.API(r.Context(), "status", "show calls count", "show channels count")
	if err != nil {
		s.eslFailed(w, err)

		return
	}

	calls, errCalls := ParseCount(replies[1])
	channels, errChannels := ParseCount(replies[2])

	if err := errors.Join(errCalls, errChannels); err != nil {
		writeError(w, http.StatusBadGateway, err)

		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status": ParseStatus(replies[0]), "calls": calls, "channels": channels,
	})
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	replies, err := s.esl.API(r.Context(), "status")
	if err != nil {
		s.eslFailed(w, err)

		return
	}

	writeJSON(w, http.StatusOK, ParseStatus(replies[0]))
}

func (s *Server) count(cmd string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		replies, err := s.esl.API(r.Context(), cmd)
		if err != nil {
			s.eslFailed(w, err)

			return
		}

		n, err := ParseCount(replies[0])
		if err != nil {
			writeError(w, http.StatusBadGateway, err)

			return
		}

		writeJSON(w, http.StatusOK, map[string]any{"count": n})
	}
}

func (s *Server) rows(cmd string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		replies, err := s.esl.API(r.Context(), cmd)
		if err != nil {
			s.eslFailed(w, err)

			return
		}

		rows, err := ParseRows(replies[0])
		if err != nil {
			writeError(w, http.StatusBadGateway, err)

			return
		}

		writeJSON(w, http.StatusOK, rows)
	}
}

func (s *Server) sofia(w http.ResponseWriter, r *http.Request) {
	replies, err := s.esl.API(r.Context(), "sofia xmlstatus")
	if err != nil {
		s.eslFailed(w, err)

		return
	}

	entries, err := ParseSofia(replies[0])
	if err != nil {
		writeError(w, http.StatusBadGateway, err)

		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

func (s *Server) runAction(w http.ResponseWriter, r *http.Request) {
	params, err := decodeParams(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)

		return
	}

	s.execute(w, r, r.PathValue("name"), params)
}

func (s *Server) killCall(w http.ResponseWriter, r *http.Request) {
	s.execute(w, r, "uuid_kill", map[string]string{"uuid": r.PathValue("uuid")})
}

// execute builds and runs one action. Every attempt is logged — an action
// ends calls, and its trail must not depend on the caller keeping one.
func (s *Server) execute(w http.ResponseWriter, r *http.Request, name string, params map[string]string) {
	cmd, err := s.actions.Command(name, params)

	switch {
	case errors.Is(err, errUnknownAction):
		writeError(w, http.StatusNotFound, err)

		return
	case errors.Is(err, errActionDisabled):
		writeError(w, http.StatusForbidden, errors.New("action "+name+" is disabled (enable it with the plugin's `actions` field)"))

		return
	case err != nil:
		writeError(w, http.StatusUnprocessableEntity, err)

		return
	}

	s.logger.Printf("action %s from %s: %s", name, r.RemoteAddr, cmd)

	replies, err := s.esl.API(r.Context(), cmd)
	if err != nil {
		s.eslFailed(w, err)

		return
	}

	if err := replyError(replies[0]); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"ok": false, "action": name, "error": err.Error()})

		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "action": name, "reply": replies[0]})
}

// fromHost refuses an action from anything but the allowed sources (the
// voodu0 gateway by default — where the controller's proxy comes from).
func (s *Server) fromHost(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)

		addr, perr := netip.ParseAddr(host)
		if err != nil || perr != nil || !s.allowedSource(addr.Unmap()) {
			writeError(w, http.StatusForbidden, errors.New("actions are accepted only through the voodu controller"))

			return
		}

		next(w, r)
	}
}

func (s *Server) allowedSource(addr netip.Addr) bool {
	for _, p := range s.sources {
		if p.Contains(addr) {
			return true
		}
	}

	return false
}

func (s *Server) eslFailed(w http.ResponseWriter, err error) {
	s.logger.Printf("esl: %v", err)

	if errors.Is(err, esl.ErrAuth) {
		err = fmt.Errorf("%w — %s", err, AuthHint)
	}

	writeError(w, http.StatusBadGateway, err)
}

func decodeParams(r *http.Request) (map[string]string, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxActionBody+1))
	if err != nil {
		return nil, err
	}

	if len(body) > maxActionBody {
		return nil, errors.New("action parameters too large")
	}

	params := map[string]string{}

	if len(body) == 0 {
		return params, nil
	}

	if err := json.Unmarshal(body, &params); err != nil {
		return nil, errors.New("action parameters must be a JSON object of strings")
	}

	return params, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]any{"error": err.Error()})
}
