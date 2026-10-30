package service

import (
	"context"
	"fmt"
	"log"
	"time"

	pb "lunar-tear/server/gen/proto"
	"lunar-tear/server/internal/gacha"
	"lunar-tear/server/internal/gametime"
	"lunar-tear/server/internal/masterdata"
	"lunar-tear/server/internal/model"
	"lunar-tear/server/internal/runtime"
	"lunar-tear/server/internal/store"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	emptypb "google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type GachaServiceServer struct {
	pb.UnimplementedGachaServiceServer
	users    store.UserRepository
	sessions store.SessionRepository
	holder   *runtime.Holder
}

func NewGachaServiceServer(
	users store.UserRepository,
	sessions store.SessionRepository,
	holder *runtime.Holder,
) *GachaServiceServer {
	return &GachaServiceServer{
		users:    users,
		sessions: sessions,
		holder:   holder,
	}
}

func (s *GachaServiceServer) GetGachaList(ctx context.Context, req *pb.GetGachaListRequest) (*pb.GetGachaListResponse, error) {
	log.Printf("[GachaService] GetGachaList: labels=%v", req.GachaLabelType)

	cat := s.holder.Get()
	catalog := cat.GachaEntries
	handler := cat.GachaHandler
	userId := CurrentUserId(ctx, s.users, s.sessions)
	nowMillis := gametime.NowMillis()

	user, err := s.users.UpdateUser(userId, func(user *store.UserState) {
		user.EnsureMaps()
		autoConvertExpiredMedals(user, catalog, handler, nowMillis)
	})
	if err != nil {
		return nil, fmt.Errorf("update user: %w", err)
	}

	gachaList := make([]*pb.Gacha, 0, len(catalog))
	for _, entry := range catalog {
		if !gachaActiveAt(entry, nowMillis) {
			continue
		}
		if !matchesGachaLabel(req.GachaLabelType, entry.GachaLabelType) {
			continue
		}
		if entry.GachaLabelType == model.GachaLabelPortalCage || entry.GachaLabelType == model.GachaLabelRecycle {
			continue
		}
		// The game never reads IsUserGachaUnlock: it shows every summon it is
		// sent as open. A locked one (a chapter summon whose quest is not
		// cleared yet) is left out until it unlocks.
		if !gachaUnlockedFor(entry, &user) {
			continue
		}
		bs := user.Gacha.BannerStates[entry.GachaId]
		gachaList = append(gachaList, protoGachas(entry, &bs, user)...)
	}

	return &pb.GetGachaListResponse{
		Gacha:               gachaList,
		// The game shows the conversion only when Summons is opened, not when
		// it returns there from a draw or the exchange, and never says it was
		// seen; so it is sent until the next game start (see GameStart).
		ConvertedGachaMedal: toProtoConvertedGachaMedal(user.Gacha.ConvertedGachaMedal),
	}, nil
}

// autoConvertExpiredMedals turns a summon's leftover medals (its shards)
// into Medals once their AutoConvertDatetime has passed, as the game did:
// the shards leave the inventory and Medals (m_config
// CONSUMABLE_ITEM_ID_FOR_MEDAL) arrive at the medal's ConversionRate. The
// conversions are added to Gacha.ConvertedGachaMedal, which the summon list
// shows until the next game start.
func autoConvertExpiredMedals(user *store.UserState, catalog []store.GachaCatalogEntry, handler *gacha.GachaHandler, nowMillis int64) {
	medalItem := convertedMedalItem()
	done := map[int32]bool{}
	var obtained int32
	for _, entry := range catalog {
		info, ok := handler.MedalInfo[entry.GachaId]
		if !ok || info.ConsumableItemId == 0 || info.AutoConvertDatetime == 0 || nowMillis < info.AutoConvertDatetime {
			continue
		}
		if bs, ok := user.Gacha.BannerStates[entry.GachaId]; ok && bs.MedalCount != 0 {
			bs.MedalCount = 0
			user.Gacha.BannerStates[entry.GachaId] = bs
		}
		if done[info.ConsumableItemId] {
			continue
		}
		done[info.ConsumableItemId] = true
		count := user.ConsumableItems[info.ConsumableItemId]
		if count <= 0 {
			continue
		}
		rate := info.ConversionRate
		if rate <= 0 {
			rate = 1
		}
		delete(user.ConsumableItems, info.ConsumableItemId)
		user.ConsumableItems[medalItem] += count * rate
		obtained += count * rate
		user.Gacha.ConvertedGachaMedal.ConvertedMedalPossession = append(user.Gacha.ConvertedGachaMedal.ConvertedMedalPossession,
			store.ConsumableItemState{ConsumableItemId: info.ConsumableItemId, Count: count})
		log.Printf("[GachaService] converted %d of medal item %d into %d of item %d", count, info.ConsumableItemId, count*rate, medalItem)
	}
	if obtained > 0 {
		if prev := user.Gacha.ConvertedGachaMedal.ObtainPossession; prev != nil {
			obtained += prev.Count
		}
		user.Gacha.ConvertedGachaMedal.ObtainPossession = &store.ConsumableItemState{ConsumableItemId: medalItem, Count: obtained}
	}
}

