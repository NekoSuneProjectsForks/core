package webrtc

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	pion "github.com/pion/webrtc/v4"
)

// whipClientChannel relays an ffmpeg egress process's local RTP to a single
// remote WHIP server, the mirror image of whepChannel: instead of fanning
// out to many browser WHEP viewers, it publishes to exactly one remote
// target via an outbound WebRTC session that this server itself offers.
type whipClientChannel struct {
	resource             string
	remoteURL            string
	videoPort, audioPort uint16
	videoReceiver        *udpReceiver
	audioReceiver        *udpReceiver

	pc          *pion.PeerConnection
	videoTrack  *pion.TrackLocalStaticRTP
	audioTrack  *pion.TrackLocalStaticRTP
	resourceURL string // the remote's Location header, used to unpublish
	tracker     *sessionTracker
}

func (ch *whipClientChannel) Close() {
	if ch.videoReceiver != nil {
		ch.videoReceiver.Close()
	}

	if ch.audioReceiver != nil {
		ch.audioReceiver.Close()
	}

	if ch.pc != nil {
		ch.pc.Close()
	}

	if ch.tracker != nil {
		ch.tracker.Close()
	}
}

func (ch *whipClientChannel) onVideoRTP(b []byte) {
	if n, err := ch.videoTrack.Write(b); err == nil {
		ch.tracker.AddTx(n)
	}
}

func (ch *whipClientChannel) onAudioRTP(b []byte) {
	if n, err := ch.audioTrack.Write(b); err == nil {
		ch.tracker.AddTx(n)
	}
}

