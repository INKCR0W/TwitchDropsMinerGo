package domain

import "testing"

func TestGameSlugUsesExplicitValueOrGeneratedFallback(t *testing.T) {
	t.Parallel()

	withSlug := Game{ID: 1, Name: "Counter-Strike 2", SlugText: "counter-strike-2"}
	if slug := withSlug.Slug(); slug != "counter-strike-2" {
		t.Fatalf("显式 slug 不匹配: %q", slug)
	}

	withoutSlug := Game{ID: 2, Name: "Tom Clancy's Rainbow   Six: Siege!"}
	if slug := withoutSlug.Slug(); slug != "tom-clancys-rainbow-six-siege" {
		t.Fatalf("生成 slug 不匹配: %q", slug)
	}
}

func TestGameIsSpecialCoversEverySpecialCategory(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		game Game
		want bool
	}{
		{name: "special events", game: Game{ID: SpecialEventsGameID, Name: "Special Events"}, want: true},
		{name: "irl", game: Game{ID: IRLGameID, Name: "IRL"}, want: true},
		{name: "regular game", game: Game{ID: 460630, Name: "Tom Clancy's Rainbow Six Siege"}, want: false},
		{name: "impostor by name only", game: Game{Name: "Special Events"}, want: false},
	}
	for _, testCase := range cases {
		if got := testCase.game.IsSpecial(); got != testCase.want {
			t.Errorf("%s: IsSpecial() = %v, want %v", testCase.name, got, testCase.want)
		}
	}
}
