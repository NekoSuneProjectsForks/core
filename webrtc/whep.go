package webrtc

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/google/uuid"
	pion "github.com/pion/webrtc/v4"
)

// whepChannel represents a resource that an ffmpeg egress process sends RTP
// to on fixed, pre-reserved loopback ports. Any number of WHEP viewers can
// subscribe concurrently; every received RTP packet is fanned out to all of
// them.
type whepChannel struct {
	resource             string
	videoPort, audioPort uint16
	videoReceiver        *udpReceiver
	audioReceiver        *udpReceiver

	lock     sync.RWMutex
	sessions map[string]*whepSession
}

type whepSession struct {
	pc         *pion.PeerConnection
	videoTrack *pion.TrackLocalStaticRTP
	audioTrack *pion.TrackLocalStaticRTP
	tracker    *sessionTracker
}

func (ch *whepChannel) addSession(id string, s *whepSession) {
	ch.lock.Lock()
	ch.sessions[id] = s
	ch.lock.Unlock()
}

func (ch *whepChannel) removeSession(id string) {
	ch.lock.Lock()
	s, ok := ch.sessions[id]
	delete(ch.sessions, id)
	ch.lock.Unlock()

	if ok {
		s.pc.Close()
		s.tracker.Close()
	}
}

func (ch *whepChannel) fanOutVideo(b []byte) {
	ch.lock.RLock()
	defer ch.lock.RUnlock()

	for _, s := range ch.sessions {
		if n, err := s.videoTrack.Write(b); err == nil {
			s.tracker.AddTx(n)
		}
	}
}

func (ch *whepChannel) fanOutAudio(b []byte) {
	ch.lock.RLock()
	defer ch.lock.RUnlock()

	for _, s := range ch.sessions {
		if n, err := s.audioTrack.Write(b); err == nil {
			s.tracker.AddTx(n)
		}
	}
}

func (ch *whepChannel) Close() {
	ch.lock.Lock()
	sessions := ch.sessions
	ch.sessions = map[string]*whepSession{}
	ch.lock.Unlock()

	for _, s := range sessions {
		s.pc.Close()
		s.tracker.Close()
	}

	if ch.videoReceiver != nil {
		ch.videoReceiver.Close()
	}

	if ch.audioReceiver != nil {
		ch.audioReceiver.Close()
	}
}

// ReserveWHEP ensures the loopback relay ports for a WHEP resource are
// open and returns them. The ports are a deterministic function of the
// resource name (see derivePort) rather than allocated from a pool, so a
// UI can independently compute the same rtp://host:port ffmpeg output
// address without any round-trip - this call only needs to happen once,
// eagerly or lazily (WHEP() calls it automatically on first access), and
// stays valid across ffmpeg restarts/reconnects for the resource's whole
// lifetime.
func (s *server) ReserveWHEP(resource string) (string, uint16, uint16, error) {
	s.whepLock.Lock()
	defer s.whepLock.Unlock()

	return s.reserveWHEPLocked(resource)
}

func (s *server) reserveWHEPLocked(resource string) (string, uint16, uint16, error) {
	if ch, ok := s.whepChannels[resource]; ok {
		return s.relayAddress, ch.videoPort, ch.audioPort, nil
	}

	videoPort := derivePort(s.portAlloc.min, s.portAlloc.max, "whep-video-", resource)
	audioPort := derivePort(s.portAlloc.min, s.portAlloc.max, "whep-audio-", resource)

	if videoPort == audioPort {
		return "", 0, 0, fmt.Errorf("derived video/audio ports collide for resource %s, pick a different resource name", resource)
	}

	for other, ch := range s.whepChannels {
		if ch.videoPort == videoPort || ch.videoPort == audioPort || ch.audioPort == videoPort || ch.audioPort == audioPort {
			return "", 0, 0, fmt.Errorf("derived relay ports for resource %s collide with resource %s, pick a different resource name", resource, other)
		}
	}

	ch := &whepChannel{
		resource:  resource,
		videoPort: videoPort,
		audioPort: audioPort,
		sessions:  map[string]*whepSession{},
	}

	var err error

	ch.videoReceiver, err = newUDPReceiver(s.relayAddress, videoPort, ch.fanOutVideo)
	if err != nil {
		return "", 0, 0, fmt.Errorf("opening video relay: %w", err)
	}

	ch.audioReceiver, err = newUDPReceiver(s.relayAddress, audioPort, ch.fanOutAudio)
	if err != nil {
		ch.videoReceiver.Close()
		return "", 0, 0, fmt.Errorf("opening audio relay: %w", err)
	}

	s.whepChannels[resource] = ch

	return s.relayAddress, videoPort, audioPort, nil
}

