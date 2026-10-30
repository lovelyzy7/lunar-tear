package service

import (
	"context"
	"fmt"

	pb "lunar-tear/server/gen/proto"
	"lunar-tear/server/internal/gametime"
	"lunar-tear/server/internal/model"
	"lunar-tear/server/internal/runtime"
	"lunar-tear/server/internal/store"
)

type BannerServiceServer struct {
	pb.UnimplementedBannerServiceServer
	users    store.UserRepository
	sessions store.SessionRepository
	holder   *runtime.Holder
}

func NewBannerServiceServer(users store.UserRepository, sessions store.SessionRepository, holder *runtime.Holder) *BannerServiceServer {
	return &BannerServiceServer{users: users, sessions: sessions, holder: holder}
}

func (s *BannerServiceServer) GetMamaBanner(ctx context.Context, req *pb.GetMamaBannerRequest) (*pb.GetMamaBannerResponse, error) {
	catalog := s.holder.Get().GachaEntries
	nowMillis := gametime.NowMillis()
	userId := CurrentUserId(ctx, s.users, s.sessions)
	user, err := s.users.LoadUser(userId)
	if err != nil {
		return nil, fmt.Errorf("get mama banner: %w", err)
	}
	return mamaBanner(catalog, &user, nowMillis), nil
}

// mamaBanner lists the active limited summons and the latest chapter summons
// the player has unlocked (the one with the highest gacha id).
func mamaBanner(catalog []store.GachaCatalogEntry, user *store.UserState, nowMillis int64) *pb.GetMamaBannerResponse {
	var termLimited []*pb.GachaBanner
	var latestChapter *pb.GachaBanner
	for _, entry := range catalog {
		if !gachaActiveAt(entry, nowMillis) {
			continue
		}
		if entry.GachaLabelType == model.GachaLabelPortalCage || entry.GachaLabelType == model.GachaLabelRecycle {
			continue
		}
		b := &pb.GachaBanner{
			GachaLabelType: entry.GachaLabelType,
			GachaAssetName: entry.BannerAssetName,
			GachaId:        entry.GachaId,
		}
		switch entry.GachaLabelType {
		case model.GachaLabelEvent, model.GachaLabelPremium:
			termLimited = append(termLimited, b)
		case model.GachaLabelChapter:
			if !gachaUnlockedFor(entry, user) {
				continue
			}
			if latestChapter == nil || entry.GachaId > latestChapter.GachaId {
				latestChapter = b
			}
		}
	}
	return &pb.GetMamaBannerResponse{
		TermLimitedGacha:   termLimited,
		LatestChapterGacha: latestChapter,
		IsExistUnreadPop:   false,
	}
}
