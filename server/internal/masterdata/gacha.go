package masterdata

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"lunar-tear/server/internal/gametime"
	"lunar-tear/server/internal/model"
	"lunar-tear/server/internal/store"
	"lunar-tear/server/internal/utils"
)

type GachaMedalInfo struct {
	GachaMedalId        int32
	ConsumableItemId    int32
	AutoConvertDatetime int64
	ConversionRate      int32
}

// chapterGachaIdBase keeps chapter summons clear of m_mom_banner's summon
// ids (200001-200003 are "Mama's Dream" summons).
const chapterGachaIdBase = model.ChapterGachaIdBase

// sharedMedalGachas are summons without their own m_gacha_medal row that
// shared another summon's medal: both volumes of the NieR:Automata crossover
// (at launch and in the rerun) gave Shard (Automata).
var sharedMedalGachas = map[int32]int32{46: 45, 151: 150}

// medalItemGachas are summons without an m_gacha_medal row whose medal is
// known by its item: its exchange shop opens with the summon and stays open
// to its end, and the names match (e.g. Festive Dress, YoRHa Stage Play,
// Fortune's Dress, Blessed Dress, DQX, Yuletide).
var medalItemGachas = map[int32]int32{
	401: 8087, 501: 8168, 535: 8192, 541: 8192, 542: 8192, 543: 8193, 549: 8197, 550: 8197,
	555: 8203, 556: 8203, 582: 8222, 583: 8222, 585: 8222,
	510001: 8030, 513001: 8033, 515001: 8045, 536001: 8110, 537001: 8133, 9000035: 8021,
}

// stepUpStepCounts is the number of steps of a multi-step summon, by the
// group in its banner asset (step_up_<group><step>): the client shows one
// appeal page per step (ui/gacha/<lang>/step_up_<group>001/appeal/000n in
// the game files) and nothing for a step without one. Groups not listed
// have five.
var stepUpStepCounts = map[int32]int{
	500: 4, 504: 3, 506: 3, 514: 4, 519: 3, 522: 3, 525: 4, 527: 3, 534: 3, 536: 3,
	537: 3, 538: 3, 551: 3, 552: 3, 557: 3, 558: 3, 560: 3, 561: 3, 563: 3, 565: 4,
	566: 3, 568: 3, 569: 4, 570: 4, 571: 3, 572: 3, 573: 3, 574: 3, 575: 3, 577: 3,
	578: 3, 579: 4, 580: 3, 581: 3, 582: 3, 583: 3, 584: 4, 585: 3, 586: 3, 587: 3,
	588: 3, 589: 3, 590: 4, 600: 3, 601: 4, 602: 4, 603: 4, 604: 3, 605: 3, 606: 3,
	607: 3, 609: 4, 610: 3, 611: 3, 613: 4,
}

const defaultStepUpSteps = 5

// parkedReruns are summons whose only banner rows start in 2099. They ran
// while their exchange shop was open: from the shop's start (this long before
// the medal's AutoConvertDatetime) to two days before the medals convert.
var parkedReruns = map[int32]int64{
	159: 3110399000, // FFXIV, 2022-05-10 to 06-13
	414: 1123199000, // Monthly Featured, 2022-09-26 to 10-07
}

const rerunEndBeforeConvert = 2 * 24 * 60 * 60 * 1000

// LoadGachaCatalog assembles the gacha catalog from master data and, when no
// calendar is running, borrows every schedule window to the server clock
// (P0 fix: the 2024-04 snapshot's periods are all in the past or written into
// 2099, so a real-time filter dropped 160/160 entries and GetGachaList
// returned an empty list).
//
// Calendar coordination (B 方案): once gametime.CurrentCalendar() is set, the
// game clock runs inside the master data's own timeline and the rotating
// windows come alive on their own schedule — the borrow is skipped so each
// summon opens and closes exactly as its term says (and the per-loop reset in
// service.calendarGameStart reruns them next loop). Without a calendar
// (calendar.json absent) the borrow remains as the deployed fallback, keeping
// the current always-open behaviour.
func LoadGachaCatalog() ([]store.GachaCatalogEntry, map[int32]GachaMedalInfo, error) {
	entries, medalInfoByGacha, err := loadGachaCatalogPeriods()
	if err != nil {
		return nil, nil, err
	}
	if gametime.CurrentCalendar() == nil {
		entries = borrowCurrentGachaPeriods(entries, gametime.NowMillis())
	}
	return entries, medalInfoByGacha, nil
}

