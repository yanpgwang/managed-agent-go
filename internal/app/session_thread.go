package app

import "time"

const DefaultSessionThreadListLimit = 1000

type SessionThreadPageBoundary struct {
	CreatedAt time.Time
	ID        string
}

type SessionThreadListQuery struct {
	Limit    int
	Boundary *SessionThreadPageBoundary
}
