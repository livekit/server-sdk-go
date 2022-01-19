package recordbot

import (
	"errors"
	"fmt"
	"log"
	"strings"

	lksdk "github.com/livekit/server-sdk-go"
	"github.com/pion/webrtc/v3"
)

var ErrRecorderNotFound = errors.New("recorder not found")
var ErrIdentityCannotBeEmpty = errors.New("identity cannot be empty")
var ErrFilenameCannotBeEmpty = errors.New("file name cannot be empty")

type RecordBot interface {
	Disconnect()
	handleTrackSubscribed(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant)
	handleTrackUnsubscribed(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant)
}

type recordbot struct {
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
	// Called on LiveKit's OnTrackSubscribed event handler. File ID should not contain `.` character
	GenerateFileID func(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) string

	// Include recorder hooks
	Recorder RecorderHooks
}

type OutputType string

var (
	OutputSplitAV   OutputType = "split AV"
	OutputVideoOnly OutputType = OutputType(webrtc.RTPCodecTypeVideo.String())
	OutputAudioOnly OutputType = OutputType(webrtc.RTPCodecTypeAudio.String())
)

func NewRecordBot(roomName string, config RecordBotConfig) (RecordBot, error) {
	// Set default name for `recordbot` if not configured
	botName := config.BotName
	if botName == "" {
		botName = "recordbot"
	}

	// Create the bot
	bot := &recordbot{
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
		for _, rec := range b.recorders {
			rec.Stop()
		}
	}

	// Disconnect from room
	if b.room != nil {
		b.room.Disconnect()
	}
}

func (bot *recordbot) handleTrackSubscribed(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
	// If the preferred output type is not Split AV (we want both tracks), and
	// the track type (video or audio only) does not match our preferred output type, return
	trackOutputType := OutputType(track.Kind().String())
	if bot.outputType != OutputSplitAV && bot.outputType != trackOutputType {
		return
	}

	// Generate ID for recorder. E.g. : `user1/video`, where "user1" is the identity and "video" is the track type
	if rp.Identity() == "" {
		log.Fatal("identity cannot be empty")
	}
	recorderID, err := generateRecorderID(rp.Identity(), track.Kind())
	if err != nil {
		log.Fatal("recorder ID is empty")
	}

	// Generate filename for recorder. File ID is obtained via hooks
	if bot.hooks.GenerateFileID == nil {
		log.Fatal("must specify file ID generator")
	}
	fileID := bot.hooks.GenerateFileID(track, publication, rp)
	if fileID == "" {
		log.Fatal("file id is empty")
	} else if strings.Contains(fileID, ".") {
		log.Fatal("file id contains `.`")
	}
	fileExtension := getFileExtension(track.Kind())
	if fileExtension == "" {
		log.Fatal("file extension is empty")
	}
	fileName := fmt.Sprintf("%s.%s", fileID, fileExtension)

	// Create recorder
	rec, err := NewRecorder(fileName, track.Codec(), bot.hooks.Recorder)
	if err != nil {
		log.Fatal("fail to initialise recorder")
	}

	// Start recording
	rec.Start(track)

	// Remember to attach the recorder to list of existing recorders
	bot.recorders[recorderID] = rec
}

func (bot *recordbot) handleTrackUnsubscribed(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
	// Generate recorder ID for access
	recorderID, err := generateRecorderID(rp.Identity(), track.Kind())
	if err != nil {
		return
	}

	// Try getting the recorder
	rec, found := bot.recorders[recorderID]
	if !found {
		return
	}

	// Stop recording
	rec.Stop()

	// Remove recorder from list
	delete(bot.recorders, recorderID)
}

func generateRecorderID(identity string, codecType webrtc.RTPCodecType) (string, error) {
	if identity == "" {
		return "", ErrIdentityCannotBeEmpty
	} else if codecType != webrtc.RTPCodecTypeVideo && codecType != webrtc.RTPCodecTypeAudio {
		return "", ErrUnsupportedCodec
	}

	return fmt.Sprintf("%s/%s", identity, codecType.String()), nil
}
