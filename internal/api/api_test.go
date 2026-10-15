package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thadeu/voodu-fswapi/internal/esl"
)

const sampleStatus = `UP 0 years, 2 days, 3 hours, 4 minutes, 5 seconds, 600 milliseconds, 70 microseconds
FreeSWITCH (Version 1.10.11 -release 64bit) is ready
1234 session(s) since startup
5 session(s) - peak 42, last 5min 10
1 session(s) per Sec out of max 30, peak 7, last 5min 2
1000 session(s) max
min idle cpu 0.00/98.47
Current Stack Size/Max 240K/8192K
`

const sampleSofia = `<?xml version="1.0" encoding="ISO-8859-1"?>
<profiles>
<profile>
<name>external</name>
<type>profile</type>
<data>sip:mod_sofia@10.0.0.5:5080</data>
<state>RUNNING (0)</state>
</profile>
<gateway>
<name>external::carrier</name>
<type>gateway</type>
<data>sip:trunk@carrier.example</data>
<state>REGED</state>
</gateway>
</profiles>
`

func TestParseStatus(t *testing.T) {
	s := ParseStatus(sampleStatus)

	if !s.Up || s.Version != "1.10.11 -release 64bit" {
		t.Errorf("up/version = %v/%q", s.Up, s.Version)
	}

	if want := int64(2*86400 + 3*3600 + 4*60 + 5); s.UptimeSeconds != want {
		t.Errorf("uptime_seconds = %d, want %d", s.UptimeSeconds, want)
	}

	if s.SessionsSinceStart != 1234 || s.Sessions != 5 || s.SessionsPeak != 42 || s.SessionsPeak5Min != 10 {
		t.Errorf("sessions = %+v", s)
	}

	if s.SessionsPerSec != 1 || s.SessionsPerSecMax != 30 || s.SessionsPerSecPeak != 7 || s.SessionsMax != 1000 {
		t.Errorf("sps/max = %+v", s)
	}

	if s.IdleCPU != 98.47 {
		t.Errorf("idle_cpu = %v", s.IdleCPU)
	}
}

func TestParseStatus_DownKeepsRaw(t *testing.T) {
	s := ParseStatus("-ERR something odd\n")

	if s.Up || s.Raw == "" {
		t.Errorf("a reply we can't read must report down and keep the raw text: %+v", s)
	}
}

func TestParseCount(t *testing.T) {
	n, err := ParseCount("\n17 total.\n")
	if err != nil || n != 17 {
		t.Fatalf("ParseCount = %d, %v", n, err)
	}

	if _, err := ParseCount("-ERR nope"); err == nil {
		t.Error("a reply without a total must be an error, not 0")
	}
}

func TestParseRows(t *testing.T) {
	rows, err := ParseRows(`{"row_count":0}`)
	if err != nil || rows.Count != 0 || rows.Rows == nil {
		t.Fatalf("empty reply = %+v, %v (rows must be [], not null)", rows, err)
	}

	rows, err = ParseRows(`{"row_count":1,"rows":[{"uuid":"abc","cid_num":"5511"}]}`)
	if err != nil || rows.Count != 1 || rows.Rows[0]["cid_num"] != "5511" {
		t.Fatalf("rows = %+v, %v", rows, err)
	}
}

func TestParseSofia(t *testing.T) {
	entries, err := ParseSofia(sampleSofia)
	if err != nil {
		t.Fatalf("ParseSofia: %v", err)
	}

	if len(entries) != 2 || entries[0].Kind != "profile" || entries[1].Kind != "gateway" || entries[1].State != "REGED" {
		t.Errorf("entries = %+v", entries)
	}
}

func TestActions_AllowlistAndValidation(t *testing.T) {
	if _, err := NewActions("reloadxml,hupal"); err == nil {
		t.Fatal("a typo in the allowlist must fail, not leave the action silently off")
	}

	a, err := NewActions("reloadxml, hupall")
	if err != nil {
		t.Fatalf("NewActions: %v", err)
	}

	if cmd, _ := a.Command("hupall", nil); cmd != "hupall NORMAL_CLEARING" {
		t.Errorf("hupall default = %q", cmd)
	}

	if _, err := a.Command("hupall", map[string]string{"cause": "X\n\napi shutdown"}); err == nil {
		t.Error("a cause outside [A-Z_] must be refused")
	}

	if _, err := a.Command("uuid_kill", map[string]string{"uuid": "0b7a8f2e-1c3d-4e5f-8a9b-0c1d2e3f4a5b"}); !errors.Is(err, errActionDisabled) {
		t.Errorf("uuid_kill not in the allowlist: err = %v, want disabled", err)
	}

	if _, err := a.Command("shutdown", nil); !errors.Is(err, errUnknownAction) {
		t.Errorf("unknown action: err = %v", err)
	}
}

