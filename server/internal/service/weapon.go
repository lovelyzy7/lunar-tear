package service

import (
	"context"
	"fmt"
	"log"

	pb "lunar-tear/server/gen/proto"
	"lunar-tear/server/internal/campaign"
	"lunar-tear/server/internal/gametime"
	"lunar-tear/server/internal/gameutil"
	"lunar-tear/server/internal/masterdata"
	"lunar-tear/server/internal/model"
	"lunar-tear/server/internal/runtime"
	"lunar-tear/server/internal/store"
)

type WeaponServiceServer struct {
	pb.UnimplementedWeaponServiceServer
	users    store.UserRepository
	sessions store.SessionRepository
	holder   *runtime.Holder
}

func NewWeaponServiceServer(users store.UserRepository, sessions store.SessionRepository, holder *runtime.Holder) *WeaponServiceServer {
	return &WeaponServiceServer{users: users, sessions: sessions, holder: holder}
}

func (s *WeaponServiceServer) Protect(ctx context.Context, req *pb.ProtectRequest) (*pb.ProtectResponse, error) {
	log.Printf("[WeaponService] Protect: uuids=%v", req.UserWeaponUuid)

	userId := CurrentUserId(ctx, s.users, s.sessions)
	nowMillis := gametime.NowMillis()

	s.users.UpdateUser(userId, func(user *store.UserState) {
		for _, uuid := range req.UserWeaponUuid {
			weapon, ok := user.Weapons[uuid]
			if !ok {
				log.Printf("[WeaponService] Protect: weapon uuid=%s not found", uuid)
				continue
			}
			weapon.IsProtected = true
			weapon.LatestVersion = nowMillis
			user.Weapons[uuid] = weapon
		}
	})

	return &pb.ProtectResponse{}, nil
}

func (s *WeaponServiceServer) Unprotect(ctx context.Context, req *pb.UnprotectRequest) (*pb.UnprotectResponse, error) {
	log.Printf("[WeaponService] Unprotect: uuids=%v", req.UserWeaponUuid)

	userId := CurrentUserId(ctx, s.users, s.sessions)
	nowMillis := gametime.NowMillis()

	s.users.UpdateUser(userId, func(user *store.UserState) {
		for _, uuid := range req.UserWeaponUuid {
			weapon, ok := user.Weapons[uuid]
			if !ok {
				log.Printf("[WeaponService] Unprotect: weapon uuid=%s not found", uuid)
				continue
			}
			weapon.IsProtected = false
			weapon.LatestVersion = nowMillis
			user.Weapons[uuid] = weapon
		}
	})

	return &pb.UnprotectResponse{}, nil
}

func (s *WeaponServiceServer) EnhanceByMaterial(ctx context.Context, req *pb.EnhanceByMaterialRequest) (*pb.EnhanceByMaterialResponse, error) {
	log.Printf("[WeaponService] EnhanceByMaterial: uuid=%s materials=%v", req.UserWeaponUuid, req.Materials)

	cat := s.holder.Get()
	catalog := cat.Weapon
	config := cat.GameConfig
	userId := CurrentUserId(ctx, s.users, s.sessions)
	nowMillis := gametime.NowMillis()

	var great bool
	_, err := s.users.UpdateUser(userId, func(user *store.UserState) {
		weapon, ok := user.Weapons[req.UserWeaponUuid]
		if !ok {
			log.Printf("[WeaponService] EnhanceByMaterial: weapon uuid=%s not found", req.UserWeaponUuid)
			return
		}

		wm, ok := catalog.Weapons[weapon.WeaponId]
		if !ok {
			log.Printf("[WeaponService] EnhanceByMaterial: weapon master id=%d not found", weapon.WeaponId)
			return
		}

		greatRate := cat.Campaign.WeaponGreatSuccessBonus(campaign.WeaponTarget{
			WeaponId:      weapon.WeaponId,
			WeaponType:    wm.WeaponType,
			AttributeType: wm.AttributeType,
		}, campaign.FilterFor(user, nowMillis))

		totalExp := int32(0)
		totalMaterialCount := int32(0)
		for materialId, count := range req.Materials {
			mat, ok := catalog.Materials[materialId]
			if !ok {
				log.Printf("[WeaponService] EnhanceByMaterial: material id=%d not found, skipping", materialId)
				continue
			}

			cur := user.Materials[materialId]
			if cur < count {
				log.Printf("[WeaponService] EnhanceByMaterial: insufficient material id=%d have=%d need=%d", materialId, cur, count)
				continue
			}
			user.Materials[materialId] = cur - count
			totalMaterialCount += count

			expPerUnit := mat.EffectValue
			if mat.WeaponType != 0 && mat.WeaponType == wm.WeaponType {
				expPerUnit = expPerUnit * config.MaterialSameWeaponExpCoefficientPermil / 1000
			}
			totalExp += expPerUnit * count
		}

		if costFunc, ok := catalog.GoldCostByEnhanceId[wm.WeaponSpecificEnhanceId]; ok && totalMaterialCount > 0 {
			goldCost := costFunc.Evaluate(totalMaterialCount)
			user.ConsumableItems[config.ConsumableItemIdForGold] -= goldCost
			log.Printf("[WeaponService] EnhanceByMaterial: gold cost=%d (materials=%d)", goldCost, totalMaterialCount)
		}

		totalExp, great = rollGreatSuccess(totalExp, greatRate.Apply(greatSuccessBasePermil), greatSuccessRand)
		weapon.Exp += totalExp
		levelingEnhanceId := catalog.LevelingEnhanceIdByWeaponId[weapon.WeaponId]
		if thresholds, ok := catalog.ExpByEnhanceId[levelingEnhanceId]; ok {
			var maxLevel int32
			if maxFunc, ok := catalog.MaxLevelByEnhanceId[wm.WeaponSpecificEnhanceId]; ok {
				maxLevel = awakenedLevelCap(catalog, user, weapon, req.UserWeaponUuid, maxFunc.Evaluate(weapon.LimitBreakCount))
			}
			weapon.Level, weapon.Exp = gameutil.ApplyExpWithMaxLevel(weapon.Exp, thresholds, maxLevel)
		}

		note := user.WeaponNotes[weapon.WeaponId]
		if note.MaxLevel < weapon.Level {
			note.WeaponId = weapon.WeaponId
			note.MaxLevel = weapon.Level
			note.LatestVersion = nowMillis
			user.WeaponNotes[weapon.WeaponId] = note
		}

		weapon.LatestVersion = nowMillis
		user.Weapons[req.UserWeaponUuid] = weapon
		log.Printf("[WeaponService] EnhanceByMaterial: weaponId=%d +%d exp -> total=%d level=%d", weapon.WeaponId, totalExp, weapon.Exp, weapon.Level)

		checkWeaponStoryUnlocks(catalog, user, weapon.WeaponId, weapon.Level, nowMillis)
	})
	if err != nil {
		return nil, fmt.Errorf("weapon enhance by material: %w", err)
	}

	return &pb.EnhanceByMaterialResponse{
		IsGreatSuccess:         great,
		SurplusEnhanceMaterial: map[int32]int32{},
	}, nil
}

