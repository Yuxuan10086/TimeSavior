package policy

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"math/big"
	mrand "math/rand"
	"strings"
	"time"
)

const (
	challengeIdle    = "idle"
	challengeWaiting = "waiting"
	challengeQuiz    = "quiz"
	challengeSuccess = "success"
)

type mathQuestion struct {
	ID        string
	Prompt    string
	Options   []int
	answer    int
	ExpiresAt time.Time
}

type challengeSession struct {
	phase     string
	waitUntil time.Time
	streak    int
	question  *mathQuestion
	startedAt time.Time
	lastMsg   string
}

func (e *Engine) waitSeconds() int {
	if e.cfg.TempUnlockWaitSeconds > 0 {
		return e.cfg.TempUnlockWaitSeconds
	}
	return 60
}

func (e *Engine) questionSeconds() int {
	if e.cfg.TempUnlockQuestionSeconds > 0 {
		return e.cfg.TempUnlockQuestionSeconds
	}
	return 5
}

func (e *Engine) streakNeeded() int {
	if e.cfg.TempUnlockStreakNeeded > 0 {
		return e.cfg.TempUnlockStreakNeeded
	}
	return 10
}

// StartUnlockChallenge no longer grants unlock via quiz — unlock starts when the user
// first opens a managed-site feed inside the daily window. Kept for API/UI compat.
func (e *Engine) StartUnlockChallenge(now time.Time) map[string]any {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.rollPolicyDay(now)
	e.advanceChallengeLocked(now)

	if !e.cfg.TempUnlockEnabled {
		return e.challengeViewLocked(now, "临时解锁已禁用")
	}
	if now.Before(e.tempUnlockUntil) {
		return e.challengeViewLocked(now, "当前已在临时解锁中")
	}
	if e.tempUnlockCount > 0 {
		return e.challengeViewLocked(now, "今日额度已用完（首次打开推荐页起计时）")
	}
	if !e.InFeedUnlockWindow(now) {
		open, _ := e.FeedUnlockWindowBounds(now)
		msg := "当前不在可用窗口内"
		if !open.IsZero() {
			msg = fmt.Sprintf("可用窗口从 %s 开始；首次打开推荐页起计时", open.Format("15:04"))
		}
		return e.challengeViewLocked(now, msg)
	}
	return e.challengeViewLocked(now, fmt.Sprintf(
		"无需挑战：%s–次日 %02d:00 内首次打开管控站推荐页，自动解锁 %d 分钟",
		winStartLabel(e), e.dayStartHour(), e.unlockMinutes(),
	))
}

func winStartLabel(e *Engine) string {
	if e != nil && e.cfg != nil && strings.TrimSpace(e.cfg.TempUnlockWindowStart) != "" {
		return e.cfg.TempUnlockWindowStart
	}
	return "21:30"
}

// UnlockChallengeState returns current challenge progress (advances timers).
func (e *Engine) UnlockChallengeState(now time.Time) map[string]any {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.rollPolicyDay(now)
	if e.challenge == nil {
		e.challenge = &challengeSession{phase: challengeIdle}
	}
	e.advanceChallengeLocked(now)
	return e.challengeViewLocked(now, e.challenge.lastMsg)
}

// TickChallenge advances waiting/quiz timers and reports whether the view changed.
// Used by the SSE pump so unlock UI updates without client polling.
func (e *Engine) TickChallenge(now time.Time) (changed bool, view map[string]any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.rollPolicyDay(now)
	if e.challenge == nil {
		e.challenge = &challengeSession{phase: challengeIdle}
	}
	before := e.challengeFingerprintLocked(now)
	activeBefore := now.Before(e.tempUnlockUntil)
	e.advanceChallengeLocked(now)
	after := e.challengeFingerprintLocked(now)
	activeAfter := now.Before(e.tempUnlockUntil)
	changed = before != after || activeBefore != activeAfter
	view = e.challengeViewLocked(now, e.challenge.lastMsg)
	if changed {
		e.notifyLocked(ChangeUnlock, true)
		if activeBefore != activeAfter || (e.challenge != nil && e.challenge.phase == challengeSuccess) {
			e.notifyLocked(ChangeStatus, true)
		}
	}
	return changed, view
}

func (e *Engine) challengeFingerprintLocked(now time.Time) string {
	ch := e.challenge
	if ch == nil {
		return "nil"
	}
	qid := ""
	if ch.question != nil {
		qid = ch.question.ID
	}
	return fmt.Sprintf("%s|%d|%s|%v|%s",
		ch.phase, ch.streak, qid, now.Before(e.tempUnlockUntil), ch.lastMsg)
}

