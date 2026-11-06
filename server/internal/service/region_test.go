package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// setupRegionMap 每个用例独立的映射文件。
func setupRegionMap(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "region_map.json")
	t.Setenv("LUNAR_REGION_MAP", p)
	regionFileCache, regionFileMtime = nil, 0
	return p
}

func readMap(t *testing.T, p string) regionMapData {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read region map: %v", err)
	}
	var d regionMapData
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatalf("parse region map: %v", err)
	}
	d.normalize()
	return d
}

// ① 官方名族：正名直接按模式判定。
func TestRegionOfficialNames(t *testing.T) {
	setupRegionMap(t)
	cases := []struct {
		pkg  string
		want Region
	}{
		{"com.square_enix.android_googleplay.nierspjp", RegionJP},
		{"com.square_enix.android_googleplay.nierspww", RegionGL},
	}
	for _, c := range cases {
		if got := resolveRegion("uuid-official-"+c.pkg, c.pkg); got != c.want {
			t.Errorf("resolveRegion(%q) = %s, want %s", c.pkg, got, c.want)
		}
	}
}

// ② 带前/后缀的简单改名：名族模式前后缀容错。
func TestRegionSuffixedRenames(t *testing.T) {
	setupRegionMap(t)
	cases := []struct {
		pkg  string
		want Region
	}{
		{"com.square_enix.android_googleplay.nierspjp2", RegionJP},
		{"com.square_enix.android_googleplay.nierspww6", RegionGL},
		{"backup.nierspjp.copy", RegionJP},
		{"my.nierspww_bak", RegionGL},
	}
	for _, c := range cases {
		if got := resolveRegion("uuid-rename-"+c.pkg, c.pkg); got != c.want {
			t.Errorf("resolveRegion(%q) = %s, want %s", c.pkg, got, c.want)
		}
	}
}

// ③ 完全陌生名 + terms 语言提示：自动分配并持久化；同 uuid 换包名仍粘住。
func TestRegionUnknownNameWithTermsHint(t *testing.T) {
	p := setupRegionMap(t)
	pkg := "com.example.renamed123"

	// CDN 侧观测：客户端请求了 /web/static/ja/terms（X-Requested-With=包名）
	noteTermsHint(pkg, "ja")

	got := resolveRegion("uuid-hint-1", pkg)
	if got != RegionJP {
		t.Fatalf("hint auto-assign = %s, want JP", got)
	}
	d := readMap(t, p)
	if d.Packages[pkg] != "JP" {
		t.Errorf("packages[%s] = %q, want JP (自动分配应持久化)", pkg, d.Packages[pkg])
	}
	if d.UUIDs["uuid-hint-1"] != "JP" {
		t.Errorf("uuids[uuid-hint-1] = %q, want JP (粘性应持久化)", d.UUIDs["uuid-hint-1"])
	}

	// ④ 换包名/重装保号不漂移：同 uuid 换一个完全陌生包名，仍按粘性返回 JP
	if got := resolveRegion("uuid-hint-1", "com.other.name456"); got != RegionJP {
		t.Errorf("sticky after rename = %s, want JP", got)
	}
}

// terms 提示语言映射：ja→JP，en/ko→GL。
func TestRegionTermsLangMapping(t *testing.T) {
	setupRegionMap(t)
	noteTermsHint("com.x.enhint", "en")
	noteTermsHint("com.x.kohint", "ko")
	if got := resolveRegion("u-en", "com.x.enhint"); got != RegionGL {
		t.Errorf("en hint = %s, want GL", got)
	}
	if got := resolveRegion("u-ko", "com.x.kohint"); got != RegionGL {
		t.Errorf("ko hint = %s, want GL", got)
	}
}

// ① 显式映射优先于一切自动判定（用户登记任意共存改名包）。
func TestRegionExplicitMapWins(t *testing.T) {
	p := setupRegionMap(t)
	updateRegionMap(func(d *regionMapData) {
		d.Packages["com.i18n.rp.keeper"] = "GL"
	})
	// 同 uuid 先按 JP 粘住
	if got := resolveRegion("uuid-explicit", "com.square_enix.android_googleplay.nierspjp"); got != RegionJP {
		t.Fatalf("seed sticky = %s, want JP", got)
	}
	// 显式映射的包即使与粘性冲突也以映射为准
	if got := resolveRegion("uuid-explicit", "com.i18n.rp.keeper"); got != RegionGL {
		t.Errorf("explicit map = %s, want GL", got)
	}
	_ = readMap(t, p)
}

// ⑤ 兜底：陌生名无提示 → LUNAR_DEFAULT_REGION（默认 JP）；不粘住（留待后续纠正）。
func TestRegionFallbackDefault(t *testing.T) {
	p := setupRegionMap(t)
	got := resolveRegion("uuid-fallback", "com.unknown.nothing")
	if got != RegionJP {
		t.Errorf("default fallback = %s, want JP", got)
	}
	d := regionMapData{}
	if b, err := os.ReadFile(p); err == nil {
		if err := json.Unmarshal(b, &d); err != nil {
			t.Fatalf("parse region map: %v", err)
		}
	}
	d.normalize()
	if _, ok := d.UUIDs["uuid-fallback"]; ok {
		t.Errorf("纯兜底不应写入 uuid 粘性")
	}

	t.Setenv("LUNAR_DEFAULT_REGION", "GL")
	if got := resolveRegion("uuid-fallback-gl", "com.unknown.nothing2"); got != RegionGL {
		t.Errorf("LUNAR_DEFAULT_REGION=GL fallback = %s, want GL", got)
	}
}

// 兜底之后到来的提示仍可纠正（因为未粘住）。
func TestRegionHintCorrectsFallback(t *testing.T) {
	setupRegionMap(t)
	if got := resolveRegion("uuid-correct", "com.late.hinted"); got != RegionJP {
		t.Fatalf("initial fallback = %s, want JP", got)
	}
	noteTermsHint("com.late.hinted", "en")
	if got := resolveRegion("uuid-correct", "com.late.hinted"); got != RegionGL {
		t.Errorf("hint correction = %s, want GL", got)
	}
}

// 映射文件容忍 UTF-8 BOM（PowerShell -Encoding utf8 / 记事本写入）。
func TestRegionMapToleratesBOM(t *testing.T) {
	p := setupRegionMap(t)
	bomJSON := "\xef\xbb\xbf" + `{"packages":{"com.i18n.rp.keeper":"GL"}}`
	if err := os.WriteFile(p, []byte(bomJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := resolveRegion("uuid-bom", "com.i18n.rp.keeper"); got != RegionGL {
		t.Errorf("BOM map lookup = %s, want GL", got)
	}
}

// 官方名族模式函数本身（②），不含任何具体改名包名。
func TestRegionFromPackageFamily(t *testing.T) {
	cases := []struct {
		pkg  string
		want Region
	}{
		{"", RegionUnknown},
		{"com.square_enix.android_googleplay.nierspjp", RegionJP},
		{"nierspjp2", RegionJP},
		{"nierspww", RegionGL},
		{"prefix.nierspww6.suffix", RegionGL},
		{"com.example.totally.unknown", RegionUnknown}, // 无提示/映射 → 未知（由默认兜底处理）
	}
	for _, c := range cases {
		if got := regionFromPackageFamily(c.pkg); got != c.want {
			t.Errorf("regionFromPackageFamily(%q) = %s, want %s", c.pkg, got, c.want)
		}
	}
}