func (s *WeaponServiceServer) Sell(ctx context.Context, req *pb.SellRequest) (*pb.SellResponse, error) {
	log.Printf("[WeaponService] Sell: uuids=%v", req.UserWeaponUuid)

	cat := s.holder.Get()
	catalog := cat.Weapon
	config := cat.GameConfig
	userId := CurrentUserId(ctx, s.users, s.sessions)

	_, err := s.users.UpdateUser(userId, func(user *store.UserState) {
		totalGold := int32(0)
		for _, uuid := range req.UserWeaponUuid {
			weapon, ok := user.Weapons[uuid]
			if !ok {
				log.Printf("[WeaponService] Sell: weapon uuid=%s not found, skipping", uuid)
				continue
			}

			wm, ok := catalog.Weapons[weapon.WeaponId]
			if !ok {
				log.Printf("[WeaponService] Sell: weapon master id=%d not found, skipping", weapon.WeaponId)
				continue
			}

			if sellFunc, ok := catalog.SellPriceByEnhanceId[wm.WeaponSpecificEnhanceId]; ok {
				totalGold += sellFunc.Evaluate(weapon.Level)
			}

			if medals, ok := catalog.MedalsByWeaponId[weapon.WeaponId]; ok {
				for itemId, count := range medals {
					user.ConsumableItems[itemId] += count
				}
			}

			delete(user.Weapons, uuid)
			delete(user.WeaponSkills, uuid)
			delete(user.WeaponAbilities, uuid)
			delete(user.WeaponAwakens, uuid)
		}

		if totalGold > 0 {
			user.ConsumableItems[config.ConsumableItemIdForGold] += totalGold
			log.Printf("[WeaponService] Sell: granted %d gold", totalGold)
		}
	})
	if err != nil {
		return nil, fmt.Errorf("weapon sell: %w", err)
	}

	return &pb.SellResponse{}, nil
}

