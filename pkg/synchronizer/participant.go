package synchronizer

import (
	"sync"
	"time"

	"github.com/pion/rtcp"
)

// internal struct for managing sender reports
type participantSynchronizer struct {
	sync.Mutex

	ntpStart      time.Time
	tracks        map[uint32]*TrackSynchronizer
	senderReports map[uint32]*rtcp.SenderReport
}

// func (p *participantSynchronizer) onSenderReport(pkt *rtcp.SenderReport) {
// 	p.Lock()
// 	defer p.Unlock()
//
// 	t := p.tracks[pkt.SSRC]
// 	pts, err := t.GetPTS(pkt.RTPTime)
// 	if err != nil {
// 		logger.Debugw("discarding sender report")
// 		return
// 	}
//
// 	p.senderReports[pkt.SSRC] = pkt
// 	if p.ntpStart.IsZero() {
// 		if len(p.senderReports) < len(p.tracks) {
// 			// wait for at least one report per track
// 			return
// 		}
//
// 		// get the max ntp start time for all tracks
// 		var minNTPStart time.Time
// 		ntpStarts := make(map[uint32]time.Time)
// 		for _, report := range p.senderReports {
// 			t := p.tracks[report.SSRC]
// 			pts, err := t.GetPTS(report.RTPTime)
// 			if err != nil {
// 				return
// 			}
// 			ntpStart := mediatransportutil.NtpTime(report.NTPTime).Time().Add(-pts)
// 			if minNTPStart.IsZero() || ntpStart.Before(minNTPStart) {
// 				minNTPStart = ntpStart
// 			}
// 			ntpStarts[report.SSRC] = ntpStart
// 		}
// 		p.ntpStart = minNTPStart
//
// 		// update pts delay so all ntp start times match
// 		for ssrc, ntpStart := range ntpStarts {
// 			t := p.tracks[ssrc]
// 			if diff := ntpStart.Sub(minNTPStart); diff != 0 {
// 				t.Lock()
// 				t.ptsOffset += int64(diff)
// 				t.Unlock()
// 			}
// 		}
// 	} else {
// 		p.tracks[pkt.SSRC].onSenderReport(pkt, pts, p.ntpStart)
// 	}
// }

func (p *participantSynchronizer) getMaxOffset() int64 {
	var maxOffset int64

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

func (p *participantSynchronizer) setMaxPTS(maxPTS time.Duration) {
	p.Lock()
	for _, t := range p.tracks {
		t.Lock()
		t.maxPTS = maxPTS
		t.Unlock()
	}
	p.Unlock()
}
