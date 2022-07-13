package lksdk

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v3"
	"go.uber.org/atomic"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/livekit/protocol/livekit"
)

const PROTOCOL = 8

var ErrSignalError = errors.New("signal error")

type SignalClient struct {
	conn      *websocket.Conn
	lock      sync.Mutex
	isClosed  atomic.Bool
	isStarted atomic.Bool

	OnClose                 func()
	OnAnswer                func(sd webrtc.SessionDescription)
	OnOffer                 func(sd webrtc.SessionDescription)
	OnTrickle               func(init webrtc.ICECandidateInit, target livekit.SignalTarget)
	OnParticipantUpdate     func([]*livekit.ParticipantInfo)
	OnLocalTrackPublished   func(response *livekit.TrackPublishedResponse)
	OnSpeakersChanged       func([]*livekit.SpeakerInfo)
	OnConnectionQuality     func([]*livekit.ConnectionQualityInfo)
	OnRoomUpdate            func(room *livekit.Room)
	OnTrackMuted            func(request *livekit.MuteTrackRequest)
	OnLocalTrackUnpublished func(response *livekit.TrackUnpublishedResponse)
	OnTokenRefresh          func(refreshToken string)
	OnLeave                 func()
}

func NewSignalClient() *SignalClient {
	c := &SignalClient{}
	return c
}

func (c *SignalClient) Start() {
	if c.isStarted.Swap(true) {
		return
	}
	go c.readWorker()
}

func (c *SignalClient) IsStarted() bool {
	return c.isStarted.Load()
}

func (c *SignalClient) Join(urlPrefix string, token string, params *ConnectParams) (*livekit.JoinResponse, error) {
	if urlPrefix == "" {
		return nil, ErrURLNotProvided
	}
	urlPrefix = ToWebsocketURL(urlPrefix)
	urlSuffix := fmt.Sprintf("/rtc?protocol=%d&sdk=go&version=%s", PROTOCOL, Version)

	if params.AutoSubscribe {
		urlSuffix += "&auto_subscribe=1"
	} else {
		urlSuffix += "&auto_subscribe=0"
	}

	if params.Reconnect {
		urlSuffix += "&reconnect=1"
	}

	u, err := url.Parse(urlPrefix + urlSuffix)
	if err != nil {
		return nil, err
	}

	header := newHeaderWithToken(token)
	conn, _, err := websocket.DefaultDialer.Dial(u.String(), header)
	if err != nil {
		return nil, fmt.Errorf("%w dial error: %s", ErrSignalError, err.Error())
	}
	c.isClosed.Store(false)
	c.conn = conn

	// server should send join as soon as connected
	res, err := c.ReadResponse()
	if err != nil {
		return nil, err
	}
	join := res.GetJoin()
	if join == nil {
		return nil, fmt.Errorf("unexpected response: %v", res.Message)
	}

	return join, nil
}

func (c *SignalClient) Close() {
	if c.isClosed.Swap(true) {
		return
	}
	if c.conn != nil {
		_ = c.conn.Close()
	}
}

func (c *SignalClient) SendICECandidate(candidate webrtc.ICECandidateInit, target livekit.SignalTarget) error {
	return c.SendRequest(&livekit.SignalRequest{
		Message: &livekit.SignalRequest_Trickle{
			Trickle: ToProtoTrickle(candidate, target),
		},
	})
}

func (c *SignalClient) SendOffer(sd webrtc.SessionDescription) error {
	return c.SendRequest(&livekit.SignalRequest{
		Message: &livekit.SignalRequest_Offer{
			Offer: ToProtoSessionDescription(sd),
		},
	})
}

func (c *SignalClient) SendAnswer(sd webrtc.SessionDescription) error {
	return c.SendRequest(&livekit.SignalRequest{
		Message: &livekit.SignalRequest_Answer{
			Answer: ToProtoSessionDescription(sd),
		},
	})
}

func (c *SignalClient) SendMuteTrack(sid string, muted bool) error {
	return c.SendRequest(&livekit.SignalRequest{
		Message: &livekit.SignalRequest_Mute{
			Mute: &livekit.MuteTrackRequest{
				Sid:   sid,
				Muted: muted,
			},
		},
	})
}

func (c *SignalClient) SendSyncState(state *livekit.SyncState) error {
	return c.SendRequest(&livekit.SignalRequest{
		Message: &livekit.SignalRequest_SyncState{
			SyncState: state,
		},
	})
}

