package store

import (
	"fmt"
	"log"
	"math/rand"
	"slices"
	"sort"

	"github.com/google/uuid"

	"lunar-tear/server/internal/gametime"
	"lunar-tear/server/internal/model"
)

func DeductPrice(user *UserState, priceType, priceId, amount int32) error {
	switch priceType {
	case model.PriceTypeConsumableItem:
		cur := user.ConsumableItems[priceId]
		if cur < amount {
			return fmt.Errorf("insufficient consumable %d: have %d, need %d", priceId, cur, amount)
		}
		user.ConsumableItems[priceId] = cur - amount
	case model.PriceTypeGem:
		total := user.Gem.FreeGem + user.Gem.PaidGem
		if total < amount {
			return fmt.Errorf("insufficient gems: have %d, need %d", total, amount)
		}
		if user.Gem.FreeGem >= amount {
			user.Gem.FreeGem -= amount
		} else {
			amount -= user.Gem.FreeGem
			user.Gem.FreeGem = 0
			user.Gem.PaidGem -= amount
		}
	case model.PriceTypePaidGem:
		if user.Gem.PaidGem < amount {
			return fmt.Errorf("insufficient paid gems: have %d, need %d", user.Gem.PaidGem, amount)
		}
		user.Gem.PaidGem -= amount
	case model.PriceTypePlatformPayment:
		// real-money purchase -- treat as free on private server
	default:
		log.Printf("[DeductPrice] unhandled priceType=%d priceId=%d amount=%d", priceType, priceId, amount)
	}
	return nil
}

func DeductPossession(user *UserState, possessionType model.PossessionType, possessionId, count int32) {
	switch possessionType {
	case model.PossessionTypeMaterial:
		user.Materials[possessionId] -= count
		if user.Materials[possessionId] <= 0 {
			delete(user.Materials, possessionId)
		}
	case model.PossessionTypeConsumableItem:
		user.ConsumableItems[possessionId] -= count
		if user.ConsumableItems[possessionId] <= 0 {
			delete(user.ConsumableItems, possessionId)
		}
	case model.PossessionTypePaidGem:
		user.Gem.PaidGem -= count
	case model.PossessionTypeFreeGem:
		user.Gem.FreeGem -= count
	default:
		log.Printf("[DeductPossession] unhandled type=%d id=%d count=%d", possessionType, possessionId, count)
	}
}

func GrantPossession(user *UserState, possessionType model.PossessionType, possessionId, count int32) {
	switch possessionType {
	case model.PossessionTypeMaterial:
		user.Materials[possessionId] += count
	case model.PossessionTypeConsumableItem:
		user.ConsumableItems[possessionId] += count
	case model.PossessionTypePaidGem:
		user.Gem.PaidGem += count
	case model.PossessionTypeFreeGem:
		user.Gem.FreeGem += count
	case model.PossessionTypeImportantItem:
		user.ImportantItems[possessionId] += count
	case model.PossessionTypePremiumItem:
		user.PremiumItems[possessionId] = gametime.NowMillis()
	case model.PossessionTypeThought:
		GrantThought(user, possessionId, gametime.NowMillis())
	default:
		log.Printf("[GrantPossession] unhandled type=%d id=%d count=%d", possessionType, possessionId, count)
	}
}

type CostumeRef struct {
	CharacterId int32
}

type WeaponRef struct {
	WeaponSkillGroupId                 int32
	WeaponAbilityGroupId               int32
	WeaponStoryReleaseConditionGroupId int32
}

type WeaponStoryReleaseCond struct {
	StoryIndex                      int32
	WeaponStoryReleaseConditionType model.WeaponStoryReleaseConditionType
	ConditionValue                  int32
}

type PartsRef struct {
	PartsGroupId                  int32
	RarityType                    int32
	PartsInitialLotteryId         int32
	PartsStatusMainLotteryGroupId int32
	PartsStatusSubLotteryGroupId  int32
}

// PartsStatusSubDef carries the per-lottery-id sub-status shape needed at
// grant time. Held here so the store package does not import masterdata.
type PartsStatusSubDef struct {
	StatusKindType           int32
	StatusCalculationType    int32
	StatusChangeInitialValue int32
	StatusFunc               func(level int32) int32
}

// DuplicateExchange is one possession given in place of a costume or
// companion the player already owns (m_costume_duplication_exchange_possession_group,
// m_companion_duplication_exchange_possession_group).
type DuplicateExchange struct {
	PossessionType model.PossessionType
	PossessionId   int32
	Count          int32
}

// CostumeEnhancedRef is an m_costume_enhanced row: possession type 7 grants
// the costume already at this level, limit break, skill level and awakening.
type CostumeEnhancedRef struct {
	CostumeId        int32
	Level            int32
	LimitBreakCount  int32
	ActiveSkillLevel int32
	AwakenCount      int32
}

// CompanionEnhancedRef is an m_companion_enhanced row: possession type 9
// grants the companion at this level.
type CompanionEnhancedRef struct {
	CompanionId int32
	Level       int32
}