// convertedMedalItem is the item leftover summon medals become (Medals).
func convertedMedalItem() int32 {
	if cfg, err := masterdata.LoadGameConfig(); err == nil && cfg.ConsumableItemIdForMedal != 0 {
		return cfg.ConsumableItemIdForMedal
	}
	return 2
}

func (s *GachaServiceServer) GetGacha(ctx context.Context, req *pb.GetGachaRequest) (*pb.GetGachaResponse, error) {
	log.Printf("[GachaService] GetGacha: ids=%v", req.GachaId)

	catalog := s.holder.Get().GachaEntries
	nowMillis := gametime.NowMillis()

	userId := CurrentUserId(ctx, s.users, s.sessions)
	user, err := s.users.LoadUser(userId)
	if err != nil {
		return nil, fmt.Errorf("snapshot user: %w", err)
	}

	byId := make(map[int32]*pb.Gacha, len(req.GachaId))
	for _, wantedId := range req.GachaId {
		entry, step := resolveGachaId(catalog, wantedId)
		if entry == nil || !gachaActiveAt(*entry, nowMillis) {
			continue
		}
		bs := user.Gacha.BannerStates[entry.GachaId]
		byId[wantedId] = protoGachaById(*entry, step, &bs, user)
	}

	return &pb.GetGachaResponse{
		Gacha: byId,
	}, nil
}

