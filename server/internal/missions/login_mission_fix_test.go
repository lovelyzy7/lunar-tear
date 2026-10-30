package missions

// login_mission_fix_test.go — BUGFIX-LOGIN-MISSION: 「登录」类任务进游戏即完成
// (a "Log in" mission completes on entering the game) and 日切后可再次完成 (it
// restarts and completes again after the game-day change).
//
// The device shape this covers: a save written under the real-time clock
// before the calendar's shift holds LastLoginDatetime / mission records in
// the game clock's future (user 1: last login 2026-10-25 real vs game clock
// 2023-05-15). The old `LastLoginDatetime >= today` test then read "already
// logged in today" forever — no login was ever counted and the daily 登录
// missions (月票每日 mid=2000101 / 任务页 mid=31/51/…) stayed 0/1.

import (
	"fmt"
	"strings"
	"testing"

	"lunar-tear/server/internal/gametime"
	"lunar-tear/server/internal/model"
	"lunar-tear/server/internal/store"
	"lunar-tear/server/internal/userdata"
)

const dayMillis = int64(24 * 3600 * 1000)

// dailyLoginMissions are the active daily "Log in" missions at now — the
// 任务页 login task (category 1) and the 月票每日「登录」 task (category 9).
func dailyLoginMissions(t *testing.T, cat *Catalog, now int64) []*Mission {
	t.Helper()
	var out []*Mission
	for _, m := range cat.Missions {
		if m.Daily() && m.Rule.Kind == "login" && cat.active(m, &store.UserState{}, now) {
			out = append(out, m)
		}
	}
	if len(out) < 2 {
		t.Fatalf("want the 任务页 and 月票 daily login missions active at %d, got %d", now, len(out))
	}
	return out
}

// TestDailyLoginMissionCompletesOnEnteringGame (模拟登录 → Login 任务
// progress=1/状态可领奖): entering the game completes every active daily
// "Log in" mission, both for a device-shaped save whose last login sits in
// the game clock's future and for a save created on this very game day (the
// seed counts that login without going through Track).
func TestDailyLoginMissionCompletesOnEnteringGame(t *testing.T) {
	cat := loadPortCatalog(t)
	now := portNow(t)
	dailies := dailyLoginMissions(t, cat, now)

	// Device shape: created under the real-time clock, so the last login is
	// more than three years in the game clock's future.
	deviceUser := func() *store.UserState {
		u := store.SeedUserState(1, "u", now, model.DefaultPlatform)
		u.Ext.MissionsVersion = trackVersion
		u.Login.TotalLoginCount = 1
		u.Login.ContinualLoginCount = 1
		u.Login.MaxContinualLoginCount = 1
		u.Login.LastLoginDatetime = now + 365*dayMillis
		u.Login.LatestVersion = 0
		return u
	}

	before, after := deviceUser(), deviceUser()
	cat.Track(before, after, "/apb.api.user.UserService/GameStart", &pbGameStart, now)

	for _, m := range dailies {
		rec := after.Missions[m.MissionId]
		if rec.ProgressValue != 1 {
			t.Errorf("daily login mission %d progress = %d, want 1", m.MissionId, rec.ProgressValue)
		}
		if rec.MissionProgressStatusType != StatusClear {
			t.Errorf("daily login mission %d status = %d, want %d (可领奖)", m.MissionId, rec.MissionProgressStatusType, StatusClear)
		}
	}
	// The skewed last login no longer blocks the day's login count.
	if after.Login.TotalLoginCount != 2 {
		t.Errorf("total logins = %d, want 2", after.Login.TotalLoginCount)
	}
	// The client is shown the cleared state (显示侧 IUserMission 投影).
	if rows := userdata.ProjectTables(*after, []string{"IUserMission"})["IUserMission"]; rows == "" {
		t.Error("IUserMission projection empty")
	} else {
		for _, m := range dailies {
			if !containsProjected(rows, m.MissionId) {
				t.Errorf("IUserMission projection missing mission %d: %s", m.MissionId, rows)
			}
		}
	}

	// A second request the same game day is not a second login.
	cat.Track(after, after, "/apb.api.user.UserService/GameStart", &pbGameStart, now+1000)
	if after.Login.TotalLoginCount != 2 {
		t.Errorf("second request of the day counted as a login: %d", after.Login.TotalLoginCount)
	}

	// Account-creation day: the seed already counted this day's login
	// (TotalLoginCount=1, LastLoginDatetime=now) without a Track — the daily
	// 登录 missions must still complete on entering the game.
	fresh := store.SeedUserState(2, "v", now, model.DefaultPlatform)
	fresh.Ext.MissionsVersion = trackVersion
	trackBefore := store.CloneUserState(*fresh)
	cat.Track(&trackBefore, fresh, "/apb.api.user.UserService/GameStart", &pbGameStart, now)
	for _, m := range dailies {
		if rec := fresh.Missions[m.MissionId]; rec.ProgressValue != 1 || rec.MissionProgressStatusType != StatusClear {
			t.Errorf("creation-day login mission %d = %+v, want 1 / clear", m.MissionId, rec)
		}
	}
	if fresh.Login.TotalLoginCount != 1 {
		t.Errorf("creation-day total logins = %d, want 1 (seed's login)", fresh.Login.TotalLoginCount)
	}
}