// PossessionLimits are MConfig POSSESSION_COUNT_LIMIT_*: the most a player
// can hold. Zero means no limit.
type PossessionLimits struct {
	Weapon         int32 // weapons owned
	Parts          int32 // parts owned
	Material       int32 // per material
	ConsumableItem int32 // per consumable item, except gold
	Money          int32 // gold
	ImportantItem  int32 // per important item
	GachaMedal     int32 // per gacha medal (ConsumableItemType GACHA_MEDAL)
}

// MGiftText ids of the gifts the server sends.
const (
	GiftTextSummons              int32 = 1  // "Earned from summons."
	GiftTextDuplicateCostume     int32 = 6  // "Earned from obtaining a duplicate costume."
	GiftTextDuplicateCompanion   int32 = 7  // "Earned from obtaining a duplicate companion."
	GiftTextOverflow             int32 = 11 // "A gift for holding too many items."
	GiftTextPremiumShopOverflow  int32 = 31 // "Item that exceeded the cap due to a purchase at the premium shop."
	GiftTextExchangeShopOverflow int32 = 32 // "Item that exceeded the cap due to an exchange at the shop."
)

// GiftExpirationMillis is how long a gift the server sends stays claimable.
const GiftExpirationMillis int64 = 30 * 24 * 60 * 60 * 1000

// GrantOverflow is what a grant sent to the gift box because of a limit.
type GrantOverflow struct {
	PossessionType model.PossessionType
	PossessionId   int32
	Count          int32
}

type PossessionGranter struct {
	CostumeById        map[int32]CostumeRef
	WeaponById         map[int32]WeaponRef
	WeaponSkillSlots   map[int32][]int32
	WeaponAbilitySlots map[int32][]int32
	ReleaseConditions  map[int32][]WeaponStoryReleaseCond

	PartsById                            map[int32]PartsRef
	DefaultPartsStatusMainByLotteryGroup map[int32]int32
	PartsVariantsByGroupRarity           map[int32]map[int32][]int32
	PartsSubStatusPool                   map[int32][]int32
	PartsSubStatusDefs                   map[int32]PartsStatusSubDef

	PartsSellPriceL1ByRarity map[int32]int32
	GoldConsumableItemId     int32

	CostumeDuplicateExchange   map[int32][]DuplicateExchange // CostumeId ->
	CompanionDuplicateExchange map[int32][]DuplicateExchange // CompanionId ->
	CostumeEnhancedById        map[int32]CostumeEnhancedRef  // CostumeEnhancedId ->
	CompanionEnhancedById      map[int32]CompanionEnhancedRef
	// CostumeExpForLevel is the exp a costume has on reaching level.
	CostumeExpForLevel func(costumeId, level int32) int32
	// ApplyCostumeAwaken applies one awakening step's effect to a costume.
	ApplyCostumeAwaken func(user *UserState, userCostumeUuid string, costumeId, step int32, nowMillis int64)

	Limits PossessionLimits
	// GachaMedalItemIds are the consumable items of ConsumableItemType
	// GACHA_MEDAL, limited by Limits.GachaMedal instead of ConsumableItem.
	GachaMedalItemIds map[int32]bool

	LastChangedStoryWeaponIds []int32
}

func (g *PossessionGranter) DrainChangedStoryWeaponIds() []int32 {
	ids := g.LastChangedStoryWeaponIds
	g.LastChangedStoryWeaponIds = nil
	return ids
}

// GrantFull grants count of a possession the way every reward path does. A
// costume or companion the player already owns becomes its duplicate
// exchange possessions instead; enhanced costumes and companions arrive at
// their m_*_enhanced level. What would go past a possession limit goes to the
// gift box as "A gift for holding too many items" (see GrantFullAs).
func (g *PossessionGranter) GrantFull(user *UserState, possessionType model.PossessionType, possessionId, count int32, nowMillis int64) {
	g.GrantFullAs(user, possessionType, possessionId, count, nowMillis, GiftTextOverflow)
}

