package api

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"strings"
	"time"
)

// Config is the server's environment. Every value has a default, so the API
// the install hook applies with no HCL boots as-is. The ESL password, when it
// is not FreeSWITCH's own default, goes in the config bucket — never in HCL.
type Config struct {
	Addr          string        // FREESWITCH_API_ADDR      listen address
	ESLAddr       string        // FREESWITCH_ESL_ADDR      FreeSWITCH event socket
	ESLPassword   string        // FREESWITCH_ESL_PASSWORD  event socket password
	ESLTimeout    time.Duration // FREESWITCH_ESL_TIMEOUT   per call
	Actions       string        // FREESWITCH_ACTIONS       allowlist, e.g. "reloadxml,hupall"
	ActionSources string        // FREESWITCH_ACTION_SOURCES CIDRs allowed to POST (default: the host)
}

const (
	// DefaultAddr is NOT 8080: that port is already taken inside the
	// platform, and a fixed, distinct port keeps the two apart even when a
	// pod ends up on the host network.
	DefaultAddr    = "0.0.0.0:8721"
	DefaultESLAddr = "host.docker.internal:8021"

	// DefaultESLPassword is FreeSWITCH's shipped event_socket password.
	// Not a secret this plugin adds: a box that never changed it accepts
	// it anyway, and one that did answers 502 with the fix (see AuthHint).
	DefaultESLPassword = "ClueCon"

	// AuthHint is appended to an ESL auth failure: the password is the one
	// setting the default install cannot guess.
	AuthHint = "set the event_socket password: vd config:set plugins/fswapi FREESWITCH_ESL_PASSWORD=<password>"
)

// LoadConfig reads the environment.
func LoadConfig() (Config, error) {
	cfg := Config{
		Addr:          envOr("FREESWITCH_API_ADDR", DefaultAddr),
		ESLAddr:       envOr("FREESWITCH_ESL_ADDR", DefaultESLAddr),
		ESLPassword:   envOr("FREESWITCH_ESL_PASSWORD", DefaultESLPassword),
		ESLTimeout:    5 * time.Second,
		Actions:       os.Getenv("FREESWITCH_ACTIONS"),
		ActionSources: os.Getenv("FREESWITCH_ACTION_SOURCES"),
	}

	if raw := os.Getenv("FREESWITCH_ESL_TIMEOUT"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			return Config{}, fmt.Errorf("invalid FREESWITCH_ESL_TIMEOUT %q (want e.g. 5s)", raw)
		}

		cfg.ESLTimeout = d
	}

	return cfg, nil
}

// ActionSourcePrefixes is who may POST an action.
//
// Why a source check at all: the controller's PAT proxy is the authenticated
// door, but this pod sits on voodu0 next to every other container, and any of
// them could POST /actions/hupall straight to it, PAT or not. The proxy runs on
// the HOST, so its requests arrive from the voodu0 gateway; by default only
// that address (plus loopback, for a local test) may run an action. Reads stay
// open — they are what the hep3 reader already exposes the same way.
func (c Config) ActionSourcePrefixes(routeFile string) ([]netip.Prefix, error) {
	if strings.TrimSpace(c.ActionSources) != "" {
		var out []netip.Prefix

		for _, raw := range strings.Split(c.ActionSources, ",") {
			raw = strings.TrimSpace(raw)
			if raw == "" {
				continue
			}

			p, err := netip.ParsePrefix(raw)
			if err != nil {
				return nil, fmt.Errorf("invalid FREESWITCH_ACTION_SOURCES entry %q: %w", raw, err)
			}

			out = append(out, p)
		}

		return out, nil
	}

	out := []netip.Prefix{
		netip.MustParsePrefix("127.0.0.1/32"),
		netip.MustParsePrefix("::1/128"),
	}

	gw, err := DefaultGateway(routeFile)
	if err != nil {
		return out, fmt.Errorf("no default gateway (%w) — actions accepted from loopback only", err)
	}

	return append(out, netip.PrefixFrom(gw, 32)), nil
}

// DefaultGateway reads the IPv4 default route from /proc/net/route (the
// voodu0 bridge gateway inside a container — the host, where the controller
// runs). Fields are hex, little-endian.
func DefaultGateway(routeFile string) (netip.Addr, error) {
	f, err := os.Open(routeFile) //nolint:gosec // fixed system path, overridable only in tests
	if err != nil {
		return netip.Addr{}, err
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)

	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 3 || fields[1] != "00000000" {
			continue
		}

		raw, err := hex.DecodeString(fields[2])
		if err != nil || len(raw) != 4 {
			continue
		}

		var b [4]byte
		binary.BigEndian.PutUint32(b[:], binary.LittleEndian.Uint32(raw))

		addr := netip.AddrFrom4(b)
		if !addr.IsUnspecified() {
			return addr, nil
		}
	}

	return netip.Addr{}, errors.New("no default route in " + routeFile)
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}

	return def
}
