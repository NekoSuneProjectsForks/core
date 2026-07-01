// Package webrtc provides a WHIP (ingest) and WHEP (egress) bridge.
//
// It terminates the actual WebRTC transport (ICE/DTLS/SRTP) and relays the
// raw H264/Opus RTP onto loopback UDP, following the same pattern as the
// rtmp and srt packages: ffmpeg processes connect to this server as a
// client (either reading a generated SDP for WHIP input, or sending RTP to
// a fixed local port for WHEP output). This package never encodes,
// decodes, or inspects media payloads; ffmpeg keeps doing all of that.
package webrtc

import (
	"time"

	"github.com/datarhei/core/v16/log"
	"github.com/datarhei/core/v16/session"
)

// Config for a new WebRTC (WHIP/WHEP) server
type Config struct {
	// Logger. Optional.
	Logger log.Logger

	Collector session.Collector

	// A token that needs to be added to the URL as query string in
	// order to publish (WHIP) or play (WHEP) a resource. The key for
	// the query parameter is "token". Optional. By default no token
	// is required.
	Token string

	// ICEUDPMuxPort is the single UDP port used for all ICE/media traffic
	// to/from WHIP/WHEP peers, keeping the firewall/Docker story to "open
	// one UDP port" instead of a wide ephemeral range.
	ICEUDPMuxPort int

	// ICEServers is a list of STUN/TURN server URLs handed to clients in
	// the SDP answer, e.g. "stun:stun.l.google.com:19302". Optional.
	ICEServers []string

	// NAT1To1IPs are the public IP(s) to advertise as host candidates.
	// Required when running behind Docker port-mapping/NAT so the remote
	// peer knows where to actually send media.
	NAT1To1IPs []string

	// RelayAddress is the loopback address ffmpeg processes use to
	// exchange RTP with this server, e.g. "127.0.0.1".
	RelayAddress string

	// RelayPortMin/Max is the range of local UDP ports handed out for the
	// ffmpeg-facing side of each WHIP/WHEP resource.
	RelayPortMin uint16
	RelayPortMax uint16

	// SDPPath is the directory where the generated ffmpeg-facing SDP
	// files for WHIP resources are written. Defaults to the OS temp dir.
	SDPPath string

	// ConnectionIdleTimeout is the time after which an inactive session
	// gets closed. Default is no timeout.
	ConnectionIdleTimeout time.Duration
}
