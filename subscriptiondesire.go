// Copyright 2026 LiveKit, Inc.
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

package lksdk

import (
	"sync"
	"time"

	"github.com/livekit/protocol/livekit"
)

const (
	subscriptionReconcileInterval   = 3 * time.Second
	subscriptionReconcileMaxBackoff = 24 * time.Second

	// desires that never matched a known track are dropped after this long
	subscriptionDesireTTL = 2 * time.Minute

	// an unsubscribe has no client-side observable to confirm convergence:
	// the SDK never clears the attached track on unsubscribe, so
	// IsSubscribed() remains true. Unsubscribe desires are therefore
	// re-asserted a bounded number of times per connection epoch instead of
	// reconciled to convergence.
	maxUnsubscribeAttempts = 3
)

// subscriptionDesire records the intended subscription state for a remote
// track. UpdateSubscription signal messages are fire-and-forget with no
// delivery acknowledgement; recording intent allows the room to re-assert it
// until the actual subscription state converges.
type subscriptionDesire struct {
	subscribe   bool
	updatedAt   time.Time
	lastAttempt time.Time
	attempts    int
}

type subscriptionDesireStore struct {
	mu      sync.Mutex
	desires map[string]*subscriptionDesire
	onDirty func()
}

func newSubscriptionDesireStore() *subscriptionDesireStore {
	return &subscriptionDesireStore{
		desires: make(map[string]*subscriptionDesire),
	}
}

// setOnDirty registers a hook invoked whenever a desire is recorded, allowing
// the reconciler to be started lazily on first use.
func (s *subscriptionDesireStore) setOnDirty(onDirty func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onDirty = onDirty
}

func (s *subscriptionDesireStore) set(trackSID string, subscribe bool) {
	now := time.Now()
	s.mu.Lock()
	s.desires[trackSID] = &subscriptionDesire{
		subscribe:   subscribe,
		updatedAt:   now,
		lastAttempt: now,
	}
	onDirty := s.onDirty
	s.mu.Unlock()

	if onDirty != nil {
		onDirty()
	}
}

func (s *subscriptionDesireStore) desired(trackSID string) (subscribe bool, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d, ok := s.desires[trackSID]; ok {
		return d.subscribe, true
	}
	return false, false
}

func (s *subscriptionDesireStore) clear(trackSID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.desires, trackSID)
}

func (s *subscriptionDesireStore) clearAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.desires = make(map[string]*subscriptionDesire)
}

// dueAttempt reports whether the desire for trackSID diverges from the actual
// subscription state and is due for a re-send, marking the attempt if so.
// Realized desires have their retry backoff reset.
func (s *subscriptionDesireStore) dueAttempt(trackSID string, actual bool, force bool) (due bool, subscribe bool, attempts int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	d, ok := s.desires[trackSID]
	if !ok {
		return false, false, 0
	}
	if d.subscribe == actual {
		d.attempts = 0
		return false, false, 0
	}
	if !d.subscribe && d.attempts >= maxUnsubscribeAttempts {
		return false, false, 0
	}

	backoff := subscriptionReconcileInterval << min(d.attempts, 3)
	if backoff > subscriptionReconcileMaxBackoff {
		backoff = subscriptionReconcileMaxBackoff
	}
	now := time.Now()
	if !force && now.Sub(d.lastAttempt) < backoff {
		return false, false, 0
	}

	d.lastAttempt = now
	d.attempts++
	return true, d.subscribe, d.attempts
}

// resetAttempts starts a fresh assertion epoch, used after reconnects where
// requests sent on the previous connection may have been lost.
func (s *subscriptionDesireStore) resetAttempts() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.desires {
		d.attempts = 0
	}
}

func (s *subscriptionDesireStore) pruneStale(known func(trackSID string) bool) {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for sid, d := range s.desires {
		if !known(sid) && now.Sub(d.updatedAt) > subscriptionDesireTTL {
			delete(s.desires, sid)
		}
	}
}

// -----------------------------------------------------------

// ensureSubscriptionReconciler starts the reconcile loop on first use so that
// rooms which never subscribe selectively never spawn it. Starting after
// cleanup is harmless: the loop observes the broken fuse and exits
// immediately.
func (r *Room) ensureSubscriptionReconciler() {
	if r.subReconcileStop.IsBroken() {
		return
	}
	r.subReconcileStart.Do(func() {
		go r.subscriptionReconcileLoop()
	})
}

func (r *Room) subscriptionReconcileLoop() {
	ticker := time.NewTicker(subscriptionReconcileInterval)
	defer ticker.Stop()

	for {
		select {
		case <-r.subReconcileStop.Watch():
			return
		case <-r.subReconcileKick:
			r.reconcileSubscriptions(true)
		case <-ticker.C:
			r.reconcileSubscriptions(false)
		}
	}
}

// kickSubscriptionReconcile triggers an immediate reconcile pass, bypassing
// the retry backoff. Used after reconnects, where requests in flight on the
// previous connection may have been lost.
func (r *Room) kickSubscriptionReconcile() {
	select {
	case r.subReconcileKick <- struct{}{}:
	default:
	}
}

// reconcileSubscriptions re-sends UpdateSubscription for tracks whose actual
// subscription state has not converged to the recorded desired state.
func (r *Room) reconcileSubscriptions(force bool) {
	if r.ConnectionState() != ConnectionStateConnected {
		return
	}

	store := r.engine.subscriptionDesires
	knownSids := make(map[string]bool)
	subscribe := make(map[string][]string)
	unsubscribe := make(map[string][]string)

	for _, rp := range r.GetRemoteParticipants() {
		for _, t := range rp.TrackPublications() {
			pub, ok := t.(*RemoteTrackPublication)
			if !ok {
				continue
			}
			sid := pub.SID()
			knownSids[sid] = true

			due, sub, attempts := store.dueAttempt(sid, pub.IsSubscribed(), force)
			if !due {
				continue
			}
			if sub {
				subscribe[pub.participantID] = append(subscribe[pub.participantID], sid)
			} else {
				unsubscribe[pub.participantID] = append(unsubscribe[pub.participantID], sid)
			}
			r.log.Infow(
				"subscription not converged, re-sending",
				"trackID", sid,
				"participant", rp.Identity(),
				"subscribe", sub,
				"attempts", attempts,
			)
		}
	}

	store.pruneStale(func(sid string) bool { return knownSids[sid] })

	r.sendSubscriptionUpdate(subscribe, true)
	r.sendSubscriptionUpdate(unsubscribe, false)
}

func (r *Room) sendSubscriptionUpdate(sidsByParticipant map[string][]string, subscribe bool) {
	if len(sidsByParticipant) == 0 {
		return
	}

	participantTracks := make([]*livekit.ParticipantTracks, 0, len(sidsByParticipant))
	var trackSids []string
	for participantSid, sids := range sidsByParticipant {
		participantTracks = append(participantTracks, &livekit.ParticipantTracks{
			ParticipantSid: participantSid,
			TrackSids:      sids,
		})
		trackSids = append(trackSids, sids...)
	}

	if err := r.engine.SendUpdateSubscription(&livekit.UpdateSubscription{
		Subscribe:         subscribe,
		ParticipantTracks: participantTracks,
	}); err != nil {
		r.log.Warnw(
			"could not re-send subscription request", err,
			"trackIDs", trackSids,
			"subscribe", subscribe,
		)
	}
}
