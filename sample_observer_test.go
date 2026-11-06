package lksdk

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/pion/webrtc/v4/pkg/media"
)

type observerTestProvider struct {
	sample media.Sample
	done   bool
}

func (p *observerTestProvider) OnBind() error   { return nil }
func (p *observerTestProvider) OnUnbind() error { return nil }
func (p *observerTestProvider) Close() error    { return nil }
func (p *observerTestProvider) NextSample(context.Context) (media.Sample, error) {
	if p.done {
		return media.Sample{}, io.EOF
	}
	p.done = true
	return p.sample, nil
}

type observerTestRecorder struct {
	mu     sync.Mutex
	reads  int
	writes []SampleWriteResult
	lags   []time.Duration
}

func (o *observerTestRecorder) OnSampleRead(media.Sample, time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.reads++
}
func (o *observerTestRecorder) OnSampleWriteComplete(_ media.Sample, result SampleWriteResult) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.writes = append(o.writes, result)
}
func (o *observerTestRecorder) OnSamplePacingLag(_ media.Sample, lag time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.lags = append(o.lags, lag)
}

func TestWriteWorkerNotifiesSampleObserver(t *testing.T) {
	recorder := &observerTestRecorder{}
	track := &LocalTrack{sampleObserver: recorder}
	done := make(chan struct{})
	track.writeWorker(&observerTestProvider{sample: media.Sample{Data: []byte{1}, Duration: 0}}, func() { close(done) })
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("write worker did not complete")
	}
	if recorder.reads != 1 {
		t.Fatalf("reads = %d, want 1", recorder.reads)
	}
	if len(recorder.writes) != 1 || recorder.writes[0].Err != nil || recorder.writes[0].Skipped {
		t.Fatalf("unexpected write result: %+v", recorder.writes)
	}
	if len(recorder.lags) != 1 {
		t.Fatalf("lags = %d, want 1", len(recorder.lags))
	}
}

func TestWriteWorkerReportsMutedSampleAsSkipped(t *testing.T) {
	recorder := &observerTestRecorder{}
	track := &LocalTrack{sampleObserver: recorder}
	track.muted.Store(true)
	track.writeWorker(&observerTestProvider{sample: media.Sample{Data: []byte{1}}}, nil)
	if len(recorder.writes) != 1 || !recorder.writes[0].Skipped {
		t.Fatalf("unexpected write result: %+v", recorder.writes)
	}
}

func TestReaderTrackWithSampleObserverAddsTrackOption(t *testing.T) {
	recorder := &observerTestRecorder{}
	provider := &ReaderSampleProvider{}
	ReaderTrackWithSampleObserver(recorder)(provider)
	track := &LocalTrack{}
	for _, option := range provider.trackOpts {
		option(track)
	}
	if track.sampleObserver != recorder {
		t.Fatal("sample observer was not applied to track")
	}
}
