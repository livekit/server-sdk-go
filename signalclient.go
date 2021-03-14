package sdk

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"sync/atomic"

	"github.com/gorilla/websocket"
	"github.com/livekit/protocol/auth"
	"github.com/pion/webrtc/v3"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	livekit "github.com/livekit/livekit-sdk-go/proto"
)

type SignalClient struct {
	conn        *websocket.Conn
	isConnected atomic.Value
	lock        sync.Mutex

	OnClose                 func()
	OnAnswer                func(sd webrtc.SessionDescription)
	OnOffer                 func(sd webrtc.SessionDescription)
	OnTrickle               func(init webrtc.ICECandidateInit, target livekit.SignalTarget)
	OnParticipantUpdate     func([]*livekit.ParticipantInfo)
	OnLocalTrackPublished   func(response *livekit.TrackPublishedResponse)
	OnActiveSpeakersChanged func([]*livekit.SpeakerInfo)
}

type ConnectInfo struct {
	APIKey              string
	APISecret           string
	RoomName            string
	ParticipantIdentity string
	ParticipantMetadata map[string]interface{}
}

func NewSignalClient() *SignalClient {
	c := &SignalClient{}
	c.isConnected.Store(false)
	return c
}

func (c *SignalClient) Join(url string, info ConnectInfo) (*livekit.JoinResponse, error) {
	// generate token
	at := auth.NewAccessToken(info.APIKey, info.APISecret)
	grant := &auth.VideoGrant{
		RoomJoin: true,
		Room:     info.RoomName,
	}
	at.AddGrant(grant).
		SetIdentity(info.ParticipantIdentity).
		SetMetadata(info.ParticipantMetadata)

	token, err := at.ToJWT()
	if err != nil {
		return nil, err
	}

	return c.JoinWithToken(url, token)
}

func (c *SignalClient) JoinWithToken(urlPrefix string, token string) (*livekit.JoinResponse, error) {
	u, err := url.Parse(urlPrefix + "/rtc")
	if err != nil {
		return nil, err
	}

	header := newHeaderWithToken(token)
	conn, _, err := websocket.DefaultDialer.Dial(u.String(), header)
	if err != nil {
		return nil, err
	}
	c.conn = conn
	conn.SetCloseHandler(func(code int, text string) error {
		if c.OnClose != nil {
			c.OnClose()
			c.lock.Lock()
			c.conn = nil
			c.lock.Unlock()
		}
		return nil
	})

	// server should send join as soon as connected
	res, err := c.ReadResponse()
	if err != nil {
		return nil, err
	}
	join := res.GetJoin()
	if join == nil {
		return nil, fmt.Errorf("unexpected response: %v", res.Message)
	}

	go c.readWorker()

	return join, nil
}

func (c *SignalClient) SendRequest(req *livekit.SignalRequest) error {
	if c.conn == nil {
		return errors.New("client is not connected")
	}
	payload, err := protojson.Marshal(req)
	if err != nil {
		return err
	}
	return c.conn.WriteMessage(websocket.BinaryMessage, payload)
}

func (c *SignalClient) ReadResponse() (*livekit.SignalResponse, error) {
	if c.conn == nil {
		return nil, errors.New("cannot read response before join")
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

func (c *SignalClient) IsConnected() bool {
	return c.isConnected.Load().(bool)
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
	case *livekit.SignalResponse_Speaker:
		if c.OnActiveSpeakersChanged != nil {
			c.OnActiveSpeakersChanged(msg.Speaker.Speakers)
		}
	case *livekit.SignalResponse_TrackPublished:
		if c.OnLocalTrackPublished != nil {
			c.OnLocalTrackPublished(msg.TrackPublished)
		}
	}
}

func (c *SignalClient) readWorker() {
	conn := c.conn
	for conn != nil && c.IsConnected() {
		res, err := c.ReadResponse()
		if err != nil {
			return
		}
		c.handleResponse(res)

		c.lock.Lock()
		conn = c.conn
		c.lock.Unlock()
	}
}

func ToProtoSessionDescription(sd webrtc.SessionDescription) *livekit.SessionDescription {
	return &livekit.SessionDescription{
		Type: sd.Type.String(),
		Sdp:  sd.SDP,
	}
}

func FromProtoSessionDescription(sd *livekit.SessionDescription) webrtc.SessionDescription {
	var sdType webrtc.SDPType
	switch sd.Type {
	case webrtc.SDPTypeOffer.String():
		sdType = webrtc.SDPTypeOffer
	case webrtc.SDPTypeAnswer.String():
		sdType = webrtc.SDPTypeAnswer
	case webrtc.SDPTypePranswer.String():
		sdType = webrtc.SDPTypePranswer
	case webrtc.SDPTypeRollback.String():
		sdType = webrtc.SDPTypeRollback
	}
	return webrtc.SessionDescription{
		Type: sdType,
		SDP:  sd.Sdp,
	}
}

func ToProtoTrickle(candidateInit webrtc.ICECandidateInit) *livekit.TrickleRequest {
	data, _ := json.Marshal(candidateInit)
	return &livekit.TrickleRequest{
		CandidateInit: string(data),
	}
}

func FromProtoTrickle(trickle *livekit.TrickleRequest) webrtc.ICECandidateInit {
	ci := webrtc.ICECandidateInit{}
	json.Unmarshal([]byte(trickle.CandidateInit), &ci)
	return ci
}
