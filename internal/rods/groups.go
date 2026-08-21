package rods

import (
	"context"

	"github.com/cyverse-de/data-info/internal/irodsclient"
)

// CreateGroup makes a new group.
func (s *Scope) CreateGroup(ctx context.Context, name string) error {
	sess, err := s.session(ctx)
	if err != nil {
		return err
	}
	return irodsclient.CreateGroup(ctx, sess, name, s.deps.Zone)
}

// DeleteGroup removes a group.
func (s *Scope) DeleteGroup(ctx context.Context, name string) error {
	sess, err := s.session(ctx)
	if err != nil {
		return err
	}
	return irodsclient.DeleteGroup(ctx, sess, name, s.deps.Zone)
}

// AddGroupMember puts a user of the local zone in a group.
func (s *Scope) AddGroupMember(ctx context.Context, group, user string) error {
	return s.AddGroupMemberIn(ctx, group, user, s.deps.Zone)
}

// AddGroupMemberIn puts a user of a named zone in a group.
func (s *Scope) AddGroupMemberIn(ctx context.Context, group, user, zone string) error {
	sess, err := s.session(ctx)
	if err != nil {
		return err
	}
	return irodsclient.AddGroupMember(ctx, sess, group, user, zone)
}

// RemoveGroupMember takes a user of the local zone out of a group.
func (s *Scope) RemoveGroupMember(ctx context.Context, group, user string) error {
	return s.RemoveGroupMemberIn(ctx, group, user, s.deps.Zone)
}

// RemoveGroupMemberIn takes a user of a named zone out of a group.
func (s *Scope) RemoveGroupMemberIn(ctx context.Context, group, user, zone string) error {
	sess, err := s.session(ctx)
	if err != nil {
		return err
	}
	return irodsclient.RemoveGroupMember(ctx, sess, group, user, zone)
}

// GroupMembers returns the names of a group's members.
func (s *Scope) GroupMembers(ctx context.Context, group string) ([]string, error) {
	sess, err := s.session(ctx)
	if err != nil {
		return nil, err
	}
	return irodsclient.ListGroupMembers(ctx, sess, group, s.deps.Zone)
}

// GroupExists reports whether a group of that name exists.
func (s *Scope) GroupExists(ctx context.Context, group string) (bool, error) {
	sess, err := s.session(ctx)
	if err != nil {
		return false, err
	}
	return irodsclient.GroupExists(ctx, sess, group, s.deps.Zone)
}
