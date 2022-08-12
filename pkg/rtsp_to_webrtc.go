package pkg

import (
	"bytes"
	"log"
	"net/http"
	"time"

	"github.com/aler9/gortsplib"
	"github.com/aler9/gortsplib/pkg/url"
	"github.com/gin-gonic/gin"
	"github.com/pion/webrtc/v3"
)

// 拉流

func (tis *WebRtcEngine) RtspToWebrtc(c *gin.Context) {
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

	var videoTrack *webrtc.TrackLocalStaticRTP

	// Create a video track
	videoTrack, err = webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{
			MimeType: MimeType,
		},
		"pion-rtsp-video-sample", "pion-rtsp-video-stream-sample",
	)
	if err != nil {
		log.Println(err)
		c.Abort()
		return
	}

	{
		rtpSender, err := peerConnection.AddTrack(videoTrack)
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
	}
	if err = peerConnection.SetLocalDescription(answer); err != nil {
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
	go rtspConsumer(RtspURL, peerConnection, videoTrack)
}

// Connect to an RTSP URL and pull media.
// Convert H264 to Annex-B, then write to outboundVideoTrack which sends to all PeerConnections
// rtspConsumer 接收rtsp h264流
func rtspConsumer(rtspURL string, pc *webrtc.PeerConnection, videoTrack *webrtc.TrackLocalStaticRTP) {
	// parse URL
	u, err := url.Parse(rtspURL)
	if err != nil {
		log.Println(err)
		return
	}

	c := gortsplib.Client{}

	// connect to the server
	if err = c.Start(u.Scheme, u.Host); err != nil {
		log.Println(err)
		return
	}
	defer func() {
		_ = c.Close()
	}()

	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateDisconnected {
			log.Println("close rtsp")
			_ = c.Close()
		}
		if state == webrtc.PeerConnectionStateClosed {
			log.Println("close rtsp")
			_ = c.Close()
		}
	})

	// find published tracks
	tracks, baseURL, _, err := c.Describe(u)
	if err != nil {
		log.Println(err)
		return
	}

	for _, track := range tracks {
		log.Printf("track %#v", track)
	}

	// find the H264 track
	h264TrackID, h264track := func() (int, *gortsplib.TrackH264) {
		for i, track := range tracks {
			if h264track, ok := track.(*gortsplib.TrackH264); ok {
				return i, h264track
			}
		}
		return -1, nil
	}()
	if h264TrackID < 0 {
		log.Println("H264 track not found")
		return
	}
	_ = h264track

	var (
		annexBNALUStartCode = []byte{0x00, 0x00, 0x00, 0x01}
		previousTime        time.Duration
		packetBuffer        bytes.Buffer
	)

	// called when a RTP packet arrives
	c.OnPacketRTP = func(ctx *gortsplib.ClientOnPacketRTPCtx) {
		if ctx.TrackID != h264TrackID {
			return
		}

		if true {
			pkt := ctx.Packet
			_ = pkt

			log.Println(pkt.Header.SequenceNumber)
			if err = videoTrack.WriteRTP(pkt); err != nil {
				log.Println(err)
			}
		}

		if false {
			if ctx.H264NALUs == nil {
				return
			}

			packetBuffer.Reset()
			for _, nalUs := range ctx.H264NALUs {
				packetBuffer.Write(annexBNALUStartCode)
				packetBuffer.Write(nalUs)
			}

			bufferDuration := ctx.H264PTS - previousTime
			previousTime = ctx.H264PTS
			_ = bufferDuration

			//err = videoTrack.WriteSample(media.Sample{
			//Data:     packetBuffer.Bytes(),
			//Duration: bufferDuration,
			//})

			if err != nil {
				log.Println(err)
			}
		}
	}

	log.Println("[rtsp] setup and play")

	// setup and read all tracks
	if err = c.SetupAndPlay(tracks, baseURL); err != nil {
		log.Println(err)
	}

	log.Println("[rtsp] wait...")
	// wait until a fatal error
	if err = c.Wait(); err != nil {
		log.Println(err)
	}
}
