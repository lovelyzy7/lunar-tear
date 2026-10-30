package masterdata

import (
	"log"
	"sort"

	"lunar-tear/server/internal/gametime"
	"lunar-tear/server/internal/utils"
)

type BigHuntBossQuestRow struct {
	BigHuntBossQuestId                int32
	BigHuntBossId                     int32
	BigHuntQuestGroupId               int32
	BigHuntScoreRewardGroupScheduleId int32
	DailyChallengeCount               int32
}

type BigHuntQuestRow struct {
	BigHuntQuestId                 int32
	QuestId                        int32
	BigHuntQuestScoreCoefficientId int32
}

type BigHuntBossRow struct {
	BigHuntBossId           int32
	BigHuntBossGradeGroupId int32
	AttributeType           int32
}

type GradeThreshold struct {
	NecessaryScore   int64
	AssetGradeIconId int32
}

type ScoreRewardScheduleEntry struct {
	BigHuntScoreRewardGroupId int32
	StartDatetime             int64
}

type ScoreRewardThreshold struct {
	NecessaryScore       int64
	BigHuntRewardGroupId int32
}

type RewardItem struct {
	PossessionType int32
	PossessionId   int32
	Count          int32
}

type BigHuntCatalog struct {
	BossQuestById               map[int32]BigHuntBossQuestRow
	QuestById                   map[int32]BigHuntQuestRow
	ScoreCoefficients           map[int32]int32
	BossByBossId                map[int32]BigHuntBossRow
	GradeThresholds             map[int32][]GradeThreshold
	ActiveScheduleId            int32 // at server start; use ScheduleAt for the current one
	schedules                   []EntityMBigHuntSchedule
	ScoreRewardSchedules        map[int32][]ScoreRewardScheduleEntry
	ScoreRewardThresholds       map[int32][]ScoreRewardThreshold
	RewardItems                 map[int32][]RewardItem
	WeeklyRewardSchedulesByAttr map[int32][]ScoreRewardScheduleEntry
}

func (c *BigHuntCatalog) ResolveActiveScoreRewardGroupId(scheduleId int32, nowMillis int64) int32 {
	entries := c.ScoreRewardSchedules[scheduleId]
	for _, e := range entries {
		if nowMillis >= e.StartDatetime {
			return e.BigHuntScoreRewardGroupId
		}
	}
	if len(entries) > 0 {
		return entries[len(entries)-1].BigHuntScoreRewardGroupId
	}
	return 0
}

func (c *BigHuntCatalog) ResolveActiveWeeklyRewardGroupIdByAttr(attributeType int32, nowMillis int64) int32 {
	entries := c.WeeklyRewardSchedulesByAttr[attributeType]
	for _, e := range entries {
		if nowMillis >= e.StartDatetime {
			return e.BigHuntScoreRewardGroupId
		}
	}
	if len(entries) > 0 {
		return entries[len(entries)-1].BigHuntScoreRewardGroupId
	}
	return 0
}

func (c *BigHuntCatalog) ResolveGradeIconId(bossId int32, score int64) int32 {
	boss, ok := c.BossByBossId[bossId]
	if !ok {
		return 0
	}
	thresholds := c.GradeThresholds[boss.BigHuntBossGradeGroupId]
	var iconId int32
	for _, t := range thresholds {
		if score >= t.NecessaryScore {
			iconId = t.AssetGradeIconId
		} else {
			break
		}
	}
	return iconId
}

// TierRewards is the reward of the highest m_big_hunt_score_reward_group tier
// a score reaches. Each tier lists its whole bundle (higher tiers repeat and
// raise the lower ones), so only that one tier is paid, as the client's
// GetWeeklyAttributeRewardPossessionItem picks
// OrderByDescending(NecessaryScore).FirstOrDefault().
func (c *BigHuntCatalog) TierRewards(scoreRewardGroupId int32, score int64) []RewardItem {
	var best *ScoreRewardThreshold
	for i, t := range c.ScoreRewardThresholds[scoreRewardGroupId] {
		if t.NecessaryScore <= score && (best == nil || t.NecessaryScore >= best.NecessaryScore) {
			best = &c.ScoreRewardThresholds[scoreRewardGroupId][i]
		}
	}
	if best == nil {
		return nil
	}
	return c.RewardItems[best.BigHuntRewardGroupId]
}

