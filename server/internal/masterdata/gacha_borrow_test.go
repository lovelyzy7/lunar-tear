package masterdata

// gacha_borrow_test.go — P0 fix self-verification (扭蛋页卡住 / GetGachaList 空列表)
// and its coordination with the B 方案 loop calendar.
//
// COMM-DIAG-REPORT §3 measured the master-data snapshot at 160 gacha schedule
// periods (156 in 2021–2024-04, 4 written into 2099) and every one of them was
// filtered out by the real-clock window test.  The Bunker gacha catalog (ported
// with B 方案) rebuilds loadGachaCatalogPeriods into one logical summon entry
// per gachaId (placeholder 2099 rows folded away, parked reruns reconstructed
// from their exchange shop, bannerless medals and chapter tickets appended):
// 354 catalog entries from the same snapshot.  These tests assert that
//
//  1. every catalog entry's raw window still borrows to cover "now"
//     (the deployed fallback when no calendar runs), and
//  2. LoadGachaCatalog keeps exactly one current logical period per gachaId
//     with a window that covers now,
//  3. the borrow rule behaves in all four directions, and
//  4. with a calendar active the borrow is skipped: the game clock runs inside
//     the data's own timeline and the windows rotate on their own schedule.
//
// Run:  go test ./internal/masterdata -run TestGacha -v
import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"lunar-tear/server/internal/gametime"
	"lunar-tear/server/internal/masterdata/memorydb"
)

var testMasterDataRel = filepath.Join("..", "..", "assets", "release", "20240404193219.bin.e")

func initTestMasterData(t *testing.T) {
	t.Helper()
	mdPath := testMasterDataRel
	if _, err := os.Stat(mdPath); err != nil {
		t.Skipf("master data not available at %s: %v", mdPath, err)
	}
	if err := memorydb.Init(mdPath); err != nil {
		t.Fatalf("memorydb.Init(%s): %v", mdPath, err)
	}
}

// coversNow mirrors gachaActiveAt's window verdict (service package) without
// importing it: start<=now<end, with zero bounds treated as open.
func coversNow(start, end, nowMillis int64) bool {
	if start != 0 && nowMillis < start {
		return false
	}
	if end != 0 && nowMillis >= end {
		return false
	}
	return true
}

func TestGachaWindowBorrowActivatesAllSnapshotPeriods(t *testing.T) {
	initTestMasterData(t)

	periods, _, err := loadGachaCatalogPeriods()
	if err != nil {
		t.Fatalf("loadGachaCatalogPeriods: %v", err)
	}
	// The 2024-04 snapshot under the Bunker loader: 354 catalog entries
	// (one logical summon per gachaId + bannerless medals + chapter tickets).
	// Before the Bunker catalog the raw-period loader measured 160 schedule
	// periods (COMM-DIAG-REPORT §3); both shapes must borrow fully active.
	if len(periods) != 354 {
		t.Fatalf("snapshot catalog count = %d, want the 354 entries of the Bunker gacha catalog", len(periods))
	}

	nowMillis := time.Now().UnixMilli()
	inactive := 0
	for _, p := range periods {
		s, e := BorrowGachaWindow(p.StartDatetime, p.EndDatetime, nowMillis)
		if !coversNow(s, e, nowMillis) {
			inactive++
			t.Errorf("gachaId=%d period %d..%d -> borrowed %d..%d does not cover now=%d",
				p.GachaId, p.StartDatetime, p.EndDatetime, s, e, nowMillis)
		}
	}
	if inactive != 0 {
		t.Fatalf("borrow activates every source period: %d of %d source periods still inactive after borrow", inactive, len(periods))
	}
	t.Logf("all %d source catalog windows cover now=%d after BorrowGachaWindow", len(periods), nowMillis)

	// The borrow rule in both directions, independent of the snapshot.
	const now = int64(1_700_000_000_000)
	s, e := BorrowGachaWindow(now-1000, now-1, now) // ended period
	if s != now-1000 || e != now+GachaBorrowedWindowMillis {
		t.Fatalf("ended period borrow = (%d,%d), want (%d,%d)", s, e, now-1000, now+GachaBorrowedWindowMillis)
	}
	s, e = BorrowGachaWindow(now+1000, now+2000, now) // 2099-style future period
	if s != now || e != now+2000 {
		t.Fatalf("future period borrow = (%d,%d), want (%d,%d)", s, e, now, now+2000)
	}
	s, e = BorrowGachaWindow(now-1000, now+1000, now) // already active
	if s != now-1000 || e != now+1000 {
		t.Fatalf("active period borrow = (%d,%d), want (%d,%d)", s, e, now-1000, now+1000)
	}
	s, e = BorrowGachaWindow(0, 0, now) // open bounds stay open
	if s != 0 || e != 0 {
		t.Fatalf("open period borrow = (%d,%d), want (0,0)", s, e)
	}
}

