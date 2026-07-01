package api

import (
	"io"
	"net/http"
	"strings"

	"github.com/datarhei/core/v16/http/api"
	"github.com/datarhei/core/v16/webrtc"

	"github.com/labstack/echo/v4"
)

// The WebRTCHandler implements the WHIP (ingest) and WHEP (egress) HTTP
// endpoints on top of a webrtc.Server. Unlike the rest of the v3 API this
// is deliberately mounted outside of /api and the admin JWT middleware:
// WHIP/WHEP clients (OBS, browsers, VRChat-facing players) authenticate
// with a per-resource bearer token, not an admin session, the same way
// RTMP/SRT publishing uses a stream token rather than the admin API.
type WebRTCHandler struct {
	webrtc webrtc.Server
}

// NewWebRTC returns a new WebRTCHandler type. You have to provide a WebRTC
// server instance.
func NewWebRTC(webrtc webrtc.Server) *WebRTCHandler {
	return &WebRTCHandler{
		webrtc: webrtc,
	}
}

// ListChannels lists all currently active WHIP (publishing) and WHEP
// (playing) resources
// @Summary List all active WHIP/WHEP resources
// @Description List all currently active WHIP (publishing) and WHEP (playing) resources.
// @Tags v16.7.2
// @ID webrtc-3-list-channels
// @Produce json
// @Success 200 {object} webrtc.Channels
// @Security ApiKeyAuth
// @Router /api/v3/webrtc [get]
func (h *WebRTCHandler) ListChannels(c echo.Context) error {
	return c.JSON(http.StatusOK, h.webrtc.Channels())
}

// WHEPRelay is the response body for reserving/inspecting a WHEP resource's
// ffmpeg-facing relay ports.
type WHEPRelay struct {
	Address   string `json:"address"`
	VideoPort uint16 `json:"video_port"`
	AudioPort uint16 `json:"audio_port"`
}

// ReserveWHEP reserves the loopback relay ports an ffmpeg egress process
// should send RTP to for this WHEP resource
// @Summary Reserve relay ports for a WHEP egress resource
// @Description Reserve (or return the already reserved) loopback relay ports for a WHEP egress resource, to be used as the ffmpeg output address.
// @Tags v16.7.2
// @ID webrtc-3-whep-reserve
// @Produce json
// @Param resource path string true "Resource name"
// @Success 200 {object} WHEPRelay
// @Security ApiKeyAuth
// @Router /api/v3/webrtc/whep/{resource} [post]
func (h *WebRTCHandler) ReserveWHEP(c echo.Context) error {
	resource := c.Param("resource")

	address, videoPort, audioPort, err := h.webrtc.ReserveWHEP(resource)
	if err != nil {
		return api.Err(http.StatusBadRequest, "", "%s", err)
	}

	return c.JSON(http.StatusOK, WHEPRelay{
		Address:   address,
		VideoPort: videoPort,
		AudioPort: audioPort,
	})
}

// ReleaseWHEP releases a reserved WHEP resource and disconnects any
// active viewers
// @Summary Release a WHEP egress resource
// @Description Release a reserved WHEP egress resource and disconnect any active viewers.
// @Tags v16.7.2
// @ID webrtc-3-whep-release
// @Param resource path string true "Resource name"
// @Success 200 {string} string ""
// @Security ApiKeyAuth
// @Router /api/v3/webrtc/whep/{resource} [delete]
func (h *WebRTCHandler) ReleaseWHEP(c echo.Context) error {
	h.webrtc.ReleaseWHEP(c.Param("resource"))

	return c.NoContent(http.StatusOK)
}

func bearerToken(c echo.Context) string {
	auth := c.Request().Header.Get("Authorization")
	if prefix := "Bearer "; strings.HasPrefix(auth, prefix) {
		return strings.TrimPrefix(auth, prefix)
	}

	return c.QueryParam("token")
}

func readSDPBody(c echo.Context) (string, error) {
	body, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return "", err
	}

	return string(body), nil
}

// WHIP publishes a new WHIP resource
// @Summary Publish a stream via WHIP
// @Description Publish a stream via WHIP (WebRTC-HTTP Ingestion Protocol, RFC 9725). The request body is an SDP offer (content-type application/sdp).
// @Tags v16.7.2
// @ID whip-3-publish
// @Accept application/sdp
// @Produce application/sdp
// @Param resource path string true "Resource name"
// @Success 201 {string} string "SDP answer"
// @Security ApiKeyAuth
// @Router /whip/{resource} [post]
func (h *WebRTCHandler) WHIP(c echo.Context) error {
	resource := c.Param("resource")

	offer, err := readSDPBody(c)
	if err != nil {
		return api.Err(http.StatusBadRequest, "", "reading SDP offer: %s", err)
	}

	answer, sessionID, err := h.webrtc.WHIP(resource, bearerToken(c), offer)
	if err != nil {
		return api.Err(http.StatusBadRequest, "", "%s", err)
	}

	c.Response().Header().Set("Location", "/whip/"+resource+"/"+sessionID)

	return c.Blob(http.StatusCreated, "application/sdp", []byte(answer))
}

// WHIPDelete ends a WHIP publishing session
// @Summary End a WHIP publishing session
// @Description End a WHIP publishing session.
// @Tags v16.7.2
// @ID whip-3-delete
// @Param resource path string true "Resource name"
// @Param session path string true "Session ID"
// @Success 200 {string} string ""
// @Security ApiKeyAuth
// @Router /whip/{resource}/{session} [delete]
func (h *WebRTCHandler) WHIPDelete(c echo.Context) error {
	resource := c.Param("resource")
	sessionID := c.Param("session")

	if err := h.webrtc.WHIPDelete(resource, sessionID); err != nil {
		return api.Err(http.StatusNotFound, "", "%s", err)
	}

	return c.NoContent(http.StatusOK)
}

// WHEP plays a WHEP resource
// @Summary Play a stream via WHEP
// @Description Play a stream via WHEP (WebRTC-HTTP Egress Protocol). The request body is an SDP offer (content-type application/sdp).
// @Tags v16.7.2
// @ID whep-3-play
// @Accept application/sdp
// @Produce application/sdp
// @Param resource path string true "Resource name"
// @Success 201 {string} string "SDP answer"
// @Security ApiKeyAuth
// @Router /whep/{resource} [post]
func (h *WebRTCHandler) WHEP(c echo.Context) error {
	resource := c.Param("resource")

	offer, err := readSDPBody(c)
	if err != nil {
		return api.Err(http.StatusBadRequest, "", "reading SDP offer: %s", err)
	}

	answer, sessionID, err := h.webrtc.WHEP(resource, bearerToken(c), offer)
	if err != nil {
		return api.Err(http.StatusBadRequest, "", "%s", err)
	}

	c.Response().Header().Set("Location", "/whep/"+resource+"/"+sessionID)

	return c.Blob(http.StatusCreated, "application/sdp", []byte(answer))
}

// WHEPDelete ends a WHEP playing session
// @Summary End a WHEP playing session
// @Description End a WHEP playing session.
// @Tags v16.7.2
// @ID whep-3-delete
// @Param resource path string true "Resource name"
// @Param session path string true "Session ID"
// @Success 200 {string} string ""
// @Security ApiKeyAuth
// @Router /whep/{resource}/{session} [delete]
func (h *WebRTCHandler) WHEPDelete(c echo.Context) error {
	resource := c.Param("resource")
	sessionID := c.Param("session")

	if err := h.webrtc.WHEPDelete(resource, sessionID); err != nil {
		return api.Err(http.StatusNotFound, "", "%s", err)
	}

	return c.NoContent(http.StatusOK)
}