// loadGachaCatalogPeriods builds the gacha catalog with the raw Start/End
// windows untouched. Kept separate from LoadGachaCatalog so tests can assert
// against the snapshot's full period set before the borrow/selection step.
func loadGachaCatalogPeriods() ([]store.GachaCatalogEntry, map[int32]GachaMedalInfo, error) {
	medals, err := utils.ReadTable[EntityMGachaMedal]("m_gacha_medal")
	if err != nil {
		return nil, nil, fmt.Errorf("load gacha medal table: %w", err)
	}
	banners, err := utils.ReadTable[EntityMMomBanner]("m_mom_banner")
	if err != nil {
		return nil, nil, fmt.Errorf("load mom banner table: %w", err)
	}

	gachaToMedal := make(map[int32]EntityMGachaMedal)
	medalByItem := make(map[int32]EntityMGachaMedal)
	for _, m := range medals {
		gachaToMedal[m.ShopTransitionGachaId] = m
		medalByItem[m.ConsumableItemId] = m
	}
	for gacha, owner := range sharedMedalGachas {
		if m, ok := gachaToMedal[owner]; ok {
			if _, own := gachaToMedal[gacha]; !own {
				gachaToMedal[gacha] = m
			}
		}
	}
	for gacha, item := range medalItemGachas {
		if m, ok := medalByItem[item]; ok {
			if _, own := gachaToMedal[gacha]; !own {
				gachaToMedal[gacha] = m
			}
		}
	}
	medalInfoByGacha := make(map[int32]GachaMedalInfo, len(gachaToMedal))
	for gacha, m := range gachaToMedal {
		medalInfoByGacha[gacha] = GachaMedalInfo{
			GachaMedalId:        m.GachaMedalId,
			ConsumableItemId:    m.ConsumableItemId,
			AutoConvertDatetime: m.AutoConvertDatetime,
			ConversionRate:      m.ConversionRate,
		}
	}

	var entries []store.GachaCatalogEntry

	// Some summons have a second, placeholder row starting in 2099; the
	// earliest row is the summon.
	sort.SliceStable(banners, func(i, j int) bool { return banners[i].StartDatetime < banners[j].StartDatetime })
	seen := make(map[int32]bool)
	for _, b := range banners {
		if b.DestinationDomainType != model.MomBannerDomainGacha {
			continue
		}
		gachaId := b.DestinationDomainId
		if seen[gachaId] {
			continue
		}
		medal, hasMedal := gachaToMedal[gachaId]
		special := specialSummons[gachaId]
		start, end := b.StartDatetime, b.EndDatetime
		if lead, ok := parkedReruns[gachaId]; ok && hasMedal {
			start, end = medal.AutoConvertDatetime-lead, medal.AutoConvertDatetime-rerunEndBeforeConvert
		}

		entry := store.GachaCatalogEntry{
			GachaId:             gachaId,
			GachaLabelType:      model.GachaLabelPremium,
			GachaModeType:       model.GachaModeBasic,
			GachaAutoResetType:  model.GachaAutoResetNone,
			IsUserGachaUnlock:   true,
			StartDatetime:       start,
			EndDatetime:         end,
			GachaDecorationType: model.GachaDecorationNormal,
			SortOrder:           b.SortOrderDesc,
			BannerAssetName:     b.BannerAssetName,
			GroupId:             gachaId,
			CeilingCount:        model.PityCeilingCount,
			UnlockConditionType: model.GachaUnlockNone,
		}
		if hasMedal {
			entry.GachaMedalId = medal.GachaMedalId
			entry.MedalConsumableItemId = medal.ConsumableItemId
			entry.CeilingCount = ceiling(medal)
		}
		switch {
		case strings.HasPrefix(b.BannerAssetName, model.BannerPrefixStepUp):
			// Each multi-step summon has one banner row; its pool and steps
			// need a medal to be offered.
			if !hasMedal {
				continue
			}
			steps := defaultStepUpSteps
			group := stepUpGroup(b.BannerAssetName)
			if n, ok := stepUpStepCounts[group]; ok {
				steps = n
			}
			entry.GachaModeType = model.GachaModeStepup
			entry.GachaDecorationType = model.GachaDecorationFestival
			entry.PricePhases = buildStepUpPricePhases(gachaId, steps, stepUpPrices[group])
			entry.MaxStepNumber = int32(steps)
		case special != 0:
			entry.PricePhases = buildSpecialPricePhases(gachaId, special)
			if special&specialFree != 0 {
				entry.GachaAutoResetType = model.GachaAutoResetDaily
				entry.GachaAutoResetPeriod = 1
			}
			if strings.HasPrefix(b.BannerAssetName, model.BannerPrefixLimited) {
				entry.GachaDecorationType = model.GachaDecorationFestival
			}
		default:
			// common_* banners are limited summons whose pool and price are
			// not in the client data; chapter summons are built from the drop
			// tables below.
			if !hasMedal {
				continue
			}
			if strings.HasPrefix(b.BannerAssetName, model.BannerPrefixLimited) {
				entry.GachaDecorationType = model.GachaDecorationFestival
			}
			entry.PricePhases = buildPremiumBasicPricePhases(gachaId)
		}
		seen[gachaId] = true
		entries = append(entries, entry)
	}

	entries = append(entries, bannerlessSummons(medals, seen)...)

	chapterTickets, err := LoadChapterTickets()
	if err != nil {
		return nil, nil, fmt.Errorf("load chapter tickets: %w", err)
	}
	for _, t := range chapterTickets {
		entries = append(entries, chapterGachaEntry(t))
	}

	return entries, medalInfoByGacha, nil
}

