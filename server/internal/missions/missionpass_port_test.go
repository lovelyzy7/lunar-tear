package missions

// missionpass_port_test.go — B 方案 required cover: 月票发奖 (mission pass
// rewards, both tracks + overflow handling + ended-pass remaining reward) and
// 任务进度入账 (mission progress ingest through Track). Unlike missions_test.go
// these run against the master data shipped with the server
// (assets/release/20240404193219.bin.e), so `go test ./...` exercises them
// without BUNKER_TEST_MASTER/BUNKER_TEST_SAVE.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"lunar-tear/server/internal/gametime"
	"lunar-tear/server/internal/masterdata/memorydb"
	"lunar-tear/server/internal/model"
	"lunar-tear/server/internal/store"
)

var portTestMaster = filepath.Join("..", "..", "assets", "release", "20240404193219.bin.e")

func loadPortCatalog(t *testing.T) *Catalog {
	t.Helper()
	if _, err := os.Stat(portTestMaster); err != nil {
		t.Skipf("master data not available at %s: %v", portTestMaster, err)
	}
	if err := memorydb.Init(portTestMaster); err != nil {
		t.Fatalf("memorydb.Init: %v", err)
	}
	cat, err := load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cat == nil || len(cat.Passes) == 0 {
		t.Fatal("no mission passes in master data")
	}
	return cat
}

// portNow is inside the game data's own timeline (2023-11-15 00:00 GMT-8), so
// the dated terms of the 2024-04 snapshot are live.
func portNow(t *testing.T) int64 {
	t.Helper()
	tm, err := time.ParseInLocation("2006-01-02 15:04", "2023-11-15 00:00", time.FixedZone("GMT-8", -8*60*60))
	if err != nil {
		t.Fatal(err)
	}
	return tm.UnixMilli()
}

// passWithPointReward finds a mission pass that real mission rewards pay into
// (possession type 15 = mission pass point) and the mission that pays it.
func passWithPointReward(t *testing.T, cat *Catalog) (*Mission, int32) {
	t.Helper()
	for _, m := range cat.Missions {
		for _, r := range cat.Rewards[m.MissionRewardId] {
			if model.PossessionType(r.PossessionType) == model.PossessionTypeMissionPassPoint && r.PossessionId != 0 {
				return m, r.PossessionId
			}
		}
	}
	t.Fatal("no mission pays mission pass points")
	return nil, 0
}

func granter() *store.PossessionGranter {
	return &store.PossessionGranter{Limits: store.PossessionLimits{
		Weapon: 9999, Parts: 9999, Material: 999999, ConsumableItem: 999999, Money: 999999999,
		ImportantItem: 9999, GachaMedal: 9999,
	}}
}

// TestMissionRewardPaysPassPoints: a cleared mission's PossessionType=15
// reward lands in Ext.MissionPass (月票积分入账), the rest is granted normally.
func TestMissionRewardPaysPassPoints(t *testing.T) {
	cat := loadPortCatalog(t)
	now := portNow(t)
	m, passId := passWithPointReward(t, cat)

	var wantPoints int32
	for _, r := range cat.Rewards[m.MissionRewardId] {
		if model.PossessionType(r.PossessionType) == model.PossessionTypeMissionPassPoint {
			wantPoints += r.Count
		}
	}

	user := store.SeedUserState(1, "u", now, model.DefaultPlatform)
	user.Missions[m.MissionId] = store.UserMissionState{
		MissionId: m.MissionId, MissionProgressStatusType: StatusClear, ClearDatetime: now,
	}
	g := granter()
	received := cat.ReceiveMissions(user, g, []int32{m.MissionId}, now)
	if len(received) != len(cat.Rewards[m.MissionRewardId]) {
		t.Fatalf("received %d rewards, want %d", len(received), len(cat.Rewards[m.MissionRewardId]))
	}
	p := user.Ext.MissionPass[passId]
	if p.MissionPassId != passId || p.Point != wantPoints {
		t.Fatalf("pass %d point = %d (id %d), want %d", passId, p.Point, p.MissionPassId, wantPoints)
	}
	if p.LatestVersion != now {
		t.Fatalf("point timestamp %d, want %d", p.LatestVersion, now)
	}
	// Received is recorded and cannot be claimed twice.
	if user.Missions[m.MissionId].MissionProgressStatusType != StatusRewardReceived {
		t.Fatalf("mission status %d, want %d", user.Missions[m.MissionId].MissionProgressStatusType, StatusRewardReceived)
	}
	if again := cat.ReceiveMissions(user, g, []int32{m.MissionId}, now); len(again) != 0 {
		t.Fatalf("second claim paid %v", again)
	}
}