func (s *GachaServiceServer) Draw(ctx context.Context, req *pb.DrawRequest) (*pb.DrawResponse, error) {
	log.Printf("[GachaService] Draw: gachaId=%d phaseId=%d execCount=%d", req.GachaId, req.GachaPricePhaseId, req.ExecCount)

	cat := s.holder.Get()
	entry := findCatalogEntry(cat.GachaEntries, req.GachaId)
	if entry == nil {
		return nil, fmt.Errorf("gacha %d not found", req.GachaId)
	}
	handler := cat.GachaHandler

	userId := CurrentUserId(ctx, s.users, s.sessions)
	execCount := req.ExecCount
	if execCount <= 0 {
		execCount = 1
	}

	var drawResult *gacha.DrawResult
	var drawErr error
	ownedCostumes := map[int32]bool{}
	ownedWeapons := map[int32]bool{}
	updatedUser, err := s.users.UpdateUser(userId, func(user *store.UserState) {
		for _, c := range user.Costumes {
			ownedCostumes[c.CostumeId] = true
		}
		for _, w := range user.Weapons {
			ownedWeapons[w.WeaponId] = true
		}
		if !gachaUnlockedFor(*entry, user) {
			drawErr = fmt.Errorf("gacha %d is locked", entry.GachaId)
			return
		}
		drawResult, drawErr = handler.HandleDraw(user, *entry, req.GachaPricePhaseId, execCount)
	})
	if err != nil {
		return nil, fmt.Errorf("update user: %w", err)
	}
	if drawErr != nil {
		// HandleDraw fails before it changes the user, so nothing was saved.
		log.Printf("[GachaService] Draw error: %v", drawErr)
		return nil, status.Error(codes.FailedPrecondition, drawErr.Error())
	}

	for i, item := range drawResult.Items {
		if bonus, ok := drawResult.BonusItems[i]; ok {
			log.Printf("[GachaService] drawn[%d]: type=%d id=%d rarity=%d + bonus type=%d id=%d rarity=%d",
				i, item.PossessionType, item.PossessionId, item.RarityType,
				bonus.PossessionType, bonus.PossessionId, bonus.RarityType)
		} else {
			log.Printf("[GachaService] drawn[%d]: type=%d id=%d rarity=%d",
				i, item.PossessionType, item.PossessionId, item.RarityType)
		}
	}

	gachaResults := make([]*pb.DrawGachaOddsItem, 0, len(drawResult.Items))
	dupMap := make(map[int]gacha.DuplicateInfo)
	for _, d := range drawResult.DuplicateInfos {
		dupMap[d.Index] = d
	}
	bonusDupMap := make(map[int]gacha.DuplicateInfo)
	for _, d := range drawResult.BonusDuplicateInfos {
		bonusDupMap[d.Index] = d
	}

	costumePT := int32(model.PossessionTypeCostume)
	weaponPT := int32(model.PossessionTypeWeapon)
	isMaterialDraw := model.IsMaterialBanner(entry.GachaLabelType)

	for i, item := range drawResult.Items {
		isNew := !isOwnedByType(item, ownedCostumes, ownedWeapons, updatedUser)

		var oddsItem *pb.DrawGachaOddsItem

		if isMaterialDraw {
			oddsItem = &pb.DrawGachaOddsItem{
				GachaItem: &pb.GachaItem{
					PossessionType: item.PossessionType,
					PossessionId:   item.PossessionId,
					Count:          1,
					IsNew:          isNew,
				},
				GachaItemBonus: &pb.GachaItem{},
			}
		} else if bonus, hasBonusWeapon := drawResult.BonusItems[i]; hasBonusWeapon {
			oddsItem = &pb.DrawGachaOddsItem{
				GachaItem: &pb.GachaItem{
					PossessionType: costumePT,
					PossessionId:   item.PossessionId,
					Count:          1,
					IsNew:          isNew,
				},
				GachaItemBonus: &pb.GachaItem{
					PossessionType: weaponPT,
					PossessionId:   bonus.PossessionId,
					Count:          1,
					IsNew:          !ownedWeapons[bonus.PossessionId],
				},
			}
		} else {
			oddsItem = &pb.DrawGachaOddsItem{
				GachaItem: &pb.GachaItem{
					PossessionType: item.PossessionType,
					PossessionId:   item.PossessionId,
					Count:          1,
					IsNew:          isNew,
				},
				GachaItemBonus: &pb.GachaItem{},
			}
		}

		// The game reads medalBonus on every result; summons without a medal
		// send an empty one.
		oddsItem.MedalBonus = &pb.GachaBonus{}
		if drawResult.MedalBonus > 0 && entry.MedalConsumableItemId != 0 {
			oddsItem.MedalBonus = &pb.GachaBonus{
				PossessionType: int32(model.PossessionTypeConsumableItem),
				PossessionId:   entry.MedalConsumableItemId,
				Count:          0,
			}
		}

		if dup, ok := dupMap[i]; ok {
			applyDuplicationBonus(oddsItem, dup)
		}
		if bdup, ok := bonusDupMap[i]; ok {
			applyDuplicationBonus(oddsItem, bdup)
		}

		gachaResults = append(gachaResults, oddsItem)
	}

	var bonuses []*pb.GachaBonus
	for _, b := range drawResult.Bonuses {
		bonuses = append(bonuses, &pb.GachaBonus{
			PossessionType: b.PossessionType,
			PossessionId:   b.PossessionId,
			Count:          b.Count,
		})
	}

	bs := updatedUser.Gacha.BannerStates[entry.GachaId]
	nextGacha := protoGachaById(*entry, stepUpCurrent(*entry, &bs), &bs, updatedUser)

	return &pb.DrawResponse{
		NextGacha:          nextGacha,
		GachaResult:        gachaResults,
		GachaBonus:         bonuses,
		MenuGachaBadgeInfo: freeSummonBadges(s.holder.Get().GachaEntries, &updatedUser, gametime.NowMillis()),
	}, nil
}

