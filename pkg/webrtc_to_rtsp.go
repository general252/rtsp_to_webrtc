package pkg

import (
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/aler9/gortsplib"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v3"
)

// 推流

func (tis *WebRtcEngine) WebrtcToRtsp(c *gin.Context) {
	var offer webrtc.SessionDescription
	if err := c.ShouldBindJSON(&offer); err != nil {
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

	if _, err = peerConnection.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo); err != nil {
		log.Println(err)
		c.Abort()
		return
	}

	if false {
		// Allow us to receive 1 audio track, and 1 video track
		if _, err = peerConnection.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio); err != nil {
			log.Println(err)
			c.Abort()
			return
		}
	}

	// Set the handler for ICE connection state
	// This will notify you when the peer has connected/disconnected
	peerConnection.OnICEConnectionStateChange(func(connectionState webrtc.ICEConnectionState) {
		log.Printf("[ice state] Connection State has changed %s \n", connectionState.String())

		if connectionState == webrtc.ICEConnectionStateFailed {
			if closeErr := peerConnection.Close(); closeErr != nil {
				log.Println(closeErr)
			}
		}
	})

	peerConnection.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate != nil {
			log.Printf("[ice] OnICECandidate %s \n", candidate.String())
		}
	})

	// 连接rtsp
	// 添加track
	// setup and record
	cli := gortsplib.Client{}

	// create an H264 track
	track := &gortsplib.TrackVP8{
		PayloadType: 96,
	}

	if err = cli.StartPublishing(RtspURL, gortsplib.Tracks{track}); err != nil {
		log.Println(err)
		c.Abort()
		return
	}

	// Set a handler for when a new remote track starts, this handler will forward data to
	// our UDP listeners.
	// In your application this is where you would handle/process audio/video
	peerConnection.OnTrack(func(remoteTrack *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		// Send a PLI on an interval so that the publisher is pushing a keyframe every rtcpPLIInterval
		go func() {
			ticker := time.NewTicker(time.Second * 2)
			for range ticker.C {
				if rtcpErr := peerConnection.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: uint32(remoteTrack.SSRC())}}); rtcpErr != nil {
					fmt.Println(rtcpErr)
				}
			}
		}()

		if remoteTrack.Kind() != webrtc.RTPCodecTypeVideo {
			return
		}

		// Create a local track, all our SFU clients will be fed via this track
		var newTrackErr error
		tis.localRTPTrack, newTrackErr = webrtc.NewTrackLocalStaticRTP(remoteTrack.Codec().RTPCodecCapability, "video", "pion")
		if newTrackErr != nil {
			panic(newTrackErr)
		}

		pkt := &rtp.Packet{}
		rtpBuf := make([]byte, 1500)
		for {
			n, _, readErr := remoteTrack.Read(rtpBuf)
			if readErr != nil {
				panic(readErr)
			}

			// 转发给其它的webrtc请求者
			if true {
				// ErrClosedPipe means we don't have any subscribers, this is ok if no peers have connected yet
				if _, err = tis.localRTPTrack.Write(rtpBuf[:n]); err != nil && !errors.Is(err, io.ErrClosedPipe) {
					panic(err)
				}
			}

			// 转发RTSP
			if true {
				// Unmarshal the packet and update the PayloadType
				if err = pkt.Unmarshal(rtpBuf[:n]); err != nil {
					log.Println(err)
					break
				}
				if err = cli.WritePacketRTP(0, pkt, true); err != nil {
					log.Println(err)
					break
				}
			}
		}

	})

	// Set the remote SessionDescription
	if err = peerConnection.SetRemoteDescription(offer); err != nil {
		log.Println(err)
		c.Abort()
		return
	}

	// Create channel that is blocked until ICE Gathering is complete
	gatherComplete := webrtc.GatheringCompletePromise(peerConnection)

	// Create answer
	answer, err := peerConnection.CreateAnswer(nil)
	if err != nil {
		log.Println(err)
		c.Abort()
		return
	} else if err = peerConnection.SetLocalDescription(answer); err != nil {
		log.Println(err)
		c.Abort()
		return
	}

	log.Println("wait PeerConnection complete")
	// Block until ICE Gathering is complete, disabling trickle ICE
	// we do this because we only can exchange one signaling message
	// in a production application you should exchange ICE Candidates via OnICECandidate
	<-gatherComplete

	c.JSON(http.StatusOK, peerConnection.LocalDescription())

	log.Printf("==== havePeerConnection")
}