// TestDailyLoginMissionResetsAndCompletesAgain (日切后重置可再次完成): a daily
// 登录 mission cleared on a record stamped in the game clock's future (how the
// real-time clock left it) must restart on the next game day and complete
// again — the old `StartDatetime < today` reset never fired for such records,
// and set() then kept the stale StatusClear.
func TestDailyLoginMissionResetsAndCompletesAgain(t *testing.T) {
	cat := loadPortCatalog(t)
	now := portNow(t)
	today := gametime.StartOfDay(now)
	tomorrow := today + dayMillis
	dailies := dailyLoginMissions(t, cat, now)

	future := now + 365*dayMillis
	u := store.SeedUserState(1, "u", now, model.DefaultPlatform)
	u.Ext.MissionsVersion = trackVersion
	u.Login.TotalLoginCount = 1
	u.Login.LastLoginDatetime = future
	for _, m := range dailies {
		// Yesterday's clear, written under the real-time clock: the whole
		// record is stamped in the game clock's future.
		u.Missions[m.MissionId] = store.UserMissionState{
			MissionId: m.MissionId, StartDatetime: future,
			ProgressValue: 1, MissionProgressStatusType: StatusClear,
			ClearDatetime: future, LatestVersion: future,
		}
	}

	before := store.CloneUserState(*u)
	cat.Track(&before, u, "/apb.api.user.UserService/GameStart", &pbGameStart, tomorrow)
	for _, m := range dailies {
		rec := u.Missions[m.MissionId]
		// Reset: the record now belongs to the new game day...
		if rec.StartDatetime < tomorrow || rec.StartDatetime >= tomorrow+dayMillis {
			t.Errorf("daily login mission %d not reset at day change: %+v", m.MissionId, rec)
		}
		// ...and the day's login completes it again (可再次完成).
		if rec.ProgressValue != 1 || rec.MissionProgressStatusType != StatusClear {
			t.Errorf("daily login mission %d not completed again after day change: %+v", m.MissionId, rec)
		}
		if rec.ClearDatetime != tomorrow {
			t.Errorf("daily login mission %d clear time %+v, want the new day %d", m.MissionId, rec.ClearDatetime, tomorrow)
		}
	}
	if u.Login.TotalLoginCount != 2 {
		t.Errorf("total logins = %d, want 2", u.Login.TotalLoginCount)
	}
}

// containsProjected reports whether an IUserMission projection row names the
// mission ("missionId":N, — the comma keeps 200010 from matching 2000101).
func containsProjected(rows string, missionId int32) bool {
	return strings.Contains(rows, fmt.Sprintf(`"missionId":%d,`, missionId))
}