func (s *GachaServiceServer) ResetBoxGacha(ctx context.Context, req *pb.ResetBoxGachaRequest) (*pb.ResetBoxGachaResponse, error) {
	log.Printf("[GachaService] ResetBoxGacha: gachaId=%d", req.GachaId)

	cat := s.holder.Get()
	entry := findCatalogEntry(cat.GachaEntries, req.GachaId)
	if entry == nil {
		return nil, fmt.Errorf("gacha %d not found", req.GachaId)
	}
	handler := cat.GachaHandler

	userId := CurrentUserId(ctx, s.users, s.sessions)
	updatedUser, err := s.users.UpdateUser(userId, func(user *store.UserState) {
		if resetErr := handler.HandleResetBox(user, *entry); resetErr != nil {
			log.Printf("[GachaService] ResetBoxGacha error: %v", resetErr)
		}
	})
	if err != nil {
		return nil, fmt.Errorf("update user: %w", err)
	}

	bs := updatedUser.Gacha.BannerStates[entry.GachaId]

	return &pb.ResetBoxGachaResponse{
		Gacha: toProtoGachaFor(*entry, &bs, updatedUser),
	}, nil
}

func (s *GachaServiceServer) GetRewardGacha(ctx context.Context, req *emptypb.Empty) (*pb.GetRewardGachaResponse, error) {
	log.Printf("[GachaService] GetRewardGacha")
	userId := CurrentUserId(ctx, s.users, s.sessions)
	user, err := s.users.LoadUser(userId)
	if err != nil {
		return nil, fmt.Errorf("snapshot user: %w", err)
	}

	maxCount := s.holder.Get().GachaHandler.Config.RewardGachaDailyMaxCount
	if maxCount <= 0 {
		maxCount = model.DefaultDailyDrawLimit
	}

	todayStart := gametime.StartOfDayMillis()
	drawCount := user.Gacha.TodaysCurrentDrawCount
	if user.Gacha.LastRewardDrawDate < todayStart {
		drawCount = 0
	}

	return &pb.GetRewardGachaResponse{
		Available:              drawCount < maxCount,
		TodaysCurrentDrawCount: drawCount,
		DailyMaxCount:          maxCount,
	}, nil
}

func (s *GachaServiceServer) RewardDraw(ctx context.Context, req *pb.RewardDrawRequest) (*pb.RewardDrawResponse, error) {
	log.Printf("[GachaService] RewardDraw: placement=%q reward=%q amount=%q", req.PlacementName, req.RewardName, req.RewardAmount)

	userId := CurrentUserId(ctx, s.users, s.sessions)
	handler := s.holder.Get().GachaHandler

	// The Special Gift Roulette: the wheel stops on the amount returned.
	var gems []int32
	if _, err := s.users.UpdateUser(userId, func(user *store.UserState) {
		var drawErr error
		gems, drawErr = handler.HandleRewardDraw(user, 1)
		if drawErr != nil {
			log.Printf("[GachaService] RewardDraw error: %v", drawErr)
		}
	}); err != nil {
		return nil, fmt.Errorf("update user: %w", err)
	}

	results := make([]*pb.RewardGachaItem, 0, len(gems))
	for _, won := range gems {
		results = append(results, &pb.RewardGachaItem{
			PossessionType: int32(model.PossessionTypeFreeGem),
			Count:          won,
		})
	}

	return &pb.RewardDrawResponse{
		RewardGachaResult: results,
	}, nil
}

func findCatalogEntry(catalog []store.GachaCatalogEntry, gachaId int32) *store.GachaCatalogEntry {
	entry, _ := resolveGachaId(catalog, gachaId)
	return entry
}

func matchesGachaLabel(labels []int32, label int32) bool {
	if len(labels) == 0 {
		return true
	}
	for _, candidate := range labels {
		if candidate == label {
			return true
		}
	}
	return false
}

func gachaActiveAt(entry store.GachaCatalogEntry, nowMillis int64) bool {
	if entry.StartDatetime != 0 && nowMillis < entry.StartDatetime {
		return false
	}
	if entry.EndDatetime != 0 && nowMillis >= entry.EndDatetime {
		return false
	}
	return true
}