func (s *WeaponServiceServer) Evolve(ctx context.Context, req *pb.EvolveRequest) (*pb.EvolveResponse, error) {
	log.Printf("[WeaponService] Evolve: uuid=%s", req.UserWeaponUuid)

	cat := s.holder.Get()
	catalog := cat.Weapon
	config := cat.GameConfig
	userId := CurrentUserId(ctx, s.users, s.sessions)
	nowMillis := gametime.NowMillis()

	_, err := s.users.UpdateUser(userId, func(user *store.UserState) {
		weapon, ok := user.Weapons[req.UserWeaponUuid]
		if !ok {
			log.Printf("[WeaponService] Evolve: weapon uuid=%s not found", req.UserWeaponUuid)
			return
		}

		wm, ok := catalog.Weapons[weapon.WeaponId]
		if !ok {
			log.Printf("[WeaponService] Evolve: weapon master id=%d not found", weapon.WeaponId)
			return
		}

		evolvedId, ok := catalog.EvolutionNextWeaponId[weapon.WeaponId]
		if !ok {
			log.Printf("[WeaponService] Evolve: no evolution for weaponId=%d", weapon.WeaponId)
			return
		}

		totalMaterialCount := int32(0)
		mats := catalog.EvolutionMaterials[wm.WeaponEvolutionMaterialGroupId]
		for _, mat := range mats {
			cur := user.Materials[mat.MaterialId]
			cost := mat.Count
			if cur < cost {
				log.Printf("[WeaponService] Evolve: insufficient material id=%d have=%d need=%d", mat.MaterialId, cur, cost)
				cost = cur
			}
			user.Materials[mat.MaterialId] = cur - cost
			totalMaterialCount += cost
		}

		if costFunc, ok := catalog.EvolutionCostByEnhanceId[wm.WeaponSpecificEnhanceId]; ok && totalMaterialCount > 0 {
			goldCost := costFunc.Evaluate(totalMaterialCount)
			user.ConsumableItems[config.ConsumableItemIdForGold] -= goldCost
			log.Printf("[WeaponService] Evolve: gold cost=%d", goldCost)
		}

		oldWeaponId := wm.WeaponId
		weapon.WeaponId = evolvedId
		weapon.LatestVersion = nowMillis
		user.Weapons[req.UserWeaponUuid] = weapon

		note, hasNote := user.WeaponNotes[evolvedId]
		if !hasNote {
			note = store.WeaponNoteState{
				WeaponId:                 evolvedId,
				MaxLevel:                 weapon.Level,
				MaxLimitBreakCount:       weapon.LimitBreakCount,
				FirstAcquisitionDatetime: nowMillis,
				LatestVersion:            nowMillis,
			}
			user.WeaponNotes[evolvedId] = note
		} else {
			changed := false
			if note.MaxLevel < weapon.Level {
				note.MaxLevel = weapon.Level
				changed = true
			}
			if note.MaxLimitBreakCount < weapon.LimitBreakCount {
				note.MaxLimitBreakCount = weapon.LimitBreakCount
				changed = true
			}
			if changed {
				note.WeaponId = evolvedId
				note.LatestVersion = nowMillis
				user.WeaponNotes[evolvedId] = note
			}
		}

		if oldStory, hasOldStory := user.WeaponStories[oldWeaponId]; hasOldStory {
			newStory, hasNewStory := user.WeaponStories[evolvedId]
			if !hasNewStory || newStory.ReleasedMaxStoryIndex < oldStory.ReleasedMaxStoryIndex {
				if user.WeaponStories == nil {
					user.WeaponStories = make(map[int32]store.WeaponStoryState)
				}
				user.WeaponStories[evolvedId] = store.WeaponStoryState{
					WeaponId:              evolvedId,
					ReleasedMaxStoryIndex: oldStory.ReleasedMaxStoryIndex,
					LatestVersion:         nowMillis,
				}
			}
		}

		evolvedMaster, ok := catalog.Weapons[evolvedId]
		if ok {
			if slots, ok := catalog.AbilitySlots[evolvedMaster.WeaponAbilityGroupId]; ok {
				// Evolving keeps each slot's ability level; a new slot starts at 1.
				levels := map[int32]int32{}
				for _, a := range user.WeaponAbilities[req.UserWeaponUuid] {
					levels[a.SlotNumber] = a.Level
				}
				abilities := make([]store.WeaponAbilityState, len(slots))
				for i, slot := range slots {
					level := levels[slot]
					if level < 1 {
						level = 1
					}
					abilities[i] = store.WeaponAbilityState{
						UserWeaponUuid: req.UserWeaponUuid,
						SlotNumber:     slot,
						Level:          level,
					}
				}
				user.WeaponAbilities[req.UserWeaponUuid] = abilities
			}
		}

		log.Printf("[WeaponService] Evolve: weaponId %d -> %d", oldWeaponId, evolvedId)

		checkWeaponStoryUnlocks(catalog, user, evolvedId, weapon.Level, nowMillis)
	})
	if err != nil {
		return nil, fmt.Errorf("weapon evolve: %w", err)
	}

	return &pb.EvolveResponse{}, nil
}

