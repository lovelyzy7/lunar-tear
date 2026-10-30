package service

// gacha_diag_dump_test.go — TEMPORARY DIAGNOSTIC (H2).
//
// TODO(diag): rollback = delete this file.
//
// Dumps the GetGachaList response construction for labels=[1 2 3 4 5] (the
// exact request observed at 2026-10-25 21:16:44) using the production
// toProtoGacha/buildProtoPricePhases path, then checks the payload against
// the client's Dark.Generated.Type enums (GachaModeType/GachaAutoResetType/
// GachaLabelType/GachaUnlockConditionType) and the proto definition in
// proto/gacha.proto.
//
// Run:  go test ./internal/service -run TestDiagDumpGachaList -v
import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	pb "lunar-tear/server/gen/proto"
	"lunar-tear/server/internal/model"
	"lunar-tear/server/internal/runtime"
	"lunar-tear/server/internal/store"

	"google.golang.org/protobuf/encoding/protojson"
)

// diagOutDir is where deliverables land; override with LUNAR_DIAG_OUT.
func diagOutDir(t *testing.T) string {
	if v := os.Getenv("LUNAR_DIAG_OUT"); v != "" {
		return v
	}
	// internal/service -> server -> lunar-tear -> repo root
	return filepath.Join("..", "..", "..", "..", "zh-l10n", "work", "diag-comm")
}

