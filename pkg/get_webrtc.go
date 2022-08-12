package pkg

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/pion/webrtc/v3"
)

// 转发webrtc流
// webrtc_to_rtsp流负责推流, 本项目拉流播放

func (tis *WebRtcEngine) GetWebrtc(c *gin.Context) {
	if tis.localRTPTrack == nil {
		c.Abort()
		return
	}

	var recvOnlyOffer webrtc.SessionDescription
	if err := c.ShouldBindJSON(&recvOnlyOffer); err != nil {
		log.Println(err)
		c.Abort()
		return
	}

	peerConnection, err := tis.api.NewPeerConnection(webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{
				URLs: []string{"stun:stun.l.google.com:19302"},
			},
		},
	})
	if err != nil {
		log.Println(err)
		c.Abort()
		return
	}

	rtpSender, err := peerConnection.AddTrack(tis.localRTPTrack)
	if err != nil {
		log.Println(err)
		c.Abort()
		return
	}

	// Read incoming RTCP packets
	// Before these packets are returned they are processed by interceptors. For things
	// like NACK this needs to be called.
	go func() {
		rtcpBuf := make([]byte, 1500)
		for {
			if _, _, rtcpErr := rtpSender.Read(rtcpBuf); rtcpErr != nil {
				return
			}
		}
	}()

	// Set the remote SessionDescription
	if err = peerConnection.SetRemoteDescription(recvOnlyOffer); err != nil {
		log.Println(err)
		c.Abort()
		return
	}

	// Create answer
	answer, err := peerConnection.CreateAnswer(nil)
	if err != nil {
		log.Println(err)
		c.Abort()
		return
	}

	// Create channel that is blocked until ICE Gathering is complete
	gatherComplete := webrtc.GatheringCompletePromise(peerConnection)

	// Sets the LocalDescription, and starts our UDP listeners
	if err = peerConnection.SetLocalDescription(answer); err != nil {
		log.Println(err)
		c.Abort()
		return
	}

	// Block until ICE Gathering is complete, disabling trickle ICE
	// we do this because we only can exchange one signaling message
	// in a production application you should exchange ICE Candidates via OnICECandidate
	<-gatherComplete

	// Get the LocalDescription and take it to base64 so we can paste in browser
	c.JSON(http.StatusOK, peerConnection.LocalDescription())
}
