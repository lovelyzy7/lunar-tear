package service

import (
	"context"
	"fmt"
	"log"
	"math/rand"

	pb "lunar-tear/server/gen/proto"
	"lunar-tear/server/internal/campaign"
	"lunar-tear/server/internal/gametime"
	"lunar-tear/server/internal/gameutil"
	"lunar-tear/server/internal/masterdata"
	"lunar-tear/server/internal/runtime"
	"lunar-tear/server/internal/store"
)

type CostumeServiceServer struct {
	pb.UnimplementedCostumeServiceServer
	users    store.UserRepository
	sessions store.SessionRepository
	holder   *runtime.Holder
}

func NewCostumeServiceServer(users store.UserRepository, sessions store.SessionRepository, holder *runtime.Holder) *CostumeServiceServer {
	return &CostumeServiceServer{users: users, sessions: sessions, holder: holder}
}

func (s *CostumeServiceServer) Enhance(ctx context.Context, req *pb.EnhanceRequest) (*pb.EnhanceResponse, error) {
	log.Printf("[CostumeService] Enhance: uuid=%s materials=%v", req.UserCostumeUuid, req.Materials)

	cat := s.holder.Get()
	catalog := cat.Costume
	config := cat.GameConfig
	userId := CurrentUserId(ctx, s.users, s.sessions)
	nowMillis := gametime.NowMillis()

	var great bool
	_, err := s.users.UpdateUser(userId, func(user *store.UserState) {
		costume, ok := user.Costumes[req.UserCostumeUuid]
		if !ok {
			log.Printf("[CostumeService] Enhance: costume uuid=%s not found", req.UserCostumeUuid)
			return
		}

		cm, ok := catalog.Costumes[costume.CostumeId]
		if !ok {
			log.Printf("[CostumeService] Enhance: costume master id=%d not found", costume.CostumeId)
			return
		}

		greatRate := cat.Campaign.CostumeGreatSuccessBonus(campaign.CostumeTarget{
			CostumeId:          costume.CostumeId,
			CharacterId:        cm.CharacterId,
			SkillfulWeaponType: cm.SkillfulWeaponType,
		}, campaign.FilterFor(user, nowMillis))

		totalExp := int32(0)
		totalMaterialCount := int32(0)
		for materialId, count := range req.Materials {
			mat, ok := catalog.Materials[materialId]
			if !ok {
				log.Printf("[CostumeService] Enhance: material id=%d not found, skipping", materialId)
				continue
			}

			cur := user.Materials[materialId]
			if cur < count {
				log.Printf("[CostumeService] Enhance: insufficient material id=%d have=%d need=%d", materialId, cur, count)
				continue
			}
			user.Materials[materialId] = cur - count
			totalMaterialCount += count

			expPerUnit := mat.EffectValue
			if mat.WeaponType != 0 && mat.WeaponType == cm.SkillfulWeaponType {
				expPerUnit = expPerUnit * config.MaterialSameWeaponExpCoefficientPermil / 1000
			}
			totalExp += expPerUnit * count
		}

		if costFunc, ok := catalog.EnhanceCostByRarity[cm.RarityType]; ok && totalMaterialCount > 0 {
			goldCost := costFunc.Evaluate(totalMaterialCount)
			user.ConsumableItems[config.ConsumableItemIdForGold] -= goldCost
			log.Printf("[CostumeService] Enhance: gold cost=%d (materials=%d)", goldCost, totalMaterialCount)
		}

		totalExp, great = rollGreatSuccess(totalExp, greatRate.Apply(greatSuccessBasePermil), greatSuccessRand)
		costume.Exp += totalExp

		if thresholds, ok := catalog.ExpByRarity[cm.RarityType]; ok {
			var maxLevel int32
			if maxLevelFunc, hasMax := catalog.MaxLevelByRarity[cm.RarityType]; hasMax {
				maxLevel = maxLevelFunc.Evaluate(costume.LimitBreakCount) +
					cat.CharacterRebirth.CostumeLevelLimitUp(cm.CharacterId, user.CharacterRebirths[cm.CharacterId].RebirthCount)
			}
			costume.Level, costume.Exp = gameutil.ApplyExpWithMaxLevel(costume.Exp, thresholds, maxLevel)
		}

		costume.LatestVersion = nowMillis
		user.Costumes[req.UserCostumeUuid] = costume
		log.Printf("[CostumeService] Enhance: costumeId=%d +%d exp -> total=%d level=%d", costume.CostumeId, totalExp, costume.Exp, costume.Level)
	})
	if err != nil {
		return nil, fmt.Errorf("costume enhance: %w", err)
	}

	return &pb.EnhanceResponse{
		IsGreatSuccess:         great,
		SurplusEnhanceMaterial: map[int32]int32{},
	}, nil
}