// fakeRunner answers commands from a table and records what it ran.
type fakeRunner struct {
	replies map[string]string
	err     error
	ran     []string
}

func (f *fakeRunner) API(_ context.Context, commands ...string) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}

	out := make([]string, 0, len(commands))

	for _, c := range commands {
		f.ran = append(f.ran, c)
		out = append(out, f.replies[c])
	}

	return out, nil
}

func newTestServer(t *testing.T, runner Runner, allow string) http.Handler {
	t.Helper()

	actions, err := NewActions(allow)
	if err != nil {
		t.Fatal(err)
	}

	sources := []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}

	return NewServer(runner, actions, sources, log.New(io.Discard, "", 0)).Handler()
}

func do(h http.Handler, method, path, body, remote string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if remote != "" {
		req.RemoteAddr = remote
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	return rec
}

func TestServer_Overview(t *testing.T) {
	runner := &fakeRunner{replies: map[string]string{
		"status": sampleStatus, "show calls count": "\n3 total.\n", "show channels count": "\n6 total.\n",
	}}

	rec := do(newTestServer(t, runner, ""), http.MethodGet, "/overview", "", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}

	var got struct {
		Calls, Channels int64
		Status          Status
	}

	_ = json.Unmarshal(rec.Body.Bytes(), &got)

	if got.Calls != 3 || got.Channels != 6 || got.Status.Sessions != 5 {
		t.Errorf("overview = %+v", got)
	}
}

func TestServer_ESLDownIsBadGateway(t *testing.T) {
	rec := do(newTestServer(t, &fakeRunner{err: errors.New("dial refused")}, ""), http.MethodGet, "/calls/count", "", "")

	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rec.Code)
	}
}

// The password is the one setting the default install cannot guess, so a
// wrong one must say how to fix it.
func TestServer_ESLAuthFailureNamesTheFix(t *testing.T) {
	runner := &fakeRunner{err: fmt.Errorf("%w: -ERR invalid", esl.ErrAuth)}

	rec := do(newTestServer(t, runner, ""), http.MethodGet, "/overview", "", "")

	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rec.Code)
	}

	if !strings.Contains(rec.Body.String(), "vd config:set plugins/fswapi FREESWITCH_ESL_PASSWORD=") {
		t.Errorf("body = %s, want the config:set hint", rec.Body)
	}
}

func TestServer_DialFailureHasNoPasswordHint(t *testing.T) {
	rec := do(newTestServer(t, &fakeRunner{err: errors.New("dial refused")}, ""), http.MethodGet, "/overview", "", "")

	if strings.Contains(rec.Body.String(), "FREESWITCH_ESL_PASSWORD") {
		t.Errorf("a dial failure is not a password problem: %s", rec.Body)
	}
}

func TestLoadConfig_DefaultsNeedNoEnv(t *testing.T) {
	for _, k := range []string{"FREESWITCH_API_ADDR", "FREESWITCH_ESL_ADDR", "FREESWITCH_ESL_PASSWORD", "FREESWITCH_ESL_TIMEOUT"} {
		t.Setenv(k, "")
	}

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("the default install sets no env; LoadConfig must still work: %v", err)
	}

	if cfg.ESLPassword != "ClueCon" || cfg.Addr != "0.0.0.0:8721" || cfg.ESLAddr != "host.docker.internal:8021" {
		t.Errorf("defaults = %+v", cfg)
	}

	t.Setenv("FREESWITCH_ESL_PASSWORD", "fsw")

	if cfg, _ := LoadConfig(); cfg.ESLPassword != "fsw" {
		t.Errorf("the bucket password must win, got %q", cfg.ESLPassword)
	}
}

func TestServer_ActionsOnlyFromTheHost(t *testing.T) {
	runner := &fakeRunner{replies: map[string]string{"reloadxml": "+OK [Success]\n"}}
	h := newTestServer(t, runner, "reloadxml")

	if rec := do(h, http.MethodPost, "/actions/reloadxml", "", "172.20.0.9:4000"); rec.Code != http.StatusForbidden {
		t.Errorf("a neighbour container on voodu0: status = %d, want 403", rec.Code)
	}

	if len(runner.ran) != 0 {
		t.Fatalf("a refused action reached FreeSWITCH: %v", runner.ran)
	}

	if rec := do(h, http.MethodPost, "/actions/reloadxml", "", "127.0.0.1:4000"); rec.Code != http.StatusOK {
		t.Errorf("from the allowed source: status = %d: %s", rec.Code, rec.Body)
	}
}