// GachaBorrowedWindowMillis is how far past now an already-ended schedule
// period is extended when borrowed (COMM-DIAG-REPORT §3: “end 已过的拉到 +∞
// 或 now+365d”; now+365d keeps proto timestamps in a sane range).
const GachaBorrowedWindowMillis int64 = 365 * 24 * 60 * 60 * 1000

// BorrowGachaWindow borrows one schedule window so it covers nowMillis:
//
//   - an end that already passed is extended to now+GachaBorrowedWindowMillis
//     (“end 已过的拉到 +∞”);
//   - a start that lies in the future is pulled back to now (“start 在未来
//     （如 2099）的拉到 now”);
//   - zero bounds stay open and are never touched.
//
// Only the time-window verdict changes; banner construction and the
// portal/recycle exclusion rules are deliberately untouched.
func BorrowGachaWindow(startDatetime, endDatetime, nowMillis int64) (int64, int64) {
	if startDatetime != 0 && startDatetime > nowMillis {
		startDatetime = nowMillis
	}
	if endDatetime != 0 && endDatetime <= nowMillis {
		endDatetime = nowMillis + GachaBorrowedWindowMillis
	}
	return startDatetime, endDatetime
}

// borrowCurrentGachaPeriods keeps, per gachaId, only the chronologically last
// schedule period (“每 gachaId 只出当前逻辑档”) and borrows its window to now.
// Superseded historical periods of the same gachaId are folded away, so every
// consumer of GachaEntries (GetGachaList, GetGacha, Draw, GetMamaBanner) sees
// exactly one active period per gacha.
func borrowCurrentGachaPeriods(entries []store.GachaCatalogEntry, nowMillis int64) []store.GachaCatalogEntry {
	keep := make(map[int32]int, len(entries))
	for i := range entries {
		gachaId := entries[i].GachaId
		if j, ok := keep[gachaId]; !ok || laterGachaPeriod(entries[i], entries[j]) {
			keep[gachaId] = i
		}
	}

	out := make([]store.GachaCatalogEntry, 0, len(keep))
	for i := range entries {
		if keep[entries[i].GachaId] != i {
			continue // superseded period of this gachaId
		}
		entries[i].StartDatetime, entries[i].EndDatetime = BorrowGachaWindow(
			entries[i].StartDatetime, entries[i].EndDatetime, nowMillis)
		out = append(out, entries[i])
	}
	return out
}

// laterGachaPeriod reports whether a's schedule period is chronologically
// later than b's. An open end counts as latest and an open start as earliest.
func laterGachaPeriod(a, b store.GachaCatalogEntry) bool {
	aEnd, bEnd := a.EndDatetime, b.EndDatetime
	if aEnd == 0 {
		aEnd = math.MaxInt64
	}
	if bEnd == 0 {
		bEnd = math.MaxInt64
	}
	if aEnd != bEnd {
		return aEnd > bEnd
	}
	if a.StartDatetime != b.StartDatetime {
		return a.StartDatetime > b.StartDatetime
	}
	return false // identical windows: keep the earlier catalog position
}