func (s *CostumeServiceServer) Awaken(ctx context.Context, req *pb.AwakenRequest) (*pb.AwakenResponse, error) {
	log.Printf("[CostumeService] Awaken: uuid=%s materials=%v", req.UserCostumeUuid, req.Materials)

	cat := s.holder.Get()
	catalog := cat.Costume
	config := cat.GameConfig
	userId := CurrentUserId(ctx, s.users, s.sessions)
	nowMillis := gametime.NowMillis()

	_, err := s.users.UpdateUser(userId, func(user *store.UserState) {
		costume, ok := user.Costumes[req.UserCostumeUuid]
		if !ok {
			log.Printf("[CostumeService] Awaken: costume uuid=%s not found", req.UserCostumeUuid)
			return
		}

		awakenRow, ok := catalog.AwakenByCostumeId[costume.CostumeId]
		if !ok {
			log.Printf("[CostumeService] Awaken: no awaken data for costumeId=%d", costume.CostumeId)
			return
		}

		nextStep := costume.AwakenCount + 1

		if gold, ok := catalog.AwakenPriceByGroup[awakenRow.CostumeAwakenPriceGroupId]; ok {
			user.ConsumableItems[config.ConsumableItemIdForGold] -= gold
			log.Printf("[CostumeService] Awaken: gold cost=%d", gold)
		}

		for materialId, count := range req.Materials {
			cur := user.Materials[materialId]
			if cur < count {
				log.Printf("[CostumeService] Awaken: insufficient material id=%d have=%d need=%d", materialId, cur, count)
				count = cur
			}
			user.Materials[materialId] = cur - count
		}

		costume.AwakenCount = nextStep
		costume.LatestVersion = nowMillis
		user.Costumes[req.UserCostumeUuid] = costume
		log.Printf("[CostumeService] Awaken: costumeId=%d awakenCount=%d", costume.CostumeId, nextStep)

		catalog.ApplyAwakenStep(user, req.UserCostumeUuid, costume.CostumeId, nextStep, nowMillis)
	})
	if err != nil {
		return nil, fmt.Errorf("costume awaken: %w", err)
	}

	return &pb.AwakenResponse{}, nil
}

