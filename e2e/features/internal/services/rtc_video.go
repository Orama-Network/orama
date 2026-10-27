//go:build e2e_fleet

package services

import (
	"fmt"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

const (
	vp8Payload      = 96
	videoClockRate  = 90000
	videoTickStride = 3000
	videoSSRC       = 0xE2E1
)

// StartVideo is Start(false) plus a published video track (addVideo).
func (p *RTCPeer) StartVideo() error {
	if err := p.Start(false); err != nil {
		return err
	}
	if err := p.addVideo(); err != nil {
		return err
	}
	return p.sendOffer()
}

// StartAudioVideo is Start(false) plus a published audio track and a published
// video track, offered together, as a client with its microphone and camera on.
func (p *RTCPeer) StartAudioVideo() error {
	if err := p.Start(false); err != nil {
		return err
	}
	if err := p.addAudio(); err != nil {
		return err
	}
	if err := p.addVideo(); err != nil {
		return err
	}
	return p.sendOffer()
}

// addVideo adds a VP8 track to the peer's connection, for its next offer, and
// reads the RTCP the SFU sends back for it: KeyframeRequests counts the PLI and
// FIR that reach the publisher (website/src/docs/developer/webrtc.mdx#keyframes-plifir).
func (p *RTCPeer) addVideo() error {
	track, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: videoClockRate}, "video", "e2e-video-"+p.PeerID)
	if err != nil {
		return fmt.Errorf("create the video track: %w", err)
	}
	sender, err := p.pc.AddTrack(track)
	if err != nil {
		return fmt.Errorf("add the video track: %w", err)
	}
	p.videoTrack = track
	go func() {
		for {
			packets, _, err := sender.ReadRTCP()
			if err != nil {
				return
			}
			for _, pkt := range packets {
				switch pkt.(type) {
				case *rtcp.PictureLossIndication, *rtcp.FullIntraRequest:
					p.plis.Add(1)
				}
			}
		}
	}()
	return nil
}

// PublishVideo writes a burst of RTP packets on the video track.
func (p *RTCPeer) PublishVideo() error {
	if p.videoTrack == nil {
		return fmt.Errorf("this peer publishes no video track")
	}
	for i := 0; i < rtcPacketBurst; i++ {
		p.videoSeq++
		pkt := &rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: vp8Payload, SequenceNumber: p.videoSeq,
			Timestamp: uint32(p.videoSeq) * videoTickStride, SSRC: videoSSRC, Marker: true}, Payload: []byte{0x10, 0x00, 0x00}}
		if err := p.videoTrack.WriteRTP(pkt); err != nil {
			return fmt.Errorf("write video RTP: %w", err)
		}
	}
	return nil
}

// RequestKeyframe sends a PLI for the video track received from the room, as a
// subscriber that lost a frame does. It fails until a video track arrived.
func (p *RTCPeer) RequestKeyframe() error {
	ssrc := p.videoSSRC.Load()
	if ssrc == 0 {
		return fmt.Errorf("no video track has been received yet")
	}
	return p.pc.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: ssrc}})
}

// KeyframeRequests is how many PLI/FIR the SFU relayed to this publisher.
func (p *RTCPeer) KeyframeRequests() int64 { return p.plis.Load() }
