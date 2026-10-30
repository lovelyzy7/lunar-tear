package questflow

import (
	"fmt"
	"log"

	"lunar-tear/server/internal/model"
	"lunar-tear/server/internal/store"
)

func (h *QuestHandler) HandleEventQuestStart(user *store.UserState, eventQuestChapterId, questId int32, isBattleOnly bool, userDeckNumber int32, nowMillis int64) {
	quest, ok := h.QuestById[questId]
	if !ok {
		panic(fmt.Sprintf("unknown questId=%d for HandleEventQuestStart", questId))
	}

	h.initQuestState(user, questId)
	ResetBattleStats(user)

	if quest.Stamina > 0 {
		maxMillis := h.MaxStaminaByLevel[user.Status.Level] * 1000
		stamina := h.staminaWithCampaign(user, quest.Stamina, h.targetForEvent(eventQuestChapterId, questId), nowMillis)
		store.ConsumeStamina(user, stamina, maxMillis, nowMillis)
	}

	questState := user.Quests[questId]
	questState.IsBattleOnly = isBattleOnly
	questState.UserDeckNumber = userDeckNumber
	questState.QuestStateType = model.UserQuestStateTypeActive
	questState.LatestStartDatetime = nowMillis
	user.Quests[questId] = questState

	user.EventQuest.CurrentEventQuestChapterId = eventQuestChapterId
	user.EventQuest.CurrentQuestId = questId
	if sceneIds := h.SceneIdsByQuestId[questId]; len(sceneIds) > 0 {
		user.EventQuest.CurrentQuestSceneId = sceneIds[0]
		user.EventQuest.HeadQuestSceneId = sceneIds[0]
	}
}

func (h *QuestHandler) HandleEventQuestFinish(user *store.UserState, eventQuestChapterId, questId int32, isRetired, isAnnihilated bool, nowMillis int64) FinishOutcome {
	quest, ok := h.QuestById[questId]
	if !ok {
		panic(fmt.Sprintf("unknown questId=%d for HandleEventQuestFinish", questId))
	}

	target := h.targetForEvent(eventQuestChapterId, questId)
	var outcome FinishOutcome
	if !isRetired && !isAnnihilated {
		outcome = h.evaluateFinishOutcome(user, questId, target, false, nowMillis)
		h.applyQuestVictory(user, questId, &outcome, nowMillis, false)
		h.recordSideStoryLimitContentStatus(user, questId, nowMillis)
		h.recordLimitContentWin(user, eventQuestChapterId, questId, nowMillis)
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

	user.EventQuest.CurrentEventQuestChapterId = 0
	user.EventQuest.CurrentQuestId = 0
	user.EventQuest.CurrentQuestSceneId = 0
	user.EventQuest.HeadQuestSceneId = 0
	user.EventQuest.LatestVersion = nowMillis

	return outcome
}

func (h *QuestHandler) recordSideStoryLimitContentStatus(user *store.UserState, questId int32, nowMillis int64) {
	chapterId, ok := h.SideStoryChapterByEventQuestId[questId]
	if !ok {
		return
	}
	st := user.QuestLimitContentStatus[questId]
	st.LimitContentQuestStatusType = 1
	st.EventQuestChapterId = chapterId
	st.LatestVersion = nowMillis
	user.QuestLimitContentStatus[questId] = st
}

func (h *QuestHandler) HandleEventQuestRestart(user *store.UserState, eventQuestChapterId, questId int32, nowMillis int64) {
	h.HandleQuestRestart(user, questId, nowMillis)

	user.EventQuest.CurrentEventQuestChapterId = eventQuestChapterId
	user.EventQuest.CurrentQuestId = questId
}

func (h *QuestHandler) HandleEventQuestSceneProgress(user *store.UserState, questSceneId int32, nowMillis int64) {
	if _, ok := h.SceneById[questSceneId]; !ok {
		log.Printf("[HandleEventQuestSceneProgress] unknown sceneId=%d, skipping", questSceneId)
		return
	}

	user.EventQuest.CurrentQuestSceneId = questSceneId
	if h.isSceneAhead(questSceneId, user.EventQuest.HeadQuestSceneId) {
		user.EventQuest.HeadQuestSceneId = questSceneId
	}

	h.applySceneGrants(user, questSceneId, nowMillis)
}
