package domain

import (
	"regexp"
	"strings"
)

const SpecialEventsGameID int64 = 509663

var (
	gameSlugApostrophePattern = regexp.MustCompile(`'`)
	gameSlugNonWordPattern    = regexp.MustCompile(`[^\p{L}\p{N}_]+`)
	gameSlugDashPattern       = regexp.MustCompile(`-{2,}`)
)

type Game struct {
	ID       int64
	Name     string
	SlugText string
}

func (g Game) Slug() string {
	if slug := strings.TrimSpace(g.SlugText); slug != "" {
		return slug
	}

	slug := strings.ToLower(strings.TrimSpace(g.Name))
	slug = gameSlugApostrophePattern.ReplaceAllString(slug, "")
	slug = gameSlugNonWordPattern.ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-")
	slug = gameSlugDashPattern.ReplaceAllString(slug, "-")
	return slug
}

func (g Game) IsSpecialEvents() bool {
	return g.ID == SpecialEventsGameID
}