// GrantFullAs is GrantFull with the MGiftText id for anything that would go
// past a possession limit: that part is sent to the gift box instead of being
// dropped or refused. It returns what went to the gift box; the same entries
// are also added to user.Ext.GrantOverflow for TakeGrantOverflow.
func (g *PossessionGranter) GrantFullAs(user *UserState, possessionType model.PossessionType, possessionId, count int32, nowMillis int64, giftTextId int32) []GrantOverflow {
	start := len(user.Ext.GrantOverflow)
	n := max(count, 1)
	switch possessionType {
	case model.PossessionTypeCostume:
		for range n {
			if !g.GrantCostume(user, possessionId, nowMillis) {
				g.grantExchange(user, g.CostumeDuplicateExchange[possessionId], nowMillis, GiftTextDuplicateCostume)
			}
		}
	case model.PossessionTypeCostumeEnhanced:
		for range n {
			g.grantCostumeEnhanced(user, possessionId, nowMillis)
		}
	case model.PossessionTypeWeapon, model.PossessionTypeWeaponEnhanced:
		over := int32(0)
		for range n {
			if g.Limits.Weapon > 0 && len(user.Weapons) >= int(g.Limits.Weapon) {
				over++
				continue
			}
			g.GrantWeapon(user, possessionId, nowMillis)
		}
		g.sendOverflow(user, possessionType, possessionId, over, nowMillis, giftTextId)
	case model.PossessionTypeCompanion:
		for range n {
			if !g.GrantCompanion(user, possessionId, nowMillis) {
				g.grantExchange(user, g.CompanionDuplicateExchange[possessionId], nowMillis, GiftTextDuplicateCompanion)
			}
		}
	case model.PossessionTypeCompanionEnhanced:
		for range n {
			g.grantCompanionEnhanced(user, possessionId, nowMillis)
		}
	case model.PossessionTypeParts, model.PossessionTypePartsEnhanced:
		over := int32(0)
		for range n {
			if g.Limits.Parts > 0 && len(user.Parts) >= int(g.Limits.Parts) {
				over++
				continue
			}
			g.GrantParts(user, possessionId, nowMillis)
		}
		g.sendOverflow(user, possessionType, possessionId, over, nowMillis, giftTextId)
	default:
		g.GrantCapped(user, possessionType, possessionId, count, nowMillis, giftTextId)
	}
	if len(user.Ext.GrantOverflow) == start {
		return nil
	}
	return slices.Clone(user.Ext.GrantOverflow[start:])
}

// GrantCapped grants a stackable possession (material, consumable item, gold,
// gacha medal, important item) up to its limit and sends the rest to the gift
// box with giftTextId. Other possession types are granted as GrantPossession
// does. For summons, which grant stackables directly.
func (g *PossessionGranter) GrantCapped(user *UserState, possessionType model.PossessionType, possessionId, count int32, nowMillis int64, giftTextId int32) {
	if have, limit, ok := g.stackLimit(user, possessionType, possessionId); ok && limit > 0 && count > 0 {
		room := max(int64(limit)-have, 0)
		if int64(count) > room {
			// Gifts of at most the limit each, so every one can be claimed once there is room.
			for rest := count - int32(room); rest > 0; rest -= min(rest, limit) {
				g.sendOverflow(user, possessionType, possessionId, min(rest, limit), nowMillis, giftTextId)
			}
			count = int32(room)
		}
		if count == 0 {
			return
		}
	}
	GrantPossession(user, possessionType, possessionId, count)
}

// GrantWeaponCapped grants one weapon, or sends it to the gift box with
// giftTextId when the player holds the most weapons allowed.
func (g *PossessionGranter) GrantWeaponCapped(user *UserState, weaponId int32, nowMillis int64, giftTextId int32) {
	if g.Limits.Weapon > 0 && len(user.Weapons) >= int(g.Limits.Weapon) {
		g.sendOverflow(user, model.PossessionTypeWeapon, weaponId, 1, nowMillis, giftTextId)
		return
	}
	g.GrantWeapon(user, weaponId, nowMillis)
}

// GrantExchangeAs grants duplicate exchange possessions; what would go past a
// limit goes to the gift box with giftTextId.
func (g *PossessionGranter) GrantExchangeAs(user *UserState, exchange []DuplicateExchange, nowMillis int64, giftTextId int32) {
	g.grantExchange(user, exchange, nowMillis, giftTextId)
}

func (g *PossessionGranter) grantExchange(user *UserState, exchange []DuplicateExchange, nowMillis int64, giftTextId int32) {
	for _, ex := range exchange {
		g.GrantCapped(user, ex.PossessionType, ex.PossessionId, ex.Count, nowMillis, giftTextId)
	}
}

// stackLimit is how many of a stackable possession the player holds and its
// MConfig limit; ok is false for possessions without a per-id count.
func (g *PossessionGranter) stackLimit(user *UserState, possessionType model.PossessionType, possessionId int32) (have int64, limit int32, ok bool) {
	switch possessionType {
	case model.PossessionTypeMaterial:
		return int64(user.Materials[possessionId]), g.Limits.Material, true
	case model.PossessionTypeConsumableItem:
		have = int64(user.ConsumableItems[possessionId])
		switch {
		case possessionId == g.GoldConsumableItemId:
			return have, g.Limits.Money, true
		case g.GachaMedalItemIds[possessionId]:
			return have, g.Limits.GachaMedal, true
		}
		return have, g.Limits.ConsumableItem, true
	case model.PossessionTypeImportantItem:
		return int64(user.ImportantItems[possessionId]), g.Limits.ImportantItem, true
	}
	return 0, 0, false
}