const chapterPromoMaxItems = 4
const maxSlideFeatured = 13

func EnrichCatalogPromotions(entries []store.GachaCatalogEntry, pool *GachaCatalog) {
	for i := range entries {
		if entries[i].GachaLabelType == model.GachaLabelChapter {
			entries[i].PromotionItems = buildChapterPromotionItems(pool.Materials)
			continue
		}

		featured := pool.FeaturedByGacha[entries[i].GachaId]

		maxRarity := int32(0)
		for _, c := range featured.Costumes {
			if c.RarityType > maxRarity {
				maxRarity = c.RarityType
			}
		}
		for _, w := range featured.Weapons {
			if w.RarityType > maxRarity {
				maxRarity = w.RarityType
			}
		}

		var topCostumes []GachaPoolItem
		for _, c := range featured.Costumes {
			if c.RarityType == maxRarity {
				topCostumes = append(topCostumes, c)
			}
		}
		var topWeapons []GachaPoolItem
		for _, w := range featured.Weapons {
			if w.RarityType == maxRarity {
				topWeapons = append(topWeapons, w)
			}
		}

		if len(topCostumes)+len(topWeapons) > maxSlideFeatured {
			topCostumes = topCostumes[:min(3, len(topCostumes))]
			topWeapons = topWeapons[:min(2, len(topWeapons))]
		}

		var items []store.GachaPromotionItem
		if entries[i].GachaModeType == model.GachaModeStepup && len(topCostumes) > 0 {
			items = append(items, toPromoItemWithBonus(topCostumes[0], pool))
			wid := pool.CostumeWeaponMap[topCostumes[0].PossessionId]
			items = append(items, toPromoItem(pool.WeaponById[wid]))
		} else {
			for _, c := range topCostumes {
				items = append(items, toPromoItemWithBonus(c, pool))
			}
			for _, w := range topWeapons {
				items = append(items, toPromoItemWithBonus(w, pool))
			}
		}

		entries[i].PromotionItems = items
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].SortOrder != entries[j].SortOrder {
			return entries[i].SortOrder < entries[j].SortOrder
		}
		return entries[i].GachaId < entries[j].GachaId
	})
}

func toPromoItem(item GachaPoolItem) store.GachaPromotionItem {
	return store.GachaPromotionItem{
		PossessionType: item.PossessionType,
		PossessionId:   item.PossessionId,
		IsTarget:       true,
	}
}

func toPromoItemWithBonus(item GachaPoolItem, pool *GachaCatalog) store.GachaPromotionItem {
	pi := store.GachaPromotionItem{
		PossessionType: item.PossessionType,
		PossessionId:   item.PossessionId,
		IsTarget:       true,
	}
	if item.PossessionType == int32(model.PossessionTypeCostume) {
		pi.BonusPossessionType = int32(model.PossessionTypeWeapon)
		pi.BonusPossessionId = pool.CostumeWeaponMap[item.PossessionId]
	}
	return pi
}

func buildChapterPromotionItems(materials []GachaPoolItem) []store.GachaPromotionItem {
	limit := min(chapterPromoMaxItems, len(materials))
	items := make([]store.GachaPromotionItem, 0, limit)
	for _, m := range materials[:limit] {
		items = append(items, toPromoItem(m))
	}
	return items
}

func buildPremiumBasicPricePhases(gachaId int32) []store.GachaPricePhaseEntry {
	return []store.GachaPricePhaseEntry{
		{
			PhaseId:      gachaId*model.PhaseIdMultiplier + 1,
			PriceType:    model.PriceTypeGem,
			Price:        model.PremiumSinglePullPrice,
			RegularPrice: model.PremiumSinglePullPrice,
			DrawCount:    1,
		},
		{
			PhaseId:        gachaId*model.PhaseIdMultiplier + 2,
			PriceType:      model.PriceTypeGem,
			Price:          model.PremiumMultiPullPrice,
			RegularPrice:   model.PremiumMultiPullPrice,
			DrawCount:      model.PremiumMultiPullCount,
			FixedRarityMin: model.RaritySRare,
			FixedCount:     1,
		},
		{
			PhaseId:      gachaId*model.PhaseIdMultiplier + 3,
			PriceType:    model.PriceTypeConsumableItem,
			PriceId:      model.ConsumableIdPremiumTicket,
			Price:        1,
			RegularPrice: 1,
			DrawCount:    1,
		},
	}
}