func TestServer_ActionStatuses(t *testing.T) {
	runner := &fakeRunner{replies: map[string]string{
		"hupall USER_BUSY": "+OK hangup all channels\n",
		"reloadxml":        "-ERR [Error loading XML]\n",
	}}
	h := newTestServer(t, runner, "hupall,reloadxml")

	if rec := do(h, http.MethodPost, "/actions/hupall", `{"cause":"USER_BUSY"}`, "127.0.0.1:1"); rec.Code != http.StatusOK {
		t.Errorf("hupall = %d: %s", rec.Code, rec.Body)
	}

	if rec := do(h, http.MethodPost, "/actions/reloadxml", "", "127.0.0.1:1"); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("a -ERR reply = %d, want 422", rec.Code)
	}

	if rec := do(h, http.MethodPost, "/calls/0b7a8f2e-1c3d-4e5f-8a9b-0c1d2e3f4a5b/kill", "", "127.0.0.1:1"); rec.Code != http.StatusForbidden {
		t.Errorf("uuid_kill not enabled = %d, want 403", rec.Code)
	}

	if rec := do(h, http.MethodPost, "/actions/shutdown", "", "127.0.0.1:1"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown action = %d, want 404", rec.Code)
	}

	if rec := do(h, http.MethodPost, "/actions/hupall", `not json`, "127.0.0.1:1"); rec.Code != http.StatusBadRequest {
		t.Errorf("bad params = %d, want 400", rec.Code)
	}
}

func TestServer_ActionsListShowsEnabled(t *testing.T) {
	rec := do(newTestServer(t, &fakeRunner{}, "reloadxml"), http.MethodGet, "/actions", "", "")

	var got struct {
		Actions []struct {
			Name    string
			Enabled bool
		}
	}

	_ = json.Unmarshal(rec.Body.Bytes(), &got)

	enabled := map[string]bool{}
	for _, a := range got.Actions {
		enabled[a.Name] = a.Enabled
	}

	if !enabled["reloadxml"] || enabled["hupall"] || len(got.Actions) != len(catalog) {
		t.Errorf("actions = %+v", got.Actions)
	}
}

func TestDefaultGateway(t *testing.T) {
	path := filepath.Join(t.TempDir(), "route")
	table := "Iface\tDestination\tGateway \tFlags\n" +
		"eth0\t00000000\t010014AC\t0003\n" +
		"eth0\t000014AC\t00000000\t0001\n"

	if err := os.WriteFile(path, []byte(table), 0o600); err != nil {
		t.Fatal(err)
	}

	gw, err := DefaultGateway(path)
	if err != nil || gw.String() != "172.20.0.1" {
		t.Fatalf("gateway = %v, %v; want 172.20.0.1", gw, err)
	}

	prefixes, err := Config{}.ActionSourcePrefixes(path)
	if err != nil || len(prefixes) != 3 || !prefixes[2].Contains(gw) {
		t.Errorf("default sources = %v, %v", prefixes, err)
	}
}

// One channel's SIP Call-ID, read on demand: the key HEP3 files the call under.
func TestServer_SipCallID(t *testing.T) {
	const uuid = "0b7a8f2e-1c3d-4e5f-8a9b-0c1d2e3f4a5b"

	runner := &fakeRunner{replies: map[string]string{
		"uuid_getvar " + uuid + " sip_call_id": "a84b4c76e66710@pbx.example\n",
	}}

	rec := do(newTestServer(t, runner, ""), http.MethodGet, "/channels/"+strings.ToUpper(uuid)+"/sip_call_id", "", "")

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"sip_call_id":"a84b4c76e66710@pbx.example"`) {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body)
	}

	if len(runner.ran) != 1 || runner.ran[0] != "uuid_getvar "+uuid+" sip_call_id" {
		t.Errorf("ran %v, want exactly one uuid_getvar on the lower-cased uuid", runner.ran)
	}
}

func TestServer_SipCallIDMissingAndInvalid(t *testing.T) {
	const uuid = "0b7a8f2e-1c3d-4e5f-8a9b-0c1d2e3f4a5b"

	for reply, want := range map[string]int{"_undef_": http.StatusNotFound, "-ERR No such channel!\n": http.StatusNotFound} {
		runner := &fakeRunner{replies: map[string]string{"uuid_getvar " + uuid + " sip_call_id": reply}}

		if rec := do(newTestServer(t, runner, ""), http.MethodGet, "/channels/"+uuid+"/sip_call_id", "", ""); rec.Code != want {
			t.Errorf("reply %q: status = %d, want %d", reply, rec.Code, want)
		}
	}

	runner := &fakeRunner{}

	if rec := do(newTestServer(t, runner, ""), http.MethodGet, "/channels/not-a-uuid/sip_call_id", "", ""); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("invalid uuid: status = %d, want 422", rec.Code)
	}

	if len(runner.ran) != 0 {
		t.Errorf("an invalid uuid must never reach ESL, ran %v", runner.ran)
	}
}