// sendOverflow puts count of a possession in the gift box and records it in
// user.Ext.GrantOverflow.
func (g *PossessionGranter) sendOverflow(user *UserState, possessionType model.PossessionType, possessionId, count int32, nowMillis int64, giftTextId int32) {
	if count <= 0 {
		return
	}
	AddGift(user, possessionType, possessionId, count, giftTextId, nowMillis)
	user.Ext.GrantOverflow = append(user.Ext.GrantOverflow, GrantOverflow{
		PossessionType: possessionType,
		PossessionId:   possessionId,
		Count:          count,
	})
	log.Printf("[GrantFull] over the possession limit: type=%d id=%d count=%d -> gift box (text %d)", possessionType, possessionId, count, giftTextId)
}

// AddGift puts an unclaimed gift in the gift box. It expires after
// GiftExpirationMillis like the login bonus gifts.
func AddGift(user *UserState, possessionType model.PossessionType, possessionId, count, giftTextId int32, nowMillis int64) {
	user.Gifts.NotReceived = append(user.Gifts.NotReceived, NotReceivedGiftState{
		GiftCommon: GiftCommonState{
			PossessionType:        int32(possessionType),
			PossessionId:          possessionId,
			Count:                 count,
			GrantDatetime:         nowMillis,
			DescriptionGiftTextId: giftTextId,
		},
		ExpirationDatetime: nowMillis + GiftExpirationMillis,
		UserGiftUuid:       uuid.New().String(),
	})
	live := int32(0)
	for _, gift := range user.Gifts.NotReceived {
		if gift.ExpirationDatetime == 0 || gift.ExpirationDatetime >= nowMillis {
			live++
		}
	}
	user.Notifications.GiftNotReceiveCount = live
}

// TakeGrantOverflow returns what grants in this request sent to the gift box
// because of a possession limit, and clears the list.
func TakeGrantOverflow(user *UserState) []GrantOverflow {
	out := user.Ext.GrantOverflow
	user.Ext.GrantOverflow = nil
	return out
}

func (g *PossessionGranter) grantCostumeEnhanced(user *UserState, costumeEnhancedId int32, nowMillis int64) {
	ref, ok := g.CostumeEnhancedById[costumeEnhancedId]
	if !ok {
		log.Printf("[GrantFull] unknown costumeEnhancedId=%d, granted as a plain costume", costumeEnhancedId)
		ref = CostumeEnhancedRef{CostumeId: costumeEnhancedId}
	}
	if !g.GrantCostume(user, ref.CostumeId, nowMillis) {
		g.grantExchange(user, g.CostumeDuplicateExchange[ref.CostumeId], nowMillis, GiftTextDuplicateCostume)
		return
	}
	var key string
	for k, c := range user.Costumes {
		if c.CostumeId == ref.CostumeId {
			key = k
			break
		}
	}
	c := user.Costumes[key]
	if ref.Level > 1 {
		c.Level = ref.Level
		if g.CostumeExpForLevel != nil {
			c.Exp = g.CostumeExpForLevel(ref.CostumeId, ref.Level)
		}
	}
	c.LimitBreakCount = ref.LimitBreakCount
	c.AwakenCount = ref.AwakenCount
	c.LatestVersion = nowMillis
	user.Costumes[key] = c
	if ref.ActiveSkillLevel > 1 {
		skill := user.CostumeActiveSkills[key]
		skill.Level = ref.ActiveSkillLevel
		skill.LatestVersion = nowMillis
		user.CostumeActiveSkills[key] = skill
	}
	if g.ApplyCostumeAwaken != nil {
		for step := int32(1); step <= ref.AwakenCount; step++ {
			g.ApplyCostumeAwaken(user, key, ref.CostumeId, step, nowMillis)
		}
	}
}

func (g *PossessionGranter) grantCompanionEnhanced(user *UserState, companionEnhancedId int32, nowMillis int64) {
	ref, ok := g.CompanionEnhancedById[companionEnhancedId]
	if !ok {
		log.Printf("[GrantFull] unknown companionEnhancedId=%d, granted as a plain companion", companionEnhancedId)
		ref = CompanionEnhancedRef{CompanionId: companionEnhancedId, Level: 1}
	}
	if !g.GrantCompanion(user, ref.CompanionId, nowMillis) {
		g.grantExchange(user, g.CompanionDuplicateExchange[ref.CompanionId], nowMillis, GiftTextDuplicateCompanion)
		return
	}
	for k, c := range user.Companions {
		if c.CompanionId == ref.CompanionId {
			c.Level = max(c.Level, ref.Level)
			c.LatestVersion = nowMillis
			user.Companions[k] = c
			return
		}
	}
}

