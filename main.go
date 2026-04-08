package main

// #cgo pkg-config: libavformat libavcodec libavutil libswresample libswscale
// #include <string.h>
// #include <stdlib.h>
// #include <libavutil/error.h>
// #include <libavutil/pixfmt.h>
// #include <libavutil/samplefmt.h>
// #include <libavutil/avutil.h>
// #include "decode_media.h"
import "C"

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"time"
	"unsafe"

	agoraservice "github.com/AgoraIO-Extensions/Agora-Golang-Server-SDK/v2/go_sdk/rtc"
	rtctokenbuilder "github.com/AgoraIO/Tools/DynamicKey/AgoraDynamicKey/go/src/rtctokenbuilder2"
)

type config struct {
	appID      string
	appCert    string
	channel    string
	uid        string
	token      string
	input      string
	audioOnly  bool
	videoOnly  bool
	debugSleep bool
}

type audioChunker struct {
	sampleRate int
	channels   int
	buffer     []byte
}

func main() {
	cfg, err := parseConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "configuration error: %v\n", err)
		os.Exit(2)
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)

	if err := run(cfg, stop); err != nil {
		fmt.Fprintf(os.Stderr, "publisher failed: %v\n", err)
		os.Exit(1)
	}
}

func parseConfig() (*config, error) {
	cfg := &config{}
	flag.StringVar(&cfg.appID, "app-id", envOr("AGORA_APP_ID", ""), "Agora App ID")
	flag.StringVar(&cfg.appCert, "app-certificate", envOr("AGORA_APP_CERTIFICATE", ""), "Agora App Certificate, used only to generate a token when --token is empty")
	flag.StringVar(&cfg.channel, "channel", envOr("AGORA_CHANNEL", ""), "Agora channel name")
	flag.StringVar(&cfg.uid, "uid", envOr("AGORA_UID", "0"), "Agora string UID / user account")
	flag.StringVar(&cfg.token, "token", envOr("AGORA_TOKEN", ""), "Agora RTC token; optional when App Certificate is supplied")
	flag.StringVar(&cfg.input, "input", envOr("MP4_INPUT", ""), "Path to an input MP4 file with H.264 video")
	flag.BoolVar(&cfg.audioOnly, "audio-only", false, "Publish only audio from the MP4")
	flag.BoolVar(&cfg.videoOnly, "video-only", false, "Publish only video from the MP4")
	flag.BoolVar(&cfg.debugSleep, "debug-sleep-log", false, "Log pacing sleeps while sending media")
	flag.Parse()

	switch {
	case cfg.appID == "":
		return nil, errors.New("missing --app-id or AGORA_APP_ID")
	case cfg.channel == "":
		return nil, errors.New("missing --channel or AGORA_CHANNEL")
	case cfg.input == "":
		return nil, errors.New("missing --input or MP4_INPUT")
	case cfg.audioOnly && cfg.videoOnly:
		return nil, errors.New("choose at most one of --audio-only or --video-only")
	}

	absInput, err := filepath.Abs(cfg.input)
	if err != nil {
		return nil, fmt.Errorf("resolve input path: %w", err)
	}
	if _, err := os.Stat(absInput); err != nil {
		return nil, fmt.Errorf("input file %q: %w", absInput, err)
	}
	cfg.input = absInput

	if cfg.token == "" && cfg.appCert != "" {
		cfg.token, err = buildToken(cfg.appID, cfg.appCert, cfg.channel, cfg.uid)
		if err != nil {
			return nil, err
		}
	}

	return cfg, nil
}