func (c *SignalClient) SendLeave() error {
	return c.SendRequest(&livekit.SignalRequest{
		Message: &livekit.SignalRequest_Leave{
			Leave: &livekit.LeaveRequest{},
		},
	})
}

func (c *SignalClient) SendRequest(req *livekit.SignalRequest) error {
	if c.conn == nil {
		return errors.New("client is not connected")
	}
	payload, err := proto.Marshal(req)
	if err != nil {
		return err
	}

	c.lock.Lock()
	defer c.lock.Unlock()
	return c.conn.WriteMessage(websocket.BinaryMessage, payload)
}

func (c *SignalClient) SendUpdateTrackSettings(settings *livekit.UpdateTrackSettings) error {
	return c.SendRequest(&livekit.SignalRequest{
		Message: &livekit.SignalRequest_TrackSetting{
			TrackSetting: settings,
		},
	})
}

func (c *SignalClient) ReadResponse() (*livekit.SignalResponse, error) {
	if c.conn == nil {
		return nil, errors.New("cannot read response before join")
	}
	if c.isClosed.Load() {
		return nil, io.EOF
	}
	for {
		// handle special messages and pass on the rest
		messageType, payload, err := c.conn.ReadMessage()
		if err != nil {
			return nil, err
		}

		msg := &livekit.SignalResponse{}
		switch messageType {
		case websocket.BinaryMessage:
			// protobuf encoded
			err := proto.Unmarshal(payload, msg)
			return msg, err
		case websocket.TextMessage:
			// json encoded
			err := protojson.Unmarshal(payload, msg)
			return msg, err
		default:
			return nil, nil
		}
	}
}

func (c *SignalClient) handleResponse(res *livekit.SignalResponse) {
	switch msg := res.Message.(type) {
	case *livekit.SignalResponse_Answer:
		if c.OnAnswer != nil {
			c.OnAnswer(FromProtoSessionDescription(msg.Answer))
		}
	case *livekit.SignalResponse_Offer:
		if c.OnOffer != nil {
			c.OnOffer(FromProtoSessionDescription(msg.Offer))
		}
	case *livekit.SignalResponse_Trickle:
		if c.OnTrickle != nil {
			c.OnTrickle(FromProtoTrickle(msg.Trickle), msg.Trickle.Target)
		}
	case *livekit.SignalResponse_Update:
		if c.OnParticipantUpdate != nil {
			c.OnParticipantUpdate(msg.Update.Participants)
		}
	case *livekit.SignalResponse_SpeakersChanged:
		if c.OnSpeakersChanged != nil {
			c.OnSpeakersChanged(msg.SpeakersChanged.Speakers)
		}
	case *livekit.SignalResponse_TrackPublished:
		if c.OnLocalTrackPublished != nil {
			c.OnLocalTrackPublished(msg.TrackPublished)
		}
	case *livekit.SignalResponse_Mute:
		if c.OnTrackMuted != nil {
			c.OnTrackMuted(msg.Mute)
		}
	case *livekit.SignalResponse_ConnectionQuality:
		if c.OnConnectionQuality != nil {
			c.OnConnectionQuality(msg.ConnectionQuality.Updates)
		}
	case *livekit.SignalResponse_RoomUpdate:
		if c.OnRoomUpdate != nil {
			c.OnRoomUpdate(msg.RoomUpdate.Room)
		}
	case *livekit.SignalResponse_Leave:
		if c.OnLeave != nil {
			c.OnLeave()
		}
	case *livekit.SignalResponse_RefreshToken:
		if c.OnTokenRefresh != nil {
			c.OnTokenRefresh(msg.RefreshToken)
		}
	case *livekit.SignalResponse_TrackUnpublished:
		if c.OnLocalTrackUnpublished != nil {
			c.OnLocalTrackUnpublished(msg.TrackUnpublished)
		}
	}
}

func (c *SignalClient) readWorker() {
	defer func() {
		c.isStarted.Store(false)
		if c.OnClose != nil {
			c.OnClose()
		}
	}()
	for !c.isClosed.Load() {
		res, err := c.ReadResponse()
		if err != nil {
			if err != io.EOF {
				logger.Error(err, "error with read worker")
			}
			return
		}
		c.handleResponse(res)
	}
}