func TestDiagDumpGachaList(t *testing.T) {
	mdPath := filepath.Join("..", "..", "assets", "release", "20240404193219.bin.e")
	if _, err := os.Stat(mdPath); err != nil {
		t.Skipf("master data not available at %s: %v", mdPath, err)
	}
	holder, err := runtime.NewHolder(mdPath)
	if err != nil {
		t.Fatalf("load master data: %v", err)
	}
	cat := holder.Get()
	catalog := cat.GachaEntries

	labels := []int32{1, 2, 3, 4, 5} // observed client request
	nowMillis := time.Now().UnixMilli()

	type find struct {
		GachaId         int32  `json:"gachaId"`
		Label           int32  `json:"label"`
		ModeType        int32  `json:"modeType"`
		AutoResetType   int32  `json:"autoResetType"`
		AutoResetPeriod int32  `json:"autoResetPeriod"`
		BannerAsset     string `json:"bannerAssetName"`
		StartRaw        int64  `json:"startDatetimeRaw"`
		EndRaw          int64  `json:"endDatetimeRaw"`
		StartSent       string `json:"startDatetimeSent"`
		EndSent         string `json:"endDatetimeSent"`
		NextResetSent   string `json:"nextAutoResetSent"`
		PricePhases     int    `json:"pricePhaseCount"`
		PromotionItems  int    `json:"promotionItemCount"`
		ModeOneof       string `json:"modeOneof"`
		Issues          []string `json:"issues"`
	}

	var (
		rows      []find
		protoJSON []map[string]any
		issueCnt  = map[string]int{}
		mark      = func(f *find, code string) { f.Issues = append(f.Issues, code); issueCnt[code]++ }
	)

	// pre-filter catalog stats — WHY entries drop out of the response.
	type rawRow struct {
		GachaId     int32  `json:"gachaId"`
		Label       int32  `json:"label"`
		ModeType    int32  `json:"modeType"`
		StartRaw    int64  `json:"startDatetimeRaw"`
		EndRaw      int64  `json:"endDatetimeRaw"`
		StartISO    string `json:"startISO"`
		EndISO      string `json:"endISO"`
		BannerAsset string `json:"bannerAssetName"`
		DroppedBy   string `json:"droppedBy"`
	}
	var raw []rawRow
	stats := map[string]int{}
	for _, entry := range catalog {
		r := rawRow{
			GachaId: entry.GachaId, Label: entry.GachaLabelType, ModeType: entry.GachaModeType,
			StartRaw: entry.StartDatetime, EndRaw: entry.EndDatetime,
			BannerAsset: entry.BannerAssetName,
		}
		r.StartISO = time.UnixMilli(entry.StartDatetime).UTC().Format(time.RFC3339)
		r.EndISO = time.UnixMilli(entry.EndDatetime).UTC().Format(time.RFC3339)
		switch {
		case !gachaActiveAt(entry, nowMillis):
			r.DroppedBy = "not_active_at_now"
			stats["not_active_at_now"]++
		case !matchesGachaLabel(labels, entry.GachaLabelType):
			r.DroppedBy = "label_mismatch"
			stats["label_mismatch"]++
		case entry.GachaLabelType == model.GachaLabelPortalCage || entry.GachaLabelType == model.GachaLabelRecycle:
			r.DroppedBy = "portal_or_recycle"
			stats["portal_or_recycle"]++
		default:
			r.DroppedBy = ""
			stats["included"]++
		}
		raw = append(raw, r)
	}
	t.Logf("catalog=%d filter_stats=%v nowMillis=%d (%s)", len(catalog), stats, nowMillis,
		time.UnixMilli(nowMillis).UTC().Format(time.RFC3339))

	for _, entry := range catalog {
		if !gachaActiveAt(entry, nowMillis) {
			continue
		}
		if !matchesGachaLabel(labels, entry.GachaLabelType) {
			continue
		}
		if entry.GachaLabelType == model.GachaLabelPortalCage || entry.GachaLabelType == model.GachaLabelRecycle {
			continue
		}
		bs := store.GachaBannerState{}
		g := toProtoGacha(entry, &bs)

		f := find{
			GachaId:         g.GachaId,
			Label:           g.GachaLabelType,
			ModeType:        g.GachaModeType,
			AutoResetType:   g.GachaAutoResetType,
			AutoResetPeriod: g.GachaAutoResetPeriod,
			BannerAsset:     entry.BannerAssetName,
			StartRaw:        entry.StartDatetime,
			EndRaw:          entry.EndDatetime,
			StartSent:       g.StartDatetime.AsTime().UTC().Format(time.RFC3339),
			EndSent:         g.EndDatetime.AsTime().UTC().Format(time.RFC3339),
			NextResetSent:   g.NextAutoResetDatetime.AsTime().UTC().Format(time.RFC3339),
			PricePhases:     len(g.GachaPricePhase),
			PromotionItems:  0,
		}
		switch g.GachaMode.(type) {
		case *pb.Gacha_GachaModeBasic:
			f.ModeOneof = "basic"
		case *pb.Gacha_GachaModeStepupComposition:
			f.ModeOneof = "stepup"
		case *pb.Gacha_GachaModeBoxComposition:
			f.ModeOneof = "box"
		default:
			f.ModeOneof = "UNSET"
			mark(&f, "mode_oneof_unset")
		}

		// --- enum legality vs client Dark.Generated.Type ---
		switch g.GachaModeType {
		case model.GachaModeUnknown, model.GachaModeBasic, model.GachaModeStepup, model.GachaModeBox:
		default:
			mark(&f, "illegal_gacha_mode_type")
		}
		if g.GachaModeType == model.GachaModeUnknown {
			// server maps Unknown -> Basic oneof; client sees modeType=UNKNOWN
			// with a basic payload — inconsistent for client switch logic.
			mark(&f, "mode_type_unknown_but_oneof_present")
		}
		switch g.GachaAutoResetType {
		case 0, 1, 2, 3: // UNKNOWN/NONE/DAILY/MONTHLY
		default:
			mark(&f, "illegal_auto_reset_type")
		}
		switch g.GachaLabelType {
		case 1, 2, 3:
		default:
			mark(&f, "unexpected_label_in_response")
		}
		for _, uc := range g.GachaUnlockCondition {
			switch uc.GachaUnlockConditionType {
			case 0, 1, 2, 3, 4, 5:
			default:
				mark(&f, "illegal_unlock_condition_type")
			}
		}

		// --- completeness ---
		if entry.BannerAssetName == "" {
			mark(&f, "empty_banner_asset_name")
		}
		if len(g.GachaPricePhase) == 0 {
			mark(&f, "no_price_phase")
		}
		if entry.StartDatetime == 0 {
			// safeTimestamp(0) -> 1970-01-01, NOT an absent timestamp.
			mark(&f, "start_datetime_epoch_sent")
		}
		if entry.EndDatetime == 0 {
			mark(&f, "end_datetime_epoch_sent")
		}
		if entry.NextAutoResetDatetime == 0 {
			mark(&f, "next_auto_reset_epoch_sent")
		}
		if g.GachaId <= 0 {
			mark(&f, "nonpositive_gacha_id")
		}
		for _, p := range g.GachaPricePhase {
			switch p.PriceType {
			case 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16:
			default:
				mark(&f, "price_type_out_of_possession_range")
			}
			if p.GachaPricePhaseId == 0 {
				mark(&f, "zero_price_phase_id")
			}
			if p.DrawCount <= 0 {
				mark(&f, "nonpositive_draw_count")
			}
		}

		b, _ := protojson.MarshalOptions{Multiline: false}.Marshal(g)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		protoJSON = append(protoJSON, m)
		rows = append(rows, f)
	}

	outDir := diagOutDir(t)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", outDir, err)
	}
	payload := map[string]any{
		"requested_labels": labels,
		"now_millis":       nowMillis,
		"filter_stats":     stats,
		"banner_count":     len(rows),
		"issue_counts":     issueCnt,
		"raw_catalog":      raw,
		"banners":          rows,
		"response_json":    protoJSON,
	}
	blob, _ := json.MarshalIndent(payload, "", "  ")
	outFile := filepath.Join(outDir, "h2_gacha_list_dump.json")
	if err := os.WriteFile(outFile, blob, 0o644); err != nil {
		t.Fatalf("write dump: %v", err)
	}

	t.Logf("GetGachaList(labels=%v) banners=%d issues=%v -> %s", labels, len(rows), issueCnt, outFile)
	if len(rows) == 0 {
		// TODO(diag): keep as Log (not Error) so go test ./... stays green.
		t.Logf("H2 RED FLAG: response is EMPTY for labels=%v (all %d catalog entries filtered as not_active_at_now)", labels, len(raw))
	}
	for code, n := range issueCnt {
		t.Logf("issue %-40s x%d", code, n)
	}
}