// ReleaseWHEP tears down a reserved WHEP resource and disconnects any
// active viewers.
func (s *server) ReleaseWHEP(resource string) {
	s.whepLock.Lock()
	ch, ok := s.whepChannels[resource]
	delete(s.whepChannels, resource)
	s.whepLock.Unlock()

	if !ok {
		return
	}

	ch.Close()
}

// WHEP handles a new WHEP play request. The resource's relay ports are
// reserved automatically on first access if they aren't already.
func (s *server) WHEP(resource, token, offer string) (string, string, error) {
	if err := s.checkToken(token); err != nil {
		return "", "", err
	}

	s.whepLock.Lock()
	_, _, _, err := s.reserveWHEPLocked(resource)
	ch := s.whepChannels[resource]
	s.whepLock.Unlock()

	if err != nil {
		return "", "", err
	}

	pc, err := s.newPeerConnection()
	if err != nil {
		return "", "", fmt.Errorf("creating peer connection: %w", err)
	}

	videoTrack, err := pion.NewTrackLocalStaticRTP(pion.RTPCodecCapability{
		MimeType:    pion.MimeTypeH264,
		ClockRate:   90000,
		SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f",
	}, "video", resource)
	if err != nil {
		pc.Close()
		return "", "", fmt.Errorf("creating video track: %w", err)
	}

	audioTrack, err := pion.NewTrackLocalStaticRTP(pion.RTPCodecCapability{
		MimeType:  pion.MimeTypeOpus,
		ClockRate: 48000,
		Channels:  2,
	}, "audio", resource)
	if err != nil {
		pc.Close()
		return "", "", fmt.Errorf("creating audio track: %w", err)
	}

	if _, err := pc.AddTrack(videoTrack); err != nil {
		pc.Close()
		return "", "", fmt.Errorf("adding video track: %w", err)
	}

	if _, err := pc.AddTrack(audioTrack); err != nil {
		pc.Close()
		return "", "", fmt.Errorf("adding audio track: %w", err)
	}

	sessionID := uuid.New().String()

	sess := &whepSession{
		pc:         pc,
		videoTrack: videoTrack,
		audioTrack: audioTrack,
		tracker:    newSessionTracker(resource, s.collector),
	}

	addr := net.JoinHostPort(s.relayAddress, "0")
	s.collector.RegisterAndActivate(resource+"/"+sessionID, resource, "play:"+resource, addr)

	pc.OnConnectionStateChange(func(state pion.PeerConnectionState) {
		if state == pion.PeerConnectionStateFailed || state == pion.PeerConnectionStateClosed || state == pion.PeerConnectionStateDisconnected {
			ch.removeSession(sessionID)
			s.collector.Unregister(resource + "/" + sessionID)
		}
	})

	if err := pc.SetRemoteDescription(pion.SessionDescription{Type: pion.SDPTypeOffer, SDP: offer}); err != nil {
		pc.Close()
		sess.tracker.Close()
		return "", "", fmt.Errorf("setting remote description: %w", err)
	}

	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		pc.Close()
		sess.tracker.Close()
		return "", "", fmt.Errorf("creating answer: %w", err)
	}

	gatherComplete := pion.GatheringCompletePromise(pc)

	if err := pc.SetLocalDescription(answer); err != nil {
		pc.Close()
		sess.tracker.Close()
		return "", "", fmt.Errorf("setting local description: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	select {
	case <-gatherComplete:
	case <-ctx.Done():
		pc.Close()
		sess.tracker.Close()
		return "", "", fmt.Errorf("timed out waiting for ICE gathering to complete")
	}

	ch.addSession(sessionID, sess)

	return pc.LocalDescription().SDP, sessionID, nil
}

// WHEPDelete ends the given WHEP viewing session.
func (s *server) WHEPDelete(resource, sessionID string) error {
	s.whepLock.RLock()
	ch, ok := s.whepChannels[resource]
	s.whepLock.RUnlock()

	if !ok {
		return fmt.Errorf("no such resource")
	}

	ch.removeSession(sessionID)
	s.collector.Unregister(resource + "/" + sessionID)

	return nil
}
