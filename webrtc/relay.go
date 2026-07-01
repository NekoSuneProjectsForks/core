package webrtc

import (
	"fmt"
	"hash/fnv"
	"net"
	"sync"
)

// derivePort deterministically maps salt+name to a port in [min, max) using
// FNV-1a. It's used for WHEP relay ports so a UI can independently compute
// the same rtp://host:port ffmpeg output address a client will need,
// without a reservation round-trip. The JS-side implementation must hash
// the same concatenated byte sequence (parts joined in order, no
// delimiter) for the two to agree - see the port derivation helper next to
// the WHEP UI component.
func derivePort(min, max uint16, parts ...string) uint16 {
	h := fnv.New32a()
	for _, p := range parts {
		h.Write([]byte(p))
	}

	span := uint32(max - min)

	return min + uint16(h.Sum32()%span)
}

// portAllocator hands out local UDP ports from a fixed range for the
// ffmpeg-facing side of WHIP/WHEP resources.
type portAllocator struct {
	min, max uint16
	lock     sync.Mutex
	used     map[uint16]bool
	next     uint16
}

func newPortAllocator(min, max uint16) *portAllocator {
	if max <= min {
		max = min + 1
	}

	return &portAllocator{
		min:  min,
		max:  max,
		used: map[uint16]bool{},
		next: min,
	}
}

// Allocate reserves and returns a free port in the configured range.
func (a *portAllocator) Allocate() (uint16, error) {
	a.lock.Lock()
	defer a.lock.Unlock()

	for i := uint32(0); i < uint32(a.max-a.min); i++ {
		port := a.next
		a.next++
		if a.next >= a.max {
			a.next = a.min
		}

		if a.used[port] {
			continue
		}

		a.used[port] = true

		return port, nil
	}

	return 0, fmt.Errorf("no free port available in range %d-%d", a.min, a.max)
}

// Release frees a previously allocated port.
func (a *portAllocator) Release(port uint16) {
	a.lock.Lock()
	defer a.lock.Unlock()

	delete(a.used, port)
}

// probeFreeUDPPort binds a UDP socket on the given port just long enough to
// confirm it's actually free, then closes it again. There's a small window
// between this check and ffmpeg (not us) binding the same port for real;
// ffmpeg's existing reconnect/retry logic covers that race the same way it
// already does for RTMP/SRT inputs that aren't ready yet.
func probeFreeUDPPort(address string, port uint16) error {
	addr := &net.UDPAddr{IP: net.ParseIP(address), Port: int(port)}

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return err
	}

	return conn.Close()
}

// udpSender is a thin wrapper around a UDP client socket used to forward
// RTP packets from a WHIP publisher's WebRTC track to the local ffmpeg
// process that's configured to read from that port (via a generated SDP).
type udpSender struct {
	conn *net.UDPConn
}

func newUDPSender(address string, port uint16) (*udpSender, error) {
	conn, err := net.Dial("udp", net.JoinHostPort(address, fmt.Sprintf("%d", port)))
	if err != nil {
		return nil, err
	}

	return &udpSender{conn: conn.(*net.UDPConn)}, nil
}

func (s *udpSender) Write(b []byte) error {
	_, err := s.conn.Write(b)
	return err
}

func (s *udpSender) Close() {
	s.conn.Close()
}

// udpReceiver listens on a local UDP port for RTP packets sent by an ffmpeg
// egress process (WHEP) and hands each packet to onPacket for fan-out to
// subscribed viewer tracks.
type udpReceiver struct {
	conn     *net.UDPConn
	done     chan struct{}
	onPacket func([]byte)
}

func newUDPReceiver(address string, port uint16, onPacket func([]byte)) (*udpReceiver, error) {
	addr := &net.UDPAddr{IP: net.ParseIP(address), Port: int(port)}

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return nil, err
	}

	r := &udpReceiver{
		conn:     conn,
		done:     make(chan struct{}),
		onPacket: onPacket,
	}

	go r.readLoop()

	return r, nil
}

func (r *udpReceiver) readLoop() {
	buf := make([]byte, 1500)

	for {
		n, err := r.conn.Read(buf)
		if err != nil {
			// Either the connection was closed deliberately via Close(), or
			// a real read error occurred. Either way there's nothing more
			// to relay on this socket.
			return
		}

		packet := make([]byte, n)
		copy(packet, buf[:n])

		r.onPacket(packet)
	}
}

func (r *udpReceiver) Close() {
	close(r.done)
	r.conn.Close()
}
