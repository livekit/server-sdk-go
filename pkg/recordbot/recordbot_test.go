package recordbot

import (
	"testing"

	lksdk "github.com/livekit/server-sdk-go"
	"github.com/pion/webrtc/v3"
	"github.com/stretchr/testify/assert"
)

func mockRecordBotConfig() RecordBotConfig {
	return RecordBotConfig{
		BaseUrl:   "wss://localhost:7880",
		ApiKey:    "INSECURE-KEY",
		ApiSecret: "INSECURE-SECRET",
		BotName:   "recordbot",
		Hooks: RecordBotHooks{
			GenerateFilename: func(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) string {
				return rp.Identity()
			},
		},
		OutputType: OutputVideoOnly,
	}
}

func TestErrorWhenGenerateFilenameIsNil(t *testing.T) {
	config := mockRecordBotConfig()
	config.Hooks.GenerateFilename = nil
	bot, err := NewRecordBot("myroom", config)

	assert.Nil(t, bot)
	assert.ErrorIs(t, err, ErrGenerateFilenameCannotBeNil)
}
