package main

// Етапи СабДею:
//   1 «collect» — збір замовлень (таймер збору, таблиці). Після першого експорту в Marbles стрімер вибирає переможця.
//   2 «play»    — гра переможного замовлення: окремий таймер (за замовчуванням 40 хв). Коли він закінчується —
//                 автоматично назад до етапу 1.

import (
	"errors"
	"strings"
	"time"
)

const (
	phaseCollect = "collect"
	phasePlay    = "play"
)

// SetWinner — вибрати переможця поточного збору і перейти до етапу 2 (таймер гри стартує одразу).
func (a *App) SetWinner(name string) (Snapshot, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return a.GetState(), errors.New("переможця не вказано")
	}
	a.mu.Lock()
	ok := a.st.Exported
	a.mu.Unlock()
	if !ok {
		return a.GetState(), errors.New("спершу експортуйте замовлення в Marbles")
	}
	snap := a.mutate(func(st *State) {
		now := time.Now().UnixMilli()
		s := &st.Session
		if s.Status == "running" || s.Status == "paused" { // збір іще йде — завершуємо
			s.Status, s.RemainingMs, s.FinishedAt = "finished", 0, now
		}
		st.Winner = name
		st.Phase = phasePlay
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
		}
	})
}

// FinishGame — завершити гру (вручну або коли вийшов час) і повернутися до етапу 1.
func (a *App) FinishGame() Snapshot {
	return a.mutate(func(st *State) { a.finishGameLocked() })
}

func (a *App) finishGameLocked() {
	st := &a.st
	if st.Phase != phasePlay {
		return
	}
	st.Game.Status, st.Game.RemainingMs, st.Game.FinishedAt = "finished", 0, time.Now().UnixMilli()
	st.Phase = phaseCollect
	a.persistCollectionLocked()
}

// перевірка таймера гри (з ticker під a.mu); true — гра щойно завершилась
func (a *App) tickGameLocked(now int64) bool {
	g := &a.st.Game
	if a.st.Phase == phasePlay && g.Status == "running" && g.EndsAt <= now {
		a.finishGameLocked()
		return true
	}
	return false
}