func (s *WeaponServiceServer) EnhanceSkill(ctx context.Context, req *pb.EnhanceSkillRequest) (*pb.EnhanceSkillResponse, error) {
	log.Printf("[WeaponService] EnhanceSkill: uuid=%s skillId=%d addLevel=%d", req.UserWeaponUuid, req.SkillId, req.AddLevelCount)

	cat := s.holder.Get()
	catalog := cat.Weapon
	config := cat.GameConfig
	userId := CurrentUserId(ctx, s.users, s.sessions)
	nowMillis := gametime.NowMillis()

	_, err := s.users.UpdateUser(userId, func(user *store.UserState) {
		weapon, ok := user.Weapons[req.UserWeaponUuid]
		if !ok {
			log.Printf("[WeaponService] EnhanceSkill: weapon uuid=%s not found", req.UserWeaponUuid)
			return
		}

		wm, ok := catalog.Weapons[weapon.WeaponId]
		if !ok {
			log.Printf("[WeaponService] EnhanceSkill: weapon master id=%d not found", weapon.WeaponId)
			return
		}

		groupRows := catalog.SkillGroupsByGroupId[wm.WeaponSkillGroupId]
		var skillGroup *masterdata.EntityMWeaponSkillGroup
		for i := range groupRows {
			if groupRows[i].SkillId == req.SkillId {
				skillGroup = &groupRows[i]
				break
			}
		}
		if skillGroup == nil {
			log.Printf("[WeaponService] EnhanceSkill: skillId=%d not found in group=%d", req.SkillId, wm.WeaponSkillGroupId)
			return
		}

		skills := user.WeaponSkills[req.UserWeaponUuid]
		skillIdx := -1
		for i, sk := range skills {
			if sk.SlotNumber == skillGroup.SlotNumber {
				skillIdx = i
				break
			}
		}
		if skillIdx < 0 {
			log.Printf("[WeaponService] EnhanceSkill: slot=%d not found for weapon uuid=%s", skillGroup.SlotNumber, req.UserWeaponUuid)
			return
		}

		maxLevelFunc, ok := catalog.SkillMaxLevelByEnhanceId[wm.WeaponSpecificEnhanceId]
		if !ok {
			log.Printf("[WeaponService] EnhanceSkill: no max skill level func for enhanceId=%d", wm.WeaponSpecificEnhanceId)
			return
		}
		maxLevel := maxLevelFunc.Evaluate(weapon.LimitBreakCount)

		currentLevel := skills[skillIdx].Level
		addCount := req.AddLevelCount
		if currentLevel+addCount > maxLevel {
			addCount = maxLevel - currentLevel
		}
		if addCount <= 0 {
			log.Printf("[WeaponService] EnhanceSkill: already at max level %d", currentLevel)
			return
		}

		enhanceMatId := skillGroup.WeaponSkillEnhancementMaterialId
		for lvl := currentLevel; lvl < currentLevel+addCount; lvl++ {
			key := [2]int32{enhanceMatId, lvl}
			mats := catalog.SkillEnhanceMats[key]
			for _, mat := range mats {
				cur := user.Materials[mat.MaterialId]
				cost := mat.Count
				if cur < cost {
					log.Printf("[WeaponService] EnhanceSkill: insufficient material id=%d have=%d need=%d", mat.MaterialId, cur, cost)
					cost = cur
				}
				user.Materials[mat.MaterialId] = cur - cost
			}

			if costFunc, ok := catalog.SkillCostByEnhanceId[wm.WeaponSpecificEnhanceId]; ok {
				goldCost := costFunc.Evaluate(lvl + 1)
				user.ConsumableItems[config.ConsumableItemIdForGold] -= goldCost
			}
		}

		skills[skillIdx].Level = currentLevel + addCount
		user.WeaponSkills[req.UserWeaponUuid] = skills
		log.Printf("[WeaponService] EnhanceSkill: skillId=%d level %d -> %d", req.SkillId, currentLevel, skills[skillIdx].Level)

		weapon.LatestVersion = nowMillis
		user.Weapons[req.UserWeaponUuid] = weapon
	})
	if err != nil {
		return nil, fmt.Errorf("weapon enhance skill: %w", err)
	}

	return &pb.EnhanceSkillResponse{}, nil
}