func (s *CostumeServiceServer) EnhanceActiveSkill(ctx context.Context, req *pb.EnhanceActiveSkillRequest) (*pb.EnhanceActiveSkillResponse, error) {
	log.Printf("[CostumeService] EnhanceActiveSkill: uuid=%s addLevel=%d", req.UserCostumeUuid, req.AddLevelCount)

	cat := s.holder.Get()
	catalog := cat.Costume
	config := cat.GameConfig
	userId := CurrentUserId(ctx, s.users, s.sessions)
	nowMillis := gametime.NowMillis()

	_, err := s.users.UpdateUser(userId, func(user *store.UserState) {
		costume, ok := user.Costumes[req.UserCostumeUuid]
		if !ok {
			log.Printf("[CostumeService] EnhanceActiveSkill: costume uuid=%s not found", req.UserCostumeUuid)
			return
		}

		cm, ok := catalog.Costumes[costume.CostumeId]
		if !ok {
			log.Printf("[CostumeService] EnhanceActiveSkill: costume master id=%d not found", costume.CostumeId)
			return
		}

		groupRows := catalog.ActiveSkillGroupsByGroupId[cm.CostumeActiveSkillGroupId]
		enhanceMatId := int32(-1)
		for _, g := range groupRows {
			if g.CostumeLimitBreakCountLowerLimit <= costume.LimitBreakCount {
				enhanceMatId = g.CostumeActiveSkillEnhancementMaterialId
				break
			}
		}
		if enhanceMatId < 0 {
			log.Printf("[CostumeService] EnhanceActiveSkill: no skill group for costumeId=%d groupId=%d lb=%d",
				costume.CostumeId, cm.CostumeActiveSkillGroupId, costume.LimitBreakCount)
			return
		}

		skill := user.CostumeActiveSkills[req.UserCostumeUuid]
		currentLevel := skill.Level

		maxLevelFunc, ok := catalog.ActiveSkillMaxLevelByRarity[cm.RarityType]
		if !ok {
			log.Printf("[CostumeService] EnhanceActiveSkill: no max level func for rarity=%d", cm.RarityType)
			return
		}
		maxLevel := maxLevelFunc.Evaluate(1)

		addCount := req.AddLevelCount
		if currentLevel+addCount > maxLevel {
			addCount = maxLevel - currentLevel
		}
		if addCount <= 0 {
			log.Printf("[CostumeService] EnhanceActiveSkill: already at max level %d", currentLevel)
			return
		}

		for lvl := currentLevel; lvl < currentLevel+addCount; lvl++ {
			key := [2]int32{enhanceMatId, lvl}
			mats := catalog.ActiveSkillEnhanceMats[key]
			for _, mat := range mats {
				cur := user.Materials[mat.MaterialId]
				cost := mat.Count
				if cur < cost {
					log.Printf("[CostumeService] EnhanceActiveSkill: insufficient material id=%d have=%d need=%d", mat.MaterialId, cur, cost)
					cost = cur
				}
				user.Materials[mat.MaterialId] = cur - cost
			}

			if costFunc, ok := catalog.ActiveSkillCostByRarity[cm.RarityType]; ok {
				goldCost := costFunc.Evaluate(lvl + 1)
				user.ConsumableItems[config.ConsumableItemIdForGold] -= goldCost
			}
		}

		skill.UserCostumeUuid = req.UserCostumeUuid
		skill.Level = currentLevel + addCount
		skill.LatestVersion = nowMillis
		user.CostumeActiveSkills[req.UserCostumeUuid] = skill
		log.Printf("[CostumeService] EnhanceActiveSkill: costumeId=%d level %d -> %d", costume.CostumeId, currentLevel, skill.Level)
	})
	if err != nil {
		return nil, fmt.Errorf("costume enhance active skill: %w", err)
	}

	return &pb.EnhanceActiveSkillResponse{}, nil
}

func (s *CostumeServiceServer) LimitBreak(ctx context.Context, req *pb.LimitBreakRequest) (*pb.LimitBreakResponse, error) {
	log.Printf("[CostumeService] LimitBreak: uuid=%s materials=%v", req.UserCostumeUuid, req.Materials)

	cat := s.holder.Get()
	catalog := cat.Costume
	config := cat.GameConfig
	userId := CurrentUserId(ctx, s.users, s.sessions)
	nowMillis := gametime.NowMillis()

	_, err := s.users.UpdateUser(userId, func(user *store.UserState) {
		costume, ok := user.Costumes[req.UserCostumeUuid]
		if !ok {
			log.Printf("[CostumeService] LimitBreak: costume uuid=%s not found", req.UserCostumeUuid)
			return
		}

		if costume.LimitBreakCount >= config.CostumeLimitBreakAvailableCount {
			log.Printf("[CostumeService] LimitBreak: already at max limit break %d", costume.LimitBreakCount)
			return
		}

		cm, ok := catalog.Costumes[costume.CostumeId]
		if !ok {
			log.Printf("[CostumeService] LimitBreak: costume master id=%d not found", costume.CostumeId)
			return
		}

		totalMaterialCount := int32(0)
		for materialId, count := range req.Materials {
			cur := user.Materials[materialId]
			if cur < count {
				log.Printf("[CostumeService] LimitBreak: insufficient material id=%d have=%d need=%d", materialId, cur, count)
				count = cur
			}
			user.Materials[materialId] = cur - count
			totalMaterialCount += count
		}

		if costFunc, ok := catalog.LimitBreakCostByRarity[cm.RarityType]; ok && totalMaterialCount > 0 {
			goldCost := costFunc.Evaluate(totalMaterialCount)
			user.ConsumableItems[config.ConsumableItemIdForGold] -= goldCost
			log.Printf("[CostumeService] LimitBreak: gold cost=%d", goldCost)
		}

		costume.LimitBreakCount++
		costume.LatestVersion = nowMillis
		user.Costumes[req.UserCostumeUuid] = costume
		log.Printf("[CostumeService] LimitBreak: costumeId=%d limitBreak -> %d", costume.CostumeId, costume.LimitBreakCount)
	})
	if err != nil {
		return nil, fmt.Errorf("costume limit break: %w", err)
	}

	return &pb.LimitBreakResponse{}, nil
}

