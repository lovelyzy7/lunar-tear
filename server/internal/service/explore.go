package service

import (
	"context"
	"fmt"
	"log"
	"math/rand"

	pb "lunar-tear/server/gen/proto"
	"lunar-tear/server/internal/gametime"
	"lunar-tear/server/internal/runtime"
	"lunar-tear/server/internal/store"
)

type ExploreServiceServer struct {
	pb.UnimplementedExploreServiceServer
	users    store.UserRepository
	sessions store.SessionRepository
	holder   *runtime.Holder
}

func NewExploreServiceServer(users store.UserRepository, sessions store.SessionRepository, holder *runtime.Holder) *ExploreServiceServer {
	return &ExploreServiceServer{users: users, sessions: sessions, holder: holder}
}

func (s *ExploreServiceServer) StartExplore(ctx context.Context, req *pb.StartExploreRequest) (*pb.StartExploreResponse, error) {
	log.Printf("[ExploreService] StartExplore: exploreId=%d useConsumableItemId=%d", req.ExploreId, req.UseConsumableItemId)

	catalog := s.holder.Get().Explore
	if _, ok := catalog.Explores[req.ExploreId]; !ok {
		return nil, fmt.Errorf("explore id=%d not found", req.ExploreId)
	}

	userId := CurrentUserId(ctx, s.users, s.sessions)
	nowMillis := gametime.NowMillis()

	var short error
	_, err := s.users.UpdateUser(userId, func(user *store.UserState) {
		explore := catalog.Explores[req.ExploreId]
		// Playing again within the waiting time (or on hard) takes Explorer's Tickets.
		usesTicket := req.UseConsumableItemId > 0 && explore.ConsumeItemCount > 0
		if usesTicket {
			cur := user.ConsumableItems[req.UseConsumableItemId]
			if cur < explore.ConsumeItemCount {
				short = fmt.Errorf("explore id=%d needs %d of item %d, have %d", req.ExploreId, explore.ConsumeItemCount, req.UseConsumableItemId, cur)
				return
			}
			user.ConsumableItems[req.UseConsumableItemId] = cur - explore.ConsumeItemCount
			log.Printf("[ExploreService] StartExplore: consumed item=%d count=%d remaining=%d", req.UseConsumableItemId, explore.ConsumeItemCount, user.ConsumableItems[req.UseConsumableItemId])
		}

		user.Explore = store.ExploreState{
			PlayingExploreId:   req.ExploreId,
			IsUseExploreTicket: usesTicket,
			LatestPlayDatetime: nowMillis,
			LatestVersion:      nowMillis,
		}
	})
	if err == nil {
		err = short
	}
	if err != nil {
		return nil, fmt.Errorf("start explore: %w", err)
	}

	return &pb.StartExploreResponse{}, nil
}

func (s *ExploreServiceServer) FinishExplore(ctx context.Context, req *pb.FinishExploreRequest) (*pb.FinishExploreResponse, error) {
	log.Printf("[ExploreService] FinishExplore: exploreId=%d score=%d", req.ExploreId, req.Score)

	cat := s.holder.Get()
	catalog := cat.Explore
	explore, ok := catalog.Explores[req.ExploreId]
	if !ok {
		return nil, fmt.Errorf("explore id=%d not found", req.ExploreId)
	}

	assetGradeIconId := catalog.GradeForScore(req.ExploreId, req.Score)

	userId := CurrentUserId(ctx, s.users, s.sessions)
	nowMillis := gametime.NowMillis()

	rank := exploreGradeRank(catalog, req.ExploreId, req.Score)
	stamina := exploreStamina(rank)
	var rewards []*pb.ExploreReward

	_, err := s.users.UpdateUser(userId, func(user *store.UserState) {
		existing, exists := user.ExploreScores[req.ExploreId]
		if !exists || req.Score > existing.MaxScore {
			user.ExploreScores[req.ExploreId] = store.ExploreScoreState{
				ExploreId:              req.ExploreId,
				MaxScore:               req.Score,
				MaxScoreUpdateDatetime: nowMillis,
				LatestVersion:          nowMillis,
			}
		}

		user.Explore = store.ExploreState{
			PlayingExploreId:   0,
			IsUseExploreTicket: false,
			LatestPlayDatetime: user.Explore.LatestPlayDatetime,
			LatestVersion:      nowMillis,
		}

		// Settle natural regeneration up to now before adding the reward, so
		// the time since the last stamina update is not lost.
		store.RecoverStamina(user, stamina*1000, cat.Shop.MaxStaminaMillis[user.Status.Level], nowMillis)
		user.Status.LatestVersion = nowMillis
		log.Printf("[ExploreService] FinishExplore: grade %d, stamina +%d -> %d", rank, stamina, user.Status.StaminaMilliValue)

		rewards = exploreDraws(user, cat.GameConfig.ConsumableItemIdForGold, rank, explore.RewardLotteryCount, rand.Intn)
		log.Printf("[ExploreService] FinishExplore: %d draws, %d rewards", explore.RewardLotteryCount, len(rewards))
	})
	if err != nil {
		return nil, fmt.Errorf("finish explore: %w", err)
	}

	return &pb.FinishExploreResponse{
		AcquireStaminaCount: stamina,
		ExploreReward:       rewards,
		AssetGradeIconId:    assetGradeIconId,
	}, nil
}

func (s *ExploreServiceServer) RetireExplore(ctx context.Context, req *pb.RetireExploreRequest) (*pb.RetireExploreResponse, error) {
	log.Printf("[ExploreService] RetireExplore: exploreId=%d", req.ExploreId)

	userId := CurrentUserId(ctx, s.users, s.sessions)
	nowMillis := gametime.NowMillis()

	_, err := s.users.UpdateUser(userId, func(user *store.UserState) {
		user.Explore = store.ExploreState{
			PlayingExploreId:   0,
			IsUseExploreTicket: false,
			LatestPlayDatetime: user.Explore.LatestPlayDatetime,
			LatestVersion:      nowMillis,
		}
	})
	if err != nil {
		return nil, fmt.Errorf("retire explore: %w", err)
	}

	return &pb.RetireExploreResponse{}, nil
}