// buildStepUpPricePhases makes one phase per step at the prices on the step
// pages (stepUpPrices); a summon without them uses the usual pattern.
func buildStepUpPricePhases(gachaId int32, totalSteps int, known []stepUpPrice) []store.GachaPricePhaseEntry {
	prices := known
	if len(prices) != totalSteps {
		usual := []int32{model.StepUpStep1Cost, model.StepUpFreeCost, model.StepUpStep3Cost, model.StepUpFreeCost, model.StepUpStep5Cost}
		prices = nil
		for _, cost := range usual[:min(totalSteps, len(usual))] {
			prices = append(prices, stepUpPrice{Gems: cost, Paid: cost > 0})
		}
	}

	var phases []store.GachaPricePhaseEntry
	for i, p := range prices {
		step := int32(i + 1)
		cost := p.Gems
		priceType := model.PriceTypeGem
		if p.Paid {
			priceType = model.PriceTypePaidGem
		}

		fixedRarityMin := int32(0)
		fixedCount := int32(0)
		if step == int32(len(prices)) {
			fixedRarityMin = model.RaritySSRare
			fixedCount = 1
		}

		phases = append(phases, store.GachaPricePhaseEntry{
			PhaseId:        gachaId*model.PhaseIdMultiplier + step,
			PriceType:      priceType,
			Price:          cost,
			RegularPrice:   model.PremiumMultiPullPrice,
			DrawCount:      model.PremiumMultiPullCount,
			FixedRarityMin: fixedRarityMin,
			FixedCount:     fixedCount,
			LimitExecCount: 1,
			StepNumber:     step,
		})
	}
	return phases
}

// buildSpecialPricePhases prices a free, paid-only or ★4-guaranteed summon
// (see specialSummons).
func buildSpecialPricePhases(gachaId int32, special specialSummon) []store.GachaPricePhaseEntry {
	priceType := model.PriceTypeGem
	if special&specialPaid != 0 {
		priceType = model.PriceTypePaidGem
	}
	if special&(specialFree|specialGuaranteed) == 0 {
		phases := buildPremiumBasicPricePhases(gachaId)[:2] // no ticket draws
		for i := range phases {
			phases[i].PriceType = priceType
		}
		return phases
	}
	phase := store.GachaPricePhaseEntry{
		PhaseId:        gachaId*model.PhaseIdMultiplier + 1,
		PriceType:      priceType,
		Price:          model.PremiumMultiPullPrice,
		RegularPrice:   model.PremiumMultiPullPrice,
		DrawCount:      model.PremiumMultiPullCount,
		FixedRarityMin: model.RaritySRare,
		FixedCount:     1,
		LimitExecCount: 1,
	}
	if special&specialFree != 0 {
		phase.PriceType, phase.Price = model.PriceTypeGem, 0 // once a day
	}
	if special&specialGuaranteed != 0 {
		phase.FixedRarityMin = model.RaritySSRare
	}
	return []store.GachaPricePhaseEntry{phase}
}

func buildChapterPricePhases(gachaId, ticketId int32) []store.GachaPricePhaseEntry {
	return []store.GachaPricePhaseEntry{
		{
			PhaseId:      gachaId*model.PhaseIdMultiplier + 1,
			PriceType:    model.PriceTypeConsumableItem,
			PriceId:      ticketId,
			Price:        1,
			RegularPrice: 1,
			DrawCount:    1,
		},
		{
			PhaseId:      gachaId*model.PhaseIdMultiplier + 2,
			PriceType:    model.PriceTypeConsumableItem,
			PriceId:      ticketId,
			Price:        10,
			RegularPrice: 10,
			DrawCount:    model.PremiumMultiPullCount,
		},
	}
}

// ceiling is the medal count that exchanges for a featured item (m_gacha_medal).
func ceiling(m EntityMGachaMedal) int32 {
	if m.CeilingCount > 0 {
		return m.CeilingCount
	}
	return model.PityCeilingCount
}

