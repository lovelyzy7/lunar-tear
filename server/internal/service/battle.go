package service

import (
	"context"
	"log"

	pb "lunar-tear/server/gen/proto"
	"lunar-tear/server/internal/gametime"
	"lunar-tear/server/internal/questflow"
	"lunar-tear/server/internal/store"
)

type BattleServiceServer struct {
	pb.UnimplementedBattleServiceServer
	users    store.UserRepository
	sessions store.SessionRepository
}

func NewBattleServiceServer(users store.UserRepository, sessions store.SessionRepository) *BattleServiceServer {
	return &BattleServiceServer{users: users, sessions: sessions}
}

func (s *BattleServiceServer) StartWave(ctx context.Context, req *pb.StartWaveRequest) (*pb.StartWaveResponse, error) {
	log.Printf("[BattleService] StartWave: userParty=%d npcParty=%d", len(req.UserPartyInitialInfoList), len(req.NpcPartyInitialInfoList))
	userId := CurrentUserId(ctx, s.users, s.sessions)
	s.users.UpdateUser(userId, func(user *store.UserState) {
		user.Battle.IsActive = true
		user.Battle.StartCount++
		user.Battle.LastStartedAt = gametime.NowMillis()
		user.Battle.LastUserPartyCount = int32(len(req.UserPartyInitialInfoList))
		user.Battle.LastNpcPartyCount = int32(len(req.NpcPartyInitialInfoList))
		questflow.RecordStartWave(user, req.UserPartyInitialInfoList)
	})
	return &pb.StartWaveResponse{}, nil
}

func (s *BattleServiceServer) FinishWave(ctx context.Context, req *pb.FinishWaveRequest) (*pb.FinishWaveResponse, error) {
	log.Printf("[BattleService] FinishWave: battleBinary=%d userParty=%d npcParty=%d elapsedFrames=%d",
		len(req.BattleBinary), len(req.UserPartyResultInfoList), len(req.NpcPartyResultInfoList), req.ElapsedFrameCount)
	userId := CurrentUserId(ctx, s.users, s.sessions)
	s.users.UpdateUser(userId, func(user *store.UserState) {
		user.Battle.IsActive = false
		user.Battle.FinishCount++
		user.Battle.LastFinishedAt = gametime.NowMillis()
		user.Battle.LastUserPartyCount = int32(len(req.UserPartyResultInfoList))
		user.Battle.LastNpcPartyCount = int32(len(req.NpcPartyResultInfoList))
		user.Battle.LastBattleBinarySize = int32(len(req.BattleBinary))
		user.Battle.LastElapsedFrameCount = req.ElapsedFrameCount
		// Quest missions are judged against these stats at the finish.
		questflow.RecordFinishWave(user, req.BattleDetail)
	})
	return &pb.FinishWaveResponse{}, nil
}
