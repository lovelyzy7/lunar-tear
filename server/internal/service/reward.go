package service

import (
	"context"
	"log"

	pb "lunar-tear/server/gen/proto"
	"lunar-tear/server/internal/gametime"
	"lunar-tear/server/internal/model"
	"lunar-tear/server/internal/runtime"
	"lunar-tear/server/internal/store"

	emptypb "google.golang.org/protobuf/types/known/emptypb"
)

type RewardServiceServer struct {
	pb.UnimplementedRewardServiceServer
	users    store.UserRepository
	sessions store.SessionRepository
	holder   *runtime.Holder
}

func NewRewardServiceServer(
	users store.UserRepository,
	sessions store.SessionRepository,
	holder *runtime.Holder,
) *RewardServiceServer {
	return &RewardServiceServer{users: users, sessions: sessions, holder: holder}
}

func (s *RewardServiceServer) ReceiveBigHuntReward(ctx context.Context, _ *emptypb.Empty) (*pb.ReceiveBigHuntRewardResponse, error) {
	log.Printf("[RewardService] ReceiveBigHuntReward")

	cat := s.holder.Get()
	bhCatalog := cat.BigHunt
	granter := cat.QuestHandler.Granter
	userId := CurrentUserId(ctx, s.users, s.sessions)
	nowMillis := gametime.NowMillis()
	weeklyVersion := gametime.WeeklyVersion(nowMillis)
	today := gametime.StartOfDayMillis()

	var weeklyScoreResults []*pb.WeeklyScoreResult
	var weeklyRewards []*pb.BigHuntReward
	isReceived := false

	s.users.UpdateUser(userId, func(user *store.UserState) {
		for bossQuestId, bossQuest := range bhCatalog.BossQuestById {
			st := user.BigHuntStatuses[bossQuestId]
			// Day-version keys name a day: `== today` means "already paid
			// today". `>= today` would also swallow every later day when the
			// key sits in the game clock's future (a real-time save or
			// RewindLoop leftover).
			if st.LastDailyRewardReceivedDayVersion == today {
				continue
			}
			rewardGroupId := bhCatalog.ResolveActiveScoreRewardGroupId(bossQuest.BigHuntScoreRewardGroupScheduleId, nowMillis)
			if rewardGroupId == 0 {
				continue
			}
			maxScore := user.BigHuntScheduleMaxScores[store.BigHuntScheduleScoreKey{
				BigHuntScheduleId: bhCatalog.ScheduleAt(nowMillis),
				BigHuntBossId:     bossQuest.BigHuntBossId,
			}].MaxScore
			if maxScore <= 0 {
				continue
			}
			items := bhCatalog.TierRewards(rewardGroupId, maxScore)
			for _, item := range items {
				granter.GrantFull(user, model.PossessionType(item.PossessionType), item.PossessionId, item.Count, nowMillis)
			}
			if len(items) > 0 {
				log.Printf("[RewardService] ReceiveBigHuntReward: bossQuestId=%d granted %d daily rewards (maxScore=%d, group=%d)",
					bossQuestId, len(items), maxScore, rewardGroupId)
			}
			st.LastDailyRewardReceivedDayVersion = today
			st.LatestVersion = nowMillis
			user.BigHuntStatuses[bossQuestId] = st
		}

		ws := user.BigHuntWeeklyStatuses[weeklyVersion]
		isReceived = ws.IsReceivedWeeklyReward

		for _, boss := range bhCatalog.BossByBossId {
			key := store.BigHuntWeeklyScoreKey{
				BigHuntWeeklyVersion: weeklyVersion,
				AttributeType:        boss.AttributeType,
			}
			wms := user.BigHuntWeeklyMaxScores[key]
			gradeIcon := bhCatalog.ResolveGradeIconId(boss.BigHuntBossId, wms.MaxScore)
			weeklyScoreResults = append(weeklyScoreResults, &pb.WeeklyScoreResult{
				AttributeType:           boss.AttributeType,
				BeforeMaxScore:          wms.MaxScore,
				CurrentMaxScore:         wms.MaxScore,
				BeforeAssetGradeIconId:  gradeIcon,
				CurrentAssetGradeIconId: gradeIcon,
				AfterMaxScore:           wms.MaxScore,
				AfterAssetGradeIconId:   gradeIcon,
			})
		}

		if !isReceived {
			for _, boss := range bhCatalog.BossByBossId {
				rewardGroupId := bhCatalog.ResolveActiveWeeklyRewardGroupIdByAttr(boss.AttributeType, nowMillis)
				if rewardGroupId == 0 {
					continue
				}

				weekKey := store.BigHuntWeeklyScoreKey{
					BigHuntWeeklyVersion: weeklyVersion,
					AttributeType:        boss.AttributeType,
				}
				maxScore := user.BigHuntWeeklyMaxScores[weekKey].MaxScore
				if maxScore <= 0 {
					continue
				}

				items := bhCatalog.TierRewards(rewardGroupId, maxScore)
				for _, item := range items {
					granter.GrantFull(user, model.PossessionType(item.PossessionType), item.PossessionId, item.Count, nowMillis)
					weeklyRewards = append(weeklyRewards, &pb.BigHuntReward{
						PossessionType: item.PossessionType,
						PossessionId:   item.PossessionId,
						Count:          item.Count,
					})
				}
			}

			ws.IsReceivedWeeklyReward = true
			ws.LatestVersion = nowMillis
			user.BigHuntWeeklyStatuses[weeklyVersion] = ws
			isReceived = true
		}
	})

	if weeklyRewards == nil {
		weeklyRewards = []*pb.BigHuntReward{}
	}
	if weeklyScoreResults == nil {
		weeklyScoreResults = []*pb.WeeklyScoreResult{}
	}

	return &pb.ReceiveBigHuntRewardResponse{
		WeeklyScoreResult:           weeklyScoreResults,
		WeeklyScoreReward:           weeklyRewards,
		IsReceivedWeeklyScoreReward: isReceived,
		LastWeekWeeklyScoreReward:   []*pb.BigHuntReward{},
	}, nil
}