func LoadBigHuntCatalog() *BigHuntCatalog {
	bossQuestRows, err := utils.ReadTable[EntityMBigHuntBossQuest]("m_big_hunt_boss_quest")
	if err != nil {
		log.Fatalf("load big hunt boss quest table: %v", err)
	}
	bossQuestById := make(map[int32]BigHuntBossQuestRow, len(bossQuestRows))
	for _, r := range bossQuestRows {
		bossQuestById[r.BigHuntBossQuestId] = BigHuntBossQuestRow{
			BigHuntBossQuestId:                r.BigHuntBossQuestId,
			BigHuntBossId:                     r.BigHuntBossId,
			BigHuntQuestGroupId:               r.BigHuntQuestGroupId,
			BigHuntScoreRewardGroupScheduleId: r.BigHuntScoreRewardGroupScheduleId,
			DailyChallengeCount:               r.DailyChallengeCount,
		}
	}

	questRows, err := utils.ReadTable[EntityMBigHuntQuest]("m_big_hunt_quest")
	if err != nil {
		log.Fatalf("load big hunt quest table: %v", err)
	}
	questById := make(map[int32]BigHuntQuestRow, len(questRows))
	for _, r := range questRows {
		questById[r.BigHuntQuestId] = BigHuntQuestRow{
			BigHuntQuestId:                 r.BigHuntQuestId,
			QuestId:                        r.QuestId,
			BigHuntQuestScoreCoefficientId: r.BigHuntQuestScoreCoefficientId,
		}
	}

	coeffRows, err := utils.ReadTable[EntityMBigHuntQuestScoreCoefficient]("m_big_hunt_quest_score_coefficient")
	if err != nil {
		log.Fatalf("load big hunt quest score coefficient table: %v", err)
	}
	scoreCoefficients := make(map[int32]int32, len(coeffRows))
	for _, r := range coeffRows {
		scoreCoefficients[r.BigHuntQuestScoreCoefficientId] = r.ScoreDifficultBonusPermil
	}

	bossRows, err := utils.ReadTable[EntityMBigHuntBoss]("m_big_hunt_boss")
	if err != nil {
		log.Fatalf("load big hunt boss table: %v", err)
	}
	bossByBossId := make(map[int32]BigHuntBossRow, len(bossRows))
	for _, r := range bossRows {
		bossByBossId[r.BigHuntBossId] = BigHuntBossRow{
			BigHuntBossId:           r.BigHuntBossId,
			BigHuntBossGradeGroupId: r.BigHuntBossGradeGroupId,
			AttributeType:           r.AttributeType,
		}
	}

	gradeRows, err := utils.ReadTable[EntityMBigHuntBossGradeGroup]("m_big_hunt_boss_grade_group")
	if err != nil {
		log.Fatalf("load big hunt boss grade group table: %v", err)
	}
	gradeThresholds := make(map[int32][]GradeThreshold)
	for _, r := range gradeRows {
		gradeThresholds[r.BigHuntBossGradeGroupId] = append(gradeThresholds[r.BigHuntBossGradeGroupId], GradeThreshold{
			NecessaryScore:   r.NecessaryScore,
			AssetGradeIconId: r.AssetGradeIconId,
		})
	}
	for k := range gradeThresholds {
		sort.Slice(gradeThresholds[k], func(i, j int) bool {
			return gradeThresholds[k][i].NecessaryScore < gradeThresholds[k][j].NecessaryScore
		})
	}

	scheduleRows, err := utils.ReadTable[EntityMBigHuntSchedule]("m_big_hunt_schedule")
	if err != nil {
		log.Fatalf("load big hunt schedule table: %v", err)
	}
	nowMillis := gametime.NowMillis()
	var activeScheduleId int32
	var latestEndDatetime int64
	for _, r := range scheduleRows {
		if nowMillis >= r.ChallengeStartDatetime && nowMillis <= r.ChallengeEndDatetime {
			activeScheduleId = r.BigHuntScheduleId
			break
		}
		if r.ChallengeEndDatetime > latestEndDatetime {
			latestEndDatetime = r.ChallengeEndDatetime
			activeScheduleId = r.BigHuntScheduleId
		}
	}

	rewardSchedRows, err := utils.ReadTable[EntityMBigHuntScoreRewardGroupSchedule]("m_big_hunt_score_reward_group_schedule")
	if err != nil {
		log.Fatalf("load big hunt score reward group schedule table: %v", err)
	}
	scoreRewardSchedules := make(map[int32][]ScoreRewardScheduleEntry)
	for _, r := range rewardSchedRows {
		scoreRewardSchedules[r.BigHuntScoreRewardGroupScheduleId] = append(
			scoreRewardSchedules[r.BigHuntScoreRewardGroupScheduleId],
			ScoreRewardScheduleEntry{
				BigHuntScoreRewardGroupId: r.BigHuntScoreRewardGroupId,
				StartDatetime:             r.StartDatetime,
			},
		)
	}
	for k := range scoreRewardSchedules {
		sort.Slice(scoreRewardSchedules[k], func(i, j int) bool {
			return scoreRewardSchedules[k][i].StartDatetime > scoreRewardSchedules[k][j].StartDatetime
		})
	}

	rewardGroupRows, err := utils.ReadTable[EntityMBigHuntScoreRewardGroup]("m_big_hunt_score_reward_group")
	if err != nil {
		log.Fatalf("load big hunt score reward group table: %v", err)
	}
	scoreRewardThresholds := make(map[int32][]ScoreRewardThreshold)
	for _, r := range rewardGroupRows {
		scoreRewardThresholds[r.BigHuntScoreRewardGroupId] = append(
			scoreRewardThresholds[r.BigHuntScoreRewardGroupId],
			ScoreRewardThreshold{
				NecessaryScore:       r.NecessaryScore,
				BigHuntRewardGroupId: r.BigHuntRewardGroupId,
			},
		)
	}
	for k := range scoreRewardThresholds {
		sort.Slice(scoreRewardThresholds[k], func(i, j int) bool {
			return scoreRewardThresholds[k][i].NecessaryScore < scoreRewardThresholds[k][j].NecessaryScore
		})
	}

	rewardItemRows, err := utils.ReadTable[EntityMBigHuntRewardGroup]("m_big_hunt_reward_group")
	if err != nil {
		log.Fatalf("load big hunt reward group table: %v", err)
	}
	rewardItems := make(map[int32][]RewardItem)
	for _, r := range rewardItemRows {
		rewardItems[r.BigHuntRewardGroupId] = append(rewardItems[r.BigHuntRewardGroupId], RewardItem{
			PossessionType: r.PossessionType,
			PossessionId:   r.PossessionId,
			Count:          r.Count,
		})
	}

	weeklySchedRows, err := utils.ReadTable[EntityMBigHuntWeeklyAttributeScoreRewardGroupSchedule]("m_big_hunt_weekly_attribute_score_reward_group_schedule")
	if err != nil {
		log.Fatalf("load big hunt weekly attribute score reward group schedule table: %v", err)
	}
	weeklyRewardSchedulesByAttr := make(map[int32][]ScoreRewardScheduleEntry)
	for _, r := range weeklySchedRows {
		weeklyRewardSchedulesByAttr[r.AttributeType] = append(weeklyRewardSchedulesByAttr[r.AttributeType], ScoreRewardScheduleEntry{
			BigHuntScoreRewardGroupId: r.BigHuntScoreRewardGroupId,
			StartDatetime:             r.StartDatetime,
		})
	}
	for k := range weeklyRewardSchedulesByAttr {
		sort.Slice(weeklyRewardSchedulesByAttr[k], func(i, j int) bool {
			return weeklyRewardSchedulesByAttr[k][i].StartDatetime > weeklyRewardSchedulesByAttr[k][j].StartDatetime
		})
	}

	log.Printf("big hunt catalog loaded: %d boss quests, %d quests, %d bosses, %d score coefficients, %d reward groups, schedule=%d",
		len(bossQuestById), len(questById), len(bossByBossId), len(scoreCoefficients), len(rewardItems), activeScheduleId)

	return &BigHuntCatalog{
		BossQuestById:               bossQuestById,
		QuestById:                   questById,
		ScoreCoefficients:           scoreCoefficients,
		BossByBossId:                bossByBossId,
		GradeThresholds:             gradeThresholds,
		ActiveScheduleId:            activeScheduleId,
		schedules:                   scheduleRows,
		ScoreRewardSchedules:        scoreRewardSchedules,
		ScoreRewardThresholds:       scoreRewardThresholds,
		RewardItems:                 rewardItems,
		WeeklyRewardSchedulesByAttr: weeklyRewardSchedulesByAttr,
	}
}

// ScheduleAt is the Subjugation Battle schedule open at nowMillis, or else the one that
// ends last. With the Original calendar the schedule changes while the server
// runs, so callers ask with the current game time.
func (c *BigHuntCatalog) ScheduleAt(nowMillis int64) int32 {
	if len(c.schedules) == 0 {
		return c.ActiveScheduleId
	}
	var id int32
	var latestEnd int64
	for _, r := range c.schedules {
		if nowMillis >= r.ChallengeStartDatetime && nowMillis <= r.ChallengeEndDatetime {
			return r.BigHuntScheduleId
		}
		if r.ChallengeEndDatetime > latestEnd {
			latestEnd, id = r.ChallengeEndDatetime, r.BigHuntScheduleId
		}
	}
	return id
}