func (s *WeaponServiceServer) EnhanceAbility(ctx context.Context, req *pb.EnhanceAbilityRequest) (*pb.EnhanceAbilityResponse, error) {
	log.Printf("[WeaponService] EnhanceAbility: uuid=%s abilityId=%d addLevel=%d", req.UserWeaponUuid, req.AbilityId, req.AddLevelCount)

	cat := s.holder.Get()
	catalog := cat.Weapon
	config := cat.GameConfig
	userId := CurrentUserId(ctx, s.users, s.sessions)
	nowMillis := gametime.NowMillis()

	_, err := s.users.UpdateUser(userId, func(user *store.UserState) {
		weapon, ok := user.Weapons[req.UserWeaponUuid]
		if !ok {
			log.Printf("[WeaponService] EnhanceAbility: weapon uuid=%s not found", req.UserWeaponUuid)
			return
		}

		wm, ok := catalog.Weapons[weapon.WeaponId]
		if !ok {
			log.Printf("[WeaponService] EnhanceAbility: weapon master id=%d not found", weapon.WeaponId)
			return
		}

		groupRows := catalog.AbilityGroupsByGroupId[wm.WeaponAbilityGroupId]
		var abilityGroup *masterdata.EntityMWeaponAbilityGroup
		for i := range groupRows {
			if groupRows[i].AbilityId == req.AbilityId {
				abilityGroup = &groupRows[i]
				break
			}
		}
		if abilityGroup == nil {
			log.Printf("[WeaponService] EnhanceAbility: abilityId=%d not found in group=%d", req.AbilityId, wm.WeaponAbilityGroupId)
			return
		}

		abilities := user.WeaponAbilities[req.UserWeaponUuid]
		abilityIdx := -1
		for i, ab := range abilities {
			if ab.SlotNumber == abilityGroup.SlotNumber {
				abilityIdx = i
				break
			}
		}
		if abilityIdx < 0 {
			log.Printf("[WeaponService] EnhanceAbility: slot=%d not found for weapon uuid=%s", abilityGroup.SlotNumber, req.UserWeaponUuid)
			return
		}

		maxLevelFunc, ok := catalog.AbilityMaxLevelByEnhanceId[wm.WeaponSpecificEnhanceId]
		if !ok {
			log.Printf("[WeaponService] EnhanceAbility: no max ability level func for enhanceId=%d", wm.WeaponSpecificEnhanceId)
			return
		}
		maxLevel := maxLevelFunc.Evaluate(weapon.LimitBreakCount)

		currentLevel := abilities[abilityIdx].Level
		addCount := req.AddLevelCount
		if currentLevel+addCount > maxLevel {
			addCount = maxLevel - currentLevel
		}
		if addCount <= 0 {
			log.Printf("[WeaponService] EnhanceAbility: already at max level %d", currentLevel)
			return
		}

		enhanceMatId := abilityGroup.WeaponAbilityEnhancementMaterialId
		for lvl := currentLevel; lvl < currentLevel+addCount; lvl++ {
			key := [2]int32{enhanceMatId, lvl}
			mats := catalog.AbilityEnhanceMats[key]
			for _, mat := range mats {
				cur := user.Materials[mat.MaterialId]
				cost := mat.Count
				if cur < cost {
					log.Printf("[WeaponService] EnhanceAbility: insufficient material id=%d have=%d need=%d", mat.MaterialId, cur, cost)
					cost = cur
				}
				user.Materials[mat.MaterialId] = cur - cost
			}

			if costFunc, ok := catalog.AbilityCostByEnhanceId[wm.WeaponSpecificEnhanceId]; ok {
				goldCost := costFunc.Evaluate(lvl + 1)
				user.ConsumableItems[config.ConsumableItemIdForGold] -= goldCost
			}
		}

		abilities[abilityIdx].Level = currentLevel + addCount
		user.WeaponAbilities[req.UserWeaponUuid] = abilities
		log.Printf("[WeaponService] EnhanceAbility: abilityId=%d level %d -> %d", req.AbilityId, currentLevel, abilities[abilityIdx].Level)

		weapon.LatestVersion = nowMillis
		user.Weapons[req.UserWeaponUuid] = weapon
	})
	if err != nil {
		return nil, fmt.Errorf("weapon enhance ability: %w", err)
	}

	return &pb.EnhanceAbilityResponse{}, nil
}

func (s *WeaponServiceServer) LimitBreakByMaterial(ctx context.Context, req *pb.LimitBreakByMaterialRequest) (*pb.LimitBreakByMaterialResponse, error) {
	log.Printf("[WeaponService] LimitBreakByMaterial: uuid=%s materials=%v", req.UserWeaponUuid, req.Materials)

	cat := s.holder.Get()
	catalog := cat.Weapon
	config := cat.GameConfig
	userId := CurrentUserId(ctx, s.users, s.sessions)
	nowMillis := gametime.NowMillis()

	_, err := s.users.UpdateUser(userId, func(user *store.UserState) {
		weapon, ok := user.Weapons[req.UserWeaponUuid]
		if !ok {
			log.Printf("[WeaponService] LimitBreakByMaterial: weapon uuid=%s not found", req.UserWeaponUuid)
			return
		}

		if weapon.LimitBreakCount >= config.WeaponLimitBreakAvailableCount {
			log.Printf("[WeaponService] LimitBreakByMaterial: already at max limit break %d", weapon.LimitBreakCount)
			return
		}

		wm, ok := catalog.Weapons[weapon.WeaponId]
		if !ok {
			log.Printf("[WeaponService] LimitBreakByMaterial: weapon master id=%d not found", weapon.WeaponId)
			return
		}

		remaining := config.WeaponLimitBreakAvailableCount - weapon.LimitBreakCount

		totalMaterialCount := int32(0)
		for materialId, count := range req.Materials {
			if totalMaterialCount >= remaining {
				break
			}
			if count > remaining-totalMaterialCount {
				count = remaining - totalMaterialCount
			}
			cur := user.Materials[materialId]
			if cur < count {
				log.Printf("[WeaponService] LimitBreakByMaterial: insufficient material id=%d have=%d need=%d", materialId, cur, count)
				count = cur
			}
			user.Materials[materialId] = cur - count
			totalMaterialCount += count
		}

		if costFunc, ok := catalog.LimitBreakCostByMaterialByEnhanceId[wm.WeaponSpecificEnhanceId]; ok && totalMaterialCount > 0 {
			goldCost := costFunc.Evaluate(totalMaterialCount)
			user.ConsumableItems[config.ConsumableItemIdForGold] -= goldCost
			log.Printf("[WeaponService] LimitBreakByMaterial: gold cost=%d", goldCost)
		}

		weapon.LimitBreakCount += totalMaterialCount
		weapon.LatestVersion = nowMillis
		user.Weapons[req.UserWeaponUuid] = weapon

		note := user.WeaponNotes[weapon.WeaponId]
		if note.MaxLimitBreakCount < weapon.LimitBreakCount {
			note.MaxLimitBreakCount = weapon.LimitBreakCount
			note.LatestVersion = nowMillis
			user.WeaponNotes[weapon.WeaponId] = note
		}

		log.Printf("[WeaponService] LimitBreakByMaterial: weaponId=%d limitBreak -> %d", weapon.WeaponId, weapon.LimitBreakCount)
	})
	if err != nil {
		return nil, fmt.Errorf("weapon limit break by material: %w", err)
	}

	return &pb.LimitBreakByMaterialResponse{}, nil
}

