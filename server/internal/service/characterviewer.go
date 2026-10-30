package service

import (
	"context"
	"fmt"
	"log"

	pb "lunar-tear/server/gen/proto"
	"lunar-tear/server/internal/gametime"
	"lunar-tear/server/internal/runtime"
	"lunar-tear/server/internal/store"

	"google.golang.org/protobuf/types/known/emptypb"
)

type CharacterViewerServiceServer struct {
	pb.UnimplementedCharacterViewerServiceServer
	users    store.UserRepository
	sessions store.SessionRepository
	holder   *runtime.Holder
}

func NewCharacterViewerServiceServer(users store.UserRepository, sessions store.SessionRepository, holder *runtime.Holder) *CharacterViewerServiceServer {
	return &CharacterViewerServiceServer{users: users, sessions: sessions, holder: holder}
}

func (s *CharacterViewerServiceServer) CharacterViewerTop(ctx context.Context, _ *emptypb.Empty) (*pb.CharacterViewerTopResponse, error) {
	log.Printf("[CharacterViewerService] CharacterViewerTop")

	catalog := s.holder.Get().CharacterViewer
	userId := CurrentUserId(ctx, s.users, s.sessions)
	var released []int32
	if _, err := s.users.UpdateUser(userId, func(user *store.UserState) {
		released = releaseCharacterViewerFields(user, catalog.ReleasedFieldIds(*user), gametime.NowMillis())
	}); err != nil {
		panic(fmt.Sprintf("CharacterViewerTop: no user for userId=%d: %v", userId, err))
	}
	log.Printf("[CharacterViewerService] %d newly released fields for user %d", len(released), userId)

	return &pb.CharacterViewerTopResponse{
		ReleaseCharacterViewerFieldId: released,
	}, nil
}

// releaseCharacterViewerFields records the unlocked fields the player has not
// been told about and returns them. The client shows an unlock notice for
// every id in CharacterViewerTopResponse.ReleaseCharacterViewerFieldId, so
// only new ones are sent.
func releaseCharacterViewerFields(user *store.UserState, unlocked []int32, nowMillis int64) []int32 {
	released := []int32{}
	for _, id := range unlocked {
		if _, seen := user.Ext.CharacterViewerFields[id]; seen {
			continue
		}
		if user.Ext.CharacterViewerFields == nil {
			user.Ext.CharacterViewerFields = make(map[int32]int64)
		}
		user.Ext.CharacterViewerFields[id] = nowMillis
		released = append(released, id)
	}
	return released
}
