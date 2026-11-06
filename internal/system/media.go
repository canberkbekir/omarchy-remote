package system

import (
	"errors"
	"hash/fnv"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/canberkbekir/omarchy-remote/internal/network"
	"github.com/canberkbekir/omarchy-remote/internal/run"
)

const maxArtBytes = 8 << 20

type Track struct {
	Title    string  `json:"title"`
	Artist   string  `json:"artist"`
	Player   string  `json:"player"`
	Art      string  `json:"art"`
	Position float64 `json:"position"`
	Length   float64 `json:"length"`
	Playing  bool    `json:"playing"`
}

type Output struct {
	Name   string `json:"name"`
	Label  string `json:"label"`
	Active bool   `json:"active"`
}

const trackFormat = "{{status}}\t{{playerName}}\t{{title}}\t{{artist}}\t{{mpris:artUrl}}\t{{position}}\t{{mpris:length}}"

// readTrack returns what the current media player is playing, and the raw
// art URL it reported, or nil when nothing is loaded.
func readTrack() (*Track, string) {
	text, err := run.Command("playerctl", "metadata", "--format", trackFormat)
	if err != nil {
		return nil, ""
	}
	fields := strings.Split(strings.TrimRight(text, "\n"), "\t")
	if len(fields) != 7 || fields[2] == "" || fields[0] == "Stopped" {
		return nil, ""
	}
	position, _ := strconv.ParseFloat(fields[5], 64)
	length, _ := strconv.ParseFloat(fields[6], 64)
	t := &Track{
		Playing:  fields[0] == "Playing",
		Player:   fields[1],
		Title:    fields[2],
		Artist:   fields[3],
		Position: position / 1e6,
		Length:   length / 1e6,
	}
	art := fields[4]
	// Served by /api/art, so the phone never contacts the cover's server.
	if strings.HasPrefix(art, "https://") || strings.HasPrefix(art, "file://") {
		t.Art = "/api/art?v=" + artVersion(art)
	}
	return t, art
}

// artVersion changes the cover URL when the cover changes, so the phone
// reloads it.
func artVersion(key string) string {
	h := fnv.New64a()
	h.Write([]byte(key))
	return strconv.FormatUint(h.Sum64(), 36)
}

var ErrNoArt = errors.New("no cover art")

// publicOnly keeps cover fetches away from this PC and the local network. It
// checks the address actually dialed, so redirects and DNS can't get around it.
func publicOnly(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	ip := net.ParseIP(host)
	if err != nil || ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || network.TailnetRange.Contains(ip) {
		return ErrNoArt
	}
	return nil
}

var artClient = &http.Client{
	Timeout: 5 * time.Second,
	Transport: &http.Transport{
		DialContext:       (&net.Dialer{Control: publicOnly}).DialContext,
		ForceAttemptHTTP2: true,
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" || len(via) >= 5 {
			return ErrNoArt
		}
		return nil
	},
}

func readArt(art string) ([]byte, error) {
	location, err := url.Parse(art)
	if err != nil {
		return nil, err
	}
	switch location.Scheme {
	case "file":
		info, err := os.Stat(location.Path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > maxArtBytes {
			return nil, ErrNoArt
		}
		return os.ReadFile(location.Path)
	case "https":
		resp, err := artClient.Get(location.String())
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, ErrNoArt
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, maxArtBytes+1))
		if err != nil || len(data) > maxArtBytes {
			return nil, ErrNoArt
		}
		return data, nil
	}
	return nil, ErrNoArt
}

var bracketName = regexp.MustCompile(`\[([^\]]+)\]`)

// outputLabel turns a sound card's long description into a short name.
func outputLabel(description string) string {
	if match := bracketName.FindStringSubmatch(description); match != nil {
		return match[1]
	}
	if strings.Contains(description, "HD Audio Controller") {
		return "PC audio jack"
	}
	for _, suffix := range []string{" Analog Stereo", " Digital Stereo (HDMI)", " Digital Stereo", " Stereo"} {
		description = strings.TrimSuffix(description, suffix)
	}
	return description
}