// CancelTempUnlock clears an in-progress challenge and/or active TempUnlock window,
// restoring SoftBlock / normal policy. Does not refund daily/weekly quota already consumed.
func (e *Engine) CancelTempUnlock(now time.Time) map[string]any {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.rollPolicyDay(now)

	wasActive := now.Before(e.tempUnlockUntil)
	phase := challengeIdle
	if e.challenge != nil {
		phase = e.challenge.phase
	}
	inChallenge := phase == challengeWaiting || phase == challengeQuiz || phase == challengeSuccess

	if !wasActive && !inChallenge {
		if e.challenge == nil {
			e.challenge = &challengeSession{phase: challengeIdle}
		}
		e.challenge.lastMsg = "当前没有进行中的解锁或挑战"
		return e.challengeViewLocked(now, e.challenge.lastMsg)
	}

	e.tempUnlockUntil = time.Time{}
	msg := "已取消，管控已恢复"
	switch {
	case wasActive && (phase == challengeWaiting || phase == challengeQuiz):
		msg = "已取消临时解锁与挑战，管控已恢复"
	case wasActive:
		msg = "已取消临时解锁，管控已恢复"
	case phase == challengeWaiting || phase == challengeQuiz:
		msg = "已取消挑战，回到待开始"
	}
	e.challenge = &challengeSession{phase: challengeIdle, lastMsg: msg}
	// Persist immediately so a quick restart cannot revive the cancelled window.
	e.cancelPersistTimerLocked()
	if e.runtimeStore != nil {
		if err := e.runtimeStore.SaveRuntime(e.snapshotLocked()); err != nil {
			log.Printf("policy: cancel persist: %v", err)
		}
	}
	out := e.challengeViewLocked(now, msg)
	e.notifyLocked(ChangeUnlock, true)
	e.notifyLocked(ChangeStatus, true)
	return out
}

// AnswerUnlockChallenge submits a multiple-choice value for the current question.
func (e *Engine) AnswerUnlockChallenge(qid string, choice int, now time.Time) map[string]any {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.rollPolicyDay(now)

	if e.challenge == nil {
		e.challenge = &challengeSession{phase: challengeIdle, lastMsg: "请先开始挑战"}
		return e.challengeViewLocked(now, e.challenge.lastMsg)
	}
	e.advanceChallengeLocked(now)

	ch := e.challenge
	if ch.phase != challengeQuiz || ch.question == nil {
		ch.lastMsg = "当前不在答题阶段"
		return e.challengeViewLocked(now, ch.lastMsg)
	}
	if qid == "" || qid != ch.question.ID {
		ch.lastMsg = "题目已切换，请答新题"
		return e.challengeViewLocked(now, ch.lastMsg)
	}
	if now.After(ch.question.ExpiresAt) {
		ch.streak = 0
		e.newQuestionLocked(now)
		ch.lastMsg = "超时未答，连续正确已清零"
		out := e.challengeViewLocked(now, ch.lastMsg)
		e.notifyLocked(ChangeUnlock, true)
		return out
	}

	if choice != ch.question.answer {
		ch.streak = 0
		e.newQuestionLocked(now)
		ch.lastMsg = "答错了，连续正确已清零"
		out := e.challengeViewLocked(now, ch.lastMsg)
		e.notifyLocked(ChangeUnlock, true)
		return out
	}

	ch.streak++
	if ch.streak >= e.streakNeeded() {
		// Challenge no longer grants unlock; feed-open rule is authoritative.
		ch.phase = challengeIdle
		ch.question = nil
		ch.lastMsg = "请打开管控站推荐页以开始今日解锁"
		out := e.challengeViewLocked(now, ch.lastMsg)
		e.notifyLocked(ChangeUnlock, true)
		return out
	}

	e.newQuestionLocked(now)
	ch.lastMsg = fmt.Sprintf("正确！连续 %d / %d", ch.streak, e.streakNeeded())
	out := e.challengeViewLocked(now, ch.lastMsg)
	e.notifyLocked(ChangeUnlock, true)
	return out
}

func (e *Engine) advanceChallengeLocked(now time.Time) {
	ch := e.challenge
	if ch == nil {
		return
	}
	switch ch.phase {
	case challengeSuccess:
		// Unlock window ended → back to idle so a second challenge can start.
		if !now.Before(e.tempUnlockUntil) {
			ch.phase = challengeIdle
			ch.question = nil
			ch.streak = 0
			ch.lastMsg = "解锁已结束，可再次开始挑战"
		}
	case challengeWaiting:
		if !now.Before(ch.waitUntil) {
			ch.phase = challengeQuiz
			ch.streak = 0
			e.newQuestionLocked(now)
			ch.lastMsg = "开始答题：连续做对才能解锁"
		}
	case challengeQuiz:
		if ch.question != nil && !now.Before(ch.question.ExpiresAt) {
			ch.streak = 0
			e.newQuestionLocked(now)
			ch.lastMsg = "超时未答，连续正确已清零"
		}
	}
}

