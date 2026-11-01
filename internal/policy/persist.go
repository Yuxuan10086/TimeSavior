package policy

import (
	"log"
	"time"

	"timesavior/internal/store"
)

// RuntimeStore persists FullBlock / unlock quotas / watch sessions.
type RuntimeStore interface {
	LoadRuntime() (store.RuntimeSnapshot, error)
	SaveRuntime(store.RuntimeSnapshot) error
}

// AttachStore enables load-on-start and debounced save of runtime state.
func (e *Engine) AttachStore(s RuntimeStore) {
	if e == nil || s == nil {
		return
	}
	e.mu.Lock()
	e.runtimeStore = s
	e.mu.Unlock()
	if err := e.LoadPersisted(time.Now()); err != nil {
		log.Printf("policy: load persisted state: %v", err)
	}
}

// LoadPersisted restores FullBlock, unlock quotas, and watch session timers.
func (e *Engine) LoadPersisted(now time.Time) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.runtimeStore == nil {
		return nil
	}
	snap, err := e.runtimeStore.LoadRuntime()
	if err != nil {
		return err
	}
	e.policyDay = snap.Unlock.DayKey
	e.tempUnlockCount = snap.Unlock.DayCount
	e.tempUnlockWeekKey = snap.Unlock.WeekKey
	e.tempUnlockWeekCount = snap.Unlock.WeekCount
	e.tempUnlockUntil = snap.Unlock.TempUnlockUntil
	e.tempUnlockStartedAt = snap.Unlock.TempUnlockStartedAt
	for _, s := range snap.Sites {
		st := e.getSite(s.Site)
		st.fullBlockUntil = s.FullBlockUntil
		st.watchStarted = s.WatchStarted
		st.lastPlayAt = s.LastPlayAt
		st.watchExempt = s.WatchExempt
		st.exemptLabel = s.ExemptLabel
		st.exemptUntil = s.ExemptUntil
	}
	// Apply day/week rolls so stale persisted counters clear if boundary passed.
	e.rollPolicyDay(now)
	return nil
}

// FlushPersist writes runtime state immediately (call on shutdown).
func (e *Engine) FlushPersist() {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.cancelPersistTimerLocked()
	snap := e.snapshotLocked()
	store := e.runtimeStore
	e.mu.Unlock()
	if store == nil {
		return
	}
	if err := store.SaveRuntime(snap); err != nil {
		log.Printf("policy: flush persist: %v", err)
	}
}

func (e *Engine) schedulePersistLocked() {
	e.notifyLocked(ChangeStatus, false)
	if e.runtimeStore == nil {
		return
	}
	if e.persistTimer != nil {
		e.persistTimer.Stop()
	}
	e.persistTimer = time.AfterFunc(time.Second, func() {
		e.mu.Lock()
		e.persistTimer = nil
		snap := e.snapshotLocked()
		store := e.runtimeStore
		e.mu.Unlock()
		if store == nil {
			return
		}
		if err := store.SaveRuntime(snap); err != nil {
			log.Printf("policy: persist: %v", err)
		}
	})
}

func (e *Engine) cancelPersistTimerLocked() {
	if e.persistTimer != nil {
		e.persistTimer.Stop()
		e.persistTimer = nil
	}
}

func (e *Engine) snapshotLocked() store.RuntimeSnapshot {
	sites := make([]store.SiteRuntime, 0, len(e.sites))
	for name, st := range e.sites {
		// Skip empty sites to keep table small.
		if st.fullBlockUntil.IsZero() && st.watchStarted.IsZero() && st.provStarted.IsZero() && st.lastPlayAt.IsZero() &&
			!st.watchExempt && st.exemptUntil.IsZero() {
			continue
		}
		sites = append(sites, store.SiteRuntime{
			Site:           name,
			FullBlockUntil: st.fullBlockUntil,
			WatchStarted:   st.watchStarted,
			LastPlayAt:     st.lastPlayAt,
			WatchExempt:    st.watchExempt,
			ExemptLabel:    st.exemptLabel,
			ExemptUntil:    st.exemptUntil,
		})
	}
	return store.RuntimeSnapshot{
		Sites: sites,
		Unlock: store.UnlockQuota{
			DayKey:              e.policyDay,
			DayCount:            e.tempUnlockCount,
			WeekKey:             e.tempUnlockWeekKey,
			WeekCount:           e.tempUnlockWeekCount,
			TempUnlockUntil:     e.tempUnlockUntil,
			TempUnlockStartedAt: e.tempUnlockStartedAt,
		},
	}
}