func gachaUnlockCondition(entry store.GachaCatalogEntry) *pb.GachaUnlockCondition {
	if entry.UnlockConditionType == 0 {
		return &pb.GachaUnlockCondition{GachaUnlockConditionType: model.GachaUnlockNone}
	}
	return &pb.GachaUnlockCondition{GachaUnlockConditionType: entry.UnlockConditionType, ConditionValue: entry.UnlockConditionValue}
}

// gachaUnlockedFor evaluates the banner's unlock condition for this user.
// Only MAIN_QUEST_CLEAR is used by Bunker's catalog (chapter summons).
func gachaUnlockedFor(entry store.GachaCatalogEntry, user *store.UserState) bool {
	if !entry.IsUserGachaUnlock {
		return false
	}
	if entry.UnlockConditionType == model.GachaUnlockMainQuestClear {
		// Replaying a cleared quest sets its state back to Active until it ends; ClearCount stays.
		q, ok := user.Quests[entry.UnlockConditionValue]
		return ok && (q.ClearCount > 0 || q.QuestStateType == model.UserQuestStateTypeCleared)
	}
	return true
}

func toProtoGachaFor(entry store.GachaCatalogEntry, bs *store.GachaBannerState, user store.UserState) *pb.Gacha {
	g := toProtoGacha(entry, bs)
	g.IsUserGachaUnlock = gachaUnlockedFor(entry, &user)
	g.GachaPricePhase = buildProtoPricePhases(entry, bs, &user)
	if mode, ok := g.GachaMode.(*pb.Gacha_GachaModeBoxComposition); ok && len(entry.BoxContents) > 0 {
		// The box runs out: Reset once its targets are drawn; no 10 draws when fewer are left.
		box := gacha.Box(&user, entry, gametime.NowMillis())
		mode.GachaModeBoxComposition.IsAlreadyDrawn = box.Drawn > 0
		mode.GachaModeBoxComposition.IsResettableByDrawingAllTargets = true
		mode.GachaModeBoxComposition.IsCurrentBoxResettable = box.Left == 0 || box.TargetsDrawn
		mode.GachaModeBoxComposition.IsHideMultipleDrawButton = box.Left < model.PremiumMultiPullCount
		mode.GachaModeBoxComposition.PromotionGachaOddsItem = boxPromotionItems(entry, &user)
	}
	return g
}

