package questflow

import (
	"errors"
	"fmt"
	"log"

	"lunar-tear/server/internal/gametime"
	"lunar-tear/server/internal/masterdata"
	"lunar-tear/server/internal/model"
	"lunar-tear/server/internal/store"
)

// addDailyClears counts n clears of a quest at nowMillis. The game shows a
// daily-limited quest's remaining clears as its limit less DailyClearCount
// when LastClearDatetime is today (GMT-8), so the count starts over on a new
// game day.
func addDailyClears(q *store.UserQuestState, n int32, nowMillis int64) {
	if q.LastClearDatetime < gametime.StartOfDay(nowMillis) {
		q.DailyClearCount = 0
	}
	q.DailyClearCount += n
	q.LastClearDatetime = nowMillis
}

func (h *QuestHandler) initQuestState(user *store.UserState, questId int32) {
	quest := user.Quests[questId]
	quest.QuestId = questId
	user.Quests[questId] = quest

	for _, missionId := range h.MissionIdsByQuestId[questId] {
		key := store.QuestMissionKey{QuestId: questId, QuestMissionId: missionId}
		mission := user.QuestMissions[key]
		mission.QuestId = questId
		mission.QuestMissionId = missionId
		user.QuestMissions[key] = mission
	}
}

func isMainQuestPlayable(quest masterdata.EntityMQuest) bool {
	if quest.IsRunInTheBackground {
		// A background quest is still actively played — and must NOT be
		// auto-cleared on start — when it carries battle content (a non-zero
		// recommended deck power, e.g. quests 500/515/30515). Pure cutscene
		// background quests have RecommendedDeckPower == 0.
		return quest.RecommendedDeckPower > 0
	}
	return quest.IsCountedAsQuest
}

func (h *QuestHandler) clearQuestMissions(user *store.UserState, questId int32, nowMillis int64) {
	for _, missionId := range h.MissionIdsByQuestId[questId] {
		key := store.QuestMissionKey{QuestId: questId, QuestMissionId: missionId}
		mission := user.QuestMissions[key]
		mission.IsClear = true
		mission.ProgressValue = 1
		mission.LatestClearDatetime = nowMillis
		user.QuestMissions[key] = mission
	}
}

func (h *QuestHandler) HandleQuestStart(user *store.UserState, questId int32, isBattleOnly, isMainFlow bool, userDeckNumber int32, nowMillis int64) {
	h.handleQuestStartInternal(user, questId, isBattleOnly, isMainFlow, userDeckNumber, false, nowMillis)
}

func (h *QuestHandler) HandleQuestStartReplay(user *store.UserState, questId int32, isBattleOnly bool, userDeckNumber int32, nowMillis int64) {
	h.handleQuestStartInternal(user, questId, isBattleOnly, false, userDeckNumber, true, nowMillis)
}