func run(cfg *config, stop <-chan os.Signal) error {
	if err := os.MkdirAll("agora_rtc_log", 0o755); err != nil {
		return fmt.Errorf("create agora log directory: %w", err)
	}

	svcCfg := agoraservice.NewAgoraServiceConfig()
	svcCfg.AppId = cfg.appID
	svcCfg.EnableVideo = !cfg.audioOnly
	svcCfg.LogPath = "./agora_rtc_log/agorasdk.log"
	svcCfg.LogSize = 2 * 1024
	agoraservice.Initialize(svcCfg)
	defer agoraservice.Release()

	publishConfig := agoraservice.NewRtcConPublishConfig()
	publishConfig.AudioScenario = agoraservice.AudioScenarioAiServer
	publishConfig.IsPublishAudio = !cfg.videoOnly
	publishConfig.IsPublishVideo = !cfg.audioOnly
	publishConfig.AudioPublishType = agoraservice.AudioPublishTypePcm
	if !cfg.audioOnly {
		publishConfig.VideoPublishType = agoraservice.VideoPublishTypeYuv
	}

	conCfg := &agoraservice.RtcConnectionConfig{
		AutoSubscribeAudio: false,
		AutoSubscribeVideo: false,
		ClientRole:         agoraservice.ClientRoleBroadcaster,
		ChannelProfile:     agoraservice.ChannelProfileLiveBroadcasting,
	}

	con := agoraservice.NewRtcConnection(conCfg, publishConfig)
	if con == nil {
		return errors.New("failed to create rtc connection")
	}
	defer con.Release()

	connected := make(chan struct{}, 1)
	disconnected := make(chan string, 1)
	con.RegisterObserver(&agoraservice.RtcConnectionObserver{
		OnConnected: func(_ *agoraservice.RtcConnection, info *agoraservice.RtcConnectionInfo, reason int) {
			fmt.Printf("connected: channel=%s uid=%s internal_uid=%d reason=%d\n", info.ChannelId, info.LocalUserId, info.InternalUid, reason)
			select {
			case connected <- struct{}{}:
			default:
			}
		},
		OnDisconnected: func(_ *agoraservice.RtcConnection, info *agoraservice.RtcConnectionInfo, reason int) {
			msg := fmt.Sprintf("disconnected: channel=%s uid=%s reason=%d", info.ChannelId, info.LocalUserId, reason)
			fmt.Println(msg)
			select {
			case disconnected <- msg:
			default:
			}
		},
		OnUserJoined: func(_ *agoraservice.RtcConnection, uid string) {
			fmt.Printf("remote user joined: %s\n", uid)
		},
		OnUserLeft: func(_ *agoraservice.RtcConnection, uid string, reason int) {
			fmt.Printf("remote user left: %s reason=%d\n", uid, reason)
		},
		OnAIQoSCapabilityMissing: func(_ *agoraservice.RtcConnection, defaultFallbackScenario int) int {
			fmt.Printf("AI QoS capability missing, falling back from scenario=%d to default audio scenario\n", defaultFallbackScenario)
			return int(agoraservice.AudioScenarioDefault)
		},
	})

	if rc := con.Connect(cfg.token, cfg.channel, cfg.uid); rc != 0 {
		return fmt.Errorf("connect failed: %d", rc)
	}

	select {
	case <-connected:
	case msg := <-disconnected:
		return errors.New(msg)
	case <-time.After(10 * time.Second):
		return errors.New("timed out waiting for Agora connection")
	case <-stop:
		return errors.New("interrupted before connection completed")
	}

	if !cfg.videoOnly {
		if rc := con.PublishAudio(); rc != 0 {
			return fmt.Errorf("publish audio failed: %d", rc)
		}
	}
	if !cfg.audioOnly {
		if rc := con.PublishVideo(); rc != 0 {
			return fmt.Errorf("publish video failed: %d", rc)
		}
	}

	fmt.Printf("publishing input %s to channel %s as uid %s\n", cfg.input, cfg.channel, cfg.uid)
	err := streamMedia(con, cfg, stop, disconnected)
	con.Disconnect()
	return err
}

func streamMedia(con *agoraservice.RtcConnection, cfg *config, stop <-chan os.Signal, disconnected <-chan string) error {
	fileName := C.CString(cfg.input)
	defer C.free(unsafe.Pointer(fileName))

	decoder := C.open_media_file(fileName)
	if decoder == nil {
		return fmt.Errorf("open media file %q", cfg.input)
	}
	defer C.close_media_file(decoder)

	var packet *C.struct__MediaPacket
	frame := C.struct__MediaFrame{}
	C.memset(unsafe.Pointer(&frame), 0, C.sizeof_struct__MediaFrame)
	audioChunker := &audioChunker{}

	var firstPTS int64
	startedAt := time.Now()

	for {
		select {
		case <-stop:
			return errors.New("interrupted")
		case msg := <-disconnected:
			return errors.New(msg)
		default:
		}

		totalSendTime := time.Since(startedAt).Milliseconds()
		ret := C.get_packet(decoder, &packet)
		if ret != 0 {
			fmt.Printf("finished reading input: code=%d\n", int(ret))
			return nil
		}
		if packet == nil {
			continue
		}

		switch packet.media_type {
		case C.AVMEDIA_TYPE_AUDIO:
			if cfg.videoOnly {
				C.free_packet(&packet)
				continue
			}
		case C.AVMEDIA_TYPE_VIDEO:
			if cfg.audioOnly {
				C.free_packet(&packet)
				continue
			}
		default:
			C.free_packet(&packet)
			continue
		}

		if packet.pts <= 0 {
			packet.pts = 1
		}

		if firstPTS == 0 {
			firstPTS = int64(packet.pts)
			startedAt = time.Now()
			totalSendTime = 0
			time.Sleep(50 * time.Millisecond)
			fmt.Printf("starting media stream at pts=%dms\n", firstPTS)
		}

		targetDelay := int64(packet.pts) - firstPTS - totalSendTime
		if targetDelay > 0 {
			sleepFor := time.Duration(min64(targetDelay, 100)) * time.Millisecond
			if cfg.debugSleep {
				fmt.Printf("pacing sleep: %s for packet pts=%d\n", sleepFor, int64(packet.pts))
			}
			time.Sleep(sleepFor)
		}

		switch packet.media_type {
		case C.AVMEDIA_TYPE_AUDIO:
			if err := sendAudioPacket(con, decoder, packet, &frame, audioChunker); err != nil {
				return err
			}
		case C.AVMEDIA_TYPE_VIDEO:
			if err := sendVideoPacket(con, decoder, packet, &frame); err != nil {
				return err
			}
		}
	}
}

