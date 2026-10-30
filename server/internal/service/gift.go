package service

import (
	"context"
	"fmt"
	"log"
	"slices"
	"sort"
	"time"

	pb "lunar-tear/server/gen/proto"
	"lunar-tear/server/internal/gametime"
	"lunar-tear/server/internal/model"
	"lunar-tear/server/internal/runtime"
	"lunar-tear/server/internal/store"

	emptypb "google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// giftPageSize is GiftDialogPresenter.kGiftPageItemMaxCount, used when the
// request has no GetCount.
const giftPageSize = 20

// Dark.Generated.Type.GiftRewardKindFilterType
const (
	giftKindNone      int32 = 1
	giftKindGem       int32 = 2
	giftKindGold      int32 = 3
	giftKindWeapon    int32 = 4
	giftKindCompanion int32 = 5
	giftKindParts     int32 = 6
	giftKindMaterial  int32 = 7
	giftKindOther     int32 = 8
	giftKindCostume   int32 = 9
)

// Dark.Generated.Type.GiftExpirationFilterType
const (
	giftExpirationOnlyExpire    int32 = 2
	giftExpirationOnlyNotExpire int32 = 3
)

type GiftServiceServer struct {
	pb.UnimplementedGiftServiceServer
	users    store.UserRepository
	sessions store.SessionRepository
	holder   *runtime.Holder
}

func NewGiftServiceServer(users store.UserRepository, sessions store.SessionRepository, holder *runtime.Holder) *GiftServiceServer {
	return &GiftServiceServer{users: users, sessions: sessions, holder: holder}
}

// ReceiveGift grants each requested gift through the general granter. An
// expired gift leaves the box without being granted; a gift that would take
// a possession past its MConfig limit stays in the box and is reported as
// overflowing.
func (s *GiftServiceServer) ReceiveGift(ctx context.Context, req *pb.ReceiveGiftRequest) (*pb.ReceiveGiftResponse, error) {
	log.Printf("[GiftService] ReceiveGift: giftUuids=%d", len(req.UserGiftUuid))

	cat := s.holder.Get()
	granter := cat.QuestHandler.Granter
	historyCap := int(cat.GameConfig.GiftReceivedListTotalCount)
	userId := CurrentUserId(ctx, s.users, s.sessions)
	var received, expired, overflow []string
	_, err := s.users.UpdateUser(userId, func(user *store.UserState) {
		received, expired, overflow = receiveGifts(user, granter, req.UserGiftUuid, historyCap, gametime.NowMillis())
	})
	if err != nil {
		return nil, fmt.Errorf("receive gift: %w", err)
	}
	log.Printf("[GiftService] ReceiveGift: received=%d expired=%d overflow=%d", len(received), len(expired), len(overflow))

	return &pb.ReceiveGiftResponse{
		ReceivedGiftUuid: received,
		ExpiredGiftUuid:  expired,
		OverflowGiftUuid: overflow,
	}, nil
}

func receiveGifts(user *store.UserState, granter *store.PossessionGranter, uuids []string, historyCap int, nowMillis int64) (received, expired, overflow []string) {
	received, expired, overflow = []string{}, []string{}, []string{}
	index := make(map[string]int, len(user.Gifts.NotReceived))
	for i, gift := range user.Gifts.NotReceived {
		index[gift.UserGiftUuid] = i
	}
	done := make(map[string]bool, len(uuids))
	for _, id := range uuids {
		i, ok := index[id]
		if !ok || done[id] {
			continue
		}
		done[id] = true
		gift := user.Gifts.NotReceived[i]
		gc := gift.GiftCommon
		switch {
		case giftExpired(gift, nowMillis):
			expired = append(expired, id)
		case granter.WouldOverflow(user, model.PossessionType(gc.PossessionType), gc.PossessionId, gc.Count):
			overflow = append(overflow, id)
			delete(done, id) // stays in the box
		default:
			granter.GrantFull(user, model.PossessionType(gc.PossessionType), gc.PossessionId, gc.Count, nowMillis)
			received = append(received, id)
			user.Gifts.Received = append(user.Gifts.Received, store.ReceivedGiftState{
				GiftCommon:       gc,
				ReceivedDatetime: nowMillis,
			})
		}
	}
	if len(done) > 0 {
		user.Gifts.NotReceived = slices.DeleteFunc(user.Gifts.NotReceived, func(g store.NotReceivedGiftState) bool {
			return done[g.UserGiftUuid]
		})
	}
	if historyCap > 0 && len(user.Gifts.Received) > historyCap {
		sortReceivedNewestFirst(user.Gifts.Received)
		user.Gifts.Received = slices.Clone(user.Gifts.Received[:historyCap])
	}
	pruneExpiredGifts(user, nowMillis)
	return
}

// pruneExpiredGifts drops gifts past their expiration, which the gift box no
// longer shows, and updates the unclaimed-gift count to match.
func pruneExpiredGifts(user *store.UserState, nowMillis int64) {
	user.Gifts.NotReceived = slices.DeleteFunc(user.Gifts.NotReceived, func(g store.NotReceivedGiftState) bool {
		return giftExpired(g, nowMillis)
	})
	user.Notifications.GiftNotReceiveCount = int32(len(user.Gifts.NotReceived))
}

// liveGiftCount is the number of unclaimed gifts that haven't expired.
func liveGiftCount(gifts []store.NotReceivedGiftState, nowMillis int64) int32 {
	n := int32(0)
	for _, g := range gifts {
		if !giftExpired(g, nowMillis) {
			n++
		}
	}
	return n
}

func giftExpired(gift store.NotReceivedGiftState, nowMillis int64) bool {
	return gift.ExpirationDatetime != 0 && gift.ExpirationDatetime < nowMillis
}

// GetGiftList returns one page of the gift box. Cursors are sort keys of the
// boundary gifts (see giftSortKeys): NextCursor asks for the gifts after a
// key, PreviousCursor for the page before it. The client reloads a page by
// sending PreviousCursor-1 (ascending) or +1 (descending) as NextCursor.
func (s *GiftServiceServer) GetGiftList(ctx context.Context, req *pb.GetGiftListRequest) (*pb.GetGiftListResponse, error) {
	log.Printf("[GiftService] GetGiftList: rewardKinds=%v expirationType=%d ascending=%v nextCursor=%d previousCursor=%d getCount=%d",
		req.RewardKindType, req.ExpirationType, req.IsAscendingSort, req.NextCursor, req.PreviousCursor, req.GetCount)

	cat := s.holder.Get()
	userId := CurrentUserId(ctx, s.users, s.sessions)
	user, err := s.users.LoadUser(userId)
	if err != nil {
		return nil, fmt.Errorf("snapshot user: %w", err)
	}
	return giftListPage(user.Gifts.NotReceived, req, cat.GameConfig.ConsumableItemIdForGold, gametime.NowMillis()), nil
}

func giftListPage(gifts []store.NotReceivedGiftState, req *pb.GetGiftListRequest, goldItemId int32, nowMillis int64) *pb.GetGiftListResponse {
	pageSize := int(req.GetCount)
	if pageSize <= 0 {
		pageSize = giftPageSize
	}

	keys := giftSortKeys(gifts)
	filtered := make([]store.NotReceivedGiftState, 0, len(gifts))
	for _, g := range gifts {
		if giftExpired(g, nowMillis) || !giftMatchesKinds(g, req.RewardKindType, goldItemId) || !giftMatchesExpiration(g, req.ExpirationType) {
			continue
		}
		filtered = append(filtered, g)
	}
	// before reports whether a comes first in the requested order.
	before := func(a, b int64) bool {
		if req.IsAscendingSort {
			return a < b
		}
		return a > b
	}
	sort.Slice(filtered, func(i, j int) bool {
		return before(keys[filtered[i].UserGiftUuid], keys[filtered[j].UserGiftUuid])
	})

	var page []store.NotReceivedGiftState
	switch {
	case req.NextCursor != 0:
		start := sort.Search(len(filtered), func(i int) bool { return before(req.NextCursor, keys[filtered[i].UserGiftUuid]) })
		page = filtered[start:min(start+pageSize, len(filtered))]
	case req.PreviousCursor != 0:
		end := sort.Search(len(filtered), func(i int) bool { return !before(keys[filtered[i].UserGiftUuid], req.PreviousCursor) })
		page = filtered[max(end-pageSize, 0):end]
	default:
		page = filtered[:min(pageSize, len(filtered))]
	}

	resp := &pb.GetGiftListResponse{
		Gift:           make([]*pb.NotReceivedGift, 0, len(page)),
		TotalPageCount: pageCount(len(filtered), pageSize),
	}
	for _, gift := range page {
		resp.Gift = append(resp.Gift, &pb.NotReceivedGift{
			GiftCommon:         toProtoGiftCommon(gift.GiftCommon),
			ExpirationDatetime: timestampOrNilGift(gift.ExpirationDatetime),
			UserGiftUuid:       gift.UserGiftUuid,
		})
	}
	if len(page) > 0 {
		resp.PreviousCursor = keys[page[0].UserGiftUuid]
		resp.NextCursor = keys[page[len(page)-1].UserGiftUuid]
	}
	return resp
}

// giftNoExpiryKey sorts gifts without an expiration after every dated one.
const giftNoExpiryKey int64 = 9_000_000_000_000

// giftSortKeys gives each gift a unique positive key that orders the box by
// expiration: expiration millis * 1000 plus the gift's rank among gifts with
// the same expiration (by grant time, then uuid).
func giftSortKeys(gifts []store.NotReceivedGiftState) map[string]int64 {
	sorted := slices.Clone(gifts)
	base := func(g store.NotReceivedGiftState) int64 {
		if g.ExpirationDatetime == 0 {
			return giftNoExpiryKey
		}
		return g.ExpirationDatetime
	}
	sort.Slice(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if base(a) != base(b) {
			return base(a) < base(b)
		}
		if a.GiftCommon.GrantDatetime != b.GiftCommon.GrantDatetime {
			return a.GiftCommon.GrantDatetime < b.GiftCommon.GrantDatetime
		}
		return a.UserGiftUuid < b.UserGiftUuid
	})
	keys := make(map[string]int64, len(sorted))
	var rank int64
	for i, g := range sorted {
		if i > 0 && base(sorted[i-1]) == base(g) {
			rank++
		} else {
			rank = 1
		}
		keys[g.UserGiftUuid] = base(g)*1000 + min(rank, 999)
	}
	return keys
}

