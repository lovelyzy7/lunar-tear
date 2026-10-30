package masterdata

import (
	"fmt"
	"log"
	"sort"

	"lunar-tear/server/internal/model"
	"lunar-tear/server/internal/store"
	"lunar-tear/server/internal/utils"
)

type CostumeCatalog struct {
	Costumes               map[int32]EntityMCostume
	Materials              map[int32]EntityMMaterial
	ExpByRarity            map[int32][]int32
	EnhanceCostByRarity    map[int32]NumericalFunc
	MaxLevelByRarity       map[int32]NumericalFunc
	LimitBreakCostByRarity map[int32]NumericalFunc

	AwakenByCostumeId           map[int32]EntityMCostumeAwaken
	AwakenPriceByGroup          map[int32]int32
	AwakenEffectsByGroupAndStep map[int32]map[int32]EntityMCostumeAwakenEffectGroup
	AwakenStatusUpByGroup       map[int32][]EntityMCostumeAwakenStatusUpGroup
	AwakenItemAcquireById       map[int32]EntityMCostumeAwakenItemAcquire

	ActiveSkillGroupsByGroupId  map[int32][]EntityMCostumeActiveSkillGroup                  // sorted by CostumeLimitBreakCountLowerLimit desc
	ActiveSkillEnhanceMats      map[[2]int32][]EntityMCostumeActiveSkillEnhancementMaterial // key: [enhancementMaterialId, skillLevel]
	ActiveSkillMaxLevelByRarity map[int32]NumericalFunc
	ActiveSkillCostByRarity     map[int32]NumericalFunc

	LotteryEffects    map[[2]int32]EntityMCostumeLotteryEffect             // key: [costumeId, slotNumber]
	LotteryEffectMats map[int32][]EntityMCostumeLotteryEffectMaterialGroup // key: materialGroupId (both unlock and draw)
	LotteryEffectOdds map[int32][]EntityMCostumeLotteryEffectOddsGroup     // key: oddsGroupId
}