// WouldOverflow reports whether granting count of a possession would take the
// player past an MConfig possession limit. For a costume or companion the
// player owns, that is whether its duplicate exchange would.
func (g *PossessionGranter) WouldOverflow(user *UserState, possessionType model.PossessionType, possessionId, count int32) bool {
	n := int64(max(count, 1))
	over := func(have int64, limit int32, add int64) bool {
		return limit > 0 && have+add > int64(limit)
	}
	exchangeOver := func(exchange []DuplicateExchange) bool {
		for _, ex := range exchange {
			if have, limit, ok := g.stackLimit(user, ex.PossessionType, ex.PossessionId); ok && over(have, limit, int64(ex.Count)*n) {
				return true
			}
		}
		return false
	}
	switch possessionType {
	case model.PossessionTypeWeapon, model.PossessionTypeWeaponEnhanced:
		return over(int64(len(user.Weapons)), g.Limits.Weapon, n)
	case model.PossessionTypeParts, model.PossessionTypePartsEnhanced:
		return over(int64(len(user.Parts)), g.Limits.Parts, n)
	case model.PossessionTypeCostume, model.PossessionTypeCostumeEnhanced:
		costumeId := possessionId
		if possessionType == model.PossessionTypeCostumeEnhanced {
			if ref, ok := g.CostumeEnhancedById[possessionId]; ok {
				costumeId = ref.CostumeId
			}
		}
		for _, c := range user.Costumes {
			if c.CostumeId == costumeId {
				return exchangeOver(g.CostumeDuplicateExchange[costumeId])
			}
		}
		return false
	case model.PossessionTypeCompanion, model.PossessionTypeCompanionEnhanced:
		companionId := possessionId
		if possessionType == model.PossessionTypeCompanionEnhanced {
			if ref, ok := g.CompanionEnhancedById[possessionId]; ok {
				companionId = ref.CompanionId
			}
		}
		for _, c := range user.Companions {
			if c.CompanionId == companionId {
				return exchangeOver(g.CompanionDuplicateExchange[companionId])
			}
		}
		return false
	}
	have, limit, ok := g.stackLimit(user, possessionType, possessionId)
	return ok && over(have, limit, n)
}

// GrantCostume adds the costume and reports whether it was new. An owned
// costume is left alone; GrantFull converts it, and summons convert their own.
func (g *PossessionGranter) GrantCostume(user *UserState, costumeId int32, nowMillis int64) bool {
	for _, row := range user.Costumes {
		if row.CostumeId == costumeId {
			return false
		}
	}
	if cm, ok := g.CostumeById[costumeId]; ok {
		if _, exists := user.Characters[cm.CharacterId]; !exists {
			user.Characters[cm.CharacterId] = CharacterState{
				CharacterId: cm.CharacterId,
				Level:       1,
			}
		}
	}
	key := uuid.New().String()
	user.Costumes[key] = CostumeState{
		UserCostumeUuid:     key,
		CostumeId:           costumeId,
		Level:               1,
		HeadupDisplayViewId: 1,
		AcquisitionDatetime: nowMillis,
	}
	user.CostumeActiveSkills[key] = CostumeActiveSkillState{
		UserCostumeUuid:     key,
		Level:               1,
		AcquisitionDatetime: nowMillis,
	}
	return true
}

// GrantCompanion adds the companion and reports whether it was new.
func (g *PossessionGranter) GrantCompanion(user *UserState, companionId int32, nowMillis int64) bool {
	for _, row := range user.Companions {
		if row.CompanionId == companionId {
			return false
		}
	}
	key := uuid.New().String()
	user.Companions[key] = CompanionState{
		UserCompanionUuid:   key,
		CompanionId:         companionId,
		Level:               1,
		HeadupDisplayViewId: 1,
		AcquisitionDatetime: nowMillis,
	}
	return true
}

func (g *PossessionGranter) GrantParts(user *UserState, requestedPartsId int32, nowMillis int64) {
	chosenPartsId, chosenRef, ok := g.rollPartsVariant(requestedPartsId)
	if !ok {
		g.grantBareParts(user, requestedPartsId, nowMillis)
		return
	}
	g.createParts(user, chosenPartsId, chosenRef, nowMillis)
}

// The rolled variant sets both rarity and rank, so the auto-sale decision can
// only happen after the roll. Returns the rolled variant id and whether it sold.
func (g *PossessionGranter) GrantOrSellPartsDrop(user *UserState, requestedPartsId int32, raritySet, rankSet map[int32]bool, nowMillis int64) (int32, bool) {
	chosenPartsId, chosenRef, ok := g.rollPartsVariant(requestedPartsId)
	if !ok {
		g.grantBareParts(user, requestedPartsId, nowMillis)
		return requestedPartsId, false
	}
	rarity := chosenRef.RarityType
	rank := chosenRef.PartsInitialLotteryId
	if price, ok := g.PartsSellPriceL1ByRarity[rarity]; ok && raritySet[rarity] && rankSet[rank] {
		user.ConsumableItems[g.GoldConsumableItemId] += price
		log.Printf("[GrantParts] auto-sold chosen=%d rarity=%d rank=%d -> %d gold", chosenPartsId, rarity, rank, price)
		return chosenPartsId, true
	}
	g.createParts(user, chosenPartsId, chosenRef, nowMillis)
	return chosenPartsId, false
}

func (g *PossessionGranter) grantBareParts(user *UserState, partsId int32, nowMillis int64) {
	key := uuid.New().String()
	user.Parts[key] = PartsState{
		UserPartsUuid:       key,
		PartsId:             partsId,
		Level:               1,
		AcquisitionDatetime: nowMillis,
	}
	log.Printf("[GrantParts] unknown partsId=%d, granted as-is with no variant roll", partsId)
}