func giftRewardKind(gc store.GiftCommonState, goldItemId int32) int32 {
	switch model.PossessionType(gc.PossessionType) {
	case model.PossessionTypeFreeGem, model.PossessionTypePaidGem:
		return giftKindGem
	case model.PossessionTypeWeapon, model.PossessionTypeWeaponEnhanced:
		return giftKindWeapon
	case model.PossessionTypeCompanion, model.PossessionTypeCompanionEnhanced:
		return giftKindCompanion
	case model.PossessionTypeParts, model.PossessionTypePartsEnhanced:
		return giftKindParts
	case model.PossessionTypeMaterial:
		return giftKindMaterial
	case model.PossessionTypeCostume, model.PossessionTypeCostumeEnhanced:
		return giftKindCostume
	case model.PossessionTypeConsumableItem:
		if gc.PossessionId == goldItemId {
			return giftKindGold
		}
	}
	return giftKindOther
}

func giftMatchesKinds(g store.NotReceivedGiftState, kinds []int32, goldItemId int32) bool {
	if len(kinds) == 0 || slices.Contains(kinds, giftKindNone) {
		return true
	}
	return slices.Contains(kinds, giftRewardKind(g.GiftCommon, goldItemId))
}

func giftMatchesExpiration(g store.NotReceivedGiftState, filter int32) bool {
	switch filter {
	case giftExpirationOnlyExpire:
		return g.ExpirationDatetime != 0
	case giftExpirationOnlyNotExpire:
		return g.ExpirationDatetime == 0
	}
	return true
}

