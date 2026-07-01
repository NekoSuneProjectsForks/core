package webrtc

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/google/uuid"
	pion "github.com/pion/webrtc/v4"
)

// whipChannel represents a single active WHIP publishing session for a
// resource. Only one publisher may be active per resource at a time, the
// same restriction the rtmp and srt packages enforce.
type whipChannel struct {
	resource  string
	sessionID string

	pc *pion.PeerConnection

	videoPort, audioPort uint16
	videoSender          *udpSender
	audioSender          *udpSender

	tracker *sessionTracker
}

func (ch *whipChannel) Close() {
	if ch.pc != nil {
		ch.pc.Close()
	}

	if ch.videoSender != nil {
		ch.videoSender.Close()
	}

	if ch.audioSender != nil {
		ch.audioSender.Close()
	}

	if ch.tracker != nil {
		ch.tracker.Close()
	}
}

// WHIP handles a new WHIP publish request. offer is the SDP offer sent by
// the publisher; the returned answer SDP already contains the fully
// gathered ICE candidates (non-trickle).
func (s *server) WHIP(resource, token, offer string) (string, string, error) {
	if err := s.checkToken(token); err != nil {
		return "", "", err
	}

	s.whipLock.Lock()
	if _, exists := s.whipChannels[resource]; exists {
		s.whipLock.Unlock()
		return "", "", fmt.Errorf("resource %s is already publishing", resource)
	}
	// Reserve the slot immediately so concurrent POSTs for the same
	// resource can't race past this point.
	s.whipChannels[resource] = &whipChannel{resource: resource}
	s.whipLock.Unlock()

	answer, ch, err := s.startWHIP(resource, offer)
	if err != nil {
		s.whipLock.Lock()
		delete(s.whipChannels, resource)
		s.whipLock.Unlock()
		return "", "", err
	}

	s.whipLock.Lock()
	s.whipChannels[resource] = ch
	s.whipLock.Unlock()

	return answer, ch.sessionID, nil
}

