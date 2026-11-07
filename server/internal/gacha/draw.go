package gacha

import (
	"log"
	"math/rand"

	"lunar-tear/server/internal/masterdata"
	"lunar-tear/server/internal/model"
)

type RateTier struct {
	Weight         int
	PossessionType int32
	RarityType     model.RarityType
}

type DrawnItem struct {
	PossessionType int32
	PossessionId   int32
	RarityType     model.RarityType
	CharacterId    int32
	Count          int32
}

// Lunar Tear's rates; DefaultRates in rates.go matches them, and Pod Programs can change them.
var premiumRates = []RateTier{
	{200, int32(model.PossessionTypeCostume), model.RaritySSRare},
	{300, int32(model.PossessionTypeWeapon), model.RaritySSRare},
	{500, int32(model.PossessionTypeCostume), model.RaritySRare},
	{1000, int32(model.PossessionTypeWeapon), model.RaritySRare},
	{8000, int32(model.PossessionTypeWeapon), model.RarityRare},
}

func DrawPremium(bp *masterdata.BannerPool, count int, fixedRarityMin int32, fixedCount int, rateMultiplier float64) []DrawnItem {
	result := make([]DrawnItem, 0, count)
	rates := adjustRates(Rates().Tiers(), rateMultiplier)
	totalWeight := 0
	for _, r := range rates {
		totalWeight += r.Weight
	}

	for i := range count {
		isGuaranteeSlot := fixedCount > 0 && i >= count-fixedCount
		item := rollOne(bp, rates, totalWeight)

		if isGuaranteeSlot && item.RarityType < fixedRarityMin {
			item = rollAtMinRarity(bp, rates, fixedRarityMin)
		}
		result = append(result, item)
	}
	return result
}

func DrawBox(items []BoxItem, count int) []DrawnItem {
	var available []int
	for i, item := range items {
		remaining := item.MaxCount - item.DrewCount
		for range remaining {
			available = append(available, i)
		}
	}

	result := make([]DrawnItem, 0, count)
	for i := 0; i < count && len(available) > 0; i++ {
		pick := rand.Intn(len(available))
		idx := available[pick]
		item := items[idx]
		result = append(result, DrawnItem{
			PossessionType: item.PossessionType,
			PossessionId:   item.PossessionId,
			RarityType:     item.RarityType,
		})
		items[idx].DrewCount++
		available = append(available[:pick], available[pick+1:]...)
	}
	return result
}

func DrawReward(materials []masterdata.GachaPoolItem, count int) []DrawnItem {
	if len(materials) == 0 {
		return nil
	}
	result := make([]DrawnItem, 0, count)
	for range count {
		m := materials[rand.Intn(len(materials))]
		result = append(result, DrawnItem{
			PossessionType: m.PossessionType,
			PossessionId:   m.PossessionId,
			RarityType:     m.RarityType,
		})
	}
	return result
}

type BoxItem struct {
	PossessionType int32
	PossessionId   int32
	RarityType     model.RarityType
	Count          int32
	MaxCount       int32
	DrewCount      int32
	IsTarget       bool
}

func adjustRates(base []RateTier, multiplier float64) []RateTier {
	if multiplier == 1.0 || multiplier == 0 {
		return base
	}
	adjusted := make([]RateTier, len(base))
	copy(adjusted, base)

	var fourStarExtra int
	var nonFourStar int
	for i, r := range adjusted {
		if r.RarityType >= model.RaritySSRare {
			extra := int(float64(r.Weight) * (multiplier - 1.0))
			adjusted[i].Weight += extra
			fourStarExtra += extra
		} else {
			nonFourStar += r.Weight
		}
	}
	if nonFourStar > 0 && fourStarExtra > 0 {
		for i, r := range adjusted {
			if r.RarityType < model.RaritySSRare {
				reduction := fourStarExtra * r.Weight / nonFourStar
				adjusted[i].Weight -= reduction
				if adjusted[i].Weight < 1 {
					adjusted[i].Weight = 1
				}
			}
		}
	}
	return adjusted
}

func rollOne(bp *masterdata.BannerPool, rates []RateTier, totalWeight int) DrawnItem {
	roll := rand.Intn(totalWeight)
	cumulative := 0
	var tier RateTier
	for _, r := range rates {
		cumulative += r.Weight
		if roll < cumulative {
			tier = r
			break
		}
	}

	if item, ok := tryFeaturedRateUp(bp, tier); ok {
		return item
	}
	return pickFromPool(bp, tier.PossessionType, tier.RarityType)
}

func tryFeaturedRateUp(bp *masterdata.BannerPool, tier RateTier) (DrawnItem, bool) {
	var matches []masterdata.GachaPoolItem
	for _, f := range bp.Featured {
		if f.PossessionType == tier.PossessionType && f.RarityType == tier.RarityType {
			matches = append(matches, f)
		}
	}
	if len(matches) == 0 {
		return DrawnItem{}, false
	}
	if rand.Float64()*100 >= Rates().FeaturedPercent {
		return DrawnItem{}, false
	}
	f := matches[rand.Intn(len(matches))]
	return DrawnItem{
		PossessionType: f.PossessionType,
		PossessionId:   f.PossessionId,
		RarityType:     f.RarityType,
		CharacterId:    f.CharacterId,
	}, true
}

func rollAtMinRarity(bp *masterdata.BannerPool, rates []RateTier, minRarity model.RarityType) DrawnItem {
	var filtered []RateTier
	filteredTotal := 0
	for _, r := range rates {
		if r.RarityType >= minRarity {
			filtered = append(filtered, r)
			filteredTotal += r.Weight
		}
	}
	if filteredTotal == 0 {
		return pickFromPool(bp, int32(model.PossessionTypeWeapon), minRarity)
	}
	return rollOne(bp, filtered, filteredTotal)
}

func pickFromPool(bp *masterdata.BannerPool, possessionType int32, rarityType model.RarityType) DrawnItem {
	if possessionType == int32(model.PossessionTypeCostume) {
		items := bp.CostumesByRarity[rarityType]
		if len(items) == 0 {
			items = bp.CostumesByRarity[model.RaritySSRare]
		}
		if len(items) == 0 {
			log.Printf("[pickFromPool] empty costume pool for rarity=%d, returning phantom item", rarityType)
			return DrawnItem{PossessionType: int32(model.PossessionTypeWeapon), RarityType: rarityType}
		}
		pick := items[rand.Intn(len(items))]
		return DrawnItem{
			PossessionType: pick.PossessionType,
			PossessionId:   pick.PossessionId,
			RarityType:     pick.RarityType,
			CharacterId:    pick.CharacterId,
		}
	}

	items := bp.WeaponsByRarity[rarityType]
	if len(items) == 0 {
		items = bp.WeaponsByRarity[model.RarityRare]
	}
	if len(items) == 0 {
		log.Printf("[pickFromPool] empty weapon pool for rarity=%d, returning phantom item", rarityType)
		return DrawnItem{PossessionType: int32(model.PossessionTypeWeapon), RarityType: rarityType}
	}
	pick := items[rand.Intn(len(items))]
	return DrawnItem{
		PossessionType: pick.PossessionType,
		PossessionId:   pick.PossessionId,
		RarityType:     pick.RarityType,
	}
}