// GetGiftReceiveHistoryList returns received gifts newest first, at most
// MConfig GIFT_RECEIVED_LIST_TOTAL_COUNT of them.
func (s *GiftServiceServer) GetGiftReceiveHistoryList(ctx context.Context, req *emptypb.Empty) (*pb.GetGiftReceiveHistoryListResponse, error) {
	log.Printf("[GiftService] GetGiftReceiveHistoryList")
	historyCap := int(s.holder.Get().GameConfig.GiftReceivedListTotalCount)
	userId := CurrentUserId(ctx, s.users, s.sessions)
	user, err := s.users.LoadUser(userId)
	if err != nil {
		return nil, fmt.Errorf("snapshot user: %w", err)
	}

	history := slices.Clone(user.Gifts.Received)
	sortReceivedNewestFirst(history)
	if historyCap > 0 && len(history) > historyCap {
		history = history[:historyCap]
	}
	items := make([]*pb.ReceivedGift, 0, len(history))
	for _, gift := range history {
		items = append(items, &pb.ReceivedGift{
			GiftCommon:       toProtoGiftCommon(gift.GiftCommon),
			ReceivedDatetime: timestampOrNilGift(gift.ReceivedDatetime),
		})
	}
	return &pb.GetGiftReceiveHistoryListResponse{
		Gift: items,
	}, nil
}

