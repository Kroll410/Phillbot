package main

// Етапи СабДею:
//   1 «collect» — збір замовлень. Після першого експорту в Marbles стрімер вибирає переможця (затисканням кнопки).
//   2 «play»    — гра переможного замовлення: таймер гри (за замовчуванням 40 хв).
//                 Коли час вийшов — етап 2 НЕ закінчується сам: чекаємо стрімера, який запускає голосування.
//                 Голосування (3 хв): чатерси пишуть «1» — продовжити гру, «2» — наступний збір; від кожного — останній голос.
//                 Більше «1» — новий раунд гри з тим самим переможцем; більше «2» — назад до етапу 1; нічия — вирішує стрімер.

import (
	"errors"
	"strings"
	"time"
)

const (
	phaseCollect = "collect"
	phasePlay    = "play"
)

// Голосування за продовження гри.
type VoteState struct {
	Status     string         `json:"status"` // idle | running | finished
	StartedAt  int64          `json:"startedAt"`
	EndsAt     int64          `json:"endsAt"`
	FinishedAt int64          `json:"finishedAt"`
	Yes        int            `json:"yes"`    // «1» — продовжити гру
	No         int            `json:"no"`     // «2» — наступний збір
	Result     string         `json:"result"` // continue | next | tie
	Votes      map[string]int `json:"votes"`  // userID → 1 | 2
}

// SetWinner — вибрати переможця поточного збору і перейти до етапу 2 (таймер гри стартує одразу).
func (a *App) SetWinner(name string) (Snapshot, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return a.GetState(), errors.New("переможця не вказано")
	}
	a.mu.Lock()
	ok, already := a.st.Exported, a.st.Winner != ""
	a.mu.Unlock()
	if !ok {
		return a.GetState(), errors.New("спершу експортуйте замовлення в Marbles")
	}
	if already { // переможця цього збору вже вибрано — наступний вибір лише після нового збору
		return a.GetState(), errors.New("переможця цього збору вже вибрано — почніть новий збір")
	}
	snap := a.mutate(func(st *State) {
		now := time.Now().UnixMilli()
		s := &st.Session
		if s.Status == "running" || s.Status == "paused" { // збір іще йде — завершуємо
			s.Status, s.RemainingMs, s.FinishedAt = "finished", 0, now
		}
		st.Winner = name
		st.Phase = phasePlay
		st.Round = 1
		st.Vote = VoteState{Status: "idle"}
		st.Game = Timer{Status: "running", StartedAt: now, EndsAt: now + int64(st.Settings.GameMin)*60000}
		a.persistCollectionLocked()
	})
	a.logf("Переможець: «%s» — гра %d хв", name, snap.Settings.GameMin)
	return snap, nil
}

func (a *App) GamePause() Snapshot {
	return a.mutate(func(st *State) {
		g := &st.Game
		if g.Status == "running" {
			g.RemainingMs = max64(0, g.EndsAt-time.Now().UnixMilli())
			g.Status = "paused"
		}
	})
}

func (a *App) GameResume() Snapshot {
	return a.mutate(func(st *State) {
		g := &st.Game
		if g.Status == "paused" {
			g.EndsAt = time.Now().UnixMilli() + g.RemainingMs
			g.Status = "running"
		}
	})
}

func (a *App) GameAddTime(ms int64) Snapshot {
	return a.mutate(func(st *State) {
		g, now := &st.Game, time.Now().UnixMilli()
		switch g.Status {
		case "running":
			g.EndsAt = max64(now+1000, g.EndsAt+ms)
		case "paused":
			g.RemainingMs = max64(1000, g.RemainingMs+ms)
		case "finished": // +1 хв після кінця — гра продовжується
			if ms > 0 && st.Vote.Status != "running" {
				g.Status, g.EndsAt = "running", now+ms
			}
		}
	})
}

// FinishGame — завершити таймер гри вручну (далі — очікування стрімера / голосування).
func (a *App) FinishGame() Snapshot {
	return a.mutate(func(st *State) { a.gameTimeUpLocked() })
}

func (a *App) gameTimeUpLocked() {
	g := &a.st.Game
	if a.st.Phase != phasePlay || g.Status == "finished" {
		return
	}
	g.Status, g.RemainingMs, g.FinishedAt = "finished", 0, time.Now().UnixMilli()
	a.persistCollectionLocked()
}