// TestMissionPassRewardsBothTracks: ReceivePass pays the standard track and —
// only with the pass's premium item held — the premium track, never paying a
// level twice and advancing both received-level watermarks.
func TestMissionPassRewardsBothTracks(t *testing.T) {
	cat := loadPortCatalog(t)
	now := portNow(t)
	_, passId := passWithPointReward(t, cat)
	pass, ok := cat.Passes[passId]
	if !ok {
		t.Fatalf("pass %d not in catalog", passId)
	}
	levels := cat.PassLevels[pass.MissionPassLevelGroupId]
	if len(levels) < 2 {
		t.Fatalf("pass %d has %d levels, want >= 2", passId, len(levels))
	}
	// Enough points for the second level that declares one.
	var need int32
	for _, l := range levels {
		if l.Level <= 2 && l.NecessaryPoint > need {
			need = l.NecessaryPoint
		}
	}

	run := func(withPremium bool) (paid []Reward, user *store.UserState) {
		user = store.SeedUserState(1, "u", now, model.DefaultPlatform)
		user.Ext.MissionPass = map[int32]store.MissionPassPointState{
			passId: {MissionPassId: passId, Point: need},
		}
		if withPremium {
			user.PremiumItems = map[int32]int64{}
			if pass.PremiumItemId != 0 {
				user.PremiumItems[pass.PremiumItemId] = now
			}
		}
		g := granter()
		paid = cat.ReceivePass(user, g, passId, now)
		return paid, user
	}

	std, stdUser := run(false)
	prem, premUser := run(true)

	// Standard rewards are paid either way; premium rewards only with the item.
	if len(std) == 0 {
		t.Fatal("standard track paid nothing at level >= 1")
	}
	if pass.PremiumItemId != 0 && len(prem) <= len(std) {
		t.Fatalf("premium track paid %d, standard %d: premium item must unlock more", len(prem), len(std))
	}

	level := cat.PassLevel(passId, need)
	if got := stdUser.Ext.MissionPass[passId]; got.NoPremiumRewardReceivedLevel != level || got.PremiumRewardReceivedLevel != 0 {
		t.Fatalf("no-premium watermarks: %+v (level %d)", got, level)
	}
	if pass.PremiumItemId != 0 {
		if got := premUser.Ext.MissionPass[passId]; got.PremiumRewardReceivedLevel != level {
			t.Fatalf("premium watermark %+v, want level %d", got, level)
		}
	}

	// A second claim pays nothing more (level watermarks hold).
	g := granter()
	if again := cat.ReceivePass(premUser, g, passId, now); len(again) != 0 {
		t.Fatalf("second claim paid %v", again)
	}

	// End-of-pass remaining reward pays what is left on an ended pass.
	pass2 := pass
	pass2.EndDatetime = now - 1 // force "ended"
	cat.Passes[passId] = pass2
	fresh := store.SeedUserState(1, "u", now, model.DefaultPlatform)
	fresh.Ext.MissionPass = map[int32]store.MissionPassPointState{passId: {MissionPassId: passId, Point: need}}
	endedAt := now + 3600_000
	last := cat.ReceiveEndedPasses(fresh, granter(), endedAt)
	if last != passId {
		t.Fatalf("ReceiveEndedPasses = %d, want %d", last, passId)
	}
	if got := fresh.Ext.MissionPass[passId]; got.NoPremiumRewardReceivedLevel != level {
		t.Fatalf("ended pass did not pay the remaining standard rewards: %+v", got)
	}
	cat.Passes[passId] = pass // restore for other tests
}

