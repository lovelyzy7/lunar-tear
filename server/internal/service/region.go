package service

import (
	"bytes"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"lunar-tear/server/internal/store"
)

// Region 客户端区域：JP=日服，GL=全球服。
type Region int

const (
	RegionUnknown Region = iota
	RegionJP
	RegionGL
)

func (r Region) String() string {
	switch r {
	case RegionJP:
		return "JP"
	case RegionGL:
		return "GL"
	}
	return "unknown"
}

// RegionFromString 解析 "JP"/"GL"（大小写不敏感）。
func RegionFromString(s string) Region {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "JP":
		return RegionJP
	case "GL":
		return RegionGL
	}
	return RegionUnknown
}

// 各区域的主数据文件与版本串前缀（下发映射保持现状）。
const (
	jpMasterVersion = "20240405123139"
	jpMasterFile    = "assets/release/20240405123139.bin.e"
	glMasterVersion = "20240404193219"
	glMasterFile    = "assets/release/20240404193219.bin.e"
)

// regionMasterData 返回该区域应下发的主数据文件与版本串前缀。
// 环境变量是显式覆盖（单区/自定义兼容）：LUNAR_MASTERDATA 覆盖文件、
// LUNAR_MASTERDATA_VERSION 覆盖版本串前缀，两者对所有区域生效。
func regionMasterData(r Region) (path, base string) {
	switch r {
	case RegionJP:
		path, base = jpMasterFile, jpMasterVersion
	default: // RegionGL / RegionUnknown
		path, base = glMasterFile, glMasterVersion
	}
	if v := envOr("LUNAR_MASTERDATA", ""); v != "" {
		path = v
	}
	if v := envOr("LUNAR_MASTERDATA_VERSION", ""); v != "" {
		base = v
	}
	return path, base
}

// masterFileExists 检查主数据文件是否存在；抽出变量便于单元测试注入。
var masterFileExists = func(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// alternativeMasterFile 返回交叉共用的另一份主数据（JP/GL 互换）。
func alternativeMasterFile(path string) string {
	switch path {
	case jpMasterFile:
		return glMasterFile
	case glMasterFile:
		return jpMasterFile
	}
	return ""
}

// resolveMasterData 返回实际应使用/下发的主数据文件与版本串前缀（含自动回退）：
//
//  1. LUNAR_MASTERDATA / LUNAR_MASTERDATA_VERSION 显式覆盖最优先（不回退）；
//  2. 区域指定文件存在则用它；
//  3. 指定文件缺失但另一份存在 -> 回退到实际存在的那份（交叉共用：单份主数据
//     同时供 JP 与 GL），版本串 = 实际文件的 base+mtime，并打明确日志；
//  4. 两份都缺失 -> 保持区域指定（上层按缺文件处理）。
func resolveMasterData(r Region) (path, base string, fellBack bool) {
	envFile := envOr("LUNAR_MASTERDATA", "")
	envBase := envOr("LUNAR_MASTERDATA_VERSION", "")

	if envFile != "" {
		// 显式覆盖最优先：不参与回退。
		_, regionBase := regionMasterData(r)
		if envBase != "" {
			regionBase = envBase
		}
		return envFile, regionBase, false
	}

	path, base = regionMasterData(r)
	if envBase != "" {
		base = envBase
	}
	if masterFileExists(path) {
		return path, base, false
	}

	if alt := alternativeMasterFile(path); alt != "" && masterFileExists(alt) {
		altBase := jpMasterVersion
		if alt == glMasterFile {
			altBase = glMasterVersion
		}
		if envBase != "" {
			altBase = envBase
		}
		log.Printf("[region] 主数据回退: %s 指定文件 %s 缺失，改用 %s（单份主数据交叉共用，版本串按实际文件 %s）", r, path, alt, altBase)
		return alt, altBase, true
	}

	log.Printf("[region] 主数据缺失: 区域 %s 指定文件 %s 不存在且无回退（两份主数据均缺失）", r, path)
	return path, base, false
}

// ServerMasterDataPath 返回服务端目录（runtime.Holder，游戏逻辑用）加载的主数据：
// LUNAR_MASTERDATA 显式覆盖最优先；否则按历史默认 GL 文件名，缺失时回退实际
// 存在的那份（单份主数据交叉共用，JP/GL 客户端均可正常服务）。
func ServerMasterDataPath() string {
	if v := envOr("LUNAR_MASTERDATA", ""); v != "" {
		return v
	}
	if masterFileExists(glMasterFile) {
		return glMasterFile
	}
	if masterFileExists(jpMasterFile) {
		log.Printf("[region] 服务端目录主数据回退: %s 缺失，改用 %s（单份主数据交叉共用）", glMasterFile, jpMasterFile)
		return jpMasterFile
	}
	log.Printf("[region] 服务端目录主数据缺失: %s 与 %s 均不存在", glMasterFile, jpMasterFile)
	return glMasterFile
}

// masterDataFileForRequest 从请求路径中的版本串前缀映射回对应主数据文件
// （CDN 的 /master-data/{version}、/assets/release/{version}/database.bin）。
func masterDataFileForRequest(requestPath string) string {
	if v := envOr("LUNAR_MASTERDATA", ""); v != "" {
		return v
	}
	switch {
	case strings.Contains(requestPath, jpMasterVersion):
		return jpMasterFile
	case strings.Contains(requestPath, glMasterVersion):
		return glMasterFile
	}
	return glMasterFile
}

// ---- 区域映射文件（db/region_map.json，LUNAR_REGION_MAP 可改路径） ----
//
// {
//   "packages": {"<包名>": "JP|GL"},     // 显式映射：用户登记或自动分配
//   "hints":    {"<包名>": "ja|en|ko"},  // CDN 条款页语言提示（观测证据）
//   "uuids":    {"<uuid>": "JP|GL"}      // 每 UUID 粘性（首次判定后沿用）
// }
//
// 文件启动加载、运行中按 mtime 热读；自动分配与提示写回（原子替换）。
const regionMapDefaultPath = "db/region_map.json"

func regionMapPath() string {
	return envOr("LUNAR_REGION_MAP", regionMapDefaultPath)
}

type regionMapData struct {
	Packages map[string]string `json:"packages"`
	Hints    map[string]string `json:"hints"`
	UUIDs    map[string]string `json:"uuids"`
}

func (d *regionMapData) normalize() {
	if d.Packages == nil {
		d.Packages = map[string]string{}
	}
	if d.Hints == nil {
		d.Hints = map[string]string{}
	}
	if d.UUIDs == nil {
		d.UUIDs = map[string]string{}
	}
}

var (
	regionFileMu    sync.Mutex
	regionFileCache *regionMapData
	regionFileMtime int64
)

// loadRegionMap 读取映射文件（mtime 热读）。文件缺失或损坏时返回空表且不落盘。
func loadRegionMap() *regionMapData {
	p := regionMapPath()
	st, err := os.Stat(p)
	if err != nil {
		return &regionMapData{}
	}
	if regionFileCache != nil && st.ModTime().UnixNano() == regionFileMtime {
		return regionFileCache
	}
	b, err := os.ReadFile(p)
	if err != nil {
		log.Printf("[region] read %s: %v", p, err)
		return &regionMapData{}
	}
	var d regionMapData
	// 容忍 UTF-8 BOM（Windows 记事本/PowerShell 常见）。
	b = bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF})
	if err := json.Unmarshal(b, &d); err != nil {
		log.Printf("[region] parse %s: %v（忽略本次内容）", p, err)
		return &regionMapData{}
	}
	d.normalize()
	regionFileCache, regionFileMtime = &d, st.ModTime().UnixNano()
	return &d
}

