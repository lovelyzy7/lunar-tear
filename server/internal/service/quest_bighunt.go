package service

import (
	"bytes"
	"context"
	"log"
	"slices"

	pb "lunar-tear/server/gen/proto"
	"lunar-tear/server/internal/gametime"
	"lunar-tear/server/internal/masterdata"
	"lunar-tear/server/internal/model"
	"lunar-tear/server/internal/runtime"
	"lunar-tear/server/internal/store"

	emptypb "google.golang.org/protobuf/types/known/emptypb"
)

type BigHuntServiceServer struct {
	pb.UnimplementedBigHuntServiceServer
	users    store.UserRepository
	sessions store.SessionRepository
	holder   *runtime.Holder
}

func NewBigHuntServiceServer(
	users store.UserRepository,
	sessions store.SessionRepository,
	holder *runtime.Holder,
) *BigHuntServiceServer {
	return &BigHuntServiceServer{users: users, sessions: sessions, holder: holder}
}

func (s *BigHuntServiceServer) StartBigHuntQuest(ctx context.Context, req *pb.StartBigHuntQuestRequest) (*pb.StartBigHuntQuestResponse, error) {
	log.Printf("[BigHuntService] StartBigHuntQuest: bossQuestId=%d questId=%d deckNumber=%d isDryRun=%v",
		req.BigHuntBossQuestId, req.BigHuntQuestId, req.UserDeckNumber, req.IsDryRun)

	cat := s.holder.Get()
	catalog := cat.BigHunt
	engine := cat.QuestHandler
	userId := CurrentUserId(ctx, s.users, s.sessions)
	nowMillis := gametime.NowMillis()

	bhQuest, ok := catalog.QuestById[req.BigHuntQuestId]
	if !ok {
		log.Printf("[BigHuntService] StartBigHuntQuest: unknown bigHuntQuestId=%d", req.BigHuntQuestId)
	}

	today := gametime.StartOfDayMillis()

	s.users.UpdateUser(userId, func(user *store.UserState) {
		if ok {
			engine.HandleBigHuntQuestStart(user, bhQuest.QuestId, req.UserDeckNumber, nowMillis)
		}

		user.BigHuntProgress = store.BigHuntProgress{
			CurrentBigHuntBossQuestId: req.BigHuntBossQuestId,
			CurrentBigHuntQuestId:     req.BigHuntQuestId,
			CurrentQuestSceneId:       0,
			IsDryRun:                  req.IsDryRun,
			LatestVersion:             nowMillis,
		}

		user.BigHuntDeckNumber = req.UserDeckNumber
		// A new run starts with no saved waves; RestartBigHuntQuest resumes
		// from them.
		user.BigHuntBattleBinary = nil
		user.BigHuntBattleDetail = store.BigHuntBattleDetail{}

		st := user.BigHuntStatuses[req.BigHuntBossQuestId]
		if st.LatestChallengeDatetime < today {
			st.DailyChallengeCount = 0
		}
		st.DailyChallengeCount++
		st.LatestChallengeDatetime = nowMillis
		st.LatestVersion = nowMillis
		user.BigHuntStatuses[req.BigHuntBossQuestId] = st
	})

	return &pb.StartBigHuntQuestResponse{}, nil
}

func (s *BigHuntServiceServer) UpdateBigHuntQuestSceneProgress(ctx context.Context, req *pb.UpdateBigHuntQuestSceneProgressRequest) (*pb.UpdateBigHuntQuestSceneProgressResponse, error) {
	log.Printf("[BigHuntService] UpdateBigHuntQuestSceneProgress: questSceneId=%d", req.QuestSceneId)

	userId := CurrentUserId(ctx, s.users, s.sessions)
	nowMillis := gametime.NowMillis()
	s.users.UpdateUser(userId, func(user *store.UserState) {
		user.BigHuntProgress.CurrentQuestSceneId = req.QuestSceneId
		user.BigHuntProgress.LatestVersion = nowMillis
	})

	return &pb.UpdateBigHuntQuestSceneProgressResponse{}, nil
}