func LoadCostumeCatalog(matCatalog *MaterialCatalog) (*CostumeCatalog, error) {
	costumes, err := utils.ReadTable[EntityMCostume]("m_costume")
	if err != nil {
		return nil, fmt.Errorf("load costume table: %w", err)
	}

	rarities, err := utils.ReadTable[EntityMCostumeRarity]("m_costume_rarity")
	if err != nil {
		return nil, fmt.Errorf("load costume rarity table: %w", err)
	}

	paramMapRows, err := LoadParameterMap()
	if err != nil {
		return nil, err
	}

	funcResolver, err := LoadFunctionResolver()
	if err != nil {
		return nil, fmt.Errorf("load function resolver: %w", err)
	}

	awakenRows, err := utils.ReadTable[EntityMCostumeAwaken]("m_costume_awaken")
	if err != nil {
		return nil, fmt.Errorf("load costume awaken table: %w", err)
	}
	awakenPriceRows, err := utils.ReadTable[EntityMCostumeAwakenPriceGroup]("m_costume_awaken_price_group")
	if err != nil {
		return nil, fmt.Errorf("load costume awaken price table: %w", err)
	}
	awakenEffectRows, err := utils.ReadTable[EntityMCostumeAwakenEffectGroup]("m_costume_awaken_effect_group")
	if err != nil {
		return nil, fmt.Errorf("load costume awaken effect table: %w", err)
	}
	awakenStatusUpRows, err := utils.ReadTable[EntityMCostumeAwakenStatusUpGroup]("m_costume_awaken_status_up_group")
	if err != nil {
		return nil, fmt.Errorf("load costume awaken status up table: %w", err)
	}
	awakenItemAcquireRows, err := utils.ReadTable[EntityMCostumeAwakenItemAcquire]("m_costume_awaken_item_acquire")
	if err != nil {
		return nil, fmt.Errorf("load costume awaken item acquire table: %w", err)
	}

	activeSkillGroupRows, err := utils.ReadTable[EntityMCostumeActiveSkillGroup]("m_costume_active_skill_group")
	if err != nil {
		return nil, fmt.Errorf("load costume active skill group table: %w", err)
	}
	activeSkillMatRows, err := utils.ReadTable[EntityMCostumeActiveSkillEnhancementMaterial]("m_costume_active_skill_enhancement_material")
	if err != nil {
		return nil, fmt.Errorf("load costume active skill enhancement material table: %w", err)
	}

	lotteryEffectRows, err := utils.ReadTable[EntityMCostumeLotteryEffect]("m_costume_lottery_effect")
	if err != nil {
		return nil, fmt.Errorf("load costume lottery effect table: %w", err)
	}
	lotteryEffectMatRows, err := utils.ReadTable[EntityMCostumeLotteryEffectMaterialGroup]("m_costume_lottery_effect_material_group")
	if err != nil {
		return nil, fmt.Errorf("load costume lottery effect material group table: %w", err)
	}
	lotteryEffectOddsRows, err := utils.ReadTable[EntityMCostumeLotteryEffectOddsGroup]("m_costume_lottery_effect_odds_group")
	if err != nil {
		return nil, fmt.Errorf("load costume lottery effect odds group table: %w", err)
	}

	catalog := &CostumeCatalog{
		Costumes:               make(map[int32]EntityMCostume, len(costumes)),
		Materials:              matCatalog.ByType[model.MaterialTypeCostumeEnhancement],
		ExpByRarity:            make(map[int32][]int32, len(rarities)),
		EnhanceCostByRarity:    make(map[int32]NumericalFunc, len(rarities)),
		MaxLevelByRarity:       make(map[int32]NumericalFunc, len(rarities)),
		LimitBreakCostByRarity: make(map[int32]NumericalFunc, len(rarities)),

		AwakenByCostumeId:           make(map[int32]EntityMCostumeAwaken, len(awakenRows)),
		AwakenPriceByGroup:          make(map[int32]int32, len(awakenPriceRows)),
		AwakenEffectsByGroupAndStep: make(map[int32]map[int32]EntityMCostumeAwakenEffectGroup),
		AwakenStatusUpByGroup:       make(map[int32][]EntityMCostumeAwakenStatusUpGroup),
		AwakenItemAcquireById:       make(map[int32]EntityMCostumeAwakenItemAcquire, len(awakenItemAcquireRows)),

		ActiveSkillGroupsByGroupId:  make(map[int32][]EntityMCostumeActiveSkillGroup),
		ActiveSkillEnhanceMats:      make(map[[2]int32][]EntityMCostumeActiveSkillEnhancementMaterial),
		ActiveSkillMaxLevelByRarity: make(map[int32]NumericalFunc, len(rarities)),
		ActiveSkillCostByRarity:     make(map[int32]NumericalFunc, len(rarities)),

		LotteryEffects:    make(map[[2]int32]EntityMCostumeLotteryEffect, len(lotteryEffectRows)),
		LotteryEffectMats: make(map[int32][]EntityMCostumeLotteryEffectMaterialGroup),
		LotteryEffectOdds: make(map[int32][]EntityMCostumeLotteryEffectOddsGroup),
	}

	for _, row := range costumes {
		catalog.Costumes[row.CostumeId] = row
	}

	for _, r := range rarities {
		if _, ok := catalog.ExpByRarity[r.RarityType]; !ok {
			catalog.ExpByRarity[r.RarityType] = BuildExpThresholds(paramMapRows, r.RequiredExpForLevelUpNumericalParameterMapId)
		}
		if _, ok := catalog.EnhanceCostByRarity[r.RarityType]; !ok {
			if f, found := funcResolver.Resolve(r.EnhancementCostByMaterialNumericalFunctionId); found {
				catalog.EnhanceCostByRarity[r.RarityType] = f
			}
		}
		if _, ok := catalog.MaxLevelByRarity[r.RarityType]; !ok {
			if f, found := funcResolver.Resolve(r.MaxLevelNumericalFunctionId); found {
				catalog.MaxLevelByRarity[r.RarityType] = f
			}
		}
		if _, ok := catalog.LimitBreakCostByRarity[r.RarityType]; !ok {
			if f, found := funcResolver.Resolve(r.LimitBreakCostNumericalFunctionId); found {
				catalog.LimitBreakCostByRarity[r.RarityType] = f
			}
		}
		if _, ok := catalog.ActiveSkillMaxLevelByRarity[r.RarityType]; !ok {
			if f, found := funcResolver.Resolve(r.ActiveSkillMaxLevelNumericalFunctionId); found {
				catalog.ActiveSkillMaxLevelByRarity[r.RarityType] = f
			}
		}
		if _, ok := catalog.ActiveSkillCostByRarity[r.RarityType]; !ok {
			if f, found := funcResolver.Resolve(r.ActiveSkillEnhancementCostNumericalFunctionId); found {
				catalog.ActiveSkillCostByRarity[r.RarityType] = f
			}
		}
	}

	for _, row := range awakenRows {
		catalog.AwakenByCostumeId[row.CostumeId] = row
	}
	for _, row := range awakenPriceRows {
		catalog.AwakenPriceByGroup[row.CostumeAwakenPriceGroupId] = row.Gold
	}
	for _, row := range awakenEffectRows {
		m, ok := catalog.AwakenEffectsByGroupAndStep[row.CostumeAwakenEffectGroupId]
		if !ok {
			m = make(map[int32]EntityMCostumeAwakenEffectGroup)
			catalog.AwakenEffectsByGroupAndStep[row.CostumeAwakenEffectGroupId] = m
		}
		m[row.AwakenStep] = row
	}
	for _, row := range awakenStatusUpRows {
		catalog.AwakenStatusUpByGroup[row.CostumeAwakenStatusUpGroupId] = append(
			catalog.AwakenStatusUpByGroup[row.CostumeAwakenStatusUpGroupId], row)
	}
	for _, row := range awakenItemAcquireRows {
		catalog.AwakenItemAcquireById[row.CostumeAwakenItemAcquireId] = row
	}

	for _, row := range activeSkillGroupRows {
		gid := row.CostumeActiveSkillGroupId
		catalog.ActiveSkillGroupsByGroupId[gid] = append(catalog.ActiveSkillGroupsByGroupId[gid], row)
	}
	for gid, rows := range catalog.ActiveSkillGroupsByGroupId {
		sort.Slice(rows, func(i, j int) bool {
			return rows[i].CostumeLimitBreakCountLowerLimit > rows[j].CostumeLimitBreakCountLowerLimit
		})
		catalog.ActiveSkillGroupsByGroupId[gid] = rows
	}

	for _, row := range activeSkillMatRows {
		key := [2]int32{row.CostumeActiveSkillEnhancementMaterialId, row.SkillLevel}
		catalog.ActiveSkillEnhanceMats[key] = append(catalog.ActiveSkillEnhanceMats[key], row)
	}

	for _, row := range lotteryEffectRows {
		key := [2]int32{row.CostumeId, row.SlotNumber}
		catalog.LotteryEffects[key] = row
	}
	for _, row := range lotteryEffectMatRows {
		gid := row.CostumeLotteryEffectMaterialGroupId
		catalog.LotteryEffectMats[gid] = append(catalog.LotteryEffectMats[gid], row)
	}
	for _, row := range lotteryEffectOddsRows {
		gid := row.CostumeLotteryEffectOddsGroupId
		catalog.LotteryEffectOdds[gid] = append(catalog.LotteryEffectOdds[gid], row)
	}

	return catalog, nil
}

