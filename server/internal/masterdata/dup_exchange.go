package masterdata

import (
	"lunar-tear/server/internal/model"
	"lunar-tear/server/internal/store"
	"lunar-tear/server/internal/utils"
)

func LoadDupExchange() (map[int32][]model.DupExchangeEntry, error) {
	result := make(map[int32][]model.DupExchangeEntry)

	costumeRows, err := utils.ReadTable[EntityMCostumeDuplicationExchangePossessionGroup]("m_costume_duplication_exchange_possession_group")
	if err != nil {
		return nil, err
	}
	for _, r := range costumeRows {
		result[r.CostumeId] = append(result[r.CostumeId], model.DupExchangeEntry{
			PossessionType: r.PossessionType,
			PossessionId:   r.PossessionId,
			Count:          r.Count,
		})
	}

	return result, nil
}

const dupExchangeFallbackCount int32 = 10

func EnrichDupExchange(dupMap map[int32][]model.DupExchangeEntry, pool *GachaCatalog) (int, error) {
	lbRows, err := utils.ReadTable[EntityMCostumeLimitBreakMaterialGroup]("m_costume_limit_break_material_group")
	if err != nil {
		return 0, err
	}
	groupToMaterial := make(map[int32]int32, len(lbRows))
	for _, r := range lbRows {
		groupToMaterial[r.CostumeLimitBreakMaterialGroupId] = r.MaterialId
	}

	costumeRows, err := utils.ReadTable[EntityMCostume]("m_costume")
	if err != nil {
		return 0, err
	}
	costumeLBGroup := make(map[int32]int32, len(costumeRows))
	for _, r := range costumeRows {
		costumeLBGroup[r.CostumeId] = r.CostumeLimitBreakMaterialGroupId
	}

	added := 0
	for costumeId := range pool.CostumeById {
		if _, exists := dupMap[costumeId]; exists {
			continue
		}
		matId := groupToMaterial[costumeLBGroup[costumeId]]
		if matId == 0 {
			continue
		}
		dupMap[costumeId] = []model.DupExchangeEntry{{
			PossessionType: int32(model.PossessionTypeMaterial),
			PossessionId:   matId,
			Count:          dupExchangeFallbackCount,
		}}
		added++
	}
	return added, nil
}

// GranterTables are the master data rows the general grant path needs beyond
// the quest catalog: duplicate conversion and enhanced (pre-levelled) grants.
type GranterTables struct {
	CostumeDuplicateExchange   map[int32][]store.DuplicateExchange
	CompanionDuplicateExchange map[int32][]store.DuplicateExchange
	CostumeEnhanced            map[int32]store.CostumeEnhancedRef
	CompanionEnhanced          map[int32]store.CompanionEnhancedRef
}

// LoadGranterTables reads the duplicate exchange and enhanced tables as they
// are; unlike the summon map above nothing is derived for costumes without a
// duplicate exchange row.
func LoadGranterTables() (*GranterTables, error) {
	costumeRows, err := utils.ReadTable[EntityMCostumeDuplicationExchangePossessionGroup]("m_costume_duplication_exchange_possession_group")
	if err != nil {
		return nil, err
	}
	companionRows, err := utils.ReadTable[EntityMCompanionDuplicationExchangePossessionGroup]("m_companion_duplication_exchange_possession_group")
	if err != nil {
		return nil, err
	}
	costumeEnhanced, err := utils.ReadTable[EntityMCostumeEnhanced]("m_costume_enhanced")
	if err != nil {
		return nil, err
	}
	companionEnhanced, err := utils.ReadTable[EntityMCompanionEnhanced]("m_companion_enhanced")
	if err != nil {
		return nil, err
	}

	t := &GranterTables{
		CostumeDuplicateExchange:   make(map[int32][]store.DuplicateExchange),
		CompanionDuplicateExchange: make(map[int32][]store.DuplicateExchange),
		CostumeEnhanced:            make(map[int32]store.CostumeEnhancedRef, len(costumeEnhanced)),
		CompanionEnhanced:          make(map[int32]store.CompanionEnhancedRef, len(companionEnhanced)),
	}
	for _, r := range costumeRows {
		t.CostumeDuplicateExchange[r.CostumeId] = append(t.CostumeDuplicateExchange[r.CostumeId], store.DuplicateExchange{
			PossessionType: model.PossessionType(r.PossessionType), PossessionId: r.PossessionId, Count: r.Count,
		})
	}
	for _, r := range companionRows {
		t.CompanionDuplicateExchange[r.CompanionId] = append(t.CompanionDuplicateExchange[r.CompanionId], store.DuplicateExchange{
			PossessionType: model.PossessionType(r.PossessionType), PossessionId: r.PossessionId, Count: r.Count,
		})
	}
	for _, r := range costumeEnhanced {
		t.CostumeEnhanced[r.CostumeEnhancedId] = store.CostumeEnhancedRef{
			CostumeId: r.CostumeId, Level: r.Level, LimitBreakCount: r.LimitBreakCount,
			ActiveSkillLevel: r.ActiveSkillLevel, AwakenCount: r.AwakenCount,
		}
	}
	for _, r := range companionEnhanced {
		t.CompanionEnhanced[r.CompanionEnhancedId] = store.CompanionEnhancedRef{CompanionId: r.CompanionId, Level: r.Level}
	}
	return t, nil
}
