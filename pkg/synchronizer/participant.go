// Copyright 2023 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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
	firstReport   time.Time
	tracks        map[uint32]*TrackSynchronizer
	senderReports map[uint32]*rtcp.SenderReport
}

func newParticipantSynchronizer() *participantSynchronizer {
	return &participantSynchronizer{
		tracks:        make(map[uint32]*TrackSynchronizer),
		senderReports: make(map[uint32]*rtcp.SenderReport),
	}
}

func (p *participantSynchronizer) onSenderReport(pkt *rtcp.SenderReport) {
	p.Lock()
	defer p.Unlock()

	if p.ntpStart.IsZero() {
		p.initialize(pkt)
	} else if t := p.tracks[pkt.SSRC]; t != nil {
		t.onSenderReport(pkt, p.ntpStart)
	}
}

func (p *participantSynchronizer) initialize(pkt *rtcp.SenderReport) {
	if p.firstReport.IsZero() {
		p.firstReport = time.Now()
	}

	p.senderReports[pkt.SSRC] = pkt
	if len(p.senderReports) < len(p.tracks) && time.Since(p.firstReport) < 5*time.Second {
		return
	}

	// update ntp start time
	for ssrc, report := range p.senderReports {
		if t := p.tracks[ssrc]; t != nil {
			pts := t.getSenderReportPTS(report)
			ntpStart := mediatransportutil.NtpTime(report.NTPTime).Time().Add(-pts)
			if p.ntpStart.IsZero() || ntpStart.Before(p.ntpStart) {
				p.ntpStart = ntpStart
			}
		}
	}

	// call onSenderReport for all tracks
	for ssrc, report := range p.senderReports {
		if t := p.tracks[ssrc]; t != nil {
			t.onSenderReport(report, p.ntpStart)
		}
	}
}

func (p *participantSynchronizer) getMaxOffset() time.Duration {
	var maxOffset time.Duration

	p.Lock()
	for _, t := range p.tracks {
		t.Lock()
		o := t.ptsOffset
		t.Unlock()

		if o > maxOffset {
			maxOffset = o
		}
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
