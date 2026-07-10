package domain

import (
	"regexp"
	"strings"
)

const (
	SpecialEventsGameID int64 = 509663
	IRLGameID           int64 = 509672
)

var specialGameIDs = map[int64]struct{}{
	SpecialEventsGameID: {},
	IRLGameID:           {},
}

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

	return normalizeGameSlug(g.Name)
}

func (g Game) IsSpecialEvents() bool {
	return g.ID == SpecialEventsGameID ||
		normalizeGameSlug(g.SlugText) == "special-events" ||
		normalizeGameSlug(g.Name) == "special-events"
}

func normalizeGameSlug(value string) string {
	slug := strings.ToLower(strings.TrimSpace(value))
	slug = gameSlugApostrophePattern.ReplaceAllString(slug, "")
	slug = gameSlugNonWordPattern.ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-")
	slug = gameSlugDashPattern.ReplaceAllString(slug, "-")
	return slug
}

func (g Game) IsSpecial() bool {
	if _, ok := specialGameIDs[g.ID]; ok {
		return true
	}
	return g.IsSpecialEvents()
}
