package service

import (
	"context"
	"log"

	pb "lunar-tear/server/gen/proto"
	"lunar-tear/server/internal/gametime"
	"lunar-tear/server/internal/runtime"
	"lunar-tear/server/internal/store"
)

type GameplayServiceServer struct {
	pb.UnimplementedGamePlayServiceServer
	users    store.UserRepository
	sessions store.SessionRepository
	holder   *runtime.Holder
}

func NewGameplayServiceServer(users store.UserRepository, sessions store.SessionRepository, holder *runtime.Holder) *GameplayServiceServer {
	return &GameplayServiceServer{users: users, sessions: sessions, holder: holder}
}

func (s *GameplayServiceServer) CheckBeforeGamePlay(ctx context.Context, req *pb.CheckBeforeGamePlayRequest) (*pb.CheckBeforeGamePlayResponse, error) {
	log.Printf("[GamePlayService] CheckBeforeGamePlay: tr=%s voiceLang=%d textLang=%d",
		req.Tr, req.VoiceClientSystemLanguageTypeId, req.TextClientSystemLanguageTypeId)

	badges := []*pb.MenuGachaBadgeInfo{}
	if user, err := s.users.LoadUser(CurrentUserId(ctx, s.users, s.sessions)); err == nil {
		badges = freeSummonBadges(s.holder.Get().GachaEntries, &user, gametime.NowMillis())
	}
	return &pb.CheckBeforeGamePlayResponse{
		IsExistUnreadPop:   false,
		MenuGachaBadgeInfo: badges,
	}, nil
}