func (s *BigHuntServiceServer) FinishBigHuntQuest(ctx context.Context, req *pb.FinishBigHuntQuestRequest) (*pb.FinishBigHuntQuestResponse, error) {
	log.Printf("[BigHuntService] FinishBigHuntQuest: bossQuestId=%d questId=%d isRetired=%v",
		req.BigHuntBossQuestId, req.BigHuntQuestId, req.IsRetired)

	cat := s.holder.Get()
	catalog := cat.BigHunt
	engine := cat.QuestHandler
	userId := CurrentUserId(ctx, s.users, s.sessions)
	nowMillis := gametime.NowMillis()

	bhQuest := catalog.QuestById[req.BigHuntQuestId]
	bossQuest := catalog.BossQuestById[req.BigHuntBossQuestId]
	boss := catalog.BossByBossId[bossQuest.BigHuntBossId]

	var scoreInfo *pb.BigHuntScoreInfo
	var scoreRewards []*pb.BigHuntReward
	var battleReportWaves []*pb.BigHuntBattleReportWave

	s.users.UpdateUser(userId, func(user *store.UserState) {
		engine.HandleBigHuntQuestFinish(user, bhQuest.QuestId, req.IsRetired, false, nowMillis)

		if req.IsRetired || user.BigHuntProgress.IsDryRun {
			user.BigHuntProgress = store.BigHuntProgress{LatestVersion: nowMillis}
			user.BigHuntBattleBinary = nil
			user.BigHuntBattleDetail = store.BigHuntBattleDetail{}
			return
		}

		detail := user.BigHuntBattleDetail
		totalDamage := detail.TotalDamage
		baseScore := totalDamage

		difficultyBonusPermil := int32(0)
		if coeff, ok := catalog.ScoreCoefficients[bhQuest.BigHuntQuestScoreCoefficientId]; ok {
			difficultyBonusPermil = coeff
		}

		aliveBonusPermil := int32(500)

		maxComboBonusPermil := int32(0)
		if detail.MaxComboCount >= 100 {
			maxComboBonusPermil = 300
		} else if detail.MaxComboCount >= 50 {
			maxComboBonusPermil = 200
		} else if detail.MaxComboCount >= 20 {
			maxComboBonusPermil = 100
		}

		userScore := baseScore * int64(1000+difficultyBonusPermil+aliveBonusPermil+maxComboBonusPermil) / 1000

		if userScore > user.BigHuntMaxScores[bossQuest.BigHuntBossId].MaxScore {
			user.BigHuntMaxScores[bossQuest.BigHuntBossId] = store.BigHuntMaxScore{
				MaxScore:               userScore,
				MaxScoreUpdateDatetime: nowMillis,
				LatestVersion:          nowMillis,
			}
		}

		schedKey := store.BigHuntScheduleScoreKey{
			BigHuntScheduleId: catalog.ScheduleAt(gametime.NowMillis()),
			BigHuntBossId:     bossQuest.BigHuntBossId,
		}
		oldSchedMax := user.BigHuntScheduleMaxScores[schedKey].MaxScore
		isHighScore := userScore > oldSchedMax
		if isHighScore {
			user.BigHuntScheduleMaxScores[schedKey] = store.BigHuntScheduleMaxScore{
				MaxScore:               userScore,
				MaxScoreUpdateDatetime: nowMillis,
				LatestVersion:          nowMillis,
			}
		}

		weeklyVersion := gametime.WeeklyVersion(nowMillis)
		weekKey := store.BigHuntWeeklyScoreKey{
			BigHuntWeeklyVersion: weeklyVersion,
			AttributeType:        boss.AttributeType,
		}
		oldWeeklyMax := user.BigHuntWeeklyMaxScores[weekKey].MaxScore
		if userScore > oldWeeklyMax {
			user.BigHuntWeeklyMaxScores[weekKey] = store.BigHuntWeeklyMaxScore{
				MaxScore:      userScore,
				LatestVersion: nowMillis,
			}
		}

		assetGradeIconId := catalog.ResolveGradeIconId(bossQuest.BigHuntBossId, userScore)

		scoreInfo = &pb.BigHuntScoreInfo{
			UserScore:             userScore,
			IsHighScore:           isHighScore,
			TotalDamage:           totalDamage,
			BaseScore:             baseScore,
			DifficultyBonusPermil: difficultyBonusPermil,
			AliveBonusPermil:      aliveBonusPermil,
			MaxComboBonusPermil:   maxComboBonusPermil,
			AssetGradeIconId:      assetGradeIconId,
		}

		// Every finished run pays the one score tier its score reaches.
		rewardGroupId := catalog.ResolveActiveScoreRewardGroupId(bossQuest.BigHuntScoreRewardGroupScheduleId, nowMillis)
		for _, item := range catalog.TierRewards(rewardGroupId, userScore) {
			engine.Granter.GrantFull(user, model.PossessionType(item.PossessionType), item.PossessionId, item.Count, nowMillis)
			scoreRewards = append(scoreRewards, &pb.BigHuntReward{
				PossessionType: item.PossessionType,
				PossessionId:   item.PossessionId,
				Count:          item.Count,
			})
		}

		if len(detail.CostumeBattleInfo) > 0 {
			wavesByIndex := map[int32]*pb.BigHuntBattleReportWave{}
			var waveOrder []int32
			for _, ci := range detail.CostumeBattleInfo {
				wave, ok := wavesByIndex[ci.WaveIndex]
				if !ok {
					wave = &pb.BigHuntBattleReportWave{}
					wavesByIndex[ci.WaveIndex] = wave
					waveOrder = append(waveOrder, ci.WaveIndex)
				}
				wave.BattleReportCostume = append(wave.BattleReportCostume, &pb.BigHuntBattleReportCostume{
					CostumeId:   ci.CostumeId,
					TotalDamage: ci.TotalDamage,
					HitCount:    ci.HitCount,
					BattleReportRandomDisplay: &pb.BattleReportRandomDisplay{
						RandomDisplayValueType: ci.RandomDisplayValueType,
						RandomDisplayValue:     ci.RandomDisplayValue,
					},
				})
			}
			for _, idx := range waveOrder {
				battleReportWaves = append(battleReportWaves, wavesByIndex[idx])
			}
		}

		user.BigHuntProgress = store.BigHuntProgress{LatestVersion: nowMillis}
		user.BigHuntBattleBinary = nil
		user.BigHuntBattleDetail = store.BigHuntBattleDetail{}
	})

	if scoreInfo == nil {
		scoreInfo = &pb.BigHuntScoreInfo{}
	}
	if scoreRewards == nil {
		scoreRewards = []*pb.BigHuntReward{}
	}

	if battleReportWaves == nil {
		battleReportWaves = []*pb.BigHuntBattleReportWave{}
	}
	battleReport := &pb.BigHuntBattleReport{
		BattleReportWave: battleReportWaves,
	}

	return &pb.FinishBigHuntQuestResponse{
		ScoreInfo:    scoreInfo,
		ScoreReward:  scoreRewards,
		BattleReport: battleReport,
	}, nil
}

