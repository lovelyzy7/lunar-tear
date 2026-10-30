package service

import (
	"context"
	"log"

	pb "lunar-tear/server/gen/proto"
	"lunar-tear/server/internal/store"

	emptypb "google.golang.org/protobuf/types/known/emptypb"
)

type FriendServiceServer struct {
	pb.UnimplementedFriendServiceServer
	users    store.UserRepository
	sessions store.SessionRepository
}

func NewFriendServiceServer(users store.UserRepository, sessions store.SessionRepository) *FriendServiceServer {
	return &FriendServiceServer{users: users, sessions: sessions}
}

// GetUser, GetFriendList and SearchRecommendedUsers are in bunker_friends.go.

func (s *FriendServiceServer) GetFriendRequestList(ctx context.Context, req *emptypb.Empty) (*pb.GetFriendRequestListResponse, error) {
	log.Printf("[FriendService] GetFriendRequestList")
	return &pb.GetFriendRequestListResponse{
		User: []*pb.User{},
	}, nil
}
