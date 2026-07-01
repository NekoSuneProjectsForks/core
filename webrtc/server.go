package webrtc

import (
	"fmt"
	"os"
	"sync"

	"github.com/datarhei/core/v16/log"
	"github.com/datarhei/core/v16/session"

	"github.com/pion/ice/v4"
	pion "github.com/pion/webrtc/v4"
)

// Channels lists the currently active WHIP (publishing), WHEP (playing),
// and WHIPClient (relaying out to a remote WHIP server) resources.
type Channels struct {
	WHIP       []string
	WHEP       []string
	WHIPClient []string
}

// Server represents a WHIP/WHEP server
type Server interface {
	// WHIP handles a new WHIP publish request for the given resource. It
	// returns the SDP answer and a session id to be used for the Location
	// header of the HTTP response and for a later WHIPDelete call.
	WHIP(resource, token, offer string) (answer string, sessionID string, err error)

	// WHIPDelete ends a WHIP publishing session.
	WHIPDelete(resource, sessionID string) error

	// WHEP handles a new WHEP play request for the given resource. It
	// returns the SDP answer and a session id to be used for the Location
	// header of the HTTP response and for a later WHEPDelete call.
	WHEP(resource, token, offer string) (answer string, sessionID string, err error)

	// WHEPDelete ends a WHEP playing session.
	WHEPDelete(resource, sessionID string) error

	// ReserveWHEP reserves fixed loopback relay ports for a WHEP resource,
	// so an ffmpeg egress process's output address can be configured
	// before ffmpeg ever starts. Idempotent: safe to call again for an
	// already-reserved resource, it just returns the existing ports.
	ReserveWHEP(resource string) (relayAddress string, videoPort, audioPort uint16, err error)

	// ReleaseWHEP tears down a reserved WHEP resource and disconnects any
	// active viewers.
	ReleaseWHEP(resource string)

	// PublishWHIP relays a resource's ffmpeg output to a remote WHIP server
	// (this instance acts as the WHIP client/publisher). Idempotent: safe
	// to call again for an already-published resource, it just returns the
	// existing relay ports.
	PublishWHIP(resource, remoteURL, token string) (relayAddress string, videoPort, audioPort uint16, err error)

	// UnpublishWHIP ends a remote WHIP publish.
	UnpublishWHIP(resource string)

	// Close closes all sessions and releases all resources.
	Close()

	// Channels returns the list of currently active resources.
	Channels() Channels
}

type server struct {
	token     string
	logger    log.Logger
	collector session.Collector

	api       *pion.API
	iceServer []pion.ICEServer
	portAlloc *portAllocator

	relayAddress string
	sdpPath      string

	whipLock     sync.RWMutex
	whipChannels map[string]*whipChannel

	whepLock     sync.RWMutex
	whepChannels map[string]*whepChannel

	whipClientLock     sync.RWMutex
	whipClientChannels map[string]*whipClientChannel
}

