package service

import "testing"

// fakeMasterFiles 注入存在性检查（返回可还原函数）。
func fakeMasterFiles(t *testing.T, jp, gl bool) {
	t.Helper()
	orig := masterFileExists
	masterFileExists = func(p string) bool {
		switch p {
		case jpMasterFile:
			return jp
		case glMasterFile:
			return gl
		}
		return false
	}
	t.Cleanup(func() { masterFileExists = orig })
	t.Setenv("LUNAR_MASTERDATA", "")
	t.Setenv("LUNAR_MASTERDATA_VERSION", "")
}

// 用例矩阵 {双文件, 仅GL, 仅JP} x {JP客户端, GL客户端}。
func TestResolveMasterDataMatrix(t *testing.T) {
	cases := []struct {
		name       string
		jp, gl     bool
		region     Region
		wantPath   string
		wantBase   string
		wantFallBk bool
	}{
		{"both/jp", true, true, RegionJP, jpMasterFile, jpMasterVersion, false},
		{"both/gl", true, true, RegionGL, glMasterFile, glMasterVersion, false},
		{"gl-only/jp", false, true, RegionJP, glMasterFile, glMasterVersion, true},
		{"gl-only/gl", false, true, RegionGL, glMasterFile, glMasterVersion, false},
		{"jp-only/jp", true, false, RegionJP, jpMasterFile, jpMasterVersion, false},
		{"jp-only/gl", true, false, RegionGL, jpMasterFile, jpMasterVersion, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fakeMasterFiles(t, c.jp, c.gl)
			gotPath, gotBase, gotFB := resolveMasterData(c.region)
			if gotPath != c.wantPath || gotBase != c.wantBase || gotFB != c.wantFallBk {
				t.Errorf("resolveMasterData(%s) = (%s, %s, %v), want (%s, %s, %v)",
					c.region, gotPath, gotBase, gotFB, c.wantPath, c.wantBase, c.wantFallBk)
			}
		})
	}
}

// 两份都缺失：保持区域指定（不回退），上层按缺文件处理。
func TestResolveMasterDataBothMissing(t *testing.T) {
	fakeMasterFiles(t, false, false)
	gotPath, gotBase, gotFB := resolveMasterData(RegionJP)
	if gotPath != jpMasterFile || gotBase != jpMasterVersion || gotFB {
		t.Errorf("both missing = (%s, %s, %v), want (jp file, jp base, false)", gotPath, gotBase, gotFB)
	}
}

// ③ env 覆盖最优先：即使区域文件缺失也不回退；版本前缀可用 env 指定。
func TestResolveMasterDataEnvOverride(t *testing.T) {
	fakeMasterFiles(t, false, false) // 两份都没有
	t.Setenv("LUNAR_MASTERDATA", "custom/master.bin.e")
	t.Setenv("LUNAR_MASTERDATA_VERSION", "99999999999999")
	gotPath, gotBase, gotFB := resolveMasterData(RegionJP)
	if gotPath != "custom/master.bin.e" || gotBase != "99999999999999" || gotFB {
		t.Errorf("env override = (%s, %s, %v), want (custom/master.bin.e, 99999999999999, false)", gotPath, gotBase, gotFB)
	}

	// 仅 env 文件：版本前缀仍按区域实际规则（此处=区域 base）
	t.Setenv("LUNAR_MASTERDATA_VERSION", "")
	fakeMasterFiles(t, false, false)
	t.Setenv("LUNAR_MASTERDATA", "custom/master.bin.e")
	gotPath, gotBase, _ = resolveMasterData(RegionGL)
	if gotPath != "custom/master.bin.e" || gotBase != glMasterVersion {
		t.Errorf("env file only = (%s, %s), want (custom/master.bin.e, %s)", gotPath, gotBase, glMasterVersion)
	}
}

// CDN 侧映射：版本前缀 -> 指定文件（回退由 serveDatabaseBinE 的存在性检查完成）。
func TestMasterDataFileForRequest(t *testing.T) {
	t.Setenv("LUNAR_MASTERDATA", "")
	if got := masterDataFileForRequest("/master-data/" + jpMasterVersion + "_123"); got != jpMasterFile {
		t.Errorf("jp prefix map = %s, want %s", got, jpMasterFile)
	}
	if got := masterDataFileForRequest("/master-data/" + glMasterVersion + "_123"); got != glMasterFile {
		t.Errorf("gl prefix map = %s, want %s", got, glMasterFile)
	}
	t.Setenv("LUNAR_MASTERDATA", "custom/master.bin.e")
	if got := masterDataFileForRequest("/master-data/whatever"); got != "custom/master.bin.e" {
		t.Errorf("env map = %s, want custom/master.bin.e", got)
	}
}