func sortReceivedNewestFirst(gifts []store.ReceivedGiftState) {
	sort.SliceStable(gifts, func(i, j int) bool { return gifts[i].ReceivedDatetime > gifts[j].ReceivedDatetime })
}

func toProtoGiftCommon(gift store.GiftCommonState) *pb.GiftCommon {
	return &pb.GiftCommon{
		PossessionType:        gift.PossessionType,
		PossessionId:          gift.PossessionId,
		Count:                 gift.Count,
		GrantDatetime:         timestampOrNilGift(gift.GrantDatetime),
		DescriptionGiftTextId: gift.DescriptionGiftTextId,
		EquipmentData:         gift.EquipmentData,
	}
}

func timestampOrNilGift(unixMillis int64) *timestamppb.Timestamp {
	if unixMillis == 0 {
		return nil
	}
	return timestamppb.New(time.UnixMilli(unixMillis))
}

func pageCount(total, pageSize int) int32 {
	if total == 0 {
		return 0
	}
	if pageSize <= 0 {
		return 1
	}
	pages := total / pageSize
	if total%pageSize != 0 {
		pages++
	}
	return int32(pages)
}

// overflowMissionRewards lists what a mission reward grant sent to the gift
// box (store.TakeGrantOverflow) for ReceiveMissionRewardsResponse.OverflowPossession.
func overflowMissionRewards(overflow []store.GrantOverflow) []*pb.MissionReward {
	out := make([]*pb.MissionReward, 0, len(overflow))
	for _, o := range overflow {
		out = append(out, &pb.MissionReward{PossessionType: int32(o.PossessionType), PossessionId: o.PossessionId, Count: o.Count})
	}
	return out
}

// overflowMissionPassRewards is overflowMissionRewards for
// ReceiveMissionPassRewardsResponse.OverflowPossession.
func overflowMissionPassRewards(overflow []store.GrantOverflow) []*pb.MissionPassReward {
	out := make([]*pb.MissionPassReward, 0, len(overflow))
	for _, o := range overflow {
		out = append(out, &pb.MissionPassReward{PossessionType: int32(o.PossessionType), PossessionId: o.PossessionId, Count: o.Count})
	}
	return out
}
