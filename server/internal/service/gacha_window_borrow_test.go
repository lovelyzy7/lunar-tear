package service

// gacha_window_borrow_test.go — P0 fix self-verification (扭蛋页卡住).
//
// Reuses the gacha_diag_dump_test.go approach (COMM-DIAG-REPORT §3 / task 4):
// the production GetGachaList filter chain plus toProtoGacha/buildProtoPricePhases,
// driven with the exact observed request labels=[1 2 3 4 5].  After the
// schedule-window borrow fix it must return a non-empty gacha list with only
// the current logical period per gachaId, and every window must be active at
// now (the pre-fix dump measured 160/160 filtered as not_active_at_now).
//
// A dump-shaped postfix report is written to zh-l10n\work\diag-comm\h2_gacha_list_dump.postfix.json.
// The pre-fix evidence (h2_gacha_list_dump.json / h2_gacha_list_dump.pre-fix.json)
// is never overwritten: the diag tool's own output is redirected with
// LUNAR_DIAG_OUT when running the suite from a verification script.
//
// Run:  go test ./internal/service -run TestGachaListNonEmptyAfterWindowBorrow -v
import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"lunar-tear/server/internal/gametime"
	"lunar-tear/server/internal/model"
	"lunar-tear/server/internal/runtime"
	"lunar-tear/server/internal/store"
)

func TestGachaListNonEmptyAfterWindowBorrow(t *testing.T) {
	mdPath := filepath.Join("..", "..", "assets", "release", "20240404193219.bin.e")
	if _, err := os.Stat(mdPath); err != nil {
		t.Skipf("master data not available at %s: %v", mdPath, err)
	}
	holder, err := runtime.NewHolder(mdPath)
	if err != nil {
		t.Fatalf("load master data: %v", err)
	}
	catalog := holder.Get().GachaEntries

	labels := []int32{1, 2, 3, 4, 5} // exact request observed at 2026-10-25 21:16:44
	nowMillis := gametime.NowMillis()

	// mirror GetGachaList's filter chain
	stats := map[string]int{}
	seenGacha := map[int32]bool{}
	var gachaIds []int32
	rows := 0
	for _, entry := range catalog {
		switch {
		case !gachaActiveAt(entry, nowMillis):
			stats["not_active_at_now"]++
			continue
		case !matchesGachaLabel(labels, entry.GachaLabelType):
			stats["label_mismatch"]++
			continue
		case entry.GachaLabelType == model.GachaLabelPortalCage || entry.GachaLabelType == model.GachaLabelRecycle:
			stats["portal_or_recycle"]++
			continue
		}
		stats["included"]++
		rows++

		bs := store.GachaBannerState{}
		g := toProtoGacha(entry, &bs)
		if g.GachaId != entry.GachaId {
			t.Fatalf("toProtoGacha returned gachaId=%d for entry %d", g.GachaId, entry.GachaId)
		}
		if g.StartDatetime == nil || g.EndDatetime == nil {
			t.Fatalf("gachaId=%d missing start/end in proto", g.GachaId)
		}
		startMs := g.StartDatetime.AsTime().UnixMilli()
		endMs := g.EndDatetime.AsTime().UnixMilli()
		if startMs != 0 && nowMillis < startMs {
			t.Errorf("gachaId=%d sent start=%d in the future (now=%d)", g.GachaId, startMs, nowMillis)
		}
		if endMs != 0 && nowMillis >= endMs {
			t.Errorf("gachaId=%d sent end=%d already passed (now=%d)", g.GachaId, endMs, nowMillis)
		}
		if seenGacha[g.GachaId] {
			t.Errorf("gachaId=%d emitted more than once: 每 gachaId 只出当前逻辑档", g.GachaId)
		}
		seenGacha[g.GachaId] = true
		gachaIds = append(gachaIds, g.GachaId)
	}

	t.Logf("catalog=%d filter_stats=%v nowMillis=%d (%s)", len(catalog), stats, nowMillis,
		time.UnixMilli(nowMillis).UTC().Format(time.RFC3339))

	// ---- assertions -------------------------------------------------------
	if rows == 0 {
		t.Fatalf("GetGachaList(labels=%v) returned an EMPTY gacha list (filter_stats=%v)", labels, stats)
	}
	t.Logf("GetGachaList(labels=%v) -> %d gacha entries (non-empty OK): ids %v …", labels, rows, gachaIds[:min(8, len(gachaIds))])

	if stats["not_active_at_now"] != 0 {
		t.Fatalf("still %d catalog entries filtered as not_active_at_now (want 0: all schedule periods active)",
			stats["not_active_at_now"])
	}
	t.Logf("window borrow OK: 0 catalog entries inactive at now (%d/160 snapshot periods folded to current periods)",
		len(catalog))
	if len(seenGacha) != rows {
		t.Fatalf("%d rows for %d unique gachaIds", rows, len(seenGacha))
	}

	// dump-tool style postfix report (h2_gacha_list_dump.postfix.json)
	outDir := diagOutDir(t)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", outDir, err)
	}
	payload := map[string]any{
		"requested_labels": labels,
		"now_millis":       nowMillis,
		"filter_stats":     stats,
		"banner_count":     rows,
		"catalog_count":    len(catalog),
		"gacha_ids":        gachaIds,
	}
	blob, _ := json.MarshalIndent(payload, "", "  ")
	outFile := filepath.Join(outDir, "h2_gacha_list_dump.postfix.json")
	if err := os.WriteFile(outFile, blob, 0o644); err != nil {
		t.Fatalf("write postfix dump: %v", err)
	}
	t.Logf("postfix dump -> %s", outFile)
}