// ExpForLevel is the exp a costume has on reaching level (0 if unknown).
func (c *CostumeCatalog) ExpForLevel(costumeId, level int32) int32 {
	cm, ok := c.Costumes[costumeId]
	if !ok {
		return 0
	}
	thresholds := c.ExpByRarity[cm.RarityType]
	if level < 0 || int(level) >= len(thresholds) {
		return 0
	}
	return thresholds[level]
}

// ApplyAwakenStep applies the effect a costume gains at one awakening step:
// a status up or a thought (abilities are resolved by the client).
func (c *CostumeCatalog) ApplyAwakenStep(user *store.UserState, userCostumeUuid string, costumeId, step int32, nowMillis int64) {
	awakenRow, ok := c.AwakenByCostumeId[costumeId]
	if !ok {
		return
	}
	effect, ok := c.AwakenEffectsByGroupAndStep[awakenRow.CostumeAwakenEffectGroupId][step]
	if !ok {
		return
	}
	switch model.CostumeAwakenEffectType(effect.CostumeAwakenEffectType) {
	case model.CostumeAwakenEffectTypeStatusUp:
		c.applyAwakenStatusUp(user, userCostumeUuid, effect.CostumeAwakenEffectId, nowMillis)
	case model.CostumeAwakenEffectTypeAbility:
		log.Printf("[CostumeCatalog] awaken: ability effect id=%d (client-resolved)", effect.CostumeAwakenEffectId)
	case model.CostumeAwakenEffectTypeItemAcquire:
		c.applyAwakenItemAcquire(user, effect.CostumeAwakenEffectId, nowMillis)
	default:
		log.Printf("[CostumeCatalog] awaken: unknown effect type=%d", effect.CostumeAwakenEffectType)
	}
}

func (c *CostumeCatalog) applyAwakenStatusUp(user *store.UserState, costumeUuid string, statusUpGroupId int32, nowMillis int64) {
	rows, ok := c.AwakenStatusUpByGroup[statusUpGroupId]
	if !ok {
		log.Printf("[CostumeCatalog] awaken: status up group %d not found", statusUpGroupId)
		return
	}

	for _, row := range rows {
		calcType := model.StatusCalculationType(row.StatusCalculationType)
		key := store.CostumeAwakenStatusKey{
			UserCostumeUuid:       costumeUuid,
			StatusCalculationType: calcType,
		}
		state := user.CostumeAwakenStatusUps[key]
		state.UserCostumeUuid = costumeUuid
		state.StatusCalculationType = calcType

		switch model.StatusKindType(row.StatusKindType) {
		case model.StatusKindTypeHp:
			state.Hp += row.EffectValue
		case model.StatusKindTypeAttack:
			state.Attack += row.EffectValue
		case model.StatusKindTypeVitality:
			state.Vitality += row.EffectValue
		case model.StatusKindTypeAgility:
			state.Agility += row.EffectValue
		case model.StatusKindTypeCriticalRatio:
			state.CriticalRatio += row.EffectValue
		case model.StatusKindTypeCriticalAttack:
			state.CriticalAttack += row.EffectValue
		}

		state.LatestVersion = nowMillis
		user.CostumeAwakenStatusUps[key] = state
	}
}

func (c *CostumeCatalog) applyAwakenItemAcquire(user *store.UserState, itemAcquireId int32, nowMillis int64) {
	acq, ok := c.AwakenItemAcquireById[itemAcquireId]
	if !ok {
		log.Printf("[CostumeCatalog] awaken: item acquire id=%d not found", itemAcquireId)
		return
	}

	if !store.GrantThought(user, acq.PossessionId, nowMillis) {
		return
	}
	log.Printf("[CostumeCatalog] awaken: granted thought id=%d", acq.PossessionId)
}
