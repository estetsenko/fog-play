package agent

import (
	"reflect"
	"testing"
)

func TestSlug(t *testing.T) {
	for in, want := range map[string]string{"Dota 2": "dota-2", "  Cyberpunk 2077!! ": "cyberpunk-2077", "Steam Big Picture": "steam-big-picture", "!!!": ""} {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSplitCommand(t *testing.T) {
	for in, want := range map[string][]string{
		`C:\Games\dota2.exe`: {`C:\Games\dota2.exe`},
		`"C:\Program Files\Steam\steam.exe" -applaunch 570`: {`C:\Program Files\Steam\steam.exe`, "-applaunch", "570"},
		`  game   --windowed `:                              {"game", "--windowed"},
	} {
		name, args := splitCommand(in)
		if got := append([]string{name}, args...); !reflect.DeepEqual(got, want) {
			t.Errorf("splitCommand(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLoadGamesAddsDesktop(t *testing.T) {
	a := &Agent{cfg: Config{Apps: []App{{Name: "Dota 2", Cmd: "dota2.exe"}, {Name: "dota 2"}, {Name: "Desktop"}}}}
	games := a.loadGames()
	if len(games) != 2 || games[0].Slug != "desktop" || games[1].Slug != "dota-2" {
		t.Fatalf("games: %+v", games)
	}
	if a.apps["dota-2"].Cmd != "dota2.exe" {
		t.Errorf("apps: %+v", a.apps)
	}
}
