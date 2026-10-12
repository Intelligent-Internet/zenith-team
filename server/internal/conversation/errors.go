package conversation

import "errors"

var (
	ErrModelSettingsForbidden = errors.New("only the Task assignee may change its chat model")
	ErrUnauthenticated        = errors.New("user is not authenticated")
	ErrInvalidInput           = errors.New("invalid Conversation input")
	ErrConflict               = errors.New("Conversation mutation conflict")
	ErrInitializationPending  = errors.New("Conversation is preparing the company welcome")
	ErrRunQueueConflict       = errors.New("Conversation Run queue mutation conflict")
	ErrRunQueueFull           = errors.New("Conversation Run queue is full")
	ErrNotFound               = errors.New("Conversation not found")
	ErrHuddleInvalid          = errors.New("invalid huddle input")
	ErrHuddleForbidden        = errors.New("huddle operation forbidden")
	ErrHuddleNotFound         = errors.New("huddle not found")
	ErrHuddleRequestConflict  = errors.New("huddle request key conflict")
	ErrHuddleUnavailable      = errors.New("huddle unavailable")
	ErrHuddleAlreadyConnected = errors.New("huddle already connected")
)