func sendAudioPacket(con *agoraservice.RtcConnection, decoder unsafe.Pointer, packet *C.struct__MediaPacket, frame *C.struct__MediaFrame, chunker *audioChunker) error {
	ret := C.decode_packet(decoder, packet, frame)
	C.free_packet(&packet)
	if ret != 0 {
		if ret == C.AVERROR_EAGAIN {
			return nil
		}
		return fmt.Errorf("decode audio packet: %d", int(ret))
	}
	if frame.format != C.AV_SAMPLE_FMT_S16 {
		return fmt.Errorf("unsupported decoded audio sample format: %d", int(frame.format))
	}

	audioData := unsafe.Slice((*byte)(unsafe.Pointer(frame.buffer)), frame.buffer_size)
	sampleRate := int(frame.sample_rate)
	channels := int(frame.channels)

	for _, chunk := range chunker.append(audioData, sampleRate, channels) {
		if rc := con.PushAudioPcmData(chunk, sampleRate, channels, 0); rc != 0 {
			return fmt.Errorf("push audio pcm data: %d", rc)
		}
	}
	return nil
}

func sendVideoPacket(con *agoraservice.RtcConnection, decoder unsafe.Pointer, packet *C.struct__MediaPacket, frame *C.struct__MediaFrame) error {
	ret := C.decode_packet(decoder, packet, frame)
	C.free_packet(&packet)
	if ret != 0 {
		if ret == C.AVERROR_EAGAIN {
			return nil
		}
		return fmt.Errorf("decode video packet: %d", int(ret))
	}
	if frame.format != C.AV_PIX_FMT_YUV420P {
		return fmt.Errorf("unsupported decoded video pixel format: %d", int(frame.format))
	}

	videoData := unsafe.Slice((*byte)(unsafe.Pointer(frame.buffer)), frame.buffer_size)
	videoFrame := &agoraservice.ExternalVideoFrame{
		Type:      agoraservice.VideoBufferRawData,
		Format:    agoraservice.VideoPixelI420,
		Buffer:    videoData,
		Stride:    int(frame.stride),
		Height:    int(frame.height),
		Rotation:  agoraservice.VideoOrientation0,
		Timestamp: int64(frame.pts),
	}

	if rc := con.PushVideoFrame(videoFrame); rc != 0 {
		return fmt.Errorf("push yuv video frame pts=%d: %d", int64(frame.pts), rc)
	}
	return nil
}

func buildToken(appID string, appCert string, channel string, uid string) (string, error) {
	tokenExpirationInSeconds := uint32(3600)
	privilegeExpirationInSeconds := uint32(3600)
	token, err := rtctokenbuilder.BuildTokenWithUserAccount(
		appID,
		appCert,
		channel,
		uid,
		rtctokenbuilder.RolePublisher,
		tokenExpirationInSeconds,
		privilegeExpirationInSeconds,
	)
	if err != nil {
		return "", fmt.Errorf("build token: %w", err)
	}
	return token, nil
}

func envOr(name string, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func min64(a int64, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func (c *audioChunker) append(data []byte, sampleRate int, channels int) [][]byte {
	if c.sampleRate != sampleRate || c.channels != channels {
		c.sampleRate = sampleRate
		c.channels = channels
		c.buffer = c.buffer[:0]
	}

	c.buffer = append(c.buffer, data...)
	bytesPer10ms := (sampleRate / 1000) * 2 * channels * 10
	if bytesPer10ms <= 0 {
		return nil
	}

	var chunks [][]byte
	for len(c.buffer) >= bytesPer10ms {
		chunk := make([]byte, bytesPer10ms)
		copy(chunk, c.buffer[:bytesPer10ms])
		chunks = append(chunks, chunk)
		c.buffer = c.buffer[bytesPer10ms:]
	}
	return chunks
}