func (s *BigHuntServiceServer) RestartBigHuntQuest(ctx context.Context, req *pb.RestartBigHuntQuestRequest) (*pb.RestartBigHuntQuestResponse, error) {
	log.Printf("[BigHuntService] RestartBigHuntQuest: bossQuestId=%d questId=%d", req.BigHuntBossQuestId, req.BigHuntQuestId)

	cat := s.holder.Get()
	catalog := cat.BigHunt
	engine := cat.QuestHandler
	userId := CurrentUserId(ctx, s.users, s.sessions)
	nowMillis := gametime.NowMillis()

	bhQuest := catalog.QuestById[req.BigHuntQuestId]

	var battleBinary []byte
	var deckNumber int32

	today := gametime.StartOfDayMillis()

	s.users.UpdateUser(userId, func(user *store.UserState) {
		engine.HandleBigHuntQuestStart(user, bhQuest.QuestId, user.BigHuntDeckNumber, nowMillis)

		user.BigHuntProgress.CurrentQuestSceneId = 0
		user.BigHuntProgress.LatestVersion = nowMillis

		st := user.BigHuntStatuses[req.BigHuntBossQuestId]
		if st.LatestChallengeDatetime < today {
			st.DailyChallengeCount = 0
		}
		st.DailyChallengeCount++
		st.LatestChallengeDatetime = nowMillis
		st.LatestVersion = nowMillis
		user.BigHuntStatuses[req.BigHuntBossQuestId] = st

		battleBinary = user.BigHuntBattleBinary
		deckNumber = user.BigHuntDeckNumber
	})

	return &pb.RestartBigHuntQuestResponse{
		BattleBinary: battleBinary,
		DeckNumber:   deckNumber,
	}, nil
}

