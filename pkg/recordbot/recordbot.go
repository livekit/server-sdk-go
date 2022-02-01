package recordbot

import (
	"errors"
	"log"
	"sync"

	lksdk "github.com/livekit/server-sdk-go"
	"github.com/pion/webrtc/v3"
)

var ErrFilenameCannotBeEmpty = errors.New("file name cannot be empty")
var ErrGenerateFilenameCannotBeNil = errors.New("GenerateFilename() cannot be nil")

type RecordBot interface {
	Disconnect()
	handleTrackSubscribed(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant)
	handleTrackUnsubscribed(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant)
}

type recordbot struct {
	lock       sync.Mutex
	recorders  map[string]Recorder
	room       *lksdk.Room
	hooks      RecordBotHooks
	outputType OutputType
}

type RecordBotConfig struct {
	BaseUrl    string
	ApiKey     string
	ApiSecret  string
	BotName    string
	Hooks      RecordBotHooks
	OutputType OutputType
}

type RecordBotHooks struct {
	// Called on LiveKit's OnTrackSubscribed event handler. This function is used for naming the file
	GenerateFilename func(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) string

	// Include recorder hooks
	Recorder RecorderHooks
}

type OutputType string

var (
	OutputSingleTrackAV OutputType = "single track AV"
	OutputVideoOnly     OutputType = OutputType(webrtc.RTPCodecTypeVideo.String())
	OutputAudioOnly     OutputType = OutputType(webrtc.RTPCodecTypeAudio.String())
)

func NewRecordBot(roomName string, config RecordBotConfig) (RecordBot, error) {
	// Sanitise config
	if config.Hooks.GenerateFilename == nil {
		return nil, ErrGenerateFilenameCannotBeNil
	}

	// Set default name for `recordbot` if not configured
	botName := config.BotName
	if botName == "" {
		botName = "recordbot"
	}

	// Create the bot
	bot := &recordbot{
		lock:       sync.Mutex{},
		recorders:  make(map[string]Recorder),
		room:       nil,
		hooks:      config.Hooks,
		outputType: config.OutputType,
	}

	// Connect to room
	room, err := lksdk.ConnectToRoom(config.BaseUrl, lksdk.ConnectInfo{
		APIKey:              config.ApiKey,
		APISecret:           config.ApiSecret,
		RoomName:            roomName,
		ParticipantIdentity: botName,
	})
	if err != nil {
		return nil, err
	}

	// Attach callback to record all remote participants
	room.Callback.OnTrackSubscribed = bot.handleTrackSubscribed

	// Attach callback to handle closing file when participant disconnects
	room.Callback.OnTrackUnsubscribed = bot.handleTrackUnsubscribed

	// Don't forget to update the room instance
	bot.room = room

	return bot, nil
}

func (b *recordbot) Disconnect() {
	// Signal all recorders to stop
	if b.recorders != nil {
		b.lock.Lock()
		for _, rec := range b.recorders {
			rec.Stop()
		}
		b.lock.Unlock()
	}

	// Disconnect from room
	if b.room != nil {
		b.room.Disconnect()
	}
}

func (b *recordbot) handleTrackSubscribed(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
	// If the preferred output type is not Split AV (we want both tracks), and
	// the track type (video or audio only) does not match our preferred output type, return
	trackOutputType := OutputType(track.Kind().String())
	if b.outputType != OutputSingleTrackAV && b.outputType != trackOutputType {
		return
	}

	// Generate filename via custom hook
	fileName := b.hooks.GenerateFilename(track, publication, rp)
	if fileName == "" {
		log.Println(ErrFilenameCannotBeEmpty)
		return
	}

	// Create recorder
	rec, err := NewSingleTrackRecorder(fileName, track.Codec(), b.hooks.Recorder)
	if err != nil {
		log.Println("fail to initialise recorder")
		return
	}

	// Start recording
	rec.Start(track)

	// Remember to attach the recorder to list of existing recorders
	b.lock.Lock()
	b.recorders[publication.SID()] = rec
	b.lock.Unlock()
}

func (b *recordbot) handleTrackUnsubscribed(_ *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, _ *lksdk.RemoteParticipant) {
	// Try getting the recorder
	b.lock.Lock()
	rec, found := b.recorders[publication.SID()]
	b.lock.Unlock()

	// If not found, stop
	if !found {
		return
	}

	// Stop recording
	rec.Stop()

	// Remove recorder from list
	b.lock.Lock()
	delete(b.recorders, publication.SID())
	b.lock.Unlock()
}
