package gametime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCalendarSpeedStepsAtDailyReset(t *testing.T) {
	day := ms(t, "2026-10-13T08:00:00Z") // a game day start (00:00 GMT-8)
	c := &Calendar{Loop: 1, LoopStart: ms(t, "2026-10-07T02:00:00Z"), Speed: 7, SpeedSince: day}
	for _, step := range []struct {
		at    string
		weeks int64
	}{
		{"2026-10-13T09:00:00Z", 0},
		{"2026-10-14T07:59:59Z", 0},
		{"2026-10-14T08:00:00Z", 0}, // 6/7 of a week after one day
		{"2026-10-15T08:00:00Z", 1},
		{"2026-10-20T08:00:00Z", 6}, // seven days at 7x: six extra weeks
	} {
		if got := c.AutoWeeks(ms(t, step.at)); got != step.weeks {
			t.Errorf("7x at %s: %d weeks, want %d", step.at, got, step.weeks)
		}
	}
	c.Speed = 3.5
	if got := c.AutoWeeks(ms(t, "2026-10-27T08:00:00Z")); got != 5 { // 14 days * 2.5 / 7
		t.Errorf("3.5x after 14 days: %d weeks, want 5", got)
	}
	c.Speed = 1
	if got := c.Shift(ms(t, "2027-10-27T08:00:00Z")); got != 0 {
		t.Errorf("real time shifted by %d", got)
	}
}

func TestNowFollowsTheCalendar(t *testing.T) {
	defer SetCalendar(nil)
	SetCalendar(&Calendar{Loop: 1, LoopStart: 1, JumpMillis: 3 * dayMillis, Speed: 1})
	if d := NowMillis() - Now().Add(-3*24*3600e9).UnixMilli(); d < 3*dayMillis-1000 || d > 3*dayMillis+1000 {
		t.Fatalf("clock not three days ahead")
	}
	SetCalendar(nil)
	if calendarShift(NowMillis()) != 0 {
		t.Fatal("calendar off still shifts")
	}
}

func TestStartNextLoop(t *testing.T) {
	dir := t.TempDir()
	master := filepath.Join(dir, "master.bin.e")
	next := filepath.Join(dir, "next.bin.e")
	path := filepath.Join(dir, "calendar.json")
	os.WriteFile(master, []byte("loop1"), 0600)
	os.WriteFile(next, []byte("loop2"), 0600)
	start := ms(t, "2026-10-07T02:00:00Z")
	c := &Calendar{Loop: 1, LoopStart: start, Speed: 1, Next: next, NextLoop: 2}
	// Before the end nothing changes.
	got, err := StartNextLoop(path, master, c, c.End()-1)
	if err != nil || got != c {
		t.Fatalf("started early: %+v %v", got, err)
	}
	got, err = StartNextLoop(path, master, c, c.End())
	if err != nil || got.Loop != 2 || got.LoopStart != start+LoopWeeks*weekMillis || got.Next != "" {
		t.Fatalf("next loop: %+v %v", got, err)
	}
	if raw, _ := os.ReadFile(master); string(raw) != "loop2" {
		t.Fatalf("master not swapped: %q", raw)
	}
	saved, err := LoadCalendar(path)
	if err != nil || saved.Loop != 2 || saved.LoopStart != got.LoopStart {
		t.Fatalf("saved %+v %v", saved, err)
	}
	// Without next-loop data the loop stays.
	again, _ := StartNextLoop(path, master, saved, saved.End())
	if again != saved {
		t.Fatal("started a loop without its game data")
	}
	// The new loop opens when its first summons do, a whole day on.
	loopStart := saved.LoopStart
	moved, err := OpenAtFirstSummons(path, saved, loopStart+3600000)
	if err != nil || moved.JumpMillis != dayMillis {
		t.Fatalf("not moved to the first summons: %+v %v", moved, err)
	}
	if reread, _ := LoadCalendar(path); reread.JumpMillis != moved.JumpMillis {
		t.Fatalf("move not saved: %+v", reread)
	}
	if again, _ := OpenAtFirstSummons(path, moved, loopStart+3600000); again != moved {
		t.Fatal("moved twice")
	}
	if later, _ := OpenAtFirstSummons(path, saved, loopStart+FirstSummons); later != saved {
		t.Fatal("moved a clock already at the first summons")
	}
	if c, err := LoadCalendar(filepath.Join(dir, "missing.json")); c != nil || err != nil {
		t.Fatal("missing file should mean calendar off")
	}
}

// TestRewindLoop pins B 方案's per-loop rotation restart: when the loop's game
// data runs out and no next-loop data file exists (Lunar Tear has no Pod
// Programs to build one), the clock jumps back one whole loop so the rotating
// terms run again from LoopStart, and the loop counter advances so the
// per-loop save reset fires.
func TestRewindLoop(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "calendar.json")
	start := ms(t, "2021-07-28T02:00:00Z") // Launch
	c := &Calendar{Loop: 1, LoopStart: start, JumpMillis: -1000, Speed: 1}

	// Before the end nothing changes (game clock = real-1000 stays below End).
	got, err := RewindLoop(path, c, c.End()-1)
	if err != nil || got != c {
		t.Fatalf("rewound early: %+v %v", got, err)
	}
	// At the end (game clock = real-1000 = End when real = End+1000) the clock
	// goes back one whole loop; LoopStart stays (the game data's dates are
	// still aligned to it) and the loop counter moves on.
	got, err = RewindLoop(path, c, c.End()+1000)
	if err != nil {
		t.Fatalf("rewind: %v", err)
	}
	if got.Loop != 2 {
		t.Fatalf("loop = %d, want 2", got.Loop)
	}
	if got.LoopStart != start {
		t.Fatalf("loopStart moved to %d, want %d (data still aligned to it)", got.LoopStart, start)
	}
	if want := c.JumpMillis - LoopWeeks*weekMillis; got.JumpMillis != want {
		t.Fatalf("jump = %dms, want %dms (one whole loop back)", got.JumpMillis, want)
	}
	// The rewind lands the game clock back at LoopStart.
	if real := c.End() + 1000; real+got.Shift(real) != start {
		t.Fatalf("game clock after rewind = %d, want loopStart=%d", real+got.Shift(real), start)
	}
	if saved, err := LoadCalendar(path); err != nil || saved.Loop != 2 || saved.JumpMillis != got.JumpMillis {
		t.Fatalf("rewind not saved: %+v %v", saved, err)
	}
	// A calendar that waits for its next-loop file is left alone.
	waiting := &Calendar{Loop: 2, LoopStart: start, Speed: 1, Next: filepath.Join(dir, "next.bin.e"), NextLoop: 3}
	if again, err := RewindLoop(path, waiting, waiting.End()); err != nil || again != waiting {
		t.Fatalf("rewound past a pending next-loop file: %+v %v", again, err)
	}
	// And so is a running loop (game clock still before the end).
	if again, err := RewindLoop(path, c, c.End()-1); err != nil || again != c {
		t.Fatalf("rewound a running loop: %+v %v", again, err)
	}
}