func toProtoGacha(entry store.GachaCatalogEntry, bs *store.GachaBannerState) *pb.Gacha {
	g := &pb.Gacha{
		GachaId:                    entry.GachaId,
		GachaLabelType:             entry.GachaLabelType,
		GachaModeType:              entry.GachaModeType,
		GachaAutoResetType:         entry.GachaAutoResetType,
		GachaAutoResetPeriod:       entry.GachaAutoResetPeriod,
		NextAutoResetDatetime:      safeTimestamp(entry.NextAutoResetDatetime),
		GachaUnlockCondition:       []*pb.GachaUnlockCondition{gachaUnlockCondition(entry)},
		IsUserGachaUnlock:          entry.IsUserGachaUnlock,
		StartDatetime:              safeTimestamp(entry.StartDatetime),
		EndDatetime:                safeTimestamp(entry.EndDatetime),
		RelatedMainQuestChapterId:  entry.RelatedMainQuestChapterId,
		RelatedEventQuestChapterId: entry.RelatedEventQuestChapterId,
		PromotionMovieAssetId:      entry.PromotionMovieAssetId,
		GachaMedalId:               entry.GachaMedalId,
		GachaDecorationType:        entry.GachaDecorationType,
		SortOrder:                  entry.SortOrder,
		IsInactive:                 entry.IsInactive,
		InformationId:              entry.InformationId,
	}

	g.GachaPricePhase = buildProtoPricePhases(entry, bs, nil)

	promotionItems := buildProtoPromotionItems(entry)

	switch entry.GachaModeType {
	case model.GachaModeBox:
		boxNumber := int32(1)
		if bs != nil && bs.BoxNumber > 0 {
			boxNumber = bs.BoxNumber
		}
		phaseId := int32(0)
		if len(entry.PricePhases) > 0 {
			phaseId = entry.PricePhases[0].PhaseId
		}
		g.GachaMode = &pb.Gacha_GachaModeBoxComposition{
			GachaModeBoxComposition: &pb.GachaModeBoxComposition{
				GachaBoxGroupId:               entry.GroupId,
				BoxNumber:                     boxNumber,
				CurrentBoxNumber:              boxNumber,
				NaviCharacterCommentAssetName: "production",
				GachaAssetName:                entry.BannerAssetName,
				GachaPricePhaseId:             phaseId,
				PromotionGachaOddsItem:        promotionItems,
				GachaDescriptionTextId:        entry.DescriptionTextId,
			},
		}
	case model.GachaModeStepup:
		stepNumber := stepUpCurrent(entry, bs)
		loopCount := int32(0)
		if bs != nil {
			loopCount = bs.LoopCount
		}
		g.GachaMode = &pb.Gacha_GachaModeStepupComposition{
			GachaModeStepupComposition: &pb.GachaModeStepupComposition{
				GachaStepGroupId:              entry.GroupId,
				StepNumber:                    1,
				CurrentStepNumber:             stepNumber,
				NaviCharacterCommentAssetName: "production",
				GachaAssetName:                entry.BannerAssetName,
				PromotionGachaOddsItem:        promotionItems,
				CurrentLoopCount:              loopCount,
			},
		}
	default:
		g.GachaMode = &pb.Gacha_GachaModeBasic{
			GachaModeBasic: &pb.GachaModeBasic{
				NaviCharacterCommentAssetName: "production",
				GachaAssetName:                entry.BannerAssetName,
				PromotionGachaOddsItem:        promotionItems,
			},
		}
	}

	return g
}

func buildProtoPricePhases(entry store.GachaCatalogEntry, bs *store.GachaBannerState, user *store.UserState) []*pb.GachaPricePhase {
	phases := make([]*pb.GachaPricePhase, 0, len(entry.PricePhases))

	for _, p := range entry.PricePhases {
		isEnabled := true
		if entry.GachaModeType == model.GachaModeStepup && bs != nil {
			isEnabled = p.StepNumber == stepUpCurrent(entry, bs)
		}

		var bonuses []*pb.GachaBonus
		for _, b := range p.Bonuses {
			bonuses = append(bonuses, &pb.GachaBonus{
				PossessionType: b.PossessionType,
				PossessionId:   b.PossessionId,
				Count:          b.Count,
			})
		}

		limitExec := p.LimitExecCount
		if limitExec <= 0 {
			limitExec = 999
		}
		var userExec int32
		if user != nil && p.LimitExecCount > 0 && entry.GachaModeType != model.GachaModeStepup {
			userExec = gacha.ExecCount(user, entry, p.PhaseId)
			// A used-up option is switched off: the game restarts if a draw is refused.
			if userExec >= p.LimitExecCount {
				isEnabled = false
			}
		}

		phases = append(phases, &pb.GachaPricePhase{
			GachaPricePhaseId: p.PhaseId,
			IsEnabled:         isEnabled,
			EndDatetime:       safeTimestamp(entry.EndDatetime),
			PriceType:         p.PriceType,
			PriceId:           p.PriceId,
			Price:             p.Price,
			RegularPrice:      p.RegularPrice,
			DrawCount:         p.DrawCount,
			LimitExecCount:    limitExec,
			UserExecCount:     userExec,
			EachMaxExecCount:  p.DrawCount,
			GachaBonus:        bonuses,
			GachaOddsFixedRarity: &pb.GachaOddsFixedRarity{
				FixedRarityTypeLowerLimit: p.FixedRarityMin,
				FixedCount:                p.FixedCount,
			},
			GachaBadgeType: model.GachaBadgeTypeNone,
		})
	}

	return phases
}

