package service

import (
	"context"
	"log"

	pb "lunar-tear/server/gen/proto"
	"lunar-tear/server/internal/gametime"
	"lunar-tear/server/internal/model"
	"lunar-tear/server/internal/questflow"
	"lunar-tear/server/internal/store"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	emptypb "google.golang.org/protobuf/types/known/emptypb"
)

func (s *QuestServiceServer) StartEventQuest(ctx context.Context, req *pb.StartEventQuestRequest) (*pb.StartEventQuestResponse, error) {
	log.Printf("[QuestService] StartEventQuest: chapterId=%d questId=%d isBattleOnly=%v maxAutoOrbitCount=%d",
		req.EventQuestChapterId, req.QuestId, req.IsBattleOnly, req.MaxAutoOrbitCount)

	engine := s.holder.Get().QuestHandler
	userId := CurrentUserId(ctx, s.users, s.sessions)
	nowMillis := gametime.NowMillis()
	var deckErr error
	s.users.UpdateUser(userId, func(user *store.UserState) {
		if deckErr = engine.CheckLimitContentDeck(user, req.EventQuestChapterId, req.QuestId, req.UserDeckNumber, nowMillis); deckErr != nil {
			return
		}
		engine.HandleEventQuestStart(user, req.EventQuestChapterId, req.QuestId, req.IsBattleOnly, req.UserDeckNumber, nowMillis)
		startAutoOrbit(user, model.QuestTypeEvent, req.EventQuestChapterId, req.QuestId, req.MaxAutoOrbitCount, nowMillis)
	})
	if deckErr != nil {
		log.Printf("[QuestService] StartEventQuest refused: %v", deckErr)
		return nil, status.Error(codes.FailedPrecondition, deckErr.Error())
	}

	drops := engine.BattleDropRewards(req.QuestId)
	pbDrops := make([]*pb.BattleDropReward, len(drops))
	for i, d := range drops {
		pbDrops[i] = &pb.BattleDropReward{
			QuestSceneId:         d.QuestSceneId,
			BattleDropCategoryId: d.BattleDropCategoryId,
			BattleDropEffectId:   1,
		}
	}

	return &pb.StartEventQuestResponse{
		BattleDropReward: pbDrops,
	}, nil
}

func (s *QuestServiceServer) FinishEventQuest(ctx context.Context, req *pb.FinishEventQuestRequest) (*pb.FinishEventQuestResponse, error) {
	log.Printf("[QuestService] FinishEventQuest: chapterId=%d questId=%d isRetired=%v isAnnihilated=%v isAutoOrbit=%v",
		req.EventQuestChapterId, req.QuestId, req.IsRetired, req.IsAnnihilated, req.IsAutoOrbit)

	nowMillis := gametime.NowMillis()
	engine := s.holder.Get().QuestHandler
	userId := CurrentUserId(ctx, s.users, s.sessions)
	var outcome questflow.FinishOutcome
	var endedDrops []store.AutoOrbitDropEntry
	var loopEnded bool
	s.users.UpdateUser(userId, func(user *store.UserState) {
		outcome = engine.HandleEventQuestFinish(user, req.EventQuestChapterId, req.QuestId, req.IsRetired, req.IsAnnihilated, nowMillis)
		endedDrops, loopEnded = finishAutoOrbit(user, req.IsAutoOrbit, req.IsRetired, req.IsAnnihilated, model.QuestTypeEvent, req.EventQuestChapterId, req.QuestId, nowMillis, outcome.DropRewards)
	})

	autoOrbitReward := emptyAutoOrbitReward()
	if loopEnded {
		autoOrbitReward.DropReward = autoOrbitDropsToProto(endedDrops)
	}

	return &pb.FinishEventQuestResponse{
		DropReward:                      toProtoRewards(outcome.DropRewards),
		FirstClearReward:                toProtoRewards(outcome.FirstClearRewards),
		MissionClearReward:              toProtoRewards(outcome.MissionClearRewards),
		MissionClearCompleteReward:      toProtoRewards(outcome.MissionClearCompleteRewards),
		AutoOrbitResult:                 []*pb.QuestReward{},
		IsBigWin:                        outcome.IsBigWin,
		BigWinClearedQuestMissionIdList: outcome.BigWinClearedQuestMissionIds,
		UserStatusCampaignReward:        []*pb.QuestReward{},
		AutoOrbitReward:                 autoOrbitReward,
	}, nil
}

func (s *QuestServiceServer) RestartEventQuest(ctx context.Context, req *pb.RestartEventQuestRequest) (*pb.RestartEventQuestResponse, error) {
	log.Printf("[QuestService] RestartEventQuest: chapterId=%d questId=%d", req.EventQuestChapterId, req.QuestId)

	engine := s.holder.Get().QuestHandler
	userId := CurrentUserId(ctx, s.users, s.sessions)
	s.users.UpdateUser(userId, func(user *store.UserState) {
		engine.HandleEventQuestRestart(user, req.EventQuestChapterId, req.QuestId, gametime.NowMillis())
	})

	return &pb.RestartEventQuestResponse{
		BattleDropReward: []*pb.BattleDropReward{},
	}, nil
}

func (s *QuestServiceServer) UpdateEventQuestSceneProgress(ctx context.Context, req *pb.UpdateEventQuestSceneProgressRequest) (*pb.UpdateEventQuestSceneProgressResponse, error) {
	log.Printf("[QuestService] UpdateEventQuestSceneProgress: questSceneId=%d", req.QuestSceneId)

	engine := s.holder.Get().QuestHandler
	userId := CurrentUserId(ctx, s.users, s.sessions)
	s.users.UpdateUser(userId, func(user *store.UserState) {
		engine.HandleEventQuestSceneProgress(user, req.QuestSceneId, gametime.NowMillis())
	})

	return &pb.UpdateEventQuestSceneProgressResponse{}, nil
}

const defaultGuerrillaFreeOpenMinutes = int32(60)

func (s *QuestServiceServer) StartGuerrillaFreeOpen(ctx context.Context, req *emptypb.Empty) (*pb.StartGuerrillaFreeOpenResponse, error) {
	log.Printf("[QuestService] StartGuerrillaFreeOpen")

	userId := CurrentUserId(ctx, s.users, s.sessions)
	nowMillis := gametime.NowMillis()
	s.users.UpdateUser(userId, func(user *store.UserState) {
		user.GuerrillaFreeOpen.StartDatetime = nowMillis
		user.GuerrillaFreeOpen.OpenMinutes = defaultGuerrillaFreeOpenMinutes
		user.GuerrillaFreeOpen.DailyOpenedCount++
		user.GuerrillaFreeOpen.LatestVersion = nowMillis
	})

	return &pb.StartGuerrillaFreeOpenResponse{}, nil
}