// TestMissionProgressIngest (任务进度入账): Track turns save changes into
// mission progress — a login and a quest clear — and daily missions restart
// each game day.
func TestMissionProgressIngest(t *testing.T) {
	cat := loadPortCatalog(t)
	now := portNow(t)
	today := gametime.StartOfDay(now)

	var loginMission, clearMission *Mission
	for _, m := range cat.Missions {
		if !cat.active(m, &store.UserState{}, now) || m.Rule.Kind == "" {
			continue
		}
		if loginMission == nil && m.Rule.Kind == "login" {
			loginMission = m
		}
		if clearMission == nil && m.Rule.Kind == "stamina" {
			clearMission = m
		}
	}
	if loginMission == nil {
		t.Fatal("no active login mission")
	}

	newUser := func() *store.UserState {
		u := store.SeedUserState(1, "u", now, model.DefaultPlatform)
		u.Ext.MissionsVersion = trackVersion // no backfill: pure increment path
		u.Login.LastLoginDatetime = today - 24*3600*1000 // yesterday: today's request logs in
		u.Login.TotalLoginCount = 5
		return u
	}

	// A login request: recordLogin counts the day's first request, and the
	// "login" mission grows by one.
	before := newUser()
	after := newUser()
	cat.Track(before, after, "/apb.api.user.UserService/GameStart", &pbGameStart, now)
	rec := after.Missions[loginMission.MissionId]
	if rec.ProgressValue != 1 {
		t.Fatalf("login progress = %d, want 1", rec.ProgressValue)
	}
	if rec.MissionProgressStatusType != StatusInProgress && rec.MissionProgressStatusType != StatusClear {
		t.Fatalf("login mission status %d", rec.MissionProgressStatusType)
	}
	if after.Login.TotalLoginCount != 6 {
		t.Fatalf("total logins = %d, want 6", after.Login.TotalLoginCount)
	}

	// A quest clear: the quest-clear counter feeds the quest/stamina rules.
	if clearMission != nil {
		questId := int32(0)
		for id, n := range cat.QuestStamina {
			if n > 0 {
				questId = id
				break
			}
		}
		if questId != 0 {
			before, after = newUser(), newUser()
			before.Quests[questId] = store.UserQuestState{QuestId: questId, ClearCount: 1}
			after.Quests[questId] = store.UserQuestState{QuestId: questId, ClearCount: 2}
			cat.Track(before, after, "/apb.api.quest.QuestService/FinishMainQuest", &pbFinishMain, now)
			rec := after.Missions[clearMission.MissionId]
			if rec.ProgressValue <= 0 {
				t.Fatalf("quest clear progress = %d, want > 0", rec.ProgressValue)
			}
		}
	}

	// Daily missions restart on a new game day.
	var daily *Mission
	for _, m := range cat.Missions {
		if m.Daily() && m.Rule.Kind == "login" && cat.active(m, &store.UserState{}, now) {
			daily = m
			break
		}
	}
	if daily != nil {
		before, after = newUser(), newUser()
		after.Missions[daily.MissionId] = store.UserMissionState{
			MissionId: daily.MissionId, StartDatetime: today - 24*3600*1000,
			MissionProgressStatusType: StatusClear, ProgressValue: 9, LatestVersion: today - 24*3600*1000,
		}
		cat.Track(before, after, "/apb.api.user.UserService/GameStart", &pbGameStart, now)
		if rec := after.Missions[daily.MissionId]; rec.ProgressValue == 9 {
			t.Fatalf("daily mission not reset on a new day: %+v", rec)
		}
	}
}

// Tiny request stubs the engine type-switches on; only the method name and
// save diff matter here.
var pbGameStart = struct{}{}
var pbFinishMain = struct{ IsRetired, IsAnnihilated bool }{}