// bannerlessSkipped are medal summons without summon art in the client (or,
// 10016, that never ran).
var bannerlessSkipped = map[int32]bool{617: true, 10016: true, 10107: true, 10108: true, 9000040: true}

// bannerlessSummons are summons with a medal but no m_mom_banner row: the
// home-screen carousel never advertised them (the weekly summons, the paid
// multi-step summons, Fortune's Dress and Blessed Dress), but the client has
// their art (ui/gacha/en/<asset>). They ran from their exchange shop's opening
// to two days before their medals convert. Prices are not in the client data:
// weekly summons cost the usual gems, multi-step summons paid gems by step.
func bannerlessSummons(medals []EntityMGachaMedal, seen map[int32]bool) []store.GachaCatalogEntry {
	opens := shopOpensByPrice()
	var out []store.GachaCatalogEntry
	for _, m := range medals {
		gachaId := m.ShopTransitionGachaId
		if gachaId == 0 || seen[gachaId] || bannerlessSkipped[gachaId] {
			continue
		}
		start, ok := opens[m.ConsumableItemId]
		if !ok || m.AutoConvertDatetime == 0 || m.AutoConvertDatetime-rerunEndBeforeConvert <= start {
			continue
		}
		entry := store.GachaCatalogEntry{
			GachaId:               gachaId,
			GachaLabelType:        model.GachaLabelPremium,
			GachaModeType:         model.GachaModeBasic,
			GachaAutoResetType:    model.GachaAutoResetNone,
			IsUserGachaUnlock:     true,
			StartDatetime:         start,
			EndDatetime:           m.AutoConvertDatetime - rerunEndBeforeConvert,
			GachaMedalId:          m.GachaMedalId,
			MedalConsumableItemId: m.ConsumableItemId,
			GachaDecorationType:   model.GachaDecorationFestival,
			BannerAssetName:       fmt.Sprintf("%s%d", model.BannerPrefixLimited, gachaId),
			GroupId:               gachaId,
			CeilingCount:          ceiling(m),
			PricePhases:           buildPremiumBasicPricePhases(gachaId),
			UnlockConditionType:   model.GachaUnlockNone,
		}
		if gachaId >= 500000 {
			entry.GachaModeType = model.GachaModeStepup
			entry.BannerAssetName = fmt.Sprintf("%s%d", model.BannerPrefixStepUp, gachaId)
			group, steps := gachaId/1000, defaultStepUpSteps
			if n, ok := stepUpStepCounts[group]; ok {
				steps = n
			}
			entry.PricePhases = buildStepUpPricePhases(gachaId, steps, stepUpPrices[group])
			entry.MaxStepNumber = int32(steps)
		}
		seen[gachaId] = true
		out = append(out, entry)
	}
	return out
}

// shopOpensByPrice is when the first shop selling for an item opens, by item.
func shopOpensByPrice() map[int32]int64 {
	items, _ := utils.ReadTable[EntityMShopItem]("m_shop_item")
	cells, _ := utils.ReadTable[EntityMShopItemCell]("m_shop_item_cell")
	groups, _ := utils.ReadTable[EntityMShopItemCellGroup]("m_shop_item_cell_group")
	shops, _ := utils.ReadTable[EntityMShop]("m_shop")
	priceOf := make(map[int32]int32, len(items))
	for _, it := range items {
		priceOf[it.ShopItemId] = it.PriceId
	}
	itemOfCell := make(map[int32]int32, len(cells))
	for _, c := range cells {
		itemOfCell[c.ShopItemCellId] = c.ShopItemId
	}
	pricesOfGroup := make(map[int32]map[int32]bool)
	for _, g := range groups {
		if pricesOfGroup[g.ShopItemCellGroupId] == nil {
			pricesOfGroup[g.ShopItemCellGroupId] = map[int32]bool{}
		}
		pricesOfGroup[g.ShopItemCellGroupId][priceOf[itemOfCell[g.ShopItemCellId]]] = true
	}
	out := make(map[int32]int64)
	for _, sh := range shops {
		for price := range pricesOfGroup[sh.ShopItemCellGroupId] {
			if cur, ok := out[price]; !ok || sh.StartDatetime < cur {
				out[price] = sh.StartDatetime
			}
		}
	}
	return out
}
