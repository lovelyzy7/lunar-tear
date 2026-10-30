package gacha

import (
	"fmt"
	"log"
	"math/rand"

	"lunar-tear/server/internal/gametime"
	"lunar-tear/server/internal/masterdata"
	"lunar-tear/server/internal/model"
	"lunar-tear/server/internal/store"
)

type DrawResult struct {
	Items               []DrawnItem
	BonusItems          map[int]DrawnItem
	Bonuses             []store.GachaBonusEntry
	DuplicateInfos      []DuplicateInfo
	BonusDuplicateInfos []DuplicateInfo
	MedalBonus          int32
}

type DuplicateInfo struct {
	Index   int
	Grade   int32
	Bonuses []model.DupExchangeEntry
}

type GachaHandler struct {
	Pool        *masterdata.GachaCatalog
	Config      *masterdata.GameConfig
	Granter     *store.PossessionGranter
	MedalInfo   map[int32]masterdata.GachaMedalInfo
	DupExchange map[int32][]model.DupExchangeEntry
}

func NewGachaHandler(
	pool *masterdata.GachaCatalog,
	config *masterdata.GameConfig,
	granter *store.PossessionGranter,
	medalInfo map[int32]masterdata.GachaMedalInfo,
	dupExchange map[int32][]model.DupExchangeEntry,
) *GachaHandler {
	return &GachaHandler{
		Pool:        pool,
		Config:      config,
		Granter:     granter,
		MedalInfo:   medalInfo,
		DupExchange: dupExchange,
	}
}

func (h *GachaHandler) HandleDraw(
	user *store.UserState,
	entry store.GachaCatalogEntry,
	phaseId int32,
	execCount int32,
) (*DrawResult, error) {
	phase, err := findPhase(entry, phaseId)
	if err != nil {
		return nil, err
	}

	// A multi-step summon draws only its current step.
	if entry.GachaModeType == model.GachaModeStepup {
		if step := CurrentStep(entry, user.Gacha.BannerStates[entry.GachaId]); phase.StepNumber != step {
			return nil, fmt.Errorf("gacha %d: step %d drawn on step %d", entry.GachaId, phase.StepNumber, step)
		}
	}

	// Options with a limit (free once a day, one-time guaranteed). Multi-step
	// summons limit each step through their step number instead.
	limited := phase.LimitExecCount > 0 && entry.GachaModeType != model.GachaModeStepup
	if limited {
		used := ExecCount(user, entry, phase.PhaseId)
		if used+execCount > phase.LimitExecCount {
			return nil, fmt.Errorf("gacha %d phase %d: limit of %d reached", entry.GachaId, phase.PhaseId, phase.LimitExecCount)
		}
	}

	if err := checkBoxDraw(user, entry, int(phase.DrawCount*execCount), gametime.NowMillis()); err != nil {
		return nil, err
	}

	// DeductPrice checks the balance before it changes anything, so a failed
	// payment leaves the user as it was.
	totalCost := phase.Price * execCount
	if totalCost > 0 {
		if err := store.DeductPrice(user, phase.PriceType, phase.PriceId, totalCost); err != nil {
			return nil, fmt.Errorf("gacha %d phase %d: %w", entry.GachaId, phase.PhaseId, err)
		}
	}

	drawCount := int(phase.DrawCount * execCount)
	nowMillis := gametime.NowMillis()

	bs := user.Gacha.BannerStates[entry.GachaId]
	bs.GachaId = entry.GachaId

	var items []DrawnItem

	switch entry.GachaLabelType {
	case model.GachaLabelPremium:
		items = h.drawPremium(entry, phase, drawCount)
	case model.GachaLabelChapter, model.GachaLabelRecycle:
		if len(entry.BoxContents) > 0 {
			items = drawFixedBox(user, entry, &bs, drawCount, nowMillis)
		} else {
			items = h.drawMaterial(drawCount)
		}
	case model.GachaLabelEvent:
		items = h.drawBox(&bs, drawCount)
	default:
		items = h.drawPremium(entry, phase, drawCount)
	}

	if entry.GachaModeType == model.GachaModeStepup {
		bs.StepNumber = CurrentStep(entry, bs) + 1
		if bs.StepNumber > entry.MaxStepNumber {
			bs.StepNumber = 1
			bs.LoopCount++
		}
	}

	var medalBonus int32
	if entry.GachaMedalId != 0 {
		medalBonus = int32(drawCount)
		bs.MedalCount = min(bs.MedalCount+medalBonus, h.medalCountCap())
	}

	bs.DrawCount += int32(drawCount)
	user.Gacha.BannerStates[entry.GachaId] = bs
	if limited {
		if user.Ext.GachaExec == nil {
			user.Ext.GachaExec = map[int32]store.GachaExecState{}
		}
		user.Ext.GachaExec[phase.PhaseId] = store.GachaExecState{
			Count: ExecCount(user, entry, phase.PhaseId) + execCount, Day: gametime.StartOfDay(nowMillis),
		}
	}

	dupInfos := h.grantItems(user, items, nowMillis)

	bonusMap := h.generateBonusItems(entry, items)
	bonusSlice := make([]DrawnItem, 0, len(bonusMap))
	for _, b := range bonusMap {
		bonusSlice = append(bonusSlice, b)
	}
	bonusDupInfos := h.grantItems(user, bonusSlice, nowMillis)

	result := &DrawResult{
		Items:               items,
		BonusItems:          bonusMap,
		DuplicateInfos:      dupInfos,
		BonusDuplicateInfos: bonusDupInfos,
		MedalBonus:          medalBonus,
	}

	for _, p := range phase.Bonuses {
		h.Granter.GrantCapped(user, model.PossessionType(p.PossessionType), p.PossessionId, p.Count, nowMillis, store.GiftTextSummons)
		result.Bonuses = append(result.Bonuses, p)
	}

	if medalBonus > 0 && entry.MedalConsumableItemId != 0 {
		h.Granter.GrantCapped(user, model.PossessionTypeConsumableItem, entry.MedalConsumableItemId, medalBonus, nowMillis, store.GiftTextSummons)
	}

	return result, nil
}