func (s *BigHuntServiceServer) SkipBigHuntQuest(ctx context.Context, req *pb.SkipBigHuntQuestRequest) (*pb.SkipBigHuntQuestResponse, error) {
	log.Printf("[BigHuntService] SkipBigHuntQuest: bossQuestId=%d skipCount=%d", req.BigHuntBossQuestId, req.SkipCount)

	cat := s.holder.Get()
	catalog := cat.BigHunt
	granter := cat.QuestHandler.Granter
	userId := CurrentUserId(ctx, s.users, s.sessions)
	nowMillis := gametime.NowMillis()
	today := gametime.StartOfDayMillis()

	bossQuest, hasBossQuest := catalog.BossQuestById[req.BigHuntBossQuestId]
	var scoreRewards []*pb.BigHuntReward

	s.users.UpdateUser(userId, func(user *store.UserState) {
		st := user.BigHuntStatuses[req.BigHuntBossQuestId]
		if st.LatestChallengeDatetime < today {
			st.DailyChallengeCount = 0
		}
		st.DailyChallengeCount += req.SkipCount
		st.LatestChallengeDatetime = nowMillis
		st.LatestVersion = nowMillis
		user.BigHuntStatuses[req.BigHuntBossQuestId] = st

		if !hasBossQuest || req.SkipCount <= 0 {
			return
		}
		rewardGroupId := catalog.ResolveActiveScoreRewardGroupId(bossQuest.BigHuntScoreRewardGroupScheduleId, nowMillis)
		if rewardGroupId == 0 {
			return
		}
		maxScore := user.BigHuntScheduleMaxScores[store.BigHuntScheduleScoreKey{
			BigHuntScheduleId: catalog.ScheduleAt(gametime.NowMillis()),
			BigHuntBossId:     bossQuest.BigHuntBossId,
		}].MaxScore
		if maxScore <= 0 {
			return
		}
		// Each skip pays the tier of the best score this schedule.
		items := catalog.TierRewards(rewardGroupId, maxScore)
		for n := int32(0); n < req.SkipCount; n++ {
			for _, item := range items {
				granter.GrantFull(user, model.PossessionType(item.PossessionType), item.PossessionId, item.Count, nowMillis)
				scoreRewards = append(scoreRewards, &pb.BigHuntReward{
					PossessionType: item.PossessionType,
					PossessionId:   item.PossessionId,
					Count:          item.Count,
				})
			}
		}
	})

	if scoreRewards == nil {
		scoreRewards = []*pb.BigHuntReward{}
	}
	return &pb.SkipBigHuntQuestResponse{
		ScoreReward: scoreRewards,
	}, nil
}