func (h *QuestHandler) handleQuestStartInternal(user *store.UserState, questId int32, isBattleOnly, isMainFlow bool, userDeckNumber int32, isReplayFlow bool, nowMillis int64) {
	quest, ok := h.QuestById[questId]
	if !ok {
		panic(fmt.Sprintf("unknown questId=%d for HandleQuestStart", questId))
	}

	h.initQuestState(user, questId)
	ResetBattleStats(user)
	if quest.Stamina > 0 {
		stamina := h.staminaWithCampaign(user, quest.Stamina, h.targetForMain(questId), nowMillis)
		store.ConsumeStamina(user, stamina, h.MaxStaminaByLevel[user.Status.Level]*1000, nowMillis)
	}

	questState := user.Quests[questId]
	questState.IsBattleOnly = isBattleOnly
	questState.UserDeckNumber = userDeckNumber

	isCleared := questState.QuestStateType == model.UserQuestStateTypeCleared
	isMenuPick := !isReplayFlow && !isMainFlow

	switch {
	case isMenuPick:
		snapshotMainQuestIfNeeded(user)
		sceneId := h.menuPickSceneId(questId, isBattleOnly)
		user.MainQuest.ProgressQuestSceneId = sceneId
		user.MainQuest.ProgressHeadQuestSceneId = sceneId
		user.MainQuest.ProgressQuestFlowType = int32(model.QuestFlowTypeMainFlow)
		user.PortalCageStatus.IsCurrentProgress = false
		user.PortalCageStatus.LatestVersion = nowMillis
		user.SideStoryActiveProgress = store.SideStoryActiveProgress{LatestVersion: nowMillis}
		user.MainQuest.LatestVersion = nowMillis
		log.Printf("[HandleQuestStart] QuestMenuPick quest=%d isBattleOnly=%v scene=%d cleared=%v",
			questId, isBattleOnly, sceneId, isCleared)
		if isCleared {
			questState.LatestStartDatetime = nowMillis
			user.Quests[questId] = questState
			return
		}

	case isReplayFlow:
		h.applyReplayStart(user, quest, questId, isBattleOnly, nowMillis)
		return
	}

	if isCleared {
		questState.QuestStateType = model.UserQuestStateTypeActive
		questState.LatestStartDatetime = nowMillis
		user.Quests[questId] = questState
		return
	}

	if isMainQuestPlayable(quest) {
		user.MainQuest.CurrentQuestFlowType = int32(model.QuestFlowTypeMainFlow)
		questState.QuestStateType = model.UserQuestStateTypeActive
		questState.LatestStartDatetime = nowMillis
	} else {
		questState.QuestStateType = model.UserQuestStateTypeCleared
		questState.ClearCount = 1
		addDailyClears(&questState, 1, nowMillis)
		if sceneIds := h.SceneIdsByQuestId[questId]; len(sceneIds) > 0 {
			firstSceneId := sceneIds[0]
			prevSceneId := user.MainQuest.CurrentQuestSceneId
			h.advanceMainFlowScene(user, questId, firstSceneId)
			user.MainQuest.CurrentQuestFlowType = int32(model.QuestFlowTypeMainFlow)
			log.Printf("[HandleQuestStart] background quest %d auto-cleared, scene %d -> %d", questId, prevSceneId, firstSceneId)
		}
	}
	user.Quests[questId] = questState
}

func snapshotMainQuestIfNeeded(user *store.UserState) {
	if user.MainQuest.SavedContext.Active {
		return
	}
	user.MainQuest.SavedContext = store.SavedQuestContext{
		Active:                  true,
		CurrentQuestSceneId:     user.MainQuest.CurrentQuestSceneId,
		HeadQuestSceneId:        user.MainQuest.HeadQuestSceneId,
		CurrentMainQuestRouteId: user.MainQuest.CurrentMainQuestRouteId,
		MainQuestSeasonId:       user.MainQuest.MainQuestSeasonId,
		IsReachedLastQuestScene: user.MainQuest.IsReachedLastQuestScene,
		PortalCageInProgress:    user.PortalCageStatus.IsCurrentProgress,
		CurrentQuestFlowType:    user.MainQuest.CurrentQuestFlowType,
	}
}

func (h *QuestHandler) applyReplayStart(user *store.UserState, quest masterdata.EntityMQuest, questId int32, isBattleOnly bool, nowMillis int64) {
	flowType := h.replayFlowTypeFromQuestId(user, questId)
	if model.IsReplayQuestFlowType(user.MainQuest.CurrentQuestFlowType) {
		flowType = model.QuestFlowType(user.MainQuest.CurrentQuestFlowType)
	}
	user.MainQuest.CurrentQuestFlowType = int32(flowType)
	user.MainQuest.LatestVersion = nowMillis

	questState := user.Quests[questId]
	questState.LatestStartDatetime = nowMillis

	if isMainQuestPlayable(quest) {
		questState.QuestStateType = model.UserQuestStateTypeActive
		user.Quests[questId] = questState
	} else {
		if questState.QuestStateType != model.UserQuestStateTypeCleared {
			questState.QuestStateType = model.UserQuestStateTypeCleared
			questState.ClearCount++
			addDailyClears(&questState, 1, nowMillis)
		}
		user.Quests[questId] = questState
		if sceneIds := h.SceneIdsByQuestId[questId]; len(sceneIds) > 0 {
			h.advanceReplayFlowScene(user, sceneIds[0])
		}
	}

	log.Printf("[HandleQuestStart] replay quest=%d flowType=%s isBattleOnly=%v playable=%v current=%d head=%d",
		questId, flowType, isBattleOnly, isMainQuestPlayable(quest),
		user.MainQuest.ReplayFlowCurrentQuestSceneId,
		user.MainQuest.ReplayFlowHeadQuestSceneId)
}

