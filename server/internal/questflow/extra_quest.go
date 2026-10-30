package questflow

import (
	"fmt"
	"log"

	"lunar-tear/server/internal/model"
	"lunar-tear/server/internal/store"
)

func (h *QuestHandler) HandleExtraQuestStart(user *store.UserState, questId, userDeckNumber int32, nowMillis int64) {
	quest, ok := h.QuestById[questId]
	if !ok {
		panic(fmt.Sprintf("unknown questId=%d for HandleExtraQuestStart", questId))
	}

	h.initQuestState(user, questId)
	ResetBattleStats(user)

	if quest.Stamina > 0 {
		maxMillis := h.MaxStaminaByLevel[user.Status.Level] * 1000
		stamina := h.staminaWithCampaign(user, quest.Stamina, h.targetForExtra(questId), nowMillis)
		store.ConsumeStamina(user, stamina, maxMillis, nowMillis)
	}

	questState := user.Quests[questId]
	questState.UserDeckNumber = userDeckNumber
	questState.QuestStateType = model.UserQuestStateTypeActive
	questState.LatestStartDatetime = nowMillis
	user.Quests[questId] = questState

	user.ExtraQuest.CurrentQuestId = questId
	if sceneIds := h.SceneIdsByQuestId[questId]; len(sceneIds) > 0 {
		user.ExtraQuest.CurrentQuestSceneId = sceneIds[0]
		user.ExtraQuest.HeadQuestSceneId = sceneIds[0]
	}
}

func (h *QuestHandler) HandleExtraQuestFinish(user *store.UserState, questId int32, isRetired, isAnnihilated bool, nowMillis int64) FinishOutcome {
	quest, ok := h.QuestById[questId]
	if !ok {
		panic(fmt.Sprintf("unknown questId=%d for HandleExtraQuestFinish", questId))
	}

	target := h.targetForExtra(questId)
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

	user.ExtraQuest.CurrentQuestId = 0
	user.ExtraQuest.CurrentQuestSceneId = 0
	user.ExtraQuest.HeadQuestSceneId = 0

	return outcome
}

func (h *QuestHandler) HandleExtraQuestRestart(user *store.UserState, questId int32, nowMillis int64) {
	h.HandleQuestRestart(user, questId, nowMillis)

	user.ExtraQuest.CurrentQuestId = questId
}

func (h *QuestHandler) HandleExtraQuestSceneProgress(user *store.UserState, questSceneId int32, nowMillis int64) {
	if _, ok := h.SceneById[questSceneId]; !ok {
		log.Printf("[HandleExtraQuestSceneProgress] unknown sceneId=%d, skipping", questSceneId)
		return
	}

	user.ExtraQuest.CurrentQuestSceneId = questSceneId
	if h.isSceneAhead(questSceneId, user.ExtraQuest.HeadQuestSceneId) {
		user.ExtraQuest.HeadQuestSceneId = questSceneId
	}

	h.applySceneGrants(user, questSceneId, nowMillis)
}