func (e *Engine) newQuestionLocked(now time.Time) {
	ch := e.challenge
	if ch == nil {
		return
	}
	q := generateMathQuestion(now.Add(time.Duration(e.questionSeconds()) * time.Second))
	ch.question = q
	ch.phase = challengeQuiz
}

func (e *Engine) challengeViewLocked(now time.Time, msg string) map[string]any {
	phase := challengeIdle
	streak := 0
	if e.challenge != nil {
		phase = e.challenge.phase
		streak = e.challenge.streak
	}
	winOpen, winClose := e.FeedUnlockWindowBounds(now)
	winStart := "21:30"
	if e.cfg != nil && strings.TrimSpace(e.cfg.TempUnlockWindowStart) != "" {
		winStart = e.cfg.TempUnlockWindowStart
	}
	out := map[string]any{
		"enabled":                  e.cfg.TempUnlockEnabled,
		"phase":                    phase,
		"message":                  msg,
		"wait_seconds":             e.waitSeconds(),
		"question_seconds":         e.questionSeconds(),
		"streak":                   streak,
		"streak_needed":            e.streakNeeded(),
		"daily_used":               e.tempUnlockCount,
		"daily_limit":              e.cfg.TempUnlockDailyLimit,
		"weekly_used":              e.tempUnlockWeekCount,
		"weekly_limit":             e.weekLimit(),
		"unlock_minutes":           e.unlockMinutes(),
		"window_start":             winStart,
		"window_end_hour":          e.dayStartHour(),
		"feed_unlock_window":       e.InFeedUnlockWindow(now),
		"feed_unlock_window_start": formatTime(winOpen),
		"feed_unlock_window_end":   formatTime(winClose),
		"temp_unlock_started":      e.tempUnlockCount > 0,
		"temp_unlock_started_at":   formatTime(e.tempUnlockStartedAt),
		"temp_unlock_active":       now.Before(e.tempUnlockUntil),
		"temp_unlock_until":        formatTime(e.tempUnlockUntil),
		"wait_remaining_ms":        0,
		"question":                 nil,
	}
	if e.challenge == nil {
		return out
	}
	ch := e.challenge
	if msg == "" {
		out["message"] = ch.lastMsg
	}
	if ch.phase == challengeWaiting {
		ms := ch.waitUntil.Sub(now).Milliseconds()
		if ms < 0 {
			ms = 0
		}
		out["wait_remaining_ms"] = ms
		out["wait_until"] = formatTime(ch.waitUntil)
	}
	if ch.question != nil && ch.phase == challengeQuiz {
		ms := ch.question.ExpiresAt.Sub(now).Milliseconds()
		if ms < 0 {
			ms = 0
		}
		out["question"] = map[string]any{
			"id":           ch.question.ID,
			"prompt":       ch.question.Prompt,
			"options":      ch.question.Options,
			"expires_at":   formatTime(ch.question.ExpiresAt),
			"remaining_ms": ms,
		}
	}
	return out
}

func generateMathQuestion(expires time.Time) *mathQuestion {
	n, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	rng := mrand.New(mrand.NewSource(n.Int64() ^ time.Now().UnixNano()))

	add := rng.Intn(2) == 0
	var a, b, ans int
	var prompt string
	if add {
		a = rng.Intn(21)
		b = rng.Intn(21 - a)
		ans = a + b
		prompt = fmt.Sprintf("%d + %d = ?", a, b)
	} else {
		a = rng.Intn(21)
		b = rng.Intn(a + 1)
		ans = a - b
		prompt = fmt.Sprintf("%d − %d = ?", a, b)
	}

	opts := []int{ans}
	for len(opts) < 4 {
		delta := rng.Intn(7) - 3
		if delta == 0 {
			delta = 1
		}
		v := ans + delta
		if v < 0 || v > 20 {
			continue
		}
		dup := false
		for _, o := range opts {
			if o == v {
				dup = true
				break
			}
		}
		if !dup {
			opts = append(opts, v)
		}
	}
	rng.Shuffle(len(opts), func(i, j int) { opts[i], opts[j] = opts[j], opts[i] })

	id := make([]byte, 8)
	_, _ = rand.Read(id)
	return &mathQuestion{
		ID:        hex.EncodeToString(id),
		Prompt:    prompt,
		Options:   opts,
		answer:    ans,
		ExpiresAt: expires,
	}
}
