package main

import (
	"path/filepath"
	"testing"
)

type recHost struct {
	nopHost
	events map[string]int
	states int
	tok    *Tokens
}

func (h *recHost) EmitState(Snapshot) { h.states++ }

func (h *recHost) Emit(name string, _ any) { h.events[name]++ }
func (h *recHost) SaveTokens(t *Tokens)    { h.tok = t }
func (h *recHost) LoadTokens() *Tokens     { return h.tok }

// Канал працює без Wails і без файлів поруч з exe: лише Host і Store (так буде у вебсервісі).
func TestChannelWithoutDesktop(t *testing.T) {
	h := &recHost{events: map[string]int{}}
	c := newChannel(h)
	s, err := openStore(filepath.Join(t.TempDir(), "c.db"), "12345")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c.store = s
	c.SelectReward("r1", "СабДей")
	c.StartTimer()
	ev := Redemption{ID: "x1", UserID: "u1", UserName: "Phill", UserInput: "Hades"}
	ev.Reward.ID = "r1"
	c.onRedemption(ev, User{})
	// повний стан (з налаштуваннями й ключами) іде лише через EmitState — не загальною подією, яку бачать віджети OBS
	if len(c.st.Entries) != 1 || h.states == 0 || h.events["state"] != 0 {
		t.Fatalf("entries=%d states=%d state events=%d", len(c.st.Entries), h.states, h.events["state"])
	}
	st, ok, err := s.Load()
	if err != nil || !ok || len(st.Entries) != 1 || st.Settings.RewardID != "r1" {
		t.Fatalf("store: ok=%v err=%v %+v", ok, err, st.Entries)
	}
	c.Logout()
	if h.tok != nil {
		t.Fatal("logout must clear tokens via host")
	}
}
