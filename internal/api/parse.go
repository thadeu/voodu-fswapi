package api

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// Status is `status` parsed. Raw keeps the original text: FreeSWITCH builds
// differ in wording, and a field this parser misses must still be visible.
type Status struct {
	Up                 bool    `json:"up"`
	Uptime             string  `json:"uptime"`
	UptimeSeconds      int64   `json:"uptime_seconds"`
	Version            string  `json:"version"`
	SessionsSinceStart int64   `json:"sessions_since_startup"`
	Sessions           int64   `json:"sessions"`
	SessionsPeak       int64   `json:"sessions_peak"`
	SessionsPeak5Min   int64   `json:"sessions_peak_5min"`
	SessionsPerSec     int64   `json:"sessions_per_sec"`
	SessionsPerSecMax  int64   `json:"sessions_per_sec_max"`
	SessionsPerSecPeak int64   `json:"sessions_per_sec_peak"`
	SessionsMax        int64   `json:"sessions_max"`
	IdleCPUMin         float64 `json:"idle_cpu_min"`
	IdleCPU            float64 `json:"idle_cpu"`
	Raw                string  `json:"raw"`
}

var (
	reUptimePart = regexp.MustCompile(`(\d+) (year|day|hour|minute|second)s?`)
	reVersion    = regexp.MustCompile(`\(Version ([^)]+)\)`)
	reSinceStart = regexp.MustCompile(`(\d+) session\(s\) since startup`)
	reSessions   = regexp.MustCompile(`(\d+) session\(s\) - peak (\d+), last 5min (\d+)`)
	reSPS        = regexp.MustCompile(`(\d+) session\(s\) per Sec out of max (\d+), peak (\d+)`)
	reMax        = regexp.MustCompile(`(\d+) session\(s\) max`)
	reIdleCPU    = regexp.MustCompile(`min idle cpu ([\d.]+)/([\d.]+)`)
	reTotal      = regexp.MustCompile(`(\d+) total\.`)
)

var uptimeUnit = map[string]int64{"year": 365 * 86400, "day": 86400, "hour": 3600, "minute": 60, "second": 1}

// ParseStatus reads the `status` api reply.
func ParseStatus(raw string) Status {
	s := Status{Raw: raw}

	firstLine, _, _ := strings.Cut(raw, "\n")
	s.Up = strings.HasPrefix(firstLine, "UP")
	s.Uptime = strings.TrimSpace(strings.TrimPrefix(firstLine, "UP"))

	for _, m := range reUptimePart.FindAllStringSubmatch(firstLine, -1) {
		s.UptimeSeconds += atoi(m[1]) * uptimeUnit[m[2]]
	}

	if m := reVersion.FindStringSubmatch(raw); m != nil {
		s.Version = strings.TrimSpace(m[1])
	}

	if m := reSinceStart.FindStringSubmatch(raw); m != nil {
		s.SessionsSinceStart = atoi(m[1])
	}

	if m := reSessions.FindStringSubmatch(raw); m != nil {
		s.Sessions, s.SessionsPeak, s.SessionsPeak5Min = atoi(m[1]), atoi(m[2]), atoi(m[3])
	}

	if m := reSPS.FindStringSubmatch(raw); m != nil {
		s.SessionsPerSec, s.SessionsPerSecMax, s.SessionsPerSecPeak = atoi(m[1]), atoi(m[2]), atoi(m[3])
	}

	if m := reMax.FindStringSubmatch(raw); m != nil {
		s.SessionsMax = atoi(m[1])
	}

	if m := reIdleCPU.FindStringSubmatch(raw); m != nil {
		s.IdleCPUMin, _ = strconv.ParseFloat(m[1], 64)
		s.IdleCPU, _ = strconv.ParseFloat(m[2], 64)
	}

	return s
}

// ParseCount reads a `show <x> count` reply ("\n3 total.\n").
func ParseCount(raw string) (int64, error) {
	m := reTotal.FindStringSubmatch(raw)
	if m == nil {
		return 0, fmt.Errorf("unexpected count reply %q", strings.TrimSpace(raw))
	}

	return atoi(m[1]), nil
}

// Rows is a `show <x> as json` reply: {"row_count": N, "rows": [...]}.
// FreeSWITCH omits "rows" when the count is 0; Rows is then an empty list,
// never null, so consumers can iterate without a nil check.
type Rows struct {
	Count int64            `json:"count"`
	Rows  []map[string]any `json:"rows"`
}

// ParseRows reads a `show <x> as json` reply.
func ParseRows(raw string) (Rows, error) {
	var payload struct {
		RowCount int64            `json:"row_count"`
		Rows     []map[string]any `json:"rows"`
	}

	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &payload); err != nil {
		return Rows{}, fmt.Errorf("unexpected json reply: %w", err)
	}

	if payload.Rows == nil {
		payload.Rows = []map[string]any{}
	}

	return Rows{Count: payload.RowCount, Rows: payload.Rows}, nil
}

// SofiaEntry is one line of `sofia xmlstatus`: a profile, gateway or alias.
type SofiaEntry struct {
	Kind  string `json:"kind"`
	Name  string `json:"name"`
	Type  string `json:"type"`
	Data  string `json:"data"`
	State string `json:"state"`
}

// ParseSofia reads `sofia xmlstatus`. The reply declares ISO-8859-1, which
// encoding/xml refuses by default; the fields we read are ASCII (names, SIP
// URIs, states), so the bytes pass through as they are.
func ParseSofia(raw string) ([]SofiaEntry, error) {
	var doc struct {
		Items []struct {
			XMLName xml.Name
			Name    string `xml:"name"`
			Type    string `xml:"type"`
			Data    string `xml:"data"`
			State   string `xml:"state"`
		} `xml:",any"`
	}

	dec := xml.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.CharsetReader = func(_ string, input io.Reader) (io.Reader, error) { return input, nil }

	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("unexpected sofia reply: %w", err)
	}

	out := make([]SofiaEntry, 0, len(doc.Items))

	for _, it := range doc.Items {
		out = append(out, SofiaEntry{
			Kind: it.XMLName.Local, Name: it.Name, Type: it.Type, Data: it.Data, State: strings.TrimSpace(it.State),
		})
	}

	return out, nil
}

// replyError turns a FreeSWITCH "-ERR ..." reply into an error. Every other
// reply ("+OK ...", or plain output) is success.
func replyError(raw string) error {
	trimmed := strings.TrimSpace(raw)

	if strings.HasPrefix(trimmed, "-ERR") {
		return fmt.Errorf("%s", strings.TrimSpace(strings.TrimPrefix(trimmed, "-ERR")))
	}

	return nil
}

func atoi(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)

	return n
}