func TestGachaCatalogKeepsOneCurrentPeriodPerGachaId(t *testing.T) {
	initTestMasterData(t)

	periods, _, err := loadGachaCatalogPeriods()
	if err != nil {
		t.Fatalf("loadGachaCatalogPeriods: %v", err)
	}
	unique := map[int32]bool{}
	for _, p := range periods {
		unique[p.GachaId] = true
	}

	entries, _, err := LoadGachaCatalog()
	if err != nil {
		t.Fatalf("LoadGachaCatalog: %v", err)
	}
	if len(entries) != len(unique) {
		t.Fatalf("catalog has %d entries for %d unique gachaIds: 每 gachaId 必须只出当前逻辑档", len(entries), len(unique))
	}

	nowMillis := time.Now().UnixMilli()
	seen := map[int32]bool{}
	for _, e := range entries {
		if seen[e.GachaId] {
			t.Fatalf("gachaId=%d emitted more than once", e.GachaId)
		}
		seen[e.GachaId] = true
		if !coversNow(e.StartDatetime, e.EndDatetime, nowMillis) {
			t.Errorf("gachaId=%d window %d..%d does not cover now=%d", e.GachaId, e.StartDatetime, e.EndDatetime, nowMillis)
		}
	}
	t.Logf("catalog=%d entries (from %d snapshot catalog windows), one current period per gachaId, all cover now=%d",
		len(entries), len(periods), nowMillis)
}

// TestGachaCatalogSkipsBorrowUnderCalendar pins the B 方案 coordination with
// the already-deployed borrowCurrentGachaPeriods: once the loop calendar runs,
// the game clock lives inside the master data's own timeline, so the borrow
// must not stretch every ended window to now+365d — the summons rotate on
// their real terms (and rerun next loop via service.calendarGameStart).
func TestGachaCatalogSkipsBorrowUnderCalendar(t *testing.T) {
	initTestMasterData(t)

	raw, _, err := loadGachaCatalogPeriods()
	if err != nil {
		t.Fatalf("loadGachaCatalogPeriods: %v", err)
	}

	// No calendar: the deployed fallback borrows (every window covers now).
	gametime.SetCalendar(nil)
	borrowed, _, err := LoadGachaCatalog()
	if err != nil {
		t.Fatalf("LoadGachaCatalog (no calendar): %v", err)
	}
	nowMillis := time.Now().UnixMilli()
	for _, e := range borrowed {
		if !coversNow(e.StartDatetime, e.EndDatetime, nowMillis) {
			t.Fatalf("fallback borrow skipped gachaId=%d window %d..%d", e.GachaId, e.StartDatetime, e.EndDatetime)
		}
	}

	// Calendar on: windows come through untouched (rotation preserved).
	gametime.SetCalendar(&gametime.Calendar{Loop: 1, LoopStart: gametime.Launch, JumpMillis: -nowMillis, Speed: 1})
	t.Cleanup(func() { gametime.SetCalendar(nil) })
	unborrowed, _, err := LoadGachaCatalog()
	if err != nil {
		t.Fatalf("LoadGachaCatalog (calendar): %v", err)
	}
	if len(unborrowed) != len(raw) {
		t.Fatalf("calendar load = %d entries, want raw %d (borrow must not fold/stretch)", len(unborrowed), len(raw))
	}
	same := 0
	for i := range unborrowed {
		if unborrowed[i].StartDatetime == raw[i].StartDatetime && unborrowed[i].EndDatetime == raw[i].EndDatetime {
			same++
		}
	}
	if same != len(raw) {
		t.Fatalf("calendar load changed %d of %d windows; borrow must be skipped while the calendar runs",
			len(raw)-same, len(raw))
	}
}