// updateRegionMap 加载-修改-原子写回（进程内串行）。
func updateRegionMap(mut func(*regionMapData)) {
	regionFileMu.Lock()
	defer regionFileMu.Unlock()
	p := regionMapPath()
	d := &regionMapData{}
	if b, err := os.ReadFile(p); err == nil {
		b = bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF})
		_ = json.Unmarshal(b, d)
	}
	d.normalize()
	mut(d)
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		log.Printf("[region] marshal region map: %v", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		log.Printf("[region] mkdir region map: %v", err)
		return
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		log.Printf("[region] write %s: %v", tmp, err)
		return
	}
	if err := os.Rename(tmp, p); err != nil {
		log.Printf("[region] rename %s: %v", tmp, err)
		return
	}
	if st, err := os.Stat(p); err == nil {
		regionFileCache, regionFileMtime = d, st.ModTime().UnixNano()
	}
}

// ---- terms 语言提示（CDN 侧观测，octo.go 调用） ----
//
// 客户端启动会请求条款页：JP 构建请求 /web/static/ja/terms/… 且带
// X-Requested-With: <包名>；GL 构建请求 en/ko。这里只记录证据，
// 真正的区域分配由 gRPC 侧 resolveRegion 完成（可为任意改名包自动分配）。
func noteTermsHint(packageName, termsLang string) {
	pkg := strings.TrimSpace(packageName)
	lang := strings.ToLower(strings.TrimSpace(termsLang))
	if pkg == "" {
		return
	}
	if regionFromTermsLang(lang) == RegionUnknown {
		return
	}
	updateRegionMap(func(d *regionMapData) {
		if d.Hints[pkg] != lang {
			d.Hints[pkg] = lang
		}
	})
	log.Printf("[region] terms hint: package=%q lang=%s", pkg, lang)
}

// regionFromTermsLang 语言提示 → 区域依据：ja→JP；en/ko→GL。
func regionFromTermsLang(lang string) Region {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "ja":
		return RegionJP
	case "en", "ko":
		return RegionGL
	}
	return RegionUnknown
}

// ---- 官方名族宽松模式（②；非单个名字硬编码） ----
//
// 官方名族：nierspjp* → JP，nierspww* → GL。前后缀容错（子串匹配），
// 覆盖 nierspjp2 / nierspww6 这类简单改名；完全改名的包靠配置映射或
// terms 提示判定。注意：不要在此登记任何具体共存改名包（如 nierspjq6）。
func regionFromPackageFamily(pkg string) Region {
	p := strings.ToLower(strings.TrimSpace(pkg))
	switch {
	case p == "":
		return RegionUnknown
	case strings.Contains(p, "nierspjp"):
		return RegionJP
	case strings.Contains(p, "nierspww"):
		return RegionGL
	}
	return RegionUnknown
}

