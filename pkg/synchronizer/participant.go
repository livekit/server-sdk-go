package synchronizer

import (
	"sync"
	"time"

	"github.com/pion/rtcp"

	"github.com/livekit/mediatransportutil"
)

// internal struct for managing sender reports
type participantSynchronizer struct {
	sync.Mutex

	ntpStart      time.Time
	tracks        map[uint32]*TrackSynchronizer
	senderReports map[uint32]*rtcp.SenderReport
}

func (p *participantSynchronizer) onSenderReport(pkt *rtcp.SenderReport) {
	p.Lock()
	defer p.Unlock()

	if p.ntpStart.IsZero() {
		p.senderReports[pkt.SSRC] = pkt
		if len(p.senderReports) == len(p.tracks) {
			p.synchronizeTracks()
		}
		return
	}

	if t := p.tracks[pkt.SSRC]; t != nil {
		t.onSenderReport(pkt, p.ntpStart)
	}
}

func (p *participantSynchronizer) synchronizeTracks() {
	// get estimated ntp start times for all tracks
	estimatedStartTimes := make(map[uint32]time.Time)

	// we will sync all tracks to the earliest
	var earliestStart time.Time
	for ssrc, pkt := range p.senderReports {
		t := p.tracks[ssrc]
		pts := t.getSenderReportPTS(pkt)
		ntpStart := mediatransportutil.NtpTime(pkt.NTPTime).Time().Add(-pts)
		if earliestStart.IsZero() || ntpStart.Before(earliestStart) {
			earliestStart = ntpStart
		}
		estimatedStartTimes[ssrc] = ntpStart
	}
	p.ntpStart = earliestStart

	// update pts delay so all ntp start times will match the earliest
	for ssrc, startedAt := range estimatedStartTimes {
		t := p.tracks[ssrc]
		if diff := startedAt.Sub(earliestStart); diff != 0 {
			t.Lock()
			t.ptsOffset += diff
			t.Unlock()
		}
	}
}

func (p *participantSynchronizer) getMaxOffset() time.Duration {
	var maxOffset time.Duration

	p.Lock()
	for _, t := range p.tracks {
		t.Lock()
		if o := t.ptsOffset; o > maxOffset {
			maxOffset = o
		}
		t.Unlock()
	}
	p.Unlock()

	return maxOffset
}

func (p *participantSynchronizer) drain(maxPTS time.Duration) {
	p.Lock()
	for _, t := range p.tracks {
		t.Lock()
		t.maxPTS = maxPTS
		t.Unlock()
	}
	p.Unlock()
}