func (s *server) startWHIP(resource, offer string) (string, *whipChannel, error) {
	videoPort, err := s.portAlloc.Allocate()
	if err != nil {
		return "", nil, fmt.Errorf("allocating video port: %w", err)
	}

	audioPort, err := s.portAlloc.Allocate()
	if err != nil {
		s.portAlloc.Release(videoPort)
		return "", nil, fmt.Errorf("allocating audio port: %w", err)
	}

	release := func() {
		s.portAlloc.Release(videoPort)
		s.portAlloc.Release(audioPort)
	}

	if err := probeFreeUDPPort(s.relayAddress, videoPort); err != nil {
		release()
		return "", nil, fmt.Errorf("video relay port unavailable: %w", err)
	}

	if err := probeFreeUDPPort(s.relayAddress, audioPort); err != nil {
		release()
		return "", nil, fmt.Errorf("audio relay port unavailable: %w", err)
	}

	if _, err := writeWHIPInputSDP(s.sdpPath, resource, s.relayAddress, videoPort, audioPort); err != nil {
		release()
		return "", nil, err
	}

	videoSender, err := newUDPSender(s.relayAddress, videoPort)
	if err != nil {
		release()
		removeWHIPInputSDP(s.sdpPath, resource)
		return "", nil, fmt.Errorf("opening video relay: %w", err)
	}

	audioSender, err := newUDPSender(s.relayAddress, audioPort)
	if err != nil {
		videoSender.Close()
		release()
		removeWHIPInputSDP(s.sdpPath, resource)
		return "", nil, fmt.Errorf("opening audio relay: %w", err)
	}

	pc, err := s.newPeerConnection()
	if err != nil {
		videoSender.Close()
		audioSender.Close()
		release()
		removeWHIPInputSDP(s.sdpPath, resource)
		return "", nil, fmt.Errorf("creating peer connection: %w", err)
	}

	if _, err := pc.AddTransceiverFromKind(pion.RTPCodecTypeVideo, pion.RTPTransceiverInit{Direction: pion.RTPTransceiverDirectionRecvonly}); err != nil {
		pc.Close()
		videoSender.Close()
		audioSender.Close()
		release()
		removeWHIPInputSDP(s.sdpPath, resource)
		return "", nil, fmt.Errorf("adding video transceiver: %w", err)
	}

	if _, err := pc.AddTransceiverFromKind(pion.RTPCodecTypeAudio, pion.RTPTransceiverInit{Direction: pion.RTPTransceiverDirectionRecvonly}); err != nil {
		pc.Close()
		videoSender.Close()
		audioSender.Close()
		release()
		removeWHIPInputSDP(s.sdpPath, resource)
		return "", nil, fmt.Errorf("adding audio transceiver: %w", err)
	}

	sessionID := uuid.New().String()

	ch := &whipChannel{
		resource:    resource,
		sessionID:   sessionID,
		pc:          pc,
		videoPort:   videoPort,
		audioPort:   audioPort,
		videoSender: videoSender,
		audioSender: audioSender,
		tracker:     newSessionTracker(resource, s.collector),
	}

	addr := net.JoinHostPort(s.relayAddress, "0")
	s.collector.RegisterAndActivate(resource, resource, "publish:"+resource, addr)

	pc.OnTrack(func(track *pion.TrackRemote, receiver *pion.RTPReceiver) {
		sender := ch.audioSender
		if track.Kind() == pion.RTPCodecTypeVideo {
			sender = ch.videoSender
		}

		for {
			packet, _, err := track.ReadRTP()
			if err != nil {
				return
			}

			b, err := packet.Marshal()
			if err != nil {
				continue
			}

			if err := sender.Write(b); err != nil {
				return
			}

			ch.tracker.AddRx(len(b))
		}
	})

	pc.OnConnectionStateChange(func(state pion.PeerConnectionState) {
		if state == pion.PeerConnectionStateFailed || state == pion.PeerConnectionStateClosed || state == pion.PeerConnectionStateDisconnected {
			s.whipLock.Lock()
			if existing, ok := s.whipChannels[resource]; ok && existing.sessionID == sessionID {
				delete(s.whipChannels, resource)
			}
			s.whipLock.Unlock()

			ch.Close()
			s.portAlloc.Release(videoPort)
			s.portAlloc.Release(audioPort)
			removeWHIPInputSDP(s.sdpPath, resource)
			s.collector.Unregister(resource)
		}
	})

	if err := pc.SetRemoteDescription(pion.SessionDescription{Type: pion.SDPTypeOffer, SDP: offer}); err != nil {
		pc.Close()
		videoSender.Close()
		audioSender.Close()
		release()
		removeWHIPInputSDP(s.sdpPath, resource)
		return "", nil, fmt.Errorf("setting remote description: %w", err)
	}

	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		pc.Close()
		videoSender.Close()
		audioSender.Close()
		release()
		removeWHIPInputSDP(s.sdpPath, resource)
		return "", nil, fmt.Errorf("creating answer: %w", err)
	}

	gatherComplete := pion.GatheringCompletePromise(pc)

	if err := pc.SetLocalDescription(answer); err != nil {
		pc.Close()
		videoSender.Close()
		audioSender.Close()
		release()
		removeWHIPInputSDP(s.sdpPath, resource)
		return "", nil, fmt.Errorf("setting local description: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	select {
	case <-gatherComplete:
	case <-ctx.Done():
		pc.Close()
		videoSender.Close()
		audioSender.Close()
		release()
		removeWHIPInputSDP(s.sdpPath, resource)
		return "", nil, fmt.Errorf("timed out waiting for ICE gathering to complete")
	}

	return pc.LocalDescription().SDP, ch, nil
}

// WHIPDelete ends the given WHIP publishing session.
func (s *server) WHIPDelete(resource, sessionID string) error {
	s.whipLock.Lock()
	ch, ok := s.whipChannels[resource]
	if !ok || ch.sessionID != sessionID {
		s.whipLock.Unlock()
		return fmt.Errorf("no such session")
	}
	delete(s.whipChannels, resource)
	s.whipLock.Unlock()

	ch.Close()
	s.portAlloc.Release(ch.videoPort)
	s.portAlloc.Release(ch.audioPort)
	removeWHIPInputSDP(s.sdpPath, resource)
	s.collector.Unregister(resource)

	return nil
}
