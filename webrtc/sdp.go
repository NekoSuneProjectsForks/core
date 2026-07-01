package webrtc

import (
	"fmt"
	"os"
	"path/filepath"
)

// h264PayloadType and opusPayloadType are the fixed dynamic RTP payload
// types used on the loopback link between this server and ffmpeg. They
// never appear on the wire towards the WHIP/WHEP peer: pion rewrites the
// payload type of every packet to whatever was actually negotiated with
// that peer (see TrackLocalStaticRTP.writeRTP), and on the WHIP ingest side
// they're only used to tell ffmpeg how to interpret payload type 96/97 -
// the same fixed pair we require the browser/publisher to negotiate.
const (
	h264PayloadType = 96
	opusPayloadType = 97
)

// writeWHIPInputSDP writes the SDP file ffmpeg reads to consume the RTP
// relayed from a WHIP publisher on the given loopback video/audio ports.
// This mirrors how ffmpeg is pointed at "rtmp://localhost:1935/..." for
// RTMP inputs, just one layer lower (raw RTP instead of RTMP).
func writeWHIPInputSDP(dir, resource, address string, videoPort, audioPort uint16) (string, error) {
	sdp := fmt.Sprintf(`v=0
o=- 0 0 IN IP4 %s
s=%s
c=IN IP4 %s
t=0 0
m=video %d RTP/AVP %d
a=rtpmap:%d H264/90000
a=fmtp:%d packetization-mode=1
m=audio %d RTP/AVP %d
a=rtpmap:%d opus/48000/2
`, address, resource, address, videoPort, h264PayloadType, h264PayloadType, h264PayloadType, audioPort, opusPayloadType, opusPayloadType)

	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("creating SDP directory: %w", err)
	}

	path := filepath.Join(dir, resource+".sdp")

	if err := os.WriteFile(path, []byte(sdp), 0644); err != nil {
		return "", fmt.Errorf("writing SDP file: %w", err)
	}

	return path, nil
}

func removeWHIPInputSDP(dir, resource string) {
	os.Remove(filepath.Join(dir, resource+".sdp"))
}