// rollPartsVariant picks one of a parts group's 5 variants at random; the five
// carry distinct PartsInitialLotteryId 1..5, which is the part's rank.
func (g *PossessionGranter) rollPartsVariant(requestedPartsId int32) (int32, PartsRef, bool) {
	ref, refOk := g.PartsById[requestedPartsId]
	if !refOk {
		return requestedPartsId, PartsRef{}, false
	}
	chosenPartsId := requestedPartsId
	chosenRef := ref
	if variants := g.PartsVariantsByGroupRarity[ref.PartsGroupId][ref.RarityType]; len(variants) == 5 {
		chosenPartsId = variants[rand.Intn(len(variants))]
		chosenRef = g.PartsById[chosenPartsId]
	} else {
		log.Printf("[GrantParts] no 5-variant set for group=%d rarity=%d (have %d), granting requested=%d", ref.PartsGroupId, ref.RarityType, len(variants), requestedPartsId)
	}
	return chosenPartsId, chosenRef, true
}

func (g *PossessionGranter) createParts(user *UserState, chosenPartsId int32, chosenRef PartsRef, nowMillis int64) {
	mainStatId := g.DefaultPartsStatusMainByLotteryGroup[chosenRef.PartsStatusMainLotteryGroupId]
	if _, exists := user.PartsGroupNotes[chosenRef.PartsGroupId]; !exists {
		user.PartsGroupNotes[chosenRef.PartsGroupId] = PartsGroupNoteState{
			PartsGroupId:             chosenRef.PartsGroupId,
			FirstAcquisitionDatetime: nowMillis,
			LatestVersion:            nowMillis,
		}
	}
	key := uuid.New().String()
	user.Parts[key] = PartsState{
		UserPartsUuid:       key,
		PartsId:             chosenPartsId,
		Level:               1,
		PartsStatusMainId:   mainStatId,
		AcquisitionDatetime: nowMillis,
	}

	initialCount := chosenRef.PartsInitialLotteryId
	pool := g.PartsSubStatusPool[chosenRef.PartsStatusSubLotteryGroupId]
	if initialCount > 1 && len(pool) > 0 {
		for i := int32(0); i < initialCount-1; i++ {
			pickId := pool[rand.Intn(len(pool))]
			def, ok := g.PartsSubStatusDefs[pickId]
			if !ok {
				continue
			}
			val := def.StatusChangeInitialValue
			if def.StatusFunc != nil {
				val = def.StatusFunc(1)
			}
			user.PartsStatusSubs[PartsStatusSubKey{UserPartsUuid: key, StatusIndex: i + 1}] = PartsStatusSubState{
				UserPartsUuid:           key,
				StatusIndex:             i + 1,
				PartsStatusSubLotteryId: pickId,
				Level:                   1,
				StatusKindType:          def.StatusKindType,
				StatusCalculationType:   def.StatusCalculationType,
				StatusChangeValue:       val,
				LatestVersion:           nowMillis,
			}
		}
	}

	log.Printf("[GrantParts] chosen=%d group=%d rarity=%d preUnlockedSubs=%d", chosenPartsId, chosenRef.PartsGroupId, chosenRef.RarityType, initialCount-1)
}

func (g *PossessionGranter) GrantWeapon(user *UserState, weaponId int32, nowMillis int64) {
	key := uuid.New().String()
	user.Weapons[key] = WeaponState{
		UserWeaponUuid:      key,
		WeaponId:            weaponId,
		Level:               1,
		AcquisitionDatetime: nowMillis,
	}
	if _, exists := user.WeaponNotes[weaponId]; !exists {
		user.WeaponNotes[weaponId] = WeaponNoteState{
			WeaponId:                 weaponId,
			MaxLevel:                 1,
			MaxLimitBreakCount:       0,
			FirstAcquisitionDatetime: nowMillis,
			LatestVersion:            nowMillis,
		}
	}
	weapon, ok := g.WeaponById[weaponId]
	if !ok {
		return
	}

	g.populateWeaponSkillsAbilities(user, key, weapon)
	if weapon.WeaponStoryReleaseConditionGroupId != 0 {
		changed := false
		for _, cond := range g.ReleaseConditions[weapon.WeaponStoryReleaseConditionGroupId] {
			switch cond.WeaponStoryReleaseConditionType {
			case model.WeaponStoryReleaseConditionTypeAcquisition:
				if grantWeaponStoryUnlock(user, weaponId, cond.StoryIndex, nowMillis) {
					changed = true
				}
			case model.WeaponStoryReleaseConditionTypeQuestClear:
				if qs, ok := user.Quests[cond.ConditionValue]; ok && qs.QuestStateType == model.UserQuestStateTypeCleared {
					if grantWeaponStoryUnlock(user, weaponId, cond.StoryIndex, nowMillis) {
						changed = true
					}
				}
			}
		}
		if changed {
			g.LastChangedStoryWeaponIds = append(g.LastChangedStoryWeaponIds, weaponId)
		}
	}
}