func (s *CostumeServiceServer) UnlockLotteryEffectSlot(ctx context.Context, req *pb.UnlockLotteryEffectSlotRequest) (*pb.UnlockLotteryEffectSlotResponse, error) {
	log.Printf("[CostumeService] UnlockLotteryEffectSlot: uuid=%s slot=%d", req.UserCostumeUuid, req.SlotNumber)

	cat := s.holder.Get()
	catalog := cat.Costume
	config := cat.GameConfig
	userId := CurrentUserId(ctx, s.users, s.sessions)
	nowMillis := gametime.NowMillis()

	_, err := s.users.UpdateUser(userId, func(user *store.UserState) {
		costume, ok := user.Costumes[req.UserCostumeUuid]
		if !ok {
			log.Printf("[CostumeService] UnlockLotteryEffectSlot: costume uuid=%s not found", req.UserCostumeUuid)
			return
		}

		effectRow, ok := catalog.LotteryEffects[[2]int32{costume.CostumeId, req.SlotNumber}]
		if !ok {
			log.Printf("[CostumeService] UnlockLotteryEffectSlot: no lottery effect for costumeId=%d slot=%d", costume.CostumeId, req.SlotNumber)
			return
		}

		user.ConsumableItems[config.ConsumableItemIdForGold] -= config.CostumeLotteryEffectUnlockSlotConsumeGold

		mats := catalog.LotteryEffectMats[effectRow.CostumeLotteryEffectUnlockMaterialGroupId]
		for _, mat := range mats {
			cur := user.Materials[mat.MaterialId]
			cost := mat.Count
			if cur < cost {
				log.Printf("[CostumeService] UnlockLotteryEffectSlot: insufficient material id=%d have=%d need=%d", mat.MaterialId, cur, cost)
				cost = cur
			}
			user.Materials[mat.MaterialId] = cur - cost
		}

		key := store.CostumeLotteryEffectKey{
			UserCostumeUuid: req.UserCostumeUuid,
			SlotNumber:      req.SlotNumber,
		}
		user.CostumeLotteryEffects[key] = store.CostumeLotteryEffectState{
			UserCostumeUuid: req.UserCostumeUuid,
			SlotNumber:      req.SlotNumber,
			OddsNumber:      0,
			LatestVersion:   nowMillis,
		}

		costume.CostumeLotteryEffectUnlockedSlotCount++
		costume.LatestVersion = nowMillis
		user.Costumes[req.UserCostumeUuid] = costume
		log.Printf("[CostumeService] UnlockLotteryEffectSlot: costumeId=%d slot=%d unlocked slotCount=%d", costume.CostumeId, req.SlotNumber, costume.CostumeLotteryEffectUnlockedSlotCount)
	})
	if err != nil {
		return nil, fmt.Errorf("costume unlock lottery effect slot: %w", err)
	}

	return &pb.UnlockLotteryEffectSlotResponse{}, nil
}