func (s *WeaponServiceServer) LimitBreakByWeapon(ctx context.Context, req *pb.LimitBreakByWeaponRequest) (*pb.LimitBreakByWeaponResponse, error) {
	log.Printf("[WeaponService] LimitBreakByWeapon: uuid=%s materialUuids=%v", req.UserWeaponUuid, req.MaterialUserWeaponUuids)

	cat := s.holder.Get()
	catalog := cat.Weapon
	config := cat.GameConfig
	userId := CurrentUserId(ctx, s.users, s.sessions)
	nowMillis := gametime.NowMillis()

	_, err := s.users.UpdateUser(userId, func(user *store.UserState) {
		weapon, ok := user.Weapons[req.UserWeaponUuid]
		if !ok {
			log.Printf("[WeaponService] LimitBreakByWeapon: weapon uuid=%s not found", req.UserWeaponUuid)
			return
		}

		if weapon.LimitBreakCount >= config.WeaponLimitBreakAvailableCount {
			log.Printf("[WeaponService] LimitBreakByWeapon: already at max limit break %d", weapon.LimitBreakCount)
			return
		}

		wm, ok := catalog.Weapons[weapon.WeaponId]
		if !ok {
			log.Printf("[WeaponService] LimitBreakByWeapon: weapon master id=%d not found", weapon.WeaponId)
			return
		}

		remaining := config.WeaponLimitBreakAvailableCount - weapon.LimitBreakCount

		consumedCount := int32(0)
		for _, uuid := range req.MaterialUserWeaponUuids {
			if consumedCount >= remaining {
				break
			}

			matWeapon, ok := user.Weapons[uuid]
			if !ok {
				log.Printf("[WeaponService] LimitBreakByWeapon: material weapon uuid=%s not found, skipping", uuid)
				continue
			}

			if medals, ok := catalog.MedalsByWeaponId[matWeapon.WeaponId]; ok {
				for itemId, count := range medals {
					user.ConsumableItems[itemId] += count
				}
			}

			delete(user.Weapons, uuid)
			delete(user.WeaponSkills, uuid)
			delete(user.WeaponAbilities, uuid)
			delete(user.WeaponAwakens, uuid)
			consumedCount++
		}

		if costFunc, ok := catalog.LimitBreakCostByWeaponByEnhanceId[wm.WeaponSpecificEnhanceId]; ok && consumedCount > 0 {
			goldCost := costFunc.Evaluate(consumedCount)
			user.ConsumableItems[config.ConsumableItemIdForGold] -= goldCost
			log.Printf("[WeaponService] LimitBreakByWeapon: gold cost=%d", goldCost)
		}

		weapon.LimitBreakCount += consumedCount
		weapon.LatestVersion = nowMillis
		user.Weapons[req.UserWeaponUuid] = weapon

		note := user.WeaponNotes[weapon.WeaponId]
		if note.MaxLimitBreakCount < weapon.LimitBreakCount {
			note.MaxLimitBreakCount = weapon.LimitBreakCount
			note.LatestVersion = nowMillis
			user.WeaponNotes[weapon.WeaponId] = note
		}

		log.Printf("[WeaponService] LimitBreakByWeapon: weaponId=%d limitBreak -> %d (consumed %d weapons)", weapon.WeaponId, weapon.LimitBreakCount, consumedCount)
	})
	if err != nil {
		return nil, fmt.Errorf("weapon limit break by weapon: %w", err)
	}

	return &pb.LimitBreakByWeaponResponse{}, nil
}