func (h *QuestHandler) menuPickSceneId(questId int32, isBattleOnly bool) int32 {
	if isBattleOnly {
		if v, ok := h.BattleOnlyTargetSceneIdFor(questId); ok {
			return v
		}
	}
	if scenes := h.SceneIdsByQuestId[questId]; len(scenes) > 0 {
		return scenes[0]
	}
	return 0
}

func (h *QuestHandler) applyQuestVictory(user *store.UserState, questId int32, outcome *FinishOutcome, nowMillis int64, wasReplay bool) {
	questState := user.Quests[questId]
	h.applyExpAndGoldRewards(user, questId, nowMillis)
	if !questState.IsRewardGranted {
		if !wasReplay {
			h.applyFirstClearItemRewards(user, questId, nowMillis)
			outcome.ChangedWeaponStoryIds = append(outcome.ChangedWeaponStoryIds,
				h.grantWeaponStoryUnlocksForQuestScene(user, questId, model.QuestResultTypeHalfResult, nowMillis)...)
			outcome.ChangedWeaponStoryIds = append(outcome.ChangedWeaponStoryIds,
				h.grantWeaponStoryUnlocksForQuestScene(user, questId, model.QuestResultTypeFullResult, nowMillis)...)
		}
		questState.IsRewardGranted = true
	}
	// Missions pay when they clear, on the first clear or any later win.
	for _, r := range outcome.MissionClearRewards {
		h.applyRewardPossession(user, r.PossessionType, r.PossessionId, r.Count, nowMillis)
	}
	for _, r := range outcome.MissionClearCompleteRewards {
		h.applyRewardPossession(user, r.PossessionType, r.PossessionId, r.Count, nowMillis)
	}
	h.markMissionsCleared(user, questId, outcome.clearedMissionIds, nowMillis)
	raritySet, rankSet := parseAutoSaleRules(user.AutoSaleSettings)
	h.grantDropRewards(user, outcome.DropRewards, raritySet, rankSet, nowMillis)
	if deck, ok := battleDeck(user, questId); ok {
		h.grantQuestBonusDrops(user, h.QuestById[questId], deck, 1, nowMillis)
	}
	for _, reward := range outcome.ReplayFlowFirstClearRewards {
		h.applyRewardPossession(user, reward.PossessionType, reward.PossessionId, reward.Count, nowMillis)
	}
	questState.QuestStateType = model.UserQuestStateTypeCleared
	questState.ClearCount++
	addDailyClears(&questState, 1, nowMillis)
	questState.IsBattleOnly = false
	user.Quests[questId] = questState
}

func (h *QuestHandler) finalizeChainPreviousQuest(user *store.UserState, questId int32, nowMillis int64) {
	if _, ok := h.QuestById[questId]; !ok {
		return
	}
	h.initQuestState(user, questId)
	questState := user.Quests[questId]
	if questState.QuestStateType == model.UserQuestStateTypeCleared {
		return
	}
	if !questState.IsRewardGranted {
		h.applyQuestRewards(user, questId, nowMillis)
		questState.IsRewardGranted = true
	}
	questState.QuestStateType = model.UserQuestStateTypeCleared
	questState.ClearCount++
	addDailyClears(&questState, 1, nowMillis)
	questState.IsBattleOnly = false
	user.Quests[questId] = questState
	h.clearQuestMissions(user, questId, nowMillis)
	log.Printf("[HandleMainQuestSceneProgress] finalized chain-previous quest %d (cleared)", questId)
}

// settleLostQuestState sets a quest's state after a retire or wipe: Cleared
// if it was ever cleared, else Challenged (4). The client's
// CalculatorQuest.IsChallengeQuest counts both as challenged, which the
// QUEST_CHALLENGE release condition reads (quest 385 opens 382-387).
func settleLostQuestState(user *store.UserState, questId int32) {
	qs := user.Quests[questId]
	if qs.ClearCount > 0 {
		qs.QuestStateType = model.UserQuestStateTypeCleared
	} else {
		qs.QuestStateType = model.UserQuestStateTypeChallenged
	}
	qs.IsBattleOnly = false
	user.Quests[questId] = qs
}

