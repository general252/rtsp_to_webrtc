package main

import (
	"bytes"
	"encoding/json"
	"html/template"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/aler9/gortsplib"
	"github.com/aler9/gortsplib/pkg/url"
	"github.com/pion/interceptor"
	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media"
)

var (
	webrtcAPI *webrtc.API
)

const (
	// The RTSP URL that will be streamed
	rtspURL = "rtsp://127.0.0.1:8554/live"
)

func main() {
	log.SetFlags(log.Lshortfile | log.LstdFlags)

	// Create a new API using our SettingEngine
	webrtcAPI = webrtc.NewAPI(getMuxOptions()...)

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_ = homeTemplate.Execute(w, rtspURL)
	})
	http.HandleFunc("/doSignaling", doSignaling)

	log.Println("Open http://localhost:8080 to access this demo")
	panic(http.ListenAndServe("0.0.0.0:8080", nil))
}

func doSignaling(w http.ResponseWriter, r *http.Request) {
	peerConnection, err := webrtcAPI.NewPeerConnection(webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{
				URLs: []string{"stun:stun.l.google.com:19302"},
			},
		},
	})
	if err != nil {
		panic(err)
	}

	var (
		track webrtc.TrackLocal

		isUseSample              = true
		outboundVideoTrackSample *webrtc.TrackLocalStaticSample
		outboundVideoTrackRTP    *webrtc.TrackLocalStaticRTP
	)

	if isUseSample {
		// Create a video track
		outboundVideoTrackSample, err = webrtc.NewTrackLocalStaticSample(
			webrtc.RTPCodecCapability{
				MimeType: webrtc.MimeTypeH264,
			},
			"pion-rtsp-video-sample", "pion-rtsp-video-stream-sample",
		)
		//videoTrack, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264}, "video", "pion")
		if err != nil {
			panic(err)
		}

		track = outboundVideoTrackSample
	} else {
		// Create a video track
		outboundVideoTrackRTP, err = webrtc.NewTrackLocalStaticRTP(
			webrtc.RTPCodecCapability{
				MimeType: webrtc.MimeTypeH264,
			},
			"pion-rtsp-video-rtp", "pion-rtsp-video-stream-rtp",
		)
		if err != nil {
			panic(err)
		}

		track = outboundVideoTrackRTP
	}

	{
		rtpSender, err := peerConnection.AddTrack(track)
		if err != nil {
			panic(err)
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

	var offer webrtc.SessionDescription
	if err = json.NewDecoder(r.Body).Decode(&offer); err != nil {
		panic(err)
	}

	// Set the remote SessionDescription
	if err = peerConnection.SetRemoteDescription(offer); err != nil {
		panic(err)
	}

	// Create channel that is blocked until ICE Gathering is complete
	gatherComplete := webrtc.GatheringCompletePromise(peerConnection)

	// Create answer
	answer, err := peerConnection.CreateAnswer(nil)
	if err != nil {
		panic(err)
	} else if err = peerConnection.SetLocalDescription(answer); err != nil {
		panic(err)
	}

	log.Println("wait PeerConnection complete")
	// Block until ICE Gathering is complete, disabling trickle ICE
	// we do this because we only can exchange one signaling message
	// in a production application you should exchange ICE Candidates via OnICECandidate
	<-gatherComplete

	response, err := json.Marshal(*peerConnection.LocalDescription())
	if err != nil {
		panic(err)
	}

	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write(response); err != nil {
		panic(err)
	}

	log.Printf("==== havePeerConnection")
	go rtspConsumer(rtspURL, isUseSample, peerConnection, outboundVideoTrackSample, outboundVideoTrackRTP)
}

func getMuxOptions() []func(*webrtc.API) {
	// Listen on UDP Port 2000, will be used for all WebRTC traffic
	udpListener, err := net.ListenUDP("udp", &net.UDPAddr{
		IP:   net.IPv4zero,
		Port: 2000,
	})
	if err != nil {
		panic(err)
	}

	_ = udpListener.SetWriteBuffer(512 * 1024)
	_ = udpListener.SetReadBuffer(512 * 1024)

	log.Printf("Listening for WebRTC traffic at %s\n", udpListener.LocalAddr())

	var options []func(*webrtc.API)

	{
		// Create a SettingEngine, this allows non-standard WebRTC behavior
		settingEngine := webrtc.SettingEngine{}

		// Configure our SettingEngine to use our UDPMux. By default a PeerConnection has
		// no global state. The API+SettingEngine allows the user to share state between them.
		// In this case we are sharing our listening port across many.
		settingEngine.SetICEUDPMux(webrtc.NewICEUDPMux(nil, udpListener))

		options = append(options, webrtc.WithSettingEngine(settingEngine))
	}

	// Create a MediaEngine object to configure the supported codec
	m := &webrtc.MediaEngine{}
	{
		// Setup the codecs you want to use.
		// We'll use a VP8 and Opus but you can also define your own
		err = m.RegisterCodec(
			webrtc.RTPCodecParameters{
				RTPCodecCapability: webrtc.RTPCodecCapability{
					MimeType:     webrtc.MimeTypeH264,
					ClockRate:    90000,
					Channels:     0,
					SDPFmtpLine:  "",
					RTCPFeedback: nil,
				},
				PayloadType: 96,
			},
			webrtc.RTPCodecTypeVideo,
		)
		if err != nil {
			panic(err)
		}

		err = m.RegisterCodec(
			webrtc.RTPCodecParameters{
				RTPCodecCapability: webrtc.RTPCodecCapability{
					MimeType:     webrtc.MimeTypeOpus,
					ClockRate:    48000,
					Channels:     0,
					SDPFmtpLine:  "",
					RTCPFeedback: nil,
				},
				PayloadType: 111,
			},
			webrtc.RTPCodecTypeAudio,
		)
		if err != nil {
			panic(err)
		}
	}

	// Create a InterceptorRegistry. This is the user configurable RTP/RTCP Pipeline.
	// This provides NACKs, RTCP Reports and other features. If you use `webrtc.NewPeerConnection`
	// this is enabled by default. If you are manually managing You MUST create a InterceptorRegistry
	// for each PeerConnection.
	i := &interceptor.Registry{}

	// Use the default set of Interceptors
	if err := webrtc.RegisterDefaultInterceptors(m, i); err != nil {
		panic(err)
	}

	options = append(options, webrtc.WithMediaEngine(m))
	options = append(options, webrtc.WithInterceptorRegistry(i))

	return options
}

// Connect to an RTSP URL and pull media.
// Convert H264 to Annex-B, then write to outboundVideoTrack which sends to all PeerConnections
// rtspConsumer 接收rtsp h264流
func rtspConsumer(rtspURL string, isUseSample bool, pc *webrtc.PeerConnection, sampleVideTrack *webrtc.TrackLocalStaticSample, rtpVideoTrack *webrtc.TrackLocalStaticRTP) {
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

		if ctx.H264NALUs == nil {
			return
		}

		if ctx.Packet == nil {
			return
		}

		if isUseSample {
			packetBuffer.Reset()
			for _, nalUs := range ctx.H264NALUs {
				packetBuffer.Write(annexBNALUStartCode)
				packetBuffer.Write(nalUs)
			}

			bufferDuration := ctx.H264PTS - previousTime
			previousTime = ctx.H264PTS

			err = sampleVideTrack.WriteSample(media.Sample{
				Data:     packetBuffer.Bytes(),
				Duration: bufferDuration,
			})
		} else {
			// 有问题
			err = rtpVideoTrack.WriteRTP(ctx.Packet)
		}

		if err != nil {
			log.Println(err)
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

var homeTemplate = template.Must(template.New("").Parse(`
<html lang="en">
<head>
    <title>rtsp-bench</title>
</head>

<div>
RTSP: {{.}}
</div>
<div>
    <button onclick="window.doSignaling(false)" style="font-size:30; "> create offer </button>
</div>

<h3> Logs </h3>
<div id="logs"></div>

<body>
<div id="remoteVideos"></div>
<br/>

</body>

<script>
    let pc = new RTCPeerConnection()
    pc.addTransceiver('video')

    let log = msg => {
        document.getElementById('logs').innerHTML += msg + '<br>'
    }
    pc.oniceconnectionstatechange = () => log(pc.iceConnectionState)
    pc.ontrack = function (event) {
        let el = document.createElement(event.track.kind)
        el.srcObject = event.streams[0]
        el.autoplay = true
        el.controls = false
		el.width = 1280
		el.height = 720

        document.getElementById('remoteVideos').appendChild(el)
    }
	pc.onicecandidate = event => {
		if (event.candidate === null) {
			console.log(' local sdp:' + pc.localDescription)
			console.log('remote sdp:' + pc.remoteDescription)
		} else {
			console.log(event.candidate)
		}
	}

	// Offer to receive 1 audio, and 1 video tracks
	//pc.addTransceiver('audio', {'direction': 'recvonly'})
	//pc.addTransceiver('video', {'direction': 'recvonly'})

    window.doSignaling = iceRestart => {
        pc.createOffer({iceRestart})
            .then(offer => {
                pc.setLocalDescription(offer)

                return fetch('/doSignaling', {
                    method: 'post',
                    headers: {
                        'Accept': 'application/json, text/plain, */*',
                        'Content-Type': 'application/json'
                    },
                    body: JSON.stringify(offer)
                })
            })
            .then(res => res.json())
            .then(res => pc.setRemoteDescription(res))
            .catch(alert)
    }
</script>
</html>

`))