// medalCountCap is MConfig POSSESSION_COUNT_LIMIT_GACHA_MEDAL.
func (h *GachaHandler) medalCountCap() int32 {
	if h.Config != nil && h.Config.PossessionCountLimitGachaMedal > 0 {
		return h.Config.PossessionCountLimitGachaMedal
	}
	return model.MedalCountCap
}

func (h *GachaHandler) HandleResetBox(
	user *store.UserState,
	entry store.GachaCatalogEntry,
) error {
	if len(entry.BoxContents) > 0 && !canResetBox(user, entry, gametime.NowMillis()) {
		return fmt.Errorf("gacha %d: the box can be reset once its targets are drawn", entry.GachaId)
	}
	bs := user.Gacha.BannerStates[entry.GachaId]
	bs.BoxDrewCounts = make(map[int32]int32)
	if bs.BoxNumber < 1 {
		bs.BoxNumber = 1
	}
	bs.BoxNumber++
	user.Gacha.BannerStates[entry.GachaId] = bs
	return nil
}

func clampDailyDraw(lastDate, todayStart int64, currentCount, maxCount, requested int32) (clamped, newCount int32, reset bool) {
	if lastDate < todayStart {
		currentCount = 0
		reset = true
	}
	remaining := maxCount - currentCount
	if remaining <= 0 {
		return 0, currentCount, reset
	}
	if requested > remaining {
		requested = remaining
	}
	return requested, currentCount + requested, reset
}

// HandleRewardDraw spins the Special Gift Roulette count times (within the
// daily limit) and returns the free gems of each spin.
func (h *GachaHandler) HandleRewardDraw(
	user *store.UserState,
	count int32,
) ([]int32, error) {
	nowMillis := gametime.NowMillis()
	todayStart := gametime.StartOfDayMillis()

	maxCount := h.Config.RewardGachaDailyMaxCount
	if maxCount <= 0 {
		maxCount = model.DefaultDailyDrawLimit
	}

	clamped, newCount, _ := clampDailyDraw(
		user.Gacha.LastRewardDrawDate, todayStart,
		user.Gacha.TodaysCurrentDrawCount, maxCount, count,
	)
	if clamped <= 0 {
		return nil, fmt.Errorf("daily reward draw limit reached")
	}

	gems := make([]int32, 0, clamped)
	for range clamped {
		won := DrawRoulette(CurrentRouletteRates(), rand.Intn)
		store.GrantPossession(user, model.PossessionTypeFreeGem, 0, won)
		gems = append(gems, won)
	}

	user.Gacha.TodaysCurrentDrawCount = newCount
	user.Gacha.DailyMaxCount = maxCount
	user.Gacha.LastRewardDrawDate = nowMillis
	user.Gacha.RewardAvailable = newCount < maxCount

	return gems, nil
}

func (h *GachaHandler) drawPremium(entry store.GachaCatalogEntry, phase store.GachaPricePhaseEntry, count int) []DrawnItem {
	cfg := Rates()
	fixedMin := phase.FixedRarityMin
	fixedCount := int(phase.FixedCount)
	if fixedCount > 0 && fixedMin < cfg.GuaranteedRarity() {
		fixedMin = cfg.GuaranteedRarity()
	}

	bp := h.Pool.BannerPools[entry.GachaId]
	if bp == nil {
		bp = &masterdata.BannerPool{
			CostumesByRarity: h.Pool.CostumesByRarity,
			WeaponsByRarity:  h.Pool.WeaponsByRarity,
		}
	}

	rateMultiplier := 1.0
	if entry.GachaModeType == model.GachaModeStepup && cfg.StepUpBoost {
		switch phase.StepNumber {
		case 1, 3:
			rateMultiplier = model.StepUpRateBoost
		case 5:
			rateMultiplier = model.StepUpRateMaxBoost
		}
	}

	return DrawPremium(bp, count, fixedMin, fixedCount, rateMultiplier)
}