func (h *QuestHandler) HandleQuestFinish(user *store.UserState, questId int32, isRetired, isAnnihilated bool, nowMillis int64) FinishOutcome {
	quest, ok := h.QuestById[questId]
	if !ok {
		panic(fmt.Sprintf("unknown questId=%d for HandleQuestFinish", questId))
	}

	h.initQuestState(user, questId)

	wasReplay := model.IsReplayQuestFlowType(user.MainQuest.CurrentQuestFlowType)
	wasMenuReplay := user.MainQuest.SavedContext.Active

	var outcome FinishOutcome
	if !isRetired && !isAnnihilated {
		outcome = h.evaluateFinishOutcome(user, questId, h.targetForMain(questId), wasReplay, nowMillis)
		h.applyQuestVictory(user, questId, &outcome, nowMillis, wasReplay)

		// A replay-flow finish must NOT move the MainFlow scene pointer: the
		// finished quest is a replay-variant (30000+) with no chapter, so a
		// replay scene left in CurrentQuestSceneId makes the client world map's
		// CalculatorWorldMap.GetCurrentSeasonId resolve chapter 0 and NRE. The
		// replay's own position is tracked in ReplayFlowCurrentQuestSceneId.
		if isMainQuestPlayable(quest) && !wasMenuReplay && !wasReplay {
			lastSceneId := h.getLastMainFlowSceneId(questId)
			h.advanceMainFlowScene(user, questId, lastSceneId)
		}
	}

	consumed := h.staminaWithCampaign(user, quest.Stamina, h.targetForMain(questId), nowMillis)
	if isRetired && !isAnnihilated && consumed > 1 {
		refund := consumed - 1
		maxMillis := h.MaxStaminaByLevel[user.Status.Level] * 1000
		store.RecoverStamina(user, refund*1000, maxMillis, nowMillis)
	}

	if isRetired || isAnnihilated {
		settleLostQuestState(user, questId)
	}

	user.MainQuest.ProgressQuestSceneId = 0
	user.MainQuest.ProgressHeadQuestSceneId = 0
	if !wasReplay {
		// Keep replay flow types on replay finish so the client's
		// Story.ApplyNewestPlayingScene keeps _isReplayed=true (popup result UI).
		user.MainQuest.ProgressQuestFlowType = 0
		user.MainQuest.CurrentQuestFlowType = int32(model.QuestFlowTypeUnknown)
	}

	if wasMenuReplay {
		ctx := user.MainQuest.SavedContext
		user.MainQuest.CurrentQuestSceneId = ctx.CurrentQuestSceneId
		user.MainQuest.HeadQuestSceneId = ctx.HeadQuestSceneId
		user.MainQuest.CurrentMainQuestRouteId = ctx.CurrentMainQuestRouteId
		user.MainQuest.MainQuestSeasonId = ctx.MainQuestSeasonId
		user.MainQuest.IsReachedLastQuestScene = ctx.IsReachedLastQuestScene
		user.MainQuest.CurrentQuestFlowType = ctx.CurrentQuestFlowType
		user.PortalCageStatus.IsCurrentProgress = ctx.PortalCageInProgress
		user.PortalCageStatus.LatestVersion = nowMillis
		user.MainQuest.SavedContext = store.SavedQuestContext{}
		user.MainQuest.LatestVersion = nowMillis
		log.Printf("[HandleQuestFinish] restored snapshot for quest %d (route=%d season=%d scene=%d head=%d cage=%v flow=%d)",
			questId, ctx.CurrentMainQuestRouteId, ctx.MainQuestSeasonId,
			ctx.CurrentQuestSceneId, ctx.HeadQuestSceneId, ctx.PortalCageInProgress, ctx.CurrentQuestFlowType)
	}

	return outcome
}

// ErrSkipRefused is returned (wrapped) when a skip is not allowed; nothing
// has been changed.
var ErrSkipRefused = errors.New("quest skip refused")