func (g *PossessionGranter) populateWeaponSkillsAbilities(user *UserState, weaponUuid string, weapon WeaponRef) {
	if slots, ok := g.WeaponSkillSlots[weapon.WeaponSkillGroupId]; ok {
		skills := make([]WeaponSkillState, len(slots))
		for i, slot := range slots {
			skills[i] = WeaponSkillState{
				UserWeaponUuid: weaponUuid,
				SlotNumber:     slot,
				Level:          1,
			}
		}
		user.WeaponSkills[weaponUuid] = skills
	}
	if slots, ok := g.WeaponAbilitySlots[weapon.WeaponAbilityGroupId]; ok {
		abilities := make([]WeaponAbilityState, len(slots))
		for i, slot := range slots {
			abilities[i] = WeaponAbilityState{
				UserWeaponUuid: weaponUuid,
				SlotNumber:     slot,
				Level:          1,
			}
		}
		user.WeaponAbilities[weaponUuid] = abilities
	}
}

// GrantThought adds a debris (possession type 14, IUserThought) and reports
// whether it was new. A debris the player already has is not added again.
func GrantThought(user *UserState, thoughtId int32, nowMillis int64) bool {
	for _, t := range user.Thoughts {
		if t.ThoughtId == thoughtId {
			return false
		}
	}
	if user.Thoughts == nil {
		user.Thoughts = make(map[string]ThoughtState)
	}
	key := uuid.New().String()
	user.Thoughts[key] = ThoughtState{
		UserThoughtUuid:     key,
		ThoughtId:           thoughtId,
		AcquisitionDatetime: nowMillis,
		LatestVersion:       nowMillis,
	}
	return true
}

func GrantWeaponStoryUnlock(user *UserState, weaponId, storyIndex int32, nowMillis int64) bool {
	return grantWeaponStoryUnlock(user, weaponId, storyIndex, nowMillis)
}

func grantWeaponStoryUnlock(user *UserState, weaponId, storyIndex int32, nowMillis int64) bool {
	hasWeapon := false
	for _, row := range user.Weapons {
		if row.WeaponId == weaponId {
			hasWeapon = true
			break
		}
	}
	if !hasWeapon {
		log.Printf("[grantWeaponStoryUnlock] skipping weaponId=%d (weapon not in user.Weapons)", weaponId)
		return false
	}
	if user.WeaponStories == nil {
		user.WeaponStories = make(map[int32]WeaponStoryState)
	}
	cur := user.WeaponStories[weaponId]
	if storyIndex <= cur.ReleasedMaxStoryIndex {
		return false
	}
	user.WeaponStories[weaponId] = WeaponStoryState{
		WeaponId:              weaponId,
		ReleasedMaxStoryIndex: storyIndex,
		LatestVersion:         nowMillis,
	}
	return true
}

// DefaultDeckName returns the canonical default name of a deck slot:
// "Deck N" (N = UserDeckNumber), matching the official server's seeded names
// ("Deck 1", "Deck 2", ...). Every server-created default deck carries this
// name so the client shows "Deck N" instead of its localized fallback composed
// by CalculatorDeck.GetDefaultDeckName ("任务N" / "クエストN").
func DefaultDeckName(userDeckNumber int32) string {
	return fmt.Sprintf("Deck %d", userDeckNumber)
}

// InitialDeckNumbers are the quest-deck slot numbers the client always lists
// (CalculatorDeck.kInitialDeckListDeckNumbers). Slot 1 carries the starter
// formation; slots 2 and 3 start empty but must still carry their default
// names, otherwise the client renders them through GetDefaultDeckName.
var InitialDeckNumbers = [...]int32{1, 2, 3}

func EnsureDefaultDeck(user *UserState, nowMillis int64) {
	if len(user.Costumes) == 0 || len(user.Decks) > 0 {
		return
	}

	const rionCostumeId = int32(10100)
	const rionWeaponId = int32(101001)

	var costumeUuid, weaponUuid string
	for k, v := range user.Costumes {
		if v.CostumeId == rionCostumeId {
			costumeUuid = k
			break
		}
	}
	for k, v := range user.Weapons {
		if v.WeaponId == rionWeaponId {
			weaponUuid = k
			break
		}
	}

	dcUuid := uuid.New().String()
	user.DeckCharacters[dcUuid] = DeckCharacterState{
		UserDeckCharacterUuid: dcUuid,
		UserCompanionUuid:     "",
		UserCostumeUuid:       costumeUuid,
		MainUserWeaponUuid:    weaponUuid,
		Power:                 100,
		LatestVersion:         nowMillis,
	}
	// Seed every initial quest-deck slot the client lists, not just slot 1:
	// a slot without a name is rendered with the client's localized default
	// name, so each slot gets its "Deck N" name here. Slot 1 keeps the starter
	// formation; the other slots start empty.
	for _, n := range InitialDeckNumbers {
		deck := DeckState{
			DeckType:       model.DeckTypeQuest,
			UserDeckNumber: n,
			Name:           DefaultDeckName(n),
			LatestVersion:  nowMillis,
		}
		if n == 1 {
			deck.UserDeckCharacterUuid01 = dcUuid
			deck.Power = 100
		}
		user.Decks[DeckKey{DeckType: model.DeckTypeQuest, UserDeckNumber: n}] = deck
	}

	if _, exists := user.DeckTypeNotes[model.DeckTypeQuest]; !exists {
		user.DeckTypeNotes[model.DeckTypeQuest] = DeckTypeNoteState{
			DeckType:      model.DeckTypeQuest,
			MaxDeckPower:  100,
			LatestVersion: nowMillis,
		}
	}
}

