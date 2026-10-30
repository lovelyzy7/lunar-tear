package service

// login_bonus_clock_skew_test.go — BUGFIX-LOGIN-MISSION 顺带修：「签到族」的
// 同病（每日印章/活动签到卡）。A save written under the real-time clock holds
// LatestRewardReceiveDatetime in the game clock's future (device: 2026-10-26
// real vs game clock 2023-05-15); `>= today` then read "already stamped
// today" forever and the stamp cards froze.
//
// Runs against the master data shipped with the server
// (assets/release/20240404193219.bin.e), like gacha_window_borrow_test.go, so
// it needs no BUNKER_TEST_MASTER.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"lunar-tear/server/internal/runtime"
	"lunar-tear/server/internal/store"

	emptypb "google.golang.org/protobuf/types/known/emptypb"
)

func TestLoginBonusStampsAfterClockSkew(t *testing.T) {
	mdPath := filepath.Join("..", "..", "assets", "release", "20240404193219.bin.e")
	if _, err := os.Stat(mdPath); err != nil {
		t.Skipf("master data not available at %s: %v", mdPath, err)
	}
	holder, err := runtime.NewHolder(mdPath)
	if err != nil {
		t.Fatalf("load master data: %v", err)
	}
	users := newMemUsers()
	srv := NewLoginBonusServiceServer(users, users, holder)
	ctx := context.Background()
	const day = int64(86400000)
	launchEvent := int64(1627509600000) // login bonus 2 opens (launch), ends 2021-09-08
	setClock(t, launchEvent+day)

	// Both cards last stamped in the game clock's future.
	future := launchEvent + 365*day
	users.user.LoginBonus.LatestRewardReceiveDatetime = future
	users.user.Ext.EventLoginBonuses = map[int32]store.UserLoginBonusState{
		2: {LoginBonusId: 2, CurrentPageNumber: 1, LatestRewardReceiveDatetime: future},
	}
	everydayStamp := users.user.LoginBonus.CurrentStampNumber
	gifts := len(users.user.Gifts.NotReceived)

	if _, err := srv.ReceiveStamp(ctx, &emptypb.Empty{}); err != nil {
		t.Fatal(err)
	}
	if users.user.LoginBonus.CurrentStampNumber != everydayStamp+1 {
		t.Fatalf("everyday card not stamped despite the future stamp time: %+v", users.user.LoginBonus)
	}
	if card := users.user.Ext.EventLoginBonuses[2]; card.CurrentStampNumber != 1 {
		t.Fatalf("event card 2 not stamped despite the future stamp time: %+v", card)
	}
	if len(users.user.Gifts.NotReceived) < gifts+2 {
		t.Fatalf("gifts %d -> %d, want the everyday stamp and the event stamp", gifts, len(users.user.Gifts.NotReceived))
	}

	// The same game day still takes one stamp per card.
	before := len(users.user.Gifts.NotReceived)
	srv.ReceiveStamp(ctx, &emptypb.Empty{})
	if len(users.user.Gifts.NotReceived) != before || users.user.LoginBonus.CurrentStampNumber != everydayStamp+1 {
		t.Fatal("stamped twice in one day")
	}
}