func (s *WeaponServiceServer) EnhanceByWeapon(ctx context.Context, req *pb.EnhanceByWeaponRequest) (*pb.EnhanceByWeaponResponse, error) {
	log.Printf("[WeaponService] EnhanceByWeapon: uuid=%s materialUuids=%v", req.UserWeaponUuid, req.MaterialUserWeaponUuids)

	cat := s.holder.Get()
	catalog := cat.Weapon
	config := cat.GameConfig
	userId := CurrentUserId(ctx, s.users, s.sessions)
	nowMillis := gametime.NowMillis()

	var great bool
	_, err := s.users.UpdateUser(userId, func(user *store.UserState) {
		weapon, ok := user.Weapons[req.UserWeaponUuid]
		if !ok {
			log.Printf("[WeaponService] EnhanceByWeapon: weapon uuid=%s not found", req.UserWeaponUuid)
			return
		}

		wm, ok := catalog.Weapons[weapon.WeaponId]
		if !ok {
			log.Printf("[WeaponService] EnhanceByWeapon: weapon master id=%d not found", weapon.WeaponId)
			return
		}

		greatRate := cat.Campaign.WeaponGreatSuccessBonus(campaign.WeaponTarget{
			WeaponId:      weapon.WeaponId,
			WeaponType:    wm.WeaponType,
			AttributeType: wm.AttributeType,
		}, campaign.FilterFor(user, nowMillis))

		totalExp := int32(0)
		consumedCount := int32(0)
		for _, uuid := range req.MaterialUserWeaponUuids {
			matWeapon, ok := user.Weapons[uuid]
			if !ok {
				log.Printf("[WeaponService] EnhanceByWeapon: material weapon uuid=%s not found, skipping", uuid)
				continue
			}

			matMaster, ok := catalog.Weapons[matWeapon.WeaponId]
			if !ok {
				log.Printf("[WeaponService] EnhanceByWeapon: material weapon master id=%d not found, skipping", matWeapon.WeaponId)
				continue
			}

			matLevelingEnhanceId := catalog.LevelingEnhanceIdByWeaponId[matWeapon.WeaponId]
			baseExp := catalog.BaseExpByEnhanceId[matLevelingEnhanceId]
			if matMaster.WeaponType != 0 && matMaster.WeaponType == wm.WeaponType {
				baseExp = baseExp * config.MaterialSameWeaponExpCoefficientPermil / 1000
			}
			totalExp += baseExp

			if medals, ok := catalog.MedalsByWeaponId[matWeapon.WeaponId]; ok {
				for itemId, count := range medals {
					user.ConsumableItems[itemId] += count
				}
			}

			delete(user.Weapons, uuid)
			delete(user.WeaponSkills, uuid)
			delete(user.WeaponAbilities, uuid)
			delete(user.WeaponAwakens, uuid)
			consumedCount++
		}

		if costFunc, ok := catalog.EnhanceCostByWeaponByEnhanceId[wm.WeaponSpecificEnhanceId]; ok && consumedCount > 0 {
			goldCost := costFunc.Evaluate(consumedCount)
			user.ConsumableItems[config.ConsumableItemIdForGold] -= goldCost
			log.Printf("[WeaponService] EnhanceByWeapon: gold cost=%d (weapons=%d)", goldCost, consumedCount)
		}

		totalExp, great = rollGreatSuccess(totalExp, greatRate.Apply(greatSuccessBasePermil), greatSuccessRand)
		weapon.Exp += totalExp
		levelingEnhanceId := catalog.LevelingEnhanceIdByWeaponId[weapon.WeaponId]
		if thresholds, ok := catalog.ExpByEnhanceId[levelingEnhanceId]; ok {
			var maxLevel int32
			if maxFunc, ok := catalog.MaxLevelByEnhanceId[wm.WeaponSpecificEnhanceId]; ok {
				maxLevel = awakenedLevelCap(catalog, user, weapon, req.UserWeaponUuid, maxFunc.Evaluate(weapon.LimitBreakCount))
			}
			weapon.Level, weapon.Exp = gameutil.ApplyExpWithMaxLevel(weapon.Exp, thresholds, maxLevel)
		}

		note := user.WeaponNotes[weapon.WeaponId]
		if note.MaxLevel < weapon.Level {
			note.WeaponId = weapon.WeaponId
			note.MaxLevel = weapon.Level
			note.LatestVersion = nowMillis
			user.WeaponNotes[weapon.WeaponId] = note
		}

		weapon.LatestVersion = nowMillis
		user.Weapons[req.UserWeaponUuid] = weapon
		log.Printf("[WeaponService] EnhanceByWeapon: weaponId=%d +%d exp -> total=%d level=%d", weapon.WeaponId, totalExp, weapon.Exp, weapon.Level)

		checkWeaponStoryUnlocks(catalog, user, weapon.WeaponId, weapon.Level, nowMillis)
	})
	if err != nil {
		return nil, fmt.Errorf("weapon enhance by weapon: %w", err)
	}

	return &pb.EnhanceByWeaponResponse{
		IsGreatSuccess:       great,
		SurplusEnhanceWeapon: []string{},
	}, nil
}

