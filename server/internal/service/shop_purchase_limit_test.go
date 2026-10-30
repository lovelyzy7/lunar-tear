package service

// shop_purchase_limit_test.go — B 方案 "商店限购" cover: the Buy-time stock
// check (shopItemInStock) and the periodic quota refill (resetShopStock) work
// together. Runs on synthetic tables (no master data needed).

import (
	"testing"

	"lunar-tear/server/internal/masterdata"
	"lunar-tear/server/internal/model"
	"lunar-tear/server/internal/store"
)

func TestShopPurchaseLimitAndRefill(t *testing.T) {
	catalog := &masterdata.ShopCatalog{
		Items: map[int32]masterdata.EntityMShopItem{
			1: {ShopItemId: 1, ShopItemLimitedStockId: 10, PriceType: int32(model.PriceTypeGem), Price: 100}, // daily, max 3
			2: {ShopItemId: 2, ShopItemLimitedStockId: 0, PriceType: int32(model.PriceTypeGem), Price: 100}, // unlimited
		},
		LimitedStock: map[int32]int32{
			10: 3,
		},
		LimitedStockReset: map[int32]masterdata.ShopStockReset{
			10: {Type: masterdata.ShopAutoResetDaily, Period: 1},
		},
	}
	user := store.SeedUserState(1, "u", 0, model.DefaultPlatform)

	// Fresh player: 3 of 3 available, then nothing.
	if !shopItemInStock(catalog, user, 1, 3) {
		t.Fatal("full quota refused")
	}
	if shopItemInStock(catalog, user, 1, 4) {
		t.Fatal("over-quota purchase allowed")
	}
	// One at a time up to the cap; the 4th is refused.
	bought := gmt8(t, "2024-07-03 12:00")
	for i := 0; i < 3; i++ {
		if !shopItemInStock(catalog, user, 1, 1) {
			t.Fatalf("purchase %d refused under quota", i+1)
		}
		si := user.ShopItems[1]
		si.ShopItemId, si.BoughtCount = 1, si.BoughtCount+1
		si.LatestBoughtCountChangedDatetime = bought
		user.ShopItems[1] = si
	}
	if shopItemInStock(catalog, user, 1, 1) {
		t.Fatal("fourth purchase allowed past the daily limit")
	}
	// An unlimited item is never refused.
	for i := 0; i < 10; i++ {
		if !shopItemInStock(catalog, user, 2, 1) {
			t.Fatal("unlimited item refused")
		}
	}
	// Unknown items are refused outright.
	if shopItemInStock(catalog, user, 99, 1) {
		t.Fatal("unknown shop item allowed")
	}

	// Same game day: the quota stays spent.
	resetShopStock(catalog, user, gmt8(t, "2024-07-03 23:59"))
	if shopItemInStock(catalog, user, 1, 1) {
		t.Fatal("quota refilled on the same game day")
	}
	// Next GMT-8 day: resetShopStock refills the quota and the item sells again.
	resetShopStock(catalog, user, gmt8(t, "2024-07-04 00:00"))
	if user.ShopItems[1].BoughtCount != 0 {
		t.Fatalf("bought count %d after the daily reset, want 0", user.ShopItems[1].BoughtCount)
	}
	if !shopItemInStock(catalog, user, 1, 3) {
		t.Fatal("quota not refilled after the daily reset")
	}
}
