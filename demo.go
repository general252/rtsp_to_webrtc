package main

import (
	"bytes"
	"fmt"
	"github.com/aler9/gortsplib/pkg/rtpcleaner"
	"github.com/aler9/gortsplib/pkg/rtpreorderer"
	"github.com/general252/rtsp_to_webrtc/pkg"
	"github.com/gookit/color"
	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v3"
	"log"
	"net"
	"os"
	"time"
)

var (
	pcA *webrtc.PeerConnection
	pcB *webrtc.PeerConnection

	config = webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{
				URLs: []string{"stun:stun.l.google.com:19302"},
			},
		},
	}

	chOffer  = make(chan webrtc.SessionDescription, 1)
	chAnswer = make(chan *webrtc.SessionDescription, 1)

	stringA = color.FgRed.Sprint
	stringB = color.FgGreen.Sprint
)

func offer() error {
	var err error
	if pcA, err = CreateWebrtcAPI(2001).NewPeerConnection(config); err != nil {
		log.Println(stringA(err))
		return err
	}
	pc := pcA

	// Set the handler for ICE connection state
	// This will notify you when the peer has connected/disconnected
	pc.OnICEConnectionStateChange(func(connectionState webrtc.ICEConnectionState) {
		log.Printf(stringA("[A] [ice state] Connection State has changed ", connectionState.String()))
		if connectionState == webrtc.ICEConnectionStateFailed {
			if closeErr := pc.Close(); closeErr != nil {
				log.Println(closeErr)
			}
		}
	})

	pc.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate != nil {
			log.Printf(stringA("[A] [ice] OnICECandidate ", candidate.String()))
		}
	})

	pc.OnTrack(func(remoteTrack *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		go func() {
			ticker := time.NewTicker(time.Second * 2)
			for range ticker.C {
				if rtcpErr := pc.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: uint32(remoteTrack.SSRC())}}); rtcpErr != nil {
					fmt.Println(stringA(rtcpErr))
				}
			}
		}()

		log.Println(stringA("[A] new track"))

		reorder := rtpreorderer.New()
		cleaner := rtpcleaner.New(true, false)

		fp, _ := os.Create("out2.h264")
		var (
			annexBNALUStartCode = []byte{0x00, 0x00, 0x00, 0x01}
			packetBuffer        bytes.Buffer
		)

		for {
			packet, _, readErr := remoteTrack.ReadRTP()
			if readErr != nil {
				log.Println(stringA(readErr))
				break
			}

			pktList := reorder.Process(packet)
			for _, pkt := range pktList {
				out, err := cleaner.Process(pkt)
				if err != nil {
					return
				}

				out0 := out[0]
				_ = out0.H264NALUs
				packetBuffer.Reset()
				for _, nalUs := range out0.H264NALUs {
					packetBuffer.Write(annexBNALUStartCode)
					packetBuffer.Write(nalUs)
				}
				_, _ = fp.Write(packetBuffer.Bytes())
			}
		}
	})

	// 请求视频
	if _, err = pc.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo); err != nil {
		log.Println(stringA(err))
		return err
	}

	// 请求offer
	offerSDP, err := pc.CreateOffer(nil)
	if err != nil {
		log.Println(stringA(err))
		return err
	} else {
		log.Println(stringA("[A] create offer"))
	}

	if err = pc.SetLocalDescription(offerSDP); err != nil {
		log.Println(err)
		return err
	}

	// 发送offer
	chOffer <- offerSDP

	log.Println(stringA("[A] wait answer"))
	// 等answer
	answerSDP := <-chAnswer

	log.Println(stringA("[A] get answer"))
	_ = pc.SetRemoteDescription(*answerSDP)

	return nil
}

func answer() error {
	var err error
	if pcB, err = CreateWebrtcAPI(2002).NewPeerConnection(config); err != nil {
		log.Println(err)
		return err
	}
	pc := pcB

	pc.OnICEConnectionStateChange(func(connectionState webrtc.ICEConnectionState) {
		log.Printf(stringB("[B] [ice state] Connection State has changed ", connectionState.String()))

		if connectionState == webrtc.ICEConnectionStateFailed {
			if closeErr := pc.Close(); closeErr != nil {
				log.Println(stringB(closeErr))
			}
		}
	})

	pc.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate != nil {
			log.Printf(stringB("[B] [ice] OnICECandidate ", candidate.String()))
		}
	})

	// Create a video track
	videoTrack, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264},
		"pion-rtsp-video-sample", "pion-rtsp-video-stream-sample",
	)

	{
		rtpSender, err := pc.AddTrack(videoTrack)
		if err != nil {
			log.Println(stringA(err))
			return err
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

	log.Println(stringB("[B] wait offer"))
	offerSDP := <-chOffer

	if err = pc.SetRemoteDescription(offerSDP); err != nil {
		log.Println(err)
		return err
	}

	gatherComplete := webrtc.GatheringCompletePromise(pc)

	log.Println(stringB("[B] get offer, create answer"))
	answerSDP, err := pc.CreateAnswer(nil)
	if err != nil {
		log.Println(err)
		return err
	}

	if err = pc.SetLocalDescription(answerSDP); err != nil {
		log.Println(err)
		return err
	}

	<-gatherComplete

	log.Println(stringB("[B] response answer"))
	chAnswer <- pc.LocalDescription()

	//go pkg.RtspConsumerSample(pkg.RtspURL, pc, videoTrack)
	go pkg.Rtp(5006, pc, videoTrack)

	return nil
}

func CreateWebrtcAPI(muxUdpPort int) *webrtc.API {
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

	return webrtc.NewAPI(options...)
}
