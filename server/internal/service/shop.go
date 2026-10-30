package service

import (
	"context"
	"fmt"
	"log"
	"time"

	pb "lunar-tear/server/gen/proto"
	"lunar-tear/server/internal/gametime"
	"lunar-tear/server/internal/masterdata"
	"lunar-tear/server/internal/model"
	"lunar-tear/server/internal/runtime"
	"lunar-tear/server/internal/store"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

type ShopServiceServer struct {
	pb.UnimplementedShopServiceServer
	users    store.UserRepository
	sessions store.SessionRepository
	holder   *runtime.Holder
}

func NewShopServiceServer(users store.UserRepository, sessions store.SessionRepository, holder *runtime.Holder) *ShopServiceServer {
	return &ShopServiceServer{users: users, sessions: sessions, holder: holder}
}

func (s *ShopServiceServer) Buy(ctx context.Context, req *pb.BuyRequest) (*pb.BuyResponse, error) {
	log.Printf("[ShopService] Buy: shopId=%d items=%v", req.ShopId, req.ShopItems)

	cat := s.holder.Get()
	catalog := cat.Shop
	granter := cat.QuestHandler.Granter
	userId := CurrentUserId(ctx, s.users, s.sessions)
	nowMillis := gametime.NowMillis()

	var overflow []store.GrantOverflow
	_, err := s.users.UpdateUser(userId, func(user *store.UserState) {
		resetShopStock(catalog, user, nowMillis)
		for shopItemId, qty := range req.ShopItems {
			item, ok := catalog.Items[shopItemId]
			if !ok {
				log.Printf("[ShopService] Buy: unknown shopItemId=%d, skipping", shopItemId)
				continue
			}
			if !shopItemInStock(catalog, user, shopItemId, qty) {
				continue
			}

			totalPrice := item.Price * qty
			if err := store.DeductPrice(user, item.PriceType, item.PriceId, totalPrice); err != nil {
				log.Printf("[ShopService] Buy: deduct failed shopItemId=%d: %v", shopItemId, err)
				continue
			}

			giftText := shopOverflowGiftText(item.PriceType)
			for _, content := range catalog.Contents[shopItemId] {
				overflow = append(overflow, granter.GrantFullAs(user,
					model.PossessionType(content.PossessionType),
					content.PossessionId,
					content.Count*qty,
					nowMillis,
					giftText,
				)...)
			}

			applyShopContentEffects(catalog, user, shopItemId, qty, nowMillis)

			si := user.ShopItems[shopItemId]
			si.ShopItemId = shopItemId
			si.BoughtCount += qty
			si.LatestBoughtCountChangedDatetime = nowMillis
			si.LatestVersion = nowMillis
			user.ShopItems[shopItemId] = si
		}
		store.TakeGrantOverflow(user)
	})
	if err != nil {
		return nil, fmt.Errorf("shop buy: %w", err)
	}
	return &pb.BuyResponse{
		OverflowPossession: overflowPossessions(overflow),
	}, nil
}

// shopItemInStock is the purchase-limit check: a limited item may not go past
// its MaxCount within one reset period (resetShopStock refills the quota when
// the day/week/month rolls over). Unlimited items always pass.
func shopItemInStock(catalog *masterdata.ShopCatalog, user *store.UserState, shopItemId, qty int32) bool {
	item, ok := catalog.Items[shopItemId]
	if !ok {
		return false
	}
	if maxCount, limited := catalog.LimitedStock[item.ShopItemLimitedStockId]; limited && item.ShopItemLimitedStockId > 0 &&
		user.ShopItems[shopItemId].BoughtCount+qty > maxCount {
		log.Printf("[ShopService] Buy: shopItemId=%d out of stock (bought %d of %d), skipping", shopItemId, user.ShopItems[shopItemId].BoughtCount, maxCount)
		return false
	}
	return true
}

// shopOverflowGiftText is the MGiftText for shop items over a possession
// limit: 31 for the premium (gem / real money) shop, 32 for exchanges.
func shopOverflowGiftText(priceType int32) int32 {
	switch priceType {
	case model.PriceTypeGem, model.PriceTypePaidGem, model.PriceTypePlatformPayment:
		return store.GiftTextPremiumShopOverflow
	}
	return store.GiftTextExchangeShopOverflow
}

func overflowPossessions(overflow []store.GrantOverflow) []*pb.Possession {
	out := make([]*pb.Possession, 0, len(overflow))
	for _, o := range overflow {
		out = append(out, &pb.Possession{
			PossessionType: int32(o.PossessionType),
			PossessionId:   o.PossessionId,
			Count:          o.Count,
		})
	}
	return out
}

// resetShopStock refills limited stock whose reset period has passed. The
// client shows MaxCount - BoughtCount with no date check, so the server zeroes
// BoughtCount once the GMT-8 day, week (period 7) or month a purchase was
// made in has ended.
func resetShopStock(catalog *masterdata.ShopCatalog, user *store.UserState, nowMillis int64) {
	syncLineupUpdateCount(user, nowMillis)
	for id, si := range user.ShopItems {
		if si.BoughtCount == 0 {
			continue
		}
		reset, ok := catalog.LimitedStockReset[catalog.Items[id].ShopItemLimitedStockId]
		if !ok {
			continue
		}
		start, ok := shopStockPeriodStart(reset, nowMillis)
		// "Changed in the current period" is `start <= ts <= now`, not
		// `ts >= start`: a change time in the game clock's future (a real-time
		// save or RewindLoop leftover) is not in this period and must not
		// block the refill forever.
		if !ok || (si.LatestBoughtCountChangedDatetime >= start && si.LatestBoughtCountChangedDatetime <= nowMillis) {
			continue
		}
		si.BoughtCount = 0
		si.LatestBoughtCountChangedDatetime = nowMillis
		si.LatestVersion = nowMillis
		user.ShopItems[id] = si
	}
}

// shopStockPeriodStart is the start of the reset period containing nowMillis.
func shopStockPeriodStart(reset masterdata.ShopStockReset, nowMillis int64) (int64, bool) {
	period := max(reset.Period, 1)
	switch reset.Type {
	case masterdata.ShopAutoResetDaily:
		switch period {
		case 1:
			return gametime.StartOfDay(nowMillis), true
		case 7:
			return gametime.WeeklyVersion(nowMillis), true
		}
		const dayMillis = 24 * 60 * 60 * 1000
		day := gametime.StartOfDay(nowMillis)
		days := (day - gametime.StartOfDay(0)) / dayMillis
		return day - (days%int64(period))*dayMillis, true
	case masterdata.ShopAutoResetMonthly:
		t := time.UnixMilli(nowMillis).In(gametime.Zone)
		months := (t.Year()*12 + int(t.Month()) - 1) / int(period) * int(period)
		return time.Date(months/12, time.Month(months%12+1), 1, 0, 0, 0, 0, gametime.Zone).UnixMilli(), true
	}
	return 0, false
}

func (s *ShopServiceServer) RefreshUserData(ctx context.Context, req *pb.RefreshRequest) (*pb.RefreshResponse, error) {
	log.Printf("[ShopService] RefreshUserData: isGemUsed=%v", req.IsGemUsed)

	catalog := s.holder.Get().Shop
	userId := CurrentUserId(ctx, s.users, s.sessions)
	nowMillis := gametime.NowMillis()

	var refused error
	_, err := s.users.UpdateUser(userId, func(user *store.UserState) {
		resetShopStock(catalog, user, nowMillis)
		if len(user.ShopReplaceableLineup) == 0 && len(catalog.ItemShopPool) > 0 {
			for i, itemId := range catalog.ItemShopPool {
				slot := int32(i + 1)
				user.ShopReplaceableLineup[slot] = store.UserShopReplaceableLineupState{
					SlotNumber:    slot,
					ShopItemId:    itemId,
					LatestVersion: nowMillis,
				}
			}
		}
		if req.IsGemUsed {
			price := replaceableRefreshGems(user.ShopReplaceable.LineupUpdateCount)
			if refused = store.DeductPrice(user, model.PriceTypeGem, 0, price); refused != nil {
				return
			}
			log.Printf("[ShopService] RefreshUserData: refresh %d for %d gems", user.ShopReplaceable.LineupUpdateCount, price)
			user.ShopReplaceable.LineupUpdateCount++
			user.ShopReplaceable.LatestVersion = nowMillis
			user.ShopReplaceable.LatestLineupUpdateDatetime = nowMillis
			for _, itemId := range catalog.ItemShopPool {
				if si, ok := user.ShopItems[itemId]; ok {
					si.BoughtCount = 0
					si.LatestVersion = nowMillis
					user.ShopItems[itemId] = si
				}
			}
		}
	})
	if err != nil {
		return nil, fmt.Errorf("shop refresh: %w", err)
	}
	if refused != nil {
		log.Printf("[ShopService] RefreshUserData refused: %v", refused)
		return nil, status.Error(codes.FailedPrecondition, refused.Error())
	}

	return &pb.RefreshResponse{}, nil
}

func (s *ShopServiceServer) GetCesaLimit(_ context.Context, _ *emptypb.Empty) (*pb.GetCesaLimitResponse, error) {
	log.Printf("[ShopService] GetCesaLimit")
	return &pb.GetCesaLimitResponse{
		CesaLimit: []*pb.CesaLimit{},
	}, nil
}

func (s *ShopServiceServer) CreatePurchaseTransaction(ctx context.Context, req *pb.CreatePurchaseTransactionRequest) (*pb.CreatePurchaseTransactionResponse, error) {
	log.Printf("[ShopService] CreatePurchaseTransaction: shopId=%d shopItemId=%d productId=%s",
		req.ShopId, req.ShopItemId, req.ProductId)

	cat := s.holder.Get()
	catalog := cat.Shop
	granter := cat.QuestHandler.Granter
	userId := CurrentUserId(ctx, s.users, s.sessions)
	nowMillis := gametime.NowMillis()

	var refused error
	_, err := s.users.UpdateUser(userId, func(user *store.UserState) {
		resetShopStock(catalog, user, nowMillis)
		item, ok := catalog.Items[req.ShopItemId]
		if !ok {
			log.Printf("[ShopService] CreatePurchaseTransaction: unknown shopItemId=%d", req.ShopItemId)
			return
		}
		limited := isLimitedOffer(req.ShopItemId)
		if refused = checkLimitedOffer(catalog, user, item, limited, nowMillis); refused != nil {
			return
		}

		if err := store.DeductPrice(user, item.PriceType, item.PriceId, item.Price); err != nil {
			log.Printf("[ShopService] CreatePurchaseTransaction: deduct failed: %v", err)
		}

		for _, content := range catalog.Contents[req.ShopItemId] {
			granter.GrantFullAs(user,
				model.PossessionType(content.PossessionType),
				content.PossessionId,
				content.Count,
				nowMillis,
				store.GiftTextPremiumShopOverflow,
			)
		}
		store.TakeGrantOverflow(user)

		applyShopContentEffects(catalog, user, req.ShopItemId, 1, nowMillis)

		si := user.ShopItems[req.ShopItemId]
		si.ShopItemId = req.ShopItemId
		si.BoughtCount++
		if item.ShopItemLimitedStockId > 0 && !limited {
			if maxCount, ok := catalog.LimitedStock[item.ShopItemLimitedStockId]; ok && si.BoughtCount >= maxCount {
				si.BoughtCount = 0
			}
		}
		si.LatestBoughtCountChangedDatetime = nowMillis
		si.LatestVersion = nowMillis
		user.ShopItems[req.ShopItemId] = si
	})
	if err != nil {
		return nil, fmt.Errorf("create purchase transaction: %w", err)
	}
	if refused != nil {
		log.Printf("[ShopService] CreatePurchaseTransaction refused: %v", refused)
		return nil, status.Error(codes.FailedPrecondition, refused.Error())
	}

	txId := fmt.Sprintf("tx_%d_%d_%d", userId, req.ShopItemId, nowMillis)

	return &pb.CreatePurchaseTransactionResponse{
		PurchaseTransactionId: txId,
	}, nil
}

func (s *ShopServiceServer) PurchaseGooglePlayStoreProduct(ctx context.Context, req *pb.PurchaseGooglePlayStoreProductRequest) (*pb.PurchaseGooglePlayStoreProductResponse, error) {
	log.Printf("[ShopService] PurchaseGooglePlayStoreProduct: txId=%s", req.PurchaseTransactionId)

	userId := CurrentUserId(ctx, s.users, s.sessions)
	_, err := s.users.LoadUser(userId)
	if err != nil {
		return nil, fmt.Errorf("purchase google play: %w", err)
	}

	return &pb.PurchaseGooglePlayStoreProductResponse{
		OverflowPossession: []*pb.Possession{},
	}, nil
}

func applyShopContentEffects(catalog *masterdata.ShopCatalog, user *store.UserState, shopItemId, qty int32, nowMillis int64) {
	for _, effect := range catalog.Effects[shopItemId] {
		switch effect.EffectTargetType {
		case model.EffectTargetStaminaRecovery:
			maxMillis := catalog.MaxStaminaMillis[user.Status.Level]
			millis := store.ResolveStaminaEffectMillis(effect.EffectValueType, effect.EffectValue, maxMillis)
			store.RecoverStamina(user, millis*qty, maxMillis, nowMillis)
		case model.EffectTargetBattlePointRecovery:
			recoverBattlePoints(user, effect.EffectValueType, effect.EffectValue, qty, nowMillis)
		default:
			log.Printf("[ShopService] unhandled effect: shopItemId=%d targetType=%d", shopItemId, effect.EffectTargetType)
		}
	}
}