// defaultRegion 兜底区域：LUNAR_DEFAULT_REGION（默认 JP）。
func defaultRegion() Region {
	v := strings.ToUpper(strings.TrimSpace(envOr("LUNAR_DEFAULT_REGION", "")))
	switch v {
	case "":
		return RegionJP
	case "JP":
		return RegionJP
	case "GL":
		return RegionGL
	}
	log.Printf("[region] LUNAR_DEFAULT_REGION=%q 无法识别，按 JP 处理（可选 JP/GL）", v)
	return RegionJP
}

// ---- 判定主入口 ----
//
// resolveRegion 决定 (uuid, pkg) 的区域，优先级：
//  1. packages 显式映射（用户登记 / 此前自动分配）
//  2. uuids 粘性（首次判定持久化；换包名/重装保号不漂移）
//  3. terms 语言提示 → 自动分配 + 持久化 + 日志
//  4. 官方名族模式（不持久化，随包名自证）
//  5. LUNAR_DEFAULT_REGION 兜底 + 未知包名告警（不持久化，便于后续纠正）
//
// 除纯兜底外均写入 uuids 粘性。
func resolveRegion(uuid, pkg string) Region {
	uuid = strings.TrimSpace(uuid)
	pkg = strings.TrimSpace(pkg)
	d := loadRegionMap()

	// 1) 显式包映射
	if pkg != "" {
		if r := RegionFromString(d.Packages[pkg]); r != RegionUnknown {
			stickRegion(uuid, r)
			return r
		}
	}

	// 2) UUID 粘性
	if uuid != "" {
		if r := RegionFromString(d.UUIDs[uuid]); r != RegionUnknown {
			return r
		}
	}

	// 3) terms 语言提示 → 自动分配
	if pkg != "" {
		if r := regionFromTermsLang(d.Hints[pkg]); r != RegionUnknown {
			updateRegionMap(func(dd *regionMapData) {
				dd.Packages[pkg] = r.String()
			})
			stickRegion(uuid, r)
			log.Printf("[region] 为包 %s 自动分配 %s，依据 terms lang=%s", pkg, r, d.Hints[pkg])
			return r
		}
	}

	// 4) 官方名族模式
	if r := regionFromPackageFamily(pkg); r != RegionUnknown {
		stickRegion(uuid, r)
		return r
	}

	// 5) 兜底 + 告警（不粘住，便于后续提示/映射纠正）
	r := defaultRegion()
	log.Printf("[region] 未知包名 %q（uuid=%s）：按默认区域 %s 处理；请在 %s 的 packages 登记映射，或让客户端先请求条款页以自动分配", pkg, uuid, r, regionMapPath())
	return r
}

// stickRegion 记录每 UUID 粘性（首次判定持久化）。
func stickRegion(uuid string, r Region) {
	if uuid == "" || r == RegionUnknown {
		return
	}
	updateRegionMap(func(d *regionMapData) {
		d.UUIDs[uuid] = r.String()
	})
}

// ---- 会话内区域缓存（供 DataService / gRPC 会话使用） ----
var (
	regionMu     sync.RWMutex
	regionByUser = map[int64]Region{}
)

func rememberRegionForUser(userId int64, r Region) {
	if r == RegionUnknown || userId == 0 {
		return
	}
	regionMu.Lock()
	regionByUser[userId] = r
	regionMu.Unlock()
}

// regionForUser 查会话区域：先查进程内缓存（Auth 时登记），
// 再经 users 表 uuid 回查持久映射（服务重启后仍粘住）。
func regionForUser(userId int64, users store.UserRepository) Region {
	regionMu.RLock()
	r, ok := regionByUser[userId]
	regionMu.RUnlock()
	if ok {
		return r
	}
	if users != nil {
		if u, err := users.LoadUser(userId); err == nil {
			d := loadRegionMap()
			if rr := RegionFromString(d.UUIDs[u.Uuid]); rr != RegionUnknown {
				rememberRegionForUser(userId, rr)
				return rr
			}
		}
	}
	return RegionUnknown
}

// ---- tr blob 包名提取（备用身份输入；不参与区域判定本身） ----
//
// Auth 的 tr 内嵌转义 JSON（含 package_name），这里只取包名字符串，
// 区域由 resolveRegion 统一决定。
var rePackageName = regexp.MustCompile(`package_name\\*" *: *\\*"([^"\\]+)`)

func packageNameFromTr(tr string) string {
	if m := rePackageName.FindStringSubmatch(tr); len(m) == 2 {
		return strings.TrimSpace(m[1])
	}
	return ""
}

// logRegion 记录一次区域判定，便于联调时从日志证明分区正确。
func logRegion(source, ident string, r Region) {
	log.Printf("[region] %s: %s -> %s", source, ident, r)
}