func (h *GachaHandler) drawMaterial(count int) []DrawnItem {
	return DrawReward(h.Pool.Materials, count)
}

func (h *GachaHandler) drawBox(bs *store.GachaBannerState, count int) []DrawnItem {
	if bs.BoxDrewCounts == nil {
		bs.BoxDrewCounts = make(map[int32]int32)
	}

	boxItems := h.buildBoxPool()
	for i := range boxItems {
		boxItems[i].DrewCount = bs.BoxDrewCounts[boxItems[i].PossessionId]
	}

	result := DrawBox(boxItems, count)

	for _, item := range result {
		bs.BoxDrewCounts[item.PossessionId]++
	}

	return result
}

func (h *GachaHandler) buildBoxPool() []BoxItem {
	var items []BoxItem
	for _, mat := range h.Pool.Materials {
		items = append(items, BoxItem{
			PossessionType: mat.PossessionType,
			PossessionId:   mat.PossessionId,
			RarityType:     mat.RarityType,
			Count:          1,
			MaxCount:       model.BoxItemDefaultMax,
		})
		if len(items) >= model.BoxPoolMaxItems {
			break
		}
	}
	if len(items) < model.BoxPoolMinItems {
		items = append(items, BoxItem{
			PossessionType: int32(model.PossessionTypeMaterial),
			PossessionId:   model.BoxFallbackItemId,
			RarityType:     model.RarityNormal,
			Count:          1,
			MaxCount:       model.BoxFallbackItemMax,
		})
	}
	return items
}

func (h *GachaHandler) grantItems(user *store.UserState, items []DrawnItem, nowMillis int64) []DuplicateInfo {
	var dupInfos []DuplicateInfo
	for i, item := range items {
		switch model.PossessionType(item.PossessionType) {
		case model.PossessionTypeCostume:
			if dup, ok := h.tryCostumeDupExchange(user, item, i, nowMillis); ok {
				dupInfos = append(dupInfos, dup)
				continue
			}
			h.Granter.GrantCostume(user, item.PossessionId, nowMillis)
		case model.PossessionTypeWeapon:
			h.Granter.GrantWeaponCapped(user, item.PossessionId, nowMillis, store.GiftTextSummons)
		default:
			if item.PossessionType != 0 {
				h.Granter.GrantCapped(user, model.PossessionType(item.PossessionType), item.PossessionId, 1, nowMillis, store.GiftTextSummons)
			}
		}
	}
	return dupInfos
}

func (h *GachaHandler) tryCostumeDupExchange(user *store.UserState, item DrawnItem, index int, nowMillis int64) (DuplicateInfo, bool) {
	for _, c := range user.Costumes {
		if c.CostumeId == item.PossessionId {
			grade := int32(rand.Intn(model.DupGradeRange) + int(model.DupGradeMin))
			exchanges := h.DupExchange[item.PossessionId]
			for _, ex := range exchanges {
				h.Granter.GrantCapped(user, model.PossessionType(ex.PossessionType), ex.PossessionId, ex.Count, nowMillis, store.GiftTextSummons)
			}
			return DuplicateInfo{Index: index, Grade: grade, Bonuses: exchanges}, true
		}
	}
	return DuplicateInfo{}, false
}

func (h *GachaHandler) generateBonusItems(entry store.GachaCatalogEntry, mainItems []DrawnItem) map[int]DrawnItem {
	bonus := make(map[int]DrawnItem)
	for i, item := range mainItems {
		if item.PossessionType != int32(model.PossessionTypeCostume) {
			continue
		}
		wid, ok := h.Pool.CostumeWeaponMap[item.PossessionId]
		if !ok {
			continue
		}
		w, ok := h.Pool.WeaponById[wid]
		if !ok {
			continue
		}
		bonus[i] = DrawnItem{
			PossessionType: w.PossessionType,
			PossessionId:   w.PossessionId,
			RarityType:     w.RarityType,
		}
	}
	return bonus
}

func findPhase(entry store.GachaCatalogEntry, phaseId int32) (store.GachaPricePhaseEntry, error) {
	for _, p := range entry.PricePhases {
		if p.PhaseId == phaseId {
			return p, nil
		}
	}
	if len(entry.PricePhases) > 0 {
		log.Printf("[GachaHandler] phase %d not found for gacha %d, using first phase", phaseId, entry.GachaId)
		return entry.PricePhases[0], nil
	}
	return store.GachaPricePhaseEntry{}, fmt.Errorf("no price phases for gacha %d", entry.GachaId)
}

// ExecCount is how many times the user has drawn a limited price option; a
// daily summon's count is for the current game day only.
func ExecCount(user *store.UserState, entry store.GachaCatalogEntry, phaseId int32) int32 {
	st := user.Ext.GachaExec[phaseId]
	if entry.GachaAutoResetType == model.GachaAutoResetDaily && st.Day != gametime.StartOfDay(gametime.NowMillis()) {
		return 0
	}
	return st.Count
}
