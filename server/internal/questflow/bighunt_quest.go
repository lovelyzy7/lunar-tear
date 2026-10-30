package questflow

import (
	"fmt"

	"lunar-tear/server/internal/model"
	"lunar-tear/server/internal/store"
)

func (h *QuestHandler) HandleBigHuntQuestStart(user *store.UserState, questId, userDeckNumber int32, nowMillis int64) {
	quest, ok := h.QuestById[questId]
	if !ok {
		panic(fmt.Sprintf("unknown questId=%d for HandleBigHuntQuestStart", questId))
	}

	h.initQuestState(user, questId)
	ResetBattleStats(user)

	if quest.Stamina > 0 {
		maxMillis := h.MaxStaminaByLevel[user.Status.Level] * 1000
		stamina := h.staminaWithCampaign(user, quest.Stamina, h.targetForBigHunt(questId), nowMillis)
		store.ConsumeStamina(user, stamina, maxMillis, nowMillis)
	}

	questState := user.Quests[questId]
	questState.UserDeckNumber = userDeckNumber
	questState.QuestStateType = model.UserQuestStateTypeActive
	questState.LatestStartDatetime = nowMillis
	user.Quests[questId] = questState
}

func (h *QuestHandler) HandleBigHuntQuestFinish(user *store.UserState, questId int32, isRetired, isAnnihilated bool, nowMillis int64) FinishOutcome {
	quest, ok := h.QuestById[questId]
	if !ok {
		panic(fmt.Sprintf("unknown questId=%d for HandleBigHuntQuestFinish", questId))
	}

	target := h.targetForBigHunt(questId)
	var outcome FinishOutcome
	if !isRetired && !isAnnihilated {
		outcome = h.evaluateFinishOutcome(user, questId, target, false, nowMillis)
		h.applyQuestVictory(user, questId, &outcome, nowMillis, false)
	}

	consumed := h.staminaWithCampaign(user, quest.Stamina, target, nowMillis)
	if isRetired && !isAnnihilated && consumed > 1 {
		refund := consumed - 1
		maxMillis := h.MaxStaminaByLevel[user.Status.Level] * 1000
		store.RecoverStamina(user, refund*1000, maxMillis, nowMillis)
	}

	if isRetired || isAnnihilated {
		settleLostQuestState(user, questId)
	}

	return outcome
}
