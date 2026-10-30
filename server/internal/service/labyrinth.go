package service

import (
	"context"
	"log"

	pb "lunar-tear/server/gen/proto"
	"lunar-tear/server/internal/gametime"
	"lunar-tear/server/internal/model"
	"lunar-tear/server/internal/runtime"
	"lunar-tear/server/internal/store"
)

type LabyrinthServiceServer struct {
	pb.UnimplementedLabyrinthServiceServer
	users    store.UserRepository
	sessions store.SessionRepository
	holder   *runtime.Holder
}

func NewLabyrinthServiceServer(users store.UserRepository, sessions store.SessionRepository, holder *runtime.Holder) *LabyrinthServiceServer {
	if holder == nil {
		panic("runtime holder is required")
	}
	return &LabyrinthServiceServer{users: users, sessions: sessions, holder: holder}
}

func (s *LabyrinthServiceServer) ReceiveStageAccumulationReward(ctx context.Context, req *pb.ReceiveStageAccumulationRewardRequest) (*pb.ReceiveStageAccumulationRewardResponse, error) {
	log.Printf("[LabyrinthService] ReceiveStageAccumulationReward: chapter=%d stage=%d questMissionClearCount=%d",
		req.EventQuestChapterId, req.StageOrder, req.QuestMissionClearCount)

	cat := s.holder.Get()
	laby := cat.Labyrinth
	granter := cat.QuestHandler.Granter

	userId := CurrentUserId(ctx, s.users, s.sessions)
	nowMillis := gametime.NowMillis()

	key := store.LabyrinthStageKey{
		EventQuestChapterId: req.EventQuestChapterId,
		StageOrder:          req.StageOrder,
	}

	s.users.UpdateUser(userId, func(user *store.UserState) {
		rec := user.LabyrinthStages[key]
		old := rec.AccumulationRewardReceivedQuestMissionCount

		items, highest := laby.CollectAccumulationRewards(req.EventQuestChapterId, req.StageOrder, old, req.QuestMissionClearCount)
		if highest <= old {
			log.Printf("[LabyrinthService] ReceiveStageAccumulationReward: nothing to grant for chapter=%d stage=%d (claimed=%d, target=%d)",
				req.EventQuestChapterId, req.StageOrder, old, req.QuestMissionClearCount)
			return
		}

		for _, it := range items {
			granter.GrantFull(user, model.PossessionType(it.PossessionType), it.PossessionId, it.Count, nowMillis)
		}

		rec.EventQuestChapterId = req.EventQuestChapterId
		rec.StageOrder = req.StageOrder
		rec.AccumulationRewardReceivedQuestMissionCount = highest
		rec.LatestVersion = nowMillis
		user.LabyrinthStages[key] = rec

		log.Printf("[LabyrinthService] ReceiveStageAccumulationReward: chapter=%d stage=%d granted %d item(s), claimed %d -> %d",
			req.EventQuestChapterId, req.StageOrder, len(items), old, highest)
	})

	return &pb.ReceiveStageAccumulationRewardResponse{}, nil
}

func (s *LabyrinthServiceServer) ReceiveStageClearReward(ctx context.Context, req *pb.ReceiveStageClearRewardRequest) (*pb.ReceiveStageClearRewardResponse, error) {
	log.Printf("[LabyrinthService] ReceiveStageClearReward: chapter=%d stage=%d",
		req.EventQuestChapterId, req.StageOrder)

	cat := s.holder.Get()
	laby := cat.Labyrinth
	granter := cat.QuestHandler.Granter

	userId := CurrentUserId(ctx, s.users, s.sessions)
	nowMillis := gametime.NowMillis()

	key := store.LabyrinthStageKey{
		EventQuestChapterId: req.EventQuestChapterId,
		StageOrder:          req.StageOrder,
	}

	s.users.UpdateUser(userId, func(user *store.UserState) {
		rec := user.LabyrinthStages[key]
		if rec.IsReceivedStageClearReward {
			log.Printf("[LabyrinthService] ReceiveStageClearReward: already claimed chapter=%d stage=%d",
				req.EventQuestChapterId, req.StageOrder)
			return
		}

		items := laby.StageClearReward(req.EventQuestChapterId, req.StageOrder)
		for _, it := range items {
			granter.GrantFull(user, model.PossessionType(it.PossessionType), it.PossessionId, it.Count, nowMillis)
		}

		rec.EventQuestChapterId = req.EventQuestChapterId
		rec.StageOrder = req.StageOrder
		rec.IsReceivedStageClearReward = true
		rec.LatestVersion = nowMillis
		user.LabyrinthStages[key] = rec

		log.Printf("[LabyrinthService] ReceiveStageClearReward: chapter=%d stage=%d granted %d item(s)",
			req.EventQuestChapterId, req.StageOrder, len(items))
	})

	return &pb.ReceiveStageClearRewardResponse{}, nil
}

// UpdateSeasonData is called when the player opens a labyrinth whose open
// season they haven't joined: it pays the ended season's reward (the client
// plays it as the "reach reward") and joins the new season, which starts the
// climb again (see bunker_labyrinth.go).
func (s *LabyrinthServiceServer) UpdateSeasonData(ctx context.Context, req *pb.UpdateSeasonDataRequest) (*pb.UpdateSeasonDataResponse, error) {
	cat := s.holder.Get()
	laby := cat.Labyrinth
	granter := cat.QuestHandler.Granter
	now := gametime.NowMillis()
	userId := CurrentUserId(ctx, s.users, s.sessions)

	seasonResult := []*pb.LabyrinthSeasonResult{}
	joined := false
	if _, err := s.users.UpdateUser(userId, func(u *store.UserState) {
		for _, ch := range laby.ChaptersByOrder {
			if ch.EventQuestChapterId != req.EventQuestChapterId {
				continue
			}
			if result := settleLabyrinthSeason(laby, granter, u, ch, now); result != nil {
				seasonResult = append(seasonResult, result)
			}
			joined = joinLabyrinthSeason(u, ch.EventQuestChapterId, now)
		}
	}); err != nil {
		return nil, err
	}

	log.Printf("[LabyrinthService] UpdateSeasonData: chapter=%d paid %d season reward(s), new season %v",
		req.EventQuestChapterId, len(seasonResult), joined)
	return &pb.UpdateSeasonDataResponse{SeasonResult: seasonResult}, nil
}