// PublishWHIP establishes (or returns the existing) relay for publishing a
// resource's ffmpeg output to a remote WHIP server, e.g.
// "https://example.com/mystream/whip" (MediaMTX-style) or any RFC
// 9725-compliant endpoint. Idempotent: calling it again for an
// already-published resource just returns the existing relay ports.
func (s *server) PublishWHIP(resource, remoteURL, token string) (string, uint16, uint16, error) {
	s.whipClientLock.Lock()
	defer s.whipClientLock.Unlock()

	if ch, ok := s.whipClientChannels[resource]; ok {
		return s.relayAddress, ch.videoPort, ch.audioPort, nil
	}

	videoPort := derivePort(s.portAlloc.min, s.portAlloc.max, "whipclient-video-", resource)
	audioPort := derivePort(s.portAlloc.min, s.portAlloc.max, "whipclient-audio-", resource)

	if videoPort == audioPort {
		return "", 0, 0, fmt.Errorf("derived video/audio ports collide for resource %s, pick a different resource name", resource)
	}

	for other, ch := range s.whipClientChannels {
		if ch.videoPort == videoPort || ch.videoPort == audioPort || ch.audioPort == videoPort || ch.audioPort == audioPort {
			return "", 0, 0, fmt.Errorf("derived relay ports for resource %s collide with resource %s, pick a different resource name", resource, other)
		}
	}

	pc, err := s.newPeerConnection()
	if err != nil {
		return "", 0, 0, fmt.Errorf("creating peer connection: %w", err)
	}

	videoTrack, err := pion.NewTrackLocalStaticRTP(pion.RTPCodecCapability{
		MimeType:    pion.MimeTypeH264,
		ClockRate:   90000,
		SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f",
	}, "video", resource)
	if err != nil {
		pc.Close()
		return "", 0, 0, fmt.Errorf("creating video track: %w", err)
	}

	audioTrack, err := pion.NewTrackLocalStaticRTP(pion.RTPCodecCapability{
		MimeType:  pion.MimeTypeOpus,
		ClockRate: 48000,
		Channels:  2,
	}, "audio", resource)
	if err != nil {
		pc.Close()
		return "", 0, 0, fmt.Errorf("creating audio track: %w", err)
	}

	if _, err := pc.AddTrack(videoTrack); err != nil {
		pc.Close()
		return "", 0, 0, fmt.Errorf("adding video track: %w", err)
	}

	if _, err := pc.AddTrack(audioTrack); err != nil {
		pc.Close()
		return "", 0, 0, fmt.Errorf("adding audio track: %w", err)
	}

	offer, err := pc.CreateOffer(nil)
	if err != nil {
		pc.Close()
		return "", 0, 0, fmt.Errorf("creating offer: %w", err)
	}

	gatherComplete := pion.GatheringCompletePromise(pc)

	if err := pc.SetLocalDescription(offer); err != nil {
		pc.Close()
		return "", 0, 0, fmt.Errorf("setting local description: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	select {
	case <-gatherComplete:
	case <-ctx.Done():
		pc.Close()
		return "", 0, 0, fmt.Errorf("timed out waiting for ICE gathering to complete")
	}

	answerSDP, resourceURL, err := postWHIPOffer(ctx, remoteURL, token, pc.LocalDescription().SDP)
	if err != nil {
		pc.Close()
		return "", 0, 0, fmt.Errorf("publishing to %s: %w", remoteURL, err)
	}

	if err := pc.SetRemoteDescription(pion.SessionDescription{Type: pion.SDPTypeAnswer, SDP: answerSDP}); err != nil {
		pc.Close()
		return "", 0, 0, fmt.Errorf("setting remote description: %w", err)
	}

	ch := &whipClientChannel{
		resource:    resource,
		remoteURL:   remoteURL,
		videoPort:   videoPort,
		audioPort:   audioPort,
		pc:          pc,
		videoTrack:  videoTrack,
		audioTrack:  audioTrack,
		resourceURL: resourceURL,
		tracker:     newSessionTracker(resource, s.collector),
	}

	ch.videoReceiver, err = newUDPReceiver(s.relayAddress, videoPort, ch.onVideoRTP)
	if err != nil {
		pc.Close()
		ch.tracker.Close()
		return "", 0, 0, fmt.Errorf("opening video relay: %w", err)
	}

	ch.audioReceiver, err = newUDPReceiver(s.relayAddress, audioPort, ch.onAudioRTP)
	if err != nil {
		ch.videoReceiver.Close()
		pc.Close()
		ch.tracker.Close()
		return "", 0, 0, fmt.Errorf("opening audio relay: %w", err)
	}

	s.collector.RegisterAndActivate(resource, resource, "whip-publish:"+resource, remoteURL)

	s.whipClientChannels[resource] = ch

	return s.relayAddress, videoPort, audioPort, nil
}

// UnpublishWHIP tears down a remote WHIP publish, telling the remote server
// (via DELETE on its resource URL, per RFC 9725) that the session is over.
func (s *server) UnpublishWHIP(resource string) {
	s.whipClientLock.Lock()
	ch, ok := s.whipClientChannels[resource]
	delete(s.whipClientChannels, resource)
	s.whipClientLock.Unlock()

	if !ok {
		return
	}

	if len(ch.resourceURL) != 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		req, err := http.NewRequestWithContext(ctx, http.MethodDelete, ch.resourceURL, nil)
		if err == nil {
			http.DefaultClient.Do(req) //nolint:errcheck
		}
		cancel()
	}

	ch.Close()

	s.collector.Unregister(resource)
}

// postWHIPOffer performs the client side of the WHIP handshake (RFC 9725):
// POST the SDP offer, get back a 201 with the SDP answer and a Location
// header pointing at the session resource (used later to DELETE it).
func postWHIPOffer(ctx context.Context, remoteURL, token, offer string) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, remoteURL, bytes.NewReader([]byte(offer)))
	if err != nil {
		return "", "", err
	}

	req.Header.Set("Content-Type", "application/sdp")
	if len(token) != 0 {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return "", "", err
	}

	if res.StatusCode != http.StatusCreated && res.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("unexpected status %d: %s", res.StatusCode, strings.TrimSpace(string(body)))
	}

	resourceURL := ""

	if location := res.Header.Get("Location"); len(location) != 0 {
		base, err := url.Parse(remoteURL)
		if err == nil {
			if ref, err := url.Parse(location); err == nil {
				resourceURL = base.ResolveReference(ref).String()
			}
		}
	}

	return string(body), resourceURL, nil
}