func (s *BigHuntServiceServer) SaveBigHuntBattleInfo(ctx context.Context, req *pb.SaveBigHuntBattleInfoRequest) (*pb.SaveBigHuntBattleInfoResponse, error) {
	log.Printf("[BigHuntService] SaveBigHuntBattleInfo: elapsedFrames=%d", req.ElapsedFrameCount)

	userId := CurrentUserId(ctx, s.users, s.sessions)
	nowMillis := gametime.NowMillis()

	var totalDamage int64
	if req.BigHuntBattleDetail != nil {
		for _, ci := range req.BigHuntBattleDetail.CostumeBattleInfo {
			if ci != nil {
				totalDamage += ci.TotalDamage
			}
		}
	}

	s.users.UpdateUser(userId, func(user *store.UserState) {
		// The client saves after every wave (CalculatorTurnBattleApiRequestBigHunt
		// .RequestFinishWaveAsync) with that wave's stats; the request names no
		// wave, so a resend is recognised by its unchanged battle snapshot.
		if len(req.BattleBinary) > 0 && bytes.Equal(req.BattleBinary, user.BigHuntBattleBinary) {
			log.Printf("[BigHuntService] SaveBigHuntBattleInfo: wave already saved, ignored")
			return
		}
		user.BigHuntBattleBinary = req.BattleBinary

		if req.BigHuntBattleDetail != nil {
			existingCostumes := user.BigHuntBattleDetail.CostumeBattleInfo
			nextWaveIndex := int32(bigHuntWaveCount(existingCostumes))
			newCostumes := make([]store.BigHuntCostumeBattleInfo, 0, len(req.BigHuntBattleDetail.CostumeBattleInfo))
			for _, ci := range req.BigHuntBattleDetail.CostumeBattleInfo {
				if ci == nil {
					continue
				}
				var rdType int32
				var rdValue int64
				if rd := ci.BattleReportRandomDisplay; rd != nil {
					rdType = rd.RandomDisplayValueType
					rdValue = rd.RandomDisplayValue
				}
				newCostumes = append(newCostumes, store.BigHuntCostumeBattleInfo{
					WaveIndex:              nextWaveIndex,
					CostumeId:              resolveBigHuntCostumeId(user, ci.UserDeckNumber, ci.DeckCharacterNumber),
					TotalDamage:            ci.TotalDamage,
					HitCount:               ci.HitCount,
					RandomDisplayValueType: rdType,
					RandomDisplayValue:     rdValue,
				})
			}
			prev := user.BigHuntBattleDetail
			user.BigHuntBattleDetail = store.BigHuntBattleDetail{
				DeckType:             req.BigHuntBattleDetail.DeckType,
				UserTripleDeckNumber: req.BigHuntBattleDetail.UserTripleDeckNumber,
				BossKnockDownCount:   prev.BossKnockDownCount + req.BigHuntBattleDetail.BossKnockDownCount,
				MaxComboCount:        max(prev.MaxComboCount, req.BigHuntBattleDetail.MaxComboCount),
				TotalDamage:          prev.TotalDamage + totalDamage,
				CostumeBattleInfo:    append(slices.Clone(existingCostumes), newCostumes...),
			}
		}

		user.BigHuntProgress.LatestVersion = nowMillis
	})

	return &pb.SaveBigHuntBattleInfoResponse{}, nil
}