// HandleQuestSkip clears a quest skipCount times with skip tickets. It
// refuses, changing nothing, when the count is outside 1..QUEST_SKIP_MAX_COUNT_AT_ONCE
// or the tickets or stamina are short. deckNumber is the quest deck whose
// quest bonuses apply.
func (h *QuestHandler) HandleQuestSkip(user *store.UserState, questId, skipCount, deckNumber int32, nowMillis int64) (FinishOutcome, error) {
	questDef, ok := h.QuestById[questId]
	if !ok {
		return FinishOutcome{}, fmt.Errorf("%w: unknown questId=%d", ErrSkipRefused, questId)
	}

	target := h.targetForMain(questId)
	maxMillis := h.MaxStaminaByLevel[user.Status.Level] * 1000
	perSkipStamina := h.staminaWithCampaign(user, questDef.Stamina, target, nowMillis)
	skipTicketId := h.Config.ConsumableItemIdForQuestSkipTicket
	if err := h.checkSkip(user, skipCount, perSkipStamina, skipTicketId, maxMillis, nowMillis); err != nil {
		return FinishOutcome{}, fmt.Errorf("quest %d: %w", questId, err)
	}

	store.ConsumeStamina(user, perSkipStamina*skipCount, maxMillis, nowMillis)
	user.ConsumableItems[skipTicketId] -= skipCount

	raritySet, rankSet := parseAutoSaleRules(user.AutoSaleSettings)
	var allDrops []RewardGrant
	for range skipCount {
		drops := h.computeDropRewards(user, questDef, target, nowMillis)
		h.grantDropRewards(user, drops, raritySet, rankSet, nowMillis)
		allDrops = append(allDrops, drops...)

		if questDef.Gold != 0 {
			user.ConsumableItems[h.Config.ConsumableItemIdForGold] += questDef.Gold
		}
		h.applyExpRewards(user, questId, nowMillis)
	}
	if deck, ok := user.Decks[store.DeckKey{DeckType: model.DeckTypeQuest, UserDeckNumber: deckNumber}]; ok {
		h.grantQuestBonusDrops(user, questDef, deck, skipCount, nowMillis)
	}

	questState := user.Quests[questId]
	questState.ClearCount += skipCount
	addDailyClears(&questState, skipCount, nowMillis)
	user.Quests[questId] = questState

	log.Printf("[HandleQuestSkip] questId=%d skipCount=%d drops=%d gold=%d", questId, skipCount, len(allDrops), questDef.Gold*skipCount)
	return FinishOutcome{DropRewards: allDrops}, nil
}

// checkSkip refuses a skip the player cannot pay for (settled stamina is
// compared, as ConsumeStamina settles before it spends).
func (h *QuestHandler) checkSkip(user *store.UserState, skipCount, perSkipStamina, skipTicketId, maxMillis int32, nowMillis int64) error {
	limit := int32(100)
	if h.Config != nil && h.Config.QuestSkipMaxCountAtOnce > 0 {
		limit = h.Config.QuestSkipMaxCountAtOnce
	}
	if skipCount <= 0 || skipCount > limit {
		return fmt.Errorf("%w: skipCount %d outside 1..%d", ErrSkipRefused, skipCount, limit)
	}
	if have := user.ConsumableItems[skipTicketId]; have < skipCount {
		return fmt.Errorf("%w: %d skip tickets, %d needed", ErrSkipRefused, have, skipCount)
	}
	probe := *user
	store.SettleStamina(&probe, maxMillis, nowMillis)
	if need := int64(perSkipStamina) * int64(skipCount) * 1000; int64(probe.Status.StaminaMilliValue) < need {
		return fmt.Errorf("%w: stamina %d/1000, %d needed", ErrSkipRefused, probe.Status.StaminaMilliValue, need/1000)
	}
	return nil
}

func (h *QuestHandler) HandleQuestRestart(user *store.UserState, questId int32, nowMillis int64) {
	questDef, ok := h.QuestById[questId]
	// Only seed CurrentQuestFlowType when it's not already set (initial
	// natural progression). Don't clobber an in-flight ReplayFlow (Map Play
	// resume).
	if ok && isMainQuestPlayable(questDef) && user.MainQuest.CurrentQuestFlowType == 0 {
		user.MainQuest.CurrentQuestFlowType = int32(model.QuestFlowTypeMainFlow)
	}

	quest := user.Quests[questId]
	quest.QuestId = questId
	quest.QuestStateType = model.UserQuestStateTypeActive
	quest.LatestStartDatetime = nowMillis
	user.Quests[questId] = quest

	// Cleared missions stay cleared: a restart resumes the same run.
	if ok {
		h.initQuestState(user, questId)
	}
}