func (s *CostumeServiceServer) DrawLotteryEffect(ctx context.Context, req *pb.DrawLotteryEffectRequest) (*pb.DrawLotteryEffectResponse, error) {
	log.Printf("[CostumeService] DrawLotteryEffect: uuid=%s slot=%d", req.UserCostumeUuid, req.SlotNumber)

	cat := s.holder.Get()
	catalog := cat.Costume
	config := cat.GameConfig
	userId := CurrentUserId(ctx, s.users, s.sessions)
	nowMillis := gametime.NowMillis()

	_, err := s.users.UpdateUser(userId, func(user *store.UserState) {
		costume, ok := user.Costumes[req.UserCostumeUuid]
		if !ok {
			log.Printf("[CostumeService] DrawLotteryEffect: costume uuid=%s not found", req.UserCostumeUuid)
			return
		}

		effectRow, ok := catalog.LotteryEffects[[2]int32{costume.CostumeId, req.SlotNumber}]
		if !ok {
			log.Printf("[CostumeService] DrawLotteryEffect: no lottery effect for costumeId=%d slot=%d", costume.CostumeId, req.SlotNumber)
			return
		}

		oddsPool := catalog.LotteryEffectOdds[effectRow.CostumeLotteryEffectOddsGroupId]
		if len(oddsPool) == 0 {
			log.Printf("[CostumeService] DrawLotteryEffect: empty odds pool for groupId=%d", effectRow.CostumeLotteryEffectOddsGroupId)
			return
		}

		user.ConsumableItems[config.ConsumableItemIdForGold] -= config.CostumeLotteryEffectDrawSlotConsumeGold

		mats := catalog.LotteryEffectMats[effectRow.CostumeLotteryEffectDrawMaterialGroupId]
		for _, mat := range mats {
			cur := user.Materials[mat.MaterialId]
			cost := mat.Count
			if cur < cost {
				log.Printf("[CostumeService] DrawLotteryEffect: insufficient material id=%d have=%d need=%d", mat.MaterialId, cur, cost)
				cost = cur
			}
			user.Materials[mat.MaterialId] = cur - cost
		}

		totalWeight := int32(0)
		for _, row := range oddsPool {
			totalWeight += row.Weight
		}
		roll := rand.Int31n(totalWeight)
		var picked masterdata.EntityMCostumeLotteryEffectOddsGroup
		for _, row := range oddsPool {
			roll -= row.Weight
			if roll < 0 {
				picked = row
				break
			}
		}

		key := store.CostumeLotteryEffectKey{
			UserCostumeUuid: req.UserCostumeUuid,
			SlotNumber:      req.SlotNumber,
		}
		existing := user.CostumeLotteryEffects[key]
		if existing.OddsNumber == 0 {
			existing.UserCostumeUuid = req.UserCostumeUuid
			existing.SlotNumber = req.SlotNumber
			existing.OddsNumber = picked.OddsNumber
			existing.LatestVersion = nowMillis
			user.CostumeLotteryEffects[key] = existing
		} else {
			user.CostumeLotteryEffectPending[req.UserCostumeUuid] = store.CostumeLotteryEffectPendingState{
				UserCostumeUuid: req.UserCostumeUuid,
				SlotNumber:      req.SlotNumber,
				OddsNumber:      picked.OddsNumber,
				LatestVersion:   nowMillis,
			}
		}

		log.Printf("[CostumeService] DrawLotteryEffect: costumeId=%d slot=%d drew oddsNumber=%d type=%d targetId=%d firstDraw=%v",
			costume.CostumeId, req.SlotNumber, picked.OddsNumber, picked.CostumeLotteryEffectType, picked.CostumeLotteryEffectTargetId, existing.OddsNumber == 0)
	})
	if err != nil {
		return nil, fmt.Errorf("costume draw lottery effect: %w", err)
	}

	return &pb.DrawLotteryEffectResponse{}, nil
}

func (s *CostumeServiceServer) ConfirmLotteryEffect(ctx context.Context, req *pb.ConfirmLotteryEffectRequest) (*pb.ConfirmLotteryEffectResponse, error) {
	log.Printf("[CostumeService] ConfirmLotteryEffect: uuid=%s accept=%v", req.UserCostumeUuid, req.IsAccept)

	userId := CurrentUserId(ctx, s.users, s.sessions)
	nowMillis := gametime.NowMillis()

	_, err := s.users.UpdateUser(userId, func(user *store.UserState) {
		pending, ok := user.CostumeLotteryEffectPending[req.UserCostumeUuid]
		if !ok {
			log.Printf("[CostumeService] ConfirmLotteryEffect: no pending for uuid=%s", req.UserCostumeUuid)
			return
		}

		if req.IsAccept {
			key := store.CostumeLotteryEffectKey{
				UserCostumeUuid: pending.UserCostumeUuid,
				SlotNumber:      pending.SlotNumber,
			}
			effect := user.CostumeLotteryEffects[key]
			effect.UserCostumeUuid = pending.UserCostumeUuid
			effect.SlotNumber = pending.SlotNumber
			effect.OddsNumber = pending.OddsNumber
			effect.LatestVersion = nowMillis
			user.CostumeLotteryEffects[key] = effect
			log.Printf("[CostumeService] ConfirmLotteryEffect: accepted oddsNumber=%d for slot=%d", pending.OddsNumber, pending.SlotNumber)
		} else {
			log.Printf("[CostumeService] ConfirmLotteryEffect: rejected oddsNumber=%d for slot=%d", pending.OddsNumber, pending.SlotNumber)
		}

		delete(user.CostumeLotteryEffectPending, req.UserCostumeUuid)
	})
	if err != nil {
		return nil, fmt.Errorf("costume confirm lottery effect: %w", err)
	}

	return &pb.ConfirmLotteryEffectResponse{}, nil
}