func (s *BigHuntServiceServer) GetBigHuntTopData(ctx context.Context, _ *emptypb.Empty) (*pb.GetBigHuntTopDataResponse, error) {
	log.Printf("[BigHuntService] GetBigHuntTopData")

	catalog := s.holder.Get().BigHunt
	userId := CurrentUserId(ctx, s.users, s.sessions)
	user, _ := s.users.LoadUser(userId)

	nowMillis := gametime.NowMillis()
	weeklyVersion := gametime.WeeklyVersion(nowMillis)

	var weeklyScoreResults []*pb.WeeklyScoreResult
	for _, boss := range catalog.BossByBossId {
		key := store.BigHuntWeeklyScoreKey{
			BigHuntWeeklyVersion: weeklyVersion,
			AttributeType:        boss.AttributeType,
		}
		ws := user.BigHuntWeeklyMaxScores[key]
		gradeIconId := catalog.ResolveGradeIconId(boss.BigHuntBossId, ws.MaxScore)

		weeklyScoreResults = append(weeklyScoreResults, &pb.WeeklyScoreResult{
			AttributeType:           boss.AttributeType,
			BeforeMaxScore:          ws.MaxScore,
			CurrentMaxScore:         ws.MaxScore,
			BeforeAssetGradeIconId:  gradeIconId,
			CurrentAssetGradeIconId: gradeIconId,
			AfterMaxScore:           ws.MaxScore,
			AfterAssetGradeIconId:   gradeIconId,
		})
	}

	ws := user.BigHuntWeeklyStatuses[weeklyVersion]

	weeklyRewards := resolveBigHuntWeeklyRewards(catalog, user, weeklyVersion, nowMillis)

	lastWeekVersion := weeklyVersion - 7*24*60*60*1000
	lastWeekRewards := resolveBigHuntWeeklyRewards(catalog, user, lastWeekVersion, nowMillis)

	return &pb.GetBigHuntTopDataResponse{
		WeeklyScoreResult:           weeklyScoreResults,
		WeeklyScoreReward:           weeklyRewards,
		IsReceivedWeeklyScoreReward: ws.IsReceivedWeeklyReward,
		LastWeekWeeklyScoreReward:   lastWeekRewards,
	}, nil
}

func bigHuntWaveCount(infos []store.BigHuntCostumeBattleInfo) int {
	if len(infos) == 0 {
		return 0
	}
	return int(infos[len(infos)-1].WaveIndex) + 1
}

func resolveBigHuntCostumeId(user *store.UserState, userDeckNumber, deckCharacterNumber int32) int32 {
	if userDeckNumber == 0 {
		userDeckNumber = user.BigHuntDeckNumber
	}
	for _, dt := range []model.DeckType{model.DeckTypeBigHunt, model.DeckTypeQuest} {
		deck, ok := user.Decks[store.DeckKey{DeckType: dt, UserDeckNumber: userDeckNumber}]
		if !ok {
			continue
		}
		var dcUuid string
		switch deckCharacterNumber {
		case 1:
			dcUuid = deck.UserDeckCharacterUuid01
		case 2:
			dcUuid = deck.UserDeckCharacterUuid02
		case 3:
			dcUuid = deck.UserDeckCharacterUuid03
		}
		if dcUuid == "" {
			continue
		}
		dc, ok := user.DeckCharacters[dcUuid]
		if !ok || dc.UserCostumeUuid == "" {
			continue
		}
		if costume, ok := user.Costumes[dc.UserCostumeUuid]; ok {
			return costume.CostumeId
		}
	}
	return 0
}

func resolveBigHuntWeeklyRewards(catalog *masterdata.BigHuntCatalog, user store.UserState, weeklyVersion, nowMillis int64) []*pb.BigHuntReward {
	var rewards []*pb.BigHuntReward
	for _, boss := range catalog.BossByBossId {
		rewardGroupId := catalog.ResolveActiveWeeklyRewardGroupIdByAttr(boss.AttributeType, nowMillis)
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
		for _, item := range catalog.TierRewards(rewardGroupId, maxScore) {
			rewards = append(rewards, &pb.BigHuntReward{
				PossessionType: item.PossessionType,
				PossessionId:   item.PossessionId,
				Count:          item.Count,
			})
		}
	}
	if rewards == nil {
		rewards = []*pb.BigHuntReward{}
	}
	return rewards
}
