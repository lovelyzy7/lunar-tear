package service

// Arena (PvP) RPC stubs.
//
// B 方案移植范围明确排除竞技场（arena）。internal/pvp 库仍被引入，因为好友系统
// （bunker_friends.go）用它的计算机玩家/好友状态助手，任务引擎也会读 Ext.Pvp；
// 但竞技场对战玩法（bunker_pvp.go）不移植，全部竞技场 RPC 维持 lunar-tear 原有的
// 空实现行为：客户端请求不报 Unimplemented，但不返回对战数据。
//
// 以后要开竞技场时：从 bunker/overlays/server/internal/service/bunker_pvp.go 移入
// 实现，删掉本文件里的同名桩即可。

import (
	"context"

	pb "lunar-tear/server/gen/proto"
	"lunar-tear/server/internal/pvp"
	"lunar-tear/server/internal/runtime"
	"lunar-tear/server/internal/store"

	"google.golang.org/protobuf/types/known/emptypb"
)

// PvpServiceServer is the Arena's empty implementation (stub).
type PvpServiceServer struct {
	pb.UnimplementedPvpServiceServer
	users    store.UserRepository
	sessions store.SessionRepository
	holder   *runtime.Holder
}

// NewPvpServiceServer keeps the signature Bunker's grpc.go registers, so the
// arena implementation can replace this stub without further wiring changes.
func NewPvpServiceServer(users store.UserRepository, sessions store.SessionRepository, holder *runtime.Holder) *PvpServiceServer {
	return &PvpServiceServer{users: users, sessions: sessions, holder: holder}
}

func (s *PvpServiceServer) GetTopData(context.Context, *emptypb.Empty) (*pb.GetTopDataResponse, error) {
	return &pb.GetTopDataResponse{}, nil
}

func (s *PvpServiceServer) GetMatchingList(context.Context, *emptypb.Empty) (*pb.GetMatchingListResponse, error) {
	return &pb.GetMatchingListResponse{}, nil
}

func (s *PvpServiceServer) UpdateMatchingList(context.Context, *emptypb.Empty) (*pb.UpdateMatchingListResponse, error) {
	return &pb.UpdateMatchingListResponse{}, nil
}

func (s *PvpServiceServer) StartBattle(_ context.Context, _ *pb.StartBattleRequest) (*pb.StartBattleResponse, error) {
	return &pb.StartBattleResponse{}, nil
}

func (s *PvpServiceServer) FinishBattle(_ context.Context, _ *pb.FinishBattleRequest) (*pb.FinishBattleResponse, error) {
	return &pb.FinishBattleResponse{}, nil
}

func (s *PvpServiceServer) GetRanking(_ context.Context, _ *pb.GetRankingRequest) (*pb.GetRankingResponse, error) {
	return &pb.GetRankingResponse{}, nil
}

func (s *PvpServiceServer) GetSeasonResult(context.Context, *emptypb.Empty) (*pb.GetSeasonResultResponse, error) {
	return &pb.GetSeasonResultResponse{}, nil
}

func (s *PvpServiceServer) GetAttackLogList(context.Context, *emptypb.Empty) (*pb.GetAttackLogListResponse, error) {
	return &pb.GetAttackLogListResponse{}, nil
}

func (s *PvpServiceServer) GetDefenseLogList(context.Context, *emptypb.Empty) (*pb.GetDefenseLogListResponse, error) {
	return &pb.GetDefenseLogListResponse{}, nil
}

// SetPvpDefenseDeck is Arena defense-deck setup: out of scope (arena excluded).
func (s *DeckServiceServer) SetPvpDefenseDeck(context.Context, *pb.SetPvpDefenseDeckRequest) (*pb.SetPvpDefenseDeckResponse, error) {
	return &pb.SetPvpDefenseDeckResponse{}, nil
}

// ReceivePvpReward is the Arena's weekly reward: out of scope (arena excluded).
func (s *RewardServiceServer) ReceivePvpReward(context.Context, *emptypb.Empty) (*pb.ReceivePvpRewardResponse, error) {
	return &pb.ReceivePvpRewardResponse{DiffUserData: map[string]*pb.DiffData{}}, nil
}

// recoverBattlePoints applies an Arena battle point item (items 4001-4005, the
// recovery shop's refill). The points live in save state (Ext.Pvp), so the
// items keep working while the Arena RPCs are stubbed.
func recoverBattlePoints(u *store.UserState, valueType, value, count int32, now int64) {
	if c := pvp.Current(); c != nil {
		c.Recover(&u.Ext.Pvp, valueType, value, count, now)
	}
}

// pvpProfileInfo keeps user profiles free of Arena results while the arena is
// stubbed (Bunker fills in the season's points and grade here).
func pvpProfileInfo(_ store.UserState) *pb.ProfilePvpInfo {
	return &pb.ProfilePvpInfo{}
}

// cpuProfile lets the Arena's computer players answer profile requests; with
// the arena stubbed every profile request falls through to the real lookup.
func (s *UserServiceServer) cpuProfile(_ context.Context, _ int64) *pb.GetUserProfileResponse {
	return nil
}