func checkWeaponStoryUnlocks(catalog *masterdata.WeaponCatalog, user *store.UserState, weaponId, level int32, nowMillis int64) {
	wm, ok := catalog.Weapons[weaponId]
	if !ok || wm.WeaponStoryReleaseConditionGroupId == 0 {
		return
	}
	evoOrder, hasEvo := catalog.EvolutionOrder[weaponId]
	conditions := catalog.ReleaseConditionsByGroupId[wm.WeaponStoryReleaseConditionGroupId]

	for _, cond := range conditions {
		switch model.WeaponStoryReleaseConditionType(cond.WeaponStoryReleaseConditionType) {
		case model.WeaponStoryReleaseConditionTypeAcquisition:
			store.GrantWeaponStoryUnlock(user, weaponId, cond.StoryIndex, nowMillis)
		case model.WeaponStoryReleaseConditionTypeReachSpecifiedLevel:
			if level >= cond.ConditionValue {
				store.GrantWeaponStoryUnlock(user, weaponId, cond.StoryIndex, nowMillis)
			}
		case model.WeaponStoryReleaseConditionTypeReachInitialMaxLevel:
			if maxFunc, ok := catalog.MaxLevelByEnhanceId[wm.WeaponSpecificEnhanceId]; ok {
				if level >= maxFunc.Evaluate(0) {
					store.GrantWeaponStoryUnlock(user, weaponId, cond.StoryIndex, nowMillis)
				}
			}
		case model.WeaponStoryReleaseConditionTypeReachOnceEvolvedMaxLevel:
			if hasEvo && evoOrder >= 1 {
				if maxFunc, ok := catalog.MaxLevelByEnhanceId[wm.WeaponSpecificEnhanceId]; ok {
					if level >= maxFunc.Evaluate(0) {
						store.GrantWeaponStoryUnlock(user, weaponId, cond.StoryIndex, nowMillis)
					}
				}
			}
		case model.WeaponStoryReleaseConditionTypeReachSpecifiedEvolutionCount:
			if hasEvo && evoOrder >= cond.ConditionValue {
				store.GrantWeaponStoryUnlock(user, weaponId, cond.StoryIndex, nowMillis)
			}
		}
	}
}

func (s *WeaponServiceServer) Awaken(ctx context.Context, req *pb.WeaponAwakenRequest) (*pb.WeaponAwakenResponse, error) {
	log.Printf("[WeaponService] Awaken: uuid=%s", req.UserWeaponUuid)

	cat := s.holder.Get()
	catalog := cat.Weapon
	config := cat.GameConfig
	userId := CurrentUserId(ctx, s.users, s.sessions)
	nowMillis := gametime.NowMillis()

	_, err := s.users.UpdateUser(userId, func(user *store.UserState) {
		weapon, ok := user.Weapons[req.UserWeaponUuid]
		if !ok {
			log.Printf("[WeaponService] Awaken: weapon uuid=%s not found", req.UserWeaponUuid)
			return
		}

		awakenRow, ok := catalog.AwakenByWeaponId[weapon.WeaponId]
		if !ok {
			log.Printf("[WeaponService] Awaken: no awaken data for weaponId=%d", weapon.WeaponId)
			return
		}

		if _, already := user.WeaponAwakens[req.UserWeaponUuid]; already {
			log.Printf("[WeaponService] Awaken: weapon uuid=%s already awakened", req.UserWeaponUuid)
			return
		}

		mats := catalog.AwakenMaterialsByGroupId[awakenRow.WeaponAwakenMaterialGroupId]
		for _, mat := range mats {
			cur := user.Materials[mat.MaterialId]
			cost := mat.Count
			if cur < cost {
				log.Printf("[WeaponService] Awaken: insufficient material id=%d have=%d need=%d", mat.MaterialId, cur, cost)
				cost = cur
			}
			user.Materials[mat.MaterialId] = cur - cost
		}

		if awakenRow.ConsumeGold > 0 {
			user.ConsumableItems[config.ConsumableItemIdForGold] -= awakenRow.ConsumeGold
			log.Printf("[WeaponService] Awaken: gold cost=%d", awakenRow.ConsumeGold)
		}

		user.WeaponAwakens[req.UserWeaponUuid] = store.WeaponAwakenState{
			UserWeaponUuid: req.UserWeaponUuid,
			LatestVersion:  nowMillis,
		}

		weapon.LatestVersion = nowMillis
		user.Weapons[req.UserWeaponUuid] = weapon
		log.Printf("[WeaponService] Awaken: weaponId=%d awakened", weapon.WeaponId)
	})
	if err != nil {
		return nil, fmt.Errorf("weapon awaken: %w", err)
	}

	return &pb.WeaponAwakenResponse{}, nil
}

func awakenedLevelCap(catalog *masterdata.WeaponCatalog, user *store.UserState, weapon store.WeaponState, weaponUuid string, baseCap int32) int32 {
	if _, awoken := user.WeaponAwakens[weaponUuid]; !awoken {
		return baseCap
	}
	row, ok := catalog.AwakenByWeaponId[weapon.WeaponId]
	if !ok {
		return baseCap
	}
	return baseCap + row.LevelLimitUp
}
