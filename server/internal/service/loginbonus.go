package service

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "lunar-tear/server/gen/proto"
	"lunar-tear/server/internal/gametime"
	"lunar-tear/server/internal/masterdata"
	"lunar-tear/server/internal/runtime"
	"lunar-tear/server/internal/store"

	emptypb "google.golang.org/protobuf/types/known/emptypb"
)

// giftTextLoginBonus is MGiftText 3, "Earned from login bonus."
const giftTextLoginBonus int32 = 3

type LoginBonusServiceServer struct {
	pb.UnimplementedLoginBonusServiceServer
	users    store.UserRepository
	sessions store.SessionRepository
	holder   *runtime.Holder
}

func NewLoginBonusServiceServer(users store.UserRepository, sessions store.SessionRepository, holder *runtime.Holder) *LoginBonusServiceServer {
	return &LoginBonusServiceServer{users: users, sessions: sessions, holder: holder}
}

func (s *LoginBonusServiceServer) ReceiveStamp(ctx context.Context, req *emptypb.Empty) (*pb.ReceiveStampResponse, error) {
	log.Printf("[LoginBonusService] ReceiveStamp")
	userId := CurrentUserId(ctx, s.users, s.sessions)
	catalog := s.holder.Get().LoginBonus

	// One stamp per game day on each card: the everyday one and every dated
	// login bonus open today (see bunker_login_bonus.go). A repeat call gets
	// the normal empty response.
	var stampErr error
	_, err := s.users.UpdateUser(userId, func(u *store.UserState) {
		now := gametime.NowMillis()
		stampEventLoginBonuses(catalog, u, now)
		// One stamp per game day: compare days, not `>= today` — a stamp time
		// in the game clock's future (a real-time save or RewindLoop leftover)
		// would otherwise block every later day.
		if gametime.SameDay(u.LoginBonus.LatestRewardReceiveDatetime, now) {
			log.Printf("[LoginBonusService] ReceiveStamp: already stamped today")
			return
		}
		nextPage, nextStamp, reward, err := resolveNextStamp(catalog, u.LoginBonus)
		if err != nil {
			stampErr = err
			return
		}
		log.Printf("[LoginBonusService] bonusId=%d page %d->%d stamp %d->%d possType=%d possId=%d count=%d (-> gift box)",
			u.LoginBonus.LoginBonusId, u.LoginBonus.CurrentPageNumber, nextPage, u.LoginBonus.CurrentStampNumber, nextStamp,
			reward.PossessionType, reward.PossessionId, reward.Count)
		addLoginBonusGift(u, reward, now)
		u.LoginBonus.CurrentPageNumber = nextPage
		u.LoginBonus.CurrentStampNumber = nextStamp
		u.LoginBonus.LatestRewardReceiveDatetime = now
		u.LoginBonus.LatestVersion = now
	})
	if err != nil {
		return nil, fmt.Errorf("receive stamp: %w", err)
	}
	if stampErr != nil {
		return nil, stampErr
	}
	return &pb.ReceiveStampResponse{}, nil
}

// addLoginBonusGift puts a stamp's reward in the gift box for 30 days.
func addLoginBonusGift(u *store.UserState, reward masterdata.LoginBonusReward, now int64) {
	u.Gifts.NotReceived = append(u.Gifts.NotReceived, store.NotReceivedGiftState{
		GiftCommon: store.GiftCommonState{
			PossessionType:        reward.PossessionType,
			PossessionId:          reward.PossessionId,
			Count:                 reward.Count,
			GrantDatetime:         now,
			DescriptionGiftTextId: giftTextLoginBonus,
		},
		ExpirationDatetime: now + int64(30*24*time.Hour/time.Millisecond),
		UserGiftUuid:       uuid.New().String(),
	})
	pruneExpiredGifts(u, now)
}

func resolveNextStamp(catalog *masterdata.LoginBonusCatalog, lb store.UserLoginBonusState) (nextPage, nextStamp int32, reward masterdata.LoginBonusReward, err error) {
	bonusId := lb.LoginBonusId
	curPage := lb.CurrentPageNumber
	curStamp := lb.CurrentStampNumber

	nextPage = curPage
	nextStamp = curStamp + 1
	var ok bool
	reward, ok = catalog.LookupStampReward(bonusId, nextPage, nextStamp)
	if !ok {
		nextPage = curPage + 1
		nextStamp = 1
		total := catalog.TotalPageCount(bonusId)
		if total > 0 && nextPage > total {
			err = status.Errorf(codes.FailedPrecondition,
				"login bonus %d exhausted (page %d stamp %d is the last)",
				bonusId, curPage, curStamp)
			return
		}
		reward, ok = catalog.LookupStampReward(bonusId, nextPage, nextStamp)
		if !ok {
			err = status.Errorf(codes.FailedPrecondition,
				"no reward found for login bonus %d page %d stamp %d",
				bonusId, nextPage, nextStamp)
			return
		}
	}
	return
}
