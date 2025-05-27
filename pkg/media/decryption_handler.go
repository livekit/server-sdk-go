package media

import (
	"crypto/aes"
	"crypto/cipher"

	"github.com/livekit/media-sdk/rtp"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

type Decryptor interface {
	DecryptSample(payload []byte) ([]byte, error)
}

type GCMDecryptor struct {
	cipherBlock cipher.Block
	sifTrailer  []byte
}

func NewGCMDecryptor(key []byte, sifTrailer []byte) (*GCMDecryptor, error) {
	cipherBlock, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	return &GCMDecryptor{cipherBlock: cipherBlock, sifTrailer: sifTrailer}, nil
}

func (d *GCMDecryptor) DecryptSample(payload []byte) ([]byte, error) {
	return lksdk.DecryptGCMAudioSampleCustomCipher(payload, d.sifTrailer, d.cipherBlock)
}

type CustomDecryptor struct {
	decryptionFunc func(payload []byte, sifTrailer []byte) ([]byte, error)
	sifTrailer     []byte
}

func NewCustomDecryptor(decryptionFunc func(payload []byte, sifTrailer []byte) ([]byte, error), sifTrailer []byte) *CustomDecryptor {
	return &CustomDecryptor{decryptionFunc: decryptionFunc, sifTrailer: sifTrailer}
}

func (d *CustomDecryptor) DecryptSample(payload []byte) ([]byte, error) {
	return d.decryptionFunc(payload, d.sifTrailer)
}

type DecryptionHandler struct {
	handler   rtp.Handler
	decryptor Decryptor
}

func NewDecryptionHandler(h rtp.Handler, decryptor Decryptor) *DecryptionHandler {
	return &DecryptionHandler{
		handler:   h,
		decryptor: decryptor,
	}
}

func (d *DecryptionHandler) HandleRTP(h *rtp.Header, payload []byte) error {
	sample, err := d.decryptor.DecryptSample(payload)
	if err != nil {
		return err
	}

	if sample == nil {
		// drop server injected frames
		return nil
	}
	return d.handler.HandleRTP(h, sample)
}

func (d *DecryptionHandler) String() string {
	return "DecryptionHandler " + d.handler.String()
}