// boxPromotionItems are the "Featured Items" of a box summon: its rarest
// items (the targets first), each with how many the box holds and how many
// the player has drawn from this box.
func boxPromotionItems(entry store.GachaCatalogEntry, user *store.UserState) []*pb.GachaOddsItem {
	const shown = 4
	box := gacha.Box(user, entry, gametime.NowMillis())
	drew := user.Gacha.BannerStates[entry.GachaId].BoxDrewCounts
	out := make([]*pb.GachaOddsItem, 0, shown)
	for i, c := range entry.BoxContents { // rarest first
		if i == shown {
			break
		}
		n := drew[c.PossessionId]
		if box.Drawn == 0 {
			n = 0 // refilled this month
		}
		out = append(out, &pb.GachaOddsItem{
			GachaItem:        &pb.GachaItem{PossessionType: c.PossessionType, PossessionId: c.PossessionId, Count: 1, PromotionOrder: int32(i + 1)},
			GachaItemBonus:   &pb.GachaItem{},
			MaxDrawableCount: c.Count,
			DrewCount:        n,
			IsTarget:         c.IsTarget,
		})
	}
	return out
}

func buildProtoPromotionItems(entry store.GachaCatalogEntry) []*pb.GachaOddsItem {
	if len(entry.PromotionItems) == 0 {
		return nil
	}
	isMaterial := model.IsMaterialBanner(entry.GachaLabelType)

	items := make([]*pb.GachaOddsItem, 0, len(entry.PromotionItems))
	for i, pi := range entry.PromotionItems {
		bonus := &pb.GachaItem{}
		if !isMaterial && pi.BonusPossessionType != 0 {
			bonus = &pb.GachaItem{
				PossessionType: pi.BonusPossessionType,
				PossessionId:   pi.BonusPossessionId,
				Count:          1,
			}
		}
		items = append(items, &pb.GachaOddsItem{
			GachaItem: &pb.GachaItem{
				PossessionType: pi.PossessionType,
				PossessionId:   pi.PossessionId,
				Count:          1,
				PromotionOrder: int32(i + 1),
			},
			GachaItemBonus:   bonus,
			MaxDrawableCount: 999,
			IsTarget:         pi.IsTarget,
		})
	}
	return items
}

func toProtoConvertedGachaMedal(state store.ConvertedGachaMedalState) *pb.ConvertedGachaMedal {
	items := make([]*pb.ConsumableItemPossession, 0, len(state.ConvertedMedalPossession))
	for _, item := range state.ConvertedMedalPossession {
		items = append(items, &pb.ConsumableItemPossession{
			ConsumableItemId: item.ConsumableItemId,
			Count:            item.Count,
		})
	}

	obtain := &pb.ConsumableItemPossession{
		ConsumableItemId: 0,
		Count:            0,
	}
	if state.ObtainPossession != nil {
		obtain.ConsumableItemId = state.ObtainPossession.ConsumableItemId
		obtain.Count = state.ObtainPossession.Count
	}

	return &pb.ConvertedGachaMedal{
		ConvertedMedalPossession: items,
		ObtainPossession:         obtain,
	}
}

func safeTimestamp(unixMillis int64) *timestamppb.Timestamp {
	if unixMillis == 0 {
		return &timestamppb.Timestamp{Seconds: 0}
	}
	return timestamppb.New(time.UnixMilli(unixMillis))
}

func applyDuplicationBonus(oddsItem *pb.DrawGachaOddsItem, dup gacha.DuplicateInfo) {
	if oddsItem.DuplicationBonusGrade == 0 {
		oddsItem.DuplicationBonusGrade = dup.Grade
	}
	for _, b := range dup.Bonuses {
		oddsItem.DuplicationBonus = append(oddsItem.DuplicationBonus, &pb.GachaBonus{
			PossessionType: b.PossessionType,
			PossessionId:   b.PossessionId,
			Count:          b.Count,
		})
	}
}

func isOwnedByType(item gacha.DrawnItem, costumes, weapons map[int32]bool, user store.UserState) bool {
	switch item.PossessionType {
	case int32(model.PossessionTypeCostume):
		return costumes[item.PossessionId]
	case int32(model.PossessionTypeWeapon):
		return weapons[item.PossessionId]
	case int32(model.PossessionTypeMaterial):
		return user.Materials[item.PossessionId] > 0
	case int32(model.PossessionTypeWeaponEnhanced):
		return user.ConsumableItems[item.PossessionId] > 0
	}
	return false
}
