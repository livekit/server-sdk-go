package e2ee_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/livekit/protocol/livekit"

	"github.com/livekit/server-sdk-go/v2/e2ee"
)

func TestDataCryptorRoundTripUser(t *testing.T) {
	dc := newTestDataCryptor(t)

	original := &livekit.DataPacket{
		Value: &livekit.DataPacket_User{
			User: &livekit.UserPacket{Payload: []byte("hello encrypted world")},
		},
	}

	encrypted, err := dc.Encrypt(original)
	require.NoError(t, err)

	ep, ok := encrypted.Value.(*livekit.DataPacket_EncryptedPacket)
	require.True(t, ok)
	require.NotEmpty(t, ep.EncryptedPacket.EncryptedValue)
	require.NotEmpty(t, ep.EncryptedPacket.Iv)

	payload, err := dc.Decrypt(ep.EncryptedPacket)
	require.NoError(t, err)

	user, ok := payload.Value.(*livekit.EncryptedPacketPayload_User)
	require.True(t, ok)
	require.Equal(t, []byte("hello encrypted world"), user.User.Payload)
}

func TestDataCryptorRoundTripChatMessage(t *testing.T) {
	dc := newTestDataCryptor(t)

	original := &livekit.DataPacket{
		Value: &livekit.DataPacket_ChatMessage{
			ChatMessage: &livekit.ChatMessage{Id: "msg-1", Message: "hi"},
		},
	}

	encrypted, err := dc.Encrypt(original)
	require.NoError(t, err)

	payload, err := dc.Decrypt(encrypted.Value.(*livekit.DataPacket_EncryptedPacket).EncryptedPacket)
	require.NoError(t, err)

	chat, ok := payload.Value.(*livekit.EncryptedPacketPayload_ChatMessage)
	require.True(t, ok)
	require.Equal(t, "msg-1", chat.ChatMessage.Id)
	require.Equal(t, "hi", chat.ChatMessage.Message)
}

func TestDataCryptorNonEncryptableTypePassesThrough(t *testing.T) {
	dc := newTestDataCryptor(t)

	// SipDtmf is not in the EncryptedPacketPayload set; should pass through unchanged.
	original := &livekit.DataPacket{
		Value: &livekit.DataPacket_SipDtmf{
			SipDtmf: &livekit.SipDTMF{Digit: "1"},
		},
	}

	encrypted, err := dc.Encrypt(original)
	require.NoError(t, err)
	require.Same(t, original, encrypted)
}

func TestDataCryptorDecryptEmptyRejected(t *testing.T) {
	dc := newTestDataCryptor(t)

	_, err := dc.Decrypt(&livekit.EncryptedPacket{Iv: nil, EncryptedValue: []byte("x")})
	require.Error(t, err)

	_, err = dc.Decrypt(&livekit.EncryptedPacket{Iv: []byte("iv"), EncryptedValue: nil})
	require.Error(t, err)
}

func TestDataCryptorWrongKeyFails(t *testing.T) {
	// Encrypt with key A, decrypt with key B under same index — must fail.
	kpA := e2ee.NewExternalKeyProvider()
	require.NoError(t, kpA.SetRawKey(bytes16(0x11), 0))
	dcA := e2ee.NewDataCryptor(kpA)

	kpB := e2ee.NewExternalKeyProvider()
	require.NoError(t, kpB.SetRawKey(bytes16(0x22), 0))
	dcB := e2ee.NewDataCryptor(kpB)

	original := &livekit.DataPacket{
		Value: &livekit.DataPacket_User{User: &livekit.UserPacket{Payload: []byte("secret")}},
	}
	encrypted, err := dcA.Encrypt(original)
	require.NoError(t, err)

	_, err = dcB.Decrypt(encrypted.Value.(*livekit.DataPacket_EncryptedPacket).EncryptedPacket)
	require.Error(t, err)
}

func TestDataCryptorMultiKeyCache(t *testing.T) {
	// Two keys at different indices; decrypt finds the right cached block.
	kp := e2ee.NewExternalKeyProvider()
	require.NoError(t, kp.SetRawKey(bytes16(0xAA), 0))
	require.NoError(t, kp.SetRawKey(bytes16(0xBB), 1))
	dc := e2ee.NewDataCryptor(kp)

	// Encrypt under key 1 (current).
	pkt := &livekit.DataPacket{Value: &livekit.DataPacket_User{User: &livekit.UserPacket{Payload: []byte("p1")}}}
	encAt1, err := dc.Encrypt(pkt)
	require.NoError(t, err)
	require.Equal(t, uint32(1), encAt1.Value.(*livekit.DataPacket_EncryptedPacket).EncryptedPacket.KeyIndex)

	// Switch current to 0, encrypt a new packet.
	require.NoError(t, kp.SetRawKey(bytes16(0xAA), 0))
	pkt2 := &livekit.DataPacket{Value: &livekit.DataPacket_User{User: &livekit.UserPacket{Payload: []byte("p0")}}}
	encAt0, err := dc.Encrypt(pkt2)
	require.NoError(t, err)
	require.Equal(t, uint32(0), encAt0.Value.(*livekit.DataPacket_EncryptedPacket).EncryptedPacket.KeyIndex)

	// Both decrypt successfully.
	p1, err := dc.Decrypt(encAt1.Value.(*livekit.DataPacket_EncryptedPacket).EncryptedPacket)
	require.NoError(t, err)
	require.Equal(t, []byte("p1"), p1.Value.(*livekit.EncryptedPacketPayload_User).User.Payload)

	p0, err := dc.Decrypt(encAt0.Value.(*livekit.DataPacket_EncryptedPacket).EncryptedPacket)
	require.NoError(t, err)
	require.Equal(t, []byte("p0"), p0.Value.(*livekit.EncryptedPacketPayload_User).User.Payload)
}

func newTestDataCryptor(t *testing.T) *e2ee.DataCryptor {
	t.Helper()
	kp := e2ee.NewExternalKeyProvider()
	require.NoError(t, kp.SetKeyFromPassphrase("12345", 0))
	return e2ee.NewDataCryptor(kp)
}