// StartVote — почати голосування в чаті: «1» продовжити гру, «2» наступний збір.
func (a *App) StartVote() (Snapshot, error) {
	a.mu.Lock()
	ok := a.st.Phase == phasePlay && a.st.Game.Status == "finished" && a.st.Vote.Status != "running"
	a.mu.Unlock()
	if !ok {
		return a.GetState(), errors.New("голосування можна почати, коли час гри вийшов")
	}
	snap := a.mutate(func(st *State) {
		now := time.Now().UnixMilli()
		st.Vote = VoteState{Status: "running", StartedAt: now, EndsAt: now + int64(st.Settings.VoteMin)*60000, Votes: map[string]int{}}
	})
	a.logf("Голосування: «1» — продовжити, «2» — наступний збір (%d хв)", snap.Settings.VoteMin)
	return snap, nil
}

// FinishVote — завершити голосування достроково і застосувати результат.
func (a *App) FinishVote() Snapshot {
	return a.mutate(func(st *State) {
		if st.Vote.Status == "running" {
			a.resolveVoteLocked()
		}
	})
}

func (a *App) VoteAddTime(ms int64) Snapshot {
	return a.mutate(func(st *State) {
		if st.Vote.Status == "running" {
			st.Vote.EndsAt = max64(time.Now().UnixMilli()+1000, st.Vote.EndsAt+ms)
		}
	})
}

// ContinueGame — новий раунд гри з тим самим переможцем (вручну або за голосуванням).
func (a *App) ContinueGame() Snapshot {
	return a.mutate(func(st *State) { a.continueGameLocked() })
}

// NextCollection — завершити етап 2 і повернутися до збору замовлень.
func (a *App) NextCollection() Snapshot {
	return a.mutate(func(st *State) { a.endPlayLocked() })
}

func (a *App) continueGameLocked() {
	st := &a.st
	if st.Phase != phasePlay {
		return
	}
	now := time.Now().UnixMilli()
	st.Round++
	st.Game = Timer{Status: "running", StartedAt: now, EndsAt: now + int64(st.Settings.GameMin)*60000}
	if st.Vote.Status == "running" {
		st.Vote.Status = "idle"
	}
}

func (a *App) endPlayLocked() {
	st := &a.st
	if st.Phase != phasePlay {
		return
	}
	if st.Game.Status != "finished" {
		st.Game.Status, st.Game.RemainingMs, st.Game.FinishedAt = "finished", 0, time.Now().UnixMilli()
	}
	if st.Vote.Status == "running" {
		st.Vote.Status = "idle"
	}
	st.Phase = phaseCollect
	a.persistCollectionLocked()
}

func (a *App) resolveVoteLocked() {
	v := &a.st.Vote
	v.Status, v.FinishedAt = "finished", time.Now().UnixMilli()
	switch {
	case v.Yes > v.No:
		v.Result = "continue"
		a.continueGameLocked()
	case v.No > v.Yes:
		v.Result = "next"
		a.endPlayLocked()
	default:
		v.Result = "tie" // вирішує стрімер
	}
}

// Голос з чату під час голосування: «1» або «2» (від кожного — останній).
func (a *App) onVoteMessage(userID, text string) {
	t := strings.TrimSpace(text)
	if t != "1" && t != "2" || userID == "" {
		return
	}
	a.mu.Lock()
	v := &a.st.Vote
	if a.st.Phase != phasePlay || v.Status != "running" {
		a.mu.Unlock()
		return
	}
	if v.Votes == nil {
		v.Votes = map[string]int{}
	}
	v.Votes[userID] = int(t[0] - '0')
	v.Yes, v.No = 0, 0
	for _, x := range v.Votes {
		if x == 1 {
			v.Yes++
		} else {
			v.No++
		}
	}
	a.saveLocked()
	a.emitLocked()
	a.mu.Unlock()
}

// таймери гри й голосування (з ticker під a.mu); повертає повідомлення для журналу або ""
func (a *App) tickGameLocked(now int64) string {
	if a.st.Phase != phasePlay {
		return ""
	}
	if g := &a.st.Game; g.Status == "running" && g.EndsAt <= now {
		a.gameTimeUpLocked()
		return "Час гри вийшов — чекаємо рішення стрімера"
	}
	if v := &a.st.Vote; v.Status == "running" && v.EndsAt <= now {
		a.resolveVoteLocked()
		switch v.Result {
		case "continue":
			return "Голосування: продовжуємо гру"
		case "next":
			return "Голосування: наступний збір"
		default:
			return "Голосування: нічия — вирішує стрімер"
		}
	}
	return ""
}