func FirstSortedKey[V any](m map[string]V) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys[0]
}

func ApplyDeckReplacement(user *UserState, deckType model.DeckType, userDeckNumber int32, slots []DeckCharacterInput, nowMillis int64) {
	deckKey := DeckKey{DeckType: deckType, UserDeckNumber: userDeckNumber}
	deck := user.Decks[deckKey]
	deck.DeckType = deckType
	deck.UserDeckNumber = userDeckNumber
	if deck.Name == "" {
		deck.Name = DefaultDeckName(userDeckNumber)
	}
	if deck.Power == 0 {
		deck.Power = 100
	}

	for _, oldUuid := range []string{deck.UserDeckCharacterUuid01, deck.UserDeckCharacterUuid02, deck.UserDeckCharacterUuid03} {
		if oldUuid == "" {
			continue
		}
		delete(user.DeckCharacters, oldUuid)
		delete(user.DeckSubWeapons, oldUuid)
		delete(user.DeckParts, oldUuid)
	}

	var newUuids [3]string
	for i := range 3 {
		if i >= len(slots) || slots[i].UserCostumeUuid == "" {
			continue
		}
		slot := slots[i]
		dcUuid := uuid.New().String()
		user.DeckCharacters[dcUuid] = DeckCharacterState{
			UserDeckCharacterUuid: dcUuid,
			UserCostumeUuid:       slot.UserCostumeUuid,
			MainUserWeaponUuid:    slot.MainUserWeaponUuid,
			UserCompanionUuid:     slot.UserCompanionUuid,
			UserThoughtUuid:       slot.UserThoughtUuid,
			DressupCostumeId:      slot.DressupCostumeId,
			LatestVersion:         nowMillis,
		}
		user.DeckSubWeapons[dcUuid] = slot.SubWeaponUuids
		user.DeckParts[dcUuid] = slot.PartsUuids
		newUuids[i] = dcUuid
	}

	deck.UserDeckCharacterUuid01 = newUuids[0]
	deck.UserDeckCharacterUuid02 = newUuids[1]
	deck.UserDeckCharacterUuid03 = newUuids[2]
	deck.LatestVersion = nowMillis
	user.Decks[deckKey] = deck
}

func RemoveDeckData(user *UserState, deckType model.DeckType, userDeckNumber int32) {
	deckKey := DeckKey{DeckType: deckType, UserDeckNumber: userDeckNumber}
	deck, ok := user.Decks[deckKey]
	if !ok {
		return
	}
	for _, dcUuid := range []string{deck.UserDeckCharacterUuid01, deck.UserDeckCharacterUuid02, deck.UserDeckCharacterUuid03} {
		if dcUuid == "" {
			continue
		}
		delete(user.DeckCharacters, dcUuid)
		delete(user.DeckSubWeapons, dcUuid)
		delete(user.DeckParts, dcUuid)
	}
	delete(user.Decks, deckKey)
}

func ReadDeckSlots(user *UserState, deckType model.DeckType, userDeckNumber int32) []DeckCharacterInput {
	deckKey := DeckKey{DeckType: deckType, UserDeckNumber: userDeckNumber}
	deck, ok := user.Decks[deckKey]
	if !ok {
		return nil
	}
	slots := make([]DeckCharacterInput, 3)
	for i, dcUuid := range []string{deck.UserDeckCharacterUuid01, deck.UserDeckCharacterUuid02, deck.UserDeckCharacterUuid03} {
		if dcUuid == "" {
			continue
		}
		dc, ok := user.DeckCharacters[dcUuid]
		if !ok {
			continue
		}
		slots[i] = DeckCharacterInput{
			UserCostumeUuid:    dc.UserCostumeUuid,
			MainUserWeaponUuid: dc.MainUserWeaponUuid,
			SubWeaponUuids:     user.DeckSubWeapons[dcUuid],
			PartsUuids:         user.DeckParts[dcUuid],
			UserCompanionUuid:  dc.UserCompanionUuid,
			UserThoughtUuid:    dc.UserThoughtUuid,
			DressupCostumeId:   dc.DressupCostumeId,
		}
	}
	return slots
}