// New creates a new WHIP/WHEP server according to the given config
func New(config Config) (Server, error) {
	if config.Logger == nil {
		config.Logger = log.New("")
	}

	if len(config.RelayAddress) == 0 {
		config.RelayAddress = "127.0.0.1"
	}

	if config.RelayPortMin == 0 {
		config.RelayPortMin = 20000
	}

	if config.RelayPortMax == 0 {
		config.RelayPortMax = 20500
	}

	if len(config.SDPPath) == 0 {
		config.SDPPath = os.TempDir()
	}

	s := &server{
		token:        config.Token,
		logger:       config.Logger,
		collector:    config.Collector,
		portAlloc:    newPortAllocator(config.RelayPortMin, config.RelayPortMax),
		relayAddress: config.RelayAddress,
		sdpPath:      config.SDPPath,
		whipChannels:       map[string]*whipChannel{},
		whepChannels:       map[string]*whepChannel{},
		whipClientChannels: map[string]*whipClientChannel{},
	}

	if s.collector == nil {
		s.collector = session.NewNullCollector()
	}

	for _, url := range config.ICEServers {
		s.iceServer = append(s.iceServer, pion.ICEServer{URLs: []string{url}})
	}

	mediaEngine := &pion.MediaEngine{}

	// Deliberately narrow codec support to H264+Opus only (instead of
	// registering pion's full default codec set). This keeps the
	// ffmpeg-facing side of the bridge deterministic: whatever a WHIP
	// publisher negotiates, and whatever a WHEP viewer is offered, is
	// always the same fixed pair ffmpeg is configured to decode/encode.
	if err := mediaEngine.RegisterCodec(pion.RTPCodecParameters{
		RTPCodecCapability: pion.RTPCodecCapability{
			MimeType:    pion.MimeTypeH264,
			ClockRate:   90000,
			SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f",
		},
		PayloadType: h264PayloadType,
	}, pion.RTPCodecTypeVideo); err != nil {
		return nil, fmt.Errorf("registering H264 codec: %w", err)
	}

	if err := mediaEngine.RegisterCodec(pion.RTPCodecParameters{
		RTPCodecCapability: pion.RTPCodecCapability{
			MimeType:  pion.MimeTypeOpus,
			ClockRate: 48000,
			Channels:  2,
		},
		PayloadType: opusPayloadType,
	}, pion.RTPCodecTypeAudio); err != nil {
		return nil, fmt.Errorf("registering Opus codec: %w", err)
	}

	settingEngine := pion.SettingEngine{}

	if config.ICEUDPMuxPort > 0 {
		udpMux, err := ice.NewMultiUDPMuxFromPort(config.ICEUDPMuxPort)
		if err != nil {
			return nil, fmt.Errorf("creating ICE UDP mux on port %d: %w", config.ICEUDPMuxPort, err)
		}

		settingEngine.SetICEUDPMux(udpMux)
	}

	if len(config.NAT1To1IPs) != 0 {
		settingEngine.SetNAT1To1IPs(config.NAT1To1IPs, pion.ICECandidateTypeHost)
	}

	s.api = pion.NewAPI(pion.WithMediaEngine(mediaEngine), pion.WithSettingEngine(settingEngine))

	return s, nil
}

func (s *server) newPeerConnection() (*pion.PeerConnection, error) {
	return s.api.NewPeerConnection(pion.Configuration{
		ICEServers: s.iceServer,
	})
}

// checkToken validates the token query parameter the same way rtmp/srt do.
func (s *server) checkToken(token string) error {
	if len(s.token) == 0 {
		return nil
	}

	if s.token != token {
		return fmt.Errorf("invalid token")
	}

	return nil
}

func (s *server) Close() {
	s.whipLock.Lock()
	for _, ch := range s.whipChannels {
		ch.Close()
	}
	s.whipChannels = map[string]*whipChannel{}
	s.whipLock.Unlock()

	s.whepLock.Lock()
	for _, ch := range s.whepChannels {
		ch.Close()
	}
	s.whepChannels = map[string]*whepChannel{}
	s.whepLock.Unlock()

	s.whipClientLock.Lock()
	for _, ch := range s.whipClientChannels {
		ch.Close()
	}
	s.whipClientChannels = map[string]*whipClientChannel{}
	s.whipClientLock.Unlock()
}

func (s *server) Channels() Channels {
	channels := Channels{}

	s.whipLock.RLock()
	for id := range s.whipChannels {
		channels.WHIP = append(channels.WHIP, id)
	}
	s.whipLock.RUnlock()

	s.whepLock.RLock()
	for id := range s.whepChannels {
		channels.WHEP = append(channels.WHEP, id)
	}
	s.whepLock.RUnlock()

	s.whipClientLock.RLock()
	for id := range s.whipClientChannels {
		channels.WHIPClient = append(channels.WHIPClient, id)
	}
	s.whipClientLock.RUnlock()

	return channels
}
