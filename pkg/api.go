package pkg

import (
	"github.com/pion/interceptor"
	"github.com/pion/webrtc/v3"
	"log"
	"net"
)

const (
	// RtspURL The RTSP URL that will be streamed
	RtspURL  = "rtsp://127.0.0.1:8554/live"
	MimeType = webrtc.MimeTypeH264
)

type WebRtcEngine struct {
	api *webrtc.API

	localRTPTrack *webrtc.TrackLocalStaticRTP
}

func NewWebRtcEngine(muxUdpPort int) *WebRtcEngine {
	c := &WebRtcEngine{}
	c.api = webrtc.NewAPI(c.getMuxOptions(muxUdpPort)...)

	return c
}

func (tis *WebRtcEngine) getMuxOptions(muxUdpPort int) []func(*webrtc.API) {
	// Listen on UDP Port 2000, will be used for all WebRTC traffic
	udpListener, err := net.ListenUDP("udp", &net.UDPAddr{
		IP:   net.IPv4zero,
		Port: muxUdpPort,
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
		var codecParams = []webrtc.RTPCodecParameters{
			{
				RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000, RTCPFeedback: nil},
				PayloadType:        96,
			},
			{
				RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP9, ClockRate: 90000, SDPFmtpLine: "profile-id=0", RTCPFeedback: nil},
				PayloadType:        98,
			},
			{
				RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP9, ClockRate: 90000, SDPFmtpLine: "profile-id=1", RTCPFeedback: nil},
				PayloadType:        100,
			},
			{
				RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f", RTCPFeedback: nil},
				PayloadType:        125,
			},
			{
				RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=0;profile-level-id=42e01f", RTCPFeedback: nil},
				PayloadType:        108,
			},
			{
				RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=640032", RTCPFeedback: nil},
				PayloadType:        123,
			},
			{
				RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeAV1, ClockRate: 90000, RTCPFeedback: nil},
				PayloadType:        35,
			},
		}

		if false {
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
		} else {
			for _, param := range codecParams {
				err = m.RegisterCodec(param, webrtc.RTPCodecTypeVideo)
				if err != nil {
					panic(err)
				}
			}
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
